// Package users owns application accounts: their persistence, their invariants,
// and their quota accounting.
//
// These accounts have no relationship to operating-system users. Nothing here
// touches /etc/passwd, PAM, or a shell.
package users

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// Errors returned by this package.
var (
	// ErrNotFound means no such user.
	ErrNotFound = errors.New("users: not found")
	// ErrUsernameTaken means the username is already in use.
	ErrUsernameTaken = errors.New("users: username is already taken")
	// ErrEmailTaken means the email address is already in use.
	ErrEmailTaken = errors.New("users: email address is already in use")
	// ErrLastAdmin means the operation would leave the instance with no
	// administrator, locking everyone out of administration permanently.
	ErrLastAdmin = errors.New("users: cannot remove the last administrator")
	// ErrQuotaExceeded means the operation would exceed the user's allowance.
	ErrQuotaExceeded = errors.New("users: storage quota exceeded")
)

// User is an application account.
//
// PasswordHash is deliberately unexported from JSON: this struct is returned
// directly by API handlers, and a forgotten tag would publish every hash.
type User struct {
	ID          uuid.UUID `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email,omitempty"`

	PasswordHash string `json:"-"`

	IsEnabled bool `json:"is_enabled"`
	IsAdmin   bool `json:"is_admin"`

	// QuotaBytes is nil for unlimited.
	QuotaBytes *int64 `json:"quota_bytes"`
	UsedBytes  int64  `json:"used_bytes"`

	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`

	FailedLoginCount int        `json:"-"`
	LockedUntil      *time.Time `json:"-"`

	MustChangePassword bool `json:"must_change_password"`
}

// IsLocked reports whether the account is currently locked out.
func (u *User) IsLocked(now time.Time) bool {
	return u.LockedUntil != nil && u.LockedUntil.After(now)
}

// CanSignIn reports whether the account may authenticate right now.
func (u *User) CanSignIn(now time.Time) bool {
	return u.IsEnabled && !u.IsLocked(now)
}

// HasUnlimitedQuota reports whether no storage limit applies.
func (u *User) HasUnlimitedQuota() bool { return u.QuotaBytes == nil }

// RemainingQuota returns the bytes still available, or -1 when unlimited.
func (u *User) RemainingQuota() int64 {
	if u.QuotaBytes == nil {
		return -1
	}
	remaining := *u.QuotaBytes - u.UsedBytes
	if remaining < 0 {
		return 0
	}
	return remaining
}

// QuotaPercent returns usage as a percentage, or 0 when unlimited.
func (u *User) QuotaPercent() float64 {
	if u.QuotaBytes == nil || *u.QuotaBytes == 0 {
		return 0
	}
	pct := float64(u.UsedBytes) / float64(*u.QuotaBytes) * 100
	if pct > 100 {
		return 100
	}
	return pct
}

// CanStore reports whether size additional bytes fit within the quota.
func (u *User) CanStore(size int64) bool {
	if u.QuotaBytes == nil {
		return true
	}
	return u.UsedBytes+size <= *u.QuotaBytes
}

// Public is the representation safe to return to any authenticated user, as
// opposed to the full record which only administrators and the account owner
// see. It omits quota, enablement, and timestamps.
type Public struct {
	ID          uuid.UUID `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
}

// Public projects a user down to the fields other users may see.
func (u *User) Public() Public {
	return Public{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName}
}

// --- Validation -----------------------------------------------------------

// ValidationError describes a rejected field. The message is written to be
// shown to the user directly.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Reason }

// Username limits, matching the database CHECK constraint.
const (
	MinUsernameLength = 2
	MaxUsernameLength = 64
	MaxDisplayNameLen = 128
	MaxEmailLength    = 254
)

// NormalizeUsername trims surrounding whitespace. Case is preserved for
// display; the database column is citext, so uniqueness and lookup are
// case-insensitive without lowering it here.
func NormalizeUsername(s string) string { return strings.TrimSpace(s) }

