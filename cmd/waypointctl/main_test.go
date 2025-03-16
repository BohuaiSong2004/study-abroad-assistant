package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/applications"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/events"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/stacktest"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/testutil"
)

// An application event that references a program the catalog does not have
// yet is parked; once the program exists, the operator replays it.
func TestDeadLetterListReplayAndResync(t *testing.T) {
	s := stacktest.New(t, ddb.CacheLocal)
	c := clients{sync: s.Timeline, admin: s.Admin}
	ctx := platform.ServiceContext(context.Background(), "waypointctl")
	sh := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := run(ctx, c, args, &out)
		return out.String(), err
	}

	// An upstream bug publishes an application for a program that does not
	// exist (yet).
	ob := events.NewOutbox(s.DB.API, s.Prefix+ddb.TableApplications, "applications", testutil.Logger(t))
	app := &appsv1.Application{Id: "app_orphan", StudentId: "usr_orphan", ProgramId: "late-program", Version: 1, Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1}
	if err := ob.Commit(ctx, nil, events.Spec{Type: applications.EventApplicationUpserted, AggregateID: app.Id, Payload: &appsv1.ApplicationUpserted{Application: app}}); err != nil {
		t.Fatal(err)
	}
	testutil.Eventually(t, 5*time.Second, func() bool {
		out, err := sh("status")
		return err == nil && strings.Contains(out, "applications") && strings.HasSuffix(strings.TrimSpace(lineWith(out, "applications")), "1")
	}, "status never showed the parked event")

	out, err := sh("dlq", "list")
	if err != nil || !strings.Contains(out, "timeline.applications") || !strings.Contains(out, "app_orphan") || !strings.Contains(out, "late-program does not exist") {
		t.Fatalf("dlq list: %v\n%s", err, out)
	}
	seq := strings.Fields(lineWith(out, "app_orphan"))[1]

	// Replaying before the cause is fixed keeps it parked.
	if _, err := sh("dlq", "replay", "timeline.applications", seq); err == nil || !strings.Contains(err.Error(), "still failing") {
		t.Fatalf("replay before fix: %v", err)
	}
	if _, err := s.Catalog.CreateProgram(stacktest.Counselor(), &catalogv1.CreateProgramRequest{Program: stacktest.Program("late-program")}); err != nil {
		t.Fatal(err)
	}
	if out, err := sh("dlq", "replay", "timeline.applications", seq); err != nil || !strings.Contains(out, "applied") {
		t.Fatalf("replay after fix: %v %s", err, out)
	}
	if _, err := s.TimelineStore.Get(ctx, "usr_orphan", "app_orphan", true); err != nil {
		t.Fatalf("timeline after replay: %v", err)
	}
	if out, _ := sh("dlq", "list"); !strings.Contains(out, "no dead letters") {
		t.Fatalf("still parked:\n%s", out)
	}

	// A resync over healthy data changes nothing.
	if out, err := sh("resync"); err != nil || !strings.Contains(out, "re-planned 0") {
		t.Fatalf("resync: %v %s", err, out)
	}
	for _, bad := range [][]string{{}, {"dlq"}, {"dlq", "replay", "x"}, {"dlq", "replay", "x", "NaN"}, {"nope"}} {
		if _, err := sh(bad...); err != errUsage {
			t.Fatalf("%v: %v", bad, err)
		}
	}
}

func lineWith(out, s string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, s) {
			return l
		}
	}
	return ""
}
