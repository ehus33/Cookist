package main

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

var (
	errDynamicNotFound = errors.New("record not found")
	errDynamicConflict = errors.New("record version conflict")
	errDynamicCapacity = errors.New("collection record limit reached")
)

type dynamicRecord struct {
	ID        string         `json:"id"`
	Values    map[string]any `json:"values"`
	Version   int64          `json:"version"`
	CreatedAt string         `json:"createdAt"`
	UpdatedAt string         `json:"updatedAt"`
	Owner     string         `json:"-"`
	CanUpdate bool           `json:"canUpdate"`
	CanDelete bool           `json:"canDelete"`
}

type dynamicStore interface {
	List(context.Context, string, string, string, string, int) ([]dynamicRecord, string, error)
	Get(context.Context, string, string, string) (dynamicRecord, error)
	Create(context.Context, string, string, dynamicRecord) error
	Update(context.Context, string, string, dynamicRecord, int64, string) error
	Delete(context.Context, string, string, string, int64, string) error
	DeleteApp(context.Context, string, []string) error
}

type dynamicDynamoAPI interface {
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
	BatchWriteItem(context.Context, *dynamodb.BatchWriteItemInput, ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error)
}

type dynamoDynamicStore struct {
	client  dynamicDynamoAPI
	table   string
	tenants *tenantDatabaseManager
}

func dynamicPK(app, collection string) string { return "APP#" + app + "#COL#" + collection }

func dynamicKey(app, collection, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: dynamicPK(app, collection)},
		"SK": &types.AttributeValueMemberS{Value: sk},
	}
}

func dynamicItem(app, collection string, r dynamicRecord) (map[string]types.AttributeValue, error) {
	values, err := json.Marshal(r.Values)
	if err != nil {
		return nil, err
	}
	item := dynamicKey(app, collection, "RECORD#"+r.ID)
	item["Values"] = &types.AttributeValueMemberS{Value: string(values)}
	item["Owner"] = &types.AttributeValueMemberS{Value: r.Owner}
	item["Version"] = &types.AttributeValueMemberN{Value: strconv.FormatInt(r.Version, 10)}
	item["CreatedAt"] = &types.AttributeValueMemberS{Value: r.CreatedAt}
	item["UpdatedAt"] = &types.AttributeValueMemberS{Value: r.UpdatedAt}
	return item, nil
}

func dynamicString(item map[string]types.AttributeValue, key string) string {
	v, _ := item[key].(*types.AttributeValueMemberS)
	if v == nil {
		return ""
	}
	return v.Value
}

func dynamicDecodeItem(item map[string]types.AttributeValue) (dynamicRecord, error) {
	r := dynamicRecord{
		ID:    strings.TrimPrefix(dynamicString(item, "SK"), "RECORD#"),
		Owner: dynamicString(item, "Owner"), CreatedAt: dynamicString(item, "CreatedAt"),
		UpdatedAt: dynamicString(item, "UpdatedAt"),
	}
	v, ok := item["Version"].(*types.AttributeValueMemberN)
	if !ok || !idPattern.MatchString(r.ID) || r.Owner == "" {
		return r, errors.New("invalid stored record")
	}
	var err error
	r.Version, err = strconv.ParseInt(v.Value, 10, 64)
	if err != nil || r.Version < 1 {
		return r, errors.New("invalid stored record version")
	}
	dec := json.NewDecoder(strings.NewReader(dynamicString(item, "Values")))
	dec.UseNumber()
	if err := dec.Decode(&r.Values); err != nil || r.Values == nil {
		return r, errors.New("invalid stored record values")
	}
	return r, nil
}

func (s *dynamoDynamicStore) List(ctx context.Context, app, collection, owner, after string, limit int) ([]dynamicRecord, string, error) {
	if s.tenants != nil {
		store, err := s.forTenant(ctx)
		if err != nil {
			return nil, "", err
		}
		return store.List(ctx, app, collection, owner, after, limit)
	}
	values := map[string]types.AttributeValue{
		":pk":     &types.AttributeValueMemberS{Value: dynamicPK(app, collection)},
		":prefix": &types.AttributeValueMemberS{Value: "RECORD#"},
	}
	input := &dynamodb.QueryInput{
		TableName: aws.String(s.table), ConsistentRead: aws.Bool(true), Limit: aws.Int32(int32(limit)),
		KeyConditionExpression:    aws.String("PK = :pk AND begins_with(SK, :prefix)"),
		ExpressionAttributeValues: values,
	}
	if owner != "" {
		input.FilterExpression = aws.String("#owner = :owner")
		input.ExpressionAttributeNames = map[string]string{"#owner": "Owner"}
		values[":owner"] = &types.AttributeValueMemberS{Value: owner}
	}
	if after != "" {
		input.ExclusiveStartKey = dynamicKey(app, collection, "RECORD#"+after)
	}
	out, err := s.client.Query(ctx, input)
	if err != nil {
		return nil, "", err
	}
	records := make([]dynamicRecord, 0, len(out.Items))
	for _, item := range out.Items {
		r, err := dynamicDecodeItem(item)
		if err != nil {
			return nil, "", err
		}
		if owner != "" && r.Owner != owner {
			return nil, "", errors.New("record ownership query failed")
		}
		records = append(records, r)
	}
	next := ""
	if len(out.LastEvaluatedKey) != 0 {
		next = strings.TrimPrefix(dynamicString(out.LastEvaluatedKey, "SK"), "RECORD#")
		if !idPattern.MatchString(next) {
			return nil, "", errors.New("invalid stored pagination key")
		}
	}
	return records, next, nil
}

