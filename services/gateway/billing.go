package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	stripe "github.com/stripe/stripe-go/v87"
)

type stripeBilling struct {
	client                      *stripe.Client
	root, origin, price, secret string
	databasePrice               string
	usage                       *databaseUsageBilling
	live                        bool
}

type tenantBilling struct {
	CustomerID              string             `json:"customerId,omitempty"`
	CheckoutID              string             `json:"checkoutId,omitempty"`
	AttemptID               string             `json:"attemptId,omitempty"`
	PriceID                 string             `json:"priceId"`
	ValidUntil              int64              `json:"validUntil"`
	HasSubscription         bool               `json:"hasSubscription"`
	CheckoutPrice           string             `json:"checkoutPrice,omitempty"`
	DatabasePriceID         string             `json:"databasePriceId,omitempty"`
	DatabaseValidUntil      int64              `json:"databaseValidUntil,omitempty"`
	DatabaseHasSubscription bool               `json:"databaseHasSubscription,omitempty"`
	DatabaseCheckoutID      string             `json:"databaseCheckoutId,omitempty"`
	DatabaseAttemptID       string             `json:"databaseAttemptId,omitempty"`
	DatabaseCheckoutPrice   string             `json:"databaseCheckoutPrice,omitempty"`
	DatabaseCheckoutModel   string             `json:"databaseCheckoutModel,omitempty"`
	DatabaseAuthorized      bool               `json:"databaseAuthorized,omitempty"`
	DatabaseConsentAt       int64              `json:"databaseConsentAt,omitempty"`
	DatabasePaymentMethod   string             `json:"databasePaymentMethod,omitempty"`
	DatabasePaymentFailed   bool               `json:"databasePaymentFailed,omitempty"`
	DatabaseState           string             `json:"databaseState,omitempty"`
	DatabaseResourceStart   int64              `json:"databaseResourceStart,omitempty"`
	DatabaseScope           string             `json:"databaseScope,omitempty"`
	DatabaseLifetimes       []databaseLifetime `json:"databaseLifetimes,omitempty"`
}

func newStripeBilling(root, origin, price, key, secret, mode string) (*stripeBilling, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("Stripe public origin must be an exact origin without path, query or credentials")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(mode == "test" && u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return nil, errors.New("Stripe public origin requires HTTPS except for loopback test mode")
	}
	if mode != "test" && mode != "live" {
		return nil, errors.New("Stripe mode must be test or live")
	}
	if (!strings.HasPrefix(key, "sk_"+mode+"_") && !strings.HasPrefix(key, "rk_"+mode+"_")) ||
		!strings.HasPrefix(secret, "whsec_") || !strings.HasPrefix(price, "price_") || !filepath.IsAbs(root) {
		return nil, errors.New("Stripe requires matching-mode secret key, webhook signing secret, recurring price ID and absolute billing root")
	}
	root = filepath.Join(root, mode)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return &stripeBilling{
		client: stripe.NewClient(key, stripe.WithBackends(stripe.NewBackends(&http.Client{Timeout: 6 * time.Second}))),
		root:   root, origin: origin, price: price, secret: secret, live: mode == "live",
	}, nil
}

func (b *stripeBilling) read(tenant string) (tenantBilling, error) {
	var record tenantBilling
	if !tenantPattern.MatchString(tenant) || tenant == "owner" {
		return record, errors.New("invalid billing tenant")
	}
	data, err := os.ReadFile(filepath.Join(b.root, tenant+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return record, nil
	}
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	if record.CustomerID == "" || record.PriceID == "" {
		return record, errors.New("billing record is invalid")
	}
	return record, nil
}

func (b *stripeBilling) save(tenant string, record tenantBilling) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	name := filepath.Join(b.root, tenant+".json")
	if err := os.WriteFile(name+".tmp", data, 0600); err != nil {
		return err
	}
	return os.Rename(name+".tmp", name)
}

func (b *stripeBilling) lock(tenant string) (func(), error) {
	if !tenantPattern.MatchString(tenant) || tenant == "owner" {
		return nil, errors.New("invalid billing tenant")
	}
	name := filepath.Join(b.root, tenant+".lock")
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return func() {
		if err := os.Remove(name); err != nil {
			log.Printf("billing lock release failed: %v", err)
		}
	}, nil
}

