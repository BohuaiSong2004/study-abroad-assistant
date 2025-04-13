// Package timeline implements TimelineService and the sync handlers that
// keep every application's timeline consistent with the catalog and the
// applications service.
package timeline

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/civil"
)

// Lead times (days before the round deadline) for generated milestones.
const (
	LeadRequestRecommendations = 42 // ask recommenders six weeks out
	LeadTestScores             = 30 // official score reports take weeks to arrive
	LeadTranscripts            = 21
	LeadDocuments              = 14
	LeadLetters                = 7 // letters due a week before the deadline
	DecisionEstimateDays       = 75
)

type docRule struct {
	id, title string
	kind      timelinev1.MilestoneKind
	lead      int
}

var docRules = map[catalogv1.DocumentType]docRule{
	catalogv1.DocumentType_DOCUMENT_TYPE_STATEMENT_OF_PURPOSE: {"gen:sop", "Finalize statement of purpose", timelinev1.MilestoneKind_MILESTONE_KIND_DOCUMENT, LeadDocuments},
	catalogv1.DocumentType_DOCUMENT_TYPE_CV:                   {"gen:cv", "Update CV / résumé", timelinev1.MilestoneKind_MILESTONE_KIND_DOCUMENT, LeadDocuments},
	catalogv1.DocumentType_DOCUMENT_TYPE_TRANSCRIPT:           {"gen:transcript", "Request official transcripts", timelinev1.MilestoneKind_MILESTONE_KIND_DOCUMENT, LeadTranscripts},
	catalogv1.DocumentType_DOCUMENT_TYPE_WRITING_SAMPLE:       {"gen:writing-sample", "Prepare writing sample", timelinev1.MilestoneKind_MILESTONE_KIND_DOCUMENT, LeadDocuments},
	catalogv1.DocumentType_DOCUMENT_TYPE_PORTFOLIO:            {"gen:portfolio", "Finalize portfolio", timelinev1.MilestoneKind_MILESTONE_KIND_DOCUMENT, LeadDocuments},
	catalogv1.DocumentType_DOCUMENT_TYPE_ENGLISH_SCORES:       {"gen:english", "Send English test scores", timelinev1.MilestoneKind_MILESTONE_KIND_TEST, LeadTestScores},
	catalogv1.DocumentType_DOCUMENT_TYPE_GRE_SCORES:           {"gen:gre", "Send GRE scores", timelinev1.MilestoneKind_MILESTONE_KIND_TEST, LeadTestScores},
}

// DocumentMilestoneID returns the generated milestone id for a checklist
// document (the UI uses it to map a milestone checkbox to the checklist).
func DocumentMilestoneID(d catalogv1.DocumentType) string { return docRules[d].id }

// RoundLabel is a human label for a round.
func RoundLabel(r catalogv1.RoundKind) string {
	switch r {
	case catalogv1.RoundKind_ROUND_KIND_PRIORITY:
		return "Priority"
	case catalogv1.RoundKind_ROUND_KIND_ROUND_1:
		return "Round 1"
	case catalogv1.RoundKind_ROUND_KIND_ROUND_2:
		return "Round 2"
	case catalogv1.RoundKind_ROUND_KIND_ROUND_3:
		return "Round 3"
	case catalogv1.RoundKind_ROUND_KIND_FINAL:
		return "Final"
	}
	return "Unspecified"
}

var decided = map[appsv1.ApplicationStatus]bool{
	appsv1.ApplicationStatus_APPLICATION_STATUS_ADMITTED: true,
	appsv1.ApplicationStatus_APPLICATION_STATUS_REJECTED: true,
}

var submitted = map[appsv1.ApplicationStatus]bool{
	appsv1.ApplicationStatus_APPLICATION_STATUS_SUBMITTED: true,
	appsv1.ApplicationStatus_APPLICATION_STATUS_INTERVIEW: true,
	appsv1.ApplicationStatus_APPLICATION_STATUS_ADMITTED:  true,
	appsv1.ApplicationStatus_APPLICATION_STATUS_REJECTED:  true,
}

