package timeline

import (
	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
)

// InvalidateOn returns the function that drops this replica's cached copy
// of a timeline written by another replica (typically the one holding the
// consumer lease, while this one only serves reads).
func InvalidateOn(c *ddb.CachedAPI, table string) func(*eventsv1.Event) {
	return func(ev *eventsv1.Event) {
		if ev.Type != EventTimelineChanged {
			return
		}
		var e timelinev1.TimelineChanged
		if ev.Payload.UnmarshalTo(&e) == nil {
			c.Invalidate(table, tlKey(e.StudentId, e.ApplicationId), true)
		}
	}
}
