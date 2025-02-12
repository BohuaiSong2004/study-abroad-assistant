// Command catalog runs CatalogService.
package main

import (
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/catalog"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/events"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/svc"
)

func main() {
	platform.HealthcheckCommand()
	b, err := svc.New("catalog", ddb.TableCatalog)
	if err != nil {
		svc.Fatal("catalog", err)
	}
	table := b.Table(ddb.TableCatalog)
	ob := events.NewOutbox(b.DB.API, table, "catalog", b.Log)
	catalogv1.RegisterCatalogServiceServer(b.GRPC, catalog.NewService(catalog.NewStore(b.DB.API, table, ob), b.Log))
	eventsv1.RegisterEventStreamServiceServer(b.GRPC, events.NewServer(ob, b.Log, events.ServerOptions{}))
	b.Serve(ddb.TableCatalog, b.CacheWatcher(ob, catalog.InvalidateOn))
}
