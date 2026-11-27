package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	stripe "github.com/stripe/stripe-go/v87"
)

type stripeFixture struct {
	mu                    sync.Mutex
	subscriptions         []any
	checkouts             int
	creationKey           string
	checkoutFailure       bool
	databasePriceResponse string
}

func billingWorker(t *testing.T) (*deploymentWorker, *stripeFixture) {
	t.Helper()
	w := policyWorker(t)
	billing, err := newStripeBilling(t.TempDir(), "https://cookist.example", "price_test", "sk_test_example", "whsec_test", "test")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &stripeFixture{subscriptions: []any{}}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/prices/price_test":
			_, _ = rw.Write([]byte(`{"id":"price_test","active":true,"livemode":false,"recurring":{"usage_type":"licensed","interval":"month"}}`))
		case "GET /v1/prices/price_database":
			response := fixture.databasePriceResponse
			if response == "" {
				response = `{"id":"price_database","active":true,"livemode":false,"currency":"usd","unit_amount":3800,"billing_scheme":"per_unit","recurring":{"usage_type":"licensed","interval":"month","interval_count":1}}`
			}
			_, _ = rw.Write([]byte(response))
		case "POST /v1/customers":
			if r.Form.Get("metadata[CookistTenant]") != "alpha" || r.Header.Get("Idempotency-Key") == "" {
				t.Error("customer creation lacks trusted tenant or idempotency key")
			}
			_, _ = rw.Write([]byte(`{"id":"cus_alpha","livemode":false,"metadata":{"CookistTenant":"alpha"}}`))
		case "GET /v1/customers/cus_alpha":
			_, _ = rw.Write([]byte(`{"id":"cus_alpha","livemode":false,"metadata":{"CookistTenant":"alpha"}}`))
		case "GET /v1/subscriptions":
			if r.Form.Get("customer") != "cus_alpha" || r.Form.Get("status") != "all" {
				t.Error("subscription lookup is not scoped to the stored customer")
			}
			_ = json.NewEncoder(rw).Encode(map[string]any{"object": "list", "data": fixture.subscriptions, "has_more": false})
		case "POST /v1/checkout/sessions":
			fixture.checkouts++
			fixture.creationKey = r.Header.Get("Idempotency-Key")
			if fixture.checkoutFailure {
				rw.WriteHeader(503)
				_, _ = rw.Write([]byte(`{"error":{"type":"api_error","message":"unavailable"}}`))
				return
			}
			price := "price_test"
			if r.Form.Get("metadata[CookistPlan]") == "database" {
				price = "price_database"
				if r.Form.Get("subscription_data[metadata][CookistPlan]") != "database" {
					t.Error("database subscription is missing its plan metadata")
				}
			}
			for key, want := range map[string]string{
				"mode": "subscription", "customer": "cus_alpha", "client_reference_id": "alpha",
				"line_items[0][price]": price, "line_items[0][quantity]": "1",
				"subscription_data[metadata][CookistTenant]": "alpha", "success_url": "https://cookist.example/?billing=returned",
			} {
				if got := r.Form.Get(key); got != want {
					t.Errorf("%s=%s, want %s", key, got, want)
				}
			}
			if price == "price_database" {
				_, _ = rw.Write([]byte(`{"id":"cs_database","customer":"cus_alpha","status":"open","url":"https://checkout.stripe.com/c/pay/database","livemode":false}`))
			} else {
				_, _ = rw.Write([]byte(`{"id":"cs_test","status":"open","url":"https://checkout.stripe.com/c/pay/test","livemode":false}`))
			}
		case "GET /v1/checkout/sessions/cs_database":
			_, _ = rw.Write([]byte(`{"id":"cs_database","customer":"cus_alpha","status":"open","url":"https://checkout.stripe.com/c/pay/database","livemode":false}`))
		case "GET /v1/checkout/sessions/cs_test":
			_, _ = rw.Write([]byte(`{"id":"cs_test","status":"open","url":"https://checkout.stripe.com/c/pay/test","livemode":false}`))
		case "POST /v1/billing_portal/sessions":
			if r.Form.Get("customer") != "cus_alpha" || r.Form.Get("return_url") != "https://cookist.example/" {
				t.Error("portal opened for the wrong customer or return origin")
			}
			_, _ = rw.Write([]byte(`{"id":"bps_test","url":"https://billing.stripe.com/p/session/test"}`))
		default:
			t.Errorf("unexpected Stripe request %s %s", r.Method, r.URL.Path)
			rw.WriteHeader(500)
			_, _ = rw.Write([]byte(`{"error":{"message":"unexpected request"}}`))
		}
	}))
	t.Cleanup(server.Close)
	backend := stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{
		URL: stripe.String(server.URL), HTTPClient: server.Client(), MaxNetworkRetries: stripe.Int64(0),
	})
	billing.client = stripe.NewClient("sk_test_example", stripe.WithBackends(&stripe.Backends{API: backend}))
	w.billing = billing
	return w, fixture
}

