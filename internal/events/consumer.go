package events

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
)

var (
	consumerApplied = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "waypoint_consumer_events_applied_total", Help: "Events applied by a consumer.",
	}, []string{"consumer"})
	consumerErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "waypoint_consumer_handler_errors_total", Help: "Handler failures (each retry counts).",
	}, []string{"consumer"})
	consumerDead = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "waypoint_consumer_dead_letters_total", Help: "Events parked after a permanent handler error.",
	}, []string{"consumer"})
	consumerLag = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "waypoint_consumer_lag_events", Help: "Head sequence minus the consumer cursor.",
	}, []string{"consumer"})
	consumerConnected = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "waypoint_consumer_connected", Help: "1 while the consumer holds an open stream.",
	}, []string{"consumer"})
	syncLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "waypoint_sync_latency_seconds",
		Help:    "Time from event commit in the source service to applied in the consumer.",
		Buckets: []float64{.002, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"consumer"})
)

// ErrPermanent marks handler errors that retrying cannot fix (malformed
// payload, violated invariant). Such events are parked in a dead-letter item
// and the consumer moves on instead of blocking the stream forever.
var ErrPermanent = errors.New("permanent")

// Permanent wraps err as a permanent failure.
func Permanent(err error) error { return fmt.Errorf("%w: %w", ErrPermanent, err) }

// Handler applies one event. It must be idempotent: after a crash the
// consumer resumes from its last saved cursor and may redeliver events.
type Handler func(ctx context.Context, ev *eventsv1.Event) error

// Status is a point-in-time view of a consumer.
type Status struct {
	Name, Stream      string
	Cursor, Head      int64
	Connected, Leader bool
	LastEventAt       time.Time
	Applied           int64
}

// Consumer tails another service's event stream and applies each event.
type Consumer struct {
	name    string
	stream  string
	client  eventsv1.EventStreamServiceClient
	api     ddb.API
	table   string
	handler Handler
	lease   *Lease
	log     *slog.Logger

	workers     int
	commitEvery time.Duration

	mu sync.Mutex
	st Status
}

// ConsumerConfig configures NewConsumer.
type ConsumerConfig struct {
	Name    string // unique per consumer, e.g. "timeline.catalog"
	Stream  string // source stream name
	Client  eventsv1.EventStreamServiceClient
	API     ddb.API // consumer's own table (cursor, lease, dead letters)
	Table   string
	Handler Handler
	// Lease enables single-active-consumer leader election; nil runs
	// unconditionally (single replica or tests).
	Lease *Lease
	Log   *slog.Logger
	// Workers applies events for different aggregates in parallel (default
	// 8); events for the same aggregate are always applied in order.
	Workers int
	// CommitEvery bounds how often the cursor is written (default 50ms).
	CommitEvery time.Duration
}

// NewConsumer creates a consumer.
func NewConsumer(c ConsumerConfig) *Consumer {
	if c.Workers <= 0 {
		c.Workers = 8
	}
	if c.CommitEvery <= 0 {
		c.CommitEvery = 50 * time.Millisecond
	}
	return &Consumer{
		name: c.Name, stream: c.Stream, client: c.Client, api: c.API, table: c.Table,
		handler: c.Handler, lease: c.Lease, log: c.Log.With("consumer", c.Name),
		workers: c.Workers, commitEvery: c.CommitEvery,
		st: Status{Name: c.Name, Stream: c.Stream},
	}
}

// Status returns the current status.
func (c *Consumer) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}

func (c *Consumer) update(f func(*Status)) {
	c.mu.Lock()
	f(&c.st)
	lag := c.st.Head - c.st.Cursor
	c.mu.Unlock()
	if lag < 0 {
		lag = 0
	}
	consumerLag.WithLabelValues(c.name).Set(float64(lag))
}

// Run consumes until ctx is cancelled, reconnecting with jittered backoff.
func (c *Consumer) Run(ctx context.Context) error {
	if c.lease == nil {
		c.update(func(s *Status) { s.Leader = true })
		return c.loop(ctx)
	}
	for {
		err := c.lease.Hold(ctx, func(lctx context.Context) error {
			c.update(func(s *Status) { s.Leader = true })
			defer c.update(func(s *Status) { s.Leader = false })
			c.log.Info("acquired consumer lease")
			return c.loop(lctx)
		})
		if ctx.Err() != nil {
			return nil //nolint:nilerr // shutting down
		}
		if err != nil {
			c.log.Warn("lease lost", "error", err)
		}
	}
}

