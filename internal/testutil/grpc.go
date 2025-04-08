package testutil

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

// UnaryTimeout is the client deadline for unary calls in tests. It is short
// so that tests notice if it ever leaks onto the long-lived streams.
var UnaryTimeout = 2 * time.Second

// Server starts an in-memory gRPC server (with the production interceptor
// chain) and returns a client connection to it that sends the internal token.
func Server(t testing.TB, register func(s *grpc.Server)) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := platform.NewGRPCServer(Logger(t), InternalToken)
	register(srv.Server)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.ServeListener(ctx, lis); close(done) }()
	opts := append(platform.DialOptions(InternalToken, UnaryTimeout, nil),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	cc, err := grpc.NewClient("passthrough:///bufnet", opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cc.Close()
		cancel()
		<-done
	})
	return cc
}
