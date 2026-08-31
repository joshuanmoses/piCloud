package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// ErrInvalidCSRFToken means the submitted token did not match the session.
var ErrInvalidCSRFToken = errors.New("auth: invalid CSRF token")

// CSRFTokenizer derives per-session CSRF tokens.
//
// The scheme is a signed double-submit cookie. The token is
// HMAC-SHA256(secret, session ID), delivered in a cookie the browser's
// JavaScript *can* read, and echoed back in a request header.
//
// Why this works: a cross-site attacker can cause the browser to send the
// session cookie, but the same-origin policy prevents them reading the CSRF
// cookie, so they cannot populate the header. Deriving the token from the
// session ID rather than storing a random value per session means no extra
// database column and no lookup — it is recomputed and compared on each
// request.
//
// SameSite=Lax on the session cookie already blocks the common cases. This is
// the second layer, because SameSite is a browser behaviour rather than a
// server-enforced guarantee.
type CSRFTokenizer struct {
	secret []byte
}

// NewCSRFTokenizer derives a signing key from the application secret.
//
// The application secret is not used directly: separating the key by purpose
// means a token from one subsystem can never be valid in another.
func NewCSRFTokenizer(appSecret string) *CSRFTokenizer {
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write([]byte("pifiles/csrf/v1"))
	return &CSRFTokenizer{secret: mac.Sum(nil)}
}

// Generate returns the CSRF token for a session.
func (c *CSRFTokenizer) Generate(sessionID uuid.UUID) string {
	mac := hmac.New(sha256.New, c.secret)
	mac.Write(sessionID[:])
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Validate checks a submitted token against a session.
//
// The comparison is constant-time; a timing side channel would let an attacker
// recover a valid token byte by byte.
func (c *CSRFTokenizer) Validate(sessionID uuid.UUID, token string) error {
	if token == "" {
		return ErrInvalidCSRFToken
	}
	expected := c.Generate(sessionID)
	if !hmac.Equal([]byte(expected), []byte(token)) {
		return ErrInvalidCSRFToken
	}
	return nil
}

// CSRFHeaderName is the header carrying the token.
const CSRFHeaderName = "X-CSRF-Token"

// CSRFCookieName is the cookie carrying the token. It is deliberately NOT
// HttpOnly: the frontend must read it in order to set the header.
const CSRFCookieName = "pifiles_csrf"

// RequiresCSRF reports whether an HTTP method changes state and therefore needs
// a token.
//
// GET, HEAD, and OPTIONS are exempt because they are defined to be safe. Any
// handler that mutates state on a GET is a bug in its own right, and would be
// vulnerable regardless of this check.
func RequiresCSRF(method string) bool {
	switch strings.ToUpper(method) {
	case "GET", "HEAD", "OPTIONS", "TRACE":
		return false
	default:
		return true
	}
}
