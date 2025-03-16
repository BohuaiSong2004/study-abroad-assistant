package stacktest_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
	. "github.com/BohuaiSong2004/study-abroad-assistant/internal/stacktest"
)

// A planner bug that shipped and wrote wrong plans is repaired by Resync,
// which rewrites only the timelines whose plan differs from what the current
// planner produces, and keeps students' own progress.
func TestResyncRepairsOnlyTheDamagedPlans(t *testing.T) {
	s := New(t, ddb.CacheLocal)
	prog, err := s.Catalog.CreateProgram(Counselor(), &catalogv1.CreateProgramRequest{Program: Program("resync-mscs")})
	if err != nil {
		t.Fatal(err)
	}
	me, acct := s.Student("resync@example.com", "Re Sync")
	var ids []string
	for _, round := range []catalogv1.RoundKind{catalogv1.RoundKind_ROUND_KIND_ROUND_1, catalogv1.RoundKind_ROUND_KIND_ROUND_2} {
		other, _ := s.Student("resync-"+round.String()+"@example.com", "Other")
		ctx := me
		if round == catalogv1.RoundKind_ROUND_KIND_ROUND_2 {
			ctx = other
		}
		app, err := s.Apps.CreateApplication(ctx, &appsv1.CreateApplicationRequest{ProgramId: prog.Id, Round: round})
		if err != nil {
			t.Fatal(err)
		}
		s.WaitTimeline(ctx, app.Id, nil)
		ids = append(ids, app.Id)
	}
	// The student adds their own milestone, then "the bug" drops the submit
	// milestone from the stored plan.
	tl, err := s.Timeline.AddMilestone(me, &timelinev1.AddMilestoneRequest{ApplicationId: ids[0], Title: "Ask Prof. Lee", DueDate: "2026-12-20"})
	if err != nil {
		t.Fatal(err)
	}
	var custom string
	for _, m := range tl.Milestones {
		if m.Title == "Ask Prof. Lee" {
			custom = m.Id
		}
	}
	bad := proto.Clone(tl).(*timelinev1.Timeline)
	kept := bad.Milestones[:0]
	for _, m := range bad.Milestones {
		if m.Id != "gen:submit" {
			kept = append(kept, m)
		}
	}
	bad.Milestones = kept
	raw, _ := proto.Marshal(bad)
	if _, err := s.DB.Raw.UpdateItem(context.Background(), &dynamodb.UpdateItemInput{
		TableName: aws.String(s.Prefix + ddb.TableTimeline), Key: ddb.Key("STUDENT#"+acct.Id, "TL#"+ids[0]),
		UpdateExpression: aws.String("SET tl = :tl"), ExpressionAttributeValues: map[string]types.AttributeValue{":tl": ddb.B(raw)},
	}); err != nil {
		t.Fatal(err)
	}

	admin := platform.ServiceContext(context.Background(), "waypointctl")
	if _, err := s.Admin.Resync(me, &timelinev1.ResyncRequest{}); code(err) != codes.PermissionDenied {
		t.Fatalf("students must not resync: %v", err)
	}
	res, err := s.Admin.Resync(admin, &timelinev1.ResyncRequest{})
	if err != nil || res.Timelines != 2 || res.Replanned != 1 {
		t.Fatalf("resync: %v %v", res, err)
	}
	got, err := s.Timeline.GetTimeline(me, &timelinev1.GetTimelineRequest{ApplicationId: ids[0], Consistent: true})
	if err != nil {
		t.Fatal(err)
	}
	if milestone(got, "gen:submit") == nil || milestone(got, custom) == nil {
		t.Fatalf("repaired timeline: %v", got)
	}
	// Idempotent, and scoped runs see only that student's timelines.
	if res, err := s.Admin.Resync(admin, &timelinev1.ResyncRequest{StudentId: acct.Id}); err != nil || res.Timelines != 1 || res.Replanned != 0 {
		t.Fatalf("second resync: %v %v", res, err)
	}
}
