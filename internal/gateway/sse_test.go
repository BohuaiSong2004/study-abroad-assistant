package gateway

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/testutil"
)

// Regression: when a browser fell behind, updates were dropped silently and
// the page kept showing stale data. Overflow must turn into a "resync".
func TestSlowSSEClientIsToldToResync(t *testing.T) {
	s, err := New(Config{PublicURL: "http://x.test"}, Clients{}, testutil.Logger(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(platform.WithPrincipal(context.Background(), platform.Principal{UserID: "s1", Role: platform.RoleStudent}))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { s.events(rec, httptest.NewRequest("GET", "/api/events", nil).WithContext(ctx)); close(done) }()

	// Wait until the handler has registered, then offer far more updates
	// than the 32-slot buffer in one burst, faster than the handler drains.
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.hub.mu.Lock()
		n := len(s.hub.clients["s1"])
		s.hub.mu.Unlock()
		if n == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.hub.mu.Lock()
	for c := range s.hub.clients["s1"] {
		for i := 0; i < 100; i++ {
			c.offer(update{Type: "timeline", ApplicationID: "app_x", Version: int64(i)})
		}
	}
	s.hub.mu.Unlock()
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
	body := rec.Body.String()
	if !strings.Contains(body, "event: resync") {
		t.Fatalf("overflowing client was not told to resync:\n%s", body[:min(len(body), 400)])
	}
	if !strings.Contains(body, "event: timeline") {
		t.Fatal("buffered updates should still be delivered")
	}
}
