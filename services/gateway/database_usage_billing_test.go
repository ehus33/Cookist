package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	stripe "github.com/stripe/stripe-go/v87"
)

type fixtureDatabaseCosts struct {
	readyErr error
	report   databaseCostReport
	calls    []string
}

func (f *fixtureDatabaseCosts) Ready(context.Context) error { return f.readyErr }
func (f *fixtureDatabaseCosts) Report(_ context.Context, period string, _ time.Time) (databaseCostReport, error) {
	f.calls = append(f.calls, period)
	if f.readyErr != nil {
		return databaseCostReport{}, f.readyErr
	}
	return databaseCostReport{Period: period, Costs: f.report.Costs}, nil
}

type usageStripeFixture struct {
	mu                                                                 sync.Mutex
	invoices                                                           map[string]map[string]any
	items                                                              map[string][]map[string]any
	credits                                                            []map[string]any
	setupComplete, wrongMethodCustomer, mainExpired, paymentFails      bool
	failInvoiceAfterCommit, failItemAfterCommit, failCreditAfterCommit bool
	setups, payAttempts                                                int
	source                                                             *fixtureDatabaseCosts
	customerTenantOverride                                             string
}

func usageBillingWorker(t *testing.T) (*deploymentWorker, *usageStripeFixture) {
	t.Helper()
	w := policyWorker(t)
	b, err := newStripeBilling(t.TempDir(), "https://cookist.example", "price_test", "sk_test_example", "whsec_test", "test")
	if err != nil {
		t.Fatal(err)
	}
	f := &usageStripeFixture{invoices: map[string]map[string]any{}, items: map[string][]map[string]any{},
		source: &fixtureDatabaseCosts{report: databaseCostReport{Costs: map[string]string{}}}}
	b.usage = &databaseUsageBilling{billing: b, source: f.source, root: filepath.Join(b.root, "usage"), scope: "data-test"}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		rw.Header().Set("Content-Type", "application/json")
		reply := func(body any) {
			if err := json.NewEncoder(rw).Encode(body); err != nil {
				t.Error(err)
			}
		}
		failure := func(status int) {
			rw.WriteHeader(status)
			reply(map[string]any{"error": map[string]string{"type": "api_error", "message": "fixture failure"}})
		}
		metadata := func() map[string]string {
			out := map[string]string{}
			for key, values := range r.Form {
				if strings.HasPrefix(key, "metadata[") && len(values) == 1 {
					out[strings.TrimSuffix(strings.TrimPrefix(key, "metadata["), "]")] = values[0]
				}
			}
			return out
		}
		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/customers/") && !strings.Contains(r.URL.Path, "/balance_transactions"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/customers/")
			tenant := strings.TrimPrefix(id, "cus_")
			if f.customerTenantOverride != "" {
				tenant = f.customerTenantOverride
			}
			reply(map[string]any{"id": id, "livemode": false, "metadata": map[string]string{"CookistTenant": tenant}, "invoice_settings": map[string]string{"default_payment_method": "pm_" + tenant}})
		case r.Method == "GET" && r.URL.Path == "/v1/subscriptions":
			data := []any{}
			if !f.mainExpired {
				s := paidSubscription("active", "price_test", true, time.Now().Add(time.Hour).Unix()).(map[string]any)
				s["customer"] = r.Form.Get("customer")
				data = append(data, s)
			}
			reply(map[string]any{"object": "list", "data": data, "has_more": false})
		case r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions":
			f.setups++
			for key, want := range map[string]string{"mode": "setup", "currency": "usd", "customer": "cus_alpha",
				"allowed_payment_method_types[0]": "card", "metadata[CookistDatabaseTerms]": databaseUsageTerms,
				"setup_intent_data[metadata][CookistDatabaseTerms]": databaseUsageTerms} {
				if r.Form.Get(key) != want {
					t.Errorf("setup %s=%q want %q", key, r.Form.Get(key), want)
				}
			}
			if r.Form.Get("line_items[0][price]") != "" || !strings.Contains(r.Form.Get("custom_text[submit][message]"), "1.9") ||
				!strings.Contains(r.Form.Get("custom_text[submit][message]"), "Retained storage") || r.Header.Get("Idempotency-Key") == "" {
				t.Error("setup included a fixed price or omitted variable-charge terms/idempotency")
			}
			reply(map[string]any{"id": "cs_usage_alpha", "mode": "setup", "status": "open", "customer": "cus_alpha", "livemode": false,
				"metadata": map[string]string{"CookistTenant": "alpha", "CookistDatabaseTerms": databaseUsageTerms}, "url": "https://checkout.stripe.com/c/pay/setup"})
		case r.Method == "GET" && r.URL.Path == "/v1/checkout/sessions/cs_usage_alpha":
			status := "open"
			if f.setupComplete {
				status = "complete"
			}
			reply(map[string]any{"id": "cs_usage_alpha", "mode": "setup", "status": status, "customer": "cus_alpha", "livemode": false,
				"metadata":     map[string]string{"CookistTenant": "alpha", "CookistDatabaseTerms": databaseUsageTerms},
				"setup_intent": "seti_alpha", "url": "https://checkout.stripe.com/c/pay/setup"})
		case r.Method == "GET" && r.URL.Path == "/v1/setup_intents/seti_alpha":
			reply(map[string]any{"id": "seti_alpha", "customer": "cus_alpha", "payment_method": "pm_alpha", "livemode": false, "status": "succeeded", "usage": "off_session",
				"metadata": map[string]string{"CookistTenant": "alpha", "CookistDatabaseTerms": databaseUsageTerms}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/payment_methods/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/payment_methods/")
			customer := "cus_" + strings.TrimPrefix(id, "pm_")
			if f.wrongMethodCustomer {
				customer = "cus_foreign"
			}
			reply(map[string]any{"id": id, "type": "card", "customer": customer, "livemode": false})
		case r.Method == "GET" && r.URL.Path == "/v1/invoices":
			data := []any{}
			for _, invoice := range f.invoices {
				if invoice["customer"] == r.Form.Get("customer") {
					data = append(data, invoice)
				}
			}
			reply(map[string]any{"object": "list", "data": data, "has_more": false})
		case r.Method == "POST" && r.URL.Path == "/v1/invoices":
			if r.Form.Get("auto_advance") != "false" || r.Form.Get("pending_invoice_items_behavior") != "exclude" || r.Form.Get("automatic_tax[enabled]") != "false" {
				t.Error("usage invoice could auto-charge before its exact line was verified")
			}
			id := fmt.Sprintf("in_usage_%d", len(f.invoices)+1)
			invoice := map[string]any{"id": id, "object": "invoice", "customer": r.Form.Get("customer"), "currency": "usd", "livemode": false, "status": "draft",
				"subtotal": int64(0), "total": int64(0), "attempted": false, "metadata": metadata(), "hosted_invoice_url": "https://invoice.stripe.com/i/" + id}
			f.invoices[id] = invoice
			if f.failInvoiceAfterCommit {
				f.failInvoiceAfterCommit = false
				failure(503)
				return
			}
			reply(invoice)
		case r.Method == "GET" && r.URL.Path == "/v1/invoiceitems":
			data := []any{}
			for _, item := range f.items[r.Form.Get("invoice")] {
				data = append(data, item)
			}
			reply(map[string]any{"object": "list", "data": data, "has_more": false})
		case r.Method == "POST" && r.URL.Path == "/v1/invoiceitems":
			id := r.Form.Get("invoice")
			invoice := f.invoices[id]
			if invoice == nil || invoice["customer"] != r.Form.Get("customer") || r.Form.Get("discountable") != "false" {
				t.Error("invoice item is not isolated or allows discounts")
			}
			amount, err := strconv.ParseInt(r.Form.Get("amount"), 10, 64)
			if err != nil {
				t.Error(err)
			}
			item := map[string]any{"id": fmt.Sprintf("ii_%s_%d", id, len(f.items[id])+1), "invoice": id, "customer": r.Form.Get("customer"),
				"currency": "usd", "amount": amount, "discountable": false, "livemode": false, "metadata": metadata()}
			f.items[id] = append(f.items[id], item)
			invoice["subtotal"] = invoice["subtotal"].(int64) + amount
			invoice["total"] = invoice["subtotal"]
			if f.failItemAfterCommit {
				f.failItemAfterCommit = false
				failure(503)
				return
			}
			reply(item)
		case strings.HasPrefix(r.URL.Path, "/v1/invoices/"):
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/invoices/"), "/")
			invoice := f.invoices[parts[0]]
			if invoice == nil {
				failure(404)
				return
			}
			if len(parts) == 2 && parts[1] == "finalize" {
				if r.Form.Get("auto_advance") != "false" {
					t.Error("invoice finalization enabled unverified auto-payment")
				}
				invoice["status"] = "open"
			}
			if len(parts) == 2 && parts[1] == "void" {
				invoice["status"] = "void"
			}
			if len(parts) == 2 && parts[1] == "pay" {
				f.payAttempts++
				if r.Form.Get("off_session") != "true" || r.Form.Get("payment_method") != "pm_"+strings.TrimPrefix(invoice["customer"].(string), "cus_") {
					t.Error("payment used an unapproved method or session")
				}
				invoice["attempted"] = true
				if f.paymentFails {
					failure(402)
					return
				}
				invoice["status"] = "paid"
			}
			reply(invoice)
		case strings.Contains(r.URL.Path, "/balance_transactions"):
			customer := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/customers/"), "/")[0]
			if r.Method == "GET" {
				data := []any{}
				for _, credit := range f.credits {
					if credit["customer"] == customer {
						data = append(data, credit)
					}
				}
				reply(map[string]any{"object": "list", "data": data, "has_more": false})
			} else {
				amount, err := strconv.ParseInt(r.Form.Get("amount"), 10, 64)
				if err != nil {
					t.Error(err)
				}
				credit := map[string]any{"id": fmt.Sprintf("cbtxn_%d", len(f.credits)+1), "customer": customer, "amount": amount, "currency": "usd", "livemode": false,
					"metadata": metadata(), "type": "adjustment"}
				f.credits = append(f.credits, credit)
				if f.failCreditAfterCommit {
					f.failCreditAfterCommit = false
					failure(503)
					return
				}
				reply(credit)
			}
		default:
			t.Errorf("unexpected usage Stripe request %s %s", r.Method, r.URL.Path)
			failure(500)
		}
	}))
	t.Cleanup(server.Close)
	backend := stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: stripe.String(server.URL), HTTPClient: server.Client(), MaxNetworkRetries: stripe.Int64(0)})
	b.client = stripe.NewClient("sk_test_example", stripe.WithBackends(&stripe.Backends{API: backend}))
	w.billing, w.dynamic = b, &dynamicService{}
	for _, tenant := range []string{"alpha", "beta"} {
		if err := b.save(tenant, tenantBilling{CustomerID: "cus_" + tenant, PriceID: b.price, ValidUntil: time.Now().Add(time.Hour).Unix(), HasSubscription: true}); err != nil {
			t.Fatal(err)
		}
	}
	return w, f
}

