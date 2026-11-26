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
