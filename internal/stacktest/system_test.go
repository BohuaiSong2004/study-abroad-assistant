package stacktest_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
	. "github.com/BohuaiSong2004/study-abroad-assistant/internal/stacktest"
)

func milestone(tl *timelinev1.Timeline, id string) *timelinev1.Milestone {
	for _, m := range tl.Milestones {
		if m.Id == id {
			return m
		}
	}
	return nil
}

func code(err error) codes.Code { return status.Code(err) }

func TestTimelineFollowsApplicationAndCatalogChanges(t *testing.T) {
	s := New(t, ddb.CacheLocal)
	prog, err := s.Catalog.CreateProgram(Counselor(), &catalogv1.CreateProgramRequest{Program: Program("northbridge-mscs")})
	if err != nil {
		t.Fatal(err)
	}
	me, _ := s.Student("ana@example.com", "Ana Silva")

	app, err := s.Apps.CreateApplication(me, &appsv1.CreateApplicationRequest{ProgramId: prog.Id, Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1})
	if err != nil {
		t.Fatal(err)
	}
	if len(app.Checklist) != 4 { // SOP, CV, transcript + English scores implied by min TOEFL
		t.Fatalf("checklist %v", app.Checklist)
	}

	// 1. Creating the application produces a planned timeline (applications -> timeline).
	tl := s.WaitTimeline(me, app.Id, nil)
	if tl.Deadline != "2027-01-15" || milestone(tl, "gen:submit") == nil || milestone(tl, "gen:english") == nil {
		t.Fatalf("initial timeline %v", tl)
	}

	// 2. Checking off a document on the application updates the timeline.
	app, err = s.Apps.UpdateApplication(me, &appsv1.UpdateApplicationRequest{
		Id: app.Id, ExpectedVersion: app.Version,
		Checklist: []*appsv1.ChecklistUpdate{{Document: catalogv1.DocumentType_DOCUMENT_TYPE_STATEMENT_OF_PURPOSE, Done: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.WaitTimeline(me, app.Id, func(tl *timelinev1.Timeline) bool { return milestone(tl, "gen:sop").Done })

	// 3. A counselor moves the Round 1 deadline: every affected timeline is re-planned (catalog -> timeline).
	upd := proto.Clone(prog).(*catalogv1.Program)
	upd.Deadlines[0].Date = "2027-01-05"
	prog, err = s.Catalog.UpdateProgram(Counselor(), &catalogv1.UpdateProgramRequest{Program: upd, ExpectedVersion: prog.Version})
	if err != nil {
		t.Fatal(err)
	}
	tl = s.WaitTimeline(me, app.Id, func(tl *timelinev1.Timeline) bool { return tl.Deadline == "2027-01-05" })
	if m := milestone(tl, "gen:submit"); m.DueDate != "2027-01-05" || !milestone(tl, "gen:sop").Done {
		t.Fatalf("re-planned timeline lost state: %v", tl)
	}

	// 4. Removing the round falls back to the next one with a warning.
	upd = proto.Clone(prog).(*catalogv1.Program)
	upd.Deadlines = upd.Deadlines[1:]
	if _, err := s.Catalog.UpdateProgram(Counselor(), &catalogv1.UpdateProgramRequest{Program: upd, ExpectedVersion: prog.Version}); err != nil {
		t.Fatal(err)
	}
	tl = s.WaitTimeline(me, app.Id, func(tl *timelinev1.Timeline) bool { return tl.Deadline == "2027-03-01" })
	if len(tl.Warnings) == 0 || tl.EffectiveRound != catalogv1.RoundKind_ROUND_KIND_ROUND_2 {
		t.Fatalf("fallback: %v", tl)
	}

	// 5. Custom milestones survive re-planning.
	tl, err = s.Timeline.AddMilestone(me, &timelinev1.AddMilestoneRequest{ApplicationId: app.Id, Title: "Book campus visit", DueDate: "2026-12-20"})
	if err != nil {
		t.Fatal(err)
	}
	var custom string
	for _, m := range tl.Milestones {
		if m.Source == timelinev1.MilestoneSource_MILESTONE_SOURCE_CUSTOM {
			custom = m.Id
		}
	}
	app, _ = s.Apps.GetApplication(me, &appsv1.GetApplicationRequest{Id: app.Id})
	if _, err := s.Apps.UpdateApplication(me, &appsv1.UpdateApplicationRequest{Id: app.Id, ExpectedVersion: app.Version, Status: appsv1.ApplicationStatus_APPLICATION_STATUS_SUBMITTED.Enum()}); err != nil {
		t.Fatal(err)
	}
	tl = s.WaitTimeline(me, app.Id, func(tl *timelinev1.Timeline) bool { return milestone(tl, "gen:submit").Done })
	if milestone(tl, custom) == nil {
		t.Fatal("custom milestone dropped by re-plan")
	}
	if _, err := s.Timeline.UpdateMilestone(me, &timelinev1.UpdateMilestoneRequest{ApplicationId: app.Id, MilestoneId: "gen:submit", Done: proto.Bool(false)}); code(err) != codes.FailedPrecondition {
		t.Fatalf("editing a generated milestone: %v", err)
	}
	if _, err := s.Timeline.UpdateMilestone(me, &timelinev1.UpdateMilestoneRequest{ApplicationId: app.Id, MilestoneId: custom, Done: proto.Bool(true)}); err != nil {
		t.Fatal(err)
	}

	// Agenda flattens upcoming milestones across applications.
	ag, err := s.Timeline.GetAgenda(me, &timelinev1.GetAgendaRequest{From: "2026-12-01", To: "2027-06-30"})
	if err != nil || len(ag.Items) == 0 {
		t.Fatalf("agenda %v %v", ag, err)
	}

	// 6. Deleting the application deletes the timeline.
	if _, err := s.Apps.DeleteApplication(me, &appsv1.DeleteApplicationRequest{Id: app.Id}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := s.Timeline.GetTimeline(me, &timelinev1.GetTimelineRequest{ApplicationId: app.Id})
		if code(err) == codes.NotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeline not deleted: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// ...and the student can apply to the program again.
	if _, err := s.Apps.CreateApplication(me, &appsv1.CreateApplicationRequest{ProgramId: prog.Id}); err != nil {
		t.Fatalf("re-apply after delete: %v", err)
	}
}

func TestRecommendationLifecycleAndConfidentiality(t *testing.T) {
	s := New(t, ddb.CacheLocal)
	prog, _ := s.Catalog.CreateProgram(Counselor(), &catalogv1.CreateProgramRequest{Program: Program("recs-program")})
	me, acct := s.Student("ben@example.com", "Ben Okafor")
	app, err := s.Apps.CreateApplication(me, &appsv1.CreateApplicationRequest{ProgramId: prog.Id, Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1})
	if err != nil {
		t.Fatal(err)
	}

	waived, err := s.Recs.RequestRecommendation(me, &appsv1.RequestRecommendationRequest{ApplicationId: app.Id, RecommenderName: "Dr. Mei Lin", RecommenderEmail: "Mei.Lin@Example.edu", Relationship: "Thesis advisor", WaivedAccess: true})
	if err != nil {
		t.Fatal(err)
	}
	open, err := s.Recs.RequestRecommendation(me, &appsv1.RequestRecommendationRequest{ApplicationId: app.Id, RecommenderName: "Sam Park", RecommenderEmail: "sam@example.com", Relationship: "Manager"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recs.RequestRecommendation(me, &appsv1.RequestRecommendationRequest{ApplicationId: app.Id, RecommenderName: "Dup", RecommenderEmail: "mei.lin@example.edu"}); code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate recommender: %v", err)
	}
	tl := s.WaitTimeline(me, app.Id, func(tl *timelinev1.Timeline) bool {
		return milestone(tl, "gen:rec:"+waived.Recommendation.Id) != nil && milestone(tl, "gen:rec-request").Done
	})
	if milestone(tl, "gen:rec:"+open.Recommendation.Id).DueDate != "2027-01-08" {
		t.Fatal("letter due date should be a week before the deadline")
	}

	// Recommender opens the link (no account) and sees the request.
	recommender := context.Background()
	inv, err := s.Recs.GetRecommendationInvite(recommender, &appsv1.GetRecommendationInviteRequest{Token: waived.Token})
	if err != nil {
		t.Fatal(err)
	}
	if inv.StudentName != acct.DisplayName || inv.Deadline != "2027-01-15" || inv.University != "Northbridge University" {
		t.Fatalf("invite %v", inv)
	}
	if _, err := s.Recs.SubmitRecommendation(recommender, &appsv1.SubmitRecommendationRequest{Token: waived.Token, LetterText: "too short"}); code(err) != codes.InvalidArgument {
		t.Fatalf("short letter: %v", err)
	}
	letter := strings.Repeat("Ben is an outstanding researcher. ", 5)
	if _, err := s.Recs.SubmitRecommendation(recommender, &appsv1.SubmitRecommendationRequest{Token: waived.Token, LetterText: letter}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recs.SubmitRecommendation(recommender, &appsv1.SubmitRecommendationRequest{Token: waived.Token, LetterText: letter}); code(err) != codes.FailedPrecondition {
		t.Fatalf("second submission: %v", err)
	}
	if _, err := s.Recs.DeclineRecommendation(recommender, &appsv1.DeclineRecommendationRequest{Token: open.Token, Reason: "On sabbatical"}); err != nil {
		t.Fatal(err)
	}

	// Timeline reflects both responses.
	tl = s.WaitTimeline(me, app.Id, func(tl *timelinev1.Timeline) bool {
		m := milestone(tl, "gen:rec:"+waived.Recommendation.Id)
		return m != nil && m.Done && milestone(tl, "gen:rec:"+open.Recommendation.Id) == nil
	})
	if milestone(tl, "gen:rec-request").Done || len(tl.Warnings) == 0 {
		t.Fatalf("one letter short after the decline: %v", tl)
	}

	// The student never sees a waived letter's text, only its digest.
	packet, err := s.Apps.GetRecommendationPacket(me, &appsv1.GetRecommendationPacketRequest{ApplicationId: app.Id})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range packet.Recommendations {
		if r.Id == waived.Recommendation.Id && (r.LetterText != "" || len(r.LetterSha256) != 64) {
			t.Fatalf("waived letter leaked or digest missing: %v", r)
		}
	}

	// Regenerating a link invalidates the old one; cancelling closes it.
	again, err := s.Recs.RequestRecommendation(me, &appsv1.RequestRecommendationRequest{ApplicationId: app.Id, RecommenderName: "Ola N", RecommenderEmail: "ola@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := s.Recs.RegenerateRecommendationLink(me, &appsv1.RegenerateRecommendationLinkRequest{ApplicationId: app.Id, RecommendationId: again.Recommendation.Id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recs.GetRecommendationInvite(recommender, &appsv1.GetRecommendationInviteRequest{Token: again.Token}); code(err) != codes.NotFound {
		t.Fatalf("old link still works: %v", err)
	}
	if _, err := s.Recs.CancelRecommendation(me, &appsv1.CancelRecommendationRequest{ApplicationId: app.Id, RecommendationId: again.Recommendation.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recs.GetRecommendationInvite(recommender, &appsv1.GetRecommendationInviteRequest{Token: fresh.Token}); code(err) != codes.NotFound {
		t.Fatalf("cancelled link still works: %v", err)
	}
}

func TestAuthorizationBoundaries(t *testing.T) {
	s := New(t, ddb.CacheLocal)
	prog, _ := s.Catalog.CreateProgram(Counselor(), &catalogv1.CreateProgramRequest{Program: Program("authz-program")})
	alice, _ := s.Student("alice@example.com", "Alice")
	mallory, _ := s.Student("mallory@example.com", "Mallory")
	app, err := s.Apps.CreateApplication(alice, &appsv1.CreateApplicationRequest{ProgramId: prog.Id})
	if err != nil {
		t.Fatal(err)
	}
	s.WaitTimeline(alice, app.Id, nil)

	if _, err := s.Apps.GetApplication(mallory, &appsv1.GetApplicationRequest{Id: app.Id}); code(err) != codes.NotFound {
		t.Fatalf("other student's application: %v", err)
	}
	if _, err := s.Timeline.GetTimeline(mallory, &timelinev1.GetTimelineRequest{ApplicationId: app.Id}); code(err) != codes.NotFound {
		t.Fatalf("other student's timeline: %v", err)
	}
	if _, err := s.Recs.RequestRecommendation(mallory, &appsv1.RequestRecommendationRequest{ApplicationId: app.Id, RecommenderName: "X", RecommenderEmail: "x@example.com"}); code(err) != codes.NotFound {
		t.Fatalf("request on other's application: %v", err)
	}
	if _, err := s.Catalog.CreateProgram(alice, &catalogv1.CreateProgramRequest{Program: Program("student-made")}); code(err) != codes.PermissionDenied {
		t.Fatalf("student creating program: %v", err)
	}
	if _, err := s.Apps.CreateApplication(Counselor(), &appsv1.CreateApplicationRequest{ProgramId: prog.Id}); code(err) != codes.PermissionDenied {
		t.Fatalf("counselor creating application: %v", err)
	}
	if _, err := s.Apps.ListApplications(context.Background(), &appsv1.ListApplicationsRequest{}); code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous list: %v", err)
	}
	if _, err := s.Apps.CreateApplication(alice, &appsv1.CreateApplicationRequest{ProgramId: prog.Id}); code(err) != codes.AlreadyExists {
		t.Fatalf("second application to the same program: %v", err)
	}
	if _, err := s.Apps.CreateApplication(alice, &appsv1.CreateApplicationRequest{ProgramId: prog.Id, Round: catalogv1.RoundKind_ROUND_KIND_FINAL}); code(err) != codes.InvalidArgument && code(err) != codes.AlreadyExists {
		t.Fatalf("round not offered: %v", err)
	}

	// Optimistic concurrency: a stale version is rejected, not lost.
	cur, _ := s.Apps.GetApplication(alice, &appsv1.GetApplicationRequest{Id: app.Id})
	if _, err := s.Apps.UpdateApplication(alice, &appsv1.UpdateApplicationRequest{Id: app.Id, ExpectedVersion: cur.Version, Notes: proto.String("first")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apps.UpdateApplication(alice, &appsv1.UpdateApplicationRequest{Id: app.Id, ExpectedVersion: cur.Version, Notes: proto.String("stale")}); code(err) != codes.Aborted {
		t.Fatalf("stale update: %v", err)
	}
}

func TestSessionsAndCredentials(t *testing.T) {
	s := New(t, ddb.CacheLocal)
	ctx := context.Background()
	if _, err := s.Accounts.Register(ctx, &appsv1.RegisterRequest{Email: "x@example.com", Password: "short", DisplayName: "X"}); code(err) != codes.InvalidArgument {
		t.Fatalf("short password: %v", err)
	}
	long := strings.Repeat("ü", 100) // 200 bytes: beyond bcrypt's 72-byte limit, fine for argon2id
	a, err := s.Accounts.Register(ctx, &appsv1.RegisterRequest{Email: "  Chen@Example.COM ", Password: long, DisplayName: "Chen"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Email != "chen@example.com" || a.Role != appsv1.Role_ROLE_STUDENT {
		t.Fatalf("account %v", a)
	}
	if _, err := s.Accounts.Register(ctx, &appsv1.RegisterRequest{Email: "chen@example.com", Password: long, DisplayName: "Other"}); code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate email: %v", err)
	}
	if _, err := s.Accounts.Authenticate(ctx, &appsv1.AuthenticateRequest{Email: "chen@example.com", Password: long[:len(long)-2]}); code(err) != codes.Unauthenticated {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := s.Accounts.Authenticate(ctx, &appsv1.AuthenticateRequest{Email: "nobody@example.com", Password: long}); code(err) != codes.Unauthenticated {
		t.Fatalf("unknown email: %v", err)
	}
	got, err := s.Accounts.Authenticate(ctx, &appsv1.AuthenticateRequest{Email: "CHEN@example.com", Password: long})
	if err != nil || got.Id != a.Id {
		t.Fatalf("login: %v", err)
	}
	sess, err := s.Accounts.CreateSession(ctx, &appsv1.CreateSessionRequest{AccountId: a.Id})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Accounts.ResolveSession(ctx, &appsv1.ResolveSessionRequest{Token: sess.Token})
	if err != nil || r.Account.Id != a.Id {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := s.Accounts.RevokeSession(ctx, &appsv1.RevokeSessionRequest{Token: sess.Token}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accounts.ResolveSession(ctx, &appsv1.ResolveSessionRequest{Token: sess.Token}); code(err) != codes.Unauthenticated {
		t.Fatalf("revoked session still valid: %v", err)
	}
	// Counselor bootstrap is idempotent.
	for i := 0; i < 2; i++ {
		if err := s.AccountService.EnsureAccount(ctx, "lead@example.com", "counselor-password", "Lead", appsv1.Role_ROLE_COUNSELOR); err != nil {
			t.Fatal(err)
		}
	}
	c, err := s.Accounts.Authenticate(ctx, &appsv1.AuthenticateRequest{Email: "lead@example.com", Password: "counselor-password"})
	if err != nil || c.Role != appsv1.Role_ROLE_COUNSELOR {
		t.Fatalf("counselor: %v %v", c, err)
	}
}

func TestReplayFromZeroIsIdempotentAndConsumersResume(t *testing.T) {
	s := New(t, ddb.CacheLocal)
	prog, _ := s.Catalog.CreateProgram(Counselor(), &catalogv1.CreateProgramRequest{Program: Program("replay-program")})
	me, _ := s.Student("dana@example.com", "Dana")
	app, _ := s.Apps.CreateApplication(me, &appsv1.CreateApplicationRequest{ProgramId: prog.Id})
	before := s.WaitTimeline(me, app.Id, nil)

	// Timeline service is down while the application changes.
	s.StopConsumers()
	cur, _ := s.Apps.GetApplication(me, &appsv1.GetApplicationRequest{Id: app.Id})
	if _, err := s.Apps.UpdateApplication(me, &appsv1.UpdateApplicationRequest{Id: app.Id, ExpectedVersion: cur.Version, Status: appsv1.ApplicationStatus_APPLICATION_STATUS_IN_PROGRESS.Enum()}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if tl, _ := s.Timeline.GetTimeline(me, &timelinev1.GetTimelineRequest{ApplicationId: app.Id}); tl.Version != before.Version {
		t.Fatal("timeline changed while consumers were stopped")
	}
	// On restart it catches up from its cursor.
	s.StartConsumers()
	caught := s.WaitTimeline(me, app.Id, func(tl *timelinev1.Timeline) bool {
		return tl.ApplicationStatus == appsv1.ApplicationStatus_APPLICATION_STATUS_IN_PROGRESS
	})

	// Rewind both cursors to zero: replaying the full history must not change anything.
	s.StopConsumers()
	for _, name := range []string{"timeline.catalog", "timeline.applications"} {
		_, err := s.DB.Raw.DeleteItem(context.Background(), &dynamodb.DeleteItemInput{
			TableName: aws.String(s.Prefix + ddb.TableTimeline), Key: ddb.Key("CURSOR#"+name, "CURSOR"),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	s.StartConsumers()
	time.Sleep(time.Second)
	for _, c := range s.Consumers() {
		if c.Status().Cursor == 0 {
			t.Fatalf("%s did not replay", c.Status().Name)
		}
	}
	after, err := s.Timeline.GetTimeline(me, &timelinev1.GetTimelineRequest{ApplicationId: app.Id})
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != caught.Version {
		t.Fatalf("replay rewrote the timeline: version %d -> %d", caught.Version, after.Version)
	}
	st, err := s.Timeline.GetSyncStatus(platform.ServiceContext(context.Background(), "test"), &timelinev1.GetSyncStatusRequest{})
	if err != nil || len(st.Streams) != 2 {
		t.Fatalf("sync status %v %v", st, err)
	}
}

func TestCatalogListingFiltersAndPagination(t *testing.T) {
	s := New(t, ddb.CacheLocal)
	mk := func(id, uni, name, country string, deg catalogv1.Degree) {
		p := Program(id)
		p.University, p.Name, p.Country, p.Degree = uni, name, country, deg
		if _, err := s.Catalog.CreateProgram(Counselor(), &catalogv1.CreateProgramRequest{Program: p}); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range []string{"Canada", "Germany", "Canada", "United Kingdom", "Canada"} {
		mk("prog-"+string(rune('a'+i)), "University "+string(rune('A'+i)), "Data Science", c, catalogv1.Degree_DEGREE_MASTER)
	}
	mk("phd-1", "Alpine Institute", "PhD in Robotics", "Switzerland", catalogv1.Degree_DEGREE_PHD)

	var all []string
	token := ""
	for page := 0; page < 10; page++ {
		res, err := s.Catalog.ListPrograms(context.Background(), &catalogv1.ListProgramsRequest{PageSize: 4, PageToken: token})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range res.Programs {
			all = append(all, p.Id)
		}
		if token = res.NextPageToken; token == "" {
			break
		}
	}
	if len(all) != 6 || all[0] != "phd-1" {
		t.Fatalf("paged listing %v", all)
	}
	res, err := s.Catalog.ListPrograms(context.Background(), &catalogv1.ListProgramsRequest{Country: "canada", PageSize: 2})
	if err != nil || len(res.Programs) != 2 || res.NextPageToken == "" {
		t.Fatalf("filtered page 1: %v %v", res, err)
	}
	res2, _ := s.Catalog.ListPrograms(context.Background(), &catalogv1.ListProgramsRequest{Country: "canada", PageSize: 2, PageToken: res.NextPageToken})
	if len(res2.Programs) != 1 || res2.NextPageToken != "" {
		t.Fatalf("filtered page 2: %v", res2)
	}
	res, _ = s.Catalog.ListPrograms(context.Background(), &catalogv1.ListProgramsRequest{Query: "ROBOTICS alpine"})
	if len(res.Programs) != 1 || res.Programs[0].Id != "phd-1" {
		t.Fatalf("search: %v", res.Programs)
	}
	res, _ = s.Catalog.ListPrograms(context.Background(), &catalogv1.ListProgramsRequest{Degree: catalogv1.Degree_DEGREE_PHD})
	if len(res.Programs) != 1 {
		t.Fatalf("degree filter: %v", res.Programs)
	}
	if _, err := s.Catalog.ListPrograms(context.Background(), &catalogv1.ListProgramsRequest{PageToken: "garbage!"}); code(err) != codes.InvalidArgument {
		t.Fatalf("bad token: %v", err)
	}
	f, err := s.Catalog.ListFacets(context.Background(), &catalogv1.ListFacetsRequest{})
	if err != nil || len(f.Countries) != 4 || f.Countries[0] != "Canada" {
		t.Fatalf("facets %v %v", f, err)
	}

	// Validation and optimistic locking on updates.
	bad := Program("bad-zone")
	bad.Timezone = "Mars/Olympus"
	if _, err := s.Catalog.CreateProgram(Counselor(), &catalogv1.CreateProgramRequest{Program: bad}); code(err) != codes.InvalidArgument {
		t.Fatalf("bad zone: %v", err)
	}
	p, _ := s.Catalog.GetProgram(context.Background(), &catalogv1.GetProgramRequest{Id: "phd-1"})
	if _, err := s.Catalog.UpdateProgram(Counselor(), &catalogv1.UpdateProgramRequest{Program: p, ExpectedVersion: p.Version + 5}); code(err) != codes.Aborted {
		t.Fatalf("stale program update: %v", err)
	}
	_ = types.ReturnValueNone
}
