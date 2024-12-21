// Command seed loads the sample program catalog through CatalogService (so
// the normal event stream carries it to the timeline service). It is
// idempotent: existing programs are left alone.
//
//	seed                      # sample catalog (~40 fictional programs)
//	seed -synthetic 5000      # plus N generated programs for load tests
package main

import (
	"context"
	"errors"
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
	today := time.Now().UTC().Format("2006-01-02")
	created, kept, rolled := 0, 0, 0
	for _, p := range programs {
		switch err := upsert(ctx, client, p, today); {
		case err == nil:
			created++
		case errors.Is(err, errKept):
			kept++
		case errors.Is(err, errRolled):
			rolled++
		default:
			log.Error("seeding program failed", "id", p.Id, "error", err)
			os.Exit(1)
		}
	}
	fmt.Printf("seed: %d programs created, %d already present, %d rolled forward to the next intake\n", created, kept, rolled)
}

var (
	errKept   = errors.New("kept")
	errRolled = errors.New("rolled forward")
)

// upsert creates a sample program, or, when it exists (the stack keeps its
// data across restarts), leaves it alone unless every round has passed: then
// the sample is replaced with the next intake's dates, so a catalog seeded
// months ago does not end up with nothing left to apply to. Programs a
// counselor still has open rounds on are never touched.
func upsert(ctx context.Context, client catalogv1.CatalogServiceClient, p *catalogv1.Program, today string) error {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cur, err := client.GetProgram(c, &catalogv1.GetProgramRequest{Id: p.Id})
	switch status.Code(err) {
	case codes.NotFound:
		_, err := client.CreateProgram(c, &catalogv1.CreateProgramRequest{Program: p})
		if status.Code(err) == codes.AlreadyExists {
			return errKept // created concurrently
		}
		return err
	case codes.OK:
	default:
		return err
	}
	for _, d := range cur.Deadlines {
		if d.Date >= today {
			return errKept
		}
	}
	p.Version = cur.Version
	if _, err := client.UpdateProgram(c, &catalogv1.UpdateProgramRequest{Program: p, ExpectedVersion: cur.Version}); err != nil {
		return err
	}
	return errRolled
}
