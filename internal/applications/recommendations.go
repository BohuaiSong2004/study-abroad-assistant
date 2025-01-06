package applications

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/civil"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ids"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

// Limits on recommendations.
const (
	MaxActiveRecommendations = 6
	MinLetterChars           = 50
	MaxLetterChars           = 20000
)

// RecommendationService implements RecommendationService.
type RecommendationService struct {
	appsv1.UnimplementedRecommendationServiceServer
	store    *Store
	accounts *AccountService
	catalog  catalogv1.CatalogServiceClient
	log      *slog.Logger
	now      func() time.Time
}

// NewRecommendationService creates the service.
func NewRecommendationService(store *Store, accounts *AccountService, catalog catalogv1.CatalogServiceClient, log *slog.Logger) *RecommendationService {
	return &RecommendationService{store: store, accounts: accounts, catalog: catalog, log: log, now: time.Now}
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func active(st appsv1.RecommendationStatus) bool {
	return st == appsv1.RecommendationStatus_RECOMMENDATION_STATUS_REQUESTED || st == appsv1.RecommendationStatus_RECOMMENDATION_STATUS_SUBMITTED
}

// studentView hides letter text the student waived the right to read.
func studentView(r *appsv1.Recommendation) *appsv1.Recommendation {
	out := proto.Clone(r).(*appsv1.Recommendation)
	if out.WaivedAccess {
		out.LetterText = ""
	}
	return out
}

func summaries(a *appsv1.Application, r *appsv1.Recommendation) {
	for _, s := range a.Recommendations {
		if s.Id == r.Id {
			s.Status, s.RecommenderName = r.Status, r.RecommenderName
			return
		}
	}
	a.Recommendations = append(a.Recommendations, &appsv1.RecommendationSummary{Id: r.Id, RecommenderName: r.RecommenderName, Status: r.Status})
}

// deadlineFor returns the selected round's deadline, if known.
func deadlineFor(p *catalogv1.Program, round catalogv1.RoundKind) (civil.Date, bool) {
	for _, d := range p.GetDeadlines() {
		if d.Round == round {
			if dd, err := civil.Parse(d.Date); err == nil {
				return dd, true
			}
		}
	}
	return civil.Date{}, false
}

// linkExpiry: two weeks after the deadline, but at least a week from now.
func (s *RecommendationService) linkExpiry(ctx context.Context, a *appsv1.Application) time.Time {
	now := s.now()
	exp := now.Add(90 * 24 * time.Hour)
	if p, err := fetchProgram(ctx, s.catalog, a.ProgramId); err == nil {
		if d, ok := deadlineFor(p, a.Round); ok {
			t, _ := time.Parse("2006-01-02", d.AddDays(14).String())
			exp = t
		}
	}
	if floor := now.Add(7 * 24 * time.Hour); exp.Before(floor) {
		exp = floor
	}
	return exp
}

func (s *RecommendationService) mapErr(ctx context.Context, err error) error {
	if _, ok := status.FromError(err); ok && status.Code(err) != codes.Unknown {
		return err
	}
	switch {
	case errors.Is(err, errNotFound):
		return status.Error(codes.NotFound, "not found")
	case errors.Is(err, errConflict):
		return status.Error(codes.Aborted, "the application changed concurrently; try again")
	case errors.Is(err, errPrecondition):
		return status.Error(codes.FailedPrecondition, "this recommendation request is no longer open")
	}
	return platform.InternalError(ctx, s.log, "recommendation", err)
}

// RequestRecommendation invites a recommender. The raw token is returned once.
func (s *RecommendationService) RequestRecommendation(ctx context.Context, req *appsv1.RequestRecommendationRequest) (*appsv1.RequestRecommendationResponse, error) {
	me, err := student(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.Join(strings.Fields(req.GetRecommenderName()), " ")
	if name == "" || utf8.RuneCountInString(name) > 80 {
		return nil, status.Error(codes.InvalidArgument, "recommender name must be 1-80 characters")
	}
	email, err := NormalizeEmail(req.GetRecommenderEmail())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "enter the recommender's email address")
	}
	rel := strings.TrimSpace(req.GetRelationship())
	if utf8.RuneCountInString(rel) > 120 {
		return nil, status.Error(codes.InvalidArgument, "relationship must be at most 120 characters")
	}
	cur, err := s.store.GetApp(ctx, me.UserID, req.GetApplicationId(), false)
	if err != nil {
		return nil, s.mapErr(ctx, err)
	}
	expires := s.linkExpiry(ctx, cur)
	existing, err := s.store.ListRecs(ctx, me.UserID, cur.Id)
	if err != nil {
		return nil, s.mapErr(ctx, err)
	}
	nActive := 0
	for _, r := range existing {
		if active(r.rec.Status) {
			nActive++
			if r.rec.RecommenderEmail == email {
				return nil, status.Error(codes.AlreadyExists, "you already asked this person for a letter")
			}
		}
	}
	if nActive >= MaxActiveRecommendations {
		return nil, status.Errorf(codes.ResourceExhausted, "at most %d active recommendation requests", MaxActiveRecommendations)
	}
	token := ids.Token()
	rec := &appsv1.Recommendation{
		Id: ids.New("rec_"), ApplicationId: cur.Id, RecommenderName: name, RecommenderEmail: email,
		Relationship: rel, WaivedAccess: req.GetWaivedAccess(),
		Status:      appsv1.RecommendationStatus_RECOMMENDATION_STATUS_REQUESTED,
		RequestedAt: timestamppb.New(s.now()), LinkExpiresAt: timestamppb.New(expires),
	}
	_, err = s.store.Mutate(ctx, me.UserID, cur.Id, 0, func(a *appsv1.Application) ([]types.TransactWriteItem, error) {
		if a.Status == appsv1.ApplicationStatus_APPLICATION_STATUS_WITHDRAWN {
			return nil, status.Error(codes.FailedPrecondition, "this application was withdrawn")
		}
		summaries(a, rec)
		a.UpdatedAt = timestamppb.New(s.now())
		put, err := s.store.recPut(me.UserID, rec, hashToken(token), &types.Put{ConditionExpression: aws.String("attribute_not_exists(pk)")})
		if err != nil {
			return nil, err
		}
		return []types.TransactWriteItem{put, s.store.tokenPut(hashToken(token), me.UserID, cur.Id, rec.Id, expires.UnixMilli())}, nil
	})
	if err != nil {
		return nil, s.mapErr(ctx, err)
	}
	s.log.InfoContext(ctx, "recommendation requested", "application_id", cur.Id, "recommendation_id", rec.Id)
	return &appsv1.RequestRecommendationResponse{Recommendation: studentView(rec), Token: token}, nil
}

