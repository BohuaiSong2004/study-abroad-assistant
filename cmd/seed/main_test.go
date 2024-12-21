package main

import (
	"errors"
	"testing"
	"time"

	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/stacktest"
)

// The stack keeps its data, so the seed runs again on every start: it must
// create what is missing, leave live programs alone, and roll a sample
// forward once all its rounds have passed.
func TestSeedRerunsCreateKeepAndRollForward(t *testing.T) {
	s := stacktest.New(t, ddb.CacheLocal)
	ctx := stacktest.Counselor()
	nov := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	p := samplePrograms(nov)[0]

	if err := upsert(ctx, s.Catalog, samplePrograms(nov)[0], "2026-11-23"); err != nil {
		t.Fatalf("first run should create: %v", err)
	}
	if err := upsert(ctx, s.Catalog, samplePrograms(nov)[0], "2026-11-24"); !errors.Is(err, errKept) {
		t.Fatalf("rerun with rounds still open: %v", err)
	}
	// Months later every round of the seeded intake has passed.
	last := p.Deadlines[len(p.Deadlines)-1].Date
	later, _ := time.Parse("2006-01-02", last)
	later = later.AddDate(0, 0, 1)
	next := samplePrograms(later)[0]
	if err := upsert(ctx, s.Catalog, next, later.Format("2006-01-02")); !errors.Is(err, errRolled) {
		t.Fatalf("all rounds passed: %v", err)
	}
	got, err := s.Catalog.GetProgram(ctx, &catalogv1.GetProgramRequest{Id: p.Id})
	if err != nil {
		t.Fatal(err)
	}
	if got.Intake == p.Intake || got.Deadlines[len(got.Deadlines)-1].Date <= last {
		t.Fatalf("not rolled forward: %s %v", got.Intake, got.Deadlines)
	}
	if got.Version != 2 {
		t.Fatalf("version %d", got.Version)
	}
}

// Every sample program, seeded on any day of the year, has a round open.
func TestEverySampleHasAnOpenRoundWhenSeeded(t *testing.T) {
	for d := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC); d.Year() < 2028; d = d.AddDate(0, 0, 1) {
		today := d.Format("2006-01-02")
		for _, p := range samplePrograms(d) {
			open := false
			for _, dl := range p.Deadlines {
				open = open || dl.Date >= today
			}
			if !open {
				t.Fatalf("seeded on %s, %s has no open round: %v", today, p.Id, p.Deadlines)
			}
		}
	}
}
