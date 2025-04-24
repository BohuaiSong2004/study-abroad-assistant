package svc

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/events"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ids"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/testutil"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// A service process end to end: tables created, gRPC and admin endpoints
// serving, readiness tied to DynamoDB, graceful stop on cancellation.
func TestServiceStartsServesAndStops(t *testing.T) {
	t.Setenv("DYNAMODB_ENDPOINT", testutil.Endpoint(t))
	prefix := "t" + strings.ToLower(ids.New("")[14:]) + "-"
	t.Setenv("TABLE_PREFIX", prefix)
	t.Setenv("INTERNAL_TOKEN", "svc-test-token")
	grpcAddr, adminAddr := freeAddr(t), freeAddr(t)
	t.Setenv("GRPC_ADDR", grpcAddr)
	t.Setenv("ADMIN_ADDR", adminAddr)

	b, err := New("catalog", ddb.TableCatalog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ddb.DeleteTables(ctx, b.DB.Raw, prefix)
	})
	if b.Table(ddb.TableCatalog) != prefix+ddb.TableCatalog || b.Token != "svc-test-token" || b.DB.Local == nil {
		t.Fatalf("base %+v", b)
	}
	ob := events.NewOutbox(b.DB.API, b.Table(ddb.TableCatalog), "catalog", b.Log)
	invalidated := make(chan string, 8)
	watcher := b.CacheWatcher(ob, func(_ *ddb.CachedAPI, table string) func(*eventsv1.Event) {
		return func(ev *eventsv1.Event) { invalidated <- ev.AggregateId }
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx, ddb.TableCatalog, watcher) }()

	testutil.Eventually(t, 10*time.Second, func() bool { return platform.Healthcheck(adminAddr, io.Discard) == 0 }, "admin /readyz never became ready")
	cc, err := grpc.NewClient(grpcAddr, platform.DialOptions("svc-test-token", 2*time.Second, nil)...)
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	if _, err := healthpb.NewHealthClient(cc).Check(context.Background(), &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("grpc health: %v", err)
	}
	// The cache watcher runs as a worker: a committed event reaches it.
	time.Sleep(300 * time.Millisecond) // watcher positioned at the head
	if err := ob.Commit(ctx, nil, events.Spec{Type: "test", AggregateID: "agg-1", Payload: &eventsv1.Heartbeat{}}); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-invalidated:
		if id != "agg-1" {
			t.Fatalf("watcher saw %q", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cache watcher never saw the event")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancel: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("did not stop")
	}
	if platform.Healthcheck(adminAddr, io.Discard) == 0 {
		t.Fatal("admin still answering after shutdown")
	}
}

func TestTokenFromEnv(t *testing.T) {
	t.Setenv("INTERNAL_TOKEN", "")
	t.Setenv("ENVIRONMENT", "")
	if tok, err := TokenFromEnv(); err != nil || tok != DevInternalToken {
		t.Fatalf("development default: %q %v", tok, err)
	}
	t.Setenv("ENVIRONMENT", "production")
	if _, err := TokenFromEnv(); err == nil {
		t.Fatal("production without a token accepted")
	}
	t.Setenv("INTERNAL_TOKEN", "s3cret")
	if tok, err := TokenFromEnv(); err != nil || tok != "s3cret" {
		t.Fatalf("explicit token: %q %v", tok, err)
	}
}

func TestStartupFailsOnBadConfiguration(t *testing.T) {
	t.Setenv("DYNAMODB_ENDPOINT", testutil.Endpoint(t))
	t.Setenv("AUTO_CREATE_TABLES", "false")
	t.Setenv("GRPC_TLS_CA", "/nonexistent/ca.pem") // partial TLS configuration
	if _, err := New("catalog", ddb.TableCatalog); err == nil || !strings.Contains(err.Error(), "tls") {
		t.Fatalf("partial TLS accepted: %v", err)
	}
	t.Setenv("GRPC_TLS_CA", "")
	t.Setenv("CACHE_MODE", "bogus")
	if _, err := New("catalog", ddb.TableCatalog); err == nil {
		t.Fatal("unknown cache mode accepted")
	}
}

func TestRetryGivesUpWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	calls := 0
	err := retry(ctx, func() error { calls++; return context.DeadlineExceeded })
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if err := retry(context.Background(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
