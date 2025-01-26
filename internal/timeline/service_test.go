package timeline

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/events"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/testutil"
)

// Regression: the agenda's default window used the UTC day, so at 7:30 pm
// in Los Angeles (already tomorrow in UTC) it started a day late.
func TestAgendaWindowFollowsTheStudentsTimeZone(t *testing.T) {
	c, prefix := testutil.Tables(t, ddb.CacheLocal)
	table := prefix + ddb.TableTimeline
	store := NewStore(c.API, c.Raw, table, events.NewOutbox(c.API, table, "timeline", testutil.Logger(t)))
	ms := func(id, due string) *timelinev1.Milestone {
		return &timelinev1.Milestone{Id: id, Kind: timelinev1.MilestoneKind_MILESTONE_KIND_CUSTOM, Source: timelinev1.MilestoneSource_MILESTONE_SOURCE_CUSTOM, Title: id, DueDate: due}
	}
	tl := &timelinev1.Timeline{
		ApplicationId: "app_1", StudentId: "s1", ProgramId: "p1", Timezone: "America/Los_Angeles", Version: 1,
		Milestones: []*timelinev1.Milestone{ms("today-la", "2026-11-22"), ms("edge-la", "2026-12-22"), ms("edge-utc", "2026-12-23")},
	}
	if err := store.Put(context.Background(), &record{tl: tl, app: &appsv1.Application{Id: "app_1"}}, 0); err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, nil, testutil.Logger(t))
	svc.now = func() time.Time { return time.Date(2026, 11, 23, 3, 30, 0, 0, time.UTC) } // 19:30 Nov 22 in LA
	ctx := platform.WithPrincipal(context.Background(), platform.Principal{UserID: "s1", Role: platform.RoleStudent})
	ids := func(zone string) map[string]bool {
		res, err := svc.GetAgenda(ctx, &timelinev1.GetAgendaRequest{TimeZone: zone})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, it := range res.Items {
			out[it.Milestone.Id] = true
		}
		return out
	}
	la := ids("America/Los_Angeles")
	if !la["today-la"] || !la["edge-la"] || la["edge-utc"] {
		t.Fatalf("Los Angeles window wrong: %v", la)
	}
	utc := ids("")
	if utc["today-la"] || !utc["edge-utc"] {
		t.Fatalf("UTC window wrong: %v", utc)
	}
	if _, err := svc.GetAgenda(ctx, &timelinev1.GetAgendaRequest{TimeZone: "Mars/Olympus"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad zone: %v", err)
	}
}
