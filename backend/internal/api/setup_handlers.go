package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/joshuanmoses/piCloud/backend/internal/audit"
	"github.com/joshuanmoses/piCloud/backend/internal/auth"
	"github.com/joshuanmoses/piCloud/backend/internal/users"
)

// First-run setup.
//
// PiFiles ships with no accounts and no default password, so the first
// administrator has to be created somehow. Two routes exist:
//
//   - `pifiles -create-admin`, for the installer and for scripted deployment.
//   - This endpoint, for someone who has just run `make up` and opened a
//     browser.
//
// The endpoint is available only while the instance has zero users. Once the
// first account exists it returns 404 permanently — there is no flag to reset
// and no way to re-open it short of emptying the users table.
//
// The window between first start and first sign-in is genuinely unauthenticated,
// which is why the server logs a prominent warning on every start until setup is
// finished, and why the installation guide says to complete setup before
// exposing the instance to a network.

type setupStatusResponse struct {
	// SetupRequired is true only while no account exists.
	SetupRequired bool `json:"setup_required"`
	// PasswordMinLength lets the form validate before submitting.
	PasswordMinLength int `json:"password_min_length"`
}

// handleSetupStatus reports whether the first administrator still needs
// creating. Unauthenticated, and safe: it reveals only whether the instance has
// been set up, which is immediately obvious from the sign-in page anyway.
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	required, err := s.setupRequired(r.Context())
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}
	WriteJSON(w, r, http.StatusOK, setupStatusResponse{
		SetupRequired:     required,
		PasswordMinLength: s.cfg.Security.PasswordMinLength,
	})
}

type setupRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Password    string `json:"password"`
}

// handleSetup creates the first administrator and signs them in.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	// Re-checked inside the transaction below; this is the fast path.
	required, err := s.setupRequired(r.Context())
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}
	if !required {
		// 404 rather than 403: once setup is done the route does not exist.
		WriteError(w, r, ErrNotFound())
		return
	}

	var req setupRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}

	created, apiErr := s.createFirstAdmin(r, req)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	// Sign the new administrator in immediately: making them retype the
	// password they just chose adds nothing.
	result, err := s.authService.Login(r.Context(), auth.LoginParams{
		Username:  created.Username,
		Password:  req.Password,
		IPAddress: ClientIPFromContext(r.Context()),
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		// The account exists, so this is not a failure the user should retry
		// setup for. Send them to the sign-in page.
		s.logger.Error("could not sign in the newly created administrator", "error", err)
		WriteJSON(w, r, http.StatusCreated, map[string]any{
			"user":            created,
			"sign_in_require": true,
		})
		return
	}

	s.setAuthCookies(w, result.Session, result.Token)

	s.logger.Warn("first administrator created; setup is now closed",
		"user_id", created.ID, "username", created.Username,
		"ip", ClientIPFromContext(r.Context()))

	// The first event in the trail, and the one that establishes who claimed
	// the instance. The setup window is unauthenticated by necessity, so this
	// is the only record of it.
	s.audit(r, audit.ActionSetup, audit.Success,
		auditActor(&created.ID, created.Username),
		auditTarget(created.ID, "user", created.Username))

	WriteJSON(w, r, http.StatusCreated, sessionResponse{
		User:      result.User,
		CSRFToken: s.csrf.Generate(result.Session.ID),
		ExpiresAt: result.Session.ExpiresAt,
	})
}

// createFirstAdmin validates and creates the account.
func (s *Server) createFirstAdmin(r *http.Request, req setupRequest) (*users.User, error) {
	policy := s.authService.Policy()
	if err := policy.Validate(req.Password, req.Username, req.DisplayName, req.Email); err != nil {
		var pwErr *auth.PasswordError
		if errors.As(err, &pwErr) {
			return nil, ErrValidation("Choose a stronger password.").
				WithField("password", pwErr.Reason)
		}
		return nil, ErrValidation("Choose a stronger password.")
	}

	hash, err := s.authService.Hasher().Hash(req.Password)
	if err != nil {
		return nil, ErrInternal(err)
	}

	created, err := s.userStore.Create(r.Context(), users.CreateParams{
		Username:     req.Username,
		DisplayName:  req.DisplayName,
		Email:        req.Email,
		PasswordHash: hash,
		IsAdmin:      true,
		IsEnabled:    true,
		// The first administrator has no quota. An administrator who cannot
		// store anything on their own instance would be a strange default.
		QuotaBytes: nil,
	})
	if err != nil {
		return nil, translateUserError(err)
	}
	return created, nil
}

// setupRequired reports whether the instance has no accounts yet.
func (s *Server) setupRequired(ctx context.Context) (bool, error) {
	count, err := s.userStore.Count(ctx)
	if err != nil {
		return false, err
	}
	return count == 0, nil
}

// translateUserError maps user-store errors to API responses.
func translateUserError(err error) error {
	var validationErr *users.ValidationError
	if errors.As(err, &validationErr) {
		return ErrValidation("Check the details you entered.").
			WithField(validationErr.Field, validationErr.Reason)
	}

	switch {
	case errors.Is(err, users.ErrUsernameTaken):
		return ErrConflict("That username is already taken.")
	case errors.Is(err, users.ErrEmailTaken):
		return ErrConflict("That email address is already in use.")
	case errors.Is(err, users.ErrLastAdmin):
		return ErrConflict("This is the only administrator. Make another account an administrator first.")
	case errors.Is(err, users.ErrNotFound):
		return ErrNotFound()
	default:
		return ErrInternal(err)
	}
}
