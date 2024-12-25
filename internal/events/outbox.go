// Package events implements the cross-service sync mechanism: a transactional
// outbox per service in DynamoDB, a gRPC server that tails it, and a
// resumable consumer with leader election.
package events

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ids"
)

// TypeNoop marks a tombstone written for a sequence number whose transaction
// failed, so subscribers do not wait for it.
const TypeNoop = "noop"

var (
	published = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "waypoint_events_published_total", Help: "Events committed to the outbox.",
	}, []string{"stream", "type"})
	tombstones = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "waypoint_events_tombstones_total", Help: "Sequence numbers released because their transaction failed.",
	}, []string{"stream"})
)

// Spec describes one event to publish.
type Spec struct {
	Type        string
	AggregateID string
	Payload     proto.Message
}

// Outbox appends events to stream in the same DynamoDB transaction as the
// state change that caused them, so an event exists if and only if the change
// was committed.
//
// Sequence numbers come from an atomic counter (UpdateItem ADD) that is
// incremented before the transaction. Two consequences the subscriber
// handles: numbers can commit out of order (writer A takes 5, writer B takes
// 6 and commits first), and a failed transaction leaves a hole. Failed
// writers fill their hole with a tombstone; the subscriber waits a bounded
// time for any other hole before skipping it (see Server).
type Outbox struct {
	api       ddb.API
	table     string
	stream    string
	notifier  *Notifier
	retention time.Duration
	log       *slog.Logger
	now       func() time.Time
}

// NewOutbox creates an outbox writing to table. api should be the service's
// normal (possibly cached) client so domain writes in the same transaction
// invalidate cached items.
func NewOutbox(api ddb.API, table, stream string, log *slog.Logger) *Outbox {
	return &Outbox{api: api, table: table, stream: stream, notifier: NewNotifier(), retention: 30 * 24 * time.Hour, log: log, now: time.Now}
}

// Stream returns the stream name.
func (o *Outbox) Stream() string { return o.stream }

// Notifier is signalled after every commit (wakes local subscribers).
func (o *Outbox) Notifier() *Notifier { return o.notifier }

// Table returns the backing table name.
func (o *Outbox) Table() string { return o.table }

func (o *Outbox) eventPK() string { return "EVT#" + o.stream }
func (o *Outbox) headKey() map[string]types.AttributeValue {
	return ddb.Key("EVTHEAD#"+o.stream, "HEAD")
}

// Commit writes the given transaction items plus one outbox item per event,
// atomically. With no events it is a plain transaction.
func (o *Outbox) Commit(ctx context.Context, writes []types.TransactWriteItem, evs ...Spec) error {
	if len(writes)+len(evs) > 100 {
		return fmt.Errorf("transaction too large: %d items", len(writes)+len(evs))
	}
	var first int64
	items := writes
	if len(evs) > 0 {
		var err error
		first, err = o.allocate(ctx, int64(len(evs)))
		if err != nil {
			return err
		}
		now := o.now()
		for i, e := range evs {
			payload, err := anypb.New(e.Payload)
			if err != nil {
				return fmt.Errorf("marshal event payload: %w", err)
			}
			raw, err := proto.Marshal(payload)
			if err != nil {
				return err
			}
			items = append(items, types.TransactWriteItem{Put: &types.Put{
				TableName: aws.String(o.table),
				Item: map[string]types.AttributeValue{
					ddb.PK:    ddb.S(o.eventPK()),
					ddb.SK:    ddb.S(ddb.Pad(first + int64(i))),
					"seq":     ddb.N(first + int64(i)),
					"type":    ddb.S(e.Type),
					"agg":     ddb.S(e.AggregateID),
					"payload": ddb.B(raw),
					"at_ms":   ddb.N(now.UnixMilli()),
					ddb.TTL:   ddb.N(now.Add(o.retention).Unix()),
				},
			}})
		}
	}
	_, err := o.api.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: items,
		// Makes SDK-level retries of this exact call idempotent.
		ClientRequestToken: aws.String(ids.New("tx_")), // 29 chars, max is 36
	})
	if err != nil {
		if len(evs) > 0 {
			o.release(ctx, first, len(evs))
		}
		return err
	}
	for _, e := range evs {
		published.WithLabelValues(o.stream, e.Type).Inc()
	}
	o.notifier.Notify()
	return nil
}

