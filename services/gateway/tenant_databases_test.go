package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dt "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type tenantAPIFixture struct {
	dynamicDynamoAPI
	mu               sync.Mutex
	tables           map[string]*dt.TableDescription
	tags             map[string][]dt.Tag
	creates, deletes int
	backupErr        error
	tagsErr          error
	queries          []string
}

func (f *tenantAPIFixture) DescribeTable(_ context.Context, in *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	table := f.tables[aws.ToString(in.TableName)]
	if table == nil {
		return nil, &dt.ResourceNotFoundException{}
	}
	copy := *table
	return &dynamodb.DescribeTableOutput{Table: &copy}, nil
}
func (f *tenantAPIFixture) ListTagsOfResource(_ context.Context, in *dynamodb.ListTagsOfResourceInput, _ ...func(*dynamodb.Options)) (*dynamodb.ListTagsOfResourceOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tagsErr != nil {
		return nil, f.tagsErr
	}
	return &dynamodb.ListTagsOfResourceOutput{Tags: f.tags[aws.ToString(in.ResourceArn)]}, nil
}
func (f *tenantAPIFixture) CreateTable(_ context.Context, in *dynamodb.CreateTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.CreateTableOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	name := aws.ToString(in.TableName)
	if in.BillingMode != dt.BillingModePayPerRequest || !aws.ToBool(in.DeletionProtectionEnabled) ||
		in.SSESpecification != nil || in.OnDemandThroughput == nil ||
		aws.ToInt64(in.OnDemandThroughput.MaxReadRequestUnits) != 100 || aws.ToInt64(in.OnDemandThroughput.MaxWriteRequestUnits) != 25 {
		return nil, errors.New("unbounded, unprotected or separately keyed tenant table")
	}
	created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	table := &dt.TableDescription{TableName: in.TableName, TableArn: aws.String("arn:aws:dynamodb:us-west-2:123456789012:table/" + name),
		TableStatus: dt.TableStatusActive, CreationDateTime: &created, KeySchema: in.KeySchema, AttributeDefinitions: in.AttributeDefinitions, DeletionProtectionEnabled: in.DeletionProtectionEnabled}
	f.tables[name], f.tags[aws.ToString(table.TableArn)] = table, in.Tags
	return &dynamodb.CreateTableOutput{TableDescription: table}, nil
}
func (f *tenantAPIFixture) UpdateTable(_ context.Context, in *dynamodb.UpdateTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateTableOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	table := f.tables[aws.ToString(in.TableName)]
	if table == nil {
		return nil, &dt.ResourceNotFoundException{}
	}
	table.DeletionProtectionEnabled = in.DeletionProtectionEnabled
	return &dynamodb.UpdateTableOutput{TableDescription: table}, nil
}
func (f *tenantAPIFixture) UpdateContinuousBackups(_ context.Context, in *dynamodb.UpdateContinuousBackupsInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateContinuousBackupsOutput, error) {
	if in.PointInTimeRecoverySpecification == nil || !aws.ToBool(in.PointInTimeRecoverySpecification.PointInTimeRecoveryEnabled) {
		return nil, errors.New("PITR missing")
	}
	return &dynamodb.UpdateContinuousBackupsOutput{}, f.backupErr
}
func (f *tenantAPIFixture) DeleteTable(_ context.Context, in *dynamodb.DeleteTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteTableOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.TableName)
	table := f.tables[name]
	if table == nil {
		return nil, &dt.ResourceNotFoundException{}
	}
	if aws.ToBool(table.DeletionProtectionEnabled) {
		return nil, errors.New("deletion protection still active")
	}
	f.deletes++
	delete(f.tables, name)
	return &dynamodb.DeleteTableOutput{TableDescription: table}, nil
}
func (f *tenantAPIFixture) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.queries = append(f.queries, aws.ToString(in.TableName))
	return &dynamodb.QueryOutput{}, nil
}
func (f *tenantAPIFixture) BatchWriteItem(context.Context, *dynamodb.BatchWriteItemInput, ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error) {
	return &dynamodb.BatchWriteItemOutput{}, nil
}

