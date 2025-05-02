package ddb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash/fnv"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/sync/singleflight"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/cache"
)

var cacheRequests = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "waypoint_ddb_cache_requests_total",
	Help: "DynamoDB read-through cache lookups by cache (item, query) and result (hit, miss, bypass).",
}, []string{"cache", "result"})

// CacheOptions configures CachedAPI.
type CacheOptions struct {
	ItemTTL       time.Duration
	QueryTTL      time.Duration
	NegativeTTL   time.Duration
	ItemCapacity  int
	QueryCapacity int
}

// CachedAPI is a read-through, write-invalidate cache in front of DynamoDB,
// modelled on DAX:
//
//   - Item cache: eventually consistent GetItem/BatchGetItem results
//     (including "not found", cached for NegativeTTL) keyed by table+key.
//   - Query cache: eventually consistent Query results keyed by the full
//     request, valid until QueryTTL or until a write through this client
//     that could change the result: base-table queries are invalidated by
//     writes to the same partition key, index queries by writes that may
//     touch the index.
//   - ConsistentRead=true and ProjectionExpression requests bypass the cache,
//     exactly like DAX passes strongly consistent reads through to DynamoDB.
//
// Two details make it safe for read-your-writes within one process, which DAX
// gives you through write-through:
//
//  1. Cache misses are filled with a strongly consistent read, so a fill that
//     happens right after a write can never cache the pre-write image.
//  2. Each fill records a generation counter (per key stripe for items, per
//     partition stripe or index for queries) before reading and only stores
//     the result if no write bumped the counter in between. That closes the
//     classic race where a slow reader caches a value that a concurrent
//     writer just replaced.
//
// Invalidation is deliberately scoped: an earlier version used one counter
// per table, so the timeline service's lease renewals (a write every few
// seconds to an unrelated item) emptied every cached query in that table
// and the query hit rate was ~50%.
//
// Across processes, staleness is bounded by the TTLs (same contract as DAX).
type CachedAPI struct {
	next    API
	opts    CacheOptions
	items   *cache.LRU[string, map[string]types.AttributeValue]
	queries *cache.LRU[string, *dynamodb.QueryOutput]
	sf      singleflight.Group

	mu   sync.Mutex
	gens map[string]*tableGen
}

const stripes = 256

type tableGen struct {
	all     atomic.Uint64          // every write (fallback for unrecognised queries)
	index   atomic.Uint64          // writes that may change a secondary index
	stripes [stripes]atomic.Uint64 // item keys
	parts   [stripes]atomic.Uint64 // partition keys (base-table queries)
}

// NewCachedAPI wraps next.
func NewCachedAPI(next API, o CacheOptions) *CachedAPI {
	if o.ItemTTL <= 0 {
		o.ItemTTL = 5 * time.Minute
	}
	if o.QueryTTL <= 0 {
		o.QueryTTL = 10 * time.Second
	}
	if o.NegativeTTL <= 0 {
		o.NegativeTTL = 2 * time.Second
	}
	if o.ItemCapacity <= 0 {
		o.ItemCapacity = 50_000
	}
	if o.QueryCapacity <= 0 {
		o.QueryCapacity = 5_000
	}
	return &CachedAPI{
		next:    next,
		opts:    o,
		items:   cache.New[string, map[string]types.AttributeValue](o.ItemCapacity, o.ItemTTL),
		queries: cache.New[string, *dynamodb.QueryOutput](o.QueryCapacity, o.QueryTTL),
		gens:    make(map[string]*tableGen),
	}
}

// ItemStats and QueryStats expose cache counters.
func (c *CachedAPI) ItemStats() cache.Stats  { return c.items.Stats() }
func (c *CachedAPI) QueryStats() cache.Stats { return c.queries.Stats() }

// Purge drops all cached entries.
func (c *CachedAPI) Purge() {
	c.items.Purge()
	c.queries.Purge()
}

