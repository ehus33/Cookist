package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

func (w *deploymentWorker) configureDatabaseUsage(ctx context.Context) error {
	if w.billing == nil || w.dynamic == nil || w.localAccess || !accountPattern.MatchString(w.account) {
		return errors.New("actual database cost billing requires hosted Stripe billing, managed dynamic services and the AWS account ID")
	}
	store, ok := w.dynamic.store.(*dynamoDynamicStore)
	if !ok || len(store.table) > 214 {
		return errors.New("actual database cost billing requires a valid DynamoDB table namespace")
	}
	control, ok := store.client.(tenantTableAPI)
	if !ok {
		return errors.New("database control-plane client is not available")
	}
	operator, err := control.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(store.table)})
	if err != nil {
		return err
	}
	expectedARN := fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s", w.dynamic.region, w.account, store.table)
	if operator.Table == nil || aws.ToString(operator.Table.TableArn) != expectedARN {
		return errors.New("operator database does not match the configured AWS account and region")
	}
	mode := "test"
	if w.billing.live {
		mode = "live"
	}
	scope := store.table + "-" + mode
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"))
	if err != nil {
		return err
	}
	root := filepath.Join(w.billing.root, "database-usage")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	w.billing.usage = &databaseUsageBilling{billing: w.billing, root: root, scope: scope,
		source: &awsDatabaseCosts{client: costexplorer.NewFromConfig(cfg), account: w.account, scope: scope}}
	store.tenants = &tenantDatabaseManager{client: control, billing: w.billing, scope: scope, region: w.dynamic.region, account: w.account}
	entries, err := os.ReadDir(w.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !idPattern.MatchString(entry.Name()) {
			continue
		}
		d, err := readDeployment(filepath.Join(w.root, entry.Name()), entry.Name())
		if err != nil {
			return err
		}
		d, err = w.ownedDeployment(d)
		if err != nil {
			return err
		}
		if d.TenantID == "owner" {
			continue
		}
		design, err := readBuilderDesign(filepath.Join(w.root, entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if design.Dynamic == nil {
			continue
		}
		record, err := w.billing.read(d.TenantID)
		if err != nil {
			return err
		}
		if record.DatabaseScope != scope {
			return errors.New("existing shared customer database data requires an explicit migration before actual-cost billing")
		}
	}
	return nil
}

func (w *deploymentWorker) tenantDatabases() *tenantDatabaseManager {
	if w.dynamic == nil {
		return nil
	}
	store, ok := w.dynamic.store.(*dynamoDynamicStore)
	if !ok {
		return nil
	}
	return store.tenants
}

func (w *deploymentWorker) prepareUsageDatabase(ctx context.Context, tenant string, dynamic *dynamicConfig) error {
	if dynamic == nil || w.billing == nil || w.billing.usage == nil || tenant == "owner" {
		return nil
	}
	manager := w.tenantDatabases()
	if manager == nil {
		return errors.New("organization databases are not configured")
	}
	return manager.ensure(ctx, tenant)
}

func (w *deploymentWorker) deleteUsageDatabase(tenant string) {
	defer w.releaseOperation()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	manager := w.tenantDatabases()
	if manager == nil {
		log.Printf("database deletion failed: manager unavailable")
		return
	}
	if err := manager.destroy(ctx, tenant); err != nil {
		log.Printf("database deletion incomplete (%T); recovery state retained", err)
		return
	}
	unlock, err := w.billing.lock(tenant)
	if err != nil {
		log.Printf("database deletion status lock failed (%T)", err)
		return
	}
	defer unlock()
	record, err := w.billing.read(tenant)
	if err != nil {
		log.Printf("database deletion status read failed (%T)", err)
		return
	}
	record.DatabaseState, record.DatabaseAuthorized = "deleted", false
	record.DatabaseCheckoutModel, record.DatabaseCheckoutID, record.DatabaseAttemptID = "", "", ""
	if len(record.DatabaseLifetimes) > 0 && record.DatabaseLifetimes[len(record.DatabaseLifetimes)-1].End == 0 {
		record.DatabaseLifetimes[len(record.DatabaseLifetimes)-1].End = time.Now().Unix()
	}
	if err := w.billing.save(tenant, record); err != nil {
		log.Printf("database deletion status save failed (%T)", err)
	}
}

func (w *deploymentWorker) usageActionRoute(rw http.ResponseWriter, r *http.Request, access deploymentAccess) bool {
	if r.URL.Path != "/api/billing/database/delete" && r.URL.Path != "/api/billing/database/reconcile" {
		return false
	}
	if w.billing.usage == nil {
		jsonError(rw, 503, "database_billing_unavailable", "Actual database usage billing is not configured.")
		return true
	}
	if r.Method != http.MethodPost {
		rw.Header().Set("Allow", "POST")
		jsonError(rw, 405, "method_not_allowed", "Use POST.")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if r.URL.Path == "/api/billing/database/reconcile" {
		if access.Role != "platform_owner" || w.localAccess {
			jsonError(rw, 403, "forbidden", "Only the operator can reconcile AWS costs.")
			return true
		}
		var body struct {
			Period string `json:"period"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			jsonError(rw, 400, "invalid_request", "Provide only a completed calendar month in period.")
			return true
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			jsonError(rw, 400, "invalid_request", "Provide exactly one request.")
			return true
		}
		if _, _, err := databaseCostPeriod(body.Period, time.Now()); err != nil {
			jsonError(rw, 400, "invalid_request", err.Error())
			return true
		}
		report, err := w.billing.usage.source.Report(ctx, body.Period, time.Now())
		if err == nil {
			err = w.billing.usage.applyReport(ctx, report, time.Now())
		}
		if err != nil {
			log.Printf("database cost reconciliation incomplete (%T)", err)
			jsonError(rw, 503, "database_reconciliation_incomplete", "Reconciliation is incomplete. Some invoices may already exist; retries recover the same ledger operations. Check the invoices before paying.")
		} else {
			builderJSON(rw, 200, map[string]bool{"reconciled": true})
		}
		return true
	}
	if access.Role != "customer_admin" || w.localAccess {
		jsonError(rw, 403, "forbidden", "Only an organization administrator can delete its database.")
		return true
	}
	var body struct {
		Confirmation string `json:"confirmation"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body.Confirmation != "DELETE DATABASE" {
		jsonError(rw, 400, "confirmation_required", "Explicitly confirm DELETE DATABASE; all organization records will be removed.")
		return true
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		jsonError(rw, 400, "invalid_request", "Provide exactly one deletion request.")
		return true
	}
	if w.tenantDatabases() == nil {
		jsonError(rw, 503, "database_unavailable", "Database deletion is not configured.")
		return true
	}
	if err := w.dynamicLock(ctx); err != nil {
		dynamicError(rw, err)
		return true
	}
	defer w.mu.Unlock()
	if err := w.acquireOperation(); err != nil {
		w.operationError(rw, err)
		return true
	}
	started := false
	defer func() {
		if !started {
			w.releaseOperation()
		}
	}()
	unlock, err := w.billing.lock(access.TenantID)
	if err != nil {
		w.billingError(rw, err)
		return true
	}
	defer unlock()
	record, err := w.billing.read(access.TenantID)
	if err != nil {
		w.billingError(rw, err)
		return true
	}
	if record.DatabaseScope != w.billing.usage.scope {
		jsonError(rw, 409, "no_database", "This organization has no managed database in this namespace.")
		return true
	}
	record.DatabaseState, record.DatabaseAuthorized = "deleting", false
	record.DatabaseCheckoutModel, record.DatabaseCheckoutID, record.DatabaseAttemptID = "", "", ""
	if err := w.billing.save(access.TenantID, record); err != nil {
		w.billingError(rw, err)
		return true
	}
	started = true
	go w.deleteUsageDatabase(access.TenantID)
	builderJSON(rw, 202, map[string]bool{"deleting": true})
	return true
}

func (w *deploymentWorker) runDatabaseUsageBilling() {
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := w.reconcileDatabaseUsage(ctx, time.Now().UTC()); err != nil {
			log.Printf("database usage reconciliation incomplete (%T)", err)
		}
	}
	run()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		run()
	}
}

func (w *deploymentWorker) reconcileDatabaseUsage(ctx context.Context, now time.Time) error {
	entries, err := os.ReadDir(w.billing.root)
	if err != nil {
		return err
	}
	current := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	cutoff := current.AddDate(0, -3, 0)
	needed := map[string]bool{}
	type recovery struct{ tenant, period, amount string }
	var recoveries []recovery
	var failures []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		tenant := strings.TrimSuffix(entry.Name(), ".json")
		record, err := w.billing.read(tenant)
		if err != nil {
			return err
		}
		if record.DatabaseState == "deleting" {
			if err := w.acquireOperation(); err == nil {
				go w.deleteUsageDatabase(tenant)
			} else {
				log.Printf("database deletion recovery deferred (%T)", err)
			}
		}
		if record.DatabaseResourceStart == 0 || record.DatabaseScope != w.billing.usage.scope {
			continue
		}
		months, err := w.billing.usage.months(tenant)
		if err != nil {
			return err
		}
		known := map[string]bool{}
		for _, month := range months {
			known[month.Period] = true
			pending := month.PendingAWSCostUSD != ""
			for _, revision := range month.Revisions {
				if revision.CancelRequested || (revision.Status != "paid" && revision.Status != "void" && revision.Status != "credited" && revision.Status != "no_charge" && revision.Status != "adjusted") {
					pending = true
				}
			}
			if pending {
				amount := month.AWSCostUSD
				if month.PendingAWSCostUSD != "" {
					amount = month.PendingAWSCostUSD
				}
				recoveries = append(recoveries, recovery{tenant, month.Period, amount})
			}
		}
		for _, life := range record.DatabaseLifetimes {
			start := time.Unix(life.Start, 0).UTC()
			start = time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)
			for month := start; month.Before(current) && (life.End == 0 || month.Unix() < life.End); month = month.AddDate(0, 1, 0) {
				period := month.Format("2006-01")
				if _, _, err := databaseCostPeriod(period, now); err != nil {
					continue
				}
				// Recent adjustments are checked weekly; old settled months require explicit operator reimport.
				if !known[period] || (!month.Before(cutoff) && now.Weekday() == time.Monday) {
					needed[period] = true
				}
			}
		}
	}
	periods := make([]string, 0, len(needed))
	for period := range needed {
		periods = append(periods, period)
	}
	sort.Slice(periods, func(i, j int) bool {
		recentI, recentJ := periods[i] >= cutoff.Format("2006-01"), periods[j] >= cutoff.Format("2006-01")
		if recentI != recentJ {
			return recentI
		}
		return periods[i] < periods[j]
	})
	processed := map[string]bool{}
	for i, period := range periods {
		if i >= 12 {
			break
		}
		processed[period] = true
		report, err := w.billing.usage.source.Report(ctx, period, now)
		if err == nil {
			err = w.billing.usage.applyReport(ctx, report, now)
		}
		if err != nil {
			log.Printf("database usage month %s reconciliation incomplete (%T)", period, err)
			failures = append(failures, fmt.Errorf("month %s: %w", period, err))
		}
	}
	for _, item := range recoveries {
		if processed[item.period] {
			continue
		}
		unlock, err := w.billing.lock(item.tenant)
		if err != nil {
			log.Printf("database usage stored month %s recovery incomplete (%T)", item.period, err)
			failures = append(failures, err)
			continue
		}
		err = w.billing.usage.applyTenant(ctx, item.tenant, item.period, item.amount)
		unlock()
		if err != nil {
			failures = append(failures, fmt.Errorf("stored month %s: %w", item.period, err))
		}
	}
	return errors.Join(failures...)
}
