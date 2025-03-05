package applications

import (
	"context"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/testutil"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// Two AccountService instances over one table stand in for two replicas:
// the limits must hold across them.
func newLimited(t *testing.T, l Limits) (*AccountService, *AccountService, *clock) {
	t.Helper()
	c, prefix := testutil.Tables(t, ddb.CacheLocal)
	clk := &clock{time.Date(2026, 11, 22, 10, 0, 1, 0, time.UTC)}
	mk := func() *AccountService {
		s := NewAccountService(c.API, prefix+ddb.TableApplications, 2, testutil.Logger(t))
		s.now = clk.now
		s.SetLimits(l)
		return s
	}
	return mk(), mk(), clk
}

func login(s *AccountService, ctx context.Context, email, pw string) error {
	_, err := s.Authenticate(ctx, &appsv1.AuthenticateRequest{Email: email, Password: pw})
	return err
}

func retryAfter(t *testing.T, err error) time.Duration {
	t.Helper()
	st := status.Convert(err)
	if st.Code() != codes.ResourceExhausted {
		t.Fatalf("want ResourceExhausted, got %v", err)
	}
	for _, d := range st.Details() {
		if ri, ok := d.(*errdetails.RetryInfo); ok {
			return ri.RetryDelay.AsDuration()
		}
	}
	t.Fatal("no RetryInfo detail")
	return 0
}

func TestRepeatedFailedSignInsLockTheAccountAcrossReplicas(t *testing.T) {
	a, b, clk := newLimited(t, Limits{FailuresPerEmail: Limit{3, 15 * time.Minute}})
	ctx := context.Background()
	if _, err := a.Register(ctx, &appsv1.RegisterRequest{Email: "lock@example.com", Password: "correct horse battery", DisplayName: "L"}); err != nil {
		t.Fatal(err)
	}
	// A wrong password, then a right one, clears the count.
	if code := status.Code(login(a, ctx, "lock@example.com", "wrong password 1")); code != codes.Unauthenticated {
		t.Fatal(code)
	}
	if err := login(b, ctx, "lock@example.com", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	// Three failures spread over both replicas lock the account...
	for i, s := range []*AccountService{a, b, a} {
		if code := status.Code(login(s, ctx, "LOCK@example.com", "wrong password")); code != codes.Unauthenticated {
			t.Fatalf("failure %d: %v", i, code)
		}
	}
	// ...even for the right password, on either replica.
	err := login(b, ctx, "lock@example.com", "correct horse battery")
	if d := retryAfter(t, err); d <= 0 || d > 15*time.Minute {
		t.Fatalf("retry delay %v", d)
	}
	// Unknown emails lock the same way, so lockout reveals nothing about
	// which emails are registered.
	for i := 0; i < 3; i++ {
		_ = login(a, ctx, "nobody@example.com", "whatever password")
	}
	retryAfter(t, login(a, ctx, "nobody@example.com", "whatever password"))
	// The next window lets the owner back in.
	clk.t = clk.t.Add(15 * time.Minute)
	if err := login(a, ctx, "lock@example.com", "correct horse battery"); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

func TestSignInAndSignUpLimitsPerAddress(t *testing.T) {
	a, b, _ := newLimited(t, Limits{LoginPerIP: Limit{4, 10 * time.Minute}, RegisterPerIP: Limit{2, time.Hour}})
	ip1 := platform.WithClientIP(context.Background(), "203.0.113.7")
	ip2 := platform.WithClientIP(context.Background(), "198.51.100.9")
	reg := func(s *AccountService, ctx context.Context, email string) error {
		_, err := s.Register(ctx, &appsv1.RegisterRequest{Email: email, Password: "correct horse battery", DisplayName: "R"})
		return err
	}
	if err := reg(a, ip1, "r1@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := reg(b, ip1, "r2@example.com"); err != nil {
		t.Fatal(err)
	}
	retryAfter(t, reg(a, ip1, "r3@example.com"))
	if err := reg(a, ip2, "r3@example.com"); err != nil {
		t.Fatalf("another address is not limited: %v", err)
	}
	if err := reg(a, context.Background(), "r4@example.com"); err != nil {
		t.Fatalf("calls without a client address (internal) are not limited: %v", err)
	}
	// Sign-in attempts per address count successes too (credential stuffing
	// rotates emails), across replicas.
	for i, s := range []*AccountService{a, b, a, b} {
		if err := login(s, ip1, "r1@example.com", "correct horse battery"); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	retryAfter(t, login(a, ip1, "r1@example.com", "correct horse battery"))
	if err := login(a, ip2, "r1@example.com", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
}
