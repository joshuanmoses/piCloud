package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/sessions"
	"github.com/joshuanmoses/piCloud/backend/internal/users"
)

// Errors returned by the sign-in flow.
//
// ErrInvalidCredentials is returned for an unknown username *and* for a wrong
// password. Distinguishing them would let anyone enumerate valid accounts.
var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrAccountDisabled    = errors.New("auth: account is disabled")
	ErrAccountLocked      = errors.New("auth: account is temporarily locked")
	ErrRateLimited        = errors.New("auth: too many attempts")
	ErrPasswordReused     = errors.New("auth: new password must differ from the current one")
)

// LockoutError carries when the account will unlock.
type LockoutError struct{ Until time.Time }

func (e *LockoutError) Error() string {
	return fmt.Sprintf("auth: account locked until %s", e.Until.Format(time.RFC3339))
}
func (e *LockoutError) Unwrap() error { return ErrAccountLocked }

// RateLimitError carries when the caller may retry.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("auth: rate limited, retry in %s", e.RetryAfter)
}
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// Config controls sign-in behaviour.
type Config struct {
	MaxLoginAttempts int
	LockoutDuration  time.Duration
	PasswordPolicy   PasswordPolicy
	// PerIPLimit and PerIPWindow throttle sign-in attempts from one address.
	PerIPLimit  int
	PerIPWindow time.Duration
}

// Service implements authentication.
type Service struct {
	users    *users.Store
	sessions *sessions.Store
	hasher   *Hasher
	cfg      Config
	logger   *slog.Logger

	// Two limiters with different keys. The per-IP limiter stops one source
	// hammering the server; the per-username limiter stops a distributed
	// attack spread across many addresses from targeting one account faster
	// than lockout can respond.
	ipLimiter       *RateLimiter
	usernameLimiter *RateLimiter

	now func() time.Time
}

// NewService wires the authentication service.
func NewService(
	userStore *users.Store,
	sessionStore *sessions.Store,
	hasher *Hasher,
	cfg Config,
	logger *slog.Logger,
) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.PerIPLimit < 1 {
		cfg.PerIPLimit = 20
	}
	if cfg.PerIPWindow <= 0 {
		cfg.PerIPWindow = 15 * time.Minute
	}
	if cfg.MaxLoginAttempts < 1 {
		cfg.MaxLoginAttempts = 10
	}
	if cfg.LockoutDuration <= 0 {
		cfg.LockoutDuration = 15 * time.Minute
	}

	return &Service{
		users:    userStore,
		sessions: sessionStore,
		hasher:   hasher,
		cfg:      cfg,
		logger:   logger,
		// The per-username limiter is deliberately more permissive than
		// lockout: it is a backstop, not the primary control.
		ipLimiter:       NewRateLimiter(cfg.PerIPLimit, cfg.PerIPWindow),
		usernameLimiter: NewRateLimiter(cfg.MaxLoginAttempts*3, cfg.PerIPWindow),
		now:             time.Now,
	}
}

// SetClock overrides the time source. For tests only.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Policy returns the password policy in force.
func (s *Service) Policy() PasswordPolicy { return s.cfg.PasswordPolicy }

// Hasher returns the password hasher.
func (s *Service) Hasher() *Hasher { return s.hasher }

// LoginParams describes a sign-in attempt.
type LoginParams struct {
	Username  string
	Password  string
	Remember  bool
	IPAddress string
	UserAgent string
}

// LoginResult is returned on success.
type LoginResult struct {
	User    *users.User
	Session *sessions.Session
	Token   sessions.Token
}

