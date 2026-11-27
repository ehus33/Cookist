package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type dynamicDynamoFixture struct {
	query    func(*dynamodb.QueryInput) (*dynamodb.QueryOutput, error)
	get      func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error)
	put      func(*dynamodb.PutItemInput) (*dynamodb.PutItemOutput, error)
	transact func(*dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error)
	batch    func(*dynamodb.BatchWriteItemInput) (*dynamodb.BatchWriteItemOutput, error)
}

func (f *dynamicDynamoFixture) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return f.query(in)
}
func (f *dynamicDynamoFixture) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return f.get(in)
}
func (f *dynamicDynamoFixture) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return f.put(in)
}
func (f *dynamicDynamoFixture) TransactWriteItems(_ context.Context, in *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	return f.transact(in)
}
func (f *dynamicDynamoFixture) BatchWriteItem(_ context.Context, in *dynamodb.BatchWriteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error) {
	return f.batch(in)
}

func TestDynamicDynamoAtomicCounterVersionAndOwnership(t *testing.T) {
	app := strings.Repeat("a", 32)
	record := dynamicRecord{ID: strings.Repeat("b", 32), Owner: "visitor-one", Version: 1,
		Values: map[string]any{"title": "Note"}, CreatedAt: "2026-11-27T18:00:00Z", UpdatedAt: "2026-11-27T18:00:00Z"}
	client := &dynamicDynamoFixture{}
	store := &dynamoDynamicStore{client: client, table: "dynamic-records"}
	client.transact = func(in *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
		if len(in.TransactItems) != 2 {
			t.Fatal("create must atomically write the record and collection counter")
		}
		counter, put := in.TransactItems[0].Update, in.TransactItems[1].Put
		if counter == nil || put == nil || dynamicString(counter.Key, "PK") != dynamicPK(app, "notes") ||
			dynamicString(counter.Key, "SK") != "META" || aws.ToString(counter.ConditionExpression) != "attribute_not_exists(#count) OR #count < :max" ||
			counter.ExpressionAttributeValues[":max"].(*types.AttributeValueMemberN).Value != "1000" ||
			aws.ToString(counter.UpdateExpression) != "ADD #count :one" ||
			dynamicString(put.Item, "PK") != dynamicPK(app, "notes") || dynamicString(put.Item, "Owner") != "visitor-one" ||
			aws.ToString(put.ConditionExpression) != "attribute_not_exists(PK)" {
			t.Fatal("create transaction scope/capacity/ownership conditions are missing")
		}
		return &dynamodb.TransactWriteItemsOutput{}, nil
	}
	if err := store.Create(context.Background(), app, "notes", record); err != nil {
		t.Fatal(err)
	}
	client.transact = func(*dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
		return nil, &types.TransactionCanceledException{CancellationReasons: []types.CancellationReason{
			{Code: aws.String("ConditionalCheckFailed")}, {Code: aws.String("None")},
		}}
	}
	if err := store.Create(context.Background(), app, "notes", record); !errors.Is(err, errDynamicCapacity) {
		t.Fatalf("capacity condition failure must be explicit: %v", err)
	}
	client.put = func(in *dynamodb.PutItemInput) (*dynamodb.PutItemOutput, error) {
		if aws.ToString(in.ConditionExpression) != "#version = :version AND #owner = :owner" ||
			in.ExpressionAttributeValues[":version"].(*types.AttributeValueMemberN).Value != "1" ||
			in.ExpressionAttributeValues[":owner"].(*types.AttributeValueMemberS).Value != "visitor-one" ||
			dynamicString(in.Item, "PK") != dynamicPK(app, "notes") ||
			in.Item["Version"].(*types.AttributeValueMemberN).Value != "2" {
			t.Fatal("update lacks atomic version/ownership checks")
		}
		return nil, &types.ConditionalCheckFailedException{}
	}
	record.Version = 2
	if err := store.Update(context.Background(), app, "notes", record, 1, "visitor-one"); !errors.Is(err, errDynamicConflict) {
		t.Fatalf("stale version must conflict: %v", err)
	}
	client.transact = func(in *dynamodb.TransactWriteItemsInput) (*dynamodb.TransactWriteItemsOutput, error) {
		counter, del := in.TransactItems[0].Update, in.TransactItems[1].Delete
		if counter == nil || del == nil || aws.ToString(counter.ConditionExpression) != "#count > :zero" ||
			counter.ExpressionAttributeValues[":minus"].(*types.AttributeValueMemberN).Value != "-1" ||
			aws.ToString(del.ConditionExpression) != "#version = :version AND #owner = :owner" ||
			dynamicString(del.Key, "SK") != "RECORD#"+record.ID || dynamicString(del.Key, "PK") != dynamicPK(app, "notes") {
			t.Fatal("delete must atomically decrement capacity and check ownership/version")
		}
		return &dynamodb.TransactWriteItemsOutput{}, nil
	}
	if err := store.Delete(context.Background(), app, "notes", record.ID, 2, "visitor-one"); err != nil {
		t.Fatal(err)
	}
}

