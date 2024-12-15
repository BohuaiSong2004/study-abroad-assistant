package events

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/testutil"
)

type fixture struct {
	ob     *Outbox
	table  string
	api    ddb.API
	client eventsv1.EventStreamServiceClient
}

func newFixture(t *testing.T, opts ServerOptions) *fixture {
	t.Helper()
	c, prefix := testutil.Tables(t, ddb.CacheLocal)
	table := prefix + ddb.TableCatalog
	log := testutil.Logger(t)
	ob := NewOutbox(c.API, table, "test", log)
	srv := NewServer(ob, log, opts)
	cc := testutil.Server(t, func(s *grpc.Server) { eventsv1.RegisterEventStreamServiceServer(s, srv) })
	return &fixture{ob: ob, table: table, api: c.API, client: eventsv1.NewEventStreamServiceClient(cc)}
}

func spec(v string) Spec {
	return Spec{Type: "test.value", AggregateID: v, Payload: wrapperspb.String(v)}
}

func putItem(table, pk string) types.TransactWriteItem {
	return types.TransactWriteItem{Put: &types.Put{
		TableName:           aws.String(table),
		Item:                ddb.Key(pk, "META"),
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	}}
}

// collect subscribes and returns a function that reads n events.
func collect(t *testing.T, f *fixture, after int64) func(n int, timeout time.Duration) []*eventsv1.Event {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := f.client.Subscribe(ctx, &eventsv1.SubscribeRequest{AfterSeq: after})
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan *eventsv1.Event, 100)
	go func() {
		for {
			m, err := st.Recv()
			if err != nil {
				close(ch)
				return
			}
			if e := m.GetEvent(); e != nil {
				ch <- e
			}
		}
	}()
	return func(n int, timeout time.Duration) []*eventsv1.Event {
		var got []*eventsv1.Event
		deadline := time.After(timeout)
		for len(got) < n {
			select {
			case e, ok := <-ch:
				if !ok {
					return got
				}
				got = append(got, e)
			case <-deadline:
				return got
			}
		}
		return got
	}
}

