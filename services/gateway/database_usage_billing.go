package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	stripe "github.com/stripe/stripe-go/v87"
)

const databaseUsageTerms = "aws-cost-1.9-v1"

type databaseUsageBilling struct {
	billing     *stripeBilling
	source      databaseCostSource
	root, scope string
}

type databaseUsageRevision struct {
	Key             string `json:"key"`
	Amount          int64  `json:"amount"`
	InvoiceID       string `json:"invoiceId,omitempty"`
	CreditID        string `json:"creditId,omitempty"`
	Status          string `json:"status"`
	URL             string `json:"url,omitempty"`
	CancelRequested bool   `json:"cancelRequested,omitempty"`
	Reverses        string `json:"reverses,omitempty"`
}

type databaseUsageMonth struct {
	Version                         int `json:"version"`
	Tenant, Customer, Scope, Period string
	AWSCostUSD                      string                  `json:"awsCostUsd"`
	PendingAWSCostUSD               string                  `json:"pendingAwsCostUsd,omitempty"`
	ChargeCents                     int64                   `json:"chargeCents"`
	Revisions                       []databaseUsageRevision `json:"revisions"`
}

func billingRandomKey() (string, error) {
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(key[:]), nil
}

func (u *databaseUsageBilling) filename(tenant, period string) (string, error) {
	if !tenantPattern.MatchString(tenant) || tenant == "owner" {
		return "", errors.New("invalid database usage tenant")
	}
	if parsed, err := time.Parse("2006-01", period); err != nil || parsed.Format("2006-01") != period {
		return "", errors.New("invalid database usage month")
	}
	return filepath.Join(u.root, tenant, period+".json"), nil
}

func (u *databaseUsageBilling) readMonth(tenant, period string) (databaseUsageMonth, error) {
	month := databaseUsageMonth{Version: 1, Tenant: tenant, Scope: u.scope, Period: period, Revisions: []databaseUsageRevision{}}
	name, err := u.filename(tenant, period)
	if err != nil {
		return month, err
	}
	data, err := os.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return month, nil
	}
	if err != nil {
		return month, err
	}
	if len(data) > 1024*1024 {
		return month, errors.New("database usage ledger is too large")
	}
	if err := json.Unmarshal(data, &month); err != nil {
		return month, err
	}
	if month.Version != 1 || month.Tenant != tenant || month.Period != period || month.Scope != u.scope || month.Customer == "" {
		return month, errors.New("database usage ledger identity is invalid")
	}
	expected, err := databaseChargeCents(month.AWSCostUSD)
	if err != nil || expected != month.ChargeCents {
		return month, errors.New("database usage ledger amount is invalid")
	}
	if month.PendingAWSCostUSD != "" {
		if _, err := databaseChargeCents(month.PendingAWSCostUSD); err != nil {
			return month, err
		}
	}
	sum := decimal.Zero
	keys := map[string]bool{}
	operations := map[string]databaseUsageRevision{}
	for _, revision := range month.Revisions {
		if len(revision.Key) != 32 || keys[revision.Key] {
			return month, errors.New("database usage ledger operations are invalid")
		}
		if _, err := hex.DecodeString(revision.Key); err != nil {
			return month, err
		}
		keys[revision.Key] = true
		operations[revision.Key] = revision
		sum = sum.Add(decimal.NewFromInt(revision.Amount))
	}
	if !sum.Equal(decimal.NewFromInt(month.ChargeCents)) {
		return month, errors.New("database usage ledger revisions do not balance")
	}
	reversed := map[string]bool{}
	for _, revision := range month.Revisions {
		if revision.Reverses == "" {
			continue
		}
		parent, ok := operations[revision.Reverses]
		if !ok || parent.Amount <= 0 || revision.Amount != -parent.Amount || revision.Status != "adjusted" || reversed[parent.Key] {
			return month, errors.New("database ledger cancellation is invalid")
		}
		reversed[parent.Key] = true
	}
	return month, nil
}

func (u *databaseUsageBilling) saveMonth(month databaseUsageMonth) error {
	name, err := u.filename(month.Tenant, month.Period)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(month)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(name+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name+".tmp", name)
}