func consentUsageFixture(t *testing.T, w *deploymentWorker, tenant string) {
	t.Helper()
	record, err := w.billing.read(tenant)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Unix()
	record.DatabaseAuthorized, record.DatabaseConsentAt, record.DatabaseResourceStart = true, start, start
	record.DatabasePaymentMethod, record.DatabaseScope, record.DatabaseState = "pm_"+tenant, w.billing.usage.scope, "retained"
	record.DatabaseLifetimes = []databaseLifetime{{Start: start}}
	if err := w.billing.save(tenant, record); err != nil {
		t.Fatal(err)
	}
}

var usageTestNow = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

func TestUsageSetupRequiresVerifiedConsentAndOwnedPaymentMethod(t *testing.T) {
	w, f := usageBillingWorker(t)
	for i := 0; i < 2; i++ {
		dynamicWantStatus(t, billingRequest(w, "database/checkout", "admin", ""), 200)
	}
	if f.setups != 1 || len(f.invoices) != 0 {
		t.Fatal("setup created duplicate agreements or charged upfront")
	}
	access, err := w.accessFor("admin")
	if err != nil || access.CanUseDynamic {
		t.Fatal("returning an open setup unlocked dynamic apps")
	}
	f.setupComplete = true
	f.wrongMethodCustomer = true
	dynamicWantStatus(t, signedBillingEvent(t, http.HandlerFunc(w.billingRoute), "checkout.session.completed", "cus_alpha", false), 503)
	access, _ = w.accessFor("admin")
	if access.DatabaseSubscribed {
		t.Fatal("foreign payment method granted usage access")
	}
	f.wrongMethodCustomer = false
	dynamicWantStatus(t, signedBillingEvent(t, http.HandlerFunc(w.billingRoute), "checkout.session.completed", "cus_alpha", false), 204)
	access, err = w.accessFor("admin")
	if err != nil || !access.CanUseDynamic || !access.Subscribed {
		t.Fatalf("verified setup did not enable dynamic use: %+v %v", access, err)
	}
	dynamicWantStatus(t, billingRequest(w, "database/checkout", "admin", ""), 409)
	dynamicWantStatus(t, billingRequest(w, "database/checkout", "viewer", ""), 403)
	dynamicWantStatus(t, billingRequest(w, "database/checkout", "deployer", ""), 403)
	dynamicWantStatus(t, billingRequest(w, "database/checkout", "admin", `{"cost":20,"customer":"cus_foreign"}`), 400)
}