func values(t *testing.T, evs []*eventsv1.Event) []string {
	var out []string
	for _, e := range evs {
		if e.Type == TypeNoop {
			out = append(out, fmt.Sprintf("%d:(released)", e.Seq))
			continue
		}
		var v wrapperspb.StringValue
		if err := e.Payload.UnmarshalTo(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d:%s", e.Seq, v.Value))
	}
	return out
}

func TestCommitIsAtomicAndOrdered(t *testing.T) {
	f := newFixture(t, ServerOptions{})
	ctx := context.Background()
	read := collect(t, f, 0)

	if err := f.ob.Commit(ctx, []types.TransactWriteItem{putItem(f.table, "A")}, spec("a1"), spec("a2")); err != nil {
		t.Fatal(err)
	}
	if err := f.ob.Commit(ctx, nil, spec("b")); err != nil {
		t.Fatal(err)
	}
	got := values(t, read(3, 3*time.Second))
	want := []string{"1:a1", "2:a2", "3:b"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}

	// The domain write and its event commit together or not at all.
	err := f.ob.Commit(ctx, []types.TransactWriteItem{putItem(f.table, "A")}, spec("dup"))
	if !ddb.IsConditionFailed(err) {
		t.Fatalf("expected condition failure, got %v", err)
	}
	head, _ := f.ob.Head(ctx)
	if head != 4 {
		t.Fatalf("head = %d, want 4 (seq 4 allocated then released)", head)
	}
}

func TestFailedTransactionTombstoneDoesNotDelaySubscribers(t *testing.T) {
	// A huge gap timeout proves the subscriber is not waiting it out.
	f := newFixture(t, ServerOptions{GapTimeout: time.Minute})
	ctx := context.Background()
	read := collect(t, f, 0)
	if err := f.ob.Commit(ctx, []types.TransactWriteItem{putItem(f.table, "X")}, spec("x")); err != nil {
		t.Fatal(err)
	}
	if err := f.ob.Commit(ctx, []types.TransactWriteItem{putItem(f.table, "X")}, spec("lost")); err == nil {
		t.Fatal("expected failure")
	}
	if err := f.ob.Commit(ctx, nil, spec("y")); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got := values(t, read(3, 5*time.Second))
	if fmt.Sprint(got) != "[1:x 2:(released) 3:y]" {
		t.Fatalf("got %v", got)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("subscriber waited %s on a tombstoned gap", time.Since(start))
	}
}

func TestOutOfOrderCommitIsDeliveredInOrder(t *testing.T) {
	f := newFixture(t, ServerOptions{GapTimeout: 10 * time.Second})
	ctx := context.Background()
	read := collect(t, f, 0)

	// Writer A reserves seq 1 but is slow; writer B takes seq 2 and commits.
	first, err := f.ob.allocate(ctx, 1)
	if err != nil || first != 1 {
		t.Fatalf("allocate: %d %v", first, err)
	}
	if err := f.ob.Commit(ctx, nil, spec("B")); err != nil {
		t.Fatal(err)
	}
	if early := read(1, 300*time.Millisecond); len(early) != 0 {
		t.Fatalf("delivered %v before the gap was filled", values(t, early))
	}
	// Writer A finally commits seq 1.
	writeRaw(t, f, 1, "A")
	got := values(t, read(2, 3*time.Second))
	if fmt.Sprint(got) != "[1:A 2:B]" {
		t.Fatalf("got %v", got)
	}
}

func TestAbandonedGapIsSkippedAfterTimeout(t *testing.T) {
	f := newFixture(t, ServerOptions{GapTimeout: 300 * time.Millisecond})
	ctx := context.Background()
	read := collect(t, f, 0)
	if _, err := f.ob.allocate(ctx, 1); err != nil { // writer crashed after allocating
		t.Fatal(err)
	}
	if err := f.ob.Commit(ctx, nil, spec("after")); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got := values(t, read(1, 5*time.Second))
	if fmt.Sprint(got) != "[2:after]" {
		t.Fatalf("got %v", got)
	}
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("gap skipped too early (%s)", d)
	}
}

func TestSubscribeFromTail(t *testing.T) {
	f := newFixture(t, ServerOptions{})
	ctx := context.Background()
	_ = f.ob.Commit(ctx, nil, spec("old"))
	st, err := f.client.Subscribe(ctx, &eventsv1.SubscribeRequest{FromTail: true})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	_ = f.ob.Commit(ctx, nil, spec("new"))
	m, err := st.Recv()
	for err == nil && m.GetEvent() == nil {
		m, err = st.Recv()
	}
	if err != nil {
		t.Fatal(err)
	}
	if m.GetEvent().Seq != 2 {
		t.Fatalf("from tail delivered seq %d", m.GetEvent().Seq)
	}
}

