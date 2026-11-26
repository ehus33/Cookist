package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func policyWorker(t *testing.T) *deploymentWorker {
	t.Helper()
	w, _ := testWorker(t)
	core := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		_ = json.NewEncoder(rw).Encode(map[string]string{"actorId": token})
	}))
	t.Cleanup(core.Close)
	w.coreURL = core.URL
	w.accessFile = filepath.Join(t.TempDir(), "access.json")
	setPolicy(t, w, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	return w
}

func setPolicy(t *testing.T, w *deploymentWorker, expires string) {
	t.Helper()
	policy := accessPolicy{Version: 1,
		Members: map[string]accessMembership{
			"admin": {"alpha", "customer_admin"}, "deployer": {"alpha", "deployer"},
			"viewer": {"alpha", "viewer"}, "outsider": {"beta", "deployer"},
		},
		Tenants: map[string]accessTenant{
			"alpha": {"active", expires, 1}, "beta": {"active", expires, 1},
		},
	}
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.accessFile, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAccessRolesAndSubscriptionConditions(t *testing.T) {
	w := policyWorker(t)
	for _, test := range []struct {
		actor                string
		list, create, delete bool
	}{
		{"owner", true, true, true},
		{"admin", true, true, true},
		{"deployer", true, true, true},
		{"viewer", true, false, false},
		{"unregistered", false, false, false},
	} {
		t.Run(test.actor, func(t *testing.T) {
			access, err := w.accessFor(test.actor)
			if err != nil {
				t.Fatal(err)
			}

			for action, want := range map[string]bool{"list": test.list, "create": test.create, "delete": test.delete} {
				if got := access.check(action, nil) == nil; got != want {
					t.Errorf("%s allowed=%v, want %v", action, got, want)
				}
			}
		})
	}
	setPolicy(t, w, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
	for _, actor := range []string{"admin", "deployer"} {
		access, err := w.accessFor(actor)
		if err != nil {
			t.Fatal(err)
		}
		if access.CanCreate || !access.CanList || !access.CanDelete {
			t.Fatalf("expiry must block creation, not recovery: %+v", access)
		}
	}
	owner, err := w.accessFor("owner")
	if err != nil || !owner.CanCreate {
		t.Fatal("owner lost subscription bypass", err)
	}
}

func TestSelfServiceBillingIsolatedTenantsAndPaidAccess(t *testing.T) {
	w := policyWorker(t)
	w.billing = &stripeBilling{root: t.TempDir(), price: "price_self_service"}
	firstActor := "cognito:" + strings.Repeat("a", 64) + ":first"
	secondActor := "cognito:" + strings.Repeat("a", 64) + ":second"
	before, err := w.accessFor(firstActor)
	if err != nil || before.CanList || before.CanCheckout {
		t.Fatal("self-service must be opt-in", err)
	}
	w.selfServiceBilling = true
	first, err := w.accessFor(firstActor)
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.accessFor(secondActor)
	if err != nil {
		t.Fatal(err)
	}
	if first.TenantID == second.TenantID || !tenantPattern.MatchString(first.TenantID) ||
		first.Role != "customer_admin" || !first.CanCheckout || first.CanCreate || first.Subscribed ||
		first.Limit != 1 || !first.CanList || !first.CanDelete {
		t.Fatalf("new customer permissions are unsafe: %+v %+v", first, second)
	}
	if rec := requestDeployment(w, "POST", "Bearer "+firstActor,
		`{"name":"Unpaid","region":"us-west-2","template":"blank"}`); rec.Code != 403 {
		t.Fatalf("new unpaid customer could deploy: %d", rec.Code)
	}
	if err := w.billing.save(first.TenantID, tenantBilling{
		CustomerID: "cus_first", PriceID: w.billing.price, HasSubscription: true,
		ValidUntil: time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	restored := &deploymentWorker{owner: w.owner, accessFile: w.accessFile, selfServiceBilling: true,
		billing: &stripeBilling{root: w.billing.root, price: w.billing.price}}
	paid, err := restored.accessFor(firstActor)
	if err != nil || !paid.CanCreate || !paid.Subscribed || paid.TenantID != first.TenantID {
		t.Fatal("paid personal tenant did not survive a fresh worker", err)
	}
	unpaid, err := restored.accessFor(secondActor)
	if err != nil || unpaid.CanCreate || unpaid.Subscribed {
		t.Fatal("another identity inherited paid access", err)
	}
	if unpaid.check("delete", &deployment{TenantID: first.TenantID, CreatedBy: firstActor}) == nil {
		t.Fatal("personal tenant could clean up another customer's resources")
	}
}

func TestSelfServiceBillingPreservesAssignedRolesAndExemptions(t *testing.T) {
	w := policyWorker(t)
	w.billing = &stripeBilling{root: t.TempDir(), price: "price_self_service"}
	w.selfServiceBilling = true
	viewer, err := w.accessFor("viewer")
	if err != nil || viewer.Role != "viewer" || viewer.TenantID != "alpha" ||
		viewer.CanCheckout || viewer.CanCreate || viewer.CanDelete {
		t.Fatal("assigned viewer was elevated by onboarding", err)
	}
	owner, err := w.accessFor("owner")
	if err != nil || owner.Role != "platform_owner" || !owner.CanCreate || owner.CanCheckout {
		t.Fatal("owner exemption changed", err)
	}
	actor := "cognito:" + strings.Repeat("a", 64) + ":new"
	w.localAccess = true
	local, err := w.accessFor(actor)
	if err != nil || local.CanCheckout || local.CanList {
		t.Fatal("hosted self-service unexpectedly enrolled a local user", err)
	}
	w.localAccess, w.billing = false, nil
	disabled, err := w.accessFor(actor)
	if err != nil || disabled.CanCheckout || disabled.CanList {
		t.Fatal("onboarding was enabled without Stripe billing", err)
	}
}

func TestSelfServiceBillingFailsClosedOnTenantConflict(t *testing.T) {
	w := policyWorker(t)
	w.billing = &stripeBilling{root: t.TempDir(), price: "price_self_service"}
	w.selfServiceBilling = true
	actor := "cognito:" + strings.Repeat("a", 64) + ":new"
	access, err := w.accessFor(actor)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := loadAccessPolicy(w.accessFile)
	if err != nil {
		t.Fatal(err)
	}
	policy.Tenants[access.TenantID] = accessTenant{SubscriptionStatus: "inactive", DeploymentLimit: 1}
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.accessFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.accessFor(actor); err == nil {
		t.Fatal("new identity joined an administrator-managed tenant through a hash conflict")
	}
	if _, err := w.accessFor("unverified"); err == nil {
		t.Fatal("onboarding accepted a non-Cognito identity")
	}
}

func TestTenantIsolationAndLegacyOwnership(t *testing.T) {
	w := policyWorker(t)
	d := cleanupFixture(t, w, "ready")
	// Unlabelled older deployments must remain private to the owner.
	for _, actor := range []string{"admin", "outsider", "viewer"} {
		rec := requestDeployment(w, "GET", "Bearer "+actor, "")
		if rec.Code != 200 || rec.Body.String() != "[]\n" {
			t.Fatalf("legacy deployment exposed to %s: %d %s", actor, rec.Code, rec.Body)
		}
	}
	d.TenantID, d.CreatedBy = "alpha", "admin"
	if err := saveDeployment(filepath.Join(w.root, d.ID), d); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"owner", "admin", "viewer"} {
		rec := requestDeployment(w, "GET", "Bearer "+actor, "")
		var listed []deployment
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &listed) != nil || len(listed) != 1 {
			t.Fatalf("authorized tenant list failed for %s: %d %s", actor, rec.Code, rec.Body)
		}
		if listed[0].CanDelete != (actor != "viewer") {
			t.Fatalf("incorrect delete permission for %s", actor)
		}
	}
	w.execute = func(context.Context, string, io.Writer, ...string) ([]byte, error) {
		t.Error("unauthorized user invoked Terraform")
		return nil, nil
	}
	for actor, want := range map[string]int{"outsider": 404, "viewer": 403, "unregistered": 403} {
		rec := deleteDeploymentRequest(w, d.ID, "Bearer "+actor)
		if rec.Code != want {
			t.Fatalf("%s cleanup returned %d, want %d: %s", actor, rec.Code, want, rec.Body)
		}
	}
}

func TestTenantQuotaAndServerAssignedOwnership(t *testing.T) {
	w := policyWorker(t)
	w.execute = func(_ context.Context, dir string, _ io.Writer, args ...string) ([]byte, error) {
		if args[0] == "apply" {
			return nil, os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte(`{"version":4,"resources":[]}`), 0600)
		}
		if args[0] == "output" {
			return []byte("https://example.cloudfront.net"), nil
		}
		return nil, nil
	}
	body := `{"name":"Site","region":"us-west-2","template":"blank"}`
	for _, extra := range []string{`,"tenantId":"beta"`, `,"role":"platform_owner"`, `,"subscribed":true`} {
		spoofed := strings.TrimSuffix(body, "}") + extra + "}"
		if rec := requestDeployment(w, "POST", "Bearer deployer", spoofed); rec.Code != 400 {
			t.Fatalf("client attributes accepted: %d %s", rec.Code, rec.Body)
		}
	}
	if rec := requestDeployment(w, "POST", "Bearer deployer", body); rec.Code != 202 {
		t.Fatalf("tenant create failed: %d %s", rec.Code, rec.Body)
	}
	d := waitDeployment(t, w, "ready")
	if d.TenantID != "alpha" || d.CreatedBy != "deployer" {
		t.Fatalf("incorrect server-assigned ownership: %+v", d)
	}
	if rec := requestDeployment(w, "POST", "Bearer admin", body); rec.Code != 429 ||
		!strings.Contains(rec.Body.String(), "tenant_deployment_limit") {
		t.Fatalf("tenant quota not enforced: %d %s", rec.Code, rec.Body)
	}
	// Expiration is re-read without restarting; cleanup remains authorized.
	setPolicy(t, w, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
	if rec := requestDeployment(w, "POST", "Bearer deployer", body); rec.Code != 403 {
		t.Fatalf("expired tenant could deploy: %d %s", rec.Code, rec.Body)
	}
	if rec := deleteDeploymentRequest(w, d.ID, "Bearer admin"); rec.Code != 202 {
		t.Fatalf("expired tenant could not clean up: %d %s", rec.Code, rec.Body)
	}
	waitCleanup(t, w, d.ID)
}

func TestInvalidAccessPolicyFailsClosed(t *testing.T) {
	w := policyWorker(t)
	for _, data := range []string{
		`{}`,
		`{"version":1,"members":{},"tenants":{},"unexpected":true}`,
		`{"version":1,"members":{"x":{"tenantId":"alpha","role":"platform_owner"}},"tenants":{"alpha":{"subscriptionStatus":"active","validUntil":"2099-01-01T00:00:00Z","deploymentLimit":1}}}`,
		`{"version":1,"members":{},"tenants":{"alpha":{"subscriptionStatus":"active","deploymentLimit":1}}}`,
		`{"version":1,"members":{"x":{"tenantId":"unknown","role":"viewer"}},"tenants":{}}`,
		`{"version":1,"members":{},"tenants":{"owner":{"subscriptionStatus":"inactive","deploymentLimit":1}}}`,
	} {
		if err := os.WriteFile(w.accessFile, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if rec := requestDeployment(w, "GET", "Bearer owner", ""); rec.Code != 503 {
			t.Fatalf("invalid policy did not fail closed: %d %s", rec.Code, rec.Body)
		}
	}
	if err := os.Remove(w.accessFile); err != nil {
		t.Fatal(err)
	}
	if rec := requestDeployment(w, "GET", "Bearer deployer", ""); rec.Code != 503 {
		t.Fatalf("missing configured policy did not fail closed: %d", rec.Code)
	}
	w.accessFile = ""
	if rec := requestDeployment(w, "GET", "Bearer deployer", ""); rec.Code != 403 {
		t.Fatalf("empty policy allowed a non-owner: %d", rec.Code)
	}
}

func TestDeploymentAccessEndpoint(t *testing.T) {
	w := policyWorker(t)
	handler, err := newGateway(w.coreURL, t.TempDir(), nil, w)
	if err != nil {
		t.Fatal(err)
	}
	for actor, wantCreate := range map[string]bool{"owner": true, "viewer": false, "unregistered": false} {
		req := httptest.NewRequest("GET", "/api/deployment-access", nil)
		req.Header.Set("Authorization", "Bearer "+actor)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		var access deploymentAccess
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &access) != nil || access.CanCreate != wantCreate {
			t.Fatalf("access endpoint failed: %d %s", rec.Code, rec.Body)
		}
	}
}

func TestCustomerQuotaFailsClosedOnInvalidOwnership(t *testing.T) {
	w := policyWorker(t)
	id := fmt.Sprintf("%032x", 1)
	dir := filepath.Join(w.root, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveDeployment(dir, deployment{ID: id, Status: "ready", TenantID: "alpha"}); err != nil {
		t.Fatal(err)
	}
	w.execute = func(context.Context, string, io.Writer, ...string) ([]byte, error) {
		t.Error("invalid ownership invoked Terraform")
		return nil, errors.New("unexpected execution")
	}
	if rec := requestDeployment(w, "POST", "Bearer admin", `{"name":"Site","region":"us-west-2","template":"blank"}`); rec.Code != 500 {
		t.Fatalf("invalid ownership bypassed quota: %d %s", rec.Code, rec.Body)
	}
}

func TestLocalSubscriptionBypassRequiresExplicitLoopbackBinding(t *testing.T) {
	for _, test := range []struct {
		mode, host     string
		local, invalid bool
	}{
		{"hosted", "0.0.0.0", false, false},
		{"hosted", "127.0.0.1", false, false},
		{"local", "127.0.0.1", true, false},
		{"local", "::1", true, false},
		{"local", "0.0.0.0", false, true},
		{"local", "::", false, true},
		{"local", "100.93.226.69", false, true},
		{"local", "localhost", false, true},
		{"local", "", false, true},
		{"unknown", "127.0.0.1", false, true},
	} {
		local, err := localSubscriptionBypass(test.mode, test.host)
		if local != test.local || (err != nil) != test.invalid {
			t.Errorf("%s/%s returned local=%v err=%v", test.mode, test.host, local, err)
		}
	}
}

func TestLocalBypassPreservesRolesOwnershipAndQuota(t *testing.T) {
	w := policyWorker(t)
	setPolicy(t, w, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
	w.localAccess = true
	for _, actor := range []string{"admin", "deployer"} {
		access, err := w.accessFor(actor)
		if err != nil || !access.CanCreate || access.PaymentRequired || access.Subscribed {
			t.Fatalf("local entitlement bypass failed: %+v %v", access, err)
		}
		if err := access.check("delete", &deployment{TenantID: "beta"}); err == nil {
			t.Fatal("local bypass defeated tenant isolation")
		}
	}
	for _, actor := range []string{"viewer", "unregistered"} {
		access, err := w.accessFor(actor)
		if err != nil || access.CanCreate {
			t.Fatalf("local bypass granted an unauthorized role: %+v %v", access, err)
		}
	}
	d := cleanupFixture(t, w, "failed")
	d.TenantID, d.CreatedBy = "alpha", "deployer"
	if err := saveDeployment(filepath.Join(w.root, d.ID), d); err != nil {
		t.Fatal(err)
	}
	if rec := requestDeployment(w, "POST", "Bearer admin", `{"name":"New","region":"us-west-2","template":"blank"}`); rec.Code != 429 {
		t.Fatalf("local bypass defeated tenant quota: %d %s", rec.Code, rec.Body)
	}
}

func TestHostedPaywallCannotBeBypassedWithLocalHeaders(t *testing.T) {
	w := policyWorker(t)
	setPolicy(t, w, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
	w.execute = func(context.Context, string, io.Writer, ...string) ([]byte, error) {
		t.Error("hosted unpaid request invoked Terraform")
		return nil, nil
	}
	req := httptest.NewRequest("POST", "http://localhost/api/deployments",
		strings.NewReader(`{"name":"Site","region":"us-west-2","template":"blank"}`))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer admin")
	req.Header.Set("X-Forwarded-Host", "localhost")
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	rec := httptest.NewRecorder()
	w.ServeHTTP(rec, req)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "subscription_required") {
		t.Fatalf("local headers bypassed hosted paywall: %d %s", rec.Code, rec.Body)
	}
}

func TestHostedPaywallConcealsURLsButAllowsRecoveryListing(t *testing.T) {
	w := policyWorker(t)
	d := cleanupFixture(t, w, "ready")
	d.TenantID, d.CreatedBy, d.URL = "alpha", "admin", "https://example.cloudfront.net"
	if err := saveDeployment(filepath.Join(w.root, d.ID), d); err != nil {
		t.Fatal(err)
	}
	setPolicy(t, w, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
	for actor, wantURL := range map[string]bool{"admin": false, "owner": true} {
		rec := requestDeployment(w, "GET", "Bearer "+actor, "")
		var listed []deployment
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &listed) != nil || len(listed) != 1 {
			t.Fatalf("recovery listing failed: %d %s", rec.Code, rec.Body)
		}
		if (listed[0].URL != "") != wantURL || !listed[0].CanDelete {
			t.Fatalf("incorrect paywall/recovery permission for %s: %+v", actor, listed[0])
		}
	}
	w.localAccess = true
	rec := requestDeployment(w, "GET", "Bearer admin", "")
	if !strings.Contains(rec.Body.String(), d.URL) {
		t.Fatalf("local subscription bypass hid authorized site: %s", rec.Body)
	}
}