func (s *dynamoDynamicStore) Get(ctx context.Context, app, collection, id string) (dynamicRecord, error) {
	if s.tenants != nil {
		store, err := s.forTenant(ctx)
		if err != nil {
			return dynamicRecord{}, err
		}
		return store.Get(ctx, app, collection, id)
	}
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.table), Key: dynamicKey(app, collection, "RECORD#"+id), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return dynamicRecord{}, err
	}
	if len(out.Item) == 0 {
		return dynamicRecord{}, errDynamicNotFound
	}
	return dynamicDecodeItem(out.Item)
}

func dynamicTransactionError(err error, create bool) error {
	var canceled *types.TransactionCanceledException
	if errors.As(err, &canceled) {
		for i, reason := range canceled.CancellationReasons {
			if aws.ToString(reason.Code) == "ConditionalCheckFailed" {
				if create && i == 0 {
					return errDynamicCapacity
				}
				return errDynamicConflict
			}
			if aws.ToString(reason.Code) == "TransactionConflict" {
				return errDynamicConflict
			}
		}
	}
	var conditional *types.ConditionalCheckFailedException
	if errors.As(err, &conditional) {
		return errDynamicConflict
	}
	return err
}

func (s *dynamoDynamicStore) Create(ctx context.Context, app, collection string, r dynamicRecord) error {
	if s.tenants != nil {
		store, err := s.forTenant(ctx)
		if err != nil {
			return err
		}
		return store.Create(ctx, app, collection, r)
	}
	item, err := dynamicItem(app, collection, r)
	if err != nil {
		return err
	}
	_, err = s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{Update: &types.Update{
			TableName: aws.String(s.table), Key: dynamicKey(app, collection, "META"),
			UpdateExpression:         aws.String("ADD #count :one"),
			ConditionExpression:      aws.String("attribute_not_exists(#count) OR #count < :max"),
			ExpressionAttributeNames: map[string]string{"#count": "Count"},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":one": &types.AttributeValueMemberN{Value: "1"},
				":max": &types.AttributeValueMemberN{Value: "1000"},
			},
		}},
		{Put: &types.Put{TableName: aws.String(s.table), Item: item, ConditionExpression: aws.String("attribute_not_exists(PK)")}},
	}})
	return dynamicTransactionError(err, true)
}

func dynamicVersionCondition(version int64, owner string) (string, map[string]string, map[string]types.AttributeValue) {
	condition := "#version = :version"
	names := map[string]string{"#version": "Version"}
	values := map[string]types.AttributeValue{":version": &types.AttributeValueMemberN{Value: strconv.FormatInt(version, 10)}}
	if owner != "" {
		condition += " AND #owner = :owner"
		names["#owner"] = "Owner"
		values[":owner"] = &types.AttributeValueMemberS{Value: owner}
	}
	return condition, names, values
}

func (s *dynamoDynamicStore) Update(ctx context.Context, app, collection string, r dynamicRecord, version int64, owner string) error {
	if s.tenants != nil {
		store, err := s.forTenant(ctx)
		if err != nil {
			return err
		}
		return store.Update(ctx, app, collection, r, version, owner)
	}
	item, err := dynamicItem(app, collection, r)
	if err != nil {
		return err
	}
	condition, names, values := dynamicVersionCondition(version, owner)
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.table), Item: item, ConditionExpression: aws.String(condition),
		ExpressionAttributeNames: names, ExpressionAttributeValues: values,
	})
	return dynamicTransactionError(err, false)
}

func (s *dynamoDynamicStore) Delete(ctx context.Context, app, collection, id string, version int64, owner string) error {
	if s.tenants != nil {
		store, err := s.forTenant(ctx)
		if err != nil {
			return err
		}
		return store.Delete(ctx, app, collection, id, version, owner)
	}
	condition, names, values := dynamicVersionCondition(version, owner)
	_, err := s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{Update: &types.Update{
			TableName: aws.String(s.table), Key: dynamicKey(app, collection, "META"),
			UpdateExpression:         aws.String("ADD #count :minus"),
			ConditionExpression:      aws.String("#count > :zero"),
			ExpressionAttributeNames: map[string]string{"#count": "Count"},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":minus": &types.AttributeValueMemberN{Value: "-1"}, ":zero": &types.AttributeValueMemberN{Value: "0"},
			},
		}},
		{Delete: &types.Delete{
			TableName: aws.String(s.table), Key: dynamicKey(app, collection, "RECORD#"+id),
			ConditionExpression: aws.String(condition), ExpressionAttributeNames: names, ExpressionAttributeValues: values,
		}},
	}})
	return dynamicTransactionError(err, false)
}

