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
	// 3 failures at the start of a window: the sliding estimate stays at the
	// limit until a third of the way into the next window (10:20).
	if d := retryAfter(t, err); d < 19*time.Minute || d > 20*time.Minute {
		t.Fatalf("retry delay %v", d)
	}
	// Unknown emails lock the same way, so lockout reveals nothing about
	// which emails are registered.
	for i := 0; i < 3; i++ {
		_ = login(a, ctx, "nobody@example.com", "whatever password")
	}
	retryAfter(t, login(a, ctx, "nobody@example.com", "whatever password"))
	// The window slides: just after the boundary the three failures still
	// count almost fully; a third of the way into the next window the
	// estimate (3 x 2/3 = 2) is under the limit again, as Retry-After said.
	clk.t = clk.t.Add(15 * time.Minute)
	d := retryAfter(t, login(a, ctx, "lock@example.com", "correct horse battery"))
	if d < 4*time.Minute || d > 6*time.Minute {
		t.Fatalf("retry after the boundary: %v, want about 5 minutes", d)
	}
	clk.t = clk.t.Add(d)
	if err := login(a, ctx, "lock@example.com", "correct horse battery"); err != nil {
		t.Fatalf("after Retry-After: %v", err)
	}
}

// The CI failure that motivated the sliding window: failures split across a
// fixed-window boundary never added up to a lockout.
func TestLockoutHoldsAcrossAWindowBoundary(t *testing.T) {
	a, _, clk := newLimited(t, Limits{FailuresPerEmail: Limit{4, 15 * time.Minute}})
	ctx := context.Background()
	clk.t = time.Date(2026, 11, 22, 10, 14, 55, 0, time.UTC)
	for i := 0; i < 2; i++ {
		_ = login(a, ctx, "edge@example.com", "wrong password")
	}
	clk.t = clk.t.Add(10 * time.Second) // 10:15:05, next fixed window
	for i := 0; i < 2; i++ {
		_ = login(a, ctx, "edge@example.com", "wrong password")
	}
	retryAfter(t, login(a, ctx, "edge@example.com", "wrong password"))
}

func TestSlidingEstimateAndRecovery(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	u := usage{cur: 2, prev: 6, start: start, window: 10 * time.Minute, now: start.Add(5 * time.Minute)}
	if e := u.estimate(); e != 5 {
		t.Fatalf("estimate %v, want 2 + 6/2", e)
	}
	if !u.atLeast(5) || u.atLeast(6) {
		t.Fatal("atLeast")
	}
	// 2 + 6(1-f) <= 3  =>  f >= 5/6.
	if got, want := u.until(3), start.Add(500*time.Second); !got.Equal(want) {
		t.Fatalf("until(3) = %v, want %v", got, want)
	}
	// Current window alone over the target: wait into the next window,
	// 4(1-f) <= 2  =>  f >= 1/2.
	u = usage{cur: 4, start: start, window: 10 * time.Minute, now: start.Add(time.Minute)}
	if got, want := u.until(2), start.Add(15*time.Minute); !got.Equal(want) {
		t.Fatalf("until(2) = %v, want %v", got, want)
	}
	if got := (usage{cur: 1, start: start, window: time.Minute, now: start}).until(3); !got.Equal(start) {
		t.Fatalf("already under: %v", got)
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