// Plan builds the timeline for an application from the program snapshot,
// the application snapshot and the student's custom milestones (carried over
// from prev). It is a pure function of its inputs plus "today", which only
// affects the fallback when the chosen round no longer exists.
func Plan(p *catalogv1.Program, a *appsv1.Application, prev *timelinev1.Timeline, now time.Time) *timelinev1.Timeline {
	t := &timelinev1.Timeline{
		ApplicationId: a.Id, StudentId: a.StudentId, ProgramId: p.Id,
		ProgramName: p.Name, University: p.University, Intake: p.Intake,
		Round: a.Round, Timezone: p.Timezone, ApplicationStatus: a.Status,
		ApplicationVersion: a.Version, ProgramVersion: p.Version,
	}
	if prev != nil {
		t.Version = prev.Version
	}

	// Effective round: the chosen one, or the next open one if it was removed.
	var deadline civil.Date
	eff := catalogv1.RoundKind_ROUND_KIND_UNSPECIFIED
	for _, d := range p.Deadlines {
		if d.Round == a.Round {
			if dd, err := civil.Parse(d.Date); err == nil {
				deadline, eff = dd, d.Round
			}
		}
	}
	if eff == catalogv1.RoundKind_ROUND_KIND_UNSPECIFIED && len(p.Deadlines) > 0 {
		today := civil.Today(now, p.Timezone)
		pick := p.Deadlines[len(p.Deadlines)-1]
		for _, d := range p.Deadlines {
			if dd, err := civil.Parse(d.Date); err == nil && !dd.Before(today) {
				pick = d
				break
			}
		}
		deadline, _ = civil.Parse(pick.Date)
		eff = pick.Round
		warn(t, fmt.Sprintf("The %s round is no longer offered; planning for %s (%s).", RoundLabel(a.Round), RoundLabel(eff), pick.Date),
			text("warn.round-gone", "asked", round(a.Round), "planned", round(eff), "date", pick.Date))
	}
	t.EffectiveRound = eff
	if deadline.IsZero() {
		warn(t, "This program has no deadlines yet.", text("warn.no-deadlines"))
		t.Milestones = customOnly(prev)
		return t
	}
	t.Deadline = deadline.String()
	due := func(lead int) string { return deadline.AddDays(-lead).String() }

	var ms []*timelinev1.Milestone
	done := map[catalogv1.DocumentType]bool{}
	for _, c := range a.Checklist {
		done[c.Document] = c.Done
	}
	req := p.GetRequirements()
	for _, d := range req.GetDocuments() {
		rule, ok := docRules[d]
		if !ok {
			continue
		}
		m := &timelinev1.Milestone{Id: rule.id, Kind: rule.kind, Source: timelinev1.MilestoneSource_MILESTONE_SOURCE_GENERATED,
			Title: rule.title, TitleText: text("ms." + strings.TrimPrefix(rule.id, "gen:")), DueDate: due(rule.lead), Done: done[d]}
		switch d {
		case catalogv1.DocumentType_DOCUMENT_TYPE_ENGLISH_SCORES:
			m.Description, m.DescriptionText = englishRequirement(req)
		case catalogv1.DocumentType_DOCUMENT_TYPE_GRE_SCORES:
			m.Description, m.DescriptionText = "Official GRE score report sent from ETS.", text("desc.gre")
		}
		ms = append(ms, m)
	}

	// Recommendation letters.
	need := int(req.GetRecommendationsRequired())
	activeCount := 0
	var declined []string
	for _, r := range a.Recommendations {
		switch r.Status {
		case appsv1.RecommendationStatus_RECOMMENDATION_STATUS_REQUESTED, appsv1.RecommendationStatus_RECOMMENDATION_STATUS_SUBMITTED:
			activeCount++
			ms = append(ms, &timelinev1.Milestone{
				Id: "gen:rec:" + r.Id, Kind: timelinev1.MilestoneKind_MILESTONE_KIND_RECOMMENDATION,
				Source: timelinev1.MilestoneSource_MILESTONE_SOURCE_GENERATED,
				Title:  "Letter from " + r.RecommenderName, TitleText: text("ms.letter", "name", r.RecommenderName), DueDate: due(LeadLetters),
				Done: r.Status == appsv1.RecommendationStatus_RECOMMENDATION_STATUS_SUBMITTED,
			})
		case appsv1.RecommendationStatus_RECOMMENDATION_STATUS_DECLINED:
			declined = append(declined, r.RecommenderName)
		}
	}
	if need > 0 {
		m := &timelinev1.Milestone{
			Id: "gen:rec-request", Kind: timelinev1.MilestoneKind_MILESTONE_KIND_RECOMMENDATION,
			Source:  timelinev1.MilestoneSource_MILESTONE_SOURCE_GENERATED,
			DueDate: due(LeadRequestRecommendations), Done: activeCount >= need,
		}
		if missing := need - activeCount; missing > 0 {
			m.Title = fmt.Sprintf("Ask %d more recommender%s", missing, plural(missing))
			m.TitleText = text("ms.ask-more", "n", strconv.Itoa(missing))
			m.Description = fmt.Sprintf("This program requires %d letter%s.", need, plural(need))
			m.DescriptionText = text("desc.letters-required", "n", strconv.Itoa(need))
			if len(declined) > 0 {
				// names: one per line, so clients can join them in their language.
				warn(t, fmt.Sprintf("%s declined to write a letter; ask someone else.", strings.Join(declined, " and ")),
					text("warn.declined", "names", strings.Join(declined, "\n")))
			}
		} else {
			m.Title = fmt.Sprintf("Request %d recommendation letter%s", need, plural(need))
			m.TitleText = text("ms.request-letters", "n", strconv.Itoa(need))
		}
		ms = append(ms, m)
	}

	sub := &timelinev1.Milestone{
		Id: "gen:submit", Kind: timelinev1.MilestoneKind_MILESTONE_KIND_SUBMISSION,
		Source:    timelinev1.MilestoneSource_MILESTONE_SOURCE_GENERATED,
		Title:     fmt.Sprintf("Submit application (%s deadline)", RoundLabel(eff)),
		TitleText: text("ms.submit", "round", round(eff)), DueDate: deadline.String(),
		Done: submitted[a.Status],
	}
	if p.ApplicationFeeCents > 0 {
		sub.Description = fmt.Sprintf("Application fee: %d.%02d %s", p.ApplicationFeeCents/100, p.ApplicationFeeCents%100, p.Currency)
		// Hundredths of the currency unit, as in the catalog; clients format it.
		sub.DescriptionText = text("desc.fee", "amount_cents", strconv.FormatInt(p.ApplicationFeeCents, 10), "currency", p.Currency)
	}
	ms = append(ms, sub, &timelinev1.Milestone{
		Id: "gen:decision", Kind: timelinev1.MilestoneKind_MILESTONE_KIND_DECISION,
		Source: timelinev1.MilestoneSource_MILESTONE_SOURCE_GENERATED,
		Title:  "Decision expected (estimate)", TitleText: text("ms.decision"), DueDate: deadline.AddDays(DecisionEstimateDays).String(),
		Description: "Estimate only: many programs decide 8-12 weeks after the deadline.", DescriptionText: text("desc.decision"),
		Done: decided[a.Status],
	})
	if a.Status == appsv1.ApplicationStatus_APPLICATION_STATUS_WITHDRAWN {
		warn(t, "You withdrew this application.", text("warn.withdrawn"))
	}
	t.Milestones = append(ms, customOnly(prev)...)
	SortMilestones(t.Milestones)
	return t
}

