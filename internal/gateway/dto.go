package gateway

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/enums"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/timeline"
)

// The public HTTP API uses small JSON documents with lower_snake enum
// values ("master", "round_1") instead of protobuf JSON, so the browser
// code stays simple and the internal proto contract can evolve separately.

func enumName(e protoreflect.Enum, prefix string) string { return enums.Name(e, prefix) }

func parseEnum(s, prefix string, values map[string]int32) (int32, error) {
	return enums.Parse(s, prefix, values)
}

func degreeName(d catalogv1.Degree) string         { return enumName(d, "DEGREE_") }
func roundName(r catalogv1.RoundKind) string       { return enumName(r, "ROUND_KIND_") }
func docName(d catalogv1.DocumentType) string      { return enumName(d, "DOCUMENT_TYPE_") }
func statusName(s appsv1.ApplicationStatus) string { return enumName(s, "APPLICATION_STATUS_") }
func recStatusName(s appsv1.RecommendationStatus) string {
	return enumName(s, "RECOMMENDATION_STATUS_")
}

func parseDegree(s string) (catalogv1.Degree, error) {
	v, err := parseEnum(s, "DEGREE_", catalogv1.Degree_value)
	return catalogv1.Degree(v), err
}
func parseRound(s string) (catalogv1.RoundKind, error) {
	v, err := parseEnum(s, "ROUND_KIND_", catalogv1.RoundKind_value)
	return catalogv1.RoundKind(v), err
}
func parseDoc(s string) (catalogv1.DocumentType, error) {
	v, err := parseEnum(s, "DOCUMENT_TYPE_", catalogv1.DocumentType_value)
	return catalogv1.DocumentType(v), err
}
func parseStatus(s string) (appsv1.ApplicationStatus, error) {
	v, err := parseEnum(s, "APPLICATION_STATUS_", appsv1.ApplicationStatus_value)
	return appsv1.ApplicationStatus(v), err
}

var docLabels = map[catalogv1.DocumentType]string{
	catalogv1.DocumentType_DOCUMENT_TYPE_STATEMENT_OF_PURPOSE: "Statement of purpose",
	catalogv1.DocumentType_DOCUMENT_TYPE_CV:                   "CV / résumé",
	catalogv1.DocumentType_DOCUMENT_TYPE_TRANSCRIPT:           "Official transcripts",
	catalogv1.DocumentType_DOCUMENT_TYPE_WRITING_SAMPLE:       "Writing sample",
	catalogv1.DocumentType_DOCUMENT_TYPE_PORTFOLIO:            "Portfolio",
	catalogv1.DocumentType_DOCUMENT_TYPE_ENGLISH_SCORES:       "English test scores",
	catalogv1.DocumentType_DOCUMENT_TYPE_GRE_SCORES:           "GRE scores",
}

// milestoneDoc maps generated document milestone ids back to documents.
var milestoneDoc = func() map[string]catalogv1.DocumentType {
	m := map[string]catalogv1.DocumentType{}
	for d := range docLabels {
		m[timeline.DocumentMilestoneID(d)] = d
	}
	return m
}()

func ts(t *timestamppb.Timestamp) string {
	if t == nil || (t.Seconds == 0 && t.Nanos == 0) {
		return ""
	}
	return t.AsTime().UTC().Format(time.RFC3339)
}

// --- catalog ---------------------------------------------------------------

type deadlineDTO struct {
	Round string `json:"round"`
	Label string `json:"label"`
	Date  string `json:"date"`
}

type requirementsDTO struct {
	MinToefl                int32    `json:"minToefl"`
	MinIelts                float64  `json:"minIelts"`
	GreRequired             bool     `json:"greRequired"`
	RecommendationsRequired int32    `json:"recommendationsRequired"`
	Documents               []string `json:"documents"`
}

type feeDTO struct {
	AmountCents int64  `json:"amountCents"`
	Currency    string `json:"currency"`
}

