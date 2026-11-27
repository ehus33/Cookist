package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func databaseBillingWorker(t *testing.T) (*deploymentWorker, *stripeFixture) {
	t.Helper()
	w, fixture := billingWorker(t)
	w.dynamic = &dynamicService{}
	if err := w.billing.configureDatabasePrice("price_database"); err != nil {
		t.Fatal(err)
	}
	dynamicWantStatus(t, billingRequest(w, "checkout", "admin", ""), 200)
	return w, fixture
}

func databaseStatusRequest(w *deploymentWorker, actor string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/billing/database", nil)
	r.Header.Set("Authorization", "Bearer "+actor)
	rw := httptest.NewRecorder()
	w.billingRoute(rw, r)
	return rw
}

func setDatabaseSubscriptions(t *testing.T, w *deploymentWorker, fixture *stripeFixture, subscriptions ...any) {
	t.Helper()
	fixture.mu.Lock()
	fixture.subscriptions = subscriptions
	fixture.mu.Unlock()
	dynamicWantStatus(t, signedBillingEvent(t, http.HandlerFunc(w.billingRoute), "invoice.paid", "cus_alpha", false), 204)
}

func TestDatabaseCheckoutSeparatesPlansAndWaitsForVerifiedPayment(t *testing.T) {
	w, fixture := databaseBillingWorker(t)
	baseKey := fixture.creationKey
	base := paidSubscription("active", "price_test", true, time.Now().Add(time.Hour).Unix())
	setDatabaseSubscriptions(t, w, fixture, base)
	for i := 0; i < 2; i++ {
		response := billingRequest(w, "database/checkout", "admin", "")
		dynamicWantStatus(t, response, 200)
		if !strings.Contains(response.Body.String(), "/pay/database") {
			t.Fatal("database checkout returned the base plan link")
		}
	}
	if fixture.checkouts != 2 || fixture.creationKey == baseKey {
		t.Fatalf("checkout was duplicated or shared the base idempotency key: %d", fixture.checkouts)
	}
	record, err := w.billing.read("alpha")
	if err != nil || record.CheckoutID != "cs_test" || record.DatabaseCheckoutID != "cs_database" ||
		record.AttemptID == record.DatabaseAttemptID || record.DatabaseValidUntil != 0 {
		t.Fatalf("database checkout corrupted base state or granted access: %+v %v", record, err)
	}
	access, err := w.accessFor("admin")
	if err != nil || !access.CanCreate || access.CanUseDynamic {
		t.Fatalf("base plan bypassed database payment: %+v %v", access, err)
	}
	dynamicWantStatus(t, signedBillingEvent(t, http.HandlerFunc(w.billingRoute), "invoice.paid", "cus_alpha", true), 400)
	setDatabaseSubscriptions(t, w, fixture, base, paidSubscription("active", "price_database", true, time.Now().Add(time.Hour).Unix()))
	access, err = w.accessFor("admin")
	if err != nil || !access.Subscribed || !access.DatabaseSubscribed || !access.CanUseDynamic {
		t.Fatalf("verified payment did not grant separate access: %+v %v", access, err)
	}
	dynamicWantStatus(t, billingRequest(w, "database/checkout", "admin", ""), 409)
	statusResponse := databaseStatusRequest(w, "admin")
	dynamicWantStatus(t, statusResponse, 200)
	var status databaseBillingStatus
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil ||
		status.Price == nil || status.Price.AmountCents != 3800 || status.Mode != "test" ||
		status.CanCheckout || !status.Subscribed || !status.HasSubscription {
		t.Fatalf("unexpected database status: %+v %v", status, err)
	}
	setDatabaseSubscriptions(t, w, fixture, base, paidSubscription("canceled", "price_database", true, time.Now().Add(time.Hour).Unix()))
	access, err = w.accessFor("admin")
	if err != nil || !access.CanCreate || access.DatabaseSubscribed || access.CanUseDynamic {
		t.Fatalf("database cancellation revoked the base plan or retained database access: %+v %v", access, err)
	}
	// A delayed invoice notification must reconcile current state, not re-grant an old payment.
	setDatabaseSubscriptions(t, w, fixture, base)
	access, _ = w.accessFor("admin")
	if access.DatabaseSubscribed || !access.CanCreate {
		t.Fatal("stale webhook re-granted database access")
	}
}

func TestDatabaseSubscriptionDoesNotGrantMainPlan(t *testing.T) {
	w, fixture := databaseBillingWorker(t)
	setDatabaseSubscriptions(t, w, fixture, paidSubscription("active", "price_database", true, time.Now().Add(time.Hour).Unix()))
	access, err := w.accessFor("admin")
	if err != nil || access.Subscribed || access.CanCreate || access.CanUseDynamic || !access.DatabaseSubscribed {
		t.Fatalf("database subscription bypassed the main plan: %+v %v", access, err)
	}
	dynamicWantStatus(t, billingRequest(w, "database/checkout", "admin", ""), 403)
	if fixture.checkouts != 1 {
		t.Fatal("purchasing a database without the main plan created another checkout")
	}
	// Customers can cancel the independent add-on even after their main plan expires.
	dynamicWantStatus(t, billingRequest(w, "portal", "admin", ""), 200)
}

