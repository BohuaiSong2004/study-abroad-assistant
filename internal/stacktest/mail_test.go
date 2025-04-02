package stacktest_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/mail"
	. "github.com/BohuaiSong2004/study-abroad-assistant/internal/stacktest"
)

var linkToken = regexp.MustCompile(`https://waypoint\.test/(recommend|reset)\.html#token=([A-Za-z0-9_-]+)`)

func tokenIn(t *testing.T, m mail.Message, page string) string {
	t.Helper()
	g := linkToken.FindStringSubmatch(m.Text)
	if g == nil || g[1] != page {
		t.Fatalf("no %s link in:\n%s", page, m.Text)
	}
	return g[2]
}

func TestRecommendationInvitationIsEmailed(t *testing.T) {
	s := New(t, ddb.CacheLocal)
	box := mail.NewCapture(4)
	s.AccountService.SetMailer(box, "https://waypoint.test/")
	prog, err := s.Catalog.CreateProgram(Counselor(), &catalogv1.CreateProgramRequest{Program: Program("mail-mscs")})
	if err != nil {
		t.Fatal(err)
	}
	me, _ := s.Student("mia@example.com", "Mia Chen")
	app, err := s.Apps.CreateApplication(me, &appsv1.CreateApplicationRequest{ProgramId: prog.Id, Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Recs.RequestRecommendation(me, &appsv1.RequestRecommendationRequest{ApplicationId: app.Id, RecommenderName: "Dr. Ada Ruiz", RecommenderEmail: "ada@uni.test"})
	if err != nil || !res.EmailSent {
		t.Fatalf("request: %v sent=%v", err, res.GetEmailSent())
	}
	m, ok := box.Next(time.Second)
	if !ok || m.To != "ada@uni.test" || !strings.Contains(m.Subject, "Mia Chen") || !strings.Contains(m.Text, "Northbridge University") {
		t.Fatalf("message %+v", m)
	}
	if tok := tokenIn(t, m, "recommend"); tok != res.Token {
		t.Fatal("emailed link carries a different token")
	}
	inv, err := s.Recs.GetRecommendationInvite(context.Background(), &appsv1.GetRecommendationInviteRequest{Token: res.Token})
	if err != nil || inv.StudentName != "Mia Chen" {
		t.Fatalf("invite: %v %v", inv, err)
	}

	// Delivery failure never fails the request: the student shares the link.
	box.Err = errors.New("relay down")
	res, err = s.Recs.RegenerateRecommendationLink(me, &appsv1.RegenerateRecommendationLinkRequest{ApplicationId: app.Id, RecommendationId: res.Recommendation.Id})
	if err != nil || res.EmailSent || res.Token == "" {
		t.Fatalf("regenerate with failing mail: %v sent=%v", err, res.GetEmailSent())
	}
}

func TestPasswordResetByEmail(t *testing.T) {
	s := New(t, ddb.CacheLocal)
	box := mail.NewCapture(4)
	s.AccountService.SetMailer(box, "https://waypoint.test")
	_, acct := s.Student("reset@example.com", "Rae Set")
	old, err := s.Accounts.CreateSession(context.Background(), &appsv1.CreateSessionRequest{AccountId: acct.Id})
	if err != nil {
		t.Fatal(err)
	}

	// Unknown emails get the same answer and no mail.
	if _, err := s.Accounts.RequestPasswordReset(context.Background(), &appsv1.RequestPasswordResetRequest{Email: "nobody@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accounts.RequestPasswordReset(context.Background(), &appsv1.RequestPasswordResetRequest{Email: "Reset@Example.com"}); err != nil {
		t.Fatal(err)
	}
	m, ok := box.Next(3 * time.Second)
	if !ok || m.To != "reset@example.com" || !strings.Contains(m.Text, "Rae Set") {
		t.Fatalf("reset mail %+v ok=%v", m, ok)
	}
	if extra, ok := box.Next(300 * time.Millisecond); ok {
		t.Fatalf("mail for an unknown email: %+v", extra)
	}
	tok := tokenIn(t, m, "reset")

	if _, err := s.Accounts.ResetPassword(context.Background(), &appsv1.ResetPasswordRequest{Token: tok, NewPassword: "short"}); code(err) != codes.InvalidArgument {
		t.Fatalf("weak password: %v", err)
	}
	if _, err := s.Accounts.ResetPassword(context.Background(), &appsv1.ResetPasswordRequest{Token: tok, NewPassword: "a brand new passphrase"}); err != nil {
		t.Fatal(err)
	}
	// Single use; the old password and the old session are gone; the new one works.
	if _, err := s.Accounts.ResetPassword(context.Background(), &appsv1.ResetPasswordRequest{Token: tok, NewPassword: "another new passphrase"}); code(err) != codes.FailedPrecondition {
		t.Fatalf("token reused: %v", err)
	}
	if _, err := s.Accounts.Authenticate(context.Background(), &appsv1.AuthenticateRequest{Email: "reset@example.com", Password: "correct horse battery"}); code(err) != codes.Unauthenticated {
		t.Fatalf("old password: %v", err)
	}
	if _, err := s.Accounts.ResolveSession(context.Background(), &appsv1.ResolveSessionRequest{Token: old.Token}); code(err) != codes.Unauthenticated {
		t.Fatalf("session from before the reset still valid: %v", err)
	}
	if _, err := s.Accounts.Authenticate(context.Background(), &appsv1.AuthenticateRequest{Email: "reset@example.com", Password: "a brand new passphrase"}); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.Accounts.CreateSession(context.Background(), &appsv1.CreateSessionRequest{AccountId: acct.Id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accounts.ResolveSession(context.Background(), &appsv1.ResolveSessionRequest{Token: fresh.Token}); err != nil {
		t.Fatalf("session after the reset: %v", err)
	}
	if _, err := s.Accounts.ResetPassword(context.Background(), &appsv1.ResetPasswordRequest{Token: strings.Repeat("x", 43), NewPassword: "a brand new passphrase"}); code(err) != codes.FailedPrecondition {
		t.Fatalf("made-up token: %v", err)
	}
}