func TestActualAWSDatabaseCostsProduce19xTenantIsolatedInvoices(t *testing.T) {
	w, f := usageBillingWorker(t)
	consentUsageFixture(t, w, "alpha")
	consentUsageFixture(t, w, "beta")
	report := databaseCostReport{Period: "2026-10", Costs: map[string]string{"alpha": "20.0000000000", "beta": "30"}}
	for i := 0; i < 2; i++ {
		if err := w.billing.usage.applyReport(context.Background(), report, usageTestNow); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.invoices) != 2 || f.payAttempts != 2 {
		t.Fatal("monthly retry created duplicate charges")
	}
	got := map[string]int64{}
	for _, invoice := range f.invoices {
		got[invoice["customer"].(string)] = invoice["subtotal"].(int64)
		if invoice["status"] != "paid" || len(f.items[invoice["id"].(string)]) != 1 {
			t.Fatal("invoice is unpaid or contains duplicate items")
		}
	}
	if got["cus_alpha"] != 3800 || got["cus_beta"] != 5700 {
		t.Fatalf("not AWS cost x1.9: %#v", got)
	}
	response := databaseStatusRequest(w, "admin")
	dynamicWantStatus(t, response, 200)
	var status databaseBillingStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.BillingModel != "aws-cost-markup" || status.Price != nil || len(status.Invoices) != 1 ||
		status.Invoices[0].AWSCostUSD != "20" || status.Invoices[0].ChargeCents != 3800 {
		t.Fatalf("status exposed a fixed price or another tenant: %+v", status)
	}
}

