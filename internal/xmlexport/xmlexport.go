// Package xmlexport renders application timelines and recommendation
// packets as XML documents that validate against the bundled XSD schemas
// (schemas/timeline-v1.xsd, schemas/packet-v1.xsd). Admissions offices and
// counselors' tools commonly ingest XML, so the export is schema-first: the
// schemas are the contract and the tests validate every document against them.
package xmlexport

import (
	"embed"
	"encoding/xml"
	"io"
	"time"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/enums"
)

// Namespaces of the two document types.
const (
	TimelineNS = "urn:waypoint:timeline:v1"
	PacketNS   = "urn:waypoint:packet:v1"
)

// Schemas holds the XSD files, served at /schemas/.
//
//go:embed schemas/*.xsd
var Schemas embed.FS

// --- timeline export -----------------------------------------------------

type xTimelines struct {
	XMLName     xml.Name    `xml:"urn:waypoint:timeline:v1 timelines"`
	GeneratedAt string      `xml:"generatedAt,attr"`
	Student     string      `xml:"student,attr,omitempty"`
	Count       int         `xml:"count,attr"`
	Timelines   []xTimeline `xml:"timeline"`
}

type xTimeline struct {
	ApplicationID string      `xml:"applicationId,attr"`
	Version       int64       `xml:"version,attr"`
	Status        string      `xml:"status,attr"`
	UpdatedAt     string      `xml:"updatedAt,attr,omitempty"`
	Program       xTLProgram  `xml:"program"`
	Round         xRound      `xml:"round"`
	Warnings      *xWarnings  `xml:"warnings,omitempty"`
	Milestones    xMilestones `xml:"milestones"`
}

type xTLProgram struct {
	ID         string `xml:"id,attr"`
	University string `xml:"university"`
	Name       string `xml:"name"`
	Intake     string `xml:"intake"`
}

type xRound struct {
	Selected  string `xml:"selected,attr"`
	Effective string `xml:"effective,attr,omitempty"`
	Deadline  string `xml:"deadline,attr,omitempty"`
	Timezone  string `xml:"timezone,attr"`
}

type xWarnings struct {
	Warning []string `xml:"warning"`
}

type xMilestones struct {
	Milestone []xMilestone `xml:"milestone"`
}

type xMilestone struct {
	ID          string `xml:"id,attr"`
	Kind        string `xml:"kind,attr"`
	Source      string `xml:"source,attr"`
	Due         string `xml:"due,attr"`
	Done        bool   `xml:"done,attr"`
	Overdue     bool   `xml:"overdue,attr"`
	Title       string `xml:"title"`
	Description string `xml:"description,omitempty"`
}

func round(r catalogv1.RoundKind) string { return enums.Name(r, "ROUND_KIND_") }

func timelineDoc(t *timelinev1.Timeline) xTimeline {
	x := xTimeline{
		ApplicationID: t.ApplicationId, Version: t.Version,
		Status:  enums.Name(t.ApplicationStatus, "APPLICATION_STATUS_"),
		Program: xTLProgram{ID: t.ProgramId, University: t.University, Name: t.ProgramName, Intake: t.Intake},
		Round:   xRound{Selected: round(t.Round), Effective: round(t.EffectiveRound), Deadline: t.Deadline, Timezone: t.Timezone},
	}
	if t.UpdatedAt != nil {
		x.UpdatedAt = t.UpdatedAt.AsTime().UTC().Format(time.RFC3339)
	}
	if len(t.Warnings) > 0 {
		x.Warnings = &xWarnings{Warning: t.Warnings}
	}
	for _, m := range t.Milestones {
		x.Milestones.Milestone = append(x.Milestones.Milestone, xMilestone{
			ID: m.Id, Kind: enums.Name(m.Kind, "MILESTONE_KIND_"), Source: enums.Name(m.Source, "MILESTONE_SOURCE_"),
			Due: m.DueDate, Done: m.Done, Overdue: m.Overdue, Title: m.Title, Description: m.Description,
		})
	}
	return x
}

// WriteTimelines writes a <timelines> document.
func WriteTimelines(w io.Writer, student string, ts []*timelinev1.Timeline, now time.Time) error {
	doc := xTimelines{GeneratedAt: now.UTC().Format(time.RFC3339), Student: student, Count: len(ts)}
	for _, t := range ts {
		doc.Timelines = append(doc.Timelines, timelineDoc(t))
	}
	return write(w, doc)
}

// --- recommendation packet ---------------------------------------------------

