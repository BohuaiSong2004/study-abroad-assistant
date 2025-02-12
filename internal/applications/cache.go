package applications

import (
	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
)

// InvalidateOn returns the function that drops this replica's cached copy
// of an application another replica changed. (Sessions, tokens and
// recommendations are always read with strong consistency, so only the
// application item can be cached.)
func InvalidateOn(c *ddb.CachedAPI, table string) func(*eventsv1.Event) {
	return func(ev *eventsv1.Event) {
		switch ev.Type {
		case EventApplicationUpserted:
			var e appsv1.ApplicationUpserted
			if ev.Payload.UnmarshalTo(&e) == nil && e.Application != nil {
				c.Invalidate(table, appKey(e.Application.StudentId, e.Application.Id), false)
			}
		case EventApplicationDeleted:
			var e appsv1.ApplicationDeleted
			if ev.Payload.UnmarshalTo(&e) == nil {
				c.Invalidate(table, appKey(e.StudentId, e.ApplicationId), false)
			}
		}
	}
}
