package timeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/applications"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/catalog"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/events"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

// Syncer applies catalog and applications events to timelines.
//
// Idempotency: every timeline records the application version and program
// version it was planned from. An event is a no-op unless it carries a newer
// version, so redelivery after a crash, or the same program arriving both
// from the stream and from a fallback fetch, cannot regress a timeline.
//
// Cross-stream ordering: the two streams are independent, so an application
// can reference a program the catalog consumer has not projected yet. The
// application handler then fetches the program from the catalog over gRPC.
// Conversely, a program update can race with a timeline being created:
// re-planning finds timelines through an eventually consistent GSI, so it
// runs again shortly after (RecheckDelay) to catch late arrivals.
type Syncer struct {
	store        *Store
	catalog      catalogv1.CatalogServiceClient
	log          *slog.Logger
	now          func() time.Time
	RecheckDelay time.Duration
	// ReplanParallelism bounds concurrent re-plans after a program change.
	ReplanParallelism int
	root              context.Context
	stop              context.CancelFunc
}

// NewSyncer creates a syncer.
func NewSyncer(store *Store, cc catalogv1.CatalogServiceClient, log *slog.Logger) *Syncer {
	root, stop := context.WithCancel(context.Background())
	return &Syncer{store: store, catalog: cc, log: log, now: time.Now, RecheckDelay: 3 * time.Second, ReplanParallelism: 8, root: root, stop: stop}
}

// Close cancels pending background rechecks.
func (s *Syncer) Close() { s.stop() }

// HandleCatalog applies a catalog stream event.
func (s *Syncer) HandleCatalog(ctx context.Context, ev *eventsv1.Event) error {
	if ev.Type != catalog.EventProgramUpserted {
		return nil // unknown types are ignored for forward compatibility
	}
	var pu catalogv1.ProgramUpserted
	if err := ev.Payload.UnmarshalTo(&pu); err != nil {
		return events.Permanent(fmt.Errorf("decode ProgramUpserted: %w", err))
	}
	if pu.Program == nil {
		return events.Permanent(errors.New("ProgramUpserted without a program"))
	}
	p := pu.Program
	if _, err := s.store.PutProgram(ctx, p); err != nil {
		return err
	}
	// Re-plan even if the projection was already current: a crash between
	// the projection write and the re-plans must not leave timelines behind.
	if err := s.replanProgram(ctx, p); err != nil {
		return err
	}
	go func() {
		select {
		case <-s.root.Done():
		case <-time.After(s.RecheckDelay):
			if err := s.replanProgram(platform.ServiceContext(s.root, "timeline"), p); err != nil && s.root.Err() == nil {
				s.log.Warn("program recheck failed", "program_id", p.Id, "error", err)
			}
		}
	}()
	return nil
}

