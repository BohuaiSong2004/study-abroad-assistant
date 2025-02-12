// Package svc wires a backend service process: configuration, DynamoDB,
// the gRPC server and the admin endpoint.
package svc

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/events"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

// DevInternalToken is used when INTERNAL_TOKEN is unset outside production.
const DevInternalToken = "dev-internal-token-change-me" //nolint:gosec // documented development default; refused when ENVIRONMENT=production

// Base holds what every service needs.
type Base struct {
	Name   string
	Log    *slog.Logger
	DB     *ddb.Clients
	Prefix string
	Token  string
	GRPC   *platform.GRPCServer
	Owner  string // unique id of this process (leases)
}

// InternalToken returns INTERNAL_TOKEN, refusing the dev default when
// ENVIRONMENT=production.
func InternalToken(log *slog.Logger) string {
	t := platform.Env("INTERNAL_TOKEN", "")
	if t == "" {
		if platform.Env("ENVIRONMENT", "development") == "production" {
			log.Error("INTERNAL_TOKEN must be set in production")
			os.Exit(1)
		}
		log.Warn("INTERNAL_TOKEN not set; using the development token")
		t = DevInternalToken
	}
	return t
}

// Fatal reports a startup error and exits.
func Fatal(name string, err error) {
	_, _ = fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
	os.Exit(1)
}

// New connects to DynamoDB (creating tables when AUTO_CREATE_TABLES=true)
// and builds the gRPC server.
func New(name string, tables ...string) (*Base, error) {
	log := platform.NewLogger(name)
	cfg := ddb.ConfigFromEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := ddb.Connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	prefix := platform.Env("TABLE_PREFIX", "waypoint-")
	if platform.EnvBool("AUTO_CREATE_TABLES", cfg.Endpoint != "") {
		if err := retry(ctx, func() error { return ddb.EnsureTables(ctx, db.Raw, prefix, tables...) }); err != nil {
			return nil, fmt.Errorf("ensure tables: %w", err)
		}
	}
	host, _ := os.Hostname()
	token := InternalToken(log)
	log.Info("starting", "cache_mode", cfg.CacheMode, "dynamodb_endpoint", cfg.Endpoint, "table_prefix", prefix)
	return &Base{
		Name: name, Log: log, DB: db, Prefix: prefix, Token: token,
		GRPC:  platform.NewGRPCServer(log, token),
		Owner: fmt.Sprintf("%s-%s-%d", name, host, os.Getpid()),
	}, nil
}

// Table returns the full table name.
func (b *Base) Table(name string) string { return b.Prefix + name }

// Serve runs the gRPC and admin servers plus any background workers until
// SIGTERM.
func (b *Base) Serve(table string, workers ...func(context.Context) error) {
	grpcAddr := platform.Env("GRPC_ADDR", ":9090")
	adminAddr := platform.Env("ADMIN_ADDR", ":9100")
	admin := platform.AdminServer(adminAddr, func(ctx context.Context) error {
		return ddb.Ping(ctx, b.DB.Raw, b.Table(table))
	})
	fns := []func(context.Context) error{
		func(ctx context.Context) error { return b.GRPC.Serve(ctx, grpcAddr) },
		func(ctx context.Context) error { return platform.ServeHTTP(ctx, b.Log, admin) },
	}
	platform.Run(b.Log, append(fns, workers...)...)
}

// CacheWatcher returns a background worker that keeps this replica's local
// cache coherent with writes made by other replicas, or nil when the local
// cache is not in use (CACHE_MODE=off, or dax: the DAX cluster is shared).
func (b *Base) CacheWatcher(ob *events.Outbox, invalidate func(*ddb.CachedAPI, string) func(*eventsv1.Event)) func(context.Context) error {
	if b.DB.Local == nil {
		return func(ctx context.Context) error { <-ctx.Done(); return nil }
	}
	fn := invalidate(b.DB.Local, ob.Table())
	return func(ctx context.Context) error { return ob.Watch(ctx, events.ServerOptions{}, fn) }
}

// retry retries fn while DynamoDB is starting up (compose start order).
func retry(ctx context.Context, fn func() error) error {
	var err error
	for i := 0; i < 30; i++ {
		if err = fn(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Second):
		}
	}
	return err
}
