package timeline

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/civil"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/events"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ids"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

// MaxCustomMilestones per timeline.
const MaxCustomMilestones = 30

// Service implements TimelineService.
type Service struct {
	timelinev1.UnimplementedTimelineServiceServer
	store     *Store
	consumers []*events.Consumer
	log       *slog.Logger
	now       func() time.Time
}

// NewService creates the service.
func NewService(store *Store, consumers []*events.Consumer, log *slog.Logger) *Service {
	return &Service{store: store, consumers: consumers, log: log, now: time.Now}
}

func (s *Service) view(t *timelinev1.Timeline) *timelinev1.Timeline {
	return WithOverdue(proto.Clone(t).(*timelinev1.Timeline), s.now())
}

func (s *Service) mapErr(ctx context.Context, err error) error {
	if _, ok := status.FromError(err); ok && status.Code(err) != codes.Unknown {
		return err
	}
	switch {
	case errors.Is(err, errNotFound):
		return status.Error(codes.NotFound, "timeline not found (a new application's timeline appears within a moment)")
	case errors.Is(err, errConflict):
		return status.Error(codes.Aborted, "timeline changed concurrently; try again")
	}
	return platform.InternalError(ctx, s.log, "timeline", err)
}

// GetTimeline returns one of the caller's timelines.
func (s *Service) GetTimeline(ctx context.Context, req *timelinev1.GetTimelineRequest) (*timelinev1.Timeline, error) {
	me, err := platform.RequireRole(ctx, platform.RoleStudent)
	if err != nil {
		return nil, err
	}
	r, err := s.store.Get(ctx, me.UserID, req.GetApplicationId(), req.GetConsistent())
	if err != nil {
		return nil, s.mapErr(ctx, err)
	}
	return s.view(r.tl), nil
}

// ListTimelines returns all of the caller's timelines, soonest deadline first.
func (s *Service) ListTimelines(ctx context.Context, req *timelinev1.ListTimelinesRequest) (*timelinev1.ListTimelinesResponse, error) {
	me, err := platform.RequireRole(ctx, platform.RoleStudent)
	if err != nil {
		return nil, err
	}
	ts, err := s.store.List(ctx, me.UserID, req.GetConsistent())
	if err != nil {
		return nil, s.mapErr(ctx, err)
	}
	out := &timelinev1.ListTimelinesResponse{}
	for _, t := range ts {
		out.Timelines = append(out.Timelines, s.view(t))
	}
	sort.SliceStable(out.Timelines, func(i, j int) bool { return out.Timelines[i].Deadline < out.Timelines[j].Deadline })
	return out, nil
}

// GetAgenda flattens milestones across all timelines into a dated list.
// Overdue items are always included, whatever the window.
func (s *Service) GetAgenda(ctx context.Context, req *timelinev1.GetAgendaRequest) (*timelinev1.GetAgendaResponse, error) {
	me, err := platform.RequireRole(ctx, platform.RoleStudent)
	if err != nil {
		return nil, err
	}
	// The default window is relative to the student's own calendar day: in
	// Los Angeles at 8 pm it is still "today" there, although UTC has moved
	// on. (Overdue flags stay relative to each program's time zone, because
	// that is where the deadline is.)
	zone := req.GetTimeZone()
	if zone == "" {
		zone = "UTC"
	} else if !civil.ValidZone(zone) {
		return nil, status.Error(codes.InvalidArgument, "unknown time zone")
	}
	today := civil.Today(s.now(), zone)
	from, to := today, today.AddDays(30)
	if req.GetFrom() != "" {
		if from, err = civil.Parse(req.GetFrom()); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	}
	if req.GetTo() != "" {
		if to, err = civil.Parse(req.GetTo()); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	}
	if to.Before(from) || from.DaysUntil(to) > 366 {
		return nil, status.Error(codes.InvalidArgument, "window must be 0-366 days")
	}
	ts, err := s.store.List(ctx, me.UserID, req.GetConsistent())
	if err != nil {
		return nil, s.mapErr(ctx, err)
	}
	out := &timelinev1.GetAgendaResponse{}
	for _, t := range ts {
		t = s.view(t)
		for _, m := range t.Milestones {
			if m.Done && !req.GetIncludeDone() {
				continue
			}
			inWindow := m.DueDate >= from.String() && m.DueDate <= to.String()
			if !inWindow && !m.Overdue {
				continue
			}
			out.Items = append(out.Items, &timelinev1.AgendaItem{ApplicationId: t.ApplicationId, ProgramName: t.ProgramName, University: t.University, Milestone: m})
		}
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		a, b := out.Items[i].Milestone, out.Items[j].Milestone
		if a.DueDate != b.DueDate {
			return a.DueDate < b.DueDate
		}
		return out.Items[i].ApplicationId < out.Items[j].ApplicationId
	})
	return out, nil
}

func validateCustom(title, desc, due *string) error {
	if title != nil {
		*title = strings.Join(strings.Fields(*title), " ")
		if *title == "" || utf8.RuneCountInString(*title) > 120 {
			return status.Error(codes.InvalidArgument, "title must be 1-120 characters")
		}
	}
	if desc != nil {
		*desc = strings.TrimSpace(*desc)
		if utf8.RuneCountInString(*desc) > 500 {
			return status.Error(codes.InvalidArgument, "description must be at most 500 characters")
		}
	}
	if due != nil {
		if _, err := civil.Parse(*due); err != nil {
			return status.Error(codes.InvalidArgument, "due_date must be YYYY-MM-DD")
		}
	}
	return nil
}

