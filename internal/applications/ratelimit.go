package applications

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
)

// Limit is "at most N per window". N <= 0 disables it.
type Limit struct {
	N      int
	Window time.Duration
}

// Limits are the account-facing abuse limits. They are enforced here, in
// DynamoDB, so they hold across every gateway and applications replica; the
// gateway's in-memory token bucket only protects one replica's CPU.
type Limits struct {
	LoginPerIP       Limit // sign-in attempts from one address
	FailuresPerEmail Limit // failed sign-ins for one email (lockout)
	RegisterPerIP    Limit // sign-ups from one address
}

// DefaultLimits are the production defaults.
func DefaultLimits() Limits {
	return Limits{
		LoginPerIP:       Limit{60, 10 * time.Minute},
		FailuresPerEmail: Limit{10, 15 * time.Minute},
		RegisterPerIP:    Limit{20, time.Hour},
	}
}

// windows is a sliding-window rate counter built from fixed windows. One item
// per (scope, key, window):
//
//	pk=RL#<scope>#<sha256(key)>  sk=W#<window start, unix seconds>  n, ttl
//
// A pure fixed window lets a burst that straddles a boundary through twice
// (up to 2N-1 attempts, and a lockout split across two windows never
// triggers), so the rate is estimated the way Cloudflare does: the current
// window's count plus the previous window's count weighted by how much of it
// still overlaps the sliding window. Keys are hashed so rate-limit items
// never hold emails or addresses.
type windows struct {
	api   ddb.API
	table string
	now   func() time.Time
}

// usage is a sliding-window estimate at one instant.
type usage struct {
	cur, prev int
	start     time.Time // current window start
	window    time.Duration
	now       time.Time
}

func (u usage) estimate() float64 {
	f := float64(u.now.Sub(u.start)) / float64(u.window)
	return float64(u.cur) + float64(u.prev)*(1-f)
}

// atLeast reports whether the estimate, rounded up, reaches n.
func (u usage) atLeast(n int) bool { return math.Ceil(u.estimate()-1e-9) >= float64(n) }

// until returns when the estimate falls to m or below, if nothing new is
// counted.
func (u usage) until(m int) time.Time {
	mf := float64(m)
	var t time.Time
	switch {
	case float64(u.cur) <= mf && u.prev > 0:
		t = u.start.Add(time.Duration((1 - (mf-float64(u.cur))/float64(u.prev)) * float64(u.window)))
	case float64(u.cur) > mf:
		t = u.start.Add(u.window).Add(time.Duration((1 - mf/float64(u.cur)) * float64(u.window)))
	}
	if t.Before(u.now) {
		return u.now
	}
	return t
}

func (w *windows) keys(scope, key string, l Limit) (cur, prev map[string]types.AttributeValue, start time.Time) {
	h := sha256.Sum256([]byte(key))
	pk := "RL#" + scope + "#" + hex.EncodeToString(h[:16])
	start = w.now().Truncate(l.Window)
	sk := func(t time.Time) string { return "W#" + strconv.FormatInt(t.Unix(), 10) }
	return ddb.Key(pk, sk(start)), ddb.Key(pk, sk(start.Add(-l.Window))), start
}

// hit counts one event and returns the usage including it.
func (w *windows) hit(ctx context.Context, scope, key string, l Limit) (usage, error) {
	now := w.now()
	cur, prev, start := w.keys(scope, key, l)
	out, err := w.api.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(w.table), Key: cur,
		UpdateExpression:          aws.String("ADD n :one SET #ttl = :ttl"),
		ExpressionAttributeNames:  map[string]string{"#ttl": ddb.TTL},
		ExpressionAttributeValues: map[string]types.AttributeValue{":one": ddb.N(1), ":ttl": ddb.N(start.Add(2*l.Window + time.Hour).Unix())},
		ReturnValues:              types.ReturnValueUpdatedNew,
	})
	if err != nil {
		return usage{}, fmt.Errorf("rate limit %s: %w", scope, err)
	}
	p, err := w.api.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(w.table), Key: prev, ConsistentRead: aws.Bool(true)})
	if err != nil {
		return usage{}, fmt.Errorf("rate limit %s: %w", scope, err)
	}
	return usage{cur: int(ddb.GetN(out.Attributes, "n")), prev: int(ddb.GetN(p.Item, "n")), start: start, window: l.Window, now: now}, nil
}

// count returns the usage without counting anything.
func (w *windows) count(ctx context.Context, scope, key string, l Limit) (usage, error) {
	now := w.now()
	cur, prev, start := w.keys(scope, key, l)
	out, err := w.api.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: map[string]types.KeysAndAttributes{
		w.table: {Keys: []map[string]types.AttributeValue{cur, prev}, ConsistentRead: aws.Bool(true)},
	}})
	if err != nil {
		return usage{}, fmt.Errorf("rate limit %s: %w", scope, err)
	}
	u := usage{start: start, window: l.Window, now: now}
	curSK := ddb.GetS(cur, ddb.SK)
	for _, it := range out.Responses[w.table] {
		if ddb.GetS(it, ddb.SK) == curSK {
			u.cur = int(ddb.GetN(it, "n"))
		} else {
			u.prev = int(ddb.GetN(it, "n"))
		}
	}
	return u, nil
}

// reset clears the count (a successful sign-in clears failures).
func (w *windows) reset(ctx context.Context, scope, key string, l Limit) error {
	cur, prev, _ := w.keys(scope, key, l)
	_, err := w.api.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{RequestItems: map[string][]types.WriteRequest{
		w.table: {{DeleteRequest: &types.DeleteRequest{Key: cur}}, {DeleteRequest: &types.DeleteRequest{Key: prev}}},
	}})
	return err
}

// limited is the ResourceExhausted error with a RetryInfo detail, which the
// gateway turns into 429 + Retry-After.
func limited(msg string, until, now time.Time) error {
	wait := until.Sub(now).Round(time.Second)
	if wait < time.Second {
		wait = time.Second
	}
	mins := int((wait + time.Minute - 1) / time.Minute)
	st := status.New(codes.ResourceExhausted, fmt.Sprintf("%s Try again in %d minute%s.", msg, mins, map[bool]string{true: "", false: "s"}[mins == 1]))
	if d, err := st.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(wait)}); err == nil {
		return d.Err()
	}
	return st.Err()
}
