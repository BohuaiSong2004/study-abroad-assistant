package applications

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ids"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

// SessionTTL is how long a session lasts.
const SessionTTL = 7 * 24 * time.Hour

// AccountService implements accounts and sessions.
//
// Items:
//
//	pk=USER#<id>        sk=PROFILE  email, name, role, password hash
//	pk=EMAIL#<email>    sk=EMAIL    user id (uniqueness guard)
//	pk=SESSION#<sha256> sk=SESSION  user id, expiry (DynamoDB TTL)
//	pk=RL#...           sk=W#...    rate-limit windows (see ratelimit.go)
type AccountService struct {
	appsv1.UnimplementedAccountServiceServer
	api    ddb.API
	table  string
	hash   *hasher
	log    *slog.Logger
	now    func() time.Time
	dummy  string
	limits Limits
	rl     *windows
}

// SetLimits replaces the abuse limits (DefaultLimits otherwise).
func (s *AccountService) SetLimits(l Limits) { s.limits = l }

// NewAccountService creates the service. hashConcurrency bounds parallel
// password hashing.
func NewAccountService(api ddb.API, table string, hashConcurrency int, log *slog.Logger) *AccountService {
	s := &AccountService{api: api, table: table, hash: newHasher(hashConcurrency), log: log, now: time.Now, limits: DefaultLimits()}
	s.rl = &windows{api: api, table: table, now: func() time.Time { return s.now() }}
	// Hash compared against when the email is unknown, so response time does
	// not reveal whether an account exists.
	s.dummy, _ = s.hash.Hash(context.Background(), ids.Token())
	return s
}

func userKey(id string) map[string]types.AttributeValue { return ddb.Key("USER#"+id, "PROFILE") }
func emailKey(e string) map[string]types.AttributeValue { return ddb.Key("EMAIL#"+e, "EMAIL") }
func sessionKey(token string) map[string]types.AttributeValue {
	sum := sha256.Sum256([]byte(token))
	return ddb.Key("SESSION#"+hex.EncodeToString(sum[:]), "SESSION")
}

// NormalizeEmail lower-cases and validates an address.
func NormalizeEmail(e string) (string, error) {
	e = strings.ToLower(strings.TrimSpace(e))
	a, err := mail.ParseAddress(e)
	if err != nil || a.Address != e || len(e) > 254 || !strings.Contains(e[strings.LastIndex(e, "@"):], ".") {
		return "", errors.New("enter a valid email address")
	}
	return e, nil
}

func validatePassword(p string) error {
	n := utf8.RuneCountInString(p)
	if n < 10 {
		return errors.New("password must be at least 10 characters")
	}
	if len(p) > 256 {
		return errors.New("password must be at most 256 bytes")
	}
	return nil
}

func accountFromItem(it map[string]types.AttributeValue) *appsv1.Account {
	return &appsv1.Account{
		Id:          strings.TrimPrefix(ddb.GetS(it, ddb.PK), "USER#"),
		Email:       ddb.GetS(it, "email"),
		DisplayName: ddb.GetS(it, "name"),
		Role:        appsv1.Role(ddb.GetN(it, "role")),
		CreatedAt:   timestamppb.New(time.UnixMilli(ddb.GetN(it, "created_ms"))),
	}
}

// RoleName maps the enum to the principal role string.
func RoleName(r appsv1.Role) string {
	if r == appsv1.Role_ROLE_COUNSELOR {
		return platform.RoleCounselor
	}
	return platform.RoleStudent
}

func (s *AccountService) create(ctx context.Context, email, password, name string, role appsv1.Role) (*appsv1.Account, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := validatePassword(password); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || utf8.RuneCountInString(name) > 80 {
		return nil, status.Error(codes.InvalidArgument, "name must be 1-80 characters")
	}
	hash, err := s.hash.Hash(ctx, password)
	if err != nil {
		return nil, platform.InternalError(ctx, s.log, "hash password", err)
	}
	id := ids.New("usr_")
	now := s.now()
	user := userKey(id)
	user["email"] = ddb.S(email)
	user["name"] = ddb.S(name)
	user["role"] = ddb.N(int64(role))
	user["pw"] = ddb.S(hash)
	user["created_ms"] = ddb.N(now.UnixMilli())
	guard := emailKey(email)
	guard["user_id"] = ddb.S(id)
	_, err = s.api.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{Put: &types.Put{TableName: aws.String(s.table), Item: guard, ConditionExpression: aws.String("attribute_not_exists(pk)")}},
		{Put: &types.Put{TableName: aws.String(s.table), Item: user, ConditionExpression: aws.String("attribute_not_exists(pk)")}},
	}})
	if ddb.IsConditionFailed(err) {
		return nil, status.Error(codes.AlreadyExists, "an account with this email already exists")
	}
	if err != nil {
		return nil, platform.InternalError(ctx, s.log, "create account", err)
	}
	return accountFromItem(user), nil
}