func (c *Consumer) loop(ctx context.Context) error {
	backoff := 100 * time.Millisecond
	for {
		healthy, err := c.session(ctx)
		consumerConnected.WithLabelValues(c.name).Set(0)
		c.update(func(s *Status) { s.Connected = false })
		if ctx.Err() != nil {
			return nil //nolint:nilerr // shutting down: the stream error is expected
		}
		if healthy {
			// The stream worked; this is a fresh failure, not a retry storm.
			backoff = 100 * time.Millisecond
		}
		c.log.Warn("event stream disconnected; reconnecting", "error", err, "backoff", backoff)
		sleep := backoff/2 + time.Duration(rand.Int64N(int64(backoff))) //nolint:gosec // jitter, not a secret
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(sleep):
		}
		if backoff *= 2; backoff > 10*time.Second {
			backoff = 10 * time.Second
		}
	}
}

// session runs one Subscribe call until the stream breaks. healthy reports
// whether it received anything (so the caller can reset its backoff).
//
// Events are applied by Workers goroutines in parallel, partitioned by
// aggregate id: two events about the same application always go to the same
// worker, in stream order, while different applications proceed
// independently. The cursor only advances to the watermark (every event up
// to it is applied), so after a crash the consumer resumes no later than the
// oldest unfinished event and handlers see at most a redelivery.
func (c *Consumer) session(ctx context.Context) (healthy bool, err error) {
	cursor, err := c.loadCursor(ctx)
	if err != nil {
		return false, fmt.Errorf("load cursor: %w", err)
	}
	c.update(func(s *Status) { s.Cursor = cursor })
	sctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stream, err := c.client.Subscribe(sctx, &eventsv1.SubscribeRequest{AfterSeq: cursor})
	if err != nil {
		return false, err
	}

	wm := newWatermark(cursor, c.workers*64)
	go func() { <-sctx.Done(); wm.close() }() // never leave the dispatcher blocked
	advanced := make(chan struct{}, 1)
	queues := make([]chan *eventsv1.Event, c.workers)
	var wg sync.WaitGroup
	for i := range queues {
		queues[i] = make(chan *eventsv1.Event, 64)
		wg.Add(1)
		go func(q chan *eventsv1.Event) {
			defer wg.Done()
			for ev := range q {
				if sctx.Err() != nil {
					continue // drain after a failure; the watermark stops here
				}
				if err := c.apply(sctx, ev); err != nil {
					cancel(err)
					wm.close()
					continue
				}
				consumerApplied.WithLabelValues(c.name).Inc()
				syncLatency.WithLabelValues(c.name).Observe(time.Since(ev.OccurredAt.AsTime()).Seconds())
				wm.complete(ev.Seq)
				c.update(func(s *Status) {
					s.Applied++
					if t := ev.OccurredAt.AsTime(); t.After(s.LastEventAt) {
						s.LastEventAt = t
					}
				})
				select {
				case advanced <- struct{}{}:
				default:
				}
			}
		}(queues[i])
	}

	// The committer persists the watermark, coalescing bursts of completions.
	saved := cursor
	save := func(sctx context.Context) error {
		mark := wm.current()
		if mark <= saved {
			return nil
		}
		if err := c.saveCursor(sctx, mark); err != nil {
			return err
		}
		saved = mark
		c.update(func(s *Status) { s.Cursor, s.Head = mark, max(s.Head, mark) })
		return nil
	}
	commitDone := make(chan struct{})
	go func() {
		defer close(commitDone)
		tick := time.NewTicker(c.commitEvery)
		defer tick.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case <-advanced:
			case <-tick.C:
			}
			if err := save(sctx); err != nil && sctx.Err() == nil {
				cancel(fmt.Errorf("save cursor: %w", err))
				wm.close()
				return
			}
			select { // pace commits: at most one write per interval under load
			case <-sctx.Done():
				return
			case <-time.After(c.commitEvery):
			}
		}
	}()

	recvErr := c.dispatchLoop(sctx, stream, wm, queues, cursor, &healthy)
	for _, q := range queues {
		close(q)
	}
	wg.Wait()
	cancel(recvErr)
	<-commitDone
	// Persist whatever finished before the stream broke.
	fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer fcancel()
	if err := save(fctx); err != nil {
		c.log.Warn("could not save final cursor; events after it will be redelivered", "error", err)
	}
	if cause := context.Cause(sctx); cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, io.EOF) {
		return healthy, cause
	}
	return healthy, nil
}

