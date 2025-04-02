// Command applications runs AccountService, ApplicationService and
// RecommendationService.
package main

import (
	"context"
	"os"
	"time"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/applications"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/events"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/mail"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/svc"
)

func main() {
	platform.HealthcheckCommand()
	b, err := svc.New("applications", ddb.TableApplications)
	if err != nil {
		svc.Fatal("applications", err)
	}
	table := b.Table(ddb.TableApplications)
	cc := platform.MustDial(platform.Env("CATALOG_ADDR", "dns:///localhost:9091"), b.Token)
	catalogClient := catalogv1.NewCatalogServiceClient(cc)

	ob := events.NewOutbox(b.DB.API, table, "applications", b.Log)
	store := applications.NewStore(b.DB.API, table, ob)
	accounts := applications.NewAccountService(b.DB.API, table, platform.EnvInt("PASSWORD_HASH_CONCURRENCY", 4), b.Log)
	d := applications.DefaultLimits()
	accounts.SetLimits(applications.Limits{
		LoginPerIP:       applications.Limit{N: platform.EnvInt("LOGIN_LIMIT_PER_IP", d.LoginPerIP.N), Window: d.LoginPerIP.Window},
		FailuresPerEmail: applications.Limit{N: platform.EnvInt("LOGIN_FAILURES_PER_ACCOUNT", d.FailuresPerEmail.N), Window: d.FailuresPerEmail.Window},
		RegisterPerIP:    applications.Limit{N: platform.EnvInt("REGISTER_LIMIT_PER_IP", d.RegisterPerIP.N), Window: d.RegisterPerIP.Window},
	})
	mailer, err := mail.FromEnv(b.Log)
	if err != nil {
		svc.Fatal("applications", err)
	}
	accounts.SetMailer(mailer, platform.Env("PUBLIC_URL", "http://localhost:8080"))
	appsv1.RegisterAccountServiceServer(b.GRPC, accounts)
	appsv1.RegisterApplicationServiceServer(b.GRPC, applications.NewApplicationService(store, accounts, catalogClient, b.Log))
	appsv1.RegisterRecommendationServiceServer(b.GRPC, applications.NewRecommendationService(store, accounts, catalogClient, b.Log))
	eventsv1.RegisterEventStreamServiceServer(b.GRPC, events.NewServer(ob, b.Log, events.ServerOptions{}))

	// Bootstrap the first counselor account (catalog editor) if configured.
	if email := platform.Env("BOOTSTRAP_COUNSELOR_EMAIL", ""); email != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := accounts.EnsureAccount(ctx, email, platform.Env("BOOTSTRAP_COUNSELOR_PASSWORD", ""), platform.Env("BOOTSTRAP_COUNSELOR_NAME", "Counselor"), appsv1.Role_ROLE_COUNSELOR)
		cancel()
		if err != nil {
			b.Log.Error("bootstrap counselor failed", "error", err)
			os.Exit(1)
		}
		b.Log.Info("counselor account ready", "email", email)
	}
	b.Serve(ddb.TableApplications, b.CacheWatcher(ob, applications.InvalidateOn))
}