func (s *Syncer) replanProgram(ctx context.Context, p *catalogv1.Program) error {
	// Never plan with an older snapshot than the projection holds.
	if cur, err := s.store.Program(ctx, p.Id); err == nil && cur != nil && cur.Version > p.Version {
		p = cur
	}
	stale, err := s.store.StaleForProgram(ctx, p.Id, p.Version)
	if err != nil {
		return err
	}
	// Timelines are independent items: re-plan them with bounded parallelism.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(s.ReplanParallelism)
	for _, ref := range stale {
		g.Go(func() error {
			return s.upsert(gctx, ref[0], ref[1], func(prev *record) (*record, error) {
				if prev == nil || prev.tl.ProgramVersion >= p.Version {
					return nil, nil
				}
				return &record{tl: Plan(p, prev.app, prev.tl, s.now()), app: prev.app}, nil
			})
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	if len(stale) > 0 {
		s.log.Info("re-planned timelines after program change", "program_id", p.Id, "version", p.Version, "timelines", len(stale))
	}
	return nil
}

// Resync re-plans timelines (all, or one student's) from the application
// snapshot each one stores and the current program, and writes only those
// whose plan changes: the repair after a planner fix is deployed. It returns
// how many timelines were examined and how many changed.
func (s *Syncer) Resync(ctx context.Context, sid string) (int, int, error) {
	refs, err := s.store.Refs(ctx, sid)
	if err != nil {
		return 0, 0, err
	}
	var changed atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(s.ReplanParallelism)
	for _, ref := range refs {
		g.Go(func() error {
			return s.upsert(gctx, ref[0], ref[1], func(prev *record) (*record, error) {
				if prev == nil {
					return nil, nil // deleted meanwhile
				}
				p, err := s.program(gctx, prev.tl.ProgramId)
				if err != nil {
					return nil, err
				}
				if p.Version < prev.tl.ProgramVersion {
					return nil, nil // never plan with an older program
				}
				next := Plan(p, prev.app, prev.tl, s.now())
				if samePlan(prev.tl, next) {
					return nil, nil
				}
				changed.Add(1)
				return &record{tl: next, app: prev.app}, nil
			})
		})
	}
	if err := g.Wait(); err != nil {
		return len(refs), int(changed.Load()), err
	}
	s.log.Info("resync finished", "timelines", len(refs), "replanned", changed.Load())
	return len(refs), int(changed.Load()), nil
}

// samePlan compares two timelines ignoring the bookkeeping fields that every
// write changes.
func samePlan(a, b *timelinev1.Timeline) bool {
	x, y := proto.Clone(a).(*timelinev1.Timeline), proto.Clone(b).(*timelinev1.Timeline)
	for _, t := range []*timelinev1.Timeline{x, y} {
		t.Version, t.UpdatedAt = 0, nil
	}
	return proto.Equal(x, y)
}

// HandleApplications applies an applications stream event.
func (s *Syncer) HandleApplications(ctx context.Context, ev *eventsv1.Event) error {
	switch ev.Type {
	case applications.EventApplicationUpserted:
		var au appsv1.ApplicationUpserted
		if err := ev.Payload.UnmarshalTo(&au); err != nil {
			return events.Permanent(fmt.Errorf("decode ApplicationUpserted: %w", err))
		}
		if au.Application == nil {
			return events.Permanent(errors.New("ApplicationUpserted without an application"))
		}
		return s.applyApplication(ctx, au.Application)
	case applications.EventApplicationDeleted:
		var ad appsv1.ApplicationDeleted
		if err := ev.Payload.UnmarshalTo(&ad); err != nil {
			return events.Permanent(err)
		}
		_, err := s.store.Delete(ctx, ad.StudentId, ad.ApplicationId, ad.Version)
		return err
	}
	return nil
}

func (s *Syncer) applyApplication(ctx context.Context, a *appsv1.Application) error {
	var program *catalogv1.Program
	return s.upsert(ctx, a.StudentId, a.Id, func(prev *record) (*record, error) {
		if prev != nil && prev.tl.ApplicationVersion >= a.Version {
			return nil, nil // already applied
		}
		if program == nil {
			p, err := s.program(ctx, a.ProgramId)
			if err != nil {
				return nil, err
			}
			program = p
		}
		// A program snapshot older than the one already planned with means
		// the projection lagged; keep the newer one.
		if prev != nil && prev.tl.ProgramVersion > program.Version {
			if p, err := s.store.Program(ctx, a.ProgramId); err == nil && p != nil && p.Version >= prev.tl.ProgramVersion {
				program = p
			}
		}
		var prevTL *timelinev1.Timeline
		if prev != nil {
			prevTL = prev.tl
		}
		return &record{tl: Plan(program, a, prevTL, s.now()), app: a}, nil
	})
}

// program returns the projected program, fetching it from the catalog when
// the catalog stream has not delivered it yet.
func (s *Syncer) program(ctx context.Context, pid string) (*catalogv1.Program, error) {
	p, err := s.store.Program(ctx, pid)
	if err != nil {
		return nil, err
	}
	if p != nil {
		return p, nil
	}
	p, err = s.catalog.GetProgram(platform.ServiceContext(ctx, "timeline"), &catalogv1.GetProgramRequest{Id: pid})
	if status.Code(err) == codes.NotFound {
		return nil, events.Permanent(fmt.Errorf("program %s does not exist", pid))
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.store.PutProgram(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// upsert is an optimistic read-modify-write of one timeline. next returns
// the new timeline, or nil for "no change".
func (s *Syncer) upsert(ctx context.Context, sid, aid string, next func(prev *record) (*record, error)) error {
	return upsert(ctx, s.store, s.now, sid, aid, next)
}

func upsert(ctx context.Context, store *Store, now func() time.Time, sid, aid string, next func(prev *record) (*record, error)) error {
	for attempt := 0; attempt < 8; attempt++ {
		prev, err := store.Get(ctx, sid, aid, true)
		if errors.Is(err, errNotFound) {
			prev, err = nil, nil
		}
		if err != nil {
			return err
		}
		r, err := next(prev)
		if err != nil || r == nil {
			return err
		}
		var prevVersion int64
		if prev != nil {
			prevVersion = prev.tl.Version
		}
		r.tl.Version = prevVersion + 1
		r.tl.UpdatedAt = timestamppb.New(now())
		err = store.Put(ctx, r, prevVersion)
		if errors.Is(err, errConflict) {
			continue // a concurrent writer (other stream or the API) won; retry
		}
		return err
	}
	return errConflict
}
