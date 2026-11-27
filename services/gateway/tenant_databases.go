package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type databaseTenantContext struct{}

func withDatabaseTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, databaseTenantContext{}, tenant)
}

type tenantTableAPI interface {
	DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
	ListTagsOfResource(context.Context, *dynamodb.ListTagsOfResourceInput, ...func(*dynamodb.Options)) (*dynamodb.ListTagsOfResourceOutput, error)
	CreateTable(context.Context, *dynamodb.CreateTableInput, ...func(*dynamodb.Options)) (*dynamodb.CreateTableOutput, error)
	UpdateTable(context.Context, *dynamodb.UpdateTableInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateTableOutput, error)
	UpdateContinuousBackups(context.Context, *dynamodb.UpdateContinuousBackupsInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateContinuousBackupsOutput, error)
	DeleteTable(context.Context, *dynamodb.DeleteTableInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteTableOutput, error)
}

type tenantDatabaseManager struct {
	client                 tenantTableAPI
	billing                *stripeBilling
	scope, region, account string
}

func (m *tenantDatabaseManager) tableName(tenant string) (string, error) {
	if !tenantPattern.MatchString(tenant) || tenant == "owner" {
		return "", errors.New("invalid database tenant")
	}
	sum := sha256.Sum256([]byte(tenant))
	return fmt.Sprintf("%s-tenant-%x", m.scope, sum[:14]), nil
}

func (s *dynamoDynamicStore) forTenant(ctx context.Context) (*dynamoDynamicStore, error) {
	tenant, _ := ctx.Value(databaseTenantContext{}).(string)
	if tenant == "owner" {
		return &dynamoDynamicStore{client: s.client, table: s.table}, nil
	}
	name, err := s.tenants.tableName(tenant)
	if err != nil {
		return nil, err
	}
	record, err := s.tenants.billing.read(tenant)
	if err != nil {
		return nil, err
	}
	if record.DatabaseScope != s.tenants.scope || record.DatabaseState != "retained" {
		return nil, &deploymentError{409, "database_unavailable", "The organization's database is not available. Check database setup or deletion status."}
	}
	return &dynamoDynamicStore{client: s.client, table: name}, nil
}

func (m *tenantDatabaseManager) verifyTableIdentity(tenant string, table *types.TableDescription) error {
	name, err := m.tableName(tenant)
	if err != nil {
		return err
	}
	if table == nil {
		return errors.New("AWS returned no organization database")
	}
	resource, parseErr := arn.Parse(aws.ToString(table.TableArn))
	if parseErr != nil || aws.ToString(table.TableName) != name || resource.Service != "dynamodb" ||
		resource.Region != m.region || resource.AccountID != m.account || resource.Resource != "table/"+name {
		return errors.New("organization database identity does not match its AWS account")
	}
	if table.CreationDateTime == nil {
		return errors.New("organization database has no creation time")
	}
	keys := map[string]types.KeyType{}
	attributes := map[string]types.ScalarAttributeType{}
	for _, key := range table.KeySchema {
		keys[aws.ToString(key.AttributeName)] = key.KeyType
	}
	for _, attribute := range table.AttributeDefinitions {
		attributes[aws.ToString(attribute.AttributeName)] = attribute.AttributeType
	}
	if len(keys) != 2 || keys["PK"] != types.KeyTypeHash || keys["SK"] != types.KeyTypeRange ||
		attributes["PK"] != types.ScalarAttributeTypeS || attributes["SK"] != types.ScalarAttributeTypeS {
		return errors.New("organization database schema does not match")
	}
	return nil
}

