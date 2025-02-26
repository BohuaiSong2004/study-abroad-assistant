package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime/debug"
	"time"

	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ids"
)

var (
	serverMetrics = grpcprom.NewServerMetrics(grpcprom.WithServerHandlingTimeHistogram(
		grpcprom.WithHistogramBuckets([]float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5})))
	clientMetrics = grpcprom.NewClientMetrics(grpcprom.WithClientHandlingTimeHistogram(
		grpcprom.WithHistogramBuckets([]float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5})))
)

func init() {
	prometheus.MustRegister(serverMetrics, clientMetrics)
}

// GRPCServer wraps a grpc.Server with health reporting.
type GRPCServer struct {
	*grpc.Server
	Health *health.Server
	log    *slog.Logger
}

// NewGRPCServer builds a server with recovery, internal-auth, request-id,
// logging and Prometheus interceptors. internalToken may be empty only in
// tests.
func NewGRPCServer(log *slog.Logger, internalToken string) *GRPCServer {
	authUnary := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if isHealth(info.FullMethod) {
			return h(ctx, req)
		}
		ctx, err := principalFromIncoming(ctx, internalToken)
		if err != nil {
			return nil, err
		}
		if RequestID(ctx) == "" {
			ctx = WithRequestID(ctx, ids.New("req_"))
		}
		start := time.Now()
		resp, err := h(ctx, req)
		logRPC(ctx, log, info.FullMethod, start, err)
		return resp, err
	}
	authStream := func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		if isHealth(info.FullMethod) {
			return h(srv, ss)
		}
		ctx, err := principalFromIncoming(ss.Context(), internalToken)
		if err != nil {
			return err
		}
		start := time.Now()
		err = h(srv, &ctxStream{ServerStream: ss, ctx: ctx})
		logRPC(ctx, log, info.FullMethod, start, err)
		return err
	}
	recoverUnary := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.ErrorContext(ctx, "panic in handler", "method", info.FullMethod, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return h(ctx, req)
	}
	recoverStream := func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic in stream handler", "method", info.FullMethod, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return h(srv, ss)
	}
	s := grpc.NewServer(
		grpc.ChainUnaryInterceptor(serverMetrics.UnaryServerInterceptor(), recoverUnary, authUnary),
		grpc.ChainStreamInterceptor(serverMetrics.StreamServerInterceptor(), recoverStream, authStream),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 10 * time.Second, PermitWithoutStream: true}),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
	)
	hs := health.NewServer()
	healthpb.RegisterHealthServer(s, hs)
	if EnvBool("GRPC_REFLECTION", false) {
		reflection.Register(s)
	}
	return &GRPCServer{Server: s, Health: hs, log: log}
}

// Serve listens on addr until ctx is cancelled, then drains gracefully.
func (s *GRPCServer) Serve(ctx context.Context, addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.ServeListener(ctx, lis)
}

// ServeListener is Serve on an existing listener (tests use bufconn).
func (s *GRPCServer) ServeListener(ctx context.Context, lis net.Listener) error {
	serverMetrics.InitializeMetrics(s.Server)
	s.Health.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	errc := make(chan error, 1)
	go func() { errc <- s.Server.Serve(lis) }()
	s.log.Info("grpc listening", "addr", lis.Addr().String())
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		s.Health.Shutdown()
		done := make(chan struct{})
		go func() { s.GracefulStop(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			s.Stop()
		}
		return nil
	}
}

func isHealth(method string) bool {
	return method == "/grpc.health.v1.Health/Check" || method == "/grpc.health.v1.Health/Watch" || method == "/grpc.health.v1.Health/List"
}

func logRPC(ctx context.Context, log *slog.Logger, method string, start time.Time, err error) {
	code := status.Code(err)
	attrs := []any{"method", method, "code", code.String(), "duration_ms", float64(time.Since(start).Microseconds()) / 1000}
	switch code {
	case codes.OK:
		log.DebugContext(ctx, "rpc", attrs...)
	case codes.Internal, codes.Unknown, codes.DataLoss, codes.Unavailable:
		log.ErrorContext(ctx, "rpc failed", append(attrs, "error", err.Error())...)
	default:
		log.InfoContext(ctx, "rpc", attrs...)
	}
}

type ctxStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *ctxStream) Context() context.Context { return s.ctx }

// serviceConfig: unary calls get a deadline and retry on UNAVAILABLE
// (connection loss, pod restart). Mutating RPCs are safe to retry because
// every write is guarded by a DynamoDB condition (version or
// attribute_not_exists).
//
// The long-lived EventStreamService/Subscribe stream must NOT inherit the
// default timeout: an earlier version applied it to every method, which
// killed each sync stream after 10 seconds. A method-specific entry wins
// over the default one, so Subscribe is listed explicitly with no timeout.
func serviceConfig(unaryTimeout time.Duration) string {
	return fmt.Sprintf(`{
  "loadBalancingConfig": [{"round_robin": {}}],
  "methodConfig": [
    {
      "name": [{}],
      "timeout": "%.3fs",
      "retryPolicy": {
        "maxAttempts": 4,
        "initialBackoff": "0.05s",
        "maxBackoff": "1s",
        "backoffMultiplier": 2,
        "retryableStatusCodes": ["UNAVAILABLE"]
      }
    },
    {
      "name": [{"service": "waypoint.events.v1.EventStreamService", "method": "Subscribe"}]
    }
  ]
}`, unaryTimeout.Seconds())
}

// DialOptions are the client options every connection uses; tests use them
// with an in-memory dialer so they exercise the production configuration.
func DialOptions(internalToken string, unaryTimeout time.Duration) []grpc.DialOption {
	if unaryTimeout <= 0 {
		unaryTimeout = 10 * time.Second
	}
	return []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(serviceConfig(unaryTimeout)),
		grpc.WithChainUnaryInterceptor(clientMetrics.UnaryClientInterceptor(), ClientUnaryInterceptor(internalToken)),
		grpc.WithChainStreamInterceptor(clientMetrics.StreamClientInterceptor(), ClientStreamInterceptor(internalToken)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 20 * time.Second, Timeout: 10 * time.Second, PermitWithoutStream: true}),
	}
}

// Dial creates a client connection that propagates the internal token,
// request id and principal on every call. Use "dns:///host:port" targets for
// client-side round-robin across replicas.
func Dial(target, internalToken string) (*grpc.ClientConn, error) {
	return grpc.NewClient(target, DialOptions(internalToken, EnvDuration("GRPC_CLIENT_TIMEOUT", 10*time.Second))...)
}

// ClientUnaryInterceptor attaches the internal contract metadata.
func ClientUnaryInterceptor(internalToken string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return inv(outgoing(ctx, internalToken), method, req, reply, cc, opts...)
	}
}

// ClientStreamInterceptor attaches the internal contract metadata.
func ClientStreamInterceptor(internalToken string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, st grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return st(outgoing(ctx, internalToken), desc, cc, method, opts...)
	}
}

// MustDial is Dial that panics on a malformed target.
func MustDial(target, internalToken string) *grpc.ClientConn {
	cc, err := Dial(target, internalToken)
	if err != nil {
		panic(err)
	}
	return cc
}

// ServiceContext returns ctx acting as the named internal service (used for
// background work such as event consumers and seeding).
func ServiceContext(ctx context.Context, name string) context.Context {
	return WithPrincipal(ctx, Principal{UserID: "svc:" + name, Role: RoleService})
}

// InternalError logs err and returns a generic Internal status, so storage
// details never leak to clients. Status errors pass through unchanged.
func InternalError(ctx context.Context, log *slog.Logger, msg string, err error) error {
	if _, ok := status.FromError(err); ok && status.Code(err) != codes.Unknown {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "request cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "deadline exceeded")
	}
	log.ErrorContext(ctx, msg, "error", err)
	return status.Error(codes.Internal, "internal error")
}