// dispatchLoop receives events and routes them to workers by aggregate.
func (c *Consumer) dispatchLoop(ctx context.Context, stream eventsv1.EventStreamService_SubscribeClient, wm *watermark, queues []chan *eventsv1.Event, cursor int64, healthy *bool) error {
	last := cursor
	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return io.EOF
			}
			return err
		}
		*healthy = true
		consumerConnected.WithLabelValues(c.name).Set(1)
		c.update(func(s *Status) { s.Connected = true })
		switch k := msg.Kind.(type) {
		case *eventsv1.SubscribeResponse_Heartbeat:
			c.update(func(s *Status) { s.Head = max(s.Head, k.Heartbeat.HeadSeq) })
		case *eventsv1.SubscribeResponse_Event:
			ev := k.Event
			if ev.Seq <= last {
				continue // duplicate from a racing reconnect
			}
			if !wm.dispatch(ev.Seq) {
				return context.Cause(ctx)
			}
			last = ev.Seq
			c.update(func(s *Status) { s.Head = max(s.Head, ev.Seq) })
			if ev.Type == TypeNoop {
				wm.complete(ev.Seq) // a released sequence number: nothing to apply
				continue
			}
			select {
			case queues[partition(ev.AggregateId, len(queues))] <- ev:
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		}
	}
}

func partition(key string, n int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() % uint32(n))
}

// apply runs the handler, retrying transient failures with backoff. Events
// are applied strictly in order, so a transient failure blocks the stream
// (that is the point: a timeline must never see "deadline changed" before
// "application created").
func (c *Consumer) apply(ctx context.Context, ev *eventsv1.Event) error {
	backoff := 50 * time.Millisecond
	for attempt := 1; ; attempt++ {
		err := c.handler(ctx, ev)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		consumerErrors.WithLabelValues(c.name).Inc()
		if errors.Is(err, ErrPermanent) {
			c.log.Error("permanent handler error; parking event", "seq", ev.Seq, "type", ev.Type, "error", err)
			consumerDead.WithLabelValues(c.name).Inc()
			return c.deadLetter(ctx, ev, err)
		}
		c.log.Warn("handler failed; retrying", "seq", ev.Seq, "type", ev.Type, "attempt", attempt, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > 5*time.Second {
			backoff = 5 * time.Second
		}
	}
}

func (c *Consumer) cursorKey() map[string]types.AttributeValue {
	return ddb.Key("CURSOR#"+c.name, "CURSOR")
}

func (c *Consumer) loadCursor(ctx context.Context) (int64, error) {
	out, err := c.api.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(c.table), Key: c.cursorKey(), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return 0, err
	}
	return ddb.GetN(out.Item, "seq"), nil
}

// saveCursor only moves the cursor forward, so a deposed leader that is
// still finishing an event cannot rewind it.
func (c *Consumer) saveCursor(ctx context.Context, seq int64) error {
	_, err := c.api.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(c.table),
		Key:                 c.cursorKey(),
		UpdateExpression:    aws.String("SET seq = :s, updated_ms = :now"),
		ConditionExpression: aws.String("attribute_not_exists(seq) OR seq < :s"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":s": ddb.N(seq), ":now": ddb.N(time.Now().UnixMilli()),
		},
	})
	if ddb.IsConditionFailed(err) {
		return nil
	}
	return err
}

func (c *Consumer) deadLetter(ctx context.Context, ev *eventsv1.Event, cause error) error {
	raw, err := proto.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = c.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(c.table),
		Item: map[string]types.AttributeValue{
			ddb.PK:  ddb.S("DLQ#" + c.name),
			ddb.SK:  ddb.S(ddb.Pad(ev.Seq)),
			"event": ddb.B(raw),
			"error": ddb.S(cause.Error()),
			"at_ms": ddb.N(time.Now().UnixMilli()),
		},
	})
	return err
}
