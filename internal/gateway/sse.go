package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
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
	clients map[string]map[chan update]struct{}
	ready   bool
}

// NewHub creates a hub.
func NewHub(c eventsv1.EventStreamServiceClient, log *slog.Logger) *Hub {
	return &Hub{client: c, log: log, clients: map[string]map[chan update]struct{}{}}
}

// Run subscribes until ctx is cancelled, reconnecting with backoff.
func (h *Hub) Run(ctx context.Context) error {
	ctx = platform.ServiceContext(ctx, "gateway")
	backoff := 200 * time.Millisecond
	first := true
	for ctx.Err() == nil {
		err := h.session(ctx, !first)
		first = false
		h.setReady(false)
		if ctx.Err() != nil {
			return nil
		}
		h.log.Warn("timeline stream disconnected", "error", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff/2 + time.Duration(rand.Int64N(int64(backoff)))):
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

func (h *Hub) session(ctx context.Context, resync bool) error {
	st, err := h.client.Subscribe(ctx, &eventsv1.SubscribeRequest{FromTail: true})
	if err != nil {
		return err
	}
	connected := false
	for {
		msg, err := st.Recv()
		if err != nil {
			return err
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

func (h *Hub) subscribe(student string) chan update {
	ch := make(chan update, 32)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[student] == nil {
		h.clients[student] = map[chan update]struct{}{}
	}
	h.clients[student][ch] = struct{}{}
	sseClients.Inc()
	return ch
}

func (h *Hub) unsubscribe(student string, ch chan update) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients[student], ch)
	if len(h.clients[student]) == 0 {
		delete(h.clients, student)
	}
	sseClients.Dec()
}

func (h *Hub) publish(student string, u update) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients[student] {
		select {
		case ch <- u:
		default: // slow client: it will get a resync on reconnect
		}
	}
}

func (h *Hub) broadcast(u update) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, set := range h.clients {
		for ch := range set {
			select {
			case ch <- u:
			default:
			}
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
	ch := s.hub.subscribe(p.UserID)
	defer s.hub.unsubscribe(p.UserID, ch)
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
		case u := <-ch:
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
