package platform

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// Regression: after a backend restart the gateway failed requests with
// UNAVAILABLE until the channel's reconnect backoff expired. Calls must now
// wait for the backend (within their deadline) instead of failing fast.
func TestCallsWaitForABackendThatIsStillStarting(t *testing.T) {
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close() // nothing listens yet: connection refused

	cc, err := Dial("passthrough:///"+addr, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()

	res := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := healthpb.NewHealthClient(cc).Check(ctx, &healthpb.HealthCheckRequest{})
		res <- err
	}()

	time.Sleep(700 * time.Millisecond) // the backend comes up later
	srv := NewGRPCServer(NewLogger("test"), "")
	srv.Health.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	lis2, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
	if err != nil {
		t.Skipf("port reuse not possible here: %v", err)
	}
	go func() { _ = srv.Server.Serve(lis2) }()
	defer srv.Stop()

	if err := <-res; err != nil {
		t.Fatalf("call failed instead of waiting for the backend: %v", err)
	}
	_ = health.NewServer
}