// ListRecommendations lists an application's recommendations.
func (s *RecommendationService) ListRecommendations(ctx context.Context, req *appsv1.ListRecommendationsRequest) (*appsv1.ListRecommendationsResponse, error) {
	me, err := student(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.GetApp(ctx, me.UserID, req.GetApplicationId(), false); err != nil {
		return nil, s.mapErr(ctx, err)
	}
	recs, err := s.store.ListRecs(ctx, me.UserID, req.GetApplicationId())
	if err != nil {
		return nil, s.mapErr(ctx, err)
	}
	out := &appsv1.ListRecommendationsResponse{}
	for _, r := range recs {
		out.Recommendations = append(out.Recommendations, studentView(r.rec))
	}
	return out, nil
}

// changeRec applies fn to a recommendation and the application summary in
// one transaction. The recommendation write is conditioned on its stored
// status still being REQUESTED (single submission / no double cancel).
func (s *RecommendationService) changeRec(ctx context.Context, sid, aid, rid string, fn func(r *storedRec) ([]types.TransactWriteItem, error)) (*storedRec, error) {
	var out *storedRec
	_, err := s.store.Mutate(ctx, sid, aid, 0, func(a *appsv1.Application) ([]types.TransactWriteItem, error) {
		r, err := s.store.GetRec(ctx, sid, aid, rid)
		if err != nil {
			return nil, err
		}
		if r.rec.Status != appsv1.RecommendationStatus_RECOMMENDATION_STATUS_REQUESTED {
			return nil, errPrecondition
		}
		extra, err := fn(r)
		if err != nil {
			return nil, err
		}
		put, err := s.store.recPut(sid, r.rec, r.tokenHash, &types.Put{
			ConditionExpression:       aws.String("#st = :requested"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":requested": ddb.N(int64(appsv1.RecommendationStatus_RECOMMENDATION_STATUS_REQUESTED))},
		})
		if err != nil {
			return nil, err
		}
		put.Put.ExpressionAttributeNames = map[string]string{"#st": "status"}
		summaries(a, r.rec)
		a.UpdatedAt = timestamppb.New(s.now())
		out = r
		return append([]types.TransactWriteItem{put}, extra...), nil
	})
	return out, err
}

// CancelRecommendation withdraws an open request and invalidates its link.
func (s *RecommendationService) CancelRecommendation(ctx context.Context, req *appsv1.CancelRecommendationRequest) (*appsv1.Recommendation, error) {
	me, err := student(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.changeRec(ctx, me.UserID, req.GetApplicationId(), req.GetRecommendationId(), func(r *storedRec) ([]types.TransactWriteItem, error) {
		r.rec.Status = appsv1.RecommendationStatus_RECOMMENDATION_STATUS_CANCELLED
		r.rec.RespondedAt = timestamppb.New(s.now())
		var extra []types.TransactWriteItem
		if r.tokenHash != "" {
			extra = append(extra, s.store.tokenDelete(r.tokenHash))
		}
		r.tokenHash = ""
		return extra, nil
	})
	if err != nil {
		return nil, s.mapErr(ctx, err)
	}
	return studentView(r.rec), nil
}

// RegenerateRecommendationLink replaces an open request's link (e.g. it was
// sent to the wrong address). The old link stops working.
func (s *RecommendationService) RegenerateRecommendationLink(ctx context.Context, req *appsv1.RegenerateRecommendationLinkRequest) (*appsv1.RequestRecommendationResponse, error) {
	me, err := student(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.store.GetApp(ctx, me.UserID, req.GetApplicationId(), false)
	if err != nil {
		return nil, s.mapErr(ctx, err)
	}
	expires := s.linkExpiry(ctx, a)
	token := ids.Token()
	r, err := s.changeRec(ctx, me.UserID, req.GetApplicationId(), req.GetRecommendationId(), func(r *storedRec) ([]types.TransactWriteItem, error) {
		var extra []types.TransactWriteItem
		if r.tokenHash != "" {
			extra = append(extra, s.store.tokenDelete(r.tokenHash))
		}
		r.tokenHash = hashToken(token)
		r.rec.LinkExpiresAt = timestamppb.New(expires)
		return append(extra, s.store.tokenPut(r.tokenHash, me.UserID, r.rec.ApplicationId, r.rec.Id, expires.UnixMilli())), nil
	})
	if err != nil {
		return nil, s.mapErr(ctx, err)
	}
	return &appsv1.RequestRecommendationResponse{Recommendation: studentView(r.rec), Token: token}, nil
}

// resolveInvite maps a token to its recommendation.
func (s *RecommendationService) resolveInvite(ctx context.Context, token string) (*tokenRef, *storedRec, error) {
	notFound := status.Error(codes.NotFound, "this recommendation link is invalid or was replaced")
	if len(token) < 20 || len(token) > 200 {
		return nil, nil, notFound
	}
	ref, err := s.store.GetToken(ctx, hashToken(token))
	if errors.Is(err, errNotFound) {
		return nil, nil, notFound
	}
	if err != nil {
		return nil, nil, platform.InternalError(ctx, s.log, "resolve token", err)
	}
	r, err := s.store.GetRec(ctx, ref.sid, ref.aid, ref.rid)
	if errors.Is(err, errNotFound) {
		return nil, nil, notFound
	}
	if err != nil {
		return nil, nil, platform.InternalError(ctx, s.log, "load recommendation", err)
	}
	return ref, r, nil
}

func (s *RecommendationService) invite(ctx context.Context, ref *tokenRef, r *storedRec) (*appsv1.RecommendationInvite, error) {
	a, err := s.store.GetApp(ctx, ref.sid, ref.aid, false)
	if err != nil {
		return nil, s.mapErr(ctx, err)
	}
	inv := &appsv1.RecommendationInvite{
		RecommendationId: r.rec.Id, RecommenderName: r.rec.RecommenderName,
		ProgramName: a.ProgramName, University: a.University, Intake: a.Intake,
		Status: r.rec.Status, LinkExpiresAt: timestamppb.New(time.UnixMilli(ref.expiresMs)),
	}
	if acct, err := s.accounts.getAccount(ctx, ref.sid); err == nil {
		inv.StudentName = acct.DisplayName
	}
	if p, err := fetchProgram(ctx, s.catalog, a.ProgramId); err == nil {
		if d, ok := deadlineFor(p, a.Round); ok {
			inv.Deadline, inv.Timezone = d.String(), p.Timezone
		}
	}
	return inv, nil
}

// GetRecommendationInvite shows the recommender what they are asked for.
func (s *RecommendationService) GetRecommendationInvite(ctx context.Context, req *appsv1.GetRecommendationInviteRequest) (*appsv1.RecommendationInvite, error) {
	ref, r, err := s.resolveInvite(ctx, req.GetToken())
	if err != nil {
		return nil, err
	}
	return s.invite(ctx, ref, r)
}

func (s *RecommendationService) respond(ctx context.Context, token string, fn func(r *appsv1.Recommendation)) (*appsv1.RecommendationInvite, error) {
	ref, r, err := s.resolveInvite(ctx, token)
	if err != nil {
		return nil, err
	}
	if ref.expiresMs < s.now().UnixMilli() {
		return nil, status.Error(codes.FailedPrecondition, "this link has expired; ask the student for a new one")
	}
	if r.rec.Status != appsv1.RecommendationStatus_RECOMMENDATION_STATUS_REQUESTED {
		return nil, status.Error(codes.FailedPrecondition, "you have already responded to this request")
	}
	// Recommenders act without an account; record the change as the service.
	sctx := platform.ServiceContext(ctx, "recommendations")
	updated, err := s.changeRec(sctx, ref.sid, ref.aid, ref.rid, func(sr *storedRec) ([]types.TransactWriteItem, error) {
		fn(sr.rec)
		sr.rec.RespondedAt = timestamppb.New(s.now())
		return nil, nil
	})
	if err != nil {
		if errors.Is(err, errPrecondition) {
			return nil, status.Error(codes.FailedPrecondition, "you have already responded to this request")
		}
		return nil, s.mapErr(ctx, err)
	}
	return s.invite(ctx, ref, updated)
}

// SubmitRecommendation stores the letter.
func (s *RecommendationService) SubmitRecommendation(ctx context.Context, req *appsv1.SubmitRecommendationRequest) (*appsv1.RecommendationInvite, error) {
	text := strings.TrimSpace(strings.ReplaceAll(req.GetLetterText(), "\r\n", "\n"))
	if n := utf8.RuneCountInString(text); n < MinLetterChars || n > MaxLetterChars {
		return nil, status.Errorf(codes.InvalidArgument, "the letter must be between %d and %d characters", MinLetterChars, MaxLetterChars)
	}
	if !utf8.ValidString(text) {
		return nil, status.Error(codes.InvalidArgument, "the letter must be valid UTF-8 text")
	}
	sum := sha256.Sum256([]byte(text))
	inv, err := s.respond(ctx, req.GetToken(), func(r *appsv1.Recommendation) {
		r.Status = appsv1.RecommendationStatus_RECOMMENDATION_STATUS_SUBMITTED
		r.LetterText = text
		r.LetterSha256 = hex.EncodeToString(sum[:])
	})
	if err == nil {
		s.log.InfoContext(ctx, "recommendation submitted", "recommendation_id", inv.RecommendationId)
	}
	return inv, err
}

// DeclineRecommendation records that the recommender declined.
func (s *RecommendationService) DeclineRecommendation(ctx context.Context, req *appsv1.DeclineRecommendationRequest) (*appsv1.RecommendationInvite, error) {
	reason := strings.TrimSpace(req.GetReason())
	if utf8.RuneCountInString(reason) > 500 {
		return nil, status.Error(codes.InvalidArgument, "reason must be at most 500 characters")
	}
	return s.respond(ctx, req.GetToken(), func(r *appsv1.Recommendation) {
		r.Status = appsv1.RecommendationStatus_RECOMMENDATION_STATUS_DECLINED
		r.DeclineReason = reason
	})
}
