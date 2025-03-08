// Package gateway is the public HTTP entry point: it serves the jQuery UI,
// exposes a JSON API, authenticates users with session cookies and calls the
// backend gRPC services on their behalf.
package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/grpc"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/cache"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ids"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

var httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "waypoint_http_request_duration_seconds",
	Help:    "Gateway HTTP request latency by route and status class.",
	Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
}, []string{"route", "method", "class"})

// Config configures the gateway.
type Config struct {
	// PublicURL is the externally visible origin, used to build
	// recommendation links and as an allowed Origin.
	PublicURL string
	// ExtraOrigins are additional allowed origins for unsafe requests.
	ExtraOrigins []string
	// CookieSecure sets the Secure attribute (on whenever PublicURL is https).
	CookieSecure bool
	// TrustedProxies whose X-Forwarded-For is believed.
	TrustedProxies []netip.Prefix
	// WebDir serves the UI from disk (development) instead of the embedded copy.
	WebDir string
	// AuthPerMinute / InvitePerMinute rate-limit login+register and the
	// recommender endpoints per client IP (0 disables).
	AuthPerMinute, InvitePerMinute int
	// RequestTimeout bounds each backend call.
	RequestTimeout time.Duration
}

// ConfigFromEnv reads PUBLIC_URL, ALLOWED_ORIGINS, COOKIE_SECURE,
// TRUSTED_PROXIES, WEB_DIR, AUTH_RATE_PER_MIN, INVITE_RATE_PER_MIN.
func ConfigFromEnv() (Config, error) {
	c := Config{
		PublicURL:       strings.TrimRight(platform.Env("PUBLIC_URL", "http://localhost:8080"), "/"),
		WebDir:          platform.Env("WEB_DIR", ""),
		AuthPerMinute:   platform.EnvInt("AUTH_RATE_PER_MIN", 20),
		InvitePerMinute: platform.EnvInt("INVITE_RATE_PER_MIN", 60),
		RequestTimeout:  platform.EnvDuration("BACKEND_TIMEOUT", 5*time.Second),
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Host == "" {
		return c, fmt.Errorf("PUBLIC_URL must be an absolute URL")
	}
	c.CookieSecure = platform.EnvBool("COOKIE_SECURE", u.Scheme == "https")
	for _, o := range strings.Split(platform.Env("ALLOWED_ORIGINS", ""), ",") {
		if o = strings.TrimSpace(o); o != "" {
			c.ExtraOrigins = append(c.ExtraOrigins, strings.TrimRight(o, "/"))
		}
	}
	for _, p := range strings.Split(platform.Env("TRUSTED_PROXIES", ""), ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		pref, err := netip.ParsePrefix(p)
		if err != nil {
			return c, fmt.Errorf("TRUSTED_PROXIES: %w", err)
		}
		c.TrustedProxies = append(c.TrustedProxies, pref)
	}
	return c, nil
}

// Clients are the backend connections.
type Clients struct {
	Catalog        catalogv1.CatalogServiceClient
	Accounts       appsv1.AccountServiceClient
	Apps           appsv1.ApplicationServiceClient
	Recs           appsv1.RecommendationServiceClient
	Timeline       timelinev1.TimelineServiceClient
	TimelineEvents eventsv1.EventStreamServiceClient
}

// NewClients builds clients from connections.
func NewClients(catalog, apps, tl grpc.ClientConnInterface) Clients {
	return Clients{
		Catalog:        catalogv1.NewCatalogServiceClient(catalog),
		Accounts:       appsv1.NewAccountServiceClient(apps),
		Apps:           appsv1.NewApplicationServiceClient(apps),
		Recs:           appsv1.NewRecommendationServiceClient(apps),
		Timeline:       timelinev1.NewTimelineServiceClient(tl),
		TimelineEvents: eventsv1.NewEventStreamServiceClient(tl),
	}
}

// Server is the gateway.
type Server struct {
	cfg      Config
	c        Clients
	log      *slog.Logger
	mux      *http.ServeMux
	sessions *cache.LRU[string, sessionEntry]
	authRL   *limiter
	inviteRL *limiter
	hub      *Hub
	assets   *assets
	origins  map[string]bool
}

type sessionEntry struct {
	principal platform.Principal
	account   accountDTO
}

// New builds the gateway. Call Hub().Run in the background for live updates.
func New(cfg Config, c Clients, log *slog.Logger) (*Server, error) {
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 5 * time.Second
	}
	as, err := newAssets(cfg.WebDir)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg: cfg, c: c, log: log, mux: http.NewServeMux(),
		sessions: cache.New[string, sessionEntry](50_000, 5*time.Second),
		authRL:   newLimiter(cfg.AuthPerMinute, max(cfg.AuthPerMinute/2, 5)),
		inviteRL: newLimiter(cfg.InvitePerMinute, max(cfg.InvitePerMinute/2, 5)),
		hub:      NewHub(c.TimelineEvents, log),
		assets:   as,
		origins:  map[string]bool{cfg.PublicURL: true},
	}
	for _, o := range cfg.ExtraOrigins {
		s.origins[o] = true
	}
	s.routes()
	return s, nil
}