type programDTO struct {
	ID             string          `json:"id"`
	University     string          `json:"university"`
	Name           string          `json:"name"`
	Degree         string          `json:"degree"`
	Field          string          `json:"field"`
	Country        string          `json:"country"`
	City           string          `json:"city"`
	Timezone       string          `json:"timezone"`
	Intake         string          `json:"intake"`
	Deadlines      []deadlineDTO   `json:"deadlines"`
	Requirements   requirementsDTO `json:"requirements"`
	ApplicationFee *feeDTO         `json:"applicationFee,omitempty"`
	Website        string          `json:"website,omitempty"`
	Tags           []string        `json:"tags"`
	Version        int64           `json:"version"`
	UpdatedAt      string          `json:"updatedAt,omitempty"`
}

func programOut(p *catalogv1.Program) programDTO {
	out := programDTO{
		ID: p.Id, University: p.University, Name: p.Name, Degree: degreeName(p.Degree), Field: p.Field,
		Country: p.Country, City: p.City, Timezone: p.Timezone, Intake: p.Intake, Website: p.Website,
		Tags: append([]string{}, p.Tags...), Version: p.Version, UpdatedAt: ts(p.UpdatedAt),
		Deadlines: []deadlineDTO{},
	}
	for _, d := range p.Deadlines {
		out.Deadlines = append(out.Deadlines, deadlineDTO{Round: roundName(d.Round), Label: timeline.RoundLabel(d.Round), Date: d.Date})
	}
	r := p.GetRequirements()
	out.Requirements = requirementsDTO{
		MinToefl: r.GetMinToefl(), MinIelts: float64(r.GetMinIeltsTenths()) / 10, GreRequired: r.GetGreRequired(),
		RecommendationsRequired: r.GetRecommendationsRequired(), Documents: []string{},
	}
	for _, d := range r.GetDocuments() {
		out.Requirements.Documents = append(out.Requirements.Documents, docName(d))
	}
	if p.ApplicationFeeCents > 0 {
		out.ApplicationFee = &feeDTO{AmountCents: p.ApplicationFeeCents, Currency: p.Currency}
	}
	return out
}

func programIn(in programDTO) (*catalogv1.Program, error) {
	deg, err := parseDegree(in.Degree)
	if err != nil {
		return nil, fmt.Errorf("degree: %w", err)
	}
	p := &catalogv1.Program{
		Id: in.ID, University: in.University, Name: in.Name, Degree: deg, Field: in.Field,
		Country: in.Country, City: in.City, Timezone: in.Timezone, Intake: in.Intake, Website: in.Website,
		Tags: in.Tags,
		Requirements: &catalogv1.Requirements{
			MinToefl: in.Requirements.MinToefl, MinIeltsTenths: int32(in.Requirements.MinIelts*10 + 0.5),
			GreRequired: in.Requirements.GreRequired, RecommendationsRequired: in.Requirements.RecommendationsRequired,
		},
	}
	if in.ApplicationFee != nil {
		p.ApplicationFeeCents, p.Currency = in.ApplicationFee.AmountCents, in.ApplicationFee.Currency
	}
	for _, d := range in.Deadlines {
		r, err := parseRound(d.Round)
		if err != nil {
			return nil, fmt.Errorf("deadline round: %w", err)
		}
		p.Deadlines = append(p.Deadlines, &catalogv1.Deadline{Round: r, Date: d.Date})
	}
	for _, d := range in.Requirements.Documents {
		doc, err := parseDoc(d)
		if err != nil {
			return nil, fmt.Errorf("document: %w", err)
		}
		p.Requirements.Documents = append(p.Requirements.Documents, doc)
	}
	return p, nil
}

// --- applications ----------------------------------------------------------

type checklistDTO struct {
	Document string `json:"document"`
	Label    string `json:"label"`
	Done     bool   `json:"done"`
}

