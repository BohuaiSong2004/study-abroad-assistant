package ddb_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/testutil"
)

// counting wraps an API and counts reads that reach DynamoDB.
type counting struct {
	ddb.API
	gets, batch, queries atomic.Int64
	// beforeGetReturns, if set, runs after the underlying read completes but
	// before the result is returned to the cache (to inject races).
	beforeGetReturns func()
}

func (c *counting) GetItem(ctx context.Context, in *dynamodb.GetItemInput, o ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	c.gets.Add(1)
	out, err := c.API.GetItem(ctx, in, o...)
	if f := c.beforeGetReturns; f != nil {
		f()
	}
	return out, err
}

func (c *counting) BatchGetItem(ctx context.Context, in *dynamodb.BatchGetItemInput, o ...func(*dynamodb.Options)) (*dynamodb.BatchGetItemOutput, error) {
	c.batch.Add(1)
	return c.API.BatchGetItem(ctx, in, o...)
}

func (c *counting) Query(ctx context.Context, in *dynamodb.QueryInput, o ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	c.queries.Add(1)
	return c.API.Query(ctx, in, o...)
}

func setup(t *testing.T) (*ddb.CachedAPI, *counting, string) {
	t.Helper()
	c, prefix := testutil.Tables(t, ddb.CacheOff)
	cnt := &counting{API: c.Raw}
	return ddb.NewCachedAPI(cnt, ddb.CacheOptions{ItemTTL: time.Minute, QueryTTL: time.Minute, NegativeTTL: 200 * time.Millisecond}), cnt, prefix + ddb.TableCatalog
}

