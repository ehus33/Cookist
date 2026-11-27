package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	stripe "github.com/stripe/stripe-go/v87"
)

var databasePricePattern = regexp.MustCompile(`^price_[A-Za-z0-9_]{1,120}$`)

func (b *stripeBilling) configureDatabasePrice(id string) error {
	if !databasePricePattern.MatchString(id) || id == b.price {
		return errors.New("database add-on requires a separate Stripe price ID")
	}
	b.databasePrice = id
	return nil
}

func validDatabasePrice(price *stripe.Price) bool {
	return price != nil && price.Active && price.Currency == stripe.CurrencyUSD && price.UnitAmount > 0 &&
		price.BillingScheme == stripe.PriceBillingSchemePerUnit && price.TransformQuantity == nil && price.Recurring != nil &&
		price.Recurring.UsageType == stripe.PriceRecurringUsageTypeLicensed &&
		price.Recurring.Interval == stripe.PriceRecurringIntervalMonth && price.Recurring.IntervalCount == 1
}

func (w *deploymentWorker) databaseEntitled(tenant string) (bool, error) {
	if tenant == "owner" || w.localAccess {
		return true, nil
	}
	if w.billing == nil || (w.billing.databasePrice == "" && w.billing.usage == nil) {
		return false, nil
	}
	record, err := w.billing.read(tenant)
	if err != nil {
		return false, err
	}
	if w.billing.usage != nil {
		return record.DatabaseAuthorized && !record.DatabasePaymentFailed && record.DatabaseState != "deleting" && record.DatabaseState != "deleted", nil
	}
	return record.DatabasePriceID == w.billing.databasePrice && record.DatabaseValidUntil > time.Now().Unix(), nil
}

func (w *deploymentWorker) requireDatabase(rw http.ResponseWriter, tenant string) bool {
	paid, err := w.databaseEntitled(tenant)
	if err != nil {
		w.billingError(rw, err)
		return false
	}
	if !paid {
		jsonError(rw, 403, "database_subscription_required", "This app's database access is paused. Its owner must activate the database add-on.")
		return false
	}
	return true
}

type databasePlanPrice struct {
	AmountCents int64  `json:"amountCents"`
	Currency    string `json:"currency"`
	Interval    string `json:"interval"`
}

type databaseBillingStatus struct {
	Configured        bool                   `json:"configured"`
	ServiceReady      bool                   `json:"serviceReady"`
	Subscribed        bool                   `json:"subscribed"`
	HasSubscription   bool                   `json:"hasSubscription"`
	CanCheckout       bool                   `json:"canCheckout"`
	CanManageBilling  bool                   `json:"canManageBilling"`
	Mode              string                 `json:"mode"`
	Price             *databasePlanPrice     `json:"price,omitempty"`
	BillingModel      string                 `json:"billingModel"`
	Multiplier        string                 `json:"multiplier"`
	Currency          string                 `json:"currency"`
	DatabaseState     string                 `json:"databaseState"`
	CanDeleteDatabase bool                   `json:"canDeleteDatabase"`
	Invoices          []databaseUsageInvoice `json:"invoices"`
	Notice            string                 `json:"notice,omitempty"`
}

type databaseUsageInvoice struct {
	Period      string `json:"period"`
	AWSCostUSD  string `json:"awsCostUsd"`
	ChargeCents int64  `json:"chargeCents"`
	Status      string `json:"status"`
	URL         string `json:"url,omitempty"`
}