func (c *CachedAPI) gen(table string) *tableGen {
	c.mu.Lock()
	defer c.mu.Unlock()
	g, ok := c.gens[table]
	if !ok {
		g = &tableGen{}
		c.gens[table] = g
	}
	return g
}

func stripe(k string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(k))
	return int(h.Sum32() % stripes)
}

// invalidate is called after every write to (table, key). index reports
// whether the write may have changed a secondary index.
func (c *CachedAPI) invalidate(table string, key map[string]types.AttributeValue, index bool) {
	g := c.gen(table)
	g.all.Add(1)
	if index {
		g.index.Add(1)
	}
	if key != nil {
		k := itemKey(table, key)
		g.stripes[stripe(k)].Add(1)
		g.parts[stripe(partKey(table, key[PK]))].Add(1)
		c.items.Delete(k)
	}
}

// putTouchesIndex: a Put can only change the index if the new item carries
// the index key (outbox events, leases and cursors never do).
func putTouchesIndex(item map[string]types.AttributeValue) bool {
	_, ok := item[GSI1PK]
	return ok
}

func partKey(table string, pk types.AttributeValue) string {
	var b strings.Builder
	b.WriteString(table)
	b.WriteByte(0)
	writeAV(&b, pk)
	return b.String()
}

var pkCond = regexp.MustCompile(`(?:^|[\s(])` + PK + `\s*=\s*(:[A-Za-z0-9_]+)`)

// queryGen returns the generation counter that guards a query's result.
func queryGen(g *tableGen, table string, in *dynamodb.QueryInput) *atomic.Uint64 {
	if in.IndexName != nil {
		return &g.index
	}
	if m := pkCond.FindStringSubmatch(aws.ToString(in.KeyConditionExpression)); m != nil {
		if v, ok := in.ExpressionAttributeValues[m[1]]; ok {
			return &g.parts[stripe(partKey(table, v))]
		}
	}
	return &g.all
}

// --- reads ---------------------------------------------------------------