func billingRequest(w *deploymentWorker, route, actor, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/billing/"+route, strings.NewReader(body))
	if actor != "" {
		r.Header.Set("Authorization", "Bearer "+actor)
	}
	rw := httptest.NewRecorder()
	w.billingRoute(rw, r)
	return rw
}

func signedBillingEvent(t *testing.T, handler http.Handler, eventType, customer string, mutate bool) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"id": "evt_test", "object": "event", "api_version": stripe.APIVersion, "livemode": false,
		"type": eventType, "data": map[string]any{"object": map[string]string{"id": "sub_test", "customer": customer}},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	signature := fmt.Sprintf("t=%d,v1=%x", now.Unix(), stripe.ComputeSignature(now, payload, "whsec_test"))
	if mutate {
		payload = bytes.ReplaceAll(payload, []byte("cus_alpha"), []byte("cus_other"))
	}
	r := httptest.NewRequest("POST", "/api/billing/webhook", bytes.NewReader(payload))
	r.Header.Set("Stripe-Signature", signature)
	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, r)
	return rw
}

func paidSubscription(status, price string, invoicePaid bool, expires int64) any {
	invoiceStatus := "open"
	if invoicePaid {
		invoiceStatus = "paid"
	}
	return map[string]any{
		"id": "sub_test", "customer": "cus_alpha", "status": status, "livemode": false,
		"latest_invoice": map[string]any{"id": "in_test", "status": invoiceStatus},
		"items": map[string]any{"data": []any{map[string]any{
			"id": "si_test", "price": map[string]string{"id": price}, "quantity": 1, "current_period_end": expires,
		}}},
	}
}

func TestStripeCheckoutUsesTrustedAttributesAndDoesNotGrantAccess(t *testing.T) {
	w, fixture := billingWorker(t)
	// Static grants must not override Stripe billing when it is enabled.
	access, err := w.accessFor("admin")
	if err != nil || access.Subscribed || !access.CanCheckout {
		t.Fatalf("Stripe billing unexpectedly granted static access: %+v %v", access, err)
	}
	for actor, want := range map[string]int{"": 401, "viewer": 403, "deployer": 403, "owner": 403} {
		if rec := billingRequest(w, "checkout", actor, ""); rec.Code != want {
			t.Fatalf("%s checkout returned %d, want %d", actor, rec.Code, want)
		}
	}
	if rec := billingRequest(w, "checkout", "admin", `{"tenantId":"beta","price":"cheap"}`); rec.Code != 400 {
		t.Fatalf("client attributes accepted: %d %s", rec.Code, rec.Body)
	}
	for i := 0; i < 2; i++ {
		rec := billingRequest(w, "checkout", "admin", "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "checkout.stripe.com") {
			t.Fatalf("checkout failed: %d %s", rec.Code, rec.Body)
		}
	}
	fixture.mu.Lock()
	if fixture.checkouts != 1 || fixture.creationKey == "" {
		t.Error("retry created duplicate checkout or omitted idempotency")
	}
	fixture.mu.Unlock()
	access, err = w.accessFor("admin")
	if err != nil || access.Subscribed || access.CanCreate || !access.CanManageBilling {
		t.Fatalf("checkout redirect granted access: %+v %v", access, err)
	}
	if rec := billingRequest(w, "portal", "admin", ""); rec.Code != 200 {
		t.Fatalf("portal failed: %d %s", rec.Code, rec.Body)
	}
	if rec := billingRequest(w, "portal", "viewer", ""); rec.Code != 403 {
		t.Fatalf("viewer opened portal: %d", rec.Code)
	}
	w.localAccess = true
	if rec := billingRequest(w, "checkout", "admin", ""); rec.Code != 403 {
		t.Fatalf("local mode allowed billing: %d", rec.Code)
	}
}

