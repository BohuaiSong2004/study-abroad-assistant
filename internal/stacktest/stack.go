// Package stacktest runs the three backend services in-process (over
// bufconn, with the production interceptors) against DynamoDB Local, for
// integration tests of the services and the gateway.
package stacktest

import (
	"context"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	eventsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/events/v1"
	timelinev1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/timeline/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/applications"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/catalog"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/events"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/testutil"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/timeline"
)

// Stack is a running set of services.
type Stack struct {
	t      testing.TB
	DB     *ddb.Clients
	Prefix string

	CatalogConn, AppsConn, TimelineConn *grpc.ClientConn

	Catalog  catalogv1.CatalogServiceClient
	Accounts appsv1.AccountServiceClient
	Apps     appsv1.ApplicationServiceClient
	Recs     appsv1.RecommendationServiceClient
	Timeline timelinev1.TimelineServiceClient

	AccountService *applications.AccountService
	Syncer         *timeline.Syncer

	mu        sync.Mutex
	consumers []*events.Consumer
	stop      context.CancelFunc
	done      chan struct{}
}

// New starts the stack with the given cache mode and running consumers.
func New(t testing.TB, cacheMode string) *Stack {
	t.Helper()
	db, prefix := testutil.Tables(t, cacheMode)
	log := testutil.Logger(t)
	s := &Stack{t: t, DB: db, Prefix: prefix}

	catTable := prefix + ddb.TableCatalog
	catOB := events.NewOutbox(db.API, catTable, "catalog", log)
	s.CatalogConn = testutil.Server(t, func(g *grpc.Server) {
		catalogv1.RegisterCatalogServiceServer(g, catalog.NewService(catalog.NewStore(db.API, catTable, catOB), log))
		eventsv1.RegisterEventStreamServiceServer(g, events.NewServer(catOB, log, events.ServerOptions{PollInterval: 50 * time.Millisecond}))
	})
	s.Catalog = catalogv1.NewCatalogServiceClient(s.CatalogConn)

	appTable := prefix + ddb.TableApplications
	appOB := events.NewOutbox(db.API, appTable, "applications", log)
	store := applications.NewStore(db.API, appTable, appOB)
	s.AccountService = applications.NewAccountService(db.API, appTable, 4, log)
	s.AppsConn = testutil.Server(t, func(g *grpc.Server) {
		appsv1.RegisterAccountServiceServer(g, s.AccountService)
		appsv1.RegisterApplicationServiceServer(g, applications.NewApplicationService(store, s.AccountService, s.Catalog, log))
		appsv1.RegisterRecommendationServiceServer(g, applications.NewRecommendationService(store, s.AccountService, s.Catalog, log))
		eventsv1.RegisterEventStreamServiceServer(g, events.NewServer(appOB, log, events.ServerOptions{PollInterval: 50 * time.Millisecond}))
	})
	s.Accounts = appsv1.NewAccountServiceClient(s.AppsConn)
	s.Apps = appsv1.NewApplicationServiceClient(s.AppsConn)
	s.Recs = appsv1.NewRecommendationServiceClient(s.AppsConn)

	tlTable := prefix + ddb.TableTimeline
	tlOB := events.NewOutbox(db.API, tlTable, "timeline", log)
	tlStore := timeline.NewStore(db.API, db.Raw, tlTable, tlOB)
	s.Syncer = timeline.NewSyncer(tlStore, s.Catalog, log)
	s.Syncer.RecheckDelay = 300 * time.Millisecond
	t.Cleanup(s.Syncer.Close)
	mk := func(name, stream string, cc *grpc.ClientConn, h events.Handler) *events.Consumer {
		return events.NewConsumer(events.ConsumerConfig{
			Name: name, Stream: stream, Client: eventsv1.NewEventStreamServiceClient(cc),
			API: db.API, Table: tlTable, Handler: h, Log: log,
		})
	}
	s.consumers = []*events.Consumer{
		mk("timeline.catalog", "catalog", s.CatalogConn, s.Syncer.HandleCatalog),
		mk("timeline.applications", "applications", s.AppsConn, s.Syncer.HandleApplications),
	}
	s.TimelineConn = testutil.Server(t, func(g *grpc.Server) {
		timelinev1.RegisterTimelineServiceServer(g, timeline.NewService(tlStore, s.consumers, log))
		eventsv1.RegisterEventStreamServiceServer(g, events.NewServer(tlOB, log, events.ServerOptions{PollInterval: 50 * time.Millisecond}))
	})
	s.Timeline = timelinev1.NewTimelineServiceClient(s.TimelineConn)
	s.StartConsumers()
	t.Cleanup(s.StopConsumers)
	return s
}