type xPacket struct {
	XMLName       xml.Name   `xml:"urn:waypoint:packet:v1 recommendationPacket"`
	GeneratedAt   string     `xml:"generatedAt,attr"`
	ApplicationID string     `xml:"applicationId,attr"`
	Applicant     xApplicant `xml:"applicant"`
	Program       xPProgram  `xml:"program"`
	Summary       xSummary   `xml:"summary"`
	Recs          xRecs      `xml:"recommendations"`
}

type xApplicant struct {
	Name  string `xml:"name"`
	Email string `xml:"email"`
}

type xPProgram struct {
	ID         string     `xml:"id,attr"`
	Degree     string     `xml:"degree,attr"`
	University string     `xml:"university"`
	Name       string     `xml:"name"`
	Intake     string     `xml:"intake"`
	Deadline   *xDeadline `xml:"deadline,omitempty"`
}

type xDeadline struct {
	Round    string `xml:"round,attr"`
	Date     string `xml:"date,attr"`
	Timezone string `xml:"timezone,attr"`
}

type xSummary struct {
	Required  int32 `xml:"lettersRequired,attr"`
	Submitted int   `xml:"lettersSubmitted,attr"`
	Pending   int   `xml:"lettersPending,attr"`
	Complete  bool  `xml:"complete,attr"`
}

type xRecs struct {
	Rec []xRec `xml:"recommendation"`
}

type xRec struct {
	ID            string       `xml:"id,attr"`
	Status        string       `xml:"status,attr"`
	WaivedAccess  bool         `xml:"waivedAccess,attr"`
	Recommender   xRecommender `xml:"recommender"`
	RequestedAt   string       `xml:"requestedAt"`
	RespondedAt   string       `xml:"respondedAt,omitempty"`
	Letter        *xLetter     `xml:"letter,omitempty"`
	DeclineReason string       `xml:"declineReason,omitempty"`
}

type xRecommender struct {
	Name         string `xml:"name"`
	Email        string `xml:"email"`
	Relationship string `xml:"relationship,omitempty"`
}

type xLetter struct {
	SHA256   string `xml:"sha256,attr"`
	Withheld bool   `xml:"withheld,attr"`
	Text     string `xml:",chardata"`
}

func rfc3339(t interface{ AsTime() time.Time }, ok bool) string {
	if !ok {
		return ""
	}
	return t.AsTime().UTC().Format(time.RFC3339)
}

// WritePacket writes a <recommendationPacket> document. Letters whose
// recommender was granted confidentiality (waived access) are emitted with
// withheld="true" and their digest only.
func WritePacket(w io.Writer, p *appsv1.RecommendationPacket) error {
	a, prog := p.Application, p.Program
	doc := xPacket{
		GeneratedAt: rfc3339(p.GeneratedAt, p.GeneratedAt != nil), ApplicationID: a.Id,
		Applicant: xApplicant{Name: p.Student.GetDisplayName(), Email: p.Student.GetEmail()},
		Program:   xPProgram{ID: prog.Id, Degree: enums.Name(prog.Degree, "DEGREE_"), University: prog.University, Name: prog.Name, Intake: prog.Intake},
	}
	for _, d := range prog.Deadlines {
		if d.Round == a.Round {
			doc.Program.Deadline = &xDeadline{Round: round(d.Round), Date: d.Date, Timezone: prog.Timezone}
		}
	}
	doc.Summary.Required = prog.GetRequirements().GetRecommendationsRequired()
	for _, r := range p.Recommendations {
		x := xRec{
			ID: r.Id, Status: enums.Name(r.Status, "RECOMMENDATION_STATUS_"), WaivedAccess: r.WaivedAccess,
			Recommender: xRecommender{Name: r.RecommenderName, Email: r.RecommenderEmail, Relationship: r.Relationship},
			RequestedAt: rfc3339(r.RequestedAt, r.RequestedAt != nil), RespondedAt: rfc3339(r.RespondedAt, r.RespondedAt != nil),
			DeclineReason: r.DeclineReason,
		}
		switch r.Status {
		case appsv1.RecommendationStatus_RECOMMENDATION_STATUS_SUBMITTED:
			doc.Summary.Submitted++
			x.Letter = &xLetter{SHA256: r.LetterSha256, Withheld: r.LetterText == "", Text: r.LetterText}
		case appsv1.RecommendationStatus_RECOMMENDATION_STATUS_REQUESTED:
			doc.Summary.Pending++
		}
		doc.Recs.Rec = append(doc.Recs.Rec, x)
	}
	doc.Summary.Complete = doc.Summary.Submitted >= int(doc.Summary.Required)
	return write(w, doc)
}

func write(w io.Writer, v any) error {
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}