// Re-fetch current Stripe state instead of applying event snapshots, which may
// arrive out of order. Only the configured paid recurring price grants access.
func (b *stripeBilling) refresh(ctx context.Context, tenant string, record *tenantBilling) error {
	params := &stripe.SubscriptionListParams{Customer: stripe.String(record.CustomerID), Status: stripe.String("all")}
	params.AddExpand("data.latest_invoice")
	validUntil := int64(0)
	hasSubscription := false
	databaseValidUntil := int64(0)
	databaseHasSubscription := false
	for subscription, err := range b.client.V1Subscriptions.List(ctx, params).All(ctx) {
		if err != nil {
			return err
		}
		if subscription.Livemode != b.live || subscription.Customer == nil ||
			subscription.Customer.ID != record.CustomerID {
			return errors.New("Stripe returned an unexpected subscription customer or mode")
		}
		if subscription.Items == nil {
			continue
		}
		for _, item := range subscription.Items.Data {
			if item == nil || item.Price == nil {
				continue
			}
			database := b.databasePrice != "" && item.Price.ID == b.databasePrice
			if item.Price.ID != b.price && !database {
				continue
			}
			switch subscription.Status {
			case stripe.SubscriptionStatusActive, stripe.SubscriptionStatusPastDue, stripe.SubscriptionStatusUnpaid,
				stripe.SubscriptionStatusPaused, stripe.SubscriptionStatusTrialing, stripe.SubscriptionStatusIncomplete:
				if database {
					databaseHasSubscription = true
				} else {
					hasSubscription = true
				}
			}
			if item.Quantity == 1 && subscription.Status == stripe.SubscriptionStatusActive && subscription.LatestInvoice != nil &&
				subscription.LatestInvoice.Status == stripe.InvoiceStatusPaid {
				if database && item.CurrentPeriodEnd > databaseValidUntil {
					databaseValidUntil = item.CurrentPeriodEnd
				} else if !database && item.CurrentPeriodEnd > validUntil {
					validUntil = item.CurrentPeriodEnd
				}
			}
		}
	}
	record.ValidUntil, record.PriceID = validUntil, b.price
	record.HasSubscription = hasSubscription
	record.DatabaseValidUntil, record.DatabasePriceID = databaseValidUntil, b.databasePrice
	record.DatabaseHasSubscription = databaseHasSubscription
	if b.usage != nil {
		if err := b.usage.refreshTenant(ctx, tenant, record); err != nil {
			return err
		}
	}
	return b.save(tenant, *record)
}

func stripeRedirect(raw, host string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != host || u.User != nil {
		return "", errors.New("Stripe returned an invalid hosted page URL")
	}
	return raw, nil
}

func (b *stripeBilling) checkout(ctx context.Context, tenant string) (string, error) {
	return b.checkoutPlan(ctx, tenant, false)
}