// Register creates a student account.
func (s *AccountService) Register(ctx context.Context, req *appsv1.RegisterRequest) (*appsv1.Account, error) {
	if ip := platform.ClientIP(ctx); ip != "" && s.limits.RegisterPerIP.N > 0 {
		u, err := s.rl.hit(ctx, "register-ip", ip, s.limits.RegisterPerIP)
		if err != nil {
			return nil, platform.InternalError(ctx, s.log, "rate limit", err)
		}
		if n := s.limits.RegisterPerIP.N; u.atLeast(n + 1) {
			s.log.WarnContext(ctx, "sign-up rate limit reached")
			return nil, limited("Too many accounts were created from your network.", u.until(n-1), u.now)
		}
	}
	a, err := s.create(ctx, req.GetEmail(), req.GetPassword(), req.GetDisplayName(), appsv1.Role_ROLE_STUDENT)
	if err == nil {
		s.log.InfoContext(ctx, "account registered", "account_id", a.Id)
	}
	return a, err
}

// EnsureAccount creates an account with the given role unless the email is
// already registered. Used to bootstrap the first counselor.
func (s *AccountService) EnsureAccount(ctx context.Context, email, password, name string, role appsv1.Role) error {
	_, err := s.create(ctx, email, password, name, role)
	if status.Code(err) == codes.AlreadyExists {
		return nil
	}
	return err
}

// Authenticate verifies credentials.
func (s *AccountService) Authenticate(ctx context.Context, req *appsv1.AuthenticateRequest) (*appsv1.Account, error) {
	bad := status.Error(codes.Unauthenticated, "email or password is incorrect")
	email, err := NormalizeEmail(req.GetEmail())
	if err != nil || len(req.GetPassword()) > 256 {
		return nil, bad
	}
	if err := s.checkLoginLimits(ctx, email); err != nil {
		return nil, err
	}
	g, err := s.api.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.table), Key: emailKey(email), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, platform.InternalError(ctx, s.log, "lookup email", err)
	}
	var user map[string]types.AttributeValue
	if g.Item != nil {
		u, err := s.api.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.table), Key: userKey(ddb.GetS(g.Item, "user_id")), ConsistentRead: aws.Bool(true)})
		if err != nil {
			return nil, platform.InternalError(ctx, s.log, "load user", err)
		}
		user = u.Item
	}
	encoded := s.dummy
	if user != nil {
		encoded = ddb.GetS(user, "pw")
	}
	ok, err := s.hash.Verify(ctx, encoded, req.GetPassword())
	if err != nil && !errors.Is(err, errBadHash) {
		return nil, platform.InternalError(ctx, s.log, "verify password", err)
	}
	if !ok || user == nil {
		s.loginFailed(ctx, email)
		return nil, bad
	}
	if s.limits.FailuresPerEmail.N > 0 {
		if err := s.rl.reset(ctx, "login-fail", email, s.limits.FailuresPerEmail); err != nil {
			s.log.WarnContext(ctx, "reset sign-in failures", "error", err)
		}
	}
	return accountFromItem(user), nil
}

// checkLoginLimits enforces the per-address attempt limit and the
// per-account lockout. The lockout is counted per email whether or not the
// account exists, so it reveals nothing about which emails are registered,
// and it is checked before the password, so a locked-out account cannot be
// probed further even with the right password.
func (s *AccountService) checkLoginLimits(ctx context.Context, email string) error {
	if ip := platform.ClientIP(ctx); ip != "" && s.limits.LoginPerIP.N > 0 {
		u, err := s.rl.hit(ctx, "login-ip", ip, s.limits.LoginPerIP)
		if err != nil {
			return platform.InternalError(ctx, s.log, "rate limit", err)
		}
		if n := s.limits.LoginPerIP.N; u.atLeast(n + 1) {
			s.log.WarnContext(ctx, "sign-in rate limit reached for an address")
			return limited("Too many sign-in attempts from your network.", u.until(n-1), u.now)
		}
	}
	if s.limits.FailuresPerEmail.N > 0 {
		u, err := s.rl.count(ctx, "login-fail", email, s.limits.FailuresPerEmail)
		if err != nil {
			return platform.InternalError(ctx, s.log, "rate limit", err)
		}
		if n := s.limits.FailuresPerEmail.N; u.atLeast(n) {
			return limited("Too many failed sign-ins for this account.", u.until(n-1), u.now)
		}
	}
	return nil
}

