package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/audit"
	"github.com/joshuanmoses/piCloud/backend/internal/auth"
	"github.com/joshuanmoses/piCloud/backend/internal/sessions"
	"github.com/joshuanmoses/piCloud/backend/internal/users"
)

// Sessions.
//
// Two audiences, one shape. A person managing their own sessions and an
// administrator investigating an account want the same fields; what differs is
// whose sessions they may see, which is decided by the route rather than by the
// response.

// sessionResponse is one session as shown to a person.
//
// The token is absent by construction: the store keeps only its hash, so there
// is nothing here that could be serialized even by mistake.
type sessionDetail struct {
	ID string `json:"id"`

	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	ExpiresAt  string `json:"expires_at"`

	// Current marks the session making this very request, so the interface can
	// label it and refuse to let somebody sign themselves out by accident.
	Current bool `json:"current"`

	Remember bool `json:"remember"`
	// IPAddress and UserAgent are what the session was created from. They are
	// how a person recognises a session they do not remember starting.
	IPAddress string `json:"ip_address,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
	// Device is the user agent reduced to something readable.
	Device string `json:"device,omitempty"`

	RevokedAt     string `json:"revoked_at,omitempty"`
	RevokedReason string `json:"revoked_reason,omitempty"`
}

func toSessionDetail(s *sessions.Session, currentID uuid.UUID) sessionDetail {
	r := sessionDetail{
		ID:         s.ID.String(),
		CreatedAt:  s.CreatedAt.UTC().Format(timeFormat),
		LastSeenAt: s.LastSeenAt.UTC().Format(timeFormat),
		ExpiresAt:  s.ExpiresAt.UTC().Format(timeFormat),
		Current:    s.ID == currentID,
		Remember:   s.Remember,
		IPAddress:  s.IPAddress,
		UserAgent:  s.UserAgent,
		Device:     describeUserAgent(s.UserAgent),
	}
	if s.RevokedAt != nil {
		r.RevokedAt = s.RevokedAt.UTC().Format(timeFormat)
		r.RevokedReason = s.RevokedReason
	}
	return r
}

func toSessionDetails(list []*sessions.Session, currentID uuid.UUID) []sessionDetail {
	out := make([]sessionDetail, 0, len(list))
	for _, s := range list {
		out = append(out, toSessionDetail(s, currentID))
	}
	return out
}

// describeUserAgent reduces a user-agent string to something a person can
// recognise.
//
// Deliberately crude. A full parser would be a dependency and a maintenance
// burden for a field whose only job is to help somebody answer "is that me?" —
// and the raw string is returned alongside for anybody who wants the detail.
func describeUserAgent(ua string) string {
	if ua == "" {
		return ""
	}

	browser := "Browser"
	switch {
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "OPR/"), strings.Contains(ua, "Opera"):
		browser = "Opera"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	case strings.Contains(ua, "curl/"):
		browser = "curl"
	}

	platform := ""
	switch {
	case strings.Contains(ua, "Android"):
		platform = "Android"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		platform = "iOS"
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		platform = "macOS"
	case strings.Contains(ua, "Windows"):
		platform = "Windows"
	case strings.Contains(ua, "Linux"):
		platform = "Linux"
	}

	if platform == "" {
		return browser
	}
	return browser + " on " + platform
}

// handleRevokeMyOtherSessions ends every session except this one.
//
// The one action a person takes when they think somebody else has their
// password, so it is deliberately a single click and deliberately keeps the
// current session — signing yourself out at the same moment would leave you
// unable to change your password.
func (s *Server) handleRevokeMyOtherSessions(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	var except *uuid.UUID
	if session := SessionFromContext(r.Context()); session != nil {
		id := session.ID
		except = &id
	}

	revoked, err := s.sessionStore.RevokeAllForUser(
		r.Context(), user.ID, except, "signed out everywhere else")
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	s.audit(r, audit.ActionSessionRevoke, audit.Success,
		auditTarget(user.ID, "user", user.Username),
		auditDetail(map[string]any{"revoked": revoked, "scope": "all other sessions"}))

	s.logger.Info("user signed out their other sessions",
		"user_id", user.ID, "revoked", revoked)

	WriteJSON(w, r, http.StatusOK, map[string]any{"revoked": revoked})
}

// --- Administrator views ----------------------------------------------------

// handleAdminListSessions returns every live session on the instance.
func (s *Server) handleAdminListSessions(w http.ResponseWriter, r *http.Request) {
	list, err := s.sessionStore.ListAll(r.Context(), 200)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	// The owner of each session, batched.
	ids := make([]uuid.UUID, 0, len(list))
	seen := make(map[uuid.UUID]bool, len(list))
	for _, session := range list {
		if !seen[session.UserID] {
			seen[session.UserID] = true
			ids = append(ids, session.UserID)
		}
	}
	owners, err := s.userStore.GetManyPublic(r.Context(), ids)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	current := uuid.Nil
	if session := SessionFromContext(r.Context()); session != nil {
		current = session.ID
	}

	type adminSession struct {
		sessionDetail
		User *users.Public `json:"user,omitempty"`
	}

	items := make([]adminSession, 0, len(list))
	for _, session := range list {
		item := adminSession{sessionDetail: toSessionDetail(session, current)}
		if owner, ok := owners[session.UserID]; ok {
			item.User = &owner
		}
		items = append(items, item)
	}

	WriteJSON(w, r, http.StatusOK, map[string]any{
		"sessions": items,
		"total":    len(items),
	})
}

// handleAdminRevokeUserSessions signs an account out everywhere.
func (s *Server) handleAdminRevokeUserSessions(w http.ResponseWriter, r *http.Request) {
	target, apiErr := s.adminUser(r)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	revoked, err := s.sessionStore.RevokeAllForUser(
		r.Context(), target.ID, nil, "signed out by an administrator")
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	s.audit(r, audit.ActionSessionRevoke, audit.Success,
		auditTarget(target.ID, "user", target.Username),
		auditDetail(map[string]any{"revoked": revoked, "by": "administrator"}))

	s.logger.Warn("administrator signed an account out everywhere",
		"user_id", target.ID, "username", target.Username,
		"revoked", revoked, "actor_id", MustUser(r.Context()).ID)

	WriteJSON(w, r, http.StatusOK, map[string]any{"revoked": revoked})
}

// translateAuthError maps auth-service errors to HTTP responses.
func translateAuthError(err error) error {
	var policy *auth.PasswordError
	if errors.As(err, &policy) {
		return ErrValidation(policy.Error()).WithField("password", policy.Error())
	}
	switch {
	case errors.Is(err, users.ErrNotFound):
		return ErrNotFound()
	default:
		return ErrInternal(err)
	}
}
