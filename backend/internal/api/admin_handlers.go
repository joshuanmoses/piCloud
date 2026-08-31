package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/audit"
	"github.com/joshuanmoses/piCloud/backend/internal/users"
)

// Administration.
//
// Every route here sits behind RequireAdmin, and the boundary is worth stating
// precisely: an administrator manages *accounts and the instance*, not other
// people's files. There is no endpoint in this file that reads, lists, or
// downloads somebody's content, and the permission model does not grant an
// administrator access to it. Running a server is not the same as being
// entitled to read what people store on it, and an audit trail could not tell
// legitimate support from snooping if it were.
//
// What an administrator can do is disable an account, reset its password, and
// change its quota — all of which are recorded, and none of which quietly hands
// them the contents.

// adminUserResponse is an account as an administrator sees it.
//
// A superset of the public profile, and deliberately still not everything: the
// password hash is unserialisable by construction, and the lockout counters are
// summarised rather than exposed as raw counts.
type adminUserResponse struct {
	ID          uuid.UUID `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email,omitempty"`

	IsEnabled bool `json:"is_enabled"`
	IsAdmin   bool `json:"is_admin"`
	IsLocked  bool `json:"is_locked"`

	QuotaBytes *int64 `json:"quota_bytes"`
	UsedBytes  int64  `json:"used_bytes"`

	CreatedAt   string `json:"created_at"`
	LastLoginAt string `json:"last_login_at,omitempty"`

	MustChangePassword bool `json:"must_change_password"`
}

func toAdminUser(u *users.User, now time.Time) adminUserResponse {
	r := adminUserResponse{
		ID:                 u.ID,
		Username:           u.Username,
		DisplayName:        u.DisplayName,
		Email:              u.Email,
		IsEnabled:          u.IsEnabled,
		IsAdmin:            u.IsAdmin,
		IsLocked:           u.IsLocked(now),
		QuotaBytes:         u.QuotaBytes,
		UsedBytes:          u.UsedBytes,
		CreatedAt:          u.CreatedAt.UTC().Format(timeFormat),
		MustChangePassword: u.MustChangePassword,
	}
	if u.LastLoginAt != nil {
		r.LastLoginAt = u.LastLoginAt.UTC().Format(timeFormat)
	}
	return r
}

// --- Accounts ---------------------------------------------------------------

func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	params := users.ListParams{
		Search: strings.TrimSpace(query.Get("q")),
		Limit:  parseIntDefault(query.Get("limit"), 50),
		Offset: parseIntDefault(query.Get("offset"), 0),
	}
	if raw := query.Get("enabled"); raw != "" {
		enabled := raw == "1" || strings.EqualFold(raw, "true")
		params.Enabled = &enabled
	}

	list, total, err := s.userStore.List(r.Context(), params)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	now := time.Now()
	items := make([]adminUserResponse, 0, len(list))
	for _, u := range list {
		items = append(items, toAdminUser(u, now))
	}

	WriteJSON(w, r, http.StatusOK, map[string]any{
		"users":    items,
		"total":    total,
		"limit":    params.Limit,
		"offset":   params.Offset,
		"has_more": params.Offset+len(items) < total,
	})
}

func (s *Server) handleAdminGetUser(w http.ResponseWriter, r *http.Request) {
	u, apiErr := s.adminUser(r)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	sessions, err := s.sessionStore.ListForUser(r.Context(), u.ID)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	WriteJSON(w, r, http.StatusOK, map[string]any{
		"user":     toAdminUser(u, time.Now()),
		"sessions": toSessionDetails(sessions, uuid.Nil),
	})
}

type createUserRequest struct {
	Username           string `json:"username"`
	DisplayName        string `json:"display_name"`
	Email              string `json:"email"`
	Password           string `json:"password"`
	IsAdmin            bool   `json:"is_admin"`
	QuotaBytes         *int64 `json:"quota_bytes"`
	MustChangePassword *bool  `json:"must_change_password"`
}