// GetItem serves eventually consistent reads from the item cache.
func (c *CachedAPI) GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	if aws.ToBool(in.ConsistentRead) || in.ProjectionExpression != nil {
		cacheRequests.WithLabelValues("item", "bypass").Inc()
		return c.next.GetItem(ctx, in, opts...)
	}
	table := aws.ToString(in.TableName)
	k := itemKey(table, in.Key)
	if item, ok := c.items.Get(k); ok {
		cacheRequests.WithLabelValues("item", "hit").Inc()
		return &dynamodb.GetItemOutput{Item: item}, nil
	}
	cacheRequests.WithLabelValues("item", "miss").Inc()
	v, err, _ := c.sf.Do("i|"+k, func() (any, error) {
		g := c.gen(table)
		st := stripe(k)
		before := g.stripes[st].Load()
		fill := *in
		fill.ConsistentRead = aws.Bool(true)
		fctx, cancel := detach(ctx)
		defer cancel()
		out, err := c.next.GetItem(fctx, &fill, opts...)
		if err != nil {
			return nil, err
		}
		if g.stripes[st].Load() == before {
			ttl := c.opts.ItemTTL
			if out.Item == nil {
				ttl = c.opts.NegativeTTL
			}
			c.items.SetTTL(k, out.Item, ttl)
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return &dynamodb.GetItemOutput{Item: v.(*dynamodb.GetItemOutput).Item}, nil
}

// BatchGetItem serves cached keys locally and fetches only the misses.
func (c *CachedAPI) BatchGetItem(ctx context.Context, in *dynamodb.BatchGetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.BatchGetItemOutput, error) {
	for _, ka := range in.RequestItems {
		if aws.ToBool(ka.ConsistentRead) || ka.ProjectionExpression != nil {
			cacheRequests.WithLabelValues("item", "bypass").Add(float64(len(ka.Keys)))
			return c.next.BatchGetItem(ctx, in, opts...)
		}
	}
	out := &dynamodb.BatchGetItemOutput{Responses: map[string][]map[string]types.AttributeValue{}}
	missing := map[string]types.KeysAndAttributes{}
	type pendingFill struct {
		key    string
		stripe int
		before uint64
	}
	fills := map[string][]pendingFill{}
	for table, ka := range in.RequestItems {
		g := c.gen(table)
		for _, key := range ka.Keys {
			k := itemKey(table, key)
			if item, ok := c.items.Get(k); ok {
				cacheRequests.WithLabelValues("item", "hit").Inc()
				if item != nil {
					out.Responses[table] = append(out.Responses[table], item)
				}
				continue
			}
			cacheRequests.WithLabelValues("item", "miss").Inc()
			st := stripe(k)
			fills[table] = append(fills[table], pendingFill{key: k, stripe: st, before: g.stripes[st].Load()})
			m := missing[table]
			m.Keys = append(m.Keys, key)
			m.ConsistentRead = aws.Bool(true)
			missing[table] = m
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	res, err := c.next.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: missing}, opts...)
	if err != nil {
		return nil, err
	}
	for table, items := range res.Responses {
		out.Responses[table] = append(out.Responses[table], items...)
		g := c.gen(table)
		found := map[string]map[string]types.AttributeValue{}
		for _, it := range items {
			found[itemKey(table, keyOf(it))] = it
		}
		unprocessed := map[string]bool{}
		if u, ok := res.UnprocessedKeys[table]; ok {
			for _, key := range u.Keys {
				unprocessed[itemKey(table, key)] = true
			}
		}
		for _, f := range fills[table] {
			if unprocessed[f.key] || g.stripes[f.stripe].Load() != f.before {
				continue
			}
			if it, ok := found[f.key]; ok {
				c.items.Set(f.key, it)
			} else {
				c.items.SetTTL(f.key, nil, c.opts.NegativeTTL)
			}
		}
	}
	out.UnprocessedKeys = res.UnprocessedKeys
	return out, nil
}

// Query serves eventually consistent queries from the query cache.
func (c *CachedAPI) Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	if aws.ToBool(in.ConsistentRead) {
		cacheRequests.WithLabelValues("query", "bypass").Inc()
		return c.next.Query(ctx, in, opts...)
	}
	table := aws.ToString(in.TableName)
	g := c.gen(table)
	gen := queryGen(g, table, in)
	before := gen.Load()
	k := queryKey(in, before)
	if out, ok := c.queries.Get(k); ok {
		cacheRequests.WithLabelValues("query", "hit").Inc()
		return out, nil
	}
	cacheRequests.WithLabelValues("query", "miss").Inc()
	v, err, _ := c.sf.Do("q|"+k, func() (any, error) {
		fctx, cancel := detach(ctx)
		defer cancel()
		out, err := c.next.Query(fctx, in, opts...)
		if err != nil {
			return nil, err
		}
		if gen.Load() == before {
			c.queries.Set(k, out)
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*dynamodb.QueryOutput), nil
}

// --- writes (pass through, then invalidate) ------------------------------

func (c *CachedAPI) PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	out, err := c.next.PutItem(ctx, in, opts...)
	c.invalidate(aws.ToString(in.TableName), keyOf(in.Item), putTouchesIndex(in.Item))
	return out, err
}

func (c *CachedAPI) UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	out, err := c.next.UpdateItem(ctx, in, opts...)
	c.invalidate(aws.ToString(in.TableName), in.Key, true)
	return out, err
}

func (c *CachedAPI) DeleteItem(ctx context.Context, in *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	out, err := c.next.DeleteItem(ctx, in, opts...)
	c.invalidate(aws.ToString(in.TableName), in.Key, true)
	return out, err
}

func (c *CachedAPI) TransactWriteItems(ctx context.Context, in *dynamodb.TransactWriteItemsInput, opts ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	out, err := c.next.TransactWriteItems(ctx, in, opts...)
	for _, it := range in.TransactItems {
		switch {
		case it.Put != nil:
			c.invalidate(aws.ToString(it.Put.TableName), keyOf(it.Put.Item), putTouchesIndex(it.Put.Item))
		case it.Update != nil:
			c.invalidate(aws.ToString(it.Update.TableName), it.Update.Key, true)
		case it.Delete != nil:
			c.invalidate(aws.ToString(it.Delete.TableName), it.Delete.Key, true)
		}
	}
	return out, err
}

func (c *CachedAPI) BatchWriteItem(ctx context.Context, in *dynamodb.BatchWriteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error) {
	out, err := c.next.BatchWriteItem(ctx, in, opts...)
	for table, reqs := range in.RequestItems {
		for _, r := range reqs {
			switch {
			case r.PutRequest != nil:
				c.invalidate(table, keyOf(r.PutRequest.Item), putTouchesIndex(r.PutRequest.Item))
			case r.DeleteRequest != nil:
				c.invalidate(table, r.DeleteRequest.Key, true)
			}
		}
	}
	return out, err
}

// --- key encoding ----------------------------------------------------------

func keyOf(item map[string]types.AttributeValue) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{PK: item[PK], SK: item[SK]}
}