// Login authenticates and creates a session.
//
// The flow is deliberately uniform in cost and in failure message:
//
//   - An unknown username still performs a hash computation, so the response
//     time does not reveal which accounts exist.
//   - Unknown username and wrong password return the identical error.
//   - Disabled and locked accounts are distinguished only *after* the password
//     is verified, so the state of an account cannot be probed without knowing
//     its password.
func (s *Service) Login(ctx context.Context, p LoginParams) (*LoginResult, error) {
	username := users.NormalizeUsername(p.Username)

	if p.IPAddress != "" {
		if ok, retry := s.ipLimiter.Allow("login:" + p.IPAddress); !ok {
			s.logger.Warn("login rate limit exceeded", "ip", p.IPAddress)
			return nil, &RateLimitError{RetryAfter: retry}
		}
	}
	if username != "" {
		if ok, retry := s.usernameLimiter.Allow("login-user:" + username); !ok {
			s.logger.Warn("login rate limit exceeded for account", "username", username)
			return nil, &RateLimitError{RetryAfter: retry}
		}
	}

	if username == "" || p.Password == "" {
		BurnTime(p.Password)
		return nil, ErrInvalidCredentials
	}

	user, err := s.users.GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, users.ErrNotFound) {
			// Spend the same time as a real verification would.
			BurnTime(p.Password)
			s.logger.Info("login failed: unknown account",
				"username", username, "ip", p.IPAddress)
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("auth: lookup failed: %w", err)
	}

	now := s.now()

	// A locked account is checked before hashing: continuing would let an
	// attacker keep the CPU busy on an account they already cannot reach.
	if user.IsLocked(now) {
		s.logger.Info("login rejected: account locked",
			"user_id", user.ID, "username", user.Username, "ip", p.IPAddress)
		return nil, &LockoutError{Until: *user.LockedUntil}
	}

	if err := Verify(p.Password, user.PasswordHash); err != nil {
		if !errors.Is(err, ErrMismatch) {
			// A malformed stored hash is an integrity problem, not a user
			// error. Log it loudly; still tell the user only "invalid".
			s.logger.Error("stored password hash is unusable",
				"user_id", user.ID, "error", err)
		}
		s.recordFailure(ctx, user, p.IPAddress)
		return nil, ErrInvalidCredentials
	}

	// Password is correct. Only now does the account's state matter, so that
	// enablement cannot be probed without the password.
	if !user.IsEnabled {
		s.logger.Info("login rejected: account disabled",
			"user_id", user.ID, "username", user.Username, "ip", p.IPAddress)
		return nil, ErrAccountDisabled
	}

	// Successful sign-in is the only moment the plaintext exists, so it is the
	// only opportunity to upgrade a hash made with weaker parameters.
	if s.hasher.NeedsRehash(user.PasswordHash) {
		if newHash, err := s.hasher.Hash(p.Password); err == nil {
			if err := s.users.SetPassword(ctx, user.ID, newHash); err != nil {
				// Not fatal: the user is authenticated either way.
				s.logger.Warn("could not upgrade password hash", "user_id", user.ID, "error", err)
			} else {
				s.logger.Info("upgraded password hash to current parameters", "user_id", user.ID)
			}
		}
	}

	session, token, err := s.sessions.Create(ctx, sessions.CreateParams{
		UserID:    user.ID,
		Remember:  p.Remember,
		IPAddress: p.IPAddress,
		UserAgent: p.UserAgent,
	})
	if err != nil {
		return nil, fmt.Errorf("auth: cannot create session: %w", err)
	}

	if err := s.users.RecordSuccessfulLogin(ctx, user.ID, now); err != nil {
		s.logger.Warn("could not record login time", "user_id", user.ID, "error", err)
	}

	if p.IPAddress != "" {
		s.ipLimiter.Reset("login:" + p.IPAddress)
	}
	s.usernameLimiter.Reset("login-user:" + username)

	s.logger.Info("login succeeded",
		"user_id", user.ID, "username", user.Username,
		"session_id", session.ID, "ip", p.IPAddress, "remember", p.Remember)

	user.FailedLoginCount = 0
	user.LockedUntil = nil
	return &LoginResult{User: user, Session: session, Token: token}, nil
}

// recordFailure increments the failure counter and logs the outcome.
func (s *Service) recordFailure(ctx context.Context, user *users.User, ip string) {
	count, lockedUntil, err := s.users.RecordFailedLogin(
		ctx, user.ID, s.cfg.MaxLoginAttempts, s.cfg.LockoutDuration)
	if err != nil {
		s.logger.Error("could not record failed login", "user_id", user.ID, "error", err)
		return
	}

	attrs := []any{
		"user_id", user.ID, "username", user.Username,
		"ip", ip, "failed_attempts", count,
	}
	if lockedUntil != nil && lockedUntil.After(s.now()) {
		s.logger.Warn("account locked after repeated failures",
			append(attrs, "locked_until", lockedUntil.Format(time.RFC3339))...)
		// Every session is ended: repeated failures may mean the account is
		// under attack, and a session established earlier could be the
		// attacker's.
		if _, err := s.sessions.RevokeAllForUser(ctx, user.ID, nil, "account locked"); err != nil {
			s.logger.Error("could not revoke sessions for locked account",
				"user_id", user.ID, "error", err)
		}
	} else {
		s.logger.Info("login failed: wrong password", attrs...)
	}
}