func (m *tenantDatabaseManager) verifyTable(ctx context.Context, tenant string, table *types.TableDescription) error {
	if err := m.verifyTableIdentity(tenant, table); err != nil {
		return err
	}
	tags, err := m.client.ListTagsOfResource(ctx, &dynamodb.ListTagsOfResourceInput{ResourceArn: table.TableArn})
	if err != nil {
		return err
	}
	found := map[string]string{}
	for _, tag := range tags.Tags {
		found[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	if found[databaseTenantTag] != tenant || found[databaseScopeTag] != m.scope {
		return errors.New("organization database cost allocation tags do not match")
	}
	return nil
}

func (m *tenantDatabaseManager) ensure(ctx context.Context, tenant string) error {
	if tenant == "owner" {
		return nil
	}
	name, err := m.tableName(tenant)
	if err != nil {
		return err
	}
	if err := m.billing.usage.source.Ready(ctx); err != nil {
		return err
	}
	unlock, err := m.billing.lock(tenant)
	if err != nil {
		return err
	}
	defer unlock()
	record, err := m.billing.read(tenant)
	if err != nil {
		return err
	}
	if !record.DatabaseAuthorized || record.DatabasePaymentFailed || record.DatabaseState == "deleting" || record.DatabaseState == "deleted" {
		return &deploymentError{403, "database_subscription_required", "Authorize database usage billing before publishing."}
	}
	if record.DatabaseScope != "" && record.DatabaseScope != m.scope {
		return errors.New("database scope changed; migration is required")
	}
	previousState := record.DatabaseState
	record.DatabaseScope = m.scope
	if previousState != "retained" {
		record.DatabaseState = "provisioning"
	}
	if err := m.billing.save(tenant, record); err != nil {
		return err
	}
	out, err := m.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)})
	var missing *types.ResourceNotFoundException
	if errors.As(err, &missing) {
		if previousState == "retained" || (len(record.DatabaseLifetimes) > 0 && record.DatabaseLifetimes[len(record.DatabaseLifetimes)-1].End == 0) {
			return errors.New("an existing organization database is missing; operator recovery is required")
		}
		_, err = m.client.CreateTable(ctx, &dynamodb.CreateTableInput{
			TableName: aws.String(name), BillingMode: types.BillingModePayPerRequest,
			AttributeDefinitions:      []types.AttributeDefinition{{AttributeName: aws.String("PK"), AttributeType: types.ScalarAttributeTypeS}, {AttributeName: aws.String("SK"), AttributeType: types.ScalarAttributeTypeS}},
			KeySchema:                 []types.KeySchemaElement{{AttributeName: aws.String("PK"), KeyType: types.KeyTypeHash}, {AttributeName: aws.String("SK"), KeyType: types.KeyTypeRange}},
			DeletionProtectionEnabled: aws.Bool(true),
			OnDemandThroughput:        &types.OnDemandThroughput{MaxReadRequestUnits: aws.Int64(100), MaxWriteRequestUnits: aws.Int64(25)},
			Tags:                      []types.Tag{{Key: aws.String(databaseTenantTag), Value: aws.String(tenant)}, {Key: aws.String(databaseScopeTag), Value: aws.String(m.scope)}},
		})
	}
	if err != nil {
		return err
	}
	waiter := dynamodb.NewTableExistsWaiter(m.client)
	if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)}, 2*time.Minute); err != nil {
		return err
	}
	out, err = m.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)})
	if err != nil {
		return err
	}
	if err := m.verifyTableIdentity(tenant, out.Table); err != nil {
		return err
	}
	if out.Table.CreationDateTime.Unix() < record.DatabaseConsentAt {
		return errors.New("organization database predates billing authorization; operator recovery is required")
	}
	if record.DatabaseResourceStart == 0 {
		record.DatabaseResourceStart = out.Table.CreationDateTime.Unix()
	}
	if len(record.DatabaseLifetimes) == 0 || record.DatabaseLifetimes[len(record.DatabaseLifetimes)-1].End != 0 {
		record.DatabaseLifetimes = append(record.DatabaseLifetimes, databaseLifetime{Start: out.Table.CreationDateTime.Unix()})
	}
	if err := m.billing.save(tenant, record); err != nil {
		return err
	}
	if err := m.verifyTable(ctx, tenant, out.Table); err != nil {
		return err
	}
	_, err = m.client.UpdateContinuousBackups(ctx, &dynamodb.UpdateContinuousBackupsInput{
		TableName: aws.String(name), PointInTimeRecoverySpecification: &types.PointInTimeRecoverySpecification{PointInTimeRecoveryEnabled: aws.Bool(true)},
	})
	if err != nil {
		return err
	}
	record.DatabaseState = "retained"
	return m.billing.save(tenant, record)
}

func (m *tenantDatabaseManager) destroy(ctx context.Context, tenant string) error {
	name, err := m.tableName(tenant)
	if err != nil {
		return err
	}
	out, err := m.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)})
	var missing *types.ResourceNotFoundException
	if errors.As(err, &missing) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := m.verifyTable(ctx, tenant, out.Table); err != nil {
		return err
	}
	if out.Table.TableStatus != types.TableStatusDeleting {
		_, err = m.client.UpdateTable(ctx, &dynamodb.UpdateTableInput{TableName: aws.String(name), DeletionProtectionEnabled: aws.Bool(false)})
		if err != nil {
			return err
		}
		if err := dynamodb.NewTableExistsWaiter(m.client).Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)}, 2*time.Minute); err != nil {
			return err
		}
		_, err = m.client.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(name)})
		if err != nil {
			return err
		}
	}
	return dynamodb.NewTableNotExistsWaiter(m.client).Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)}, 10*time.Minute)
}

type databaseLifetime struct {
	Start int64 `json:"start"`
	End   int64 `json:"end,omitempty"`
}