// Hub returns the live-update hub.
func (s *Server) Hub() *Hub { return s.hub }

type handler func(w http.ResponseWriter, r *http.Request)

func (s *Server) handle(pattern string, h handler, mw ...func(handler) handler) {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	route := pattern
	s.mux.Handle(pattern, s.observe(route, h))
}

func (s *Server) routes() {
	// Session & auth.
	s.handle("GET /api/session", s.getSession)
	s.handle("POST /api/auth/register", s.register, s.rateLimit(s.authRL))
	s.handle("POST /api/auth/login", s.login, s.rateLimit(s.authRL))
	s.handle("POST /api/auth/logout", s.logout)

	// Catalog (public read, counselor write).
	s.handle("GET /api/programs", s.listPrograms)
	s.handle("GET /api/programs/facets", s.facets)
	s.handle("GET /api/programs/{id}", s.getProgram)
	s.handle("POST /api/programs", s.createProgram, s.require(platform.RoleCounselor))
	s.handle("PUT /api/programs/{id}", s.updateProgram, s.require(platform.RoleCounselor))
	s.handle("GET /api/sync-status", s.syncStatus, s.require(platform.RoleCounselor))

	// Student workspace.
	st := s.require(platform.RoleStudent)
	s.handle("GET /api/applications", s.listApplications, st)
	s.handle("POST /api/applications", s.createApplication, st)
	s.handle("GET /api/applications/{id}", s.getApplication, st)
	s.handle("PATCH /api/applications/{id}", s.updateApplication, st)
	s.handle("DELETE /api/applications/{id}", s.deleteApplication, st)
	s.handle("GET /api/applications/{id}/timeline", s.getTimeline, st)
	s.handle("POST /api/applications/{id}/milestones", s.addMilestone, st)
	s.handle("PATCH /api/applications/{id}/milestones/{mid}", s.updateMilestone, st)
	s.handle("DELETE /api/applications/{id}/milestones/{mid}", s.deleteMilestone, st)
	s.handle("GET /api/applications/{id}/recommendations", s.listRecommendations, st)
	s.handle("POST /api/applications/{id}/recommendations", s.requestRecommendation, st)
	s.handle("POST /api/applications/{id}/recommendations/{rid}/cancel", s.cancelRecommendation, st)
	s.handle("POST /api/applications/{id}/recommendations/{rid}/regenerate", s.regenerateLink, st)
	s.handle("GET /api/timelines", s.listTimelines, st)
	s.handle("GET /api/agenda", s.agenda, st)
	s.handle("GET /api/events", s.events, st)

	// XML exports.
	s.handle("GET /api/applications/{id}/timeline.xml", s.timelineXML, st)
	s.handle("GET /api/applications/{id}/packet.xml", s.packetXML, st)
	s.handle("GET /api/timelines.xml", s.allTimelinesXML, st)
	s.handle("GET /schemas/{file}", s.schema)

	// Recommenders (no account; the invitation token authorizes).
	s.handle("POST /api/invites/lookup", s.inviteLookup, s.rateLimit(s.inviteRL))
	s.handle("POST /api/invites/submit", s.inviteSubmit, s.rateLimit(s.inviteRL))
	s.handle("POST /api/invites/decline", s.inviteDecline, s.rateLimit(s.inviteRL))

	s.handle("GET /api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	s.handle("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	s.handle("GET /", s.assets.serve)
}

// ServeHTTP applies the outer middleware: request id, recovery, security
// headers, CSRF origin check and session resolution.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.Header.Get("X-Request-Id")
	if id == "" || len(id) > 64 {
		id = ids.New("req_")
	}
	w.Header().Set("X-Request-Id", id)
	ctx := platform.WithRequestID(r.Context(), id)
	defer func() {
		if rec := recover(); rec != nil {
			s.log.ErrorContext(ctx, "panic", "panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
			writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
		}
	}()
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	// "same-origin", not "no-referrer": under no-referrer, Chrome sends
	// "Origin: null" on same-site form posts, which the CSRF check must
	// reject, and that broke the no-JavaScript sign-in fallback.
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'")
	if s.cfg.CookieSecure {
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "bad_origin", "cross-site request rejected")
		return
	}
	ctx = s.resolveSession(ctx, r)
	s.mux.ServeHTTP(w, r.WithContext(ctx))
}

