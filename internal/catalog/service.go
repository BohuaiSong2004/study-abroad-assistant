package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	catalogv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/catalog/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/civil"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ids"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

// Service implements CatalogService.
type Service struct {
	catalogv1.UnimplementedCatalogServiceServer
	store *Store
	log   *slog.Logger
	now   func() time.Time

	facetsMu  sync.Mutex
	facets    *catalogv1.ListFacetsResponse
	facetsExp time.Time
}

// NewService creates the service.
func NewService(store *Store, log *slog.Logger) *Service {
	return &Service{store: store, log: log, now: time.Now}
}

// GetProgram returns one program.
func (s *Service) GetProgram(ctx context.Context, req *catalogv1.GetProgramRequest) (*catalogv1.Program, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	p, err := s.store.Get(ctx, req.GetId(), false)
	if errors.Is(err, ErrNotFound) {
		return nil, status.Error(codes.NotFound, "program not found")
	}
	if err != nil {
		return nil, platform.InternalError(ctx, s.log, "get program", err)
	}
	return p, nil
}

// BatchGetPrograms returns the programs that exist among ids.
func (s *Service) BatchGetPrograms(ctx context.Context, req *catalogv1.BatchGetProgramsRequest) (*catalogv1.BatchGetProgramsResponse, error) {
	if len(req.GetIds()) > 200 {
		return nil, status.Error(codes.InvalidArgument, "at most 200 ids")
	}
	ps, err := s.store.BatchGet(ctx, req.GetIds())
	if err != nil {
		return nil, platform.InternalError(ctx, s.log, "batch get programs", err)
	}
	return &catalogv1.BatchGetProgramsResponse{Programs: ps}, nil
}

// ListPrograms searches the catalog.
func (s *Service) ListPrograms(ctx context.Context, req *catalogv1.ListProgramsRequest) (*catalogv1.ListProgramsResponse, error) {
	size := int(req.GetPageSize())
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	if utf8.RuneCountInString(req.GetQuery()) > 100 {
		return nil, status.Error(codes.InvalidArgument, "query too long")
	}
	ps, next, err := s.store.List(ctx, Filter{Query: req.GetQuery(), Country: req.GetCountry(), Field: req.GetField(), Degree: req.GetDegree()}, size, req.GetPageToken())
	if errors.Is(err, ErrBadPageToken) {
		return nil, status.Error(codes.InvalidArgument, "invalid page_token")
	}
	if err != nil {
		return nil, platform.InternalError(ctx, s.log, "list programs", err)
	}
	return &catalogv1.ListProgramsResponse{Programs: ps, NextPageToken: next}, nil
}

// ListFacets returns filter values; cached for 30 seconds.
func (s *Service) ListFacets(ctx context.Context, _ *catalogv1.ListFacetsRequest) (*catalogv1.ListFacetsResponse, error) {
	s.facetsMu.Lock()
	defer s.facetsMu.Unlock()
	if s.facets != nil && s.now().Before(s.facetsExp) {
		return s.facets, nil
	}
	cs, fs, err := s.store.Facets(ctx)
	if err != nil {
		return nil, platform.InternalError(ctx, s.log, "list facets", err)
	}
	sort.Strings(cs)
	sort.Strings(fs)
	s.facets = &catalogv1.ListFacetsResponse{Countries: cs, Fields: fs}
	s.facetsExp = s.now().Add(30 * time.Second)
	return s.facets, nil
}

