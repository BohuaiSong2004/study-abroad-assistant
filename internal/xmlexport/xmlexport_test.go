package xmlexport

import (
	"bytes"
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
)

// validate checks doc against the named schema with xmllint. CI installs
// libxml2-utils and sets REQUIRE_XMLLINT=1 so this never silently skips.
func validate(t *testing.T, schema string, doc []byte) {
	t.Helper()
	bin, err := exec.LookPath("xmllint")
	if err != nil {
		if os.Getenv("REQUIRE_XMLLINT") == "1" {
			t.Fatal("xmllint is required")
		}
		t.Skip("xmllint not installed")
	}
	dir := t.TempDir()
	xsd, _ := Schemas.ReadFile("schemas/" + schema)
	xsdPath := filepath.Join(dir, schema)
	docPath := filepath.Join(dir, "doc.xml")
	_ = os.WriteFile(xsdPath, xsd, 0o600)
	_ = os.WriteFile(docPath, doc, 0o600)
	out, err := exec.Command(bin, "--noout", "--nonet", "--schema", xsdPath, docPath).CombinedOutput()
	if err != nil {
		t.Fatalf("schema validation failed: %v\n%s\n--- document ---\n%s", err, out, doc)
	}
}

var tricky = "R&D <lab> \"quotes\" ]]> \x01ctrl ✓ 東京"

func sampleTimeline() *timelinev1.Timeline {
	return &timelinev1.Timeline{
		ApplicationId: "app_1", StudentId: "s", ProgramId: "p1", ProgramName: tricky, University: "Northbridge",
		Intake: "Fall 2027", Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1, EffectiveRound: catalogv1.RoundKind_ROUND_KIND_ROUND_2,
		Deadline: "2027-03-01", Timezone: "America/New_York", ApplicationStatus: appsv1.ApplicationStatus_APPLICATION_STATUS_IN_PROGRESS,
		Warnings: []string{"Round 1 removed"}, Version: 3, UpdatedAt: timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
		Milestones: []*timelinev1.Milestone{
			{Id: "gen:sop", Kind: timelinev1.MilestoneKind_MILESTONE_KIND_DOCUMENT, Source: timelinev1.MilestoneSource_MILESTONE_SOURCE_GENERATED, Title: "Finalize SOP", DueDate: "2027-02-15", Done: true},
			{Id: "c:1", Kind: timelinev1.MilestoneKind_MILESTONE_KIND_CUSTOM, Source: timelinev1.MilestoneSource_MILESTONE_SOURCE_CUSTOM, Title: tricky, Description: tricky, DueDate: "2026-12-01", Overdue: true},
		},
	}
}

func TestTimelinesValidateAndRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	second := sampleTimeline()
	second.ApplicationId, second.Warnings, second.Deadline, second.EffectiveRound = "app_2", nil, "", catalogv1.RoundKind_ROUND_KIND_UNSPECIFIED
	if err := WriteTimelines(&buf, "Ana & Co", []*timelinev1.Timeline{sampleTimeline(), second}, time.Now()); err != nil {
		t.Fatal(err)
	}
	validate(t, "timeline-v1.xsd", buf.Bytes())

	var back xTimelines
	if err := xml.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if back.Count != 2 || back.Student != "Ana & Co" || back.XMLName.Space != TimelineNS {
		t.Fatalf("header %+v", back)
	}
	m := back.Timelines[0].Milestones.Milestone[1]
	// encoding/xml replaces characters that are illegal in XML 1.0 with U+FFFD.
	want := strings.ReplaceAll(tricky, "\x01", "�")
	if m.Title != want || back.Timelines[0].Program.Name != want {
		t.Fatalf("escaping round trip: %q", m.Title)
	}
	if !m.Overdue || m.Kind != "custom" || back.Timelines[0].Round.Effective != "round_2" {
		t.Fatalf("milestone %+v", m)
	}
}

