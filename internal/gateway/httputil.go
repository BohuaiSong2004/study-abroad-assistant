package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/cache"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

const maxBody = 64 << 10

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, slug, msg string) {
	writeJSON(w, code, map[string]apiError{"error": {Code: slug, Message: msg}})
}

var grpcToHTTP = map[codes.Code]struct {
	status int
	slug   string
}{
	codes.InvalidArgument:    {http.StatusBadRequest, "invalid_argument"},
	codes.NotFound:           {http.StatusNotFound, "not_found"},
	codes.AlreadyExists:      {http.StatusConflict, "already_exists"},
	codes.Aborted:            {http.StatusConflict, "conflict"},
	codes.FailedPrecondition: {http.StatusConflict, "failed_precondition"},
	codes.PermissionDenied:   {http.StatusForbidden, "forbidden"},
	codes.Unauthenticated:    {http.StatusUnauthorized, "unauthenticated"},
	codes.ResourceExhausted:  {http.StatusTooManyRequests, "limit_reached"},
	codes.Unavailable:        {http.StatusServiceUnavailable, "unavailable"},
	codes.DeadlineExceeded:   {http.StatusGatewayTimeout, "timeout"},
	codes.Canceled:           {499, "cancelled"},
}

// writeRPCError maps a gRPC error to an HTTP response. Internal details are
// logged, not returned.
func writeRPCError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	st := status.Convert(err)
	if m, ok := grpcToHTTP[st.Code()]; ok {
		writeError(w, m.status, m.slug, st.Message())
		return
	}
	log.ErrorContext(r.Context(), "backend error", "path", r.URL.Path, "code", st.Code().String(), "error", st.Message())
	writeError(w, http.StatusInternalServerError, "internal", "Something went wrong. Please try again.")
}

var errNotJSON = errors.New("Content-Type must be application/json")

// readJSON decodes a JSON body strictly (size-limited, no unknown fields).
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", errNotJSON.Error())
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body: "+err.Error())
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "invalid_json", "unexpected data after JSON body")
		return false
	}
	_, _ = io.Copy(io.Discard, r.Body)
	return true
}

// clientIP returns the caller's address. X-Forwarded-For is only honoured
// when the direct peer is a trusted proxy (the load balancer); otherwise any
// client could spoof it to dodge rate limits. With trusted proxies, the
// right-most address that is not itself a trusted proxy is the client.
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a.Unmap()) {
				return true
			}
		}
		return false
	}
	if !isTrusted(peer) {
		return peer.Unmap().String()
	}
	parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			break
		}
		if !isTrusted(a) {
			return a.Unmap().String()
		}
	}
	return peer.Unmap().String()
}

// limiter is a per-key token bucket with bounded memory.
type limiter struct {
	mu      sync.Mutex
	buckets *cache.LRU[string, *rate.Limiter]
	every   time.Duration
	burst   int
}

func newLimiter(perMinute, burst int) *limiter {
	if perMinute <= 0 {
		return nil
	}
	return &limiter{buckets: cache.New[string, *rate.Limiter](100_000, 10*time.Minute), every: time.Minute / time.Duration(perMinute), burst: burst}
}

// allow reports whether key may proceed; nil limiters always allow.
func (l *limiter) allow(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	b, ok := l.buckets.Get(key)
	if !ok {
		b = rate.NewLimiter(rate.Every(l.every), l.burst)
		l.buckets.Set(key, b)
	}
	l.mu.Unlock()
	return b.Allow()
}

// statusRecorder captures the response status for logging and metrics.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Flush supports streaming responses (SSE) through the recorder.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func principal(r *http.Request) (platform.Principal, bool) {
	return platform.PrincipalFrom(r.Context())
}