func (s *AccountService) loginFailed(ctx context.Context, email string) {
	if s.limits.FailuresPerEmail.N <= 0 {
		return
	}
	u, err := s.rl.hit(ctx, "login-fail", email, s.limits.FailuresPerEmail)
	if err != nil {
		s.log.WarnContext(ctx, "count sign-in failure", "error", err)
		return
	}
	if n := s.limits.FailuresPerEmail.N; u.atLeast(n) && !(usage{cur: u.cur - 1, prev: u.prev, start: u.start, window: u.window, now: u.now}).atLeast(n) {
		s.log.WarnContext(ctx, "account locked after repeated failed sign-ins")
	}
}

// CreateSession issues a session token for an account.
func (s *AccountService) CreateSession(ctx context.Context, req *appsv1.CreateSessionRequest) (*appsv1.CreateSessionResponse, error) {
	if req.GetAccountId() == "" {
		return nil, status.Error(codes.InvalidArgument, "account_id is required")
	}
	token := ids.Token()
	exp := s.now().Add(SessionTTL)
	item := sessionKey(token)
	item["user_id"] = ddb.S(req.GetAccountId())
	item["expires_ms"] = ddb.N(exp.UnixMilli())
	item[ddb.TTL] = ddb.N(exp.Unix())
	if _, err := s.api.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(s.table), Item: item}); err != nil {
		return nil, platform.InternalError(ctx, s.log, "create session", err)
	}
	return &appsv1.CreateSessionResponse{Token: token, ExpiresAt: timestamppb.New(exp)}, nil
}

// ResolveSession returns the account behind a token. Sessions are read with
// strong consistency (never from a cache) so that logout is effective
// immediately on every replica.
func (s *AccountService) ResolveSession(ctx context.Context, req *appsv1.ResolveSessionRequest) (*appsv1.ResolveSessionResponse, error) {
	unauth := status.Error(codes.Unauthenticated, "session expired; sign in again")
	if len(req.GetToken()) < 20 || len(req.GetToken()) > 200 {
		return nil, unauth
	}
	out, err := s.api.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.table), Key: sessionKey(req.GetToken()), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, platform.InternalError(ctx, s.log, "resolve session", err)
	}
	// DynamoDB TTL deletion lags by up to days, so check expiry explicitly.
	if out.Item == nil || ddb.GetN(out.Item, "expires_ms") < s.now().UnixMilli() {
		return nil, unauth
	}
	acct, err := s.getAccount(ctx, ddb.GetS(out.Item, "user_id"))
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, unauth
		}
		return nil, err
	}
	return &appsv1.ResolveSessionResponse{Account: acct, ExpiresAt: timestamppb.New(time.UnixMilli(ddb.GetN(out.Item, "expires_ms")))}, nil
}

// RevokeSession deletes a session (logout). Idempotent.
func (s *AccountService) RevokeSession(ctx context.Context, req *appsv1.RevokeSessionRequest) (*appsv1.RevokeSessionResponse, error) {
	if req.GetToken() == "" {
		return &appsv1.RevokeSessionResponse{}, nil
	}
	if _, err := s.api.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(s.table), Key: sessionKey(req.GetToken())}); err != nil {
		return nil, platform.InternalError(ctx, s.log, "revoke session", err)
	}
	return &appsv1.RevokeSessionResponse{}, nil
}

// GetAccount returns an account (self or internal services only).
func (s *AccountService) GetAccount(ctx context.Context, req *appsv1.GetAccountRequest) (*appsv1.Account, error) {
	p, err := platform.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	if p.Role != platform.RoleService && p.UserID != req.GetId() {
		return nil, status.Error(codes.PermissionDenied, "not your account")
	}
	return s.getAccount(ctx, req.GetId())
}

func (s *AccountService) getAccount(ctx context.Context, id string) (*appsv1.Account, error) {
	out, err := s.api.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.table), Key: userKey(id)})
	if err != nil {
		return nil, platform.InternalError(ctx, s.log, "get account", err)
	}
	if out.Item == nil {
		return nil, status.Error(codes.NotFound, "account not found")
	}
	return accountFromItem(out.Item), nil
}
