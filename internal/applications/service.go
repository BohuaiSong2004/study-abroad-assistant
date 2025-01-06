package applications

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/civil"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ids"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

// MaxApplications caps how many applications one student can hold.
const MaxApplications = 40

// ApplicationService implements ApplicationService.
type ApplicationService struct {
	appsv1.UnimplementedApplicationServiceServer
	store    *Store
	accounts *AccountService
	catalog  catalogv1.CatalogServiceClient
	log      *slog.Logger
	now      func() time.Time
}

// NewApplicationService creates the service.
func NewApplicationService(store *Store, accounts *AccountService, catalog catalogv1.CatalogServiceClient, log *slog.Logger) *ApplicationService {
	return &ApplicationService{store: store, accounts: accounts, catalog: catalog, log: log, now: time.Now}
}

func student(ctx context.Context) (platform.Principal, error) {
	return platform.RequireRole(ctx, platform.RoleStudent)
}

// storeErr maps store errors to gRPC status.
func (s *ApplicationService) storeErr(ctx context.Context, what string, err error) error {
	switch {
	case errors.Is(err, errNotFound):
		return status.Error(codes.NotFound, what+" not found")
	case errors.Is(err, errConflict):
		return status.Error(codes.Aborted, what+" was changed elsewhere; reload and try again")
	case errors.Is(err, errDuplicate):
		return status.Error(codes.AlreadyExists, what+" already exists")
	}
	return platform.InternalError(ctx, s.log, what, err)
}

// fetchProgram loads a program from the catalog as this service.
func fetchProgram(ctx context.Context, cc catalogv1.CatalogServiceClient, id string) (*catalogv1.Program, error) {
	p, err := cc.GetProgram(ctx, &catalogv1.GetProgramRequest{Id: id})
	if status.Code(err) == codes.NotFound {
		return nil, status.Error(codes.FailedPrecondition, "program not found in the catalog")
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "catalog unavailable: "+status.Convert(err).Message())
	}
	return p, nil
}

func hasRound(p *catalogv1.Program, r catalogv1.RoundKind) bool {
	for _, d := range p.Deadlines {
		if d.Round == r {
			return true
		}
	}
	return false
}

// defaultRound picks the earliest round whose deadline has not passed, or
// the last round when all have passed.
func defaultRound(p *catalogv1.Program, now time.Time) catalogv1.RoundKind {
	today := civil.Today(now, p.Timezone)
	for _, d := range p.Deadlines {
		if dd, err := civil.Parse(d.Date); err == nil && !dd.Before(today) {
			return d.Round
		}
	}
	return p.Deadlines[len(p.Deadlines)-1].Round
}