func TestEmptyTimelinesDocumentValidates(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteTimelines(&buf, "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	validate(t, "timeline-v1.xsd", buf.Bytes())
}

func TestDuplicateMilestoneIDsAreRejectedBySchema(t *testing.T) {
	if _, err := exec.LookPath("xmllint"); err != nil {
		t.Skip("xmllint not installed")
	}
	tl := sampleTimeline()
	tl.Milestones[1].Id = tl.Milestones[0].Id
	var buf bytes.Buffer
	_ = WriteTimelines(&buf, "", []*timelinev1.Timeline{tl}, time.Now())
	dir := t.TempDir()
	xsd, _ := Schemas.ReadFile("schemas/timeline-v1.xsd")
	_ = os.WriteFile(filepath.Join(dir, "s.xsd"), xsd, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "d.xml"), buf.Bytes(), 0o600)
	if err := exec.Command("xmllint", "--noout", "--schema", filepath.Join(dir, "s.xsd"), filepath.Join(dir, "d.xml")).Run(); err == nil {
		t.Fatal("schema accepted duplicate milestone ids")
	}
}

func TestPacketWithholdsWaivedLetters(t *testing.T) {
	now := timestamppb.New(time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC))
	digest := strings.Repeat("ab", 32)
	p := &appsv1.RecommendationPacket{
		GeneratedAt: now,
		Application: &appsv1.Application{Id: "app_1", Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1},
		Student:     &appsv1.Account{DisplayName: "Ana Silva", Email: "ana@example.com"},
		Program: &catalogv1.Program{Id: "p1", Degree: catalogv1.Degree_DEGREE_MASTER, University: "Northbridge", Name: "MS CS", Intake: "Fall 2027", Timezone: "America/New_York",
			Deadlines:    []*catalogv1.Deadline{{Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1, Date: "2027-01-15"}},
			Requirements: &catalogv1.Requirements{RecommendationsRequired: 2}},
		Recommendations: []*appsv1.Recommendation{
			{Id: "r1", RecommenderName: "Dr. Lin", RecommenderEmail: "lin@example.edu", WaivedAccess: true, Status: appsv1.RecommendationStatus_RECOMMENDATION_STATUS_SUBMITTED, LetterSha256: digest, RequestedAt: now, RespondedAt: now},
			{Id: "r2", RecommenderName: "Sam", RecommenderEmail: "sam@example.com", Relationship: "Manager", Status: appsv1.RecommendationStatus_RECOMMENDATION_STATUS_SUBMITTED, LetterText: tricky, LetterSha256: digest, RequestedAt: now, RespondedAt: now},
			{Id: "r3", RecommenderName: "Kim", RecommenderEmail: "kim@example.com", Status: appsv1.RecommendationStatus_RECOMMENDATION_STATUS_DECLINED, DeclineReason: "Busy", RequestedAt: now},
			{Id: "r4", RecommenderName: "Ola", RecommenderEmail: "ola@example.com", Status: appsv1.RecommendationStatus_RECOMMENDATION_STATUS_REQUESTED, RequestedAt: now},
		},
	}
	var buf bytes.Buffer
	if err := WritePacket(&buf, p); err != nil {
		t.Fatal(err)
	}
	validate(t, "packet-v1.xsd", buf.Bytes())
	var back xPacket
	if err := xml.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	recs := back.Recs.Rec
	if !recs[0].Letter.Withheld || recs[0].Letter.Text != "" || recs[0].Letter.SHA256 != digest {
		t.Fatalf("waived letter %+v", recs[0].Letter)
	}
	if recs[1].Letter.Withheld || !strings.Contains(recs[1].Letter.Text, "R&D <lab>") {
		t.Fatalf("open letter %+v", recs[1].Letter)
	}
	if recs[2].Letter != nil || recs[2].DeclineReason != "Busy" || recs[3].Letter != nil {
		t.Fatal("letters only exist for submitted recommendations")
	}
	if s := back.Summary; s.Required != 2 || s.Submitted != 2 || s.Pending != 1 || !s.Complete {
		t.Fatalf("summary %+v", s)
	}
	if back.Program.Deadline == nil || back.Program.Deadline.Date != "2027-01-15" {
		t.Fatal("deadline missing")
	}
}