type recSummaryDTO struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type applicationDTO struct {
	ID              string          `json:"id"`
	ProgramID       string          `json:"programId"`
	ProgramName     string          `json:"programName"`
	University      string          `json:"university"`
	Intake          string          `json:"intake"`
	Round           string          `json:"round"`
	RoundLabel      string          `json:"roundLabel"`
	Status          string          `json:"status"`
	Checklist       []checklistDTO  `json:"checklist"`
	Notes           string          `json:"notes"`
	Recommendations []recSummaryDTO `json:"recommendations"`
	Version         int64           `json:"version"`
	CreatedAt       string          `json:"createdAt"`
	UpdatedAt       string          `json:"updatedAt"`
	SubmittedAt     string          `json:"submittedAt,omitempty"`
}

func applicationOut(a *appsv1.Application) applicationDTO {
	out := applicationDTO{
		ID: a.Id, ProgramID: a.ProgramId, ProgramName: a.ProgramName, University: a.University, Intake: a.Intake,
		Round: roundName(a.Round), RoundLabel: timeline.RoundLabel(a.Round), Status: statusName(a.Status),
		Notes: a.Notes, Version: a.Version, CreatedAt: ts(a.CreatedAt), UpdatedAt: ts(a.UpdatedAt),
		SubmittedAt: ts(a.SubmittedAt), Checklist: []checklistDTO{}, Recommendations: []recSummaryDTO{},
	}
	for _, c := range a.Checklist {
		out.Checklist = append(out.Checklist, checklistDTO{Document: docName(c.Document), Label: docLabels[c.Document], Done: c.Done})
	}
	for _, r := range a.Recommendations {
		out.Recommendations = append(out.Recommendations, recSummaryDTO{ID: r.Id, Name: r.RecommenderName, Status: recStatusName(r.Status)})
	}
	return out
}

type recommendationDTO struct {
	ID            string `json:"id"`
	ApplicationID string `json:"applicationId"`
	Name          string `json:"name"`
	Email         string `json:"email"`
	Relationship  string `json:"relationship"`
	WaivedAccess  bool   `json:"waivedAccess"`
	Status        string `json:"status"`
	LetterText    string `json:"letterText,omitempty"`
	LetterSha256  string `json:"letterSha256,omitempty"`
	DeclineReason string `json:"declineReason,omitempty"`
	RequestedAt   string `json:"requestedAt"`
	RespondedAt   string `json:"respondedAt,omitempty"`
	LinkExpiresAt string `json:"linkExpiresAt,omitempty"`
	// InviteURL is only present right after creating or regenerating a link.
	InviteURL string `json:"inviteUrl,omitempty"`
}

func recommendationOut(r *appsv1.Recommendation) recommendationDTO {
	return recommendationDTO{
		ID: r.Id, ApplicationID: r.ApplicationId, Name: r.RecommenderName, Email: r.RecommenderEmail,
		Relationship: r.Relationship, WaivedAccess: r.WaivedAccess, Status: recStatusName(r.Status),
		LetterText: r.LetterText, LetterSha256: r.LetterSha256, DeclineReason: r.DeclineReason,
		RequestedAt: ts(r.RequestedAt), RespondedAt: ts(r.RespondedAt), LinkExpiresAt: ts(r.LinkExpiresAt),
	}
}

type inviteDTO struct {
	RecommenderName string `json:"recommenderName"`
	StudentName     string `json:"studentName"`
	ProgramName     string `json:"programName"`
	University      string `json:"university"`
	Intake          string `json:"intake"`
	Deadline        string `json:"deadline,omitempty"`
	Timezone        string `json:"timezone,omitempty"`
	Status          string `json:"status"`
	LinkExpiresAt   string `json:"linkExpiresAt"`
	WaivedAccess    bool   `json:"waivedAccess"`
}