// Logout ends the session belonging to a token.
func (s *Service) Logout(ctx context.Context, rawToken string) error {
	err := s.sessions.RevokeByToken(ctx, rawToken, "signed out")
	if err != nil && !errors.Is(err, sessions.ErrNotFound) && !errors.Is(err, sessions.ErrInvalidToken) {
		return fmt.Errorf("auth: logout: %w", err)
	}
	// An already-invalid token is not an error worth reporting: the caller
	// wanted to be signed out, and they are.
	return nil
}

// ChangePasswordParams describes a self-service password change.
type ChangePasswordParams struct {
	UserID uuid.UUID
	// CurrentPassword is required. Without it, anyone who borrows an
	// unlocked browser could lock the real owner out of their account.
	CurrentPassword string
	NewPassword     string
	// KeepSessionID stays signed in; every other session is ended.
	KeepSessionID *uuid.UUID
}

// ChangePassword updates a user's own password and revokes their other
// sessions.
func (s *Service) ChangePassword(ctx context.Context, p ChangePasswordParams) error {
	user, err := s.users.GetByID(ctx, p.UserID)
	if err != nil {
		return fmt.Errorf("auth: change password: %w", err)
	}

	if err := Verify(p.CurrentPassword, user.PasswordHash); err != nil {
		s.logger.Info("password change rejected: wrong current password", "user_id", user.ID)
		return ErrInvalidCredentials
	}

	if err := s.cfg.PasswordPolicy.Validate(
		p.NewPassword, user.Username, user.DisplayName, user.Email); err != nil {
		return err
	}
	if Verify(p.NewPassword, user.PasswordHash) == nil {
		return ErrPasswordReused
	}

	hash, err := s.hasher.Hash(p.NewPassword)
	if err != nil {
		return fmt.Errorf("auth: hash new password: %w", err)
	}
	if err := s.users.SetPassword(ctx, user.ID, hash); err != nil {
		return fmt.Errorf("auth: store new password: %w", err)
	}

	// Changing a password must end sessions elsewhere. If the reason for the
	// change is that someone else had access, leaving their session alive
	// defeats the point.
	revoked, err := s.sessions.RevokeAllForUser(ctx, user.ID, p.KeepSessionID, "password changed")
	if err != nil {
		s.logger.Error("could not revoke sessions after password change",
			"user_id", user.ID, "error", err)
	}

	s.logger.Info("password changed", "user_id", user.ID, "sessions_revoked", revoked)
	return nil
}

// AdminResetPassword sets a user's password without knowing the current one.
//
// Every session for that user is ended, and the account is unlocked.
func (s *Service) AdminResetPassword(
	ctx context.Context, actorID, targetID uuid.UUID, newPassword string, mustChange bool,
) error {
	target, err := s.users.GetByID(ctx, targetID)
	if err != nil {
		return fmt.Errorf("auth: admin reset: %w", err)
	}

	if err := s.cfg.PasswordPolicy.Validate(
		newPassword, target.Username, target.DisplayName, target.Email); err != nil {
		return err
	}

	hash, err := s.hasher.Hash(newPassword)
	if err != nil {
		return fmt.Errorf("auth: hash password: %w", err)
	}
	if err := s.users.SetPassword(ctx, targetID, hash); err != nil {
		return fmt.Errorf("auth: store password: %w", err)
	}

	if mustChange {
		flag := true
		if _, err := s.users.Update(ctx, targetID,
			users.UpdateParams{MustChangePassword: &flag}); err != nil {
			s.logger.Warn("could not set must-change-password flag",
				"user_id", targetID, "error", err)
		}
	}

	revoked, err := s.sessions.RevokeAllForUser(ctx, targetID, nil, "password reset by administrator")
	if err != nil {
		s.logger.Error("could not revoke sessions after reset", "user_id", targetID, "error", err)
	}

	s.logger.Warn("administrator reset a password",
		"actor_id", actorID, "target_id", targetID,
		"target_username", target.Username, "sessions_revoked", revoked)
	return nil
}

// Cleanup releases stale rate-limiter memory. Called periodically.
func (s *Service) Cleanup() {
	s.ipLimiter.Cleanup()
	s.usernameLimiter.Cleanup()
}