// CreateApplication starts an application to a program.
func (s *ApplicationService) CreateApplication(ctx context.Context, req *appsv1.CreateApplicationRequest) (*appsv1.Application, error) {
	me, err := student(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetProgramId() == "" {
		return nil, status.Error(codes.InvalidArgument, "program_id is required")
	}
	n, err := s.store.CountApps(ctx, me.UserID)
	if err != nil {
		return nil, s.storeErr(ctx, "applications", err)
	}
	if n >= MaxApplications {
		return nil, status.Errorf(codes.ResourceExhausted, "you can track at most %d applications", MaxApplications)
	}
	p, err := fetchProgram(ctx, s.catalog, req.GetProgramId())
	if err != nil {
		return nil, err
	}
	round := req.GetRound()
	if round == catalogv1.RoundKind_ROUND_KIND_UNSPECIFIED {
		round = defaultRound(p, s.now())
	} else if !hasRound(p, round) {
		return nil, status.Error(codes.InvalidArgument, "this program does not offer that round")
	}
	now := timestamppb.New(s.now())
	a := &appsv1.Application{
		Id: ids.New("app_"), StudentId: me.UserID, ProgramId: p.Id, ProgramName: p.Name,
		University: p.University, Intake: p.Intake, Round: round,
		Status: appsv1.ApplicationStatus_APPLICATION_STATUS_PLANNING, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	for _, d := range p.GetRequirements().GetDocuments() {
		a.Checklist = append(a.Checklist, &appsv1.ChecklistItem{Document: d})
	}
	if err := s.store.CreateApp(ctx, a); err != nil {
		if errors.Is(err, errDuplicate) {
			return nil, status.Error(codes.AlreadyExists, "you already have an application for this program")
		}
		return nil, s.storeErr(ctx, "application", err)
	}
	s.log.InfoContext(ctx, "application created", "application_id", a.Id, "program_id", p.Id)
	return a, nil
}

// GetApplication returns one of the caller's applications.
func (s *ApplicationService) GetApplication(ctx context.Context, req *appsv1.GetApplicationRequest) (*appsv1.Application, error) {
	me, err := student(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.store.GetApp(ctx, me.UserID, req.GetId(), false)
	if err != nil {
		return nil, s.storeErr(ctx, "application", err)
	}
	return a, nil
}

// ListApplications lists the caller's applications, newest first.
func (s *ApplicationService) ListApplications(ctx context.Context, req *appsv1.ListApplicationsRequest) (*appsv1.ListApplicationsResponse, error) {
	me, err := student(ctx)
	if err != nil {
		return nil, err
	}
	size := req.GetPageSize()
	if size <= 0 || size > 100 {
		size = 50
	}
	var start map[string]types.AttributeValue
	if t := req.GetPageToken(); t != "" {
		aid, err := base64.RawURLEncoding.DecodeString(t)
		if err != nil || !ids.Valid("app_", string(aid)) {
			return nil, status.Error(codes.InvalidArgument, "invalid page_token")
		}
		start = appKey(me.UserID, string(aid))
	}
	apps, last, err := s.store.ListApps(ctx, me.UserID, size, start)
	if err != nil {
		return nil, s.storeErr(ctx, "applications", err)
	}
	resp := &appsv1.ListApplicationsResponse{Applications: apps}
	if last != nil {
		resp.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(strings.TrimPrefix(ddb.GetS(last, ddb.SK), "APP#")))
	}
	return resp, nil
}

var validStatus = map[appsv1.ApplicationStatus]bool{
	appsv1.ApplicationStatus_APPLICATION_STATUS_PLANNING:    true,
	appsv1.ApplicationStatus_APPLICATION_STATUS_IN_PROGRESS: true,
	appsv1.ApplicationStatus_APPLICATION_STATUS_SUBMITTED:   true,
	appsv1.ApplicationStatus_APPLICATION_STATUS_INTERVIEW:   true,
	appsv1.ApplicationStatus_APPLICATION_STATUS_ADMITTED:    true,
	appsv1.ApplicationStatus_APPLICATION_STATUS_REJECTED:    true,
	appsv1.ApplicationStatus_APPLICATION_STATUS_WITHDRAWN:   true,
}

// UpdateApplication changes status, round, notes or checklist items.
func (s *ApplicationService) UpdateApplication(ctx context.Context, req *appsv1.UpdateApplicationRequest) (*appsv1.Application, error) {
	me, err := student(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetExpectedVersion() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "expected_version is required")
	}
	if req.Status != nil && !validStatus[req.GetStatus()] {
		return nil, status.Error(codes.InvalidArgument, "unknown status")
	}
	if req.Notes != nil && utf8.RuneCountInString(req.GetNotes()) > 5000 {
		return nil, status.Error(codes.InvalidArgument, "notes must be at most 5000 characters")
	}
	var program *catalogv1.Program
	if req.Round != nil {
		cur, err := s.store.GetApp(ctx, me.UserID, req.GetId(), false)
		if err != nil {
			return nil, s.storeErr(ctx, "application", err)
		}
		if program, err = fetchProgram(ctx, s.catalog, cur.ProgramId); err != nil {
			return nil, err
		}
		if !hasRound(program, req.GetRound()) {
			return nil, status.Error(codes.InvalidArgument, "this program does not offer that round")
		}
	}
	a, err := s.store.Mutate(ctx, me.UserID, req.GetId(), req.GetExpectedVersion(), func(a *appsv1.Application) ([]types.TransactWriteItem, error) {
		if req.Status != nil {
			a.Status = req.GetStatus()
			if a.Status == appsv1.ApplicationStatus_APPLICATION_STATUS_SUBMITTED && a.SubmittedAt == nil {
				a.SubmittedAt = timestamppb.New(s.now())
			}
		}
		if req.Round != nil {
			a.Round = req.GetRound()
		}
		if req.Notes != nil {
			a.Notes = strings.TrimSpace(req.GetNotes())
		}
		for _, u := range req.GetChecklist() {
			found := false
			for _, it := range a.Checklist {
				if it.Document == u.Document {
					it.Done, found = u.Done, true
				}
			}
			if !found {
				return nil, status.Errorf(codes.InvalidArgument, "%s is not on this application's checklist", u.Document)
			}
		}
		a.UpdatedAt = timestamppb.New(s.now())
		return nil, nil
	})
	if err != nil {
		if _, ok := status.FromError(err); ok && status.Code(err) != codes.Unknown {
			return nil, err
		}
		return nil, s.storeErr(ctx, "application", err)
	}
	return a, nil
}

// DeleteApplication removes an application and everything under it.
func (s *ApplicationService) DeleteApplication(ctx context.Context, req *appsv1.DeleteApplicationRequest) (*appsv1.DeleteApplicationResponse, error) {
	me, err := student(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.store.GetApp(ctx, me.UserID, req.GetId(), true)
	if err != nil {
		return nil, s.storeErr(ctx, "application", err)
	}
	recs, err := s.store.ListRecs(ctx, me.UserID, a.Id)
	if err != nil {
		return nil, s.storeErr(ctx, "recommendations", err)
	}
	if err := s.store.DeleteApp(ctx, a, recs); err != nil {
		return nil, s.storeErr(ctx, "application", err)
	}
	s.log.InfoContext(ctx, "application deleted", "application_id", a.Id)
	return &appsv1.DeleteApplicationResponse{}, nil
}

// GetRecommendationPacket assembles everything needed for the XML packet.
// Letters the student waived access to are returned without text.
func (s *ApplicationService) GetRecommendationPacket(ctx context.Context, req *appsv1.GetRecommendationPacketRequest) (*appsv1.RecommendationPacket, error) {
	me, err := student(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.store.GetApp(ctx, me.UserID, req.GetApplicationId(), false)
	if err != nil {
		return nil, s.storeErr(ctx, "application", err)
	}
	acct, err := s.accounts.getAccount(ctx, me.UserID)
	if err != nil {
		return nil, err
	}
	p, err := fetchProgram(ctx, s.catalog, a.ProgramId)
	if err != nil {
		return nil, err
	}
	recs, err := s.store.ListRecs(ctx, me.UserID, a.Id)
	if err != nil {
		return nil, s.storeErr(ctx, "recommendations", err)
	}
	out := &appsv1.RecommendationPacket{Application: a, Student: acct, Program: p, GeneratedAt: timestamppb.New(s.now())}
	for _, r := range recs {
		out.Recommendations = append(out.Recommendations, studentView(r.rec))
	}
	return out, nil
}