func itemKey(table string, key map[string]types.AttributeValue) string {
	var b strings.Builder
	b.WriteString(table)
	b.WriteByte(0)
	writeAV(&b, key[PK])
	b.WriteByte(0)
	writeAV(&b, key[SK])
	return b.String()
}

func queryKey(in *dynamodb.QueryInput, gen uint64) string {
	var b strings.Builder
	w := func(s string) { b.WriteString(s); b.WriteByte(0) }
	w(aws.ToString(in.TableName))
	w(aws.ToString(in.IndexName))
	w(aws.ToString(in.KeyConditionExpression))
	w(aws.ToString(in.FilterExpression))
	w(aws.ToString(in.ProjectionExpression))
	w(string(in.Select))
	if in.Limit != nil {
		w("limit=" + strconv.Itoa(int(*in.Limit)))
	}
	if in.ScanIndexForward != nil && !*in.ScanIndexForward {
		w("desc")
	}
	names := make([]string, 0, len(in.ExpressionAttributeNames))
	for k := range in.ExpressionAttributeNames {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		w(k + "=" + in.ExpressionAttributeNames[k])
	}
	writeMap(&b, in.ExpressionAttributeValues)
	writeMap(&b, in.ExclusiveStartKey)
	sum := sha256.Sum256([]byte(b.String()))
	return aws.ToString(in.TableName) + "|" + hex.EncodeToString(sum[:16]) + "|" + uitoa(gen)
}

func writeMap(b *strings.Builder, m map[string]types.AttributeValue) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		writeAV(b, m[k])
		b.WriteByte(0)
	}
}

func writeAV(b *strings.Builder, v types.AttributeValue) {
	switch t := v.(type) {
	case *types.AttributeValueMemberS:
		b.WriteString("S:" + t.Value)
	case *types.AttributeValueMemberN:
		b.WriteString("N:" + t.Value)
	case *types.AttributeValueMemberB:
		b.WriteString("B:" + hex.EncodeToString(t.Value))
	case *types.AttributeValueMemberBOOL:
		if t.Value {
			b.WriteString("T")
		} else {
			b.WriteString("F")
		}
	case *types.AttributeValueMemberSS:
		b.WriteString("SS:" + strings.Join(t.Value, "\x01"))
	case *types.AttributeValueMemberL:
		b.WriteString("L[")
		for _, e := range t.Value {
			writeAV(b, e)
			b.WriteByte(1)
		}
		b.WriteString("]")
	case *types.AttributeValueMemberM:
		b.WriteString("M{")
		writeMap(b, t.Value)
		b.WriteString("}")
	case *types.AttributeValueMemberNULL:
		b.WriteString("NULL")
	case nil:
		b.WriteString("<nil>")
	default:
		b.WriteString("?")
	}
}

func uitoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// detach keeps request values (tracing, request id) but not cancellation, so
// one caller giving up does not fail every request sharing a singleflight
// fill. The fill is still bounded.
func detach(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}
