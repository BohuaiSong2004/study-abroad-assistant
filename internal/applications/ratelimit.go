package applications

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

// windows is a fixed-window counter store. One item per (scope, key,
// window):
//
//	pk=RL#<scope>#<sha256(key)>  sk=W#<window start, unix seconds>  n, ttl
//
// Keys are hashed so rate-limit items never hold emails or addresses.
type windows struct {
	api   ddb.API
	table string
	now   func() time.Time
}

func (w *windows) key(scope, key string, l Limit) (map[string]types.AttributeValue, time.Time) {
	h := sha256.Sum256([]byte(key))
	start := w.now().Truncate(l.Window)
	return ddb.Key("RL#"+scope+"#"+hex.EncodeToString(h[:16]), "W#"+strconv.FormatInt(start.Unix(), 10)), start.Add(l.Window)
}

// hit counts one event and returns the count in the current window.
func (w *windows) hit(ctx context.Context, scope, key string, l Limit) (int, time.Time, error) {
	k, end := w.key(scope, key, l)
	out, err := w.api.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(w.table), Key: k,
		UpdateExpression:          aws.String("ADD n :one SET #ttl = :ttl"),
		ExpressionAttributeNames:  map[string]string{"#ttl": ddb.TTL},
		ExpressionAttributeValues: map[string]types.AttributeValue{":one": ddb.N(1), ":ttl": ddb.N(end.Add(time.Hour).Unix())},
		ReturnValues:              types.ReturnValueUpdatedNew,
	})
	if err != nil {
		return 0, end, fmt.Errorf("rate limit %s: %w", scope, err)
	}
	return int(ddb.GetN(out.Attributes, "n")), end, nil
}

// count returns the current window's count without changing it.
func (w *windows) count(ctx context.Context, scope, key string, l Limit) (int, time.Time, error) {
	k, end := w.key(scope, key, l)
	out, err := w.api.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(w.table), Key: k, ConsistentRead: aws.Bool(true)})
	if err != nil {
		return 0, end, fmt.Errorf("rate limit %s: %w", scope, err)
	}
	return int(ddb.GetN(out.Item, "n")), end, nil
}

// reset clears the current window (a successful sign-in clears failures).
func (w *windows) reset(ctx context.Context, scope, key string, l Limit) error {
	k, _ := w.key(scope, key, l)
	_, err := w.api.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(w.table), Key: k})
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