// The caller must stop visitor/admin writes before cleanup. Queries are restricted
// to the immutable saved collection partitions; no table scan is performed.
func (s *dynamoDynamicStore) DeleteApp(ctx context.Context, app string, collections []string) error {
	if s.tenants != nil {
		tenant, _ := ctx.Value(databaseTenantContext{}).(string)
		if tenant != "owner" {
			record, err := s.tenants.billing.read(tenant)
			if err != nil {
				return err
			}
			if record.DatabaseScope == s.tenants.scope && record.DatabaseState == "deleted" {
				return nil
			}
			if record.DatabaseScope == s.tenants.scope && (record.DatabaseState == "provisioning" || record.DatabaseState == "deleting") {
				name, err := s.tenants.tableName(tenant)
				if err != nil {
					return err
				}
				out, err := s.tenants.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)})
				var missing *types.ResourceNotFoundException
				if errors.As(err, &missing) {
					return nil
				}
				if err != nil {
					return err
				}
				if err := s.tenants.verifyTable(ctx, tenant, out.Table); err != nil {
					return err
				}
				store := &dynamoDynamicStore{client: s.client, table: name}
				return store.DeleteApp(ctx, app, collections)
			}
		}
		store, err := s.forTenant(ctx)
		if err != nil {
			return err
		}
		return store.DeleteApp(ctx, app, collections)
	}
	for _, collection := range collections {
		after := ""
		for page := 0; ; page++ {
			if page >= 40 {
				return errors.New("dynamic cleanup pagination exceeded its bound")
			}
			input := &dynamodb.QueryInput{
				TableName: aws.String(s.table), ConsistentRead: aws.Bool(true), Limit: aws.Int32(50),
				KeyConditionExpression: aws.String("PK = :pk AND begins_with(SK, :prefix)"),
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":pk":     &types.AttributeValueMemberS{Value: dynamicPK(app, collection)},
					":prefix": &types.AttributeValueMemberS{Value: "RECORD#"},
				},
				ProjectionExpression: aws.String("PK, SK"),
			}
			if after != "" {
				input.ExclusiveStartKey = dynamicKey(app, collection, "RECORD#"+after)
			}
			out, err := s.client.Query(ctx, input)
			if err != nil {
				return err
			}
			writes := make([]types.WriteRequest, 0, len(out.Items))
			for _, item := range out.Items {
				id := strings.TrimPrefix(dynamicString(item, "SK"), "RECORD#")
				if !idPattern.MatchString(id) || dynamicString(item, "PK") != dynamicPK(app, collection) {
					return errors.New("invalid dynamic cleanup key")
				}
				writes = append(writes, types.WriteRequest{DeleteRequest: &types.DeleteRequest{Key: dynamicKey(app, collection, "RECORD#"+id)}})
			}
			for start := 0; start < len(writes); start += 25 {
				end := start + 25
				if end > len(writes) {
					end = len(writes)
				}
				if err := s.cleanupBatch(ctx, writes[start:end]); err != nil {
					return err
				}
			}
			if len(out.LastEvaluatedKey) == 0 {
				break
			}
			next := strings.TrimPrefix(dynamicString(out.LastEvaluatedKey, "SK"), "RECORD#")
			if !idPattern.MatchString(next) || next == after {
				return errors.New("invalid dynamic cleanup cursor")
			}
			after = next
		}
		if err := s.cleanupBatch(ctx, []types.WriteRequest{{DeleteRequest: &types.DeleteRequest{Key: dynamicKey(app, collection, "META")}}}); err != nil {
			return err
		}
	}
	return nil
}

func (s *dynamoDynamicStore) cleanupBatch(ctx context.Context, writes []types.WriteRequest) error {
	pending := map[string][]types.WriteRequest{s.table: writes}
	for attempt := 0; attempt < 5; attempt++ {
		out, err := s.client.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{RequestItems: pending})
		if err != nil {
			return err
		}
		pending = out.UnprocessedItems
		if len(pending[s.table]) == 0 {
			return nil
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("dynamic cleanup throttled; retry cleanup")
}

func dynamicStoreFailure(err error) error {
	switch {
	case errors.Is(err, errDynamicNotFound):
		return &deploymentError{404, "not_found", "Record not found."}
	case errors.Is(err, errDynamicConflict):
		return &deploymentError{409, "version_conflict", "Record changed. Reload and retry."}
	case errors.Is(err, errDynamicCapacity):
		return &deploymentError{409, "collection_full", "A collection can contain at most 1000 records."}
	default:
		return &deploymentError{503, "dynamic_unavailable", "Dynamic storage unavailable. Retry later."}
	}
}