func TestStripeWebhooksVerifySignaturesAndRefreshCurrentSubscription(t *testing.T) {
	w, fixture := billingWorker(t)
	if rec := billingRequest(w, "checkout", "admin", ""); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	gateway, err := newGateway(w.coreURL, t.TempDir(), nil, w)
	if err != nil {
		t.Fatal(err)
	}
	// Real gateway auth must exempt only the signed webhook, not billing actions.
	handler, err := requireCognito(gateway, w.coreURL)
	if err != nil {
		t.Fatal(err)
	}
	if rec := signedBillingEvent(t, handler, "invoice.paid", "cus_alpha", true); rec.Code != 400 {
		t.Fatalf("tampered webhook accepted: %d", rec.Code)
	}
	for _, request := range []struct{ method, path string }{
		{"POST", "/api/billing/checkout"}, {"POST", "/api/billing/portal"},
		{"GET", "/api/billing/webhook"}, {"POST", "/api/billing/webhook/"},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(request.method, request.path, nil))
		if rec.Code != 401 {
			t.Fatalf("unsigned route bypassed Cognito: %s %s returned %d", request.method, request.path, rec.Code)
		}
	}
	expiry := time.Now().Add(time.Hour).Unix()
	for _, scenario := range []struct {
		status, price string
		paid, want    bool
	}{
		{"active", "wrong_price", true, false},
		{"active", "price_test", false, false},
		{"active", "price_test", true, true},
		{"past_due", "price_test", false, false},
		{"incomplete", "price_test", false, false},
		{"canceled", "price_test", true, false},
	} {
		fixture.mu.Lock()
		fixture.subscriptions = []any{paidSubscription(scenario.status, scenario.price, scenario.paid, expiry)}
		fixture.mu.Unlock()
		// Even repeated/older invoice.paid events must reflect current Stripe state.
		for i := 0; i < 2; i++ {
			if rec := signedBillingEvent(t, handler, "invoice.paid", "cus_alpha", false); rec.Code != 204 {
				t.Fatalf("verified webhook failed: %d %s", rec.Code, rec.Body)
			}
		}
		access, err := w.accessFor("admin")
		if err != nil || access.Subscribed != scenario.want || !access.CanDelete {
			t.Fatalf("incorrect subscription/recovery state: %+v %v", access, err)
		}
		if scenario.want {
			if rec := billingRequest(w, "checkout", "admin", ""); rec.Code != 409 {
				t.Fatalf("existing subscription allowed duplicate checkout: %d", rec.Code)
			}
			if scenario.status == "incomplete" {
				if rec := billingRequest(w, "checkout", "admin", ""); rec.Code != 409 {
					t.Fatalf("pending payment allowed duplicate checkout: %d", rec.Code)
				}
			}
			other, err := w.accessFor("other")
			if err != nil || other.Subscribed {
				t.Fatalf("Stripe payment leaked to another tenant: %+v %v", other, err)
			}
		}
	}
	fixture.mu.Lock()
	fixture.subscriptions = []any{paidSubscription("active", "price_test", true, time.Now().Add(-time.Hour).Unix())}
	fixture.mu.Unlock()
	if rec := signedBillingEvent(t, handler, "invoice.paid", "cus_alpha", false); rec.Code != 204 {
		t.Fatal(rec.Body)
	}
	access, err := w.accessFor("admin")
	if err != nil || access.Subscribed || !access.CanDelete {
		t.Fatalf("expired Stripe grant remains usable: %+v %v", access, err)
	}
}

