// Package publiclinks implements access by token, for people without an
// account.
//
// One type covers both directions. A share link hands existing content
// outward; a file request collects content inward. Both are a token granting a
// permission set on a node to an anonymous visitor, and the permission set is
// what tells them apart — so the security-critical path (resolve, check
// revoked, check expired, check password, check the usage cap) exists once.
package publiclinks

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
)

// Errors returned by this package.
var (
	// ErrNotFound means no such link. Returned for every failure mode —
	// unknown token, revoked, expired, used up — so a caller probing tokens
	// cannot tell which they found.
	ErrNotFound = errors.New("publiclinks: no such link")
	// ErrInvalidToken means the token was malformed.
	ErrInvalidToken = errors.New("publiclinks: malformed token")
	// ErrPasswordRequired means the link is password-protected.
	ErrPasswordRequired = errors.New("publiclinks: this link requires a password")
	// ErrWrongPassword means the supplied password did not match.
	ErrWrongPassword = errors.New("publiclinks: incorrect password")
	// ErrUsedUp means the usage cap has been reached.
	ErrUsedUp = errors.New("publiclinks: this link has reached its limit")
	// ErrNotPermitted means the caller may not do this with the link.
	ErrNotPermitted = errors.New("publiclinks: not permitted")
	// ErrDisabled means the feature is switched off for the instance.
	ErrDisabled = errors.New("publiclinks: public links are disabled on this server")
	// ErrFileTypeNotAllowed means an upload's extension is not on the list.
	ErrFileTypeNotAllowed = errors.New("publiclinks: that file type is not accepted here")
)

// TokenBytes is the entropy of a link token.
//
// 256 bits. The token *is* the credential for a share link, so guessing must
// be infeasible however many links exist and however long an attacker probes.
const TokenBytes = 32

// EncodedTokenLength is the length of a base64url-encoded token.
const EncodedTokenLength = 43

// Token is a freshly minted link token.
//
// It exists in memory once, long enough to build the URL shown to its creator.
// Only its hash is stored.
type Token string

// String hides the value so it cannot reach a log line through %v.
func (t Token) String() string { return "[REDACTED]" }

// Reveal returns the real token. Call this only when building the URL.
func (t Token) Reveal() string { return string(t) }

// GenerateToken produces a URL-safe random token.
func GenerateToken() (Token, error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		// A predictable token would be guessable, which for a public link
		// means the content is effectively published. Failing is the only
		// safe response.
		return "", fmt.Errorf("publiclinks: cannot generate token: %w", err)
	}
	return Token(base64.RawURLEncoding.EncodeToString(b)), nil
}

