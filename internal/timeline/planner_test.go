package timeline

import (
	"strings"
	"testing"
	"time"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
)

func program() *catalogv1.Program {
	return &catalogv1.Program{
		Id: "p1", University: "U", Name: "MS", Timezone: "America/New_York", Version: 3,
		Deadlines: []*catalogv1.Deadline{
			{Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1, Date: "2027-01-15"},
			{Round: catalogv1.RoundKind_ROUND_KIND_ROUND_2, Date: "2027-03-01"},
		},
		Requirements: &catalogv1.Requirements{
			MinToefl: 100, MinIeltsTenths: 70, RecommendationsRequired: 2,
			Documents: []catalogv1.DocumentType{
				catalogv1.DocumentType_DOCUMENT_TYPE_STATEMENT_OF_PURPOSE, catalogv1.DocumentType_DOCUMENT_TYPE_TRANSCRIPT,
				catalogv1.DocumentType_DOCUMENT_TYPE_ENGLISH_SCORES,
			},
		},
		ApplicationFeeCents: 9050, Currency: "USD",
	}
}

func app() *appsv1.Application {
	return &appsv1.Application{
		Id: "a1", StudentId: "s1", ProgramId: "p1", Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1, Version: 7,
		Status: appsv1.ApplicationStatus_APPLICATION_STATUS_IN_PROGRESS,
		Checklist: []*appsv1.ChecklistItem{
			{Document: catalogv1.DocumentType_DOCUMENT_TYPE_STATEMENT_OF_PURPOSE, Done: true},
			{Document: catalogv1.DocumentType_DOCUMENT_TYPE_TRANSCRIPT},
			{Document: catalogv1.DocumentType_DOCUMENT_TYPE_ENGLISH_SCORES},
		},
	}
}

var now = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func byID(t *timelinev1.Timeline) map[string]*timelinev1.Milestone {
	m := map[string]*timelinev1.Milestone{}
	for _, x := range t.Milestones {
		m[x.Id] = x
	}
	return m
}

func TestPlanDerivesDatesFromRoundDeadline(t *testing.T) {
	tl := Plan(program(), app(), nil, now)
	if tl.Deadline != "2027-01-15" || tl.EffectiveRound != catalogv1.RoundKind_ROUND_KIND_ROUND_1 {
		t.Fatalf("deadline %s round %s", tl.Deadline, tl.EffectiveRound)
	}
	if tl.ApplicationVersion != 7 || tl.ProgramVersion != 3 {
		t.Fatal("versions not recorded")
	}
	ms := byID(tl)
	cases := []struct {
		id, due string
		done    bool
	}{
		{"gen:rec-request", "2026-12-04", false}, // D-42
		{"gen:english", "2026-12-16", false},     // D-30
		{"gen:transcript", "2026-12-25", false},  // D-21
		{"gen:sop", "2027-01-01", true},          // D-14, checklist done
		{"gen:submit", "2027-01-15", false},
		{"gen:decision", "2027-03-31", false}, // D+75
	}
	for _, c := range cases {
		m, ok := ms[c.id]
		if !ok {
			t.Fatalf("missing milestone %s in %v", c.id, tl.Milestones)
		}
		if m.DueDate != c.due || m.Done != c.done {
			t.Errorf("%s: due %s done %v, want %s %v", c.id, m.DueDate, m.Done, c.due, c.done)
		}
	}
	if _, ok := ms["gen:cv"]; ok {
		t.Error("CV milestone generated although the program does not require a CV")
	}
	if !strings.Contains(ms["gen:english"].Description, "TOEFL iBT 100+") || !strings.Contains(ms["gen:english"].Description, "IELTS 7.0+") {
		t.Errorf("english description %q", ms["gen:english"].Description)
	}
	if ms["gen:submit"].Description != "Application fee: 90.50 USD" {
		t.Errorf("fee %q", ms["gen:submit"].Description)
	}
	for i := 1; i < len(tl.Milestones); i++ {
		if tl.Milestones[i-1].DueDate > tl.Milestones[i].DueDate {
			t.Fatal("milestones not sorted by due date")
		}
	}
}