func inviteOut(i *appsv1.RecommendationInvite) inviteDTO {
	return inviteDTO{
		RecommenderName: i.RecommenderName, StudentName: i.StudentName, ProgramName: i.ProgramName,
		University: i.University, Intake: i.Intake, Deadline: i.Deadline, Timezone: i.Timezone,
		Status: recStatusName(i.Status), LinkExpiresAt: ts(i.LinkExpiresAt), WaivedAccess: i.WaivedAccess,
	}
}

// --- timeline --------------------------------------------------------------

type milestoneDTO struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Source      string `json:"source"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	DueDate     string `json:"dueDate"`
	Done        bool   `json:"done"`
	Overdue     bool   `json:"overdue"`
	// Document is set for generated milestones backed by a checklist item:
	// completing them means ticking that item on the application.
	Document string `json:"document,omitempty"`
}

type progressDTO struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

type timelineDTO struct {
	ApplicationID     string         `json:"applicationId"`
	ProgramID         string         `json:"programId"`
	ProgramName       string         `json:"programName"`
	University        string         `json:"university"`
	Intake            string         `json:"intake"`
	Round             string         `json:"round"`
	EffectiveRound    string         `json:"effectiveRound"`
	RoundLabel        string         `json:"roundLabel"`
	Deadline          string         `json:"deadline"`
	Timezone          string         `json:"timezone"`
	ApplicationStatus string         `json:"applicationStatus"`
	Milestones        []milestoneDTO `json:"milestones"`
	Warnings          []string       `json:"warnings"`
	Progress          progressDTO    `json:"progress"`
	Version           int64          `json:"version"`
	// ApplicationVersion is the application version the timeline was planned
	// from; a page compares it with the (strongly consistent) application to
	// tell a cached, older timeline from a current one.
	ApplicationVersion int64  `json:"applicationVersion"`
	UpdatedAt          string `json:"updatedAt"`
}

func milestoneOut(m *timelinev1.Milestone) milestoneDTO {
	out := milestoneDTO{
		ID: m.Id, Kind: enumName(m.Kind, "MILESTONE_KIND_"), Source: enumName(m.Source, "MILESTONE_SOURCE_"),
		Title: m.Title, Description: m.Description, DueDate: m.DueDate, Done: m.Done, Overdue: m.Overdue,
	}
	if d, ok := milestoneDoc[m.Id]; ok {
		out.Document = docName(d)
	}
	return out
}

func timelineOut(t *timelinev1.Timeline) timelineDTO {
	out := timelineDTO{
		ApplicationID: t.ApplicationId, ProgramID: t.ProgramId, ProgramName: t.ProgramName, University: t.University,
		Intake: t.Intake, Round: roundName(t.Round), EffectiveRound: roundName(t.EffectiveRound),
		RoundLabel: timeline.RoundLabel(t.EffectiveRound), Deadline: t.Deadline, Timezone: t.Timezone,
		ApplicationStatus: statusName(t.ApplicationStatus), Version: t.Version, ApplicationVersion: t.ApplicationVersion, UpdatedAt: ts(t.UpdatedAt),
		Milestones: []milestoneDTO{}, Warnings: append([]string{}, t.Warnings...),
	}
	for _, m := range t.Milestones {
		out.Milestones = append(out.Milestones, milestoneOut(m))
		if m.Kind != timelinev1.MilestoneKind_MILESTONE_KIND_DECISION {
			out.Progress.Total++
			if m.Done {
				out.Progress.Done++
			}
		}
	}
	return out
}

type agendaItemDTO struct {
	ApplicationID string       `json:"applicationId"`
	ProgramName   string       `json:"programName"`
	University    string       `json:"university"`
	Milestone     milestoneDTO `json:"milestone"`
}

type accountDTO struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

func accountOut(a *appsv1.Account) accountDTO {
	return accountDTO{ID: a.Id, Email: a.Email, Name: a.DisplayName, Role: enumName(a.Role, "ROLE_")}
}
