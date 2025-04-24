package platform

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthcheckExitCodes(t *testing.T) {
	ready := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
			return
		}
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	if code := Healthcheck(addr, io.Discard); code != 0 {
		t.Fatalf("ready: %d", code)
	}
	ready = false
	var msg strings.Builder
	if code := Healthcheck(addr, &msg); code != 1 || !strings.Contains(msg.String(), "503") {
		t.Fatalf("not ready: %d %q", code, msg.String())
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	_, port, _ := net.SplitHostPort(l.Addr().String())
	_ = l.Close()
	if code := Healthcheck(":"+port, io.Discard); code != 1 {
		t.Fatalf("nothing listening: %d", code)
	}
}

func TestRunContextStopsEverythingWhenOneFails(t *testing.T) {
	boom := errors.New("boom")
	stopped := make(chan struct{})
	err := RunContext(context.Background(),
		func(ctx context.Context) error { <-ctx.Done(); close(stopped); return ctx.Err() },
		func(context.Context) error { return boom },
	)
	if !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("the other worker kept running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunContext(ctx, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }); err != nil {
		t.Fatalf("cancellation is not an error: %v", err)
	}
}