func TestDynamicDynamoScopedQueriesAndCleanup(t *testing.T) {
	app := strings.Repeat("a", 32)
	id := strings.Repeat("b", 32)
	record := dynamicRecord{ID: id, Owner: "visitor-one", Version: 1, Values: map[string]any{"title": "Note"}}
	item, err := dynamicItem(app, "notes", record)
	if err != nil {
		t.Fatal(err)
	}
	client := &dynamicDynamoFixture{}
	store := &dynamoDynamicStore{client: client, table: "dynamic-records"}
	client.query = func(in *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
		if aws.ToString(in.TableName) != store.table || !aws.ToBool(in.ConsistentRead) || aws.ToInt32(in.Limit) != 50 ||
			aws.ToString(in.KeyConditionExpression) != "PK = :pk AND begins_with(SK, :prefix)" ||
			dynamicString(in.ExpressionAttributeValues, ":pk") != dynamicPK(app, "notes") ||
			aws.ToString(in.FilterExpression) != "#owner = :owner" || dynamicString(in.ExpressionAttributeValues, ":owner") != "visitor-one" ||
			dynamicString(in.ExclusiveStartKey, "PK") != dynamicPK(app, "notes") ||
			dynamicString(in.ExclusiveStartKey, "SK") != "RECORD#"+id {
			t.Fatal("own query is not bounded and scoped")
		}
		return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{item}}, nil
	}
	records, next, err := store.List(context.Background(), app, "notes", "visitor-one", id, 50)
	if err != nil || next != "" || len(records) != 1 || records[0].ID != id {
		t.Fatalf("query failed: %+v %s %v", records, next, err)
	}
	client.get = func(in *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
		if !aws.ToBool(in.ConsistentRead) || dynamicString(in.Key, "PK") != dynamicPK(app, "notes") ||
			dynamicString(in.Key, "SK") != "RECORD#"+id {
			t.Fatal("individual reads are not app scoped")
		}
		return &dynamodb.GetItemOutput{}, nil
	}
	if _, err := store.Get(context.Background(), app, "notes", id); !errors.Is(err, errDynamicNotFound) {
		t.Fatal("missing record not mapped to not found")
	}

	// Cleanup uses indexed, bounded partitions and retries unprocessed writes.
	client.query = func(in *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
		if aws.ToInt32(in.Limit) != 50 || aws.ToString(in.ProjectionExpression) != "PK, SK" ||
			dynamicString(in.ExpressionAttributeValues, ":pk") != dynamicPK(app, "notes") {
			t.Fatal("cleanup query unbounded or cross-app")
		}
		return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{dynamicKey(app, "notes", "RECORD#"+id)}}, nil
	}
	calls := 0
	client.batch = func(in *dynamodb.BatchWriteItemInput) (*dynamodb.BatchWriteItemOutput, error) {
		calls++
		for _, write := range in.RequestItems[store.table] {
			if write.DeleteRequest == nil || dynamicString(write.DeleteRequest.Key, "PK") != dynamicPK(app, "notes") {
				t.Fatal("cleanup deletes an unrelated key")
			}
		}
		if calls == 1 {
			return &dynamodb.BatchWriteItemOutput{UnprocessedItems: in.RequestItems}, nil
		}
		return &dynamodb.BatchWriteItemOutput{}, nil
	}
	if err := store.DeleteApp(context.Background(), app, []string{"notes"}); err != nil || calls != 3 {
		t.Fatalf("cleanup retry/counter deletion failed: %v calls=%d", err, calls)
	}
}

func TestDynamicFactoryAllOrNone(t *testing.T) {
	if service, err := newDynamicService(context.Background(), "us-west-2", "", "", ""); service != nil || err != nil {
		t.Fatal("unconfigured service must remain disabled")
	}
	for _, input := range [][4]string{
		{"", "records", "us-west-2_pool", "client"},
		{"us-west-2", "records", "", "client"},
		{"us-west-2", "", "us-west-2_pool", "client"},
		{"us-west-2", "records", "us-west-2_pool", ""},
		{"us-west-2", "records", "us-east-1_pool", "client"},
		{"us-west-2", "records", "us-west-2_../../pool", "client"},
		{"us-west-2", "records", "us-west-2_pool", "secret/client"},
	} {
		if _, err := newDynamicService(context.Background(), input[0], input[1], input[2], input[3]); err == nil {
			t.Fatalf("partial or invalid configuration accepted: %v", input)
		}
	}
}