func writeRaw(t *testing.T, f *fixture, seq int64, v string) {
	t.Helper()
	a, _ := anypb.New(wrapperspb.String(v))
	raw, _ := proto.Marshal(a)
	_, err := f.api.PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: aws.String(f.table),
		Item: map[string]types.AttributeValue{
			ddb.PK: ddb.S("EVT#test"), ddb.SK: ddb.S(ddb.Pad(seq)), "seq": ddb.N(seq),
			"type": ddb.S("test.value"), "agg": ddb.S(v), "payload": ddb.B(raw), "at_ms": ddb.N(time.Now().UnixMilli()),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.ob.notifier.Notify()
}

type recorder struct {
	mu   sync.Mutex
	seen []string
	fail map[string]int // value -> remaining transient failures
	perm map[string]bool
}

func (r *recorder) handle(_ context.Context, ev *eventsv1.Event) error {
	var v wrapperspb.StringValue
	if err := ev.Payload.UnmarshalTo(&v); err != nil {
		return Permanent(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail[v.Value] > 0 {
		r.fail[v.Value]--
		return errors.New("transient")
	}
	if r.perm[v.Value] {
		return Permanent(errors.New("bad event"))
	}
	r.seen = append(r.seen, v.Value)
	return nil
}

func (r *recorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

func TestConsumerResumesFromCursorRetriesAndParks(t *testing.T) {
	f := newFixture(t, ServerOptions{})
	ctx := context.Background()
	rec := &recorder{fail: map[string]int{"e2": 2}, perm: map[string]bool{"e3": true}}
	newConsumer := func() *Consumer {
		return NewConsumer(ConsumerConfig{
			Name: "test.consumer", Stream: "test", Client: f.client, API: f.api, Table: f.table,
			Handler: rec.handle, Log: testutil.Logger(t),
		})
	}
	for _, v := range []string{"e1", "e2", "e3", "e4"} {
		if err := f.ob.Commit(ctx, nil, spec(v)); err != nil {
			t.Fatal(err)
		}
	}
	c1 := newConsumer()
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = c1.Run(cctx); close(done) }()
	testutil.Eventually(t, 5*time.Second, func() bool { return len(rec.got()) == 3 }, "got %v", rec.got())
	// Different aggregates are applied in parallel, so only the set is fixed
	// (per-aggregate order is covered by TestParallelApplyKeepsPerAggregateOrder).
	if got := sorted(rec.got()); fmt.Sprint(got) != "[e1 e2 e4]" {
		t.Fatalf("got %v", got)
	}
	testutil.Eventually(t, 2*time.Second, func() bool { return c1.Status().Cursor == 4 }, "cursor %d", c1.Status().Cursor)
	cancel()
	<-done

	// Parked event is recorded in the dead-letter item.
	dl, err := f.api.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(f.table), Key: ddb.Key("DLQ#test.consumer", ddb.Pad(3)), ConsistentRead: aws.Bool(true)})
	if err != nil || dl.Item == nil {
		t.Fatalf("dead letter missing: %v", err)
	}

	// A new consumer resumes after the saved cursor: no redelivery.
	_ = f.ob.Commit(ctx, nil, spec("e5"))
	c2 := newConsumer()
	cctx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	go func() { _ = c2.Run(cctx2) }()
	testutil.Eventually(t, 5*time.Second, func() bool { return len(rec.got()) == 4 }, "got %v", rec.got())
	time.Sleep(200 * time.Millisecond)
	if got := sorted(rec.got()); fmt.Sprint(got) != "[e1 e2 e4 e5]" {
		t.Fatalf("after resume got %v", got)
	}
}

func TestLeaseHasSingleHolderAndExpires(t *testing.T) {
	c, prefix := testutil.Tables(t, ddb.CacheOff)
	table := prefix + ddb.TableTimeline
	ctx := context.Background()
	a := NewLease(c.API, table, "x", "a", 300*time.Millisecond)
	b := NewLease(c.API, table, "x", "b", 300*time.Millisecond)
	if ok, err := a.TryAcquire(ctx); !ok || err != nil {
		t.Fatalf("a acquire: %v %v", ok, err)
	}
	if ok, _ := b.TryAcquire(ctx); ok {
		t.Fatal("b acquired a held lease")
	}
	if ok, _ := a.TryAcquire(ctx); !ok {
		t.Fatal("a could not renew")
	}
	time.Sleep(350 * time.Millisecond) // a stops renewing (crash)
	if ok, _ := b.TryAcquire(ctx); !ok {
		t.Fatal("b could not take over an expired lease")
	}
	b.Release(ctx)
	if ok, _ := a.TryAcquire(ctx); !ok {
		t.Fatal("a could not acquire a released lease")
	}

	// Hold runs fn while renewing and returns when fn does.
	ran := false
	if err := a.Hold(ctx, func(context.Context) error { time.Sleep(400 * time.Millisecond); ran = true; return nil }); err != nil || !ran {
		t.Fatalf("hold: %v ran=%v", err, ran)
	}
}

// Regression: the client service config once applied the unary deadline to
// every method, so each Subscribe stream died after 10 s and sync stalled
// while the consumer backed off. Streams must outlive the unary timeout.
func TestSubscribeOutlivesUnaryDeadline(t *testing.T) {
	old := testutil.UnaryTimeout
	testutil.UnaryTimeout = 300 * time.Millisecond
	t.Cleanup(func() { testutil.UnaryTimeout = old })
	f := newFixture(t, ServerOptions{Heartbeat: 100 * time.Millisecond})
	read := collect(t, f, 0)
	time.Sleep(time.Second) // > 3x the unary deadline
	if err := f.ob.Commit(context.Background(), nil, spec("late")); err != nil {
		t.Fatal(err)
	}
	if got := values(t, read(1, 3*time.Second)); fmt.Sprint(got) != "[1:late]" {
		t.Fatalf("stream did not survive the unary deadline: %v", got)
	}
	// Unary calls still work under the deadline.
	if _, err := f.client.Head(context.Background(), &eventsv1.HeadRequest{}); err != nil {
		t.Fatal(err)
	}
}

func sorted(v []string) []string {
	out := append([]string(nil), v...)
	sort.Strings(out)
	return out
}

func TestParallelApplyKeepsPerAggregateOrder(t *testing.T) {
	f := newFixture(t, ServerOptions{})
	ctx := context.Background()
	const perAgg = 25
	aggs := []string{"app_a", "app_b", "app_c", "app_d", "app_e"}
	for i := 0; i < perAgg; i++ {
		var specs []Spec
		for _, a := range aggs {
			specs = append(specs, Spec{Type: "test.value", AggregateID: a, Payload: wrapperspb.String(fmt.Sprintf("%s/%02d", a, i))})
		}
		if err := f.ob.Commit(ctx, nil, specs...); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	seen := map[string][]string{}
	c := NewConsumer(ConsumerConfig{
		Name: "order", Stream: "test", Client: f.client, API: f.api, Table: f.table, Log: testutil.Logger(t), Workers: 4,
		Handler: func(_ context.Context, ev *eventsv1.Event) error {
			time.Sleep(time.Duration(rand.IntN(3)) * time.Millisecond) // shuffle completion order
			var v wrapperspb.StringValue
			_ = ev.Payload.UnmarshalTo(&v)
			mu.Lock()
			seen[ev.AggregateId] = append(seen[ev.AggregateId], v.Value)
			mu.Unlock()
			return nil
		},
	})
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = c.Run(cctx) }()
	total := perAgg * len(aggs)
	testutil.Eventually(t, 10*time.Second, func() bool { return c.Status().Cursor == int64(total) }, "cursor %d", c.Status().Cursor)
	mu.Lock()
	defer mu.Unlock()
	for _, a := range aggs {
		if len(seen[a]) != perAgg || !sort.StringsAreSorted(seen[a]) {
			t.Fatalf("aggregate %s applied out of order or incompletely: %v", a, seen[a])
		}
	}
}

func TestCursorNeverPassesAnUnfinishedEvent(t *testing.T) {
	f := newFixture(t, ServerOptions{})
	ctx := context.Background()
	for i := 1; i <= 10; i++ {
		if err := f.ob.Commit(ctx, nil, Spec{Type: "test.value", AggregateID: fmt.Sprintf("agg%d", i), Payload: wrapperspb.String(fmt.Sprintf("e%d", i))}); err != nil {
			t.Fatal(err)
		}
	}
	release := make(chan struct{})
	var mu sync.Mutex
	var applied []string
	handler := func(ctx context.Context, ev *eventsv1.Event) error {
		var v wrapperspb.StringValue
		_ = ev.Payload.UnmarshalTo(&v)
		if v.Value == "e4" {
			select { // a slow event, e.g. a large re-plan
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		mu.Lock()
		applied = append(applied, v.Value)
		mu.Unlock()
		return nil
	}
	mk := func() *Consumer {
		return NewConsumer(ConsumerConfig{Name: "wm", Stream: "test", Client: f.client, API: f.api, Table: f.table, Log: testutil.Logger(t), Workers: 4, Handler: handler})
	}
	c1 := mk()
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = c1.Run(cctx); close(done) }()
	// Events on e4's worker queue behind it (head-of-line blocking within a
	// partition); every other event completes.
	expect := 0
	for i := 1; i <= 10; i++ {
		if i != 4 && (i < 4 || partition(fmt.Sprintf("agg%d", i), 4) != partition("agg4", 4)) {
			expect++
		}
	}
	testutil.Eventually(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(applied) == expect
	}, "events not behind e4 should complete while it is stuck")
	time.Sleep(200 * time.Millisecond) // give the committer time to (wrongly) run ahead
	if cur := c1.Status().Cursor; cur != 3 {
		t.Fatalf("cursor %d passed the unfinished event 4", cur)
	}
	cancel() // crash while e4 is in flight
	<-done

	// On restart e4..e10 are redelivered (at-least-once); e1..e3 are not.
	mu.Lock()
	applied = nil
	mu.Unlock()
	close(release)
	c2 := mk()
	cctx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	go func() { _ = c2.Run(cctx2) }()
	testutil.Eventually(t, 5*time.Second, func() bool { return c2.Status().Cursor == 10 }, "cursor %d", c2.Status().Cursor)
	mu.Lock()
	defer mu.Unlock()
	got := strings.Join(sorted(applied), ",")
	if strings.Contains(","+got+",", ",e1,") || strings.Contains(","+got+",", ",e3,") || !strings.Contains(got, "e4") {
		t.Fatalf("redelivery after restart: %s", got)
	}
}

func TestWatermarkFoldsCompletedPrefix(t *testing.T) {
	w := newWatermark(0, 3)
	for seq := int64(1); seq <= 3; seq++ {
		if !w.dispatch(seq) {
			t.Fatal("dispatch failed")
		}
	}
	if m := w.complete(2); m != 0 {
		t.Fatalf("mark %d after completing 2 before 1", m)
	}
	blocked := make(chan bool)
	go func() { blocked <- w.dispatch(4) }() // limit reached: waits
	select {
	case <-blocked:
		t.Fatal("dispatch should block at the in-flight limit")
	case <-time.After(50 * time.Millisecond):
	}
	if m := w.complete(1); m != 2 {
		t.Fatalf("mark %d, want 2", m)
	}
	if !<-blocked {
		t.Fatal("dispatch should resume after completions")
	}
	w.complete(4)
	if m := w.complete(3); m != 4 {
		t.Fatalf("mark %d, want 4", m)
	}
	w.close()
	if w.dispatch(5) {
		t.Fatal("dispatch after close")
	}
}

// A failed write leaves a tombstone. Subscribers get it as a position
// marker: the consumer commits past it without calling the handler, so its
// cursor reaches the head (no permanent "lag" after failed writes, and no
// re-reading them after a restart).
func TestConsumerCommitsPastTombstones(t *testing.T) {
	f := newFixture(t, ServerOptions{})
	ctx := context.Background()
	if err := f.ob.Commit(ctx, []types.TransactWriteItem{putItem(f.table, "X")}, spec("x")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // e.g. re-running a seed: every create already exists
		if err := f.ob.Commit(ctx, []types.TransactWriteItem{putItem(f.table, "X")}, spec("dup")); err == nil {
			t.Fatal("expected a conditional failure")
		}
	}
	var mu sync.Mutex
	var applied []string
	c := NewConsumer(ConsumerConfig{
		Name: "tomb", Stream: "test", Client: f.client, API: f.api, Table: f.table, Log: testutil.Logger(t), Workers: 2,
		Handler: func(_ context.Context, ev *eventsv1.Event) error {
			mu.Lock()
			defer mu.Unlock()
			applied = append(applied, ev.Type)
			return nil
		},
	})
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = c.Run(cctx) }()
	var st Status
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if st = c.Status(); st.Cursor == 4 && st.Head == 4 {
			break
		}
	}
	if st.Cursor != 4 || st.Head != 4 {
		t.Fatalf("cursor %d, head %d: the consumer did not commit past the tombstones", st.Cursor, st.Head)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(applied) != 1 {
		t.Fatalf("handler saw %v; tombstones must not reach it", applied)
	}
	if lag := c.Status().Head - c.Status().Cursor; lag != 0 {
		t.Fatalf("lag %d after tombstones", lag)
	}
}
