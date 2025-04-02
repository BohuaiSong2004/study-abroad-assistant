package applications

import (
	"context"
	"regexp"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/mail"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

func TestResetLinksExpireAndRequestsAreLimited(t *testing.T) {
	a, _, clk := newLimited(t, DefaultLimits())
	box := mail.NewCapture(8)
	a.SetMailer(box, "http://w.test")
	ctx := context.Background()
	if _, err := a.Register(ctx, &appsv1.RegisterRequest{Email: "exp@example.com", Password: "correct horse battery", DisplayName: "E"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RequestPasswordReset(ctx, &appsv1.RequestPasswordResetRequest{Email: "exp@example.com"}); err != nil {
		t.Fatal(err)
	}
	m, ok := box.Next(3 * time.Second)
	if !ok {
		t.Fatal("no mail")
	}
	tok := regexp.MustCompile(`#token=(\S+)`).FindStringSubmatch(m.Text)[1]
	clk.t = clk.t.Add(ResetTTL + time.Second)
	_, err := a.ResetPassword(ctx, &appsv1.ResetPasswordRequest{Token: tok, NewPassword: "a brand new passphrase"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expired link accepted: %v", err)
	}

	// At most three emails per address per hour, silently: the caller sees
	// the same answer.
	for i := 0; i < 5; i++ {
		if _, err := a.RequestPasswordReset(ctx, &appsv1.RequestPasswordResetRequest{Email: "exp@example.com"}); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for {
		if _, ok := box.Next(500 * time.Millisecond); !ok {
			break
		}
		n++
	}
	if n != 2 { // 3 per hour, one already used above (same hour)
		t.Fatalf("%d reset emails, want 2 more", n)
	}
	// Per address, requests are refused openly past the limit.
	ip := platform.WithClientIP(ctx, "192.0.2.10")
	for i := 0; i < resetPerIP.N; i++ {
		_, _ = a.RequestPasswordReset(ip, &appsv1.RequestPasswordResetRequest{Email: "someone@example.com"})
	}
	if _, err := a.RequestPasswordReset(ip, &appsv1.RequestPasswordResetRequest{Email: "someone@example.com"}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("per-address reset limit: %v", err)
	}
}
