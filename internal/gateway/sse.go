package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

var sseClients = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "waypoint_sse_clients", Help: "Open live-update (Server-Sent Events) connections.",
})

// update is what the browser receives.
type update struct {
	Type          string `json:"type"` // "timeline" or "resync"
	ApplicationID string `json:"applicationId,omitempty"`
	Version       int64  `json:"version,omitempty"`
	Deleted       bool   `json:"deleted,omitempty"`
}

// Hub fans timeline changes out to the browsers of the affected student.
// Each gateway replica holds one Subscribe stream to the timeline service
// (from the tail: live updates only) and routes events by student id.
// After a reconnect it tells every client to "resync", because changes may
// have happened while the stream was down.
type Hub struct {
	client eventsv1.EventStreamServiceClient
	log    *slog.Logger

	mu      sync.Mutex
	clients map[string]map[*sseClient]struct{}
	ready   bool
}

// sseClient is one open /api/events connection.
type sseClient struct {
	ch chan update
	// needResync is set when an update could not be queued because the
	// client is not keeping up; the connection then sends "resync" so the
	// page reloads its state instead of silently missing a change.
	needResync atomic.Bool
	kick       chan struct{}
}

func (c *sseClient) offer(u update) {
	select {
	case c.ch <- u:
	default:
		c.needResync.Store(true)
		select {
		case c.kick <- struct{}{}:
		default:
		}
	}
}

// NewHub creates a hub.
func NewHub(c eventsv1.EventStreamServiceClient, log *slog.Logger) *Hub {
	return &Hub{client: c, log: log, clients: map[string]map[*sseClient]struct{}{}}
}

// Run subscribes until ctx is cancelled, reconnecting with backoff.
func (h *Hub) Run(ctx context.Context) error {
	ctx = platform.ServiceContext(ctx, "gateway")
	backoff := 200 * time.Millisecond
	first := true
	for ctx.Err() == nil {
		healthy, err := h.session(ctx, !first)
		first = false
		h.setReady(false)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // shutting down
		}
		if healthy {
			backoff = 200 * time.Millisecond
		}
		h.log.Warn("timeline stream disconnected", "error", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff/2 + time.Duration(rand.Int64N(int64(backoff)))): //nolint:gosec // jitter
		}
		backoff = min(backoff*2, 10*time.Second)
	}
	return nil
}

func (h *Hub) setReady(v bool) {
	h.mu.Lock()
	h.ready = v
	h.mu.Unlock()
}

func (h *Hub) session(ctx context.Context, resync bool) (bool, error) {
	st, err := h.client.Subscribe(ctx, &eventsv1.SubscribeRequest{FromTail: true})
	if err != nil {
		return false, err
	}
	connected := false
	for {
		msg, err := st.Recv()
		if err != nil {
			return connected, err
		}
		if !connected {
			connected = true
			h.setReady(true)
			if resync {
				h.broadcast(update{Type: "resync"})
			}
		}
		ev := msg.GetEvent()
		if ev == nil {
			continue
		}
		var tc timelinev1.TimelineChanged
		if err := ev.Payload.UnmarshalTo(&tc); err != nil {
			continue
		}
		h.publish(tc.StudentId, update{Type: "timeline", ApplicationID: tc.ApplicationId, Version: tc.Version, Deleted: tc.Deleted})
	}
}

func (h *Hub) subscribe(student string) *sseClient {
	c := &sseClient{ch: make(chan update, 32), kick: make(chan struct{}, 1)}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[student] == nil {
		h.clients[student] = map[*sseClient]struct{}{}
	}
	h.clients[student][c] = struct{}{}
	sseClients.Inc()
	return c
}

func (h *Hub) unsubscribe(student string, c *sseClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients[student], c)
	if len(h.clients[student]) == 0 {
		delete(h.clients, student)
	}
	sseClients.Dec()
}

func (h *Hub) publish(student string, u update) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients[student] {
		c.offer(u)
	}
}

func (h *Hub) broadcast(u update) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, set := range h.clients {
		for c := range set {
			c.offer(u)
		}
	}
}

// events streams the student's timeline changes as Server-Sent Events.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r)
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	c := s.hub.subscribe(p.UserID)
	defer s.hub.unsubscribe(p.UserID, c)
	// Tell the browser how long to wait before reconnecting.
	if _, err := fmt.Fprint(w, "retry: 3000\n: connected\n\n"); err != nil {
		return
	}
	_ = rc.Flush()
	beat := time.NewTicker(20 * time.Second)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
		case <-c.kick:
			if c.needResync.Swap(false) {
				if _, err := fmt.Fprint(w, "event: resync\ndata: {\"type\":\"resync\"}\n\n"); err != nil {
					return
				}
			}
		case u := <-c.ch:
			b, _ := json.Marshal(u)
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", u.Type, b); err != nil {
				return
			}
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}