func TestStripeCheckoutFailurePreservesIdempotencyAndFailsClosed(t *testing.T) {
	w, fixture := billingWorker(t)
	fixture.mu.Lock()
	fixture.checkoutFailure = true
	fixture.mu.Unlock()
	if rec := billingRequest(w, "checkout", "admin", ""); rec.Code != 503 {
		t.Fatalf("Stripe failure was not surfaced: %d %s", rec.Code, rec.Body)
	}
	access, err := w.accessFor("admin")
	if err != nil || access.Subscribed || access.CanCreate {
		t.Fatalf("API failure granted access: %+v %v", access, err)
	}
	fixture.mu.Lock()
	firstKey := fixture.creationKey
	fixture.checkoutFailure = false
	fixture.mu.Unlock()
	if rec := billingRequest(w, "checkout", "admin", ""); rec.Code != 200 {
		t.Fatalf("checkout retry failed: %d %s", rec.Code, rec.Body)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if firstKey == "" || fixture.creationKey != firstKey {
		t.Fatal("retry changed the persisted idempotency key")
	}
}
func TestStripeWebhookRejectsWrongModeAccountAndExpiredSignatures(t *testing.T) {
	w, _ := billingWorker(t)
	for _, scenario := range []struct {
		live    bool
		account string
		age     time.Duration
	}{
		{true, "", 0}, {false, "acct_other", 0}, {false, "", -10 * time.Minute},
	} {
		payload, err := json.Marshal(map[string]any{
			"id": "evt_test", "object": "event", "api_version": stripe.APIVersion,
			"type": "invoice.paid", "livemode": scenario.live, "account": scenario.account,
			"data": map[string]any{"object": map[string]string{"customer": "cus_alpha"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(scenario.age)
		r := httptest.NewRequest("POST", "/api/billing/webhook", bytes.NewReader(payload))
		r.Header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%x", when.Unix(), stripe.ComputeSignature(when, payload, "whsec_test")))
		rec := httptest.NewRecorder()
		w.billingWebhook(rec, r)
		if rec.Code != 400 {
			t.Fatalf("invalid webhook accepted: %d %s", rec.Code, rec.Body)
		}
	}
}
func TestStripeWebhookRetriesWhenTenantLocked(t *testing.T) {
	w, _ := billingWorker(t)
	if rec := billingRequest(w, "checkout", "admin", ""); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	unlock, err := w.billing.lock("alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	handler := http.HandlerFunc(w.billingWebhook)
	if rec := signedBillingEvent(t, handler, "invoice.paid", "cus_alpha", false); rec.Code != 503 {
		t.Fatalf("locked webhook was acknowledged instead of retried: %d", rec.Code)
	}
}

func TestStripeRejectsUnsafeConfiguration(t *testing.T) {
	for _, origin := range []string{"http://public.example", "https://user:password@cookist.example", "https://cookist.example/?price=cheap"} {
		if _, err := newStripeBilling(t.TempDir(), origin, "price_test", "sk_test_example", "whsec_test", "test"); err == nil {
			t.Fatalf("unsafe origin accepted: %s", origin)
		}
	}
	if _, err := newStripeBilling(t.TempDir(), "https://cookist.example", "price_test", "sk_live_example", "whsec_test", "test"); err == nil {
		t.Fatal("live key accepted in test mode")
	}
}
