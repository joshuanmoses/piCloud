package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/audit"
	"github.com/joshuanmoses/piCloud/backend/internal/auth"
	"github.com/joshuanmoses/piCloud/backend/internal/sessions"
	"github.com/joshuanmoses/piCloud/backend/internal/users"
)

// setAuthCookies writes the session and CSRF cookies.
//
// The session cookie is HttpOnly, so a cross-site script cannot read the
// credential. The CSRF cookie deliberately is not: the frontend must read it to
// echo it back in a header, and it is useless without the session cookie.
func (s *Server) setAuthCookies(w http.ResponseWriter, session *sessions.Session, token sessions.Token) {
	secure := s.cfg.Session.CookieSecure
	domain := s.cfg.Session.CookieDomain

	// Expiry is set on the cookie only for a remembered session. A session
	// cookie with no Max-Age disappears when the browser closes, which is the
	// right default on a shared computer.
	var expires time.Time
	if session.Remember {
		expires = session.ExpiresAt
	}

	sessionCookie := &http.Cookie{
		Name:     s.cfg.Session.CookieName,
		Value:    token.Reveal(),
		Path:     "/",
		Domain:   domain,
		HttpOnly: true,
		Secure:   secure,
		// Lax rather than Strict: Strict would mean a user following a share
		// link from an email arrives signed out, which reads as broken. Lax
		// still blocks cross-site POSTs, and the CSRF token covers the rest.
		SameSite: http.SameSiteLaxMode,
	}
	if !expires.IsZero() {
		sessionCookie.Expires = expires
		sessionCookie.MaxAge = int(time.Until(expires).Seconds())
	}
	http.SetCookie(w, sessionCookie)

	csrfCookie := &http.Cookie{
		Name:     auth.CSRFCookieName,
		Value:    s.csrf.Generate(session.ID),
		Path:     "/",
		Domain:   domain,
		HttpOnly: false, // read by the frontend, by design
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	if !expires.IsZero() {
		csrfCookie.Expires = expires
		csrfCookie.MaxAge = int(time.Until(expires).Seconds())
	}
	http.SetCookie(w, csrfCookie)
}

// clearAuthCookies removes both cookies.
func (s *Server) clearAuthCookies(w http.ResponseWriter) {
	for _, name := range []string{s.cfg.Session.CookieName, auth.CSRFCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			Domain:   s.cfg.Session.CookieDomain,
			HttpOnly: name == s.cfg.Session.CookieName,
			Secure:   s.cfg.Session.CookieSecure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
			Expires:  time.Unix(0, 0),
		})
	}
}

// --- Request and response bodies -------------------------------------------

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Remember bool   `json:"remember"`
}

// sessionResponse describes the signed-in user to the browser.
type sessionResponse struct {
	User               *users.User `json:"user"`
	CSRFToken          string      `json:"csrf_token"`
	ExpiresAt          time.Time   `json:"expires_at"`
	MustChangePassword bool        `json:"must_change_password"`
}

// handleLogin authenticates and starts a session.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}

	// An already-signed-in caller starting a new session would leave the old one
	// orphaned, and is nearly always a stale tab. It is ended below, *after* the
	// new credentials check out.
	//
	// The ordering matters: revoking first meant a wrong password signed the
	// caller out of the session they already had, so a mistyped retry from a
	// stale tab logged them out of the working one. Session fixation is still
	// prevented — a new sign-in always issues a new identifier and kills the
	// old session — and now only a *successful* sign-in does so.
	existing := SessionFromContext(r.Context())

	result, err := s.authService.Login(r.Context(), auth.LoginParams{
		Username:  req.Username,
		Password:  req.Password,
		Remember:  req.Remember,
		IPAddress: ClientIPFromContext(r.Context()),
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		// Recorded with the username as the actor label and no identifier: the
		// caller has not authenticated, and the name may not belong to any
		// account. A run of these against one name is what a brute-force
		// attempt looks like in the trail.
		//
		// The password is not in the event, and there is no branch here that
		// could put it there.
		action := audit.ActionLoginFailed
		if errors.Is(err, auth.ErrAccountLocked) {
			action = audit.ActionLockout
		}
		s.audit(r, action, audit.Failure,
			auditActor(nil, strings.TrimSpace(req.Username)))

		WriteError(w, r, translateLoginError(err))
		return
	}

	if existing != nil && existing.ID != result.Session.ID {
		if err := s.sessionStore.Revoke(
			r.Context(), existing.ID, "replaced by new sign-in"); err != nil {
			s.logger.Debug("could not revoke prior session", "error", err)
		}
	}

	s.setAuthCookies(w, result.Session, result.Token)

	s.audit(r, audit.ActionLogin, audit.Success,
		auditActor(&result.User.ID, result.User.Username),
		auditTarget(result.User.ID, "user", result.User.Username),
		auditDetail(map[string]any{"remember": req.Remember}))

	WriteJSON(w, r, http.StatusOK, sessionResponse{
		User:               result.User,
		CSRFToken:          s.csrf.Generate(result.Session.ID),
		ExpiresAt:          result.Session.ExpiresAt,
		MustChangePassword: result.User.MustChangePassword,
	})
}

