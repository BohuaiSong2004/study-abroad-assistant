package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/stacktest"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/testutil"
)

func TestClientIPOnlyTrustsConfiguredProxies(t *testing.T) {
	lb := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	cases := []struct {
		name, remote, xff string
		trusted           []netip.Prefix
		want              string
	}{
		{"direct client, no proxies", "198.51.100.7:5000", "1.2.3.4", nil, "198.51.100.7"},
		{"spoofed header from untrusted peer", "198.51.100.7:5000", "1.2.3.4", lb, "198.51.100.7"},
		{"behind load balancer", "10.1.2.3:5000", "203.0.113.5", lb, "203.0.113.5"},
		{"client-supplied prefix is ignored", "10.1.2.3:5000", "1.2.3.4, 203.0.113.5", lb, "203.0.113.5"},
		{"chain of trusted proxies", "10.1.2.3:5000", "203.0.113.5, 10.9.9.9", lb, "203.0.113.5"},
		{"garbage header falls back to peer", "10.1.2.3:5000", "not-an-ip", lb, "10.1.2.3"},
		{"ipv6 peer", "[2001:db8::1]:443", "", nil, "2001:db8::1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.remote
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if got := clientIP(r, c.trusted); got != c.want {
				t.Fatalf("got %s want %s", got, c.want)
			}
		})
	}
}

func TestRPCErrorMapping(t *testing.T) {
	cases := map[codes.Code]int{
		codes.InvalidArgument: 400, codes.NotFound: 404, codes.AlreadyExists: 409, codes.Aborted: 409,
		codes.FailedPrecondition: 409, codes.PermissionDenied: 403, codes.Unauthenticated: 401,
		codes.ResourceExhausted: 429, codes.Unavailable: 503, codes.Internal: 500, codes.Unknown: 500,
	}
	for c, want := range cases {
		w := httptest.NewRecorder()
		writeRPCError(w, httptest.NewRequest("GET", "/x", nil), testutil.Logger(t), status.Error(c, "secret detail"))
		if w.Code != want {
			t.Errorf("%s -> %d, want %d", c, w.Code, want)
		}
		if want == 500 && containsDetail(w) {
			t.Errorf("%s leaked the internal message", c)
		}
	}
}

func containsDetail(w *httptest.ResponseRecorder) bool {
	return len(w.Body.String()) > 0 && (contains(w.Body.String(), "secret detail"))
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestProgramDTORoundTrip(t *testing.T) {
	p := stacktest.Program("dto")
	p.Requirements.MinIeltsTenths = 65
	p.Requirements.GreRequired = true
	out := programOut(p)
	if out.Degree != "master" || out.Deadlines[0].Round != "round_1" || out.Requirements.MinIelts != 6.5 || out.ApplicationFee.AmountCents != 9000 {
		t.Fatalf("out %+v", out)
	}
	back, err := programIn(out)
	if err != nil {
		t.Fatal(err)
	}
	if back.Degree != p.Degree || back.Requirements.MinIeltsTenths != 65 || len(back.Requirements.Documents) != 3 || back.Deadlines[1].Date != "2027-03-01" {
		t.Fatalf("round trip %+v", back)
	}
	out.Degree = "wizard"
	if _, err := programIn(out); err == nil {
		t.Fatal("unknown degree accepted")
	}
}

func TestLimiterBurstAndRefill(t *testing.T) {
	l := newLimiter(60, 3)
	for i := 0; i < 3; i++ {
		if !l.allow("a") {
			t.Fatalf("request %d within burst denied", i)
		}
	}
	if l.allow("a") {
		t.Fatal("burst exceeded")
	}
	if !l.allow("b") {
		t.Fatal("buckets must be per key")
	}
	var nilLimiter *limiter
	if !nilLimiter.allow("x") {
		t.Fatal("disabled limiter must allow")
	}
	_ = http.StatusOK
}