func (s *Service) invalidateFacets() {
	s.facetsMu.Lock()
	s.facets = nil
	s.facetsMu.Unlock()
}

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,63}$`)

// CreateProgram adds a program (counselors and internal services only).
func (s *Service) CreateProgram(ctx context.Context, req *catalogv1.CreateProgramRequest) (*catalogv1.Program, error) {
	if _, err := platform.RequireRole(ctx, platform.RoleCounselor, platform.RoleService); err != nil {
		return nil, err
	}
	p := proto.Clone(req.GetProgram()).(*catalogv1.Program)
	if p == nil {
		return nil, status.Error(codes.InvalidArgument, "program is required")
	}
	if p.Id == "" {
		p.Id = ids.New("prg_")
	} else if !slugRE.MatchString(p.Id) {
		return nil, status.Error(codes.InvalidArgument, "id must be 3-64 lowercase letters, digits or dashes")
	}
	if err := Normalize(p); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	now := timestamppb.New(s.now())
	p.Version, p.CreatedAt, p.UpdatedAt = 1, now, now
	if err := s.store.Create(ctx, p); errors.Is(err, ErrExists) {
		return nil, status.Error(codes.AlreadyExists, "a program with this id already exists")
	} else if err != nil {
		return nil, platform.InternalError(ctx, s.log, "create program", err)
	}
	s.invalidateFacets()
	s.log.InfoContext(ctx, "program created", "program_id", p.Id)
	return p, nil
}

// UpdateProgram replaces a program's fields with optimistic concurrency.
func (s *Service) UpdateProgram(ctx context.Context, req *catalogv1.UpdateProgramRequest) (*catalogv1.Program, error) {
	if _, err := platform.RequireRole(ctx, platform.RoleCounselor, platform.RoleService); err != nil {
		return nil, err
	}
	in := req.GetProgram()
	if in.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "program.id is required")
	}
	cur, err := s.store.Get(ctx, in.GetId(), true)
	if errors.Is(err, ErrNotFound) {
		return nil, status.Error(codes.NotFound, "program not found")
	}
	if err != nil {
		return nil, platform.InternalError(ctx, s.log, "get program", err)
	}
	if req.GetExpectedVersion() != cur.Version {
		return nil, status.Errorf(codes.Aborted, "program was changed by someone else (now version %d); reload and retry", cur.Version)
	}
	p := proto.Clone(in).(*catalogv1.Program)
	if err := Normalize(p); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	p.Version = cur.Version + 1
	p.CreatedAt = cur.CreatedAt
	p.UpdatedAt = timestamppb.New(s.now())
	if err := s.store.Update(ctx, p, cur.Version); errors.Is(err, ErrVersionConflict) {
		return nil, status.Error(codes.Aborted, "program was changed by someone else; reload and retry")
	} else if err != nil {
		return nil, platform.InternalError(ctx, s.log, "update program", err)
	}
	s.invalidateFacets()
	s.log.InfoContext(ctx, "program updated", "program_id", p.Id, "version", p.Version)
	return p, nil
}

// Normalize trims and validates a program in place.
func Normalize(p *catalogv1.Program) error {
	trim := func(s *string, field string, maxLen int, required bool) error {
		*s = strings.Join(strings.Fields(*s), " ")
		if required && *s == "" {
			return fmt.Errorf("%s is required", field)
		}
		if utf8.RuneCountInString(*s) > maxLen {
			return fmt.Errorf("%s must be at most %d characters", field, maxLen)
		}
		return nil
	}
	for _, f := range []struct {
		v        *string
		name     string
		max      int
		required bool
	}{
		{&p.University, "university", 120, true},
		{&p.Name, "name", 120, true},
		{&p.Field, "field", 60, true},
		{&p.Country, "country", 60, true},
		{&p.City, "city", 60, false},
		{&p.Intake, "intake", 40, true},
		{&p.Website, "website", 300, false},
	} {
		if err := trim(f.v, f.name, f.max, f.required); err != nil {
			return err
		}
	}
	if _, ok := catalogv1.Degree_name[int32(p.Degree)]; !ok || p.Degree == catalogv1.Degree_DEGREE_UNSPECIFIED {
		return errors.New("degree is required")
	}
	if !civil.ValidZone(p.Timezone) {
		return fmt.Errorf("timezone %q is not a valid IANA time zone", p.Timezone)
	}
	if p.Website != "" {
		u, err := url.Parse(p.Website)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return errors.New("website must be an http(s) URL")
		}
	}
	if len(p.Deadlines) == 0 || len(p.Deadlines) > 6 {
		return errors.New("a program needs between 1 and 6 application rounds")
	}
	rounds := map[catalogv1.RoundKind]bool{}
	for _, d := range p.Deadlines {
		if _, ok := catalogv1.RoundKind_name[int32(d.Round)]; !ok || d.Round == catalogv1.RoundKind_ROUND_KIND_UNSPECIFIED {
			return errors.New("every deadline needs a round")
		}
		if rounds[d.Round] {
			return fmt.Errorf("round %s appears twice", d.Round)
		}
		rounds[d.Round] = true
		if _, err := civil.Parse(d.Date); err != nil {
			return err
		}
	}
	sort.Slice(p.Deadlines, func(i, j int) bool { return p.Deadlines[i].Date < p.Deadlines[j].Date })
	if p.Requirements == nil {
		p.Requirements = &catalogv1.Requirements{}
	}
	r := p.Requirements
	if r.MinToefl < 0 || r.MinToefl > 120 {
		return errors.New("min_toefl must be between 0 and 120")
	}
	if r.MinIeltsTenths < 0 || r.MinIeltsTenths > 90 || r.MinIeltsTenths%5 != 0 {
		return errors.New("min_ielts must be between 0 and 9.0 in steps of 0.5")
	}
	if r.RecommendationsRequired < 0 || r.RecommendationsRequired > 6 {
		return errors.New("recommendations_required must be between 0 and 6")
	}
	docs := map[catalogv1.DocumentType]bool{}
	var uniq []catalogv1.DocumentType
	for _, d := range r.Documents {
		if _, ok := catalogv1.DocumentType_name[int32(d)]; !ok || d == catalogv1.DocumentType_DOCUMENT_TYPE_UNSPECIFIED {
			return errors.New("unknown document type")
		}
		if !docs[d] {
			docs[d] = true
			uniq = append(uniq, d)
		}
	}
	// Test score reports follow from the score requirements.
	if (r.MinToefl > 0 || r.MinIeltsTenths > 0) && !docs[catalogv1.DocumentType_DOCUMENT_TYPE_ENGLISH_SCORES] {
		uniq = append(uniq, catalogv1.DocumentType_DOCUMENT_TYPE_ENGLISH_SCORES)
	}
	if r.GreRequired && !docs[catalogv1.DocumentType_DOCUMENT_TYPE_GRE_SCORES] {
		uniq = append(uniq, catalogv1.DocumentType_DOCUMENT_TYPE_GRE_SCORES)
	}
	sort.Slice(uniq, func(i, j int) bool { return uniq[i] < uniq[j] })
	r.Documents = uniq
	if p.ApplicationFeeCents < 0 || p.ApplicationFeeCents > 1_000_000_00 { // hundredths of the currency unit
		return errors.New("application fee is out of range")
	}
	p.Currency = strings.ToUpper(strings.TrimSpace(p.Currency))
	if p.ApplicationFeeCents > 0 && !regexp.MustCompile(`^[A-Z]{3}$`).MatchString(p.Currency) {
		return errors.New("currency must be a 3-letter ISO code")
	}
	if len(p.Tags) > 10 {
		return errors.New("at most 10 tags")
	}
	tags := p.Tags[:0]
	for _, t := range p.Tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if utf8.RuneCountInString(t) > 30 {
			return errors.New("tags must be at most 30 characters")
		}
		tags = append(tags, t)
	}
	p.Tags = tags
	return nil
}