// translateLoginError maps domain errors to HTTP responses.
//
// Unknown username and wrong password both become the same 401 with the same
// message, so the response cannot be used to enumerate accounts.
func translateLoginError(err error) error {
	var lockout *auth.LockoutError
	if errors.As(err, &lockout) {
		return &APIError{
			Status: http.StatusTooManyRequests,
			Code:   CodeAccountLocked,
			Message: "This account is temporarily locked after too many failed sign-in attempts. " +
				"Try again later, or ask an administrator to unlock it.",
		}
	}

	var limited *auth.RateLimitError
	if errors.As(err, &limited) {
		return ErrRateLimited("Too many sign-in attempts. Please wait a few minutes and try again.")
	}

	switch {
	case errors.Is(err, auth.ErrAccountDisabled):
		return &APIError{
			Status:  http.StatusForbidden,
			Code:    CodeAccountDisabled,
			Message: "This account has been disabled. Contact an administrator.",
		}
	case errors.Is(err, auth.ErrInvalidCredentials):
		return ErrUnauthorized("Incorrect username or password.")
	default:
		return ErrInternal(err)
	}
}

// handleLogout ends the current session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := s.sessionTokenFromRequest(r)
	if token != "" {
		if err := s.authService.Logout(r.Context(), token); err != nil {
			s.logger.Warn("logout failed", "error", err)
		}
	}
	// Cookies are cleared regardless. A caller asking to sign out must end up
	// signed out even if the token was already invalid.
	s.clearAuthCookies(w)

	if user := UserFromContext(r.Context()); user != nil {
		s.logger.Info("logout", "user_id", user.ID, "username", user.Username)
		s.audit(r, audit.ActionLogout, audit.Success,
			auditTarget(user.ID, "user", user.Username))
	}
	WriteNoContent(w)
}

// handleSession reports who the caller is.
//
// Returns 401 when unauthenticated rather than an empty body, so the frontend
// has one unambiguous signal to show the sign-in page.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	session := SessionFromContext(r.Context())
	if user == nil || session == nil {
		WriteError(w, r, ErrUnauthorized("Not signed in."))
		return
	}

	WriteJSON(w, r, http.StatusOK, sessionResponse{
		User:               user,
		CSRFToken:          s.csrf.Generate(session.ID),
		ExpiresAt:          session.ExpiresAt,
		MustChangePassword: user.MustChangePassword,
	})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// handleChangePassword updates the caller's own password.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())
	session := SessionFromContext(r.Context())

	var req changePasswordRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}

	var keep *uuid.UUID
	if session != nil {
		keep = &session.ID
	}

	err := s.authService.ChangePassword(r.Context(), auth.ChangePasswordParams{
		UserID:          user.ID,
		CurrentPassword: req.CurrentPassword,
		NewPassword:     req.NewPassword,
		KeepSessionID:   keep,
	})
	if err != nil {
		// A wrong current password is recorded: repeated failures here mean
		// somebody with a stolen session is trying to take the account over.
		s.audit(r, audit.ActionPasswordChange, audit.Failure,
			auditTarget(user.ID, "user", user.Username))
		WriteError(w, r, translatePasswordError(err))
		return
	}

	s.audit(r, audit.ActionPasswordChange, audit.Success,
		auditTarget(user.ID, "user", user.Username),
		auditDetail(map[string]any{"other_sessions_kept": keep != nil}))

	WriteNoContent(w)
}

// translatePasswordError maps password-change failures to responses.
func translatePasswordError(err error) error {
	var policyErr *auth.PasswordError
	if errors.As(err, &policyErr) {
		return ErrValidation("The password does not meet the requirements.").
			WithField("new_password", policyErr.Reason)
	}

	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		return ErrValidation("Your current password is incorrect.").
			WithField("current_password", "Incorrect password.")
	case errors.Is(err, auth.ErrPasswordReused):
		return ErrValidation("Choose a password you have not used here before.").
			WithField("new_password", "This is your current password.")
	case errors.Is(err, users.ErrNotFound):
		return ErrNotFound()
	default:
		return ErrInternal(err)
	}
}

// handleListMySessions lists the caller's active sessions so they can spot a
// device they do not recognise.
func (s *Server) handleListMySessions(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())
	current := SessionFromContext(r.Context())

	list, err := s.sessionStore.ListForUser(r.Context(), user.ID)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	currentID := uuid.Nil
	if current != nil {
		currentID = current.ID
	}

	// The detail shape rather than the raw domain struct: it adds a readable
	// device label, which is how somebody recognises a session they do not
	// remember starting, and it omits the fields that only mean something to
	// the server.
	WriteJSON(w, r, http.StatusOK, map[string]any{
		"sessions": toSessionDetails(list, currentID),
	})
}

// handleRevokeMySession ends one of the caller's own sessions.
func (s *Server) handleRevokeMySession(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	// Ownership is verified before revoking. Without this check any signed-in
	// user could end any other user's session by guessing an identifier.
	owned, err := s.sessionStore.ListForUser(r.Context(), user.ID)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}
	found := false
	for _, sess := range owned {
		if sess.ID == id {
			found = true
			break
		}
	}
	if !found {
		// A uniform 404 rather than 403: confirming the session exists would
		// tell the caller they had guessed a real identifier.
		WriteError(w, r, ErrNotFound())
		return
	}

	if err := s.sessionStore.Revoke(r.Context(), id, "revoked by user"); err != nil {
		if errors.Is(err, sessions.ErrNotFound) {
			WriteError(w, r, ErrNotFound())
			return
		}
		WriteError(w, r, ErrInternal(err))
		return
	}

	current := SessionFromContext(r.Context())
	if current != nil && current.ID == id {
		s.clearAuthCookies(w)
	}

	s.logger.Info("session revoked by user", "user_id", user.ID, "session_id", id)
	WriteNoContent(w)
}
