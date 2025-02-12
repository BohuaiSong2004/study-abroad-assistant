package timeline

import (
	"context"
	"testing"
	"time"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/events"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/testutil"
)

// Three replicas of the timeline service share a table but each has its own
// in-process cache. A (the consumer lease holder) re-plans a timeline; B
// watches the outbox and must serve the new version within a poll interval;
// C (no watcher) shows the problem the watcher solves: it keeps serving the
// stale copy until its TTL.
func TestReplicaCachesFollowWritesFromOtherReplicas(t *testing.T) {
	c, prefix := testutil.Tables(t, ddb.CacheOff)
	table := prefix + ddb.TableTimeline
	log := testutil.Logger(t)
	replica := func() (*ddb.CachedAPI, *Store, *events.Outbox) {
		cache := ddb.NewCachedAPI(c.Raw, ddb.CacheOptions{ItemTTL: time.Hour})
		ob := events.NewOutbox(cache, table, "timeline", log)
		return cache, NewStore(cache, c.Raw, table, ob), ob
	}
	_, storeA, _ := replica()
	cacheB, storeB, obB := replica()
	_, storeC, _ := replica()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = obB.Watch(ctx, events.ServerOptions{PollInterval: 50 * time.Millisecond}, InvalidateOn(cacheB, table))
	}()
	time.Sleep(200 * time.Millisecond) // watcher positioned at the head

	tl := &timelinev1.Timeline{ApplicationId: "app_1", StudentId: "s1", ProgramId: "p1", Deadline: "2027-01-15", Version: 1}
	app := &appsv1.Application{Id: "app_1", StudentId: "s1"}
	if err := storeA.Put(ctx, &record{tl: tl, app: app}, 0); err != nil {
		t.Fatal(err)
	}
	deadline := func(s *Store) string {
		r, err := s.Get(ctx, "s1", "app_1", false)
		if err != nil {
			t.Fatal(err)
		}
		return r.tl.Deadline
	}
	if deadline(storeB) != "2027-01-15" || deadline(storeC) != "2027-01-15" {
		t.Fatal("initial read")
	}
	// A re-plans after a deadline change.
	tl2 := &timelinev1.Timeline{ApplicationId: "app_1", StudentId: "s1", ProgramId: "p1", Deadline: "2027-01-05", Version: 2}
	if err := storeA.Put(ctx, &record{tl: tl2, app: app}, 1); err != nil {
		t.Fatal(err)
	}
	testutil.Eventually(t, 2*time.Second, func() bool { return deadline(storeB) == "2027-01-05" }, "replica B kept serving its stale cached timeline")
	if deadline(storeC) != "2027-01-15" {
		t.Fatal("control replica unexpectedly fresh: the test no longer demonstrates staleness")
	}
}