// HashToken derives the stored form of a token.
//
// SHA-256 rather than a slow hash: the input is 256 bits of uniform
// randomness, so there is no dictionary to attack, and this runs on every
// portal request.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// ValidateTokenShape rejects obviously malformed tokens without a database
// round trip, so a flood of garbage cannot be turned into database load.
func ValidateTokenShape(token string) error {
	if len(token) != EncodedTokenLength {
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

// Kind describes what a link is for, derived from its permissions.
type Kind string

const (
	// KindDownload lets a visitor see and fetch content.
	KindDownload Kind = "download"
	// KindView lets a visitor see content but not fetch it.
	KindView Kind = "view"
	// KindUpload is a file request: the visitor may add files but sees nothing.
	KindUpload Kind = "upload"
	// KindUploadAndView lets a visitor both add files and see what is there.
	KindUploadAndView Kind = "upload_view"
)

// Link is a tokenised grant.
type Link struct {
	ID     uuid.UUID `json:"id"`
	NodeID uuid.UUID `json:"node_id"`

	Permissions permissions.Set `json:"-"`

	// HasPassword reports protection without revealing anything about it.
	HasPassword bool `json:"has_password"`

	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	MaxUses   *int       `json:"max_uses,omitempty"`
	UseCount  int        `json:"use_count"`

	MaxUploadBytes    *int64   `json:"max_upload_bytes,omitempty"`
	AllowedExtensions []string `json:"allowed_extensions,omitempty"`

	Title   string `json:"title,omitempty"`
	Message string `json:"message,omitempty"`

	CreatedBy  *uuid.UUID `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// Kind classifies the link from its permissions.
func (l *Link) Kind() Kind {
	canUpload := l.Permissions.HasAny(permissions.Upload | permissions.Create)
	canRead := l.Permissions.Has(permissions.Read)
	canDownload := l.Permissions.Has(permissions.Download)

	switch {
	case canUpload && canRead:
		return KindUploadAndView
	case canUpload:
		return KindUpload
	case canDownload:
		return KindDownload
	default:
		return KindView
	}
}

// IsUploadPortal reports whether visitors may add files.
func (l *Link) IsUploadPortal() bool {
	return l.Permissions.HasAny(permissions.Upload | permissions.Create)
}

// IsRevoked reports whether the owner has withdrawn the link.
func (l *Link) IsRevoked() bool { return l.RevokedAt != nil }

// IsExpired reports whether the link's expiry has passed.
func (l *Link) IsExpired(now time.Time) bool {
	return l.ExpiresAt != nil && !l.ExpiresAt.After(now)
}

// IsUsedUp reports whether the usage cap has been reached.
func (l *Link) IsUsedUp() bool {
	return l.MaxUses != nil && l.UseCount >= *l.MaxUses
}

// IsLive reports whether the link may currently be used.
func (l *Link) IsLive(now time.Time) bool {
	return !l.IsRevoked() && !l.IsExpired(now) && !l.IsUsedUp()
}

// RemainingUses returns how many uses are left, or -1 when unlimited.
func (l *Link) RemainingUses() int {
	if l.MaxUses == nil {
		return -1
	}
	remaining := *l.MaxUses - l.UseCount
	if remaining < 0 {
		return 0
	}
	return remaining
}

// --- Extension filtering ----------------------------------------------------

// NormalizeExtensions cleans a caller-supplied extension list.
//
// The result is lowercase, dot-prefixed, deduplicated, and bounded. This is a
// convenience for whoever is collecting files — so a visitor uploading the
// wrong thing is told immediately — and never a security control: the server
// still sniffs content and never trusts an extension.
func NormalizeExtensions(raw []string) []string {
	const maxExtensions = 50

	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))

	for _, value := range raw {
		ext := strings.ToLower(strings.TrimSpace(value))
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		// Only a plain extension: no separators, no wildcards, nothing that
		// would be surprising when compared against a filename.
		if strings.ContainsAny(ext[1:], "./\\*? \t") || len(ext) > 16 {
			continue
		}
		if _, duplicate := seen[ext]; duplicate {
			continue
		}
		seen[ext] = struct{}{}
		out = append(out, ext)
		if len(out) >= maxExtensions {
			break
		}
	}
	return out
}

// ExtensionAllowed reports whether a filename passes the link's filter.
func (l *Link) ExtensionAllowed(filename string) bool {
	if len(l.AllowedExtensions) == 0 {
		return true
	}
	ext := strings.ToLower(path.Ext(filename))
	for _, allowed := range l.AllowedExtensions {
		if ext == allowed {
			return true
		}
	}
	return false
}

// Presets for the four ways a link is normally configured.
var (
	// PresetView lets a visitor see what is there, without fetching it.
	PresetView = permissions.Read
	// PresetDownload is the ordinary share link.
	PresetDownload = permissions.Read | permissions.Download
	// PresetUpload is a file request: the visitor adds files and sees nothing
	// else. Read is deliberately absent, so somebody handed the link cannot
	// enumerate what other people submitted.
	PresetUpload = permissions.Upload | permissions.Create
	// PresetUploadAndView lets a visitor both contribute and browse.
	PresetUploadAndView = permissions.Read | permissions.Download | permissions.Upload | permissions.Create
)

// ParseKind maps a kind name onto its permission set.
func ParseKind(name string) (permissions.Set, error) {
	switch Kind(strings.ToLower(strings.TrimSpace(name))) {
	case KindView:
		return PresetView, nil
	case KindDownload, "":
		return PresetDownload, nil
	case KindUpload:
		return PresetUpload, nil
	case KindUploadAndView:
		return PresetUploadAndView, nil
	default:
		return 0, fmt.Errorf("publiclinks: %q is not a valid link type", name)
	}
}

// KindNames lists the valid kind names, for validation messages.
func KindNames() []string {
	return []string{string(KindView), string(KindDownload), string(KindUpload), string(KindUploadAndView)}
}
