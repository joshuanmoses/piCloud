package auth

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestCSRFTokenRoundTrip(t *testing.T) {
	t.Parallel()
	c := NewCSRFTokenizer("an-application-secret-key-value-here")
	sessionID := uuid.New()

	token := c.Generate(sessionID)
	if token == "" {
		t.Fatal("Generate returned an empty token")
	}
	if err := c.Validate(sessionID, token); err != nil {
		t.Errorf("a freshly generated token was rejected: %v", err)
	}
}

func TestCSRFTokenIsDeterministicPerSession(t *testing.T) {
	t.Parallel()
	c := NewCSRFTokenizer("secret")
	sessionID := uuid.New()

	// Determinism is what allows the token to be recomputed on each request
	// instead of stored.
	if c.Generate(sessionID) != c.Generate(sessionID) {
		t.Error("the token for a session must be stable")
	}
}

// A token minted for one session must be useless in another, or an attacker
// with any account could forge requests as any other user.
func TestCSRFTokenIsBoundToItsSession(t *testing.T) {
	t.Parallel()
	c := NewCSRFTokenizer("secret")

	victim := uuid.New()
	attacker := uuid.New()

	attackerToken := c.Generate(attacker)
	if err := c.Validate(victim, attackerToken); !errors.Is(err, ErrInvalidCSRFToken) {
		t.Fatalf("a token from another session was accepted for the victim: %v", err)
	}
}

// Two instances with different secrets must not accept each other's tokens.
func TestCSRFTokenIsBoundToTheApplicationSecret(t *testing.T) {
	t.Parallel()
	sessionID := uuid.New()

	first := NewCSRFTokenizer("secret-one")
	second := NewCSRFTokenizer("secret-two")

	if err := second.Validate(sessionID, first.Generate(sessionID)); !errors.Is(err, ErrInvalidCSRFToken) {
		t.Fatal("a token signed with a different secret was accepted")
	}
}

func TestCSRFValidateRejectsBadTokens(t *testing.T) {
	t.Parallel()
	c := NewCSRFTokenizer("secret")
	sessionID := uuid.New()
	valid := c.Generate(sessionID)

	bad := []struct{ name, token string }{
		{"empty", ""},
		{"garbage", "not-a-token"},
		{"truncated", valid[:len(valid)-1]},
		{"extended", valid + "a"},
		{"flipped character", "A" + valid[1:]},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if err := c.Validate(sessionID, tc.token); !errors.Is(err, ErrInvalidCSRFToken) {
				t.Errorf("Validate accepted %s: %q", tc.name, tc.token)
			}
		})
	}
}

// Safe methods are exempt; anything that can change state is not.
func TestRequiresCSRF(t *testing.T) {
	t.Parallel()
	exempt := []string{"GET", "HEAD", "OPTIONS", "TRACE", "get", "head"}
	for _, m := range exempt {
		if RequiresCSRF(m) {
			t.Errorf("%s should be exempt from CSRF", m)
		}
	}
	required := []string{"POST", "PUT", "PATCH", "DELETE", "post", "patch"}
	for _, m := range required {
		if !RequiresCSRF(m) {
			t.Errorf("%s must require a CSRF token", m)
		}
	}
}

// The CSRF cookie must be readable by the frontend; the session cookie must not.
func TestCSRFCookieIsNotTheSessionCookie(t *testing.T) {
	t.Parallel()
	if CSRFCookieName == "pifiles_session" {
		t.Fatal("the CSRF cookie must be distinct from the session cookie")
	}
}
