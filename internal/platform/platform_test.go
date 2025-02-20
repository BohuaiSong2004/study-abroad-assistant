package platform

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestEnvHelpers(t *testing.T) {
	t.Setenv("WP_STR", "  x ")
	t.Setenv("WP_INT", "42")
	t.Setenv("WP_BOOL", "true")
	t.Setenv("WP_DUR", "1500ms")
	t.Setenv("WP_BAD", "nope")
	if Env("WP_STR", "d") != "x" || Env("WP_MISSING", "d") != "d" {
		t.Fatal("Env")
	}
	if EnvInt("WP_INT", 1) != 42 || EnvInt("WP_BAD", 1) != 1 {
		t.Fatal("EnvInt")
	}
	if !EnvBool("WP_BOOL", false) || EnvBool("WP_BAD", false) {
		t.Fatal("EnvBool")
	}
	if EnvDuration("WP_DUR", 0) != 1500*time.Millisecond || EnvDuration("WP_BAD", time.Second) != time.Second {
		t.Fatal("EnvDuration")
	}
}

func TestInternalAuthContract(t *testing.T) {
	in := metadata.NewIncomingContext(context.Background(), metadata.Pairs(MDInternalToken, "secret", MDUserID, "usr_1", MDUserRole, RoleStudent, MDRequestID, "req_9"))
	ctx, err := principalFromIncoming(in, "secret")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := PrincipalFrom(ctx)
	if !ok || p.UserID != "usr_1" || p.Role != RoleStudent || RequestID(ctx) != "req_9" {
		t.Fatalf("principal %+v", p)
	}
	if _, err := principalFromIncoming(in, "other"); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("wrong token accepted: %v", err)
	}
	if _, err := RequireRole(ctx, RoleCounselor); status.Code(err) != codes.PermissionDenied {
		t.Fatal("role check")
	}
	if _, err := RequireUser(context.Background()); status.Code(err) != codes.Unauthenticated {
		t.Fatal("anonymous")
	}
	out := outgoing(ctx, "secret")
	md, _ := metadata.FromOutgoingContext(out)
	if md.Get(MDUserID)[0] != "usr_1" || md.Get(MDInternalToken)[0] != "secret" || md.Get(MDRequestID)[0] != "req_9" {
		t.Fatalf("outgoing %v", md)
	}
	svc, _ := PrincipalFrom(ServiceContext(context.Background(), "timeline"))
	if svc.Role != RoleService || svc.UserID != "svc:timeline" {
		t.Fatal("service context")
	}
}

func TestInternalErrorHidesDetails(t *testing.T) {
	log := NewLogger("test")
	if status.Code(InternalError(context.Background(), log, "x", errors.New("dynamodb: secret table"))) != codes.Internal {
		t.Fatal("generic error")
	}
	if s := status.Convert(InternalError(context.Background(), log, "x", errors.New("dynamodb: secret table"))); s.Message() != "internal error" {
		t.Fatal("details leaked")
	}
	if status.Code(InternalError(context.Background(), log, "x", status.Error(codes.NotFound, "nf"))) != codes.NotFound {
		t.Fatal("status errors pass through")
	}
	if status.Code(InternalError(context.Background(), log, "x", context.DeadlineExceeded)) != codes.DeadlineExceeded {
		t.Fatal("deadline")
	}
	if status.Code(InternalError(context.Background(), log, "x", context.Canceled)) != codes.Canceled {
		t.Fatal("cancel")
	}
}