// StartConsumers starts the timeline consumers.
func (s *Stack) StartConsumers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		return
	}
	ctx, cancel := context.WithCancel(platform.ServiceContext(context.Background(), "timeline"))
	s.stop, s.done = cancel, make(chan struct{})
	var wg sync.WaitGroup
	for _, c := range s.consumers {
		wg.Add(1)
		go func() { defer wg.Done(); _ = c.Run(ctx) }()
	}
	go func() { wg.Wait(); close(s.done) }()
}

// StopConsumers stops them and waits.
func (s *Stack) StopConsumers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop == nil {
		return
	}
	s.stop()
	<-s.done
	s.stop = nil
}

// Consumers returns the consumers (for status assertions).
func (s *Stack) Consumers() []*events.Consumer { return s.consumers }

// As returns ctx acting as the given user.
func As(id, role string) context.Context {
	return platform.WithPrincipal(context.Background(), platform.Principal{UserID: id, Role: role})
}

// Counselor is a context acting as a counselor.
func Counselor() context.Context { return As("usr_counselor", platform.RoleCounselor) }

// Student registers a student and returns a context acting as them.
func (s *Stack) Student(email, name string) (context.Context, *appsv1.Account) {
	s.t.Helper()
	a, err := s.Accounts.Register(context.Background(), &appsv1.RegisterRequest{Email: email, Password: "correct horse battery", DisplayName: name})
	if err != nil {
		s.t.Fatal(err)
	}
	return As(a.Id, platform.RoleStudent), a
}

// Program returns a valid program for tests.
func Program(id string, deadlines ...*catalogv1.Deadline) *catalogv1.Program {
	if len(deadlines) == 0 {
		deadlines = []*catalogv1.Deadline{
			{Round: catalogv1.RoundKind_ROUND_KIND_ROUND_1, Date: "2027-01-15"},
			{Round: catalogv1.RoundKind_ROUND_KIND_ROUND_2, Date: "2027-03-01"},
		}
	}
	return &catalogv1.Program{
		Id: id, University: "Northbridge University", Name: "MS in Computer Science",
		Degree: catalogv1.Degree_DEGREE_MASTER, Field: "Computer Science", Country: "United States",
		City: "Boston", Timezone: "America/New_York", Intake: "Fall 2027", Deadlines: deadlines,
		Requirements: &catalogv1.Requirements{
			MinToefl: 100, RecommendationsRequired: 2,
			Documents: []catalogv1.DocumentType{catalogv1.DocumentType_DOCUMENT_TYPE_STATEMENT_OF_PURPOSE, catalogv1.DocumentType_DOCUMENT_TYPE_CV, catalogv1.DocumentType_DOCUMENT_TYPE_TRANSCRIPT},
		},
		ApplicationFeeCents: 9000, Currency: "USD", Website: "https://example.edu/mscs",
	}
}

// WaitTimeline polls until the timeline exists and cond holds.
func (s *Stack) WaitTimeline(ctx context.Context, appID string, cond func(*timelinev1.Timeline) bool) *timelinev1.Timeline {
	s.t.Helper()
	var last *timelinev1.Timeline
	var lastErr error
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tl, err := s.Timeline.GetTimeline(ctx, &timelinev1.GetTimelineRequest{ApplicationId: appID})
		if err == nil {
			last = tl
			if cond == nil || cond(tl) {
				return tl
			}
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	s.t.Fatalf("timeline %s did not reach the expected state; last=%v err=%v", appID, last, lastErr)
	return nil
}