func TestUsageLedgerRecoversRemoteSuccessAfterLostResponses(t *testing.T) {
	for _, stage := range []string{"invoice", "item"} {
		t.Run(stage, func(t *testing.T) {
			w, f := usageBillingWorker(t)
			consentUsageFixture(t, w, "alpha")
			if stage == "invoice" {
				f.failInvoiceAfterCommit = true
			} else {
				f.failItemAfterCommit = true
			}
			report := databaseCostReport{Period: "2026-10", Costs: map[string]string{"alpha": "20"}}
			if err := w.billing.usage.applyReport(context.Background(), report, usageTestNow); err == nil {
				t.Fatal("lost response was reported as success")
			}
			// The fixture has no Stripe idempotency cache: metadata recovery must work even after key expiry.
			if err := w.billing.usage.applyReport(context.Background(), report, usageTestNow.Add(48*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if len(f.invoices) != 1 || len(f.items["in_usage_1"]) != 1 || f.payAttempts != 1 {
				t.Fatal("recovery duplicated the monthly charge")
			}
		})
	}
}

func TestUsageCorrectionsChargeOnlyDeltaAndCreditDeclinesExactlyOnce(t *testing.T) {
	w, f := usageBillingWorker(t)
	consentUsageFixture(t, w, "alpha")
	apply := func(amount string) error {
		return w.billing.usage.applyReport(context.Background(), databaseCostReport{Period: "2026-10", Costs: map[string]string{"alpha": amount}}, usageTestNow)
	}
	if err := apply("20"); err != nil {
		t.Fatal(err)
	}
	if err := apply("30"); err != nil {
		t.Fatal(err)
	}
	f.failCreditAfterCommit = true
	if err := apply("20"); err == nil {
		t.Fatal("lost credit response was reported as success")
	}
	if err := apply("20"); err != nil {
		t.Fatal(err)
	}
	if len(f.invoices) != 2 || len(f.credits) != 1 || f.credits[0]["amount"].(int64) != -1900 {
		t.Fatal("correction recharged the full month or duplicated its credit")
	}
	month, err := w.billing.usage.readMonth("alpha", "2026-10")
	if err != nil || month.ChargeCents != 3800 || len(month.Revisions) != 3 || month.Revisions[2].Status != "credited" {
		t.Fatalf("corrected ledger does not balance: %+v %v", month, err)
	}
}

func TestUsagePaymentFailurePausesDatabaseNotMainPlanAndDoesNotAutoRetry(t *testing.T) {
	w, f := usageBillingWorker(t)
	consentUsageFixture(t, w, "alpha")
	f.paymentFails = true
	report := databaseCostReport{Period: "2026-10", Costs: map[string]string{"alpha": "20"}}
	if err := w.billing.usage.applyReport(context.Background(), report, usageTestNow); err == nil {
		t.Fatal("decline was hidden")
	}
	access, err := w.accessFor("admin")
	if err != nil || !access.CanCreate || access.DatabaseSubscribed || !access.CanDelete {
		t.Fatalf("failure revoked main/recovery or granted database: %+v %v", access, err)
	}
	_ = w.billing.usage.applyReport(context.Background(), report, usageTestNow)
	if f.payAttempts != 1 {
		t.Fatal("declined usage invoice was automatically retried")
	}
	f.mu.Lock()
	f.invoices["in_usage_1"]["status"] = "paid"
	f.mu.Unlock()
	dynamicWantStatus(t, signedBillingEvent(t, http.HandlerFunc(w.billingRoute), "invoice.paid", "cus_alpha", false), 204)
	access, _ = w.accessFor("admin")
	if !access.DatabaseSubscribed {
		t.Fatal("confirmed invoice payment did not resume database")
	}
	f.mainExpired = true
	dynamicWantStatus(t, signedBillingEvent(t, http.HandlerFunc(w.billingRoute), "customer.subscription.deleted", "cus_alpha", false), 204)
	access, _ = w.accessFor("admin")
	if access.CanCreate || !access.DatabaseSubscribed {
		t.Fatal("database agreement granted main access or stopped paid database recovery")
	}
}

func TestUsageNoMissingEstimatedUnconsentedOrClientSuppliedCostBilling(t *testing.T) {
	w, f := usageBillingWorker(t)
	consentUsageFixture(t, w, "alpha")
	cases := []databaseCostReport{
		{Period: "2026-10", Costs: map[string]string{}},
		{Period: "2026-10", Costs: map[string]string{"alpha": "not-a-cost"}},
		{Period: "2026-10", Costs: map[string]string{"alpha": "20", "unknown": "30"}},
	}
	for _, report := range cases {
		if err := w.billing.usage.applyReport(context.Background(), report, usageTestNow); err == nil {
			t.Fatal("missing/invalid/unconsented report was accepted")
		}
	}
	if len(f.invoices) != 0 {
		t.Fatal("invalid report initiated a monetary operation")
	}
	f.source.readyErr = errors.New("cost tags inactive")
	response := databaseStatusRequest(w, "admin")
	dynamicWantStatus(t, response, 200)
	var status databaseBillingStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.ServiceReady || status.Notice == "" || !status.CanDeleteDatabase {
		t.Fatal("attribution outage hid recovery or allowed new checkout")
	}
	record, err := w.billing.read("alpha")
	if err != nil {
		t.Fatal(err)
	}
	record.DatabaseAuthorized = false
	if err := w.billing.save("alpha", record); err != nil {
		t.Fatal(err)
	}
	dynamicWantStatus(t, billingRequest(w, "database/checkout", "admin", ""), 503)
	dynamicWantStatus(t, billingRequest(w, "database/reconcile", "admin", `{"period":"2026-10","cost":20}`), 403)
}

func TestUsageZeroCostProducesNoChargeAndReplaysSafely(t *testing.T) {
	w, f := usageBillingWorker(t)
	consentUsageFixture(t, w, "alpha")
	report := databaseCostReport{Period: "2026-10", Costs: map[string]string{"alpha": "0.000"}}
	for i := 0; i < 2; i++ {
		if err := w.billing.usage.applyReport(context.Background(), report, usageTestNow); err != nil {
			t.Fatal(err)
		}

	}
	month, err := w.billing.usage.readMonth("alpha", "2026-10")
	if err != nil || month.ChargeCents != 0 || len(month.Revisions) != 1 || month.Revisions[0].Status != "no_charge" ||
		len(f.invoices) != 0 || f.payAttempts != 0 {
		t.Fatal("zero cost was charged, guessed or duplicated")
	}
}

func TestDownwardCostCorrectionReplacesUnpaidInvoiceWithoutIssuingUnearnedCredit(t *testing.T) {
	w, f := usageBillingWorker(t)
	consentUsageFixture(t, w, "alpha")
	apply := func(amount string) error {
		return w.billing.usage.applyReport(context.Background(), databaseCostReport{Period: "2026-10", Costs: map[string]string{"alpha": amount}}, usageTestNow)
	}
	f.paymentFails = true
	if err := apply("20"); err == nil {
		t.Fatal("declined invoice was hidden")
	}
	f.paymentFails = false
	if err := apply("10"); err != nil {
		t.Fatal(err)
	}
	if len(f.credits) != 0 || f.invoices["in_usage_1"]["status"] != "void" ||
		f.invoices["in_usage_2"]["subtotal"].(int64) != 1900 || f.invoices["in_usage_2"]["status"] != "paid" {
		t.Fatal("correction required paying an overstated bill or issued credit for unpaid costs")
	}
	if err := apply("10"); err != nil {
		t.Fatal(err)
	}
	if len(f.invoices) != 2 || f.payAttempts != 2 {
		t.Fatal("replacement was duplicated")
	}
	month, err := w.billing.usage.readMonth("alpha", "2026-10")
	if err != nil || month.ChargeCents != 1900 || len(month.Revisions) != 3 {
		t.Fatalf("replacement ledger is invalid: %+v %v", month, err)
	}
}

func TestUsageSchedulerBoundsBackfillAndDoesNotRefetchSettledAncientMonths(t *testing.T) {
	w, f := usageBillingWorker(t)
	consentUsageFixture(t, w, "alpha")
	record, err := w.billing.read("alpha")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	record.DatabaseConsentAt, record.DatabaseResourceStart = start, start
	record.DatabaseLifetimes = []databaseLifetime{{Start: start}}
	if err := w.billing.save("alpha", record); err != nil {
		t.Fatal(err)
	}
	f.source.report.Costs = map[string]string{"alpha": "0"}
	if err := w.reconcileDatabaseUsage(context.Background(), usageTestNow); err != nil {
		t.Fatal(err)
	}
	if len(f.source.calls) != 12 {
		t.Fatal("daily AWS backfill is not bounded")
	}
	if err := w.reconcileDatabaseUsage(context.Background(), usageTestNow.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(f.source.calls) != 22 {
		t.Fatal("unbilled older months were dropped")
	}
	if err := w.reconcileDatabaseUsage(context.Background(), usageTestNow.Add(96*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(f.source.calls) != 22 || len(f.invoices) != 0 {
		t.Fatal("settled historical months were queried again or guessed charges applied")
	}
}

func TestUsageNeverCollectsChangedInvoiceOrCustomerIdentity(t *testing.T) {
	for _, field := range []string{"customer", "livemode", "currency", "total", "scope"} {
		t.Run(field, func(t *testing.T) {
			w, f := usageBillingWorker(t)
			consentUsageFixture(t, w, "alpha")
			f.failItemAfterCommit = true
			report := databaseCostReport{Period: "2026-10", Costs: map[string]string{"alpha": "20"}}
			if err := w.billing.usage.applyReport(context.Background(), report, usageTestNow); err == nil {
				t.Fatal("expected lost draft response")
			}
			invoice := f.invoices["in_usage_1"]
			switch field {
			case "customer":
				invoice[field] = "cus_beta"
			case "livemode":
				invoice[field] = true
			case "currency":
				invoice[field] = "eur"
			case "total":
				invoice[field] = int64(3900)
			case "scope":
				invoice["metadata"].(map[string]string)["CookistCostScope"] = "foreign-live"
			}
			if err := w.billing.usage.applyReport(context.Background(), report, usageTestNow); err == nil || f.payAttempts != 0 {
				t.Fatal("changed invoice was collected")
			}
		})
	}
	w, f := usageBillingWorker(t)
	consentUsageFixture(t, w, "alpha")
	f.customerTenantOverride = "beta"
	if err := w.billing.usage.applyReport(context.Background(), databaseCostReport{Period: "2026-10", Costs: map[string]string{"alpha": "20"}}, usageTestNow); err == nil || len(f.invoices) != 0 {
		t.Fatal("customer metadata mismatch initiated billing")
	}
}