// handleAdminCreateUser adds an account.
//
// The password is supplied by the administrator and the account is marked as
// needing a change by default, so the person who ends up using it chooses
// something the administrator does not know. Turning that off is possible and
// deliberate; leaving it on is the default because the alternative is an
// administrator who can sign in as anybody they created.
func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}

	username := strings.TrimSpace(req.Username)
	if err := users.ValidateUsername(username); err != nil {
		WriteError(w, r, ErrValidation(err.Error()).WithField("username", err.Error()))
		return
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = username
	}

	// The policy sees the username, display name, and email so it can refuse a
	// password containing any of them.
	if err := s.authService.Policy().Validate(
		req.Password, username, displayName, req.Email); err != nil {
		WriteError(w, r, ErrValidation(err.Error()).WithField("password", err.Error()))
		return
	}

	hash, err := s.authService.Hasher().Hash(req.Password)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	mustChange := true
	if req.MustChangePassword != nil {
		mustChange = *req.MustChangePassword
	}

	created, err := s.userStore.Create(r.Context(), users.CreateParams{
		Username:           username,
		DisplayName:        displayName,
		Email:              strings.TrimSpace(req.Email),
		PasswordHash:       hash,
		IsAdmin:            req.IsAdmin,
		IsEnabled:          true,
		QuotaBytes:         req.QuotaBytes,
		MustChangePassword: mustChange,
	})
	if err != nil {
		s.audit(r, audit.ActionUserCreate, audit.Failure,
			auditTarget(uuid.Nil, "user", username))
		WriteError(w, r, translateUserError(err))
		return
	}

	s.audit(r, audit.ActionUserCreate, audit.Success,
		auditTarget(created.ID, "user", created.Username),
		auditDetail(map[string]any{"is_admin": created.IsAdmin}))

	s.logger.Info("account created by an administrator",
		"user_id", created.ID, "username", created.Username,
		"is_admin", created.IsAdmin, "actor_id", MustUser(r.Context()).ID)

	WriteJSON(w, r, http.StatusCreated, map[string]any{"user": toAdminUser(created, time.Now())})
}

type updateUserRequest struct {
	DisplayName *string `json:"display_name"`
	Email       *string `json:"email"`
	IsEnabled   *bool   `json:"is_enabled"`
	IsAdmin     *bool   `json:"is_admin"`
	// QuotaBytes carries three states, which is one more than a pointer can
	// express: absent means leave it alone, null means unlimited, a number
	// means that many bytes.
	QuotaBytes         quotaField `json:"quota_bytes"`
	MustChangePassword *bool      `json:"must_change_password"`
}

// quotaField distinguishes an omitted quota from an explicit null.
//
// A value type rather than a pointer, deliberately: encoding/json sets a
// *pointer* field to nil for a JSON null without ever calling UnmarshalJSON,
// so "null" and "absent" would arrive identically and clearing a quota would
// silently do nothing. A value type is always unmarshalled when the key is
// present, and left alone when it is not, which is exactly the distinction.
type quotaField struct {
	// Present is true when the key appeared in the request at all.
	Present bool
	// Value is nil for an explicit null, meaning unlimited.
	Value *int64
}

func (q *quotaField) UnmarshalJSON(data []byte) error {
	q.Present = true
	if string(data) == "null" {
		q.Value = nil
		return nil
	}
	var n int64
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("a quota must be a number of bytes, or null for unlimited")
	}
	if n < 0 {
		return fmt.Errorf("a quota cannot be negative")
	}
	q.Value = &n
	return nil
}

func (s *Server) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	target, apiErr := s.adminUser(r)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	var req updateUserRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}

	actor := MustUser(r.Context())

	// An administrator removing their own last route back in is a mistake the
	// store also refuses, but refusing here names the actual problem instead of
	// "the last administrator", which reads as being about somebody else.
	if target.ID == actor.ID {
		if req.IsAdmin != nil && !*req.IsAdmin {
			WriteError(w, r, ErrBadRequest(
				"You cannot remove your own administrator access. Ask another administrator to do it."))
			return
		}
		if req.IsEnabled != nil && !*req.IsEnabled {
			WriteError(w, r, ErrBadRequest("You cannot disable your own account."))
			return
		}
	}

	params := users.UpdateParams{
		DisplayName:        req.DisplayName,
		Email:              req.Email,
		IsEnabled:          req.IsEnabled,
		IsAdmin:            req.IsAdmin,
		MustChangePassword: req.MustChangePassword,
	}
	if req.QuotaBytes.Present {
		params.QuotaBytes = &req.QuotaBytes.Value
	}

	updated, err := s.userStore.Update(r.Context(), target.ID, params)
	if err != nil {
		s.audit(r, audit.ActionUserUpdate, audit.Failure,
			auditTarget(target.ID, "user", target.Username))
		WriteError(w, r, translateUserError(err))
		return
	}

	// Disabling an account has to end its sessions, or the person stays signed
	// in until their cookie expires — which is not what "disabled" means to
	// the administrator who clicked it.
	if req.IsEnabled != nil && !*req.IsEnabled {
		if _, err := s.sessionStore.RevokeAllForUser(
			r.Context(), target.ID, nil, "account disabled"); err != nil {
			s.logger.Error("could not revoke sessions for a disabled account",
				"user_id", target.ID, "error", err)
		}
	}

	s.auditUserChange(r, target, updated)
	WriteJSON(w, r, http.StatusOK, map[string]any{"user": toAdminUser(updated, time.Now())})
}

