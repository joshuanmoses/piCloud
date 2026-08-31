// Package sessions implements server-side authentication sessions.
//
// Sessions are stored records, not self-contained tokens. A JWT cannot be
// revoked before it expires; an administrator disabling a compromised account
// must be able to end its sessions immediately, and a user must be able to see
// their active devices and sign one of them out. That requires server state,
// and on a single-node file server the cost of a database lookup per request is
// negligible.
package sessions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joshuanmoses/piCloud/backend/internal/database"
)

// Errors returned by this package.
var (
	// ErrNotFound means no live session matches the token. It is returned for
	// every failure mode — unknown, expired, revoked — so a caller cannot
	// distinguish "never existed" from "expired".
	ErrNotFound = errors.New("sessions: no valid session")
	// ErrInvalidToken means the token was malformed.
	ErrInvalidToken = errors.New("sessions: malformed token")
)

// TokenBytes is the entropy of a session token. 256 bits makes guessing
// infeasible regardless of how many sessions exist.
const TokenBytes = 32

// MaxUserAgentLength bounds what is stored from a client-supplied header.
const MaxUserAgentLength = 512

// Session is a stored authentication session.
type Session struct {
	ID     uuid.UUID `json:"id"`
	UserID uuid.UUID `json:"user_id"`

	CreatedAt     time.Time  `json:"created_at"`
	LastSeenAt    time.Time  `json:"last_seen_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	IdleExpiresAt *time.Time `json:"-"`

	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	RevokedReason string     `json:"revoked_reason,omitempty"`

	Remember  bool   `json:"remember"`
	IPAddress string `json:"ip_address,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
}

// IsLive reports whether the session is currently usable.
func (s *Session) IsLive(now time.Time) bool {
	if s.RevokedAt != nil {
		return false
	}
	if !now.Before(s.ExpiresAt) {
		return false
	}
	if s.IdleExpiresAt != nil && !now.Before(*s.IdleExpiresAt) {
		return false
	}
	return true
}

// Token is a freshly minted session token.
//
// It exists only in memory, only long enough to be written into a Set-Cookie
// header. Only its hash reaches the database.
type Token string

// String hides the value so it cannot reach a log line through %v.
func (t Token) String() string { return "[REDACTED]" }

// Reveal returns the real token. Call this only when writing the cookie.
func (t Token) Reveal() string { return string(t) }

// generateToken produces a URL-safe random token.
func generateToken() (Token, error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		// Without secure randomness a session token would be predictable.
		// Failing the sign-in is the only safe response.
		return "", fmt.Errorf("sessions: cannot generate token: %w", err)
	}
	return Token(base64.RawURLEncoding.EncodeToString(b)), nil
}

// hashToken derives the stored form of a token.
//
// SHA-256 rather than Argon2: the input is 256 bits of uniform randomness, so
// there is no dictionary to attack and no benefit to a slow hash. Speed matters
// here because this runs on every authenticated request.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Config controls session lifetimes and limits.
type Config struct {
	// Lifetime is the absolute maximum age of an ordinary session.
	Lifetime time.Duration
	// RememberLifetime applies when the user asked to be remembered.
	RememberLifetime time.Duration
	// IdleTimeout expires an unused session early. Zero disables it.
	IdleTimeout time.Duration
	// MaxPerUser bounds concurrent sessions; the oldest are pruned beyond it.
	MaxPerUser int
}

// Store persists sessions.
type Store struct {
	db  *database.DB
	cfg Config
	now func() time.Time
}

// NewStore returns a Store backed by db.
func NewStore(db *database.DB, cfg Config) *Store {
	if cfg.MaxPerUser <= 0 {
		cfg.MaxPerUser = 25
	}
	return &Store{db: db, cfg: cfg, now: time.Now}
}

// SetClock overrides the time source. For tests only.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

const columns = `
	id, user_id, created_at, last_seen_at, expires_at, idle_expires_at,
	revoked_at, coalesce(revoked_reason, ''), remember,
	coalesce(host(ip_address), ''), user_agent`