func tenantManagerFixture(t *testing.T) (*deploymentWorker, *tenantAPIFixture, *tenantDatabaseManager) {
	t.Helper()
	w, _ := usageBillingWorker(t)
	f := &tenantAPIFixture{tables: map[string]*dt.TableDescription{}, tags: map[string][]dt.Tag{}}
	m := &tenantDatabaseManager{client: f, billing: w.billing, scope: w.billing.usage.scope, region: "us-west-2", account: "123456789012"}
	w.dynamic.store = &dynamoDynamicStore{client: f, table: "operator-records", tenants: m}
	for _, tenant := range []string{"alpha", "beta"} {
		consentUsageFixture(t, w, tenant)
		record, err := w.billing.read(tenant)
		if err != nil {
			t.Fatal(err)
		}
		record.DatabaseState, record.DatabaseResourceStart, record.DatabaseLifetimes = "not_created", 0, nil
		if err := w.billing.save(tenant, record); err != nil {
			t.Fatal(err)
		}
	}
	return w, f, m
}

func TestDedicatedDatabasesRouteByDeploymentTenantAndRecoverPartialProvisioning(t *testing.T) {
	w, f, m := tenantManagerFixture(t)
	f.backupErr = errors.New("backup unavailable")
	if err := m.ensure(context.Background(), "alpha"); err == nil {
		t.Fatal("failed PITR reported success")
	}
	record, _ := w.billing.read("alpha")
	if record.DatabaseState != "provisioning" || record.DatabaseResourceStart == 0 || len(record.DatabaseLifetimes) != 1 {
		t.Fatal("incurred costs lost after partial provisioning")
	}
	f.backupErr = nil
	if err := m.ensure(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := m.ensure(context.Background(), "beta"); err != nil {
		t.Fatal(err)
	}
	if f.creates != 2 {
		t.Fatal("retry created a duplicate table")
	}
	store := w.dynamic.store.(*dynamoDynamicStore)
	for _, tenant := range []string{"alpha", "beta", "owner"} {
		scoped, err := store.forTenant(withDatabaseTenant(context.Background(), tenant))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := scoped.List(context.Background(), "app", "collection", "", "", 20); err != nil {
			t.Fatal(err)
		}
	}
	alpha, _ := m.tableName("alpha")
	beta, _ := m.tableName("beta")
	if len(f.queries) != 3 || f.queries[0] != alpha || f.queries[1] != beta || f.queries[2] != "operator-records" || alpha == beta {
		t.Fatal("tenant data used a shared/wrong table")
	}
	if _, err := store.forTenant(context.Background()); err == nil {
		t.Fatal("missing tenant silently fell back to shared data")
	}
	delete(f.tables, alpha)
	if err := m.ensure(context.Background(), "alpha"); err == nil || f.creates != 2 {
		t.Fatal("missing existing database was silently recreated")
	}
}

func TestTenantDeletionVerifiesIdentityAndKeepsOtherOrganization(t *testing.T) {
	w, f, m := tenantManagerFixture(t)
	for _, tenant := range []string{"alpha", "beta"} {
		if err := m.ensure(context.Background(), tenant); err != nil {
			t.Fatal(err)
		}
	}
	alpha, _ := m.tableName("alpha")
	beta, _ := m.tableName("beta")
	arn := aws.ToString(f.tables[alpha].TableArn)
	f.tags[arn][0].Value = aws.String("beta")
	if err := m.destroy(context.Background(), "alpha"); err == nil || f.deletes != 0 {
		t.Fatal("wrong tenant tags allowed deletion")
	}
	f.tags[arn][0].Value = aws.String("alpha")
	dynamicWantStatus(t, billingRequest(w, "database/delete", "viewer", `{"confirmation":"DELETE DATABASE"}`), 403)
	dynamicWantStatus(t, billingRequest(w, "database/delete", "deployer", `{"confirmation":"DELETE DATABASE"}`), 403)
	for _, body := range []string{`{}`, `{"confirmation":"DELETE DATABASE","tenant":"beta"}`, `{"confirmation":"DELETE DATABASE"} {}`} {
		dynamicWantStatus(t, billingRequest(w, "database/delete", "admin", body), 400)
	}
	record, _ := w.billing.read("alpha")
	record.DatabaseState, record.DatabaseAuthorized = "deleting", false
	record.DatabaseCheckoutModel, record.DatabaseCheckoutID = "", ""
	if err := w.billing.save("alpha", record); err != nil {
		t.Fatal(err)
	}
	if err := w.acquireOperation(); err != nil {
		t.Fatal(err)
	}
	w.deleteUsageDatabase("alpha")
	if f.deletes != 1 || f.tables[alpha] != nil || f.tables[beta] == nil {
		t.Fatal("deletion affected another organization's database")
	}
	record, _ = w.billing.read("alpha")
	if record.DatabaseState != "deleted" || record.DatabaseAuthorized || record.DatabaseLifetimes[0].End == 0 || record.DatabaseConsentAt == 0 {
		t.Fatal("deletion lost cost history or retained access")
	}
	if err := w.billing.usage.refreshTenant(context.Background(), "alpha", &record); err != nil || record.DatabaseAuthorized {
		t.Fatal("late setup event resurrected deleted database")
	}
	if err := m.destroy(context.Background(), "owner"); err == nil {
		t.Fatal("operator shared table deletion was allowed")
	}
}

func TestFailedTenantProvisioningCleanupDoesNotCreateTablesOrTrustWrongResources(t *testing.T) {
	w, f, m := tenantManagerFixture(t)
	record, _ := w.billing.read("alpha")
	record.DatabaseState, record.DatabaseScope = "provisioning", m.scope
	if err := w.billing.save("alpha", record); err != nil {
		t.Fatal(err)
	}
	store := w.dynamic.store.(*dynamoDynamicStore)
	ctx := withDatabaseTenant(context.Background(), "alpha")
	if err := store.DeleteApp(ctx, "app", []string{"collection"}); err != nil || f.creates != 0 {
		t.Fatal("missing partial table blocked cleanup or was created")
	}
	f.backupErr = errors.New("backup failure")
	if err := m.ensure(context.Background(), "alpha"); err == nil {
		t.Fatal("expected partial provisioning")
	}
	if err := store.DeleteApp(ctx, "app", []string{"collection"}); err != nil || f.creates != 1 {
		t.Fatalf("partial table cleanup failed: %v", err)
	}
	name, _ := m.tableName("alpha")
	f.tables[name].TableArn = aws.String("arn:aws:dynamodb:us-west-2:999999999999:table/" + name)
	if err := store.DeleteApp(ctx, "app", []string{"collection"}); err == nil {
		t.Fatal("cleanup trusted a table from another account")
	}
}

func TestTenantCreationRetainsIncurredCostHistoryWhenTagVerificationFails(t *testing.T) {
	w, f, m := tenantManagerFixture(t)
	f.tagsErr = errors.New("tag verification temporarily unavailable")
	if err := m.ensure(context.Background(), "alpha"); err == nil {
		t.Fatal("tag failure was ignored")
	}
	record, err := w.billing.read("alpha")
	if err != nil || record.DatabaseResourceStart == 0 || len(record.DatabaseLifetimes) != 1 || record.DatabaseState != "provisioning" {
		t.Fatal("created AWS resource cost history was discarded")
	}
	f.tagsErr = nil
	if err := m.ensure(context.Background(), "alpha"); err != nil || f.creates != 1 {
		t.Fatalf("tag recovery recreated resource: %v", err)
	}
}
