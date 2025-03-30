package events

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
)

var gapsSkipped = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "waypoint_events_gaps_skipped_total",
	Help: "Sequence numbers skipped by subscribers after the gap timeout.",
}, []string{"stream"})

// ServerOptions tunes Server.
type ServerOptions struct {
	// PollInterval bounds how long a subscriber waits for events written by
	// other replicas of this service (local writes wake it immediately).
	PollInterval time.Duration
	// GapTimeout is how long to wait for an allocated-but-uncommitted
	// sequence number before skipping it.
	GapTimeout time.Duration
	// Heartbeat interval.
	Heartbeat time.Duration
	PageSize  int32
}

// Server implements EventStreamService on top of an Outbox.
type Server struct {
	eventsv1.UnimplementedEventStreamServiceServer
	ob   *Outbox
	opts ServerOptions
	log  *slog.Logger
}

// NewServer creates the stream server.
func NewServer(ob *Outbox, log *slog.Logger, opts ServerOptions) *Server {
	if opts.PollInterval <= 0 {
		opts.PollInterval = 250 * time.Millisecond
	}
	if opts.GapTimeout <= 0 {
		opts.GapTimeout = 5 * time.Second
	}
	if opts.Heartbeat <= 0 {
		opts.Heartbeat = 5 * time.Second
	}
	if opts.PageSize <= 0 {
		opts.PageSize = 200
	}
	return &Server{ob: ob, opts: opts, log: log}
}

// Head returns the current head sequence number.
func (s *Server) Head(ctx context.Context, _ *eventsv1.HeadRequest) (*eventsv1.HeadResponse, error) {
	h, err := s.ob.Head(ctx)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "read head: "+err.Error())
	}
	return &eventsv1.HeadResponse{Stream: s.ob.stream, HeadSeq: h}, nil
}

// Subscribe streams events with seq > after_seq in order, forever.
//
// Ordering guarantee: events are delivered strictly in sequence order. When
// the next expected number is missing but a later one exists, the writer of
// the missing number is either still committing (it will appear within
// milliseconds) or failed without writing a tombstone. The server waits up to
// GapTimeout from first noticing the hole, then skips it.
func (s *Server) Subscribe(req *eventsv1.SubscribeRequest, st eventsv1.EventStreamService_SubscribeServer) error {
	ctx := st.Context()
	next := req.GetAfterSeq() + 1
	if req.GetFromTail() {
		h, err := s.ob.Head(ctx)
		if err != nil {
			return status.Error(codes.Unavailable, "read head: "+err.Error())
		}
		next = h + 1
	}
	if next < 1 {
		next = 1
	}
	var gapSince time.Time
	lastBeat := time.Time{}
	for {
		wake := s.ob.notifier.Wait()
		evs, err := s.ob.read(ctx, next, s.opts.PageSize)
		if err != nil {
			if ctx.Err() != nil {
				return nil //nolint:nilerr // subscriber went away
			}
			return status.Error(codes.Unavailable, "read outbox: "+err.Error())
		}
		waitingOnGap := false
		for _, e := range evs {
			if e.seq > next {
				if gapSince.IsZero() {
					gapSince = time.Now()
				}
				if time.Since(gapSince) < s.opts.GapTimeout {
					waitingOnGap = true
					break
				}
				s.log.Warn("skipping event gap", "stream", s.ob.stream, "from", next, "to", e.seq-1)
				gapsSkipped.WithLabelValues(s.ob.stream).Add(float64(e.seq - next))
			}
			gapSince = time.Time{}
			next = e.seq + 1
			if e.typ == TypeNoop {
				continue
			}
			ev, err := toProto(s.ob.stream, e)
			if err != nil {
				s.log.Error("corrupt event; skipping", "stream", s.ob.stream, "seq", e.seq, "error", err)
				continue
			}
			if err := st.Send(&eventsv1.SubscribeResponse{Kind: &eventsv1.SubscribeResponse_Event{Event: ev}}); err != nil {
				return err
			}
		}
		if !waitingOnGap && len(evs) == int(s.opts.PageSize) {
			continue // more pages ready
		}
		if time.Since(lastBeat) >= s.opts.Heartbeat {
			h, err := s.ob.Head(ctx)
			if err == nil {
				if err := st.Send(&eventsv1.SubscribeResponse{Kind: &eventsv1.SubscribeResponse_Heartbeat{
					Heartbeat: &eventsv1.Heartbeat{HeadSeq: h, ServerTime: timestamppb.Now()},
				}}); err != nil {
					return err
				}
			}
			lastBeat = time.Now()
		}
		delay := s.opts.PollInterval
		if waitingOnGap {
			delay = 20 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func toProto(stream string, e stored) (*eventsv1.Event, error) {
	var payload anypb.Any
	if err := proto.Unmarshal(e.payload, &payload); err != nil {
		return nil, err
	}
	return &eventsv1.Event{
		Stream: stream, Seq: e.seq, Type: e.typ, AggregateId: e.agg, Payload: &payload,
		OccurredAt: timestamppb.New(time.UnixMilli(e.atMs)),
	}, nil
}
