package platform

import (
	"context"
	"crypto/subtle"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Metadata keys of the internal contract. The gateway authenticates the end
// user (session cookie) and forwards who they are; services accept these
// headers only from callers that present the shared internal token. In a
// production deployment the token would be replaced by mTLS/service mesh
// identity, but the services' authorization checks stay the same.
const (
	MDInternalToken = "x-waypoint-internal-token"
	MDUserID        = "x-waypoint-user-id"
	MDUserRole      = "x-waypoint-user-role"
	MDRequestID     = "x-request-id"
	// MDClientIP is the end user's address as the gateway determined it
	// (honouring X-Forwarded-For only from trusted proxies). Services use it
	// for shared rate limits; like the principal, it is only trusted from
	// callers holding the internal token.
	MDClientIP = "x-waypoint-client-ip"
)

// Roles carried in MDUserRole.
const (
	RoleStudent   = "student"
	RoleCounselor = "counselor"
	RoleService   = "service"
)

// Principal is the authenticated caller on whose behalf a request runs.
type Principal struct {
	UserID string
	Role   string
}

// WithPrincipal stores p in ctx (used by the gateway before calling services).
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// PrincipalFrom returns the principal in ctx.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok
}

// RequireUser returns the calling user or Unauthenticated.
func RequireUser(ctx context.Context) (Principal, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok || p.UserID == "" {
		return Principal{}, status.Error(codes.Unauthenticated, "sign in required")
	}
	return p, nil
}

// RequireRole returns the principal if it has one of the roles.
func RequireRole(ctx context.Context, roles ...string) (Principal, error) {
	p, err := RequireUser(ctx)
	if err != nil {
		return p, err
	}
	for _, r := range roles {
		if p.Role == r {
			return p, nil
		}
	}
	return p, status.Error(codes.PermissionDenied, "not allowed for your role")
}

type clientIPKey struct{}

// WithClientIP records the end user's address in ctx (gateway side).
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey{}, ip)
}

// ClientIP returns the end user's address, or "" for calls that did not
// originate from a browser request (service-to-service, tests).
func ClientIP(ctx context.Context) string {
	ip, _ := ctx.Value(clientIPKey{}).(string)
	return ip
}

// principalFromIncoming validates the internal token and extracts the
// principal from incoming gRPC metadata.
func principalFromIncoming(ctx context.Context, token string) (context.Context, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	got := first(md, MDInternalToken)
	if token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
		return ctx, status.Error(codes.Unauthenticated, "missing or invalid internal token")
	}
	if id := first(md, MDRequestID); id != "" {
		ctx = WithRequestID(ctx, id)
	}
	if uid := first(md, MDUserID); uid != "" {
		ctx = WithPrincipal(ctx, Principal{UserID: uid, Role: first(md, MDUserRole)})
	}
	if ip := first(md, MDClientIP); ip != "" {
		ctx = WithClientIP(ctx, ip)
	}
	return ctx, nil
}

// outgoing appends the internal token, request id and principal to ctx.
func outgoing(ctx context.Context, token string) context.Context {
	kv := []string{MDInternalToken, token}
	if id := RequestID(ctx); id != "" {
		kv = append(kv, MDRequestID, id)
	}
	if p, ok := PrincipalFrom(ctx); ok && p.UserID != "" {
		kv = append(kv, MDUserID, p.UserID, MDUserRole, p.Role)
	}
	if ip := ClientIP(ctx); ip != "" {
		kv = append(kv, MDClientIP, ip)
	}
	return metadata.AppendToOutgoingContext(ctx, kv...)
}

func first(md metadata.MD, k string) string {
	if v := md.Get(k); len(v) > 0 {
		return v[0]
	}
	return ""
}