func TestDatabaseUnpaidWrongPriceAndQuantityFailClosed(t *testing.T) {
	for _, test := range []struct {
		name, status, price string
		paid                bool
		remaining           time.Duration
		quantity            int
	}{
		{"wrong price", "active", "price_other", true, time.Hour, 1},
		{"unpaid", "active", "price_database", false, time.Hour, 1},
		{"past due", "past_due", "price_database", true, time.Hour, 1},
		{"trial", "trialing", "price_database", true, time.Hour, 1},
		{"expired", "active", "price_database", true, -time.Hour, 1},
		{"quantity", "active", "price_database", true, time.Hour, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			w, fixture := databaseBillingWorker(t)
			subscription := paidSubscription(test.status, test.price, test.paid, time.Now().Add(test.remaining).Unix()).(map[string]any)
			subscription["items"].(map[string]any)["data"].([]any)[0].(map[string]any)["quantity"] = test.quantity
			setDatabaseSubscriptions(t, w, fixture,
				paidSubscription("active", "price_test", true, time.Now().Add(time.Hour).Unix()), subscription)
			access, err := w.accessFor("admin")
			if err != nil || !access.CanCreate || access.DatabaseSubscribed || access.CanUseDynamic {
				t.Fatalf("invalid database subscription granted access: %+v %v", access, err)
			}
		})
	}
}

func TestDatabaseCheckoutRolesAvailabilityAndTrustedRequest(t *testing.T) {
	w, fixture := databaseBillingWorker(t)
	setDatabaseSubscriptions(t, w, fixture, paidSubscription("active", "price_test", true, time.Now().Add(time.Hour).Unix()))
	for _, actor := range []string{"viewer", "deployer", "owner", "intruder"} {
		dynamicWantStatus(t, billingRequest(w, "database/checkout", actor, ""), 403)
	}
	dynamicWantStatus(t, billingRequest(w, "database/checkout", "admin", `{"price":"price_other","tenant":"beta","customer":"cus_other"}`), 400)
	w.dynamic = nil
	dynamicWantStatus(t, billingRequest(w, "database/checkout", "admin", ""), 503)
	w.dynamic = &dynamicService{}
	w.billing.databasePrice = ""
	dynamicWantStatus(t, billingRequest(w, "database/checkout", "admin", ""), 503)
	if fixture.checkouts != 1 {
		t.Fatal("a rejected database purchase reached Stripe checkout")
	}
}