func (b *stripeBilling) checkoutPlan(ctx context.Context, tenant string, database bool) (string, error) {
	if database && b.usage != nil {
		return b.checkoutUsage(ctx, tenant)
	}
	priceID := b.price
	if database {
		priceID = b.databasePrice
		if priceID == "" {
			return "", &deploymentError{503, "database_billing_unavailable", "The database add-on price is not configured."}
		}
	}
	unlock, err := b.lock(tenant)
	if err != nil {
		return "", err
	}
	defer unlock()
	record, err := b.read(tenant)
	if err != nil {
		return "", err
	}
	price, err := b.client.V1Prices.Retrieve(ctx, priceID, nil)
	if err != nil {
		return "", err
	}
	if !price.Active || price.Livemode != b.live || price.Recurring == nil || price.Recurring.UsageType != stripe.PriceRecurringUsageTypeLicensed {
		return "", errors.New("configured Stripe price must be an active, fixed recurring price in the configured mode")
	}
	if database && (!validDatabasePrice(price) || price.ID != priceID) {
		return "", errors.New("database price must be a fixed positive monthly USD price")
	}
	if record.CustomerID == "" {
		params := &stripe.CustomerCreateParams{Metadata: map[string]string{"CookistTenant": tenant}}
		params.SetIdempotencyKey("cookist-customer-" + tenant)
		customer, err := b.client.V1Customers.Create(ctx, params)
		if err != nil {
			return "", err
		}
		if customer.ID == "" || customer.Livemode != b.live {
			return "", errors.New("Stripe returned an invalid customer")
		}
		record.CustomerID, record.PriceID = customer.ID, b.price
		if err := b.save(tenant, record); err != nil {
			return "", err
		}
	}
	if err := b.refresh(ctx, tenant, &record); err != nil {
		return "", err
	}
	hasSubscription := record.HasSubscription
	checkoutID, attemptID, checkoutPrice := &record.CheckoutID, &record.AttemptID, &record.CheckoutPrice
	kind := ""
	if database {
		if record.PriceID != b.price || record.ValidUntil <= time.Now().Unix() {
			return "", &deploymentError{403, "subscription_required", "Activate your Cookist subscription before adding a database plan."}
		}
		hasSubscription = record.DatabaseHasSubscription
		checkoutID, attemptID, checkoutPrice = &record.DatabaseCheckoutID, &record.DatabaseAttemptID, &record.DatabaseCheckoutPrice
		kind = "database-"
	}
	if hasSubscription {
		return "", &deploymentError{409, "already_subscribed", "Your organization already has a subscription. Use Manage billing."}
	}
	if *checkoutID != "" {
		session, err := b.client.V1CheckoutSessions.Retrieve(ctx, *checkoutID, nil)
		if err != nil {
			return "", err
		}
		if session.Status == stripe.CheckoutSessionStatusOpen {
			if *checkoutPrice != priceID {
				return "", &deploymentError{409, "checkout_price_changed", "An older checkout is still open. Ask the operator to expire it before starting checkout for the new price."}
			}
			if database && (session.Customer == nil || session.Customer.ID != record.CustomerID || session.Livemode != b.live) {
				return "", errors.New("database checkout customer or mode changed")
			}
			return stripeRedirect(session.URL, "checkout.stripe.com")
		}
		*checkoutID, *attemptID = "", ""
	}
	if *attemptID == "" {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return "", err
		}
		*attemptID = hex.EncodeToString(id[:])
		if err := b.save(tenant, record); err != nil {
			return "", err
		}
	}
	params := &stripe.CheckoutSessionCreateParams{
		Mode: stripe.String("subscription"), Customer: stripe.String(record.CustomerID),
		ClientReferenceID: stripe.String(tenant),
		LineItems:         []*stripe.CheckoutSessionCreateLineItemParams{{Price: stripe.String(priceID), Quantity: stripe.Int64(1)}},
		SuccessURL:        stripe.String(b.origin + "/?billing=returned"),
		CancelURL:         stripe.String(b.origin + "/?billing=cancelled"),
		Metadata:          map[string]string{"CookistTenant": tenant},
		SubscriptionData:  &stripe.CheckoutSessionCreateSubscriptionDataParams{Metadata: map[string]string{"CookistTenant": tenant}},
	}
	if database {
		params.Metadata["CookistPlan"] = "database"
		params.SubscriptionData.Metadata["CookistPlan"] = "database"
	}
	params.SetIdempotencyKey("cookist-checkout-" + kind + tenant + "-" + *attemptID)
	session, err := b.client.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return "", err
	}
	redirect, err := stripeRedirect(session.URL, "checkout.stripe.com")
	if err != nil || session.ID == "" {
		return "", errors.New("Stripe returned an invalid checkout session")
	}
	if database && (session.Customer == nil || session.Customer.ID != record.CustomerID || session.Livemode != b.live) {
		return "", errors.New("Stripe returned an unexpected database checkout customer or mode")
	}
	*checkoutID, *checkoutPrice = session.ID, priceID
	if err := b.save(tenant, record); err != nil {
		return "", err
	}
	return redirect, nil
}

