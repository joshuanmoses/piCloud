package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/joshuanmoses/piCloud/backend/internal/auth"
	"github.com/joshuanmoses/piCloud/backend/internal/sessions"
	"github.com/joshuanmoses/piCloud/backend/internal/users"
)

const (
	userKey    contextKey = "user"
	sessionKey contextKey = "session"
)

// UserFromContext returns the authenticated user, or nil when unauthenticated.
//
// A handler behind RequireAuth may rely on this being non-nil. A handler that
// is not must check.
func UserFromContext(ctx context.Context) *users.User {
	if ctx == nil {
		return nil
	}
	u, _ := ctx.Value(userKey).(*users.User)
	return u
}

// SessionFromContext returns the current session, or nil.
func SessionFromContext(ctx context.Context) *sessions.Session {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(sessionKey).(*sessions.Session)
	return s
}

// MustUser returns the authenticated user, panicking if there is none.
//
// Only for handlers registered behind RequireAuth. The panic is a loud
// programming error — a route mounted without its middleware — and is caught by
// Recoverer, so it becomes a 500 rather than a nil dereference that might
// otherwise be mistaken for an authorization bypass.
func MustUser(ctx context.Context) *users.User {
	u := UserFromContext(ctx)
	if u == nil {
		panic("api: MustUser called on an unauthenticated request; the route is missing RequireAuth")
	}
	return u
}

// Authenticate resolves the session cookie and attaches the user to the
// context. It does not reject unauthenticated requests — RequireAuth does that
// — so a route can serve both signed-in and anonymous callers.
func (s *Server) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := s.sessionTokenFromRequest(r)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}

		session, err := s.sessionStore.Validate(r.Context(), token)
		if err != nil {
			// Unknown, expired, revoked, or malformed: the cookie is stale, so
			// clear it. All four are treated identically in the response —
			// distinguishing them would leak whether a token was ever real.
			//
			// An infrastructure failure is different. A database hiccup must
			// not sign everybody out: the cookie is left alone so the session
			// resumes once the database recovers, and the request simply
			// proceeds unauthenticated.
			if errors.Is(err, sessions.ErrNotFound) || errors.Is(err, sessions.ErrInvalidToken) {
				s.clearAuthCookies(w)
			} else {
				s.logger.Error("could not validate a session", "error", err)
			}
			next.ServeHTTP(w, r)
			return
		}

		user, err := s.userStore.GetByID(r.Context(), session.UserID)
		if err != nil {
			if errors.Is(err, users.ErrNotFound) {
				// The account was deleted out from under a live session.
				s.clearAuthCookies(w)
			} else {
				s.logger.Error("could not load the session's user", "error", err)
			}
			next.ServeHTTP(w, r)
			return
		}

		// An account disabled after the session was created must lose access
		// immediately, without waiting for the session to expire.
		if !user.IsEnabled {
			if err := s.sessionStore.Revoke(r.Context(), session.ID, "account disabled"); err != nil {
				s.logger.Warn("could not revoke session for disabled account",
					"user_id", user.ID, "error", err)
			}
			s.clearAuthCookies(w)
			next.ServeHTTP(w, r)
			return
		}

		ctx := context.WithValue(r.Context(), userKey, user)
		ctx = context.WithValue(ctx, sessionKey, session)

		// Tag the request logger so every subsequent line is attributable.
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireAuth rejects unauthenticated requests.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserFromContext(r.Context()) == nil {
			WriteError(w, r, ErrUnauthorized("You must be signed in to do that."))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin rejects requests from non-administrators.
//
// This is a coarse gate for administration routes. It is not a substitute for
// per-object permission checks, which every content handler performs
// independently.
func (s *Server) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := UserFromContext(r.Context())
		if user == nil {
			WriteError(w, r, ErrUnauthorized(""))
			return
		}
		if !user.IsAdmin {
			// Logged as a warning: a non-administrator reaching an admin route
			// is either a bug or an attempt.
			s.logger.Warn("non-administrator attempted an administration route",
				"user_id", user.ID, "username", user.Username,
				"method", r.Method, "path", r.URL.Path,
				"ip", ClientIPFromContext(r.Context()))
			WriteError(w, r, ErrForbidden("This action requires administrator access."))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequirePasswordChange blocks normal API use while a forced password change is
// outstanding.
//
// Without it, an administrator's reset would be advisory: the user could ignore
// the prompt and keep using the credential the administrator just replaced.
func RequirePasswordChange(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := UserFromContext(r.Context())
		if user != nil && user.MustChangePassword {
			WriteError(w, r, &APIError{
				Status:  http.StatusForbidden,
				Code:    CodePasswordChangeRequired,
				Message: "You must choose a new password before continuing.",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CSRF rejects state-changing requests that do not carry a valid token.
func (s *Server) CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.RequiresCSRF(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		session := SessionFromContext(r.Context())
		if session == nil {
			// Nothing to forge against: an unauthenticated state-changing
			// request is either sign-in or setup, both of which have their own
			// protections.
			next.ServeHTTP(w, r)
			return
		}

		token := r.Header.Get(auth.CSRFHeaderName)
		if token == "" {
			// Fall back to a form field so a plain HTML form — used by the
			// public upload portal in Phase 6 — can carry the token.
			token = r.FormValue("csrf_token")
		}

		if err := s.csrf.Validate(session.ID, token); err != nil {
			s.logger.Warn("CSRF validation failed",
				"user_id", session.UserID, "session_id", session.ID,
				"method", r.Method, "path", r.URL.Path,
				"ip", ClientIPFromContext(r.Context()),
				"origin", r.Header.Get("Origin"),
				"token_present", token != "")
			WriteError(w, r, &APIError{
				Status:  http.StatusForbidden,
				Code:    CodeCSRFFailed,
				Message: "Your session could not be verified. Reload the page and try again.",
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// sessionTokenFromRequest extracts the session token from the cookie.
//
// Only the cookie is consulted. Accepting a token from an Authorization header
// or a query parameter would make CSRF protection meaningless and would put
// credentials in server logs and browser history.
func (s *Server) sessionTokenFromRequest(r *http.Request) string {
	cookie, err := r.Cookie(s.cfg.Session.CookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}
