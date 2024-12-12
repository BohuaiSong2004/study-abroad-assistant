package platform

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"
)

// AdminServer exposes /metrics, /healthz (liveness) and /readyz (readiness).
func AdminServer(addr string, ready func(context.Context) error) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if ready != nil {
			if err := ready(ctx); err != nil {
				http.Error(w, "not ready: "+err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
		_, _ = w.Write([]byte("ready\n"))
	})
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
}

// ServeHTTP runs srv until ctx is done, then shuts it down gracefully.
func ServeHTTP(ctx context.Context, log *slog.Logger, srv *http.Server) error {
	errc := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}

// Run runs fns concurrently until SIGINT/SIGTERM or the first error.
func Run(log *slog.Logger, fns ...func(ctx context.Context) error) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	g, gctx := errgroup.WithContext(ctx)
	for _, fn := range fns {
		g.Go(func() error { return fn(gctx) })
	}
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("exiting", "error", err)
		stop()
		panicExit(1)
	}
	log.Info("shutdown complete")
}
