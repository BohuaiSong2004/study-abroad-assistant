package catalog

import (
	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
)

// InvalidateOn returns the function that drops this replica's cached copy
// of whatever a catalog event (from any replica) changed.
func InvalidateOn(c *ddb.CachedAPI, table string) func(*eventsv1.Event) {
	return func(ev *eventsv1.Event) {
		if ev.Type == EventProgramUpserted {
			c.Invalidate(table, programKey(ev.AggregateId), true) // listing index too
		}
	}
}
