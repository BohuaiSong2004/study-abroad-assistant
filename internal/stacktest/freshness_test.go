package stacktest_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
	. "github.com/BohuaiSong2004/study-abroad-assistant/internal/stacktest"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/timeline"
)

// The browser reloads its timelines when the gateway relays a TimelineChanged
// event. That reload must see the change, whatever the cache: with DAX the
// query cache ignores writes, and with several replicas the reading replica's
// cache may not have caught up when the notification arrives. Reloads after a
// notification therefore ask for a consistent read.
//
// Run under every cache mode with WAYPOINT_TEST_CACHE_MODE=off|local|daxsim.
func TestReloadAfterChangeNotificationSeesTheChange(t *testing.T) {
	s := New(t, ddb.CacheLocal)
	prog, err := s.Catalog.CreateProgram(Counselor(), &catalogv1.CreateProgramRequest{Program: Program("fresh-mscs")})
	if err != nil {
		t.Fatal(err)
	}
	me, _ := s.Student("fresh@example.com", "Fresh Reader")
	app, err := s.Apps.CreateApplication(me, &appsv1.CreateApplicationRequest{ProgramId: prog.Id, Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1})
	if err != nil {
		t.Fatal(err)
	}
	s.WaitTimeline(me, app.Id, nil)

	deadlineOf := func(consistent bool) string {
		res, err := s.Timeline.ListTimelines(me, &timelinev1.ListTimelinesRequest{Consistent: consistent})
		if err != nil || len(res.Timelines) != 1 {
			t.Fatalf("list: %v %v", res, err)
		}
		return res.Timelines[0].Deadline
	}
	agendaHas := func(date string) bool {
		res, err := s.Timeline.GetAgenda(me, &timelinev1.GetAgendaRequest{From: "2026-12-01", To: "2027-02-01", Consistent: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range res.Items {
			if m := it.GetMilestone(); m.GetId() == "gen:submit" && m.GetDueDate() == date {
				return true
			}
		}
		return false
	}
	if deadlineOf(false) != "2027-01-15" { // also populates the query cache
		t.Fatal("initial list")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := eventsv1.NewEventStreamServiceClient(s.TimelineConn).Subscribe(platform.WithPrincipal(ctx, platform.Principal{UserID: "svc:gateway", Role: platform.RoleService}), &eventsv1.SubscribeRequest{FromTail: true})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond) // positioned at the tail

	upd := proto.Clone(prog).(*catalogv1.Program)
	upd.Deadlines[0].Date = "2027-01-05"
	if _, err := s.Catalog.UpdateProgram(Counselor(), &catalogv1.UpdateProgramRequest{Program: upd, ExpectedVersion: prog.Version}); err != nil {
		t.Fatal(err)
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if ev := msg.GetEvent(); ev != nil && ev.Type == timeline.EventTimelineChanged {
			break
		}
	}
	// The notification has arrived: a consistent reload sees the re-plan.
	if got := deadlineOf(true); got != "2027-01-05" {
		t.Fatalf("consistent reload after notification: deadline %s", got)
	}
	if !agendaHas("2027-01-05") {
		t.Fatal("consistent agenda reload after notification misses the moved deadline")
	}
	// What a cached reload would have shown, per cache contract.
	cached := deadlineOf(false)
	switch s.CacheMode {
	case ddb.CacheDAXSim:
		if cached != "2027-01-15" {
			t.Fatalf("DAX query cache should still hold the old list (TTL-only expiry), got %s", cached)
		}
	default: // off, or the local cache invalidated by the writer in this process
		if cached != "2027-01-05" {
			t.Fatalf("cache mode %s served a stale list: %s", s.CacheMode, cached)
		}
	}
}