func (u *databaseUsageBilling) months(tenant string) ([]databaseUsageMonth, error) {
	if !tenantPattern.MatchString(tenant) || tenant == "owner" {
		return nil, errors.New("invalid database usage tenant")
	}
	entries, err := os.ReadDir(filepath.Join(u.root, tenant))
	if errors.Is(err, os.ErrNotExist) {
		return []databaseUsageMonth{}, nil
	}
	if err != nil {
		return nil, err
	}
	months := []databaseUsageMonth{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		month, err := u.readMonth(tenant, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		months = append(months, month)
	}
	return months, nil
}

func (b *stripeBilling) checkoutUsage(ctx context.Context, tenant string) (string, error) {
	if err := b.usage.source.Ready(ctx); err != nil {
		return "", err
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
	if record.CustomerID == "" {
		return "", &deploymentError{403, "subscription_required", "Activate your Cookist plan first."}
	}
	if _, err := b.usage.customer(ctx, tenant, record.CustomerID); err != nil {
		return "", err
	}
	if err := b.refresh(ctx, tenant, &record); err != nil {
		return "", err
	}
	if record.ValidUntil <= time.Now().Unix() {
		return "", &deploymentError{403, "subscription_required", "Activate your Cookist plan first."}
	}
	if record.DatabaseAuthorized || record.DatabaseState == "deleting" {
		return "", &deploymentError{409, "database_already_authorized", "Database billing is already authorized or deletion is pending."}
	}
	if record.DatabaseCheckoutID != "" {
		session, err := b.client.V1CheckoutSessions.Retrieve(ctx, record.DatabaseCheckoutID, nil)
		if err != nil {
			return "", err
		}
		if session.Status == stripe.CheckoutSessionStatusOpen {
			if !validUsageSession(session, tenant, record.CustomerID, b.live) || session.ID != record.DatabaseCheckoutID {
				return "", errors.New("unexpected database setup session")
			}
			return stripeRedirect(session.URL, "checkout.stripe.com")
		}
		record.DatabaseCheckoutID, record.DatabaseAttemptID = "", ""
	}
	if record.DatabaseAttemptID == "" {
		record.DatabaseAttemptID, err = billingRandomKey()
		if err != nil {
			return "", err
		}
		record.DatabaseCheckoutModel = databaseUsageTerms
		if err := b.save(tenant, record); err != nil {
			return "", err
		}
	}
	params := &stripe.CheckoutSessionCreateParams{
		Mode: stripe.String("setup"), Currency: stripe.String("usd"), Customer: stripe.String(record.CustomerID),
		AllowedPaymentMethodTypes: []*string{stripe.String("card")}, ClientReferenceID: stripe.String(tenant),
		SuccessURL: stripe.String(b.origin + "/?billing=returned"), CancelURL: stripe.String(b.origin + "/?billing=cancelled"),
		Metadata: map[string]string{"CookistTenant": tenant, "CookistDatabaseTerms": databaseUsageTerms},
		SetupIntentData: &stripe.CheckoutSessionCreateSetupIntentDataParams{
			Metadata:    map[string]string{"CookistTenant": tenant, "CookistDatabaseTerms": databaseUsageTerms},
			Description: stripe.String("Monthly actual AWS database costs multiplied by 1.9, billed in arrears."),
		},
		CustomText: &stripe.CheckoutSessionCreateCustomTextParams{Submit: &stripe.CheckoutSessionCreateCustomTextSubmitParams{
			Message: stripe.String("Authorize monthly off-session charges: your organization's AWS-tagged DynamoDB USD cost x 1.9, rounded to cents, billed after the month closes. Retained storage keeps accruing charges until you explicitly delete the database in Cookist. No fixed monthly database fee."),
		}},
	}
	params.SetIdempotencyKey("cookist-database-setup-" + tenant + "-" + record.DatabaseAttemptID)
	session, err := b.client.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return "", err
	}
	if !validUsageSession(session, tenant, record.CustomerID, b.live) || session.ID == "" {
		return "", errors.New("Stripe returned an invalid database setup session")
	}
	redirect, err := stripeRedirect(session.URL, "checkout.stripe.com")
	if err != nil {
		return "", err
	}
	record.DatabaseCheckoutID = session.ID
	if err := b.save(tenant, record); err != nil {
		return "", err
	}
	return redirect, nil
}

func validUsageSession(session *stripe.CheckoutSession, tenant, customer string, live bool) bool {
	return session != nil && session.Mode == stripe.CheckoutSessionModeSetup && session.Livemode == live &&
		session.Customer != nil && session.Customer.ID == customer && session.Metadata["CookistTenant"] == tenant &&
		session.Metadata["CookistDatabaseTerms"] == databaseUsageTerms
}

func (u *databaseUsageBilling) refreshTenant(ctx context.Context, tenant string, record *tenantBilling) error {
	b := u.billing
	if !record.DatabaseAuthorized && record.DatabaseState != "deleting" &&
		record.DatabaseCheckoutModel == databaseUsageTerms && record.DatabaseCheckoutID != "" {
		session, err := b.client.V1CheckoutSessions.Retrieve(ctx, record.DatabaseCheckoutID, nil)
		if err != nil {
			return err
		}
		if !validUsageSession(session, tenant, record.CustomerID, b.live) || session.ID != record.DatabaseCheckoutID {
			return errors.New("database setup customer, mode or terms changed")
		}
		if session.Status == stripe.CheckoutSessionStatusComplete {
			if _, err := u.customer(ctx, tenant, record.CustomerID); err != nil {
				return err
			}
			if session.SetupIntent == nil || session.SetupIntent.ID == "" {
				return errors.New("database setup has no intent")
			}
			intent, err := b.client.V1SetupIntents.Retrieve(ctx, session.SetupIntent.ID, nil)
			if err != nil {
				return err
			}
			if intent.ID != session.SetupIntent.ID || intent.Livemode != b.live || intent.Customer == nil || intent.Customer.ID != record.CustomerID ||
				intent.Status != stripe.SetupIntentStatusSucceeded || intent.Usage != stripe.SetupIntentUsageOffSession ||
				intent.Metadata["CookistTenant"] != tenant || intent.Metadata["CookistDatabaseTerms"] != databaseUsageTerms ||
				intent.PaymentMethod == nil || intent.PaymentMethod.ID == "" {
				return errors.New("database payment setup was not verified")
			}
			method, err := b.client.V1PaymentMethods.Retrieve(ctx, intent.PaymentMethod.ID, nil)
			if err != nil {
				return err
			}
			if method.ID != intent.PaymentMethod.ID || method.Type != stripe.PaymentMethodTypeCard || method.Livemode != b.live || method.Customer == nil || method.Customer.ID != record.CustomerID {
				return errors.New("database payment method is not owned by the billing customer")
			}
			record.DatabasePaymentMethod = method.ID
			record.DatabaseAuthorized = true
			if record.DatabaseConsentAt == 0 {
				record.DatabaseConsentAt = time.Now().Unix()
			}
			if record.DatabaseState == "deleted" {
				record.DatabaseState = "not_created"
			}
		}
	}
	months, err := u.months(tenant)
	if err != nil {
		return err
	}
	failed := false
	for _, month := range months {
		if month.Customer != record.CustomerID {
			return errors.New("database usage ledger customer changed")
		}
		changed := false
		for i := range month.Revisions {
			revision := &month.Revisions[i]
			if revision.InvoiceID == "" || revision.Status == "paid" || revision.Status == "void" {
				continue
			}
			invoice, err := b.client.V1Invoices.Retrieve(ctx, revision.InvoiceID, nil)
			if err != nil {
				return err
			}
			if err := u.validateInvoice(invoice, month, *revision, invoice.Status != stripe.InvoiceStatusDraft); err != nil {
				return err
			}
			revision.Status = string(invoice.Status)
			revision.URL = invoice.HostedInvoiceURL
			changed = true
			if invoice.Status == stripe.InvoiceStatusUncollectible || (invoice.Status == stripe.InvoiceStatusOpen && invoice.Attempted) {
				failed = true
			}
		}
		if changed {
			if err := u.saveMonth(month); err != nil {
				return err
			}
		}
	}
	record.DatabasePaymentFailed = failed
	return nil
}

func (u *databaseUsageBilling) validateInvoice(invoice *stripe.Invoice, month databaseUsageMonth, revision databaseUsageRevision, amount bool) error {
	if invoice == nil || invoice.ID == "" || invoice.Livemode != u.billing.live || invoice.Customer == nil ||
		invoice.Customer.ID != month.Customer || invoice.Currency != stripe.CurrencyUSD ||
		invoice.Metadata["CookistUsageKey"] != revision.Key || invoice.Metadata["CookistTenant"] != month.Tenant ||
		invoice.Metadata["CookistCostPeriod"] != month.Period || invoice.Metadata["CookistCostScope"] != month.Scope ||
		(amount && (invoice.Subtotal != revision.Amount || invoice.Total != revision.Amount)) {
		return errors.New("Stripe database invoice identity or amount changed")
	}
	return nil
}

func (u *databaseUsageBilling) findInvoice(ctx context.Context, month databaseUsageMonth, revision databaseUsageRevision) (*stripe.Invoice, error) {
	params := &stripe.InvoiceListParams{Customer: stripe.String(month.Customer)}
	params.Limit = stripe.Int64(100)
	var found *stripe.Invoice
	count := 0
	for invoice, err := range u.billing.client.V1Invoices.List(ctx, params).All(ctx) {
		if err != nil {
			return nil, err
		}
		count++
		if count > 10000 {
			return nil, errors.New("database invoice recovery needs operator review")
		}
		if invoice.Metadata["CookistUsageKey"] != revision.Key {
			continue
		}
		if found != nil {
			return nil, errors.New("multiple invoices match a database ledger operation")
		}
		if err := u.validateInvoice(invoice, month, revision, false); err != nil {
			return nil, err
		}
		found = invoice
	}
	return found, nil
}

func (u *databaseUsageBilling) customer(ctx context.Context, tenant, id string) (*stripe.Customer, error) {
	customer, err := u.billing.client.V1Customers.Retrieve(ctx, id, nil)
	if err != nil {
		return nil, err
	}
	if customer.ID != id || customer.Livemode != u.billing.live || customer.Metadata["CookistTenant"] != tenant {
		return nil, errors.New("database billing customer changed")
	}
	return customer, nil
}

func (u *databaseUsageBilling) paymentMethod(ctx context.Context, tenant string, record tenantBilling) (string, error) {
	customer, err := u.customer(ctx, tenant, record.CustomerID)
	if err != nil {
		return "", err
	}
	id := record.DatabasePaymentMethod
	if customer.InvoiceSettings != nil && customer.InvoiceSettings.DefaultPaymentMethod != nil {
		id = customer.InvoiceSettings.DefaultPaymentMethod.ID
	}
	if id == "" {
		return "", errors.New("database charges require the authorized payment method")
	}
	method, err := u.billing.client.V1PaymentMethods.Retrieve(ctx, id, nil)
	if err != nil {
		return "", err
	}
	if method.ID != id || method.Type != stripe.PaymentMethodTypeCard || method.Customer == nil || method.Customer.ID != record.CustomerID || method.Livemode != u.billing.live {
		return "", errors.New("database payment method ownership changed")
	}
	return id, nil
}

func (u *databaseUsageBilling) billRevision(ctx context.Context, month *databaseUsageMonth, index int, record tenantBilling, collect bool) error {
	revision := &month.Revisions[index]
	if revision.Reverses != "" {
		return nil
	}
	if revision.Amount == 0 {
		revision.Status = "no_charge"
		return u.saveMonth(*month)
	}
	if revision.Amount < 0 {
		return u.creditRevision(ctx, month, index)
	}
	b := u.billing
	var invoice *stripe.Invoice
	var err error
	if revision.InvoiceID != "" {
		invoice, err = b.client.V1Invoices.Retrieve(ctx, revision.InvoiceID, nil)
	} else {
		invoice, err = u.findInvoice(ctx, *month, *revision)
		if err == nil && invoice == nil {
			method, err := u.paymentMethod(ctx, month.Tenant, record)
			if err != nil {
				return err
			}
			params := &stripe.InvoiceCreateParams{
				Customer: stripe.String(month.Customer), Currency: stripe.String("usd"), AutoAdvance: stripe.Bool(false),
				CollectionMethod: stripe.String("charge_automatically"), PendingInvoiceItemsBehavior: stripe.String("exclude"),
				DefaultPaymentMethod: stripe.String(method), AutomaticTax: &stripe.InvoiceCreateAutomaticTaxParams{Enabled: stripe.Bool(false)},
				Description: stripe.String("Cookist database " + month.Period + ": actual AWS cost x 1.9 (including any correction)."),
				Metadata:    map[string]string{"CookistUsageKey": revision.Key, "CookistTenant": month.Tenant, "CookistCostPeriod": month.Period, "CookistCostScope": month.Scope},
			}
			params.SetIdempotencyKey(revision.Key + "-invoice")
			invoice, err = b.client.V1Invoices.Create(ctx, params)
		}
	}
	if err != nil {
		return err
	}
	if err := u.validateInvoice(invoice, *month, *revision, false); err != nil {
		return err
	}
	revision.InvoiceID, revision.Status = invoice.ID, string(invoice.Status)
	if err := u.saveMonth(*month); err != nil {
		return err
	}
	if invoice.Status == stripe.InvoiceStatusDraft {
		itemFound := false
		items := &stripe.InvoiceItemListParams{Invoice: stripe.String(invoice.ID), Customer: stripe.String(month.Customer)}
		for item, err := range b.client.V1InvoiceItems.List(ctx, items).All(ctx) {
			if err != nil {
				return err
			}
			if itemFound || item.Metadata["CookistUsageKey"] != revision.Key || item.Amount != revision.Amount ||
				item.Currency != stripe.CurrencyUSD || item.Customer == nil || item.Customer.ID != month.Customer ||
				item.Invoice == nil || item.Invoice.ID != invoice.ID || item.Discountable || item.Livemode != b.live {
				return errors.New("database invoice contains unexpected items")
			}
			itemFound = true
		}
		if !itemFound {
			start, err := time.Parse("2006-01", month.Period)
			if err != nil {
				return err
			}
			params := &stripe.InvoiceItemCreateParams{
				Customer: stripe.String(month.Customer), Invoice: stripe.String(invoice.ID), Amount: stripe.Int64(revision.Amount),
				Currency: stripe.String("usd"), Discountable: stripe.Bool(false),
				Description: stripe.String(fmt.Sprintf("AWS-tagged DynamoDB %s: %s USD x 1.9 = %d cents total. This invoice adjusts prior billing by %d cents.", month.Period, month.AWSCostUSD, month.ChargeCents, revision.Amount)),
				Metadata:    map[string]string{"CookistUsageKey": revision.Key},
				Period:      &stripe.InvoiceItemCreatePeriodParams{Start: stripe.Int64(start.Unix()), End: stripe.Int64(start.AddDate(0, 1, 0).Unix())},
			}
			params.SetIdempotencyKey(revision.Key + "-item")
			if _, err := b.client.V1InvoiceItems.Create(ctx, params); err != nil {
				return err
			}
		}
		invoice, err = b.client.V1Invoices.Retrieve(ctx, invoice.ID, nil)
		if err != nil {
			return err
		}
		if err := u.validateInvoice(invoice, *month, *revision, true); err != nil {
			return err
		}
		params := &stripe.InvoiceFinalizeInvoiceParams{AutoAdvance: stripe.Bool(false)}
		params.SetIdempotencyKey(revision.Key + "-finalize")
		invoice, err = b.client.V1Invoices.FinalizeInvoice(ctx, invoice.ID, params)
		if err != nil {
			return err
		}
	}
	if err := u.validateInvoice(invoice, *month, *revision, true); err != nil {
		return err
	}
	if collect && invoice.Status == stripe.InvoiceStatusOpen && !invoice.Attempted {
		method, err := u.paymentMethod(ctx, month.Tenant, record)
		if err != nil {
			return err
		}
		params := &stripe.InvoicePayParams{OffSession: stripe.Bool(true), PaymentMethod: stripe.String(method)}
		params.SetIdempotencyKey(revision.Key + "-pay")
		paid, payErr := b.client.V1Invoices.Pay(ctx, invoice.ID, params)
		if payErr == nil {
			invoice = paid
		} else {
			invoice, err = b.client.V1Invoices.Retrieve(ctx, invoice.ID, nil)
			if err != nil {
				return err
			}
			if err := u.validateInvoice(invoice, *month, *revision, true); err != nil {
				return err
			}
			revision.Status, revision.URL = string(invoice.Status), invoice.HostedInvoiceURL
			if err := u.saveMonth(*month); err != nil {
				return err
			}
			if invoice.Status != stripe.InvoiceStatusPaid {
				return payErr
			}
		}
	}
	if err := u.validateInvoice(invoice, *month, *revision, true); err != nil {
		return err
	}
	revision.Status, revision.URL = string(invoice.Status), invoice.HostedInvoiceURL
	if err := u.saveMonth(*month); err != nil {
		return err
	}
	if collect && invoice.Status != stripe.InvoiceStatusPaid && invoice.Status != stripe.InvoiceStatusVoid {
		return &deploymentError{402, "database_payment_required", "The database usage invoice requires payment. Use its Stripe invoice link."}
	}
	return nil
}

func (u *databaseUsageBilling) creditRevision(ctx context.Context, month *databaseUsageMonth, index int) error {
	revision := &month.Revisions[index]
	if revision.CreditID != "" && revision.Status == "credited" {
		return nil
	}
	if _, err := u.customer(ctx, month.Tenant, month.Customer); err != nil {
		return err
	}
	params := &stripe.CustomerBalanceTransactionListParams{Customer: stripe.String(month.Customer)}
	var found *stripe.CustomerBalanceTransaction
	count := 0
	for transaction, err := range u.billing.client.V1CustomerBalanceTransactions.List(ctx, params).All(ctx) {
		if err != nil {
			return err
		}
		count++
		if count > 10000 {
			return errors.New("database credit recovery needs operator review")
		}
		if transaction.Metadata["CookistUsageKey"] != revision.Key {
			continue
		}
		if found != nil {
			return errors.New("multiple database credit transactions match a ledger operation")
		}
		found = transaction
	}
	if found == nil {
		params := &stripe.CustomerBalanceTransactionCreateParams{
			Customer: stripe.String(month.Customer), Amount: stripe.Int64(revision.Amount), Currency: stripe.String("usd"),
			Description: stripe.String("Cookist AWS database cost correction " + month.Period + " x 1.9; credit to future invoices."),
			Metadata:    map[string]string{"CookistUsageKey": revision.Key, "CookistTenant": month.Tenant, "CookistCostPeriod": month.Period},
		}
		params.SetIdempotencyKey(revision.Key + "-credit")
		var err error
		found, err = u.billing.client.V1CustomerBalanceTransactions.Create(ctx, params)
		if err != nil {
			return err
		}
	}
	if found.ID == "" || found.Amount != revision.Amount || found.Currency != stripe.CurrencyUSD ||
		found.Customer == nil || found.Customer.ID != month.Customer || found.Livemode != u.billing.live ||
		found.Metadata["CookistUsageKey"] != revision.Key {
		return errors.New("Stripe returned an unexpected database credit")
	}
	revision.CreditID, revision.Status = found.ID, "credited"
	return u.saveMonth(*month)
}

func (u *databaseUsageBilling) applyReport(ctx context.Context, report databaseCostReport, now time.Time) error {
	if err := report.validate(); err != nil {
		return err
	}
	start, end, err := databaseCostPeriod(report.Period, now)
	if err != nil {
		return err
	}
	// Validate every attributed customer before initiating any monetary operation.
	for tenant := range report.Costs {
		record, err := u.billing.read(tenant)
		if err != nil {
			return err
		}
		if record.CustomerID == "" || record.DatabaseConsentAt == 0 || record.DatabaseResourceStart == 0 ||
			record.DatabaseScope != u.scope || !time.Unix(record.DatabaseResourceStart, 0).UTC().Before(end) ||
			!time.Unix(record.DatabaseConsentAt, 0).UTC().Before(end) {
			return errors.New("AWS costs do not match a consented Cookist database resource")
		}
	}
	entries, err := os.ReadDir(u.billing.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		tenant := strings.TrimSuffix(entry.Name(), ".json")
		record, err := u.billing.read(tenant)
		if err != nil {
			return err
		}
		if record.DatabaseScope != u.scope {
			continue
		}
		expected := false
		for _, life := range record.DatabaseLifetimes {
			if life.Start < end.Unix() && (life.End == 0 || life.End > start.Unix()) {
				expected = true
			}
		}
		if expected {
			if _, present := report.Costs[tenant]; !present {
				return errors.New("AWS report is missing organization database costs; no zero-cost fallback was applied")
			}
		}
	}
	var failures []error
	for tenant, raw := range report.Costs {
		unlock, err := u.billing.lock(tenant)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		err = u.applyTenant(ctx, tenant, report.Period, raw)
		unlock()
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (u *databaseUsageBilling) applyTenant(ctx context.Context, tenant, period, raw string) (resultErr error) {
	record, err := u.billing.read(tenant)
	if err != nil {
		return err
	}
	defer func() {
		if err := u.refreshTenant(ctx, tenant, &record); err != nil {
			if resultErr == nil {
				resultErr = err
			}
			return
		}
		if err := u.billing.save(tenant, record); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	month, err := u.readMonth(tenant, period)
	if err != nil {
		return err
	}
	if month.Customer != "" && month.Customer != record.CustomerID {
		return errors.New("database usage customer changed")
	}
	expected, err := databaseChargeCents(raw)
	if err != nil {
		return err
	}
	amount, err := databaseCostAmount(raw)
	if err != nil {
		return err
	}
	if month.Customer != "" {
		month.PendingAWSCostUSD = amount.String()
		if err := u.saveMonth(month); err != nil {
			return err
		}
	}
	base := decimal.NewFromInt(month.ChargeCents)
	reversed := map[string]bool{}
	for _, revision := range month.Revisions {
		if revision.Reverses != "" {
			reversed[revision.Reverses] = true
		}
	}
	var cancellations []databaseUsageRevision
	for i := range month.Revisions {
		revision := &month.Revisions[i]
		if revision.Amount <= 0 || reversed[revision.Key] || (!revision.CancelRequested && expected >= month.ChargeCents) {
			continue
		}
		// Finish the exact draft without collecting; do not require payment of an overstated old report.
		if err := u.billRevision(ctx, &month, i, record, false); err != nil {
			return err
		}
		if revision.Status == "paid" {
			revision.CancelRequested = false
			continue
		}
		revision.CancelRequested = true
		if err := u.saveMonth(month); err != nil {
			return err
		}
		if revision.Status != "void" {
			params := &stripe.InvoiceVoidInvoiceParams{}
			params.SetIdempotencyKey(revision.Key + "-void-correction")
			invoice, voidErr := u.billing.client.V1Invoices.VoidInvoice(ctx, revision.InvoiceID, params)
			if voidErr != nil {
				invoice, err = u.billing.client.V1Invoices.Retrieve(ctx, revision.InvoiceID, nil)
				if err != nil {
					return err
				}
			}
			if err := u.validateInvoice(invoice, month, *revision, true); err != nil {
				return err
			}
			revision.Status = string(invoice.Status)
			if revision.Status == "paid" {
				revision.CancelRequested = false
				continue
			}
			if revision.Status != "void" {
				if voidErr != nil {
					return voidErr
				}
				return errors.New("database invoice correction could not cancel the old amount")
			}
			if err := u.saveMonth(month); err != nil {
				return err
			}
		}
		key, err := billingRandomKey()
		if err != nil {
			return err
		}
		cancellations = append(cancellations, databaseUsageRevision{Key: key, Amount: -revision.Amount, Status: "adjusted", Reverses: revision.Key})
		base = base.Sub(decimal.NewFromInt(revision.Amount))
	}
	if month.Customer == "" || expected != month.ChargeCents || len(cancellations) > 0 {
		delta := decimal.NewFromInt(expected).Sub(base).BigInt()
		if !delta.IsInt64() {
			return errors.New("database cost correction exceeds supported amount")
		}
		for i := range month.Revisions {
			month.Revisions[i].CancelRequested = false
		}
		month.Revisions = append(month.Revisions, cancellations...)
		key, err := billingRandomKey()
		if err != nil {
			return err
		}
		month.Revisions = append(month.Revisions, databaseUsageRevision{Key: key, Amount: delta.Int64(), Status: "pending"})
	}
	month.Customer, month.AWSCostUSD, month.ChargeCents = record.CustomerID, amount.String(), expected
	month.PendingAWSCostUSD = ""
	if err := u.saveMonth(month); err != nil {
		return err
	}
	// Apply credits before collecting any remaining invoices for the corrected month.
	for _, negative := range []bool{true, false} {
		for i := range month.Revisions {
			if (month.Revisions[i].Amount < 0) != negative {
				continue
			}
			if err := u.billRevision(ctx, &month, i, record, true); err != nil {
				return err
			}
		}
	}
	return nil
}