func put(t *testing.T, api ddb.API, table, pk, val string) {
	t.Helper()
	item := ddb.Key(pk, "META")
	item["v"] = ddb.S(val)
	item[ddb.GSI1PK] = ddb.S("ALL")
	item[ddb.GSI1SK] = ddb.S(pk)
	if _, err := api.PutItem(context.Background(), &dynamodb.PutItemInput{TableName: aws.String(table), Item: item}); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, api ddb.API, table, pk string) string {
	t.Helper()
	out, err := api.GetItem(context.Background(), &dynamodb.GetItemInput{TableName: aws.String(table), Key: ddb.Key(pk, "META")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Item == nil {
		return "<none>"
	}
	return ddb.GetS(out.Item, "v")
}

func TestItemCacheReadThroughAndWriteInvalidation(t *testing.T) {
	c, cnt, table := setup(t)
	ctx := context.Background()
	put(t, c, table, "A", "1")
	for i := 0; i < 5; i++ {
		if v := get(t, c, table, "A"); v != "1" {
			t.Fatalf("got %s", v)
		}
	}
	if cnt.gets.Load() != 1 {
		t.Fatalf("expected 1 DynamoDB read, got %d", cnt.gets.Load())
	}

	// Each write path invalidates.
	put(t, c, table, "A", "2")
	if v := get(t, c, table, "A"); v != "2" {
		t.Fatalf("after put got %s", v)
	}
	_, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(table), Key: ddb.Key("A", "META"),
		UpdateExpression: aws.String("SET v = :v"), ExpressionAttributeValues: map[string]types.AttributeValue{":v": ddb.S("3")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v := get(t, c, table, "A"); v != "3" {
		t.Fatalf("after update got %s", v)
	}
	_, err = c.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{{
		Update: &types.Update{TableName: aws.String(table), Key: ddb.Key("A", "META"),
			UpdateExpression: aws.String("SET v = :v"), ExpressionAttributeValues: map[string]types.AttributeValue{":v": ddb.S("4")}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if v := get(t, c, table, "A"); v != "4" {
		t.Fatalf("after transact got %s", v)
	}
	if _, err := c.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(table), Key: ddb.Key("A", "META")}); err != nil {
		t.Fatal(err)
	}
	if v := get(t, c, table, "A"); v != "<none>" {
		t.Fatalf("after delete got %s", v)
	}

	// Strongly consistent reads always bypass.
	before := cnt.gets.Load()
	for i := 0; i < 3; i++ {
		_, _ = c.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(table), Key: ddb.Key("A", "META"), ConsistentRead: aws.Bool(true)})
	}
	if cnt.gets.Load()-before != 3 {
		t.Fatal("consistent reads were served from cache")
	}
}

func TestNegativeEntriesExpireQuickly(t *testing.T) {
	c, cnt, table := setup(t)
	if v := get(t, c, table, "missing"); v != "<none>" {
		t.Fatal(v)
	}
	get(t, c, table, "missing")
	if cnt.gets.Load() != 1 {
		t.Fatalf("negative result not cached: %d reads", cnt.gets.Load())
	}
	// Written by another process (directly, bypassing this cache).
	put(t, cnt.API, table, "missing", "now-here")
	time.Sleep(250 * time.Millisecond)
	if v := get(t, c, table, "missing"); v != "now-here" {
		t.Fatalf("negative entry outlived its TTL: %s", v)
	}
}

func TestSlowFillDoesNotCacheValueReplacedByConcurrentWrite(t *testing.T) {
	c, cnt, table := setup(t)
	put(t, c, table, "R", "old")

	// Reader 1 misses and reads "old" from DynamoDB; before it stores the
	// result, a writer replaces the item. The stale fill must be discarded.
	var once sync.Once
	cnt.beforeGetReturns = func() {
		once.Do(func() { put(t, c, table, "R", "new") })
	}
	if v := get(t, c, table, "R"); v != "old" {
		t.Fatalf("first read got %s", v)
	}
	cnt.beforeGetReturns = nil
	if v := get(t, c, table, "R"); v != "new" {
		t.Fatalf("stale value was cached: got %s", v)
	}
}

func TestBatchGetServesHitsAndFetchesMisses(t *testing.T) {
	c, cnt, table := setup(t)
	ctx := context.Background()
	for _, k := range []string{"a", "b", "c"} {
		put(t, c, table, k, k)
	}
	get(t, c, table, "a") // warm one key
	keys := []map[string]types.AttributeValue{ddb.Key("a", "META"), ddb.Key("b", "META"), ddb.Key("c", "META"), ddb.Key("zz", "META")}
	out, err := c.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: map[string]types.KeysAndAttributes{table: {Keys: keys}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Responses[table]) != 3 {
		t.Fatalf("got %d items", len(out.Responses[table]))
	}
	out, err = c.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: map[string]types.KeysAndAttributes{table: {Keys: keys}}})
	if err != nil || len(out.Responses[table]) != 3 {
		t.Fatalf("second batch: %v %d", err, len(out.Responses[table]))
	}
	if cnt.batch.Load() != 1 {
		t.Fatalf("second batch should be fully cached, DynamoDB batches = %d", cnt.batch.Load())
	}
}

func TestQueryCacheInvalidatedByTableWrites(t *testing.T) {
	c, cnt, table := setup(t)
	ctx := context.Background()
	put(t, c, table, "q1", "x")
	q := func() int {
		out, err := c.Query(ctx, &dynamodb.QueryInput{
			TableName: aws.String(table), IndexName: aws.String(ddb.GSI1),
			KeyConditionExpression:    aws.String("gsi1pk = :p"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":p": ddb.S("ALL")},
		})
		if err != nil {
			t.Fatal(err)
		}
		return len(out.Items)
	}
	q()
	q()
	if cnt.queries.Load() != 1 {
		t.Fatalf("query not cached: %d", cnt.queries.Load())
	}
	put(t, c, table, "q2", "y")
	// GSIs are eventually consistent even on DynamoDB Local; poll.
	testutil.Eventually(t, 3*time.Second, func() bool { return q() == 2 }, "query cache not invalidated")
	if s := c.QueryStats(); s.Hits == 0 {
		t.Fatalf("expected query hits, stats %+v", s)
	}
}

func TestSingleflightCollapsesConcurrentMisses(t *testing.T) {
	c, cnt, table := setup(t)
	put(t, c, table, "hot", "v")
	cnt.beforeGetReturns = func() { time.Sleep(50 * time.Millisecond) }
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); get(t, c, table, "hot") }()
	}
	wg.Wait()
	if n := cnt.gets.Load(); n > 2 {
		t.Fatalf("expected concurrent misses to share one read, got %d", n)
	}
}