// editCustom runs fn on the caller's timeline and saves it.
func (s *Service) editCustom(ctx context.Context, aid string, fn func(t *timelinev1.Timeline) error) (*timelinev1.Timeline, error) {
	me, err := platform.RequireRole(ctx, platform.RoleStudent)
	if err != nil {
		return nil, err
	}
	var saved *timelinev1.Timeline
	err = upsert(ctx, s.store, s.now, me.UserID, aid, func(prev *record) (*record, error) {
		if prev == nil {
			return nil, errNotFound
		}
		t := proto.Clone(prev.tl).(*timelinev1.Timeline)
		if err := fn(t); err != nil {
			return nil, err
		}
		SortMilestones(t.Milestones)
		saved = t
		return &record{tl: t, app: prev.app}, nil
	})
	if err != nil {
		return nil, s.mapErr(ctx, err)
	}
	return s.view(saved), nil
}

func findCustom(t *timelinev1.Timeline, id string) (int, error) {
	for i, m := range t.Milestones {
		if m.Id == id {
			if m.Source != timelinev1.MilestoneSource_MILESTONE_SOURCE_CUSTOM {
				return -1, status.Error(codes.FailedPrecondition, "this milestone is tracked automatically; update the application's checklist or recommendations instead")
			}
			return i, nil
		}
	}
	return -1, status.Error(codes.NotFound, "milestone not found")
}

// AddMilestone adds a custom milestone.
func (s *Service) AddMilestone(ctx context.Context, req *timelinev1.AddMilestoneRequest) (*timelinev1.Timeline, error) {
	title, desc, due := req.GetTitle(), req.GetDescription(), req.GetDueDate()
	if err := validateCustom(&title, &desc, &due); err != nil {
		return nil, err
	}
	return s.editCustom(ctx, req.GetApplicationId(), func(t *timelinev1.Timeline) error {
		if len(customOnly(t)) >= MaxCustomMilestones {
			return status.Errorf(codes.ResourceExhausted, "at most %d custom milestones", MaxCustomMilestones)
		}
		t.Milestones = append(t.Milestones, &timelinev1.Milestone{
			Id: "c:" + ids.New(""), Kind: timelinev1.MilestoneKind_MILESTONE_KIND_CUSTOM,
			Source: timelinev1.MilestoneSource_MILESTONE_SOURCE_CUSTOM, Title: title, Description: desc, DueDate: due,
		})
		return nil
	})
}

// UpdateMilestone edits a custom milestone.
func (s *Service) UpdateMilestone(ctx context.Context, req *timelinev1.UpdateMilestoneRequest) (*timelinev1.Timeline, error) {
	if err := validateCustom(req.Title, req.Description, req.DueDate); err != nil {
		return nil, err
	}
	return s.editCustom(ctx, req.GetApplicationId(), func(t *timelinev1.Timeline) error {
		i, err := findCustom(t, req.GetMilestoneId())
		if err != nil {
			return err
		}
		m := t.Milestones[i]
		if req.Done != nil {
			m.Done = req.GetDone()
		}
		if req.Title != nil {
			m.Title = req.GetTitle()
		}
		if req.Description != nil {
			m.Description = req.GetDescription()
		}
		if req.DueDate != nil {
			m.DueDate = req.GetDueDate()
		}
		return nil
	})
}

// DeleteMilestone removes a custom milestone.
func (s *Service) DeleteMilestone(ctx context.Context, req *timelinev1.DeleteMilestoneRequest) (*timelinev1.Timeline, error) {
	return s.editCustom(ctx, req.GetApplicationId(), func(t *timelinev1.Timeline) error {
		i, err := findCustom(t, req.GetMilestoneId())
		if err != nil {
			return err
		}
		t.Milestones = append(t.Milestones[:i], t.Milestones[i+1:]...)
		return nil
	})
}

// GetSyncStatus reports the consumers' progress.
func (s *Service) GetSyncStatus(ctx context.Context, _ *timelinev1.GetSyncStatusRequest) (*timelinev1.GetSyncStatusResponse, error) {
	if _, err := platform.RequireRole(ctx, platform.RoleCounselor, platform.RoleService); err != nil {
		return nil, err
	}
	out := &timelinev1.GetSyncStatusResponse{}
	for _, c := range s.consumers {
		st := c.Status()
		dead, err := c.CountDeadLetters(ctx)
		if err != nil {
			return nil, s.mapErr(ctx, err)
		}
		ss := &timelinev1.StreamStatus{
			Stream: st.Stream, Cursor: st.Cursor, HeadSeq: st.Head, Connected: st.Connected,
			Leader: st.Leader, EventsApplied: st.Applied, DeadLetters: dead,
		}
		if !st.LastEventAt.IsZero() {
			ss.LastEventAt = timestamppb.New(st.LastEventAt)
		}
		out.Streams = append(out.Streams, ss)
	}
	return out, nil
}
