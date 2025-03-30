package gateway

import (
	"context"
	"mime"
	"net/http"
	"net/url"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	appsv1 "github.com/BohuaiSong2004/study-abroad-assistant/gen/waypoint/applications/v1"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/applications"
	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

const sessionCookie = "wp_session"

// resolveSession attaches the principal for a valid session cookie. Results
// are cached for a few seconds per gateway to avoid a backend call on every
// request; logout through this gateway evicts immediately.
func (s *Server) resolveSession(ctx context.Context, r *http.Request) context.Context {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return ctx
	}
	if e, ok := s.sessions.Get(c.Value); ok {
		return platform.WithPrincipal(withAccount(ctx, e.account), e.principal)
	}
	cctx, cancel := context.WithTimeout(ctx, s.cfg.RequestTimeout)
	defer cancel()
	res, err := s.c.Accounts.ResolveSession(cctx, &appsv1.ResolveSessionRequest{Token: c.Value})
	if err != nil {
		if status.Code(err) != codes.Unauthenticated {
			s.log.WarnContext(ctx, "session lookup failed", "error", err)
		}
		return ctx
	}
	e := sessionEntry{
		principal: platform.Principal{UserID: res.Account.Id, Role: applications.RoleName(res.Account.Role)},
		account:   accountOut(res.Account),
	}
	s.sessions.Set(c.Value, e)
	return platform.WithPrincipal(withAccount(ctx, e.account), e.principal)
}

type accountKey struct{}

func withAccount(ctx context.Context, a accountDTO) context.Context {
	return context.WithValue(ctx, accountKey{}, a)
}

func accountFrom(ctx context.Context) (accountDTO, bool) {
	a, ok := ctx.Value(accountKey{}).(accountDTO)
	return a, ok
}

// getSession never returns 401: the UI calls it on every page load to decide
// what to render, and an error status there is noise in the console.
func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	a, ok := accountFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "account": a})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, exp time.Time) {
	// Secure follows PUBLIC_URL's scheme (on for https); plain-http local
	// development needs it off. HttpOnly and SameSite are always set.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // see above
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode, Expires: exp, MaxAge: int(time.Until(exp).Seconds()),
	})
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name,omitempty"`
}

// readCredentials accepts JSON (the jQuery UI) or a classic form post (the
// same forms before JavaScript has loaded, so a fast click never falls back
// to a GET with the password in the URL).
func readCredentials(w http.ResponseWriter, r *http.Request) (credentials, bool, bool) {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt == "application/x-www-form-urlencoded" {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if err := r.ParseForm(); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_form", "invalid form")
			return credentials{}, true, false
		}
		return credentials{Email: r.PostForm.Get("email"), Password: r.PostForm.Get("password"), Name: r.PostForm.Get("name")}, true, true
	}
	var c credentials
	ok := readJSON(w, r, &c)
	return c, false, ok
}

func homeFor(a *appsv1.Account) string {
	if a.Role == appsv1.Role_ROLE_COUNSELOR {
		return "/counselor.html"
	}
	return "/dashboard.html"
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, a *appsv1.Account, form bool) {
	ctx, cancel := s.backend(r)
	defer cancel()
	sess, err := s.c.Accounts.CreateSession(ctx, &appsv1.CreateSessionRequest{AccountId: a.Id})
	if err != nil {
		writeRPCError(w, r, s.log, err)
		return
	}
	s.setSessionCookie(w, sess.Token, sess.ExpiresAt.AsTime())
	if form {
		http.Redirect(w, r, homeFor(a), http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": accountOut(a), "redirect": homeFor(a)})
}

func (s *Server) formError(w http.ResponseWriter, r *http.Request, mode string, err error) {
	q := url.Values{"mode": {mode}, "error": {status.Convert(err).Message()}}
	http.Redirect(w, r, "/?"+q.Encode(), http.StatusSeeOther)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	c, form, ok := readCredentials(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.backend(r)
	defer cancel()
	a, err := s.c.Accounts.Register(ctx, &appsv1.RegisterRequest{Email: c.Email, Password: c.Password, DisplayName: c.Name})
	if err != nil {
		if form {
			s.formError(w, r, "register", err)
			return
		}
		writeRPCError(w, r, s.log, err)
		return
	}
	s.startSession(w, r, a, form)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	c, form, ok := readCredentials(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.backend(r)
	defer cancel()
	a, err := s.c.Accounts.Authenticate(ctx, &appsv1.AuthenticateRequest{Email: c.Email, Password: c.Password})
	if err != nil {
		if form {
			s.formError(w, r, "login", err)
			return
		}
		writeRPCError(w, r, s.log, err)
		return
	}
	s.startSession(w, r, a, form)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		s.sessions.Delete(c.Value)
		ctx, cancel := s.backend(r)
		defer cancel()
		if _, err := s.c.Accounts.RevokeSession(ctx, &appsv1.RevokeSessionRequest{Token: c.Value}); err != nil {
			writeRPCError(w, r, s.log, err)
			return
		}
	}
	// Expire the cookie; Secure follows PUBLIC_URL like when it was set.
	expired := &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1} //nolint:gosec // see above
	http.SetCookie(w, expired)
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt == "application/x-www-form-urlencoded" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
