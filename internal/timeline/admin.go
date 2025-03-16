package timeline

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/events"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

// AdminService implements SyncAdminService (operators only).
type AdminService struct {
	timelinev1.UnimplementedSyncAdminServiceServer
	consumers []*events.Consumer
	syncer    *Syncer
	log       *slog.Logger
}

// NewAdminService creates the operator API.
func NewAdminService(consumers []*events.Consumer, syncer *Syncer, log *slog.Logger) *AdminService {
	return &AdminService{consumers: consumers, syncer: syncer, log: log}
}

func (s *AdminService) consumer(name string) (*events.Consumer, error) {
	for _, c := range s.consumers {
		if c.Name() == name {
			return c, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "no consumer %q", name)
}

// ListDeadLetters lists parked events.
func (s *AdminService) ListDeadLetters(ctx context.Context, req *timelinev1.ListDeadLettersRequest) (*timelinev1.ListDeadLettersResponse, error) {
	if _, err := platform.RequireRole(ctx, platform.RoleService); err != nil {
		return nil, err
	}
	limit := req.GetLimit()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cs := s.consumers
	if req.GetConsumer() != "" {
		c, err := s.consumer(req.GetConsumer())
		if err != nil {
			return nil, err
		}
		cs = []*events.Consumer{c}
	}
	out := &timelinev1.ListDeadLettersResponse{}
	for _, c := range cs {
		dls, err := c.DeadLetters(ctx, limit)
		if err != nil {
			return nil, platform.InternalError(ctx, s.log, "list dead letters", err)
		}
		for _, d := range dls {
			out.DeadLetters = append(out.DeadLetters, &timelinev1.DeadLetter{
				Consumer: d.Consumer, Seq: d.Event.Seq, Type: d.Event.Type, AggregateId: d.Event.AggregateId,
				Error: d.Error, ParkedAt: timestamppb.New(d.ParkedAt),
			})
		}
	}
	return out, nil
}

// ReplayDeadLetter re-applies one parked event.
func (s *AdminService) ReplayDeadLetter(ctx context.Context, req *timelinev1.ReplayDeadLetterRequest) (*timelinev1.ReplayDeadLetterResponse, error) {
	p, err := platform.RequireRole(ctx, platform.RoleService)
	if err != nil {
		return nil, err
	}
	c, err := s.consumer(req.GetConsumer())
	if err != nil {
		return nil, err
	}
	err = c.Replay(ctx, req.GetSeq())
	switch {
	case errors.Is(err, events.ErrNoDeadLetter):
		return nil, status.Errorf(codes.NotFound, "no dead letter %d for %s", req.GetSeq(), req.GetConsumer())
	case err != nil:
		s.log.WarnContext(ctx, "dead letter replay failed", "consumer", c.Name(), "seq", req.GetSeq(), "by", p.UserID, "error", err)
		return &timelinev1.ReplayDeadLetterResponse{Applied: false, Error: err.Error()}, nil
	}
	s.log.InfoContext(ctx, "dead letter replay", "consumer", c.Name(), "seq", req.GetSeq(), "by", p.UserID)
	return &timelinev1.ReplayDeadLetterResponse{Applied: true}, nil
}

// Resync re-plans timelines whose plan would change.
func (s *AdminService) Resync(ctx context.Context, req *timelinev1.ResyncRequest) (*timelinev1.ResyncResponse, error) {
	p, err := platform.RequireRole(ctx, platform.RoleService)
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "resync requested", "student_id", req.GetStudentId(), "by", p.UserID)
	n, changed, err := s.syncer.Resync(ctx, req.GetStudentId())
	if err != nil {
		return nil, platform.InternalError(ctx, s.log, "resync", err)
	}
	return &timelinev1.ResyncResponse{Timelines: int64(n), Replanned: int64(changed)}, nil
}
