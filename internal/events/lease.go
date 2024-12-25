package events

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
)

// ErrLeaseLost is returned by Hold when renewal fails.
var ErrLeaseLost = errors.New("lease lost")

// Lease is a DynamoDB-backed lock with expiry, used so that exactly one
// replica of a service consumes a given stream at a time. Correctness does
// not depend on the lease (handlers are idempotent and the cursor only moves
// forward); it avoids duplicated work and write contention.
type Lease struct {
	api   ddb.API
	table string
	name  string
	owner string
	ttl   time.Duration
	now   func() time.Time
}

// NewLease creates a lease named name held by owner (e.g. hostname+pid).
func NewLease(api ddb.API, table, name, owner string, ttl time.Duration) *Lease {
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	return &Lease{api: api, table: table, name: name, owner: owner, ttl: ttl, now: time.Now}
}

func (l *Lease) key() map[string]types.AttributeValue { return ddb.Key("LEASE#"+l.name, "LEASE") }

// TryAcquire takes or renews the lease; false means another owner holds it.
func (l *Lease) TryAcquire(ctx context.Context) (bool, error) {
	now := l.now()
	_, err := l.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(l.table),
		Item: map[string]types.AttributeValue{
			ddb.PK:       ddb.S("LEASE#" + l.name),
			ddb.SK:       ddb.S("LEASE"),
			"owner":      ddb.S(l.owner),
			"expires_ms": ddb.N(now.Add(l.ttl).UnixMilli()),
			ddb.TTL:      ddb.N(now.Add(24 * time.Hour).Unix()),
		},
		ConditionExpression:      aws.String("attribute_not_exists(pk) OR expires_ms < :now OR #o = :me"),
		ExpressionAttributeNames: map[string]string{"#o": "owner"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":now": ddb.N(now.UnixMilli()), ":me": ddb.S(l.owner),
		},
	})
	if ddb.IsConditionFailed(err) {
		return false, nil
	}
	return err == nil, err
}

// Release gives the lease up if still held by this owner.
func (l *Lease) Release(ctx context.Context) {
	_, _ = l.api.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:                 aws.String(l.table),
		Key:                       l.key(),
		ConditionExpression:       aws.String("#o = :me"),
		ExpressionAttributeNames:  map[string]string{"#o": "owner"},
		ExpressionAttributeValues: map[string]types.AttributeValue{":me": ddb.S(l.owner)},
	})
}

// Hold blocks until the lease is acquired, then runs fn with a context that
// is cancelled if a renewal fails. It renews every ttl/3 and releases the
// lease when fn returns.
func (l *Lease) Hold(ctx context.Context, fn func(context.Context) error) error {
	for {
		ok, err := l.TryAcquire(ctx)
		if ok {
			break
		}
		_ = err // transient errors and contention are both retried
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(l.ttl / 3):
		}
	}
	lctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go func() {
		t := time.NewTicker(l.ttl / 3)
		defer t.Stop()
		for {
			select {
			case <-lctx.Done():
				return
			case <-t.C:
				ok, err := l.TryAcquire(lctx)
				if lctx.Err() != nil {
					return
				}
				if !ok || err != nil {
					cancel(ErrLeaseLost)
					return
				}
			}
		}
	}()
	err := fn(lctx)
	rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	l.Release(rctx)
	rcancel()
	if cause := context.Cause(lctx); errors.Is(cause, ErrLeaseLost) {
		return ErrLeaseLost
	}
	return err
}
