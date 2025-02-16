// Command seed loads the sample program catalog through CatalogService (so
// the normal event stream carries it to the timeline service). It is
// idempotent: existing programs are left alone.
//
//	seed                      # sample catalog (~40 fictional programs)
//	seed -synthetic 5000      # plus N generated programs for load tests
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/svc"
)

func main() {
	synthetic := flag.Int("synthetic", 0, "also create N synthetic programs (load testing)")
	addr := flag.String("catalog", platform.Env("CATALOG_ADDR", "dns:///localhost:9091"), "catalog gRPC address")
	wait := flag.Duration("wait", 60*time.Second, "how long to wait for the catalog service")
	flag.Parse()
	log := platform.NewLogger("seed")
	cc := platform.MustDial(*addr, svc.InternalToken(log))
	client := catalogv1.NewCatalogServiceClient(cc)
	ctx := platform.ServiceContext(context.Background(), "seed")

	deadline := time.Now().Add(*wait)
	for {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := client.ListFacets(c, &catalogv1.ListFacetsRequest{})
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			log.Error("catalog not reachable", "error", err)
			os.Exit(1)
		}
		time.Sleep(time.Second)
	}

	programs := samplePrograms(time.Now())
	for i := 0; i < *synthetic; i++ {
		programs = append(programs, syntheticProgram(i, time.Now()))
	}
	created, skipped := 0, 0
	for _, p := range programs {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := client.CreateProgram(c, &catalogv1.CreateProgramRequest{Program: p})
		cancel()
		switch status.Code(err) {
		case codes.OK:
			created++
		case codes.AlreadyExists:
			skipped++
		default:
			log.Error("create program failed", "id", p.Id, "error", err)
			os.Exit(1)
		}
	}
	fmt.Printf("seed: %d programs created, %d already present\n", created, skipped)
}