// ValidateUsername enforces the same rules as the database constraint, so a
// bad value produces a clear message rather than a constraint violation.
//
// The permitted set is deliberately narrow. A username appears in audit logs,
// share dialogs, and URLs; allowing arbitrary Unicode would invite homoglyph
// impersonation, where "аdmin" with a Cyrillic 'а' is visually identical to
// the real administrator.
func ValidateUsername(username string) error {
	n := len([]rune(username))
	switch {
	case n == 0:
		return &ValidationError{"username", "Enter a username."}
	case n < MinUsernameLength:
		return &ValidationError{"username", fmt.Sprintf("Username must be at least %d characters.", MinUsernameLength)}
	case n > MaxUsernameLength:
		return &ValidationError{"username", fmt.Sprintf("Username must be no more than %d characters.", MaxUsernameLength)}
	}

	for i, r := range username {
		if r > unicode.MaxASCII {
			return &ValidationError{"username", "Username may contain only letters, numbers, dots, hyphens, and underscores."}
		}
		isAlnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if i == 0 && !isAlnum {
			return &ValidationError{"username", "Username must begin with a letter or a number."}
		}
		if !isAlnum && r != '.' && r != '_' && r != '-' {
			return &ValidationError{"username", "Username may contain only letters, numbers, dots, hyphens, and underscores."}
		}
	}

	// Names that would collide with routes or be confusing in a share dialog.
	switch strings.ToLower(username) {
	case "admin", "administrator", "root", "system", "api", "public", "anonymous", "guest", "me", "self", "everyone", "nobody":
		return &ValidationError{"username", fmt.Sprintf("%q is reserved. Choose a different username.", username)}
	}
	return nil
}

// ValidateDisplayName checks the human-readable name.
func ValidateDisplayName(name string) error {
	trimmed := strings.TrimSpace(name)
	n := len([]rune(trimmed))
	switch {
	case n == 0:
		return &ValidationError{"display_name", "Enter a display name."}
	case n > MaxDisplayNameLen:
		return &ValidationError{"display_name", fmt.Sprintf("Display name must be no more than %d characters.", MaxDisplayNameLen)}
	}
	// Control characters would corrupt log lines and could be used to spoof
	// interface text.
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return &ValidationError{"display_name", "Display name must not contain control characters."}
		}
	}
	return nil
}

// ValidateEmail performs a deliberately permissive check. Exhaustive RFC 5322
// validation rejects addresses that genuinely work; the only authoritative test
// is sending mail.
func ValidateEmail(email string) error {
	if email == "" {
		return nil // optional
	}
	if len(email) > MaxEmailLength {
		return &ValidationError{"email", fmt.Sprintf("Email address must be no more than %d characters.", MaxEmailLength)}
	}
	local, domain, found := strings.Cut(email, "@")
	if !found || local == "" || domain == "" ||
		strings.ContainsAny(email, " \t\r\n") ||
		!strings.Contains(domain, ".") ||
		strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return &ValidationError{"email", "Enter a valid email address."}
	}
	return nil
}

// CreateParams describes a new account.
type CreateParams struct {
	Username    string
	DisplayName string
	Email       string
	// PasswordHash must already be hashed. This package never sees plaintext.
	PasswordHash string
	IsAdmin      bool
	IsEnabled    bool
	// QuotaBytes nil means unlimited.
	QuotaBytes         *int64
	MustChangePassword bool
}

// Validate checks the fields this package is responsible for. Password strength
// is the auth package's concern and is checked before hashing.
func (p *CreateParams) Validate() error {
	p.Username = NormalizeUsername(p.Username)
	p.DisplayName = strings.TrimSpace(p.DisplayName)
	p.Email = strings.TrimSpace(strings.ToLower(p.Email))

	if p.DisplayName == "" {
		p.DisplayName = p.Username
	}

	if err := ValidateUsername(p.Username); err != nil {
		return err
	}
	if err := ValidateDisplayName(p.DisplayName); err != nil {
		return err
	}
	if err := ValidateEmail(p.Email); err != nil {
		return err
	}
	if p.PasswordHash == "" {
		return &ValidationError{"password", "A password hash is required."}
	}
	if p.QuotaBytes != nil && *p.QuotaBytes < 0 {
		return &ValidationError{"quota_bytes", "Quota must not be negative."}
	}
	return nil
}
