// Command gateway serves the web UI and the public JSON API, and calls the
// backend services over gRPC.
package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/gateway"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/svc"
)

func main() {
	log := platform.NewLogger("gateway")
	cfg, err := gateway.ConfigFromEnv()
	if err != nil {
		log.Error("bad configuration", "error", err)
		os.Exit(1)
	}
	token := svc.InternalToken(log)
	catalog := platform.MustDial(platform.Env("CATALOG_ADDR", "dns:///localhost:9091"), token)
	apps := platform.MustDial(platform.Env("APPLICATIONS_ADDR", "dns:///localhost:9092"), token)
	tl := platform.MustDial(platform.Env("TIMELINE_ADDR", "dns:///localhost:9093"), token)

	srv, err := gateway.New(cfg, gateway.NewClients(catalog, apps, tl), log)
	if err != nil {
		log.Error("init", "error", err)
		os.Exit(1)
	}
	httpSrv := &http.Server{
		Addr:              platform.Env("HTTP_ADDR", ":8080"),
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: /api/events is a long-lived stream. Every other
		// handler bounds its own work with BACKEND_TIMEOUT.
		MaxHeaderBytes: 32 << 10,
	}
	admin := platform.AdminServer(platform.Env("ADMIN_ADDR", ":8081"), nil)
	log.Info("gateway configured", "public_url", cfg.PublicURL, "web_dir", cfg.WebDir)
	platform.Run(log,
		func(ctx context.Context) error { return platform.ServeHTTP(ctx, log, httpSrv) },
		func(ctx context.Context) error { return platform.ServeHTTP(ctx, log, admin) },
		srv.Hub().Run,
	)
}
