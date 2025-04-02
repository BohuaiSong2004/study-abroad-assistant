package applications

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ddb"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/ids"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/links"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/mail"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

// ResetTTL is how long a password-reset link works.
const ResetTTL = 30 * time.Minute

var (
	resetPerIP    = Limit{10, time.Hour} // requests from one address
	resetPerEmail = Limit{3, time.Hour}  // emails to one address (no mail bombing)
)

// SetMailer configures outgoing email and the public URL used in links.
func (s *AccountService) SetMailer(m mail.Sender, publicURL string) {
	s.mailer, s.publicURL = m, publicURL
}

func resetKey(tokenHash string) map[string]types.AttributeValue {
	return ddb.Key("RESET#"+tokenHash, "RESET")
}

// RequestPasswordReset emails a reset link when the email has an account.
// Everything that depends on whether the account exists (the lookup, the
// token write, the email) happens after the response, so neither the answer
// nor its timing reveals it.
func (s *AccountService) RequestPasswordReset(ctx context.Context, req *appsv1.RequestPasswordResetRequest) (*appsv1.RequestPasswordResetResponse, error) {
	email, err := NormalizeEmail(req.GetEmail())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "enter your email address")
	}
	if ip := platform.ClientIP(ctx); ip != "" {
		u, err := s.rl.hit(ctx, "reset-ip", ip, resetPerIP)
		if err != nil {
			return nil, platform.InternalError(ctx, s.log, "rate limit", err)
		}
		if u.atLeast(resetPerIP.N + 1) {
			return nil, limited("Too many reset requests from your network.", u.until(resetPerIP.N-1), u.now)
		}
	}
	if s.mailer == nil {
		return nil, status.Error(codes.Unimplemented, "password reset needs email, which is not configured")
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	go func() {
		defer cancel()
		if err := s.sendReset(bg, email); err != nil {
			s.log.WarnContext(bg, "password reset email failed", "error", err)
		}
	}()
	return &appsv1.RequestPasswordResetResponse{}, nil
}

func (s *AccountService) sendReset(ctx context.Context, email string) error {
	u, err := s.rl.hit(ctx, "reset-email", email, resetPerEmail)
	if err != nil {
		return err
	}
	if u.atLeast(resetPerEmail.N + 1) {
		s.log.WarnContext(ctx, "password reset emails limited for an address")
		return nil
	}
	g, err := s.api.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.table), Key: emailKey(email), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return err
	}
	if g.Item == nil {
		return nil // no account: nothing to send, nothing to reveal
	}
	uid := ddb.GetS(g.Item, "user_id")
	acct, err := s.getAccount(ctx, uid)
	if err != nil {
		return err
	}
	token := ids.Token()
	exp := s.now().Add(ResetTTL)
	item := resetKey(hashToken(token))
	item["user_id"] = ddb.S(uid)
	item["expires_ms"] = ddb.N(exp.UnixMilli())
	item[ddb.TTL] = ddb.N(exp.Add(time.Hour).Unix())
	if _, err := s.api.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(s.table), Item: item}); err != nil {
		return err
	}
	s.log.InfoContext(ctx, "password reset requested", "account_id", uid)
	return s.mailer.Send(ctx, mail.Message{
		To:      email,
		Subject: "Reset your Waypoint password",
		Text: fmt.Sprintf(`Hi %s,

Someone (hopefully you) asked to reset the password for your Waypoint account.
Open this link within 30 minutes to choose a new one:

%s

The link works once. Resetting signs you out everywhere. If you did not ask
for this, ignore this email; your password stays the same.

Waypoint
`, acct.DisplayName, links.PasswordReset(s.publicURL, token)),
	})
}

// ResetPassword sets a new password, consumes the token and bumps the
// account's session epoch, which invalidates every existing session.
func (s *AccountService) ResetPassword(ctx context.Context, req *appsv1.ResetPasswordRequest) (*appsv1.ResetPasswordResponse, error) {
	invalid := status.Error(codes.FailedPrecondition, "this reset link is invalid or has expired; request a new one")
	if len(req.GetToken()) < 20 || len(req.GetToken()) > 200 {
		return nil, invalid
	}
	if err := validatePassword(req.GetNewPassword()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	key := resetKey(hashToken(req.GetToken()))
	out, err := s.api.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.table), Key: key, ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, platform.InternalError(ctx, s.log, "load reset token", err)
	}
	if out.Item == nil || ddb.GetN(out.Item, "expires_ms") < s.now().UnixMilli() {
		return nil, invalid
	}
	uid := ddb.GetS(out.Item, "user_id")
	hash, err := s.hash.Hash(ctx, req.GetNewPassword())
	if err != nil {
		return nil, platform.InternalError(ctx, s.log, "hash password", err)
	}
	_, err = s.api.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{Delete: &types.Delete{TableName: aws.String(s.table), Key: key, ConditionExpression: aws.String("attribute_exists(pk)")}},
		{Update: &types.Update{
			TableName: aws.String(s.table), Key: userKey(uid),
			UpdateExpression:          aws.String("SET pw = :pw ADD sess_epoch :one"),
			ConditionExpression:       aws.String("attribute_exists(pk)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":pw": ddb.S(hash), ":one": ddb.N(1)},
		}},
	}})
	if ddb.IsConditionFailed(err) {
		return nil, invalid // used concurrently, or the account is gone
	}
	if err != nil {
		return nil, platform.InternalError(ctx, s.log, "reset password", err)
	}
	// Proving control of the mailbox lifts a lockout.
	if u, err := s.loadUser(ctx, uid); err == nil && s.limits.FailuresPerEmail.N > 0 {
		_ = s.rl.reset(ctx, "login-fail", ddb.GetS(u, "email"), s.limits.FailuresPerEmail)
	}
	s.log.InfoContext(ctx, "password reset; all sessions revoked", "account_id", uid)
	return &appsv1.ResetPasswordResponse{}, nil
}

// loadUser reads the profile item with strong consistency.
func (s *AccountService) loadUser(ctx context.Context, uid string) (map[string]types.AttributeValue, error) {
	out, err := s.api.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.table), Key: userKey(uid), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, status.Error(codes.NotFound, "account not found")
	}
	return out.Item, nil
}
