package testutil

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

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
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(platform.ClientUnaryInterceptor(InternalToken)),
		grpc.WithChainStreamInterceptor(platform.ClientStreamInterceptor(InternalToken)),
	)
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
