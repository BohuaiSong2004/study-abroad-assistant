package applications

import (
	"context"
	"fmt"
	"time"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/links"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/mail"
)

// invitesPerStudent bounds invitation emails per student, so the feature
// cannot be used to send mail to arbitrary addresses at volume.
var invitesPerStudent = Limit{30, 24 * time.Hour}

// emailInvite sends the invitation and reports whether it went out. It never
// fails the request: the student always gets the link to share themselves.
// The raw token exists only in this request, which is why the email is sent
// here and not from an outbox-driven worker: the outbox is readable by other
// services, and the token must never be stored.
func (s *RecommendationService) emailInvite(ctx context.Context, studentID string, a *appsv1.Application, rec *appsv1.Recommendation, token string) bool {
	m := s.accounts.mailer
	if m == nil {
		return false
	}
	u, err := s.accounts.rl.hit(ctx, "invite-mail", studentID, invitesPerStudent)
	if err != nil {
		s.log.WarnContext(ctx, "invitation email limit check failed", "error", err)
		return false
	}
	if u.atLeast(invitesPerStudent.N + 1) {
		s.log.WarnContext(ctx, "invitation emails limited for a student")
		return false
	}
	acct, err := s.accounts.getAccount(ctx, studentID)
	if err != nil {
		s.log.WarnContext(ctx, "invitation email: load student", "error", err)
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err = m.Send(ctx, mail.Message{
		To:      rec.RecommenderEmail,
		Subject: fmt.Sprintf("Recommendation request from %s", acct.DisplayName),
		Text: fmt.Sprintf(`Dear %s,

%s has asked you for a letter of recommendation for their application to
%s, %s (%s).

Open this private link to read the request and submit or decline it:

%s

The link expires on %s. Please do not forward it: anyone with the link can
submit a letter.

This message was sent by Waypoint on %s's behalf.
`, rec.RecommenderName, acct.DisplayName, a.ProgramName, a.University, a.Intake,
			links.Invite(s.accounts.publicURL, token), rec.LinkExpiresAt.AsTime().UTC().Format("2 January 2006"), acct.DisplayName),
	})
	if err != nil {
		s.log.WarnContext(ctx, "invitation email failed; the student can share the link", "recommendation_id", rec.Id, "error", err)
		return false
	}
	s.log.InfoContext(ctx, "invitation emailed", "recommendation_id", rec.Id)
	return true
}