func TestDatabasePricingErrorsNeverFallBackOrCreateCheckout(t *testing.T) {
	for name, response := range map[string]string{
		"free":                    `{"id":"price_database","active":true,"currency":"usd","unit_amount":0,"billing_scheme":"per_unit","recurring":{"usage_type":"licensed","interval":"month","interval_count":1}}`,
		"metered":                 `{"id":"price_database","active":true,"currency":"usd","unit_amount":3800,"billing_scheme":"per_unit","recurring":{"usage_type":"metered","interval":"month","interval_count":1}}`,
		"annual":                  `{"id":"price_database","active":true,"currency":"usd","unit_amount":3800,"billing_scheme":"per_unit","recurring":{"usage_type":"licensed","interval":"year","interval_count":1}}`,
		"wrong mode":              `{"id":"price_database","active":true,"livemode":true,"currency":"usd","unit_amount":3800,"billing_scheme":"per_unit","recurring":{"usage_type":"licensed","interval":"month","interval_count":1}}`,
		"wrong response price":    `{"id":"price_other","active":true,"currency":"usd","unit_amount":3800,"billing_scheme":"per_unit","recurring":{"usage_type":"licensed","interval":"month","interval_count":1}}`,
		"quantity transformation": `{"id":"price_database","active":true,"currency":"usd","unit_amount":3800,"billing_scheme":"per_unit","transform_quantity":{"divide_by":100,"round":"down"},"recurring":{"usage_type":"licensed","interval":"month","interval_count":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			w, fixture := databaseBillingWorker(t)
			setDatabaseSubscriptions(t, w, fixture, paidSubscription("active", "price_test", true, time.Now().Add(time.Hour).Unix()))
			fixture.databasePriceResponse = response
			dynamicWantStatus(t, databaseStatusRequest(w, "admin"), 503)
			dynamicWantStatus(t, billingRequest(w, "database/checkout", "admin", ""), 503)
			if fixture.checkouts != 1 {
				t.Fatal("invalid price reached Stripe checkout")
			}
		})
	}
}

func TestDatabasePauseRetainsOwnerReadDeleteAndVisitorLogout(t *testing.T) {
	w, _ := databaseBillingWorker(t)
	f := newDynamicFixture(t)
	f.worker.localAccess = false
	f.worker.billing = w.billing
	f.access.TenantID = "alpha"
	f.saveApp(t, f.app, "ready", "alpha", f.design)
	path := "/apps/" + f.app
	dynamicWantStatus(t, f.request(t, "GET", path+"/config", "", "", nil, ""), 403)
	dynamicWantStatus(t, f.request(t, "POST", path+"/auth/signup", "", `{"email":"member@example.com","password":"valid-password"}`, nil, ""), 403)
	adminPath := "/api/deployments/" + f.app + "/collections/" + f.design.Dynamic.Collections[0].ID + "/records"
	dynamicWantStatus(t, f.request(t, "POST", adminPath, "", `{"values":{"title":"No unpaid writes"}}`, &f.access, ""), 403)
	dynamicWantStatus(t, f.request(t, "GET", adminPath, "", "", &f.access, ""), 200)
	// Seed through the operator's billing-exempt recovery account, not an unpaid customer.
	operator := deploymentAccess{Role: "platform_owner", ActorID: "owner", CanDelete: true}
	created := f.request(t, "POST", adminPath, "", `{"values":{"title":"Recoverable"}}`, &operator, "")
	dynamicWantStatus(t, created, 201)
	record := dynamicRecordResponse(t, created)
	dynamicWantStatus(t, f.request(t, "PATCH", adminPath+"/"+record.ID, "", `{"values":{"title":"Blocked"},"version":1}`, &f.access, ""), 403)
	dynamicWantStatus(t, f.request(t, "DELETE", adminPath+"/"+record.ID, "", "", &f.access, "1"), 200)
	dynamicWantStatus(t, f.request(t, "POST", path+"/auth/logout", f.loginToken, "", nil, ""), 200)
	other := f.access
	other.TenantID = "beta"
	dynamicWantStatus(t, f.request(t, "GET", adminPath, "", "", &other, ""), 404)
	// Paid database access for an existing app is independent of main-plan expiry.
	bill, err := w.billing.read("alpha")
	if err != nil {
		t.Fatal(err)
	}
	bill.DatabasePriceID, bill.DatabaseValidUntil = "price_database", time.Now().Add(time.Hour).Unix()
	if err := w.billing.save("alpha", bill); err != nil {
		t.Fatal(err)
	}
	dynamicWantStatus(t, f.request(t, "GET", path+"/config", "", "", nil, ""), 200)
	bill.DatabaseValidUntil = time.Now().Add(-time.Hour).Unix()
	if err := w.billing.save("alpha", bill); err != nil {
		t.Fatal(err)
	}
	dynamicWantStatus(t, f.request(t, "GET", path+"/config", "", "", nil, ""), 403)
}

func TestDatabaseAddonIsRequiredForCreateAndRepublish(t *testing.T) {
	w, fixture := databaseBillingWorker(t)
	setDatabaseSubscriptions(t, w, fixture, paidSubscription("active", "price_test", true, time.Now().Add(time.Hour).Unix()))
	w.publicOrigin = w.billing.origin
	f := newDynamicFixture(t)
	req := f.design
	req.Name = "dynamic-test"
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	access, err := w.accessFor("admin")
	if err != nil || !access.CanCreate {
		t.Fatalf("base plan fixture lacks ordinary deployment access: %+v %v", access, err)
	}
	request := httptest.NewRequest("POST", "/api/deployments", strings.NewReader(string(body)))
	response := httptest.NewRecorder()
	w.create(response, request, access)
	dynamicWantStatus(t, response, 403)
	if !strings.Contains(response.Body.String(), "database_subscription_required") {
		t.Fatal("creation failed for a reason other than missing database payment")
	}
	f.worker.root = w.root
	f.saveApp(t, f.app, "ready", "alpha", req)
	if err := saveDeployment(filepath.Join(w.root, f.app), deployment{
		ID: f.app, Name: req.Name, Region: req.Region, Status: "ready",
		URL: "https://example.cloudfront.net", TenantID: "alpha", CreatedBy: w.owner,
	}); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest("PATCH", "/api/deployments/"+f.app+"/design", strings.NewReader(string(body)))
	response = httptest.NewRecorder()
	w.builderDeploymentDesign(response, request, access, f.app)
	dynamicWantStatus(t, response, 403)
	if !strings.Contains(response.Body.String(), "database_subscription_required") {
		t.Fatal("republish failed for a reason other than missing database payment")
	}
}

func TestDatabaseCheckoutRetryKeepsIndependentIdempotencyState(t *testing.T) {
	w, fixture := databaseBillingWorker(t)
	setDatabaseSubscriptions(t, w, fixture, paidSubscription("active", "price_test", true, time.Now().Add(time.Hour).Unix()))
	base, err := w.billing.read("alpha")
	if err != nil {
		t.Fatal(err)
	}
	fixture.checkoutFailure = true
	dynamicWantStatus(t, billingRequest(w, "database/checkout", "admin", ""), 503)
	retryKey := fixture.creationKey
	record, err := w.billing.read("alpha")
	if err != nil || record.DatabaseAttemptID == "" || record.DatabaseCheckoutID != "" || record.AttemptID != base.AttemptID {
		t.Fatalf("failed database checkout lost retry state or altered the base attempt: %+v %v", record, err)
	}
	fixture.checkoutFailure = false
	dynamicWantStatus(t, billingRequest(w, "database/checkout", "admin", ""), 200)
	if fixture.creationKey != retryKey {
		t.Fatal("database retry could create a duplicate subscription")
	}
}