func TestPlanRecommendationProgress(t *testing.T) {
	a := app()
	a.Recommendations = []*appsv1.RecommendationSummary{
		{Id: "r1", RecommenderName: "Dr. Lee", Status: appsv1.RecommendationStatus_RECOMMENDATION_STATUS_SUBMITTED},
		{Id: "r2", RecommenderName: "Prof. Diaz", Status: appsv1.RecommendationStatus_RECOMMENDATION_STATUS_DECLINED},
		{Id: "r3", RecommenderName: "Ms. Wu", Status: appsv1.RecommendationStatus_RECOMMENDATION_STATUS_CANCELLED},
	}
	tl := Plan(program(), a, nil, now)
	ms := byID(tl)
	if m := ms["gen:rec:r1"]; m == nil || !m.Done || m.DueDate != "2027-01-08" {
		t.Fatalf("submitted letter milestone: %v", m)
	}
	if ms["gen:rec:r2"] != nil || ms["gen:rec:r3"] != nil {
		t.Fatal("declined/cancelled letters should not get milestones")
	}
	req := ms["gen:rec-request"]
	if req.Done || req.Title != "Ask 1 more recommender" {
		t.Fatalf("request milestone %q done=%v", req.Title, req.Done)
	}
	if len(tl.Warnings) != 1 || !strings.Contains(tl.Warnings[0], "Prof. Diaz declined") {
		t.Fatalf("warnings %v", tl.Warnings)
	}

	a.Recommendations = append(a.Recommendations, &appsv1.RecommendationSummary{Id: "r4", RecommenderName: "Dr. Kim", Status: appsv1.RecommendationStatus_RECOMMENDATION_STATUS_REQUESTED})
	tl = Plan(program(), a, nil, now)
	if req := byID(tl)["gen:rec-request"]; !req.Done || req.Title != "Request 2 recommendation letters" {
		t.Fatalf("after enough requests: %q done=%v", req.Title, req.Done)
	}
	if len(tl.Warnings) != 0 {
		t.Fatalf("no warning expected once enough letters are requested: %v", tl.Warnings)
	}
}

func TestPlanFallsBackWhenRoundRemoved(t *testing.T) {
	p := program()
	p.Deadlines = p.Deadlines[1:] // Round 1 removed
	tl := Plan(p, app(), nil, now)
	if tl.EffectiveRound != catalogv1.RoundKind_ROUND_KIND_ROUND_2 || tl.Deadline != "2027-03-01" {
		t.Fatalf("effective %s %s", tl.EffectiveRound, tl.Deadline)
	}
	if len(tl.Warnings) == 0 || !strings.Contains(tl.Warnings[0], "Round 1 round is no longer offered") {
		t.Fatalf("warnings %v", tl.Warnings)
	}
}

func TestPlanKeepsCustomMilestonesAndStatusDrivesDone(t *testing.T) {
	prev := &timelinev1.Timeline{Version: 4, Milestones: []*timelinev1.Milestone{
		{Id: "gen:sop", Source: timelinev1.MilestoneSource_MILESTONE_SOURCE_GENERATED, DueDate: "2000-01-01"},
		{Id: "c:1", Source: timelinev1.MilestoneSource_MILESTONE_SOURCE_CUSTOM, Kind: timelinev1.MilestoneKind_MILESTONE_KIND_CUSTOM, Title: "Campus visit", DueDate: "2026-12-10", Done: true},
	}}
	a := app()
	a.Status = appsv1.ApplicationStatus_APPLICATION_STATUS_ADMITTED
	tl := Plan(program(), a, prev, now)
	ms := byID(tl)
	if ms["c:1"] == nil || !ms["c:1"].Done {
		t.Fatal("custom milestone lost")
	}
	if ms["gen:sop"].DueDate == "2000-01-01" {
		t.Fatal("stale generated milestone kept instead of re-planned")
	}
	if !ms["gen:submit"].Done || !ms["gen:decision"].Done {
		t.Fatal("admitted application should complete submit and decision")
	}
	if tl.Version != 4 {
		t.Fatal("Plan must carry the previous version (the store bumps it)")
	}
}

func TestOverdueUsesProgramTimeZone(t *testing.T) {
	p := program()
	p.Deadlines = []*catalogv1.Deadline{{Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1, Date: "2026-11-15"}}
	tl := Plan(p, app(), nil, now)
	// 2026-11-16 03:00 UTC is still 2026-11-15 in New York: submit not overdue.
	WithOverdue(tl, time.Date(2026, 11, 16, 3, 0, 0, 0, time.UTC))
	if byID(tl)["gen:submit"].Overdue {
		t.Fatal("deadline day in New York flagged overdue because of UTC")
	}
	WithOverdue(tl, time.Date(2026, 11, 16, 6, 0, 0, 0, time.UTC))
	ms := byID(tl)
	if !ms["gen:submit"].Overdue || ms["gen:sop"].Overdue {
		t.Fatalf("submit overdue=%v sop(done) overdue=%v", ms["gen:submit"].Overdue, ms["gen:sop"].Overdue)
	}
	if ms["gen:decision"].Overdue {
		t.Fatal("decision estimates are never overdue")
	}
	tl.ApplicationStatus = appsv1.ApplicationStatus_APPLICATION_STATUS_WITHDRAWN
	WithOverdue(tl, time.Date(2026, 12, 30, 0, 0, 0, 0, time.UTC))
	for _, m := range tl.Milestones {
		if m.Overdue {
			t.Fatal("withdrawn applications have nothing overdue")
		}
	}
}

func TestPlanWithoutDeadlines(t *testing.T) {
	p := program()
	p.Deadlines = nil
	tl := Plan(p, app(), nil, now)
	if tl.Deadline != "" || len(tl.Milestones) != 0 || len(tl.Warnings) != 1 {
		t.Fatalf("got %+v", tl)
	}
}