// allocate reserves n consecutive sequence numbers and returns the first.
func (o *Outbox) allocate(ctx context.Context, n int64) (int64, error) {
	out, err := o.api.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(o.table),
		Key:                       o.headKey(),
		UpdateExpression:          aws.String("ADD seq :n"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":n": ddb.N(n)},
		ReturnValues:              types.ReturnValueUpdatedNew,
	})
	if err != nil {
		return 0, fmt.Errorf("allocate sequence: %w", err)
	}
	last := ddb.GetN(out.Attributes, "seq")
	return last - n + 1, nil
}

// release writes tombstones for sequence numbers whose transaction failed.
// The condition makes this safe when the transaction actually committed but
// the response was lost: a real event is never overwritten.
func (o *Outbox) release(ctx context.Context, first int64, n int) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	for i := 0; i < n; i++ {
		seq := first + int64(i)
		_, err := o.api.PutItem(ctx, &dynamodb.PutItemInput{
			TableName: aws.String(o.table),
			Item: map[string]types.AttributeValue{
				ddb.PK:  ddb.S(o.eventPK()),
				ddb.SK:  ddb.S(ddb.Pad(seq)),
				"seq":   ddb.N(seq),
				"type":  ddb.S(TypeNoop),
				"at_ms": ddb.N(o.now().UnixMilli()),
				ddb.TTL: ddb.N(o.now().Add(o.retention).Unix()),
			},
			ConditionExpression: aws.String("attribute_not_exists(pk)"),
		})
		if err != nil && !ddb.IsConditionFailed(err) {
			o.log.Warn("could not write tombstone; subscribers will skip the gap after a timeout", "stream", o.stream, "seq", seq, "error", err)
			continue
		}
		tombstones.WithLabelValues(o.stream).Inc()
	}
	o.notifier.Notify()
}

// Head returns the highest allocated sequence number.
func (o *Outbox) Head(ctx context.Context) (int64, error) {
	out, err := o.api.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(o.table), Key: o.headKey(), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return 0, err
	}
	return ddb.GetN(out.Item, "seq"), nil
}

// stored is an outbox row.
type stored struct {
	seq     int64
	typ     string
	agg     string
	payload []byte
	atMs    int64
}

// read returns up to limit events with seq >= from, in order.
func (o *Outbox) read(ctx context.Context, from int64, limit int32) ([]stored, error) {
	out, err := o.api.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(o.table),
		KeyConditionExpression: aws.String("pk = :pk AND sk >= :from"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": ddb.S(o.eventPK()), ":from": ddb.S(ddb.Pad(from)),
		},
		ConsistentRead: aws.Bool(true),
		Limit:          aws.Int32(limit),
	})
	if err != nil {
		return nil, err
	}
	res := make([]stored, 0, len(out.Items))
	for _, it := range out.Items {
		res = append(res, stored{
			seq: ddb.GetN(it, "seq"), typ: ddb.GetS(it, "type"), agg: ddb.GetS(it, "agg"),
			payload: ddb.GetB(it, "payload"), atMs: ddb.GetN(it, "at_ms"),
		})
	}
	return res, nil
}

// Notifier is a broadcast signal: Wait returns a channel that is closed by
// the next Notify.
type Notifier struct {
	mu sync.Mutex
	ch chan struct{}
}

// NewNotifier creates a Notifier.
func NewNotifier() *Notifier { return &Notifier{ch: make(chan struct{})} }

// Wait returns a channel closed on the next Notify.
func (n *Notifier) Wait() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ch
}

// Notify wakes all current waiters.
func (n *Notifier) Notify() {
	n.mu.Lock()
	defer n.mu.Unlock()
	close(n.ch)
	n.ch = make(chan struct{})
}