// auditUserChange records what actually changed, one event per meaningful
// change rather than a single opaque "updated".
func (s *Server) auditUserChange(r *http.Request, before, after *users.User) {
	target := auditTarget(after.ID, "user", after.Username)

	if before.IsEnabled != after.IsEnabled {
		action := audit.ActionUserEnable
		if !after.IsEnabled {
			action = audit.ActionUserDisable
		}
		s.audit(r, action, audit.Success, target)
	}
	if before.IsAdmin != after.IsAdmin {
		s.audit(r, audit.ActionUserUpdate, audit.Success, target,
			auditDetail(map[string]any{"is_admin": after.IsAdmin}))
	}
	if !sameQuota(before.QuotaBytes, after.QuotaBytes) {
		s.audit(r, audit.ActionQuotaChange, audit.Success, target,
			auditDetail(map[string]any{
				"from": describeQuota(before.QuotaBytes),
				"to":   describeQuota(after.QuotaBytes),
			}))
	}
	if before.DisplayName != after.DisplayName || before.Email != after.Email {
		s.audit(r, audit.ActionUserUpdate, audit.Success, target)
	}
}

func sameQuota(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func describeQuota(q *int64) string {
	if q == nil {
		return "unlimited"
	}
	return strconv.FormatInt(*q, 10)
}

type resetPasswordRequest struct {
	Password string `json:"password"`
	// MustChange defaults to true: an administrator who knows somebody's
	// password should not be able to keep signing in as them.
	MustChange *bool `json:"must_change_password"`
}

func (s *Server) handleAdminResetPassword(w http.ResponseWriter, r *http.Request) {
	target, apiErr := s.adminUser(r)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	var req resetPasswordRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}

	mustChange := true
	if req.MustChange != nil {
		mustChange = *req.MustChange
	}

	actor := MustUser(r.Context())

	// The auth service owns this: it validates against the policy, hashes, and
	// — crucially — revokes every existing session. A reset is what an
	// administrator does when an account may be compromised, and leaving the
	// intruder signed in would defeat the exercise.
	if err := s.authService.AdminResetPassword(
		r.Context(), actor.ID, target.ID, req.Password, mustChange); err != nil {
		s.audit(r, audit.ActionUserReset, audit.Failure,
			auditTarget(target.ID, "user", target.Username))
		WriteError(w, r, translateAuthError(err))
		return
	}

	s.audit(r, audit.ActionUserReset, audit.Success,
		auditTarget(target.ID, "user", target.Username),
		auditDetail(map[string]any{"must_change_password": mustChange}))

	WriteJSON(w, r, http.StatusOK, map[string]any{
		"must_change_password": mustChange,
		"sessions_revoked":     true,
	})
}

// handleAdminDeleteUser removes an account and everything it owned.
//
// Genuinely destructive, and the response says how much went with it. The audit
// record survives, which is the point of storing the actor's name alongside
// their identifier.
func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	target, apiErr := s.adminUser(r)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	actor := MustUser(r.Context())
	if target.ID == actor.ID {
		WriteError(w, r, ErrBadRequest(
			"You cannot delete your own account. Ask another administrator to do it."))
		return
	}

	// Recorded before the deletion, because afterwards there is no account to
	// name and the label is all that survives.
	s.audit(r, audit.ActionUserDelete, audit.Success,
		auditTarget(target.ID, "user", target.Username),
		auditDetail(map[string]any{
			"used_bytes": target.UsedBytes,
			"was_admin":  target.IsAdmin,
		}))

	if err := s.userStore.Delete(r.Context(), target.ID); err != nil {
		WriteError(w, r, translateUserError(err))
		return
	}

	s.logger.Warn("account deleted by an administrator",
		"user_id", target.ID, "username", target.Username, "actor_id", actor.ID)
	WriteNoContent(w)
}

// handleAdminUnlockUser clears a lockout early.
func (s *Server) handleAdminUnlockUser(w http.ResponseWriter, r *http.Request) {
	target, apiErr := s.adminUser(r)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	if err := s.userStore.Unlock(r.Context(), target.ID); err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	s.audit(r, audit.ActionUserUpdate, audit.Success,
		auditTarget(target.ID, "user", target.Username),
		auditDetail(map[string]any{"unlocked": true}))

	WriteJSON(w, r, http.StatusOK, map[string]any{"unlocked": true})
}

// adminUser resolves the {id} path parameter to an account.
func (s *Server) adminUser(r *http.Request) (*users.User, error) {
	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		return nil, ErrNotFound()
	}
	u, err := s.userStore.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, users.ErrNotFound) {
			return nil, ErrNotFound()
		}
		return nil, ErrInternal(err)
	}
	return u, nil
}