func (w *deploymentWorker) databaseBillingRoute(rw http.ResponseWriter, r *http.Request, access deploymentAccess) bool {
	if w.usageActionRoute(rw, r, access) {
		return true
	}
	if r.URL.Path != "/api/billing/database" && r.URL.Path != "/api/billing/database/checkout" {
		return false
	}
	if !access.CanList {
		jsonError(rw, 403, "forbidden", "An organization account is required for database billing.")
		return true
	}
	if r.URL.Path == "/api/billing/database/checkout" {
		if r.Method != http.MethodPost {
			rw.Header().Set("Allow", "POST")
			jsonError(rw, 405, "method_not_allowed", "Use POST.")
			return true
		}
		if access.Role != "customer_admin" || w.localAccess {
			jsonError(rw, 403, "forbidden", "Only a customer administrator can purchase the database add-on.")
			return true
		}
		if !access.Subscribed {
			jsonError(rw, 403, "subscription_required", "Activate your Cookist subscription before adding a database plan.")
			return true
		}
		body, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, 1024))
		if err != nil || len(strings.TrimSpace(string(body))) != 0 {
			jsonError(rw, 400, "invalid_request", "Database billing requests must not contain prices, customers or tenant IDs.")
			return true
		}
		if w.dynamic == nil {
			jsonError(rw, 503, "dynamic_unavailable", "The app database service must be configured before purchasing this add-on.")
			return true
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		redirect, err := w.billing.checkoutPlan(ctx, access.TenantID, true)
		if err != nil {
			w.billingError(rw, err)
			return true
		}
		builderJSON(rw, 200, map[string]string{"url": redirect})
		return true
	}
	if r.Method != http.MethodGet {
		rw.Header().Set("Allow", "GET")
		jsonError(rw, 405, "method_not_allowed", "Use GET.")
		return true
	}
	status := databaseBillingStatus{
		Configured: w.billing.databasePrice != "", ServiceReady: w.dynamic != nil,
		CanManageBilling: access.CanManageBilling, Mode: "test",
		BillingModel: "fixed-monthly", Multiplier: "1.9", Currency: "usd", DatabaseState: "not_created", Invoices: []databaseUsageInvoice{},
	}
	if w.billing.live {
		status.Mode = "live"
	}
	var err error
	status.Subscribed, err = w.databaseEntitled(access.TenantID)
	if err != nil {
		w.billingError(rw, err)
		return true
	}
	if access.TenantID != "owner" && !w.localAccess {
		record, err := w.billing.read(access.TenantID)
		if err != nil {
			w.billingError(rw, err)
			return true
		}
		status.HasSubscription = record.DatabaseHasSubscription
		if w.billing.usage != nil {
			status.HasSubscription = record.DatabaseAuthorized
			if record.DatabaseState == "retained" || record.DatabaseState == "provisioning" {
				status.DatabaseState = "retained"
			}
			if record.DatabaseState == "deleting" || record.DatabaseState == "deleted" {
				status.DatabaseState = record.DatabaseState
			}
			status.CanDeleteDatabase = access.Role == "customer_admin" && status.DatabaseState == "retained"
			months, err := w.billing.usage.months(access.TenantID)
			if err != nil {
				w.billingError(rw, err)
				return true
			}
			for _, month := range months {
				if month.Customer != record.CustomerID {
					w.billingError(rw, errors.New("database usage customer changed"))
					return true
				}
				invoice := databaseUsageInvoice{Period: month.Period, AWSCostUSD: month.AWSCostUSD, ChargeCents: month.ChargeCents, Status: "no_charge"}
				if len(month.Revisions) > 0 {
					last := month.Revisions[len(month.Revisions)-1]
					invoice.Status, invoice.URL = last.Status, last.URL
					if invoice.URL != "" {
						if _, err := stripeRedirect(invoice.URL, "invoice.stripe.com"); err != nil {
							if _, err := stripeRedirect(invoice.URL, "pay.stripe.com"); err != nil {
								w.billingError(rw, err)
								return true
							}
						}
					}
				}
				status.Invoices = append(status.Invoices, invoice)
			}
		}
	}
	if w.billing.usage != nil {
		status.Configured, status.BillingModel = true, "aws-cost-markup"
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		err := w.billing.usage.source.Ready(ctx)
		cancel()
		if err != nil {
			log.Printf("database cost attribution unavailable (%T)", err)
			status.ServiceReady = false
			status.Notice = "AWS cost attribution is unavailable. New authorizations are paused; data recovery and database deletion remain available."
		}
	} else if status.Configured {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		price, err := w.billing.client.V1Prices.Retrieve(ctx, w.billing.databasePrice, nil)
		cancel()
		if err != nil || !validDatabasePrice(price) || price.Livemode != w.billing.live || price.ID != w.billing.databasePrice {
			if err == nil {
				err = errors.New("database price is not an active fixed monthly USD price in the configured mode")
			}
			w.billingError(rw, err)
			return true
		}
		status.Price = &databasePlanPrice{price.UnitAmount, string(price.Currency), "month"}
	}
	status.CanCheckout = status.Configured && status.ServiceReady && access.Role == "customer_admin" &&
		access.Subscribed && !w.localAccess && !status.HasSubscription && !status.Subscribed
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(rw).Encode(status)
	return true
}
