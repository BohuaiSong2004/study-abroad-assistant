// Command timeline runs TimelineService and the consumers that keep
// timelines in sync with the catalog and applications streams.
package main

import (
	"context"

	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/events"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/svc"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/timeline"
)

func main() {
	platform.HealthcheckCommand()
	b, err := svc.New("timeline", ddb.TableTimeline)
	if err != nil {
		svc.Fatal("timeline", err)
	}
	table := b.Table(ddb.TableTimeline)
	catalogConn := platform.MustDial(platform.Env("CATALOG_ADDR", "dns:///localhost:9091"), b.Token)
	appsConn := platform.MustDial(platform.Env("APPLICATIONS_ADDR", "dns:///localhost:9092"), b.Token)

	ob := events.NewOutbox(b.DB.API, table, "timeline", b.Log)
	store := timeline.NewStore(b.DB.API, b.DB.Raw, table, ob)
	syncer := timeline.NewSyncer(store, catalogv1.NewCatalogServiceClient(catalogConn), b.Log)

	leaseTTL := platform.EnvDuration("CONSUMER_LEASE_TTL", 0)
	consumer := func(name, stream string, cc eventsv1.EventStreamServiceClient, h events.Handler) *events.Consumer {
		return events.NewConsumer(events.ConsumerConfig{
			Name: name, Stream: stream, Client: cc, API: b.DB.API, Table: table, Handler: h, Log: b.Log,
			Lease: events.NewLease(b.DB.API, table, name, b.Owner, leaseTTL),
		})
	}
	fromCatalog := consumer("timeline.catalog", "catalog", eventsv1.NewEventStreamServiceClient(catalogConn), syncer.HandleCatalog)
	fromApps := consumer("timeline.applications", "applications", eventsv1.NewEventStreamServiceClient(appsConn), syncer.HandleApplications)

	timelinev1.RegisterTimelineServiceServer(b.GRPC, timeline.NewService(store, []*events.Consumer{fromCatalog, fromApps}, b.Log))
	eventsv1.RegisterEventStreamServiceServer(b.GRPC, events.NewServer(ob, b.Log, events.ServerOptions{}))

	run := func(c *events.Consumer) func(context.Context) error {
		return func(ctx context.Context) error {
			return c.Run(platform.ServiceContext(ctx, "timeline"))
		}
	}
	b.Serve(ddb.TableTimeline,
		func(ctx context.Context) error { <-ctx.Done(); syncer.Close(); return nil },
		run(fromCatalog), run(fromApps),
	)
}