// sameOrigin is the CSRF defence for cookie-authenticated unsafe requests:
// browsers send Origin (or Sec-Fetch-Site) on every POST/PUT/PATCH/DELETE,
// so a cross-site form or fetch is rejected before it reaches a handler.
// Requests without either header come from non-browser clients, which do not
// carry a victim's cookies.
func (s *Server) sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if o := r.Header.Get("Origin"); o != "" {
		if s.origins[o] {
			return true
		}
		u, err := url.Parse(o)
		return err == nil && u.Host == r.Host
	}
	return r.Header.Get("Sec-Fetch-Site") != "cross-site"
}

func (s *Server) observe(route string, h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		h(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		class := strconv.Itoa(rec.status/100) + "xx"
		httpDuration.WithLabelValues(route, r.Method, class).Observe(time.Since(start).Seconds())
		if strings.HasPrefix(r.URL.Path, "/api/") && route != "GET /api/events" {
			lvl := slog.LevelDebug
			if rec.status >= 500 {
				lvl = slog.LevelError
			} else if rec.status >= 400 {
				lvl = slog.LevelInfo
			}
			s.log.Log(r.Context(), lvl, "http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
				"duration_ms", float64(time.Since(start).Microseconds())/1000)
		}
	})
}

// require rejects requests without a session of one of the roles.
func (s *Server) require(roles ...string) func(handler) handler {
	return func(next handler) handler {
		return func(w http.ResponseWriter, r *http.Request) {
			p, ok := principal(r)
			if !ok {
				writeError(w, http.StatusUnauthorized, "unauthenticated", "Please sign in.")
				return
			}
			for _, role := range roles {
				if p.Role == role {
					next(w, r)
					return
				}
			}
			writeError(w, http.StatusForbidden, "forbidden", "Your account cannot do this.")
		}
	}
}

func (s *Server) rateLimit(l *limiter) func(handler) handler {
	return func(next handler) handler {
		return func(w http.ResponseWriter, r *http.Request) {
			if !l.allow(clientIP(r, s.cfg.TrustedProxies)) {
				w.Header().Set("Retry-After", "30")
				writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Please wait a moment and try again.")
				return
			}
			next(w, r)
		}
	}
}

// backend returns a context for a backend call with the request's identity
// and a deadline.
func (s *Server) backend(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
}