type scanner interface{ Scan(dest ...any) error }

func scanSession(sc scanner) (*Session, error) {
	var s Session
	err := sc.Scan(
		&s.ID, &s.UserID, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.IdleExpiresAt,
		&s.RevokedAt, &s.RevokedReason, &s.Remember, &s.IPAddress, &s.UserAgent,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// CreateParams describes a new session.
type CreateParams struct {
	UserID    uuid.UUID
	Remember  bool
	IPAddress string
	UserAgent string
}

// Create mints a session and returns both the record and the raw token.
//
// The token is returned exactly once. It is never recoverable afterwards,
// because only its hash is stored.
func (s *Store) Create(ctx context.Context, p CreateParams) (*Session, Token, error) {
	token, err := generateToken()
	if err != nil {
		return nil, "", err
	}

	now := s.now()
	lifetime := s.cfg.Lifetime
	if p.Remember && s.cfg.RememberLifetime > lifetime {
		lifetime = s.cfg.RememberLifetime
	}
	expiresAt := now.Add(lifetime)

	var idleExpiresAt *time.Time
	if s.cfg.IdleTimeout > 0 {
		t := now.Add(s.cfg.IdleTimeout)
		idleExpiresAt = &t
	}

	var ip *string
	if addr, err := netip.ParseAddr(p.IPAddress); err == nil {
		str := addr.String()
		ip = &str
	}

	userAgent := p.UserAgent
	if len(userAgent) > MaxUserAgentLength {
		userAgent = userAgent[:MaxUserAgentLength]
	}

	var created *Session
	err = s.db.InTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO sessions (
				user_id, token_hash, expires_at, idle_expires_at,
				remember, ip_address, user_agent
			) VALUES ($1, $2, $3, $4, $5, $6::inet, $7)
			RETURNING `+columns,
			p.UserID, hashToken(token.Reveal()), expiresAt, idleExpiresAt,
			p.Remember, ip, userAgent)

		created, err = scanSession(row)
		if err != nil {
			return fmt.Errorf("sessions: create: %w", err)
		}

		// Keep the per-user session count bounded. Without this, a script that
		// signs in repeatedly grows the table without limit.
		_, err = tx.Exec(ctx, `
			UPDATE sessions SET revoked_at = now(), revoked_reason = 'session limit reached'
			WHERE id IN (
				SELECT id FROM sessions
				WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
				ORDER BY last_seen_at DESC
				OFFSET $2
			)`, p.UserID, s.cfg.MaxPerUser)
		if err != nil {
			return fmt.Errorf("sessions: prune: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}

	return created, token, nil
}

// Validate resolves a raw token to a live session and refreshes its activity
// timestamps.
//
// Every failure returns ErrNotFound. Distinguishing "unknown token" from
// "expired token" would tell an attacker probing tokens that they had found a
// real one.
func (s *Store) Validate(ctx context.Context, rawToken string) (*Session, error) {
	if err := validateTokenShape(rawToken); err != nil {
		return nil, err
	}

	now := s.now()

	// The UPDATE ... RETURNING does the lookup, the liveness test, and the
	// activity refresh in a single round trip — this runs on every
	// authenticated request, so a second query would be a real cost.
	var idleExtension any
	if s.cfg.IdleTimeout > 0 {
		idleExtension = s.cfg.IdleTimeout
	}

	row := s.db.Pool().QueryRow(ctx, `
		UPDATE sessions SET
			last_seen_at = $2,
			idle_expires_at = CASE
				WHEN $3::interval IS NULL THEN NULL
				ELSE $2::timestamptz + $3::interval
			END
		WHERE token_hash = $1
		  AND revoked_at IS NULL
		  AND expires_at > $2
		  AND (idle_expires_at IS NULL OR idle_expires_at > $2)
		RETURNING `+columns,
		hashToken(rawToken), now, idleExtension)

	sess, err := scanSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sessions: validate: %w", err)
	}
	return sess, nil
}

// validateTokenShape rejects obviously malformed tokens without a database
// round trip, so a flood of garbage cannot be turned into database load.
func validateTokenShape(token string) error {
	const encodedLength = 43 // base64url of 32 bytes, unpadded
	if len(token) != encodedLength {
		return ErrInvalidToken
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		ok := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_'
		if !ok {
			return ErrInvalidToken
		}
	}
	return nil
}

// Revoke ends one session.
func (s *Store) Revoke(ctx context.Context, id uuid.UUID, reason string) error {
	tag, err := s.db.Pool().Exec(ctx, `
		UPDATE sessions SET revoked_at = now(), revoked_reason = $2
		WHERE id = $1 AND revoked_at IS NULL`, id, reason)
	if err != nil {
		return fmt.Errorf("sessions: revoke: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeByToken ends the session a token belongs to. Used for sign-out, where
// the caller holds the token rather than the identifier.
func (s *Store) RevokeByToken(ctx context.Context, rawToken, reason string) error {
	if err := validateTokenShape(rawToken); err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx, `
		UPDATE sessions SET revoked_at = now(), revoked_reason = $2
		WHERE token_hash = $1 AND revoked_at IS NULL`,
		hashToken(rawToken), reason)
	if err != nil {
		return fmt.Errorf("sessions: revoke by token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeAllForUser ends every live session for a user, optionally sparing one.
//
// This is what makes a password change or an account suspension take effect
// immediately across every device.
func (s *Store) RevokeAllForUser(
	ctx context.Context, userID uuid.UUID, except *uuid.UUID, reason string,
) (int64, error) {
	tag, err := s.db.Pool().Exec(ctx, `
		UPDATE sessions SET revoked_at = now(), revoked_reason = $3
		WHERE user_id = $1
		  AND revoked_at IS NULL
		  AND ($2::uuid IS NULL OR id <> $2)`,
		userID, except, reason)
	if err != nil {
		return 0, fmt.Errorf("sessions: revoke all: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListForUser returns a user's live sessions, most recently active first.
func (s *Store) ListForUser(ctx context.Context, userID uuid.UUID) ([]*Session, error) {
	rows, err := s.db.Pool().Query(ctx, `
		SELECT `+columns+` FROM sessions
		WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
		ORDER BY last_seen_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("sessions: list: %w", err)
	}
	defer rows.Close()

	var out []*Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("sessions: scan: %w", err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// ListAll returns live sessions across every account, newest first.
//
// The administrator's view of who is signed in. Bounded, because on a busy
// instance this is one of the larger tables and an unbounded listing would be
// both a slow query and an unusable page.
//
// Revoked and expired sessions are excluded: "who is signed in right now" is
// the question this answers, and history belongs in the audit trail.
func (s *Store) ListAll(ctx context.Context, limit int) ([]*Session, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}

	rows, err := s.db.Pool().Query(ctx, `
		SELECT `+columns+` FROM sessions
		WHERE revoked_at IS NULL AND expires_at > now()
		ORDER BY last_seen_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("sessions: list all: %w", err)
	}
	defer rows.Close()

	out := make([]*Session, 0, limit)
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("sessions: scan: %w", err)
		}
		out = append(out, session)
	}
	return out, rows.Err()
}

// CountLive returns how many sessions are currently usable, for the
// administrator's overview.
func (s *Store) CountLive(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE revoked_at IS NULL AND expires_at > now()`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sessions: count live: %w", err)
	}
	return n, nil
}

// DeleteExpired removes sessions that ended more than retain ago. Called by the
// cleanup job; revoked sessions are kept briefly so the administrator's session
// list can still explain why one ended.
func (s *Store) DeleteExpired(ctx context.Context, retain time.Duration) (int64, error) {
	tag, err := s.db.Pool().Exec(ctx, `
		DELETE FROM sessions
		WHERE (expires_at < now() - $1::interval)
		   OR (revoked_at IS NOT NULL AND revoked_at < now() - $1::interval)`, retain)
	if err != nil {
		return 0, fmt.Errorf("sessions: delete expired: %w", err)
	}
	return tag.RowsAffected(), nil
}

// SameToken compares two tokens in constant time.
func SameToken(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
