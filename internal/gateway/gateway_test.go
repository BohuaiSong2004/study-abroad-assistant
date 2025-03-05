package gateway_test

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/applications"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/gateway"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/stacktest"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/testutil"
)

type env struct {
	t     *testing.T
	stack *stacktest.Stack
	srv   *httptest.Server
}

func newEnv(t *testing.T, mutate ...func(*gateway.Config)) *env {
	t.Helper()
	s := stacktest.New(t, ddb.CacheLocal)
	cfg := gateway.Config{PublicURL: "http://waypoint.test", AuthPerMinute: 1000, InvitePerMinute: 1000}
	for _, m := range mutate {
		m(&cfg)
	}
	g, err := gateway.New(cfg, gateway.NewClients(s.CatalogConn, s.AppsConn, s.TimelineConn), testutil.Logger(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = g.Hub().Run(ctx) }()
	srv := httptest.NewServer(g)
	t.Cleanup(func() { srv.Close(); cancel() })
	if _, err := s.Catalog.CreateProgram(stacktest.Counselor(), &catalogv1.CreateProgramRequest{Program: stacktest.Program("gw-program")}); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, stack: s, srv: srv}
}

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func (e *env) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: e.t, base: e.srv.URL, http: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// do sends a same-origin request; body (if any) is JSON.
func (c *client) do(method, path string, body any, hdr ...string) (*http.Response, map[string]any) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", c.base)
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			req.Header.Del(hdr[i])
		} else {
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]any{"_raw": string(raw)}
	}
	return res, out
}

func (c *client) expect(code int, method, path string, body any, hdr ...string) map[string]any {
	c.t.Helper()
	res, out := c.do(method, path, body, hdr...)
	if res.StatusCode != code {
		c.t.Fatalf("%s %s: status %d, want %d: %v", method, path, res.StatusCode, code, out)
	}
	return out
}