func englishRequirement(r *catalogv1.Requirements) (string, *timelinev1.Text) {
	var parts []string
	var kv []string
	if r.MinToefl > 0 {
		parts = append(parts, fmt.Sprintf("TOEFL iBT %d+", r.MinToefl))
		kv = append(kv, "toefl", strconv.Itoa(int(r.MinToefl)))
	}
	if r.MinIeltsTenths > 0 {
		ielts := fmt.Sprintf("%d.%d", r.MinIeltsTenths/10, r.MinIeltsTenths%10)
		parts = append(parts, "IELTS "+ielts+"+")
		kv = append(kv, "ielts", ielts)
	}
	if len(parts) == 0 {
		return "", nil
	}
	variant := "both"
	switch {
	case r.MinIeltsTenths == 0:
		variant = "toefl"
	case r.MinToefl == 0:
		variant = "ielts"
	}
	return "Minimum: " + strings.Join(parts, " or ") + ".", text("desc.english-"+variant, kv...)
}

// text builds a structured Text from a code and key/value pairs.
func text(code string, kv ...string) *timelinev1.Text {
	t := &timelinev1.Text{Code: code}
	if len(kv) > 0 {
		t.Args = make(map[string]string, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			t.Args[kv[i]] = kv[i+1]
		}
	}
	return t
}

// warn adds a warning in both forms.
func warn(t *timelinev1.Timeline, english string, structured *timelinev1.Text) {
	t.Warnings = append(t.Warnings, english)
	t.WarningTexts = append(t.WarningTexts, structured)
}

// round is the enum value without its prefix ("ROUND_1"), as Text args use.
func round(r catalogv1.RoundKind) string {
	return strings.TrimPrefix(r.String(), "ROUND_KIND_")
}

func customOnly(prev *timelinev1.Timeline) []*timelinev1.Milestone {
	if prev == nil {
		return nil
	}
	var out []*timelinev1.Milestone
	for _, m := range prev.Milestones {
		if m.Source == timelinev1.MilestoneSource_MILESTONE_SOURCE_CUSTOM {
			out = append(out, m)
		}
	}
	return out
}

// SortMilestones orders by due date, then generated before custom, then id.
func SortMilestones(ms []*timelinev1.Milestone) {
	sort.SliceStable(ms, func(i, j int) bool {
		if ms[i].DueDate != ms[j].DueDate {
			return ms[i].DueDate < ms[j].DueDate
		}
		if ms[i].Kind != ms[j].Kind {
			return ms[i].Kind < ms[j].Kind
		}
		return ms[i].Id < ms[j].Id
	})
}

// WithOverdue returns a copy-free annotation of overdue flags relative to
// "today" in the program's time zone. Withdrawn applications have nothing
// overdue.
func WithOverdue(t *timelinev1.Timeline, now time.Time) *timelinev1.Timeline {
	today := civil.Today(now, t.Timezone).String()
	withdrawn := t.ApplicationStatus == appsv1.ApplicationStatus_APPLICATION_STATUS_WITHDRAWN
	for _, m := range t.Milestones {
		m.Overdue = !withdrawn && !m.Done && m.DueDate < today && m.Kind != timelinev1.MilestoneKind_MILESTONE_KIND_DECISION
	}
	return t
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