func (w *deploymentWorker) billingRoute(rw http.ResponseWriter, r *http.Request) {
	if w.billing == nil {
		jsonError(rw, 503, "billing_unavailable", "Subscription checkout is not configured.")
		return
	}
	if r.URL.Path == "/api/billing/webhook" {
		w.billingWebhook(rw, r)
		return
	}
	access, err := w.authorize(r)
	if err != nil {
		w.billingError(rw, err)
		return
	}
	if w.databaseBillingRoute(rw, r, access) {
		return
	}
	if r.URL.Path != "/api/billing/checkout" && r.URL.Path != "/api/billing/portal" {
		jsonError(rw, 404, "not_found", "Route not found.")
		return
	}
	if r.Method != http.MethodPost {
		rw.Header().Set("Allow", "POST")
		jsonError(rw, 405, "method_not_allowed", "Use POST.")
		return
	}
	if access.Role != "customer_admin" || w.localAccess {
		jsonError(rw, 403, "forbidden", "Only a customer administrator can manage hosted subscription billing. Owner and local access do not require payment.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, 1024))
	if err != nil || len(strings.TrimSpace(string(body))) != 0 {
		jsonError(rw, 400, "invalid_request", "Billing requests must not contain client-supplied prices, customers or tenant IDs.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var redirect string
	if r.URL.Path == "/api/billing/checkout" {
		redirect, err = w.billing.checkout(ctx, access.TenantID)
	} else {
		var record tenantBilling
		record, err = w.billing.read(access.TenantID)
		if err == nil && record.CustomerID == "" {
			err = &deploymentError{409, "no_billing_account", "Start a subscription before opening the billing portal."}
		}
		if err == nil {
			var session *stripe.BillingPortalSession
			session, err = w.billing.client.V1BillingPortalSessions.Create(ctx,
				&stripe.BillingPortalSessionCreateParams{Customer: stripe.String(record.CustomerID), ReturnURL: stripe.String(w.billing.origin + "/")})
			if err == nil {
				redirect, err = stripeRedirect(session.URL, "billing.stripe.com")
			}
		}
	}
	if err != nil {
		w.billingError(rw, err)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(rw).Encode(map[string]string{"url": redirect})
}

func (w *deploymentWorker) billingError(rw http.ResponseWriter, err error) {
	var apiErr *deploymentError
	if errors.As(err, &apiErr) {
		jsonError(rw, apiErr.status, apiErr.code, apiErr.message)
	} else if errors.Is(err, os.ErrExist) {
		jsonError(rw, 409, "billing_busy", "Billing is processing another request or needs operator recovery. Try again shortly.")
	} else {
		log.Printf("billing request failed (%T)", err)
		jsonError(rw, 503, "billing_unavailable", "Billing is temporarily unavailable. No access was granted; check your subscription before retrying.")
	}
}

func (w *deploymentWorker) billingWebhook(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		rw.Header().Set("Allow", "POST")
		jsonError(rw, 405, "method_not_allowed", "Use POST.")
		return
	}
	payload, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, 1024*1024))
	if err != nil {
		jsonError(rw, 413, "body_too_large", "Webhook payload exceeds the size limit.")
		return
	}
	event, err := stripe.ConstructEvent(payload, r.Header.Get("Stripe-Signature"), w.billing.secret)
	if err != nil || event.Livemode != w.billing.live || event.Account != "" {
		jsonError(rw, 400, "invalid_webhook", "Invalid Stripe webhook signature, API version, account or mode.")
		return
	}
	switch event.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded", "checkout.session.async_payment_failed",
		"customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted", "invoice.paid", "invoice.payment_failed":
	default:
		log.Printf("Stripe event type ignored: %s", event.Type)
		rw.WriteHeader(http.StatusNoContent)
		return
	}
	var object struct {
		Customer string `json:"customer"`
	}
	if event.Data == nil || json.Unmarshal(event.Data.Raw, &object) != nil || object.Customer == "" {
		jsonError(rw, 400, "invalid_webhook", "Stripe event lacks a customer.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	customer, err := w.billing.client.V1Customers.Retrieve(ctx, object.Customer, nil)
	if err != nil {
		w.billingError(rw, err)
		return
	}
	tenant := customer.Metadata["CookistTenant"]
	if !tenantPattern.MatchString(tenant) || tenant == "owner" {
		log.Printf("Stripe event ignored for a non-Cookist customer")
		rw.WriteHeader(http.StatusNoContent)
		return
	}
	unlock, err := w.billing.lock(tenant)
	if err != nil {
		// Non-2xx causes Stripe to retry instead of losing a concurrent event.
		log.Printf("Stripe webhook lock unavailable (%T)", err)
		jsonError(rw, 503, "billing_busy", "Retry the webhook later.")
		return
	}
	defer unlock()
	record, err := w.billing.read(tenant)
	if err == nil && (record.CustomerID != customer.ID || customer.Livemode != w.billing.live) {
		err = errors.New("Stripe customer does not match the stored tenant")
	}
	if err == nil {
		err = w.billing.refresh(ctx, tenant, &record)
	}
	if err != nil {
		w.billingError(rw, err)
		return
	}
	rw.WriteHeader(http.StatusNoContent)
}