func (c *client) register(email string) map[string]any {
	return c.expect(200, "POST", "/api/auth/register", map[string]any{"name": "Test Student", "email": email, "password": "correct horse battery"})
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func TestSessionLifecycleAndRoleChecks(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	if out := c.expect(200, "GET", "/api/session", nil); out["authenticated"] != false {
		t.Fatalf("anonymous session %v", out)
	}
	c.expect(401, "GET", "/api/applications", nil)
	c.register("sess@example.com")
	out := c.expect(200, "GET", "/api/session", nil)
	if out["authenticated"] != true || out["account"].(map[string]any)["role"] != "student" {
		t.Fatalf("session %v", out)
	}
	c.expect(200, "GET", "/api/applications", nil)
	c.expect(403, "POST", "/api/programs", map[string]any{})
	c.expect(403, "GET", "/api/sync-status", nil)

	// Cookie attributes.
	u, _ := url.Parse(e.srv.URL)
	var found bool
	for _, ck := range c.http.Jar.Cookies(u) {
		found = found || ck.Name == "wp_session"
	}
	if !found {
		t.Fatal("session cookie not stored")
	}

	c.expect(200, "POST", "/api/auth/logout", map[string]any{})
	c.expect(401, "GET", "/api/applications", nil)

	// Counselor role.
	if err := e.stack.AccountService.EnsureAccount(context.Background(), "lead@example.com", "counselor-password", "Lead", 2); err != nil {
		t.Fatal(err)
	}
	cc := e.client()
	res := cc.expect(200, "POST", "/api/auth/login", map[string]any{"email": "lead@example.com", "password": "counselor-password"})
	if res["redirect"] != "/counselor.html" {
		t.Fatalf("counselor redirect %v", res)
	}
	cc.expect(200, "GET", "/api/sync-status", nil)
	cc.expect(403, "GET", "/api/applications", nil)
}

func TestFormPostFallbackForLogin(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.register("form@example.com")
	c.expect(200, "POST", "/api/auth/logout", map[string]any{})

	post := func(form url.Values) *http.Response {
		req, _ := http.NewRequest("POST", e.srv.URL+"/api/auth/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", e.srv.URL)
		res, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	bad := post(url.Values{"email": {"form@example.com"}, "password": {"wrong password!!"}})
	if bad.StatusCode != http.StatusSeeOther || !strings.HasPrefix(bad.Header.Get("Location"), "/?") || strings.Contains(bad.Header.Get("Location"), "wrong") {
		t.Fatalf("failed form login: %d %s", bad.StatusCode, bad.Header.Get("Location"))
	}
	ok := post(url.Values{"email": {"form@example.com"}, "password": {"correct horse battery"}})
	if ok.StatusCode != http.StatusSeeOther || ok.Header.Get("Location") != "/dashboard.html" {
		t.Fatalf("form login: %d %s", ok.StatusCode, ok.Header.Get("Location"))
	}
	c.expect(200, "GET", "/api/applications", nil)
}

func TestCSRFAndRequestValidation(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.register("csrf@example.com")
	body := map[string]any{"programId": "gw-program"}
	if out := c.expect(403, "POST", "/api/applications", body, "Origin", "https://evil.example"); errCode(out) != "bad_origin" {
		t.Fatalf("cross-origin: %v", out)
	}
	c.expect(403, "POST", "/api/applications", body, "Origin", "", "Sec-Fetch-Site", "cross-site")
	// Browsers send "Origin: null" from opaque contexts (sandboxed frames,
	// some redirects): never treat it as same-origin.
	c.expect(403, "DELETE", "/api/applications/x", nil, "Origin", "null")
	// Configured public origin is accepted even though it differs from Host.
	c.expect(201, "POST", "/api/applications", body, "Origin", "http://waypoint.test")

	// Media type, unknown fields and oversized bodies.
	res, _ := c.do("POST", "/api/applications", nil, "Content-Type", "text/plain")
	if res.StatusCode != 415 {
		t.Fatalf("non-JSON body: %d", res.StatusCode)
	}
	if out := c.expect(400, "POST", "/api/applications", map[string]any{"programId": "x", "admin": true}); errCode(out) != "invalid_json" {
		t.Fatalf("unknown field: %v", out)
	}
	c.expect(413, "POST", "/api/applications/x/milestones", map[string]any{"title": strings.Repeat("a", 70_000)})
	c.expect(400, "POST", "/api/applications", map[string]any{"programId": "gw-program", "round": "round_9"})
	c.expect(404, "GET", "/api/nope", nil)
}

func TestRateLimitPerClientIP(t *testing.T) {
	e := newEnv(t, func(c *gateway.Config) { c.AuthPerMinute = 10 })
	c := e.client()
	creds := map[string]any{"email": "nobody@example.com", "password": "not the password"}
	limited := 0
	for i := 0; i < 12; i++ {
		res, _ := c.do("POST", "/api/auth/login", creds)
		if res.StatusCode == 429 {
			limited++
			if res.Header.Get("Retry-After") == "" {
				t.Fatal("429 without Retry-After")
			}
		}
	}
	if limited == 0 {
		t.Fatal("login was never rate limited")
	}
	// X-Forwarded-For from an untrusted peer must not reset the bucket.
	res, _ := c.do("POST", "/api/auth/login", creds, "X-Forwarded-For", "203.0.113.9")
	if res.StatusCode != 429 {
		t.Fatalf("spoofed X-Forwarded-For bypassed the limiter: %d", res.StatusCode)
	}
}

// The shared limits live in the applications service. The gateway forwards
// the client address and maps the backend's RetryInfo to Retry-After, with
// the in-memory bucket set high enough not to interfere.
func TestBackendLockoutReachesTheBrowserAs429(t *testing.T) {
	e := newEnv(t)
	e.stack.AccountService.SetLimits(applications.Limits{
		FailuresPerEmail: applications.Limit{N: 3, Window: 15 * time.Minute},
		LoginPerIP:       applications.Limit{N: 4, Window: 10 * time.Minute},
	})
	c := e.client()
	c.expect(200, "POST", "/api/auth/register", map[string]any{"email": "gw-lock@example.com", "password": "correct horse battery", "name": "Lock"})
	for i := 0; i < 3; i++ {
		c.expect(401, "POST", "/api/auth/login", map[string]any{"email": "gw-lock@example.com", "password": "wrong password"})
	}
	res, body := c.do("POST", "/api/auth/login", map[string]any{"email": "gw-lock@example.com", "password": "correct horse battery"})
	if res.StatusCode != 429 || res.Header.Get("Retry-After") == "" {
		t.Fatalf("locked account: %d Retry-After=%q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	if msg, _ := body["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "failed sign-ins") {
		t.Fatalf("message %q", msg)
	}
	// That was the fourth attempt from this address: the fifth exceeds the
	// per-address limit whatever the account, which shows the gateway
	// forwarded the address.
	res, _ = c.do("POST", "/api/auth/login", map[string]any{"email": "someone-else@example.com", "password": "whatever password"})
	if res.StatusCode != 429 {
		t.Fatalf("per-address limit: %d", res.StatusCode)
	}
}

func TestApplicationTimelineRecommendationAndXMLOverHTTP(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.register("flow@example.com")

	// Live updates stream.
	events := e.sse(c)

	app := c.expect(201, "POST", "/api/applications", map[string]any{"programId": "gw-program", "round": "round_1"})
	id := app["id"].(string)
	if app["status"] != "planning" || len(app["checklist"].([]any)) != 4 {
		t.Fatalf("application %v", app)
	}
	select {
	case ev := <-events:
		if !strings.Contains(ev, id) {
			t.Fatalf("event for another application: %s", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no live update after creating an application")
	}
	tl := c.expect(200, "GET", "/api/applications/"+id+"/timeline", nil)
	if tl["deadline"] != "2027-01-15" || tl["roundLabel"] != "Round 1" {
		t.Fatalf("timeline %v", tl)
	}
	var sop map[string]any
	for _, m := range tl["milestones"].([]any) {
		if mm := m.(map[string]any); mm["id"] == "gen:sop" {
			sop = mm
		}
	}
	if sop == nil || sop["document"] != "statement_of_purpose" {
		t.Fatalf("document milestone not mapped to checklist: %v", sop)
	}

	// Ticking the checklist propagates to the timeline (applications -> timeline).
	upd := c.expect(200, "PATCH", "/api/applications/"+id, map[string]any{"version": app["version"], "checklist": []any{map[string]any{"document": "statement_of_purpose", "done": true}}})
	c.expect(409, "PATCH", "/api/applications/"+id, map[string]any{"version": app["version"], "notes": "stale"})
	testutil.Eventually(t, 5*time.Second, func() bool {
		tl := c.expect(200, "GET", "/api/applications/"+id+"/timeline", nil)
		for _, m := range tl["milestones"].([]any) {
			if mm := m.(map[string]any); mm["id"] == "gen:sop" {
				return mm["done"] == true
			}
		}
		return false
	}, "checklist change did not reach the timeline")
	_ = upd

	// Custom milestone.
	c.expect(201, "POST", "/api/applications/"+id+"/milestones", map[string]any{"title": "Visit campus", "dueDate": "2026-12-20"})
	c.expect(409, "PATCH", "/api/applications/"+id+"/milestones/gen:submit", map[string]any{"done": true})

	// Recommendation via invitation link.
	rec := c.expect(201, "POST", "/api/applications/"+id+"/recommendations", map[string]any{"name": "Dr. Lin", "email": "lin@example.edu", "waivedAccess": true})
	link := rec["inviteUrl"].(string)
	if !strings.HasPrefix(link, "http://waypoint.test/recommend.html#token=") {
		t.Fatalf("invite url %s", link)
	}
	token := strings.SplitN(link, "#token=", 2)[1]
	anon := e.client()
	inv := anon.expect(200, "POST", "/api/invites/lookup", map[string]any{"token": token})
	if inv["studentName"] != "Test Student" || inv["waivedAccess"] != true {
		t.Fatalf("invite %v", inv)
	}
	anon.expect(400, "POST", "/api/invites/submit", map[string]any{"token": token, "letter": "short"})
	anon.expect(200, "POST", "/api/invites/submit", map[string]any{"token": token, "letter": strings.Repeat("Excellent student. ", 5)})
	anon.expect(409, "POST", "/api/invites/submit", map[string]any{"token": token, "letter": strings.Repeat("Excellent student. ", 5)})
	anon.expect(404, "POST", "/api/invites/lookup", map[string]any{"token": "x" + token[1:]})
	recs := c.expect(200, "GET", "/api/applications/"+id+"/recommendations", nil)["recommendations"].([]any)
	if r := recs[0].(map[string]any); r["status"] != "submitted" || r["letterText"] != nil || r["inviteUrl"] != nil {
		t.Fatalf("recommendation list leaks letter or link: %v", r)
	}

	// XML exports.
	for _, path := range []string{"/api/applications/" + id + "/timeline.xml", "/api/applications/" + id + "/packet.xml", "/api/timelines.xml"} {
		res, err := c.http.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/xml") || !strings.Contains(res.Header.Get("Content-Disposition"), "attachment") {
			t.Fatalf("%s: %d %v", path, res.StatusCode, res.Header)
		}
		if err := xml.Unmarshal(b, new(struct{})); err != nil {
			t.Fatalf("%s is not well-formed: %v", path, err)
		}
		if strings.Contains(path, "packet") && strings.Contains(string(b), "Excellent student") {
			t.Fatal("waived letter text present in packet export")
		}
	}
	res, _ := c.http.Get(e.srv.URL + "/schemas/timeline-v1.xsd")
	if res.StatusCode != 200 {
		t.Fatalf("schema: %d", res.StatusCode)
	}
	res.Body.Close()

	// Another student cannot see any of it.
	other := e.client()
	other.register("other@example.com")
	other.expect(404, "GET", "/api/applications/"+id, nil)
	other.expect(404, "GET", "/api/applications/"+id+"/timeline.xml", nil)

	// Delete.
	res2, _ := c.do("DELETE", "/api/applications/"+id, nil)
	if res2.StatusCode != 204 {
		t.Fatalf("delete: %d", res2.StatusCode)
	}
	c.expect(404, "GET", "/api/applications/"+id, nil)
}

// sse opens /api/events and returns a channel of event data lines.
func (e *env) sse(c *client) <-chan string {
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/events", nil)
	res, err := c.http.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" {
		e.t.Fatalf("events: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	ch := make(chan string, 16)
	go func() {
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "data: ") {
				ch <- line
			}
		}
	}()
	e.t.Cleanup(func() { res.Body.Close() })
	time.Sleep(200 * time.Millisecond) // let the hub register the client
	return ch
}

func TestStaticAssetsAreFingerprintedAndCompressed(t *testing.T) {
	e := newEnv(t)
	get := func(path string, hdr ...string) (*http.Response, []byte) {
		req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		res, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, b
	}
	res, html := get("/")
	if res.StatusCode != 200 || res.Header.Get("Cache-Control") != "no-cache" || res.Header.Get("ETag") == "" {
		t.Fatalf("index: %d %v", res.StatusCode, res.Header)
	}
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("csp %q", csp)
	}
	m := regexp.MustCompile(`src="(/js/common\.js\?v=[0-9a-f]{12})"`).FindSubmatch(html)
	if m == nil {
		t.Fatalf("script reference not fingerprinted:\n%s", html)
	}
	res, _ = get(string(m[1]))
	if res.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("fingerprinted asset cache: %q", res.Header.Get("Cache-Control"))
	}
	res, _ = get("/js/common.js")
	if res.Header.Get("Cache-Control") != "no-cache" {
		t.Fatal("unversioned asset must revalidate")
	}
	res, body := get("/vendor/jquery-3.7.1.min.js", "Accept-Encoding", "gzip")
	if res.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("jquery not gzip-encoded")
	}
	zr, err := gzip.NewReader(strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if !strings.Contains(string(plain), "jQuery") {
		t.Fatal("gzip body corrupt")
	}
	etag := res.Header.Get("ETag")
	res, _ = get("/vendor/jquery-3.7.1.min.js", "If-None-Match", etag)
	if res.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional GET: %d", res.StatusCode)
	}
	res, body = get("/nope.html")
	if res.StatusCode != 404 || !strings.Contains(string(body), "Page not found") {
		t.Fatalf("404 page: %d", res.StatusCode)
	}
	// ServeMux redirects to the cleaned path; following it must never reach
	// a file outside the embedded UI.
	for _, p := range []string{"/../go.mod", "/js/../../go.mod", "/%2e%2e/go.mod"} {
		res, err := http.Get(e.srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Fatalf("path traversal %s: %d", p, res.StatusCode)
		}
	}
}
