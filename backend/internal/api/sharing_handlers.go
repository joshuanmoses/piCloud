package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/audit"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
	"github.com/joshuanmoses/piCloud/backend/internal/sharing"
)

// Sharing handlers.
//
// A share is a permission grant presented for people. The endpoints are named
// /shares because that is the user-facing concept; the rows they create live in
// the permissions table, which is also what the evaluator reads. One source of
// truth, so a revoked share cannot leave access behind.

type createShareRequest struct {
	NodeID string `json:"node_id"`

	// Exactly one recipient. A username is accepted so the interface can offer
	// a plain text field rather than requiring the client to resolve an id.
	Username  string `json:"username,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	GroupID   string `json:"group_id,omitempty"`
	GroupName string `json:"group_name,omitempty"`

	// Permission names, or a single role name such as "editor".
	Permissions []string `json:"permissions"`

	// Inherit defaults to true; a grant that does not reach a folder's
	// contents is rarely what somebody means.
	Inherit *bool `json:"inherit,omitempty"`

	// Deny takes access away instead of granting it.
	//
	// The only way to express "everyone in Accounting may edit this, except
	// Bob": a denial beats every grant, including one inherited from a group.
	// It needs Manage rather than Share, because taking somebody's access away
	// is administering the item rather than sharing it out.
	Deny bool `json:"deny,omitempty"`

	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Message   string     `json:"message,omitempty"`
}

// handleCreateShare shares a node with a user or a group.
func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	var req createShareRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}

	nodeID, err := uuid.Parse(req.NodeID)
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	// A denial must take away exactly what it names. Parse implies Read, which
	// is right for a grant — you cannot download what you cannot find — and
	// dangerous for a denial, where implying Read turns "you may not delete
	// this" into "you may not see this at all".
	parse := permissions.Parse
	if req.Deny {
		parse = permissions.ParseExact
	}

	permissionSet, err := parse(req.Permissions)
	if err != nil {
		WriteError(w, r, ErrValidation(
			"Choose what the recipient may do.").WithField("permissions", err.Error()))
		return
	}

	params := sharing.CreateParams{
		NodeID:      nodeID,
		ActorID:     user.ID,
		Username:    req.Username,
		GroupName:   req.GroupName,
		Permissions: permissionSet,
		Deny:        req.Deny,
		Inherit:     req.Inherit == nil || *req.Inherit,
		ExpiresAt:   req.ExpiresAt,
		Message:     req.Message,
	}

	if req.UserID != "" {
		id, err := uuid.Parse(req.UserID)
		if err != nil {
			WriteError(w, r, ErrValidation("That is not a valid user.").WithField("user_id", "Invalid identifier."))
			return
		}
		params.UserID = &id
	}
	if req.GroupID != "" {
		id, err := uuid.Parse(req.GroupID)
		if err != nil {
			WriteError(w, r, ErrValidation("That is not a valid group.").WithField("group_id", "Invalid identifier."))
			return
		}
		params.GroupID = &id
	}

	if params.ExpiresAt != nil && !params.ExpiresAt.After(time.Now()) {
		WriteError(w, r, ErrValidation("The expiry date must be in the future.").
			WithField("expires_at", "Choose a date in the future."))
		return
	}

	share, err := s.sharing.Create(r.Context(), params)
	if err != nil {
		WriteError(w, r, translateSharingError(err))
		return
	}

	// Who was given access to what, and how much. The most-asked question of an
	// audit trail on a file server, and the recipient is named rather than
	// left as an identifier so the record still reads if their account goes.
	//
	// A denial is recorded under its own action: "who took Bob's access away"
	// and "who gave Bob access" are different questions, and a trail that
	// answered both with the same word would be poorer for it.
	action := audit.ActionShareGrant
	if share.Deny {
		action = audit.ActionShareDeny
	}
	s.audit(r, action, audit.Success,
		auditTarget(share.Node.ID, string(share.Node.Kind), share.Node.Name),
		auditDetail(map[string]any{
			"recipient":    shareRecipient(share),
			"subject_type": string(share.SubjectType),
			"role":         share.Role,
			"permissions":  share.Permissions,
			"deny":         share.Deny,
		}))

	WriteJSON(w, r, http.StatusCreated, map[string]any{"share": share})
}

// handleListNodeShares returns who has access to a node.
func (s *Server) handleListNodeShares(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	shares, err := s.sharing.ListForNode(r.Context(), id, user.ID)
	if err != nil {
		WriteError(w, r, translateSharingError(err))
		return
	}

	WriteJSON(w, r, http.StatusOK, map[string]any{"shares": shares})
}

// handleRevokeShare removes a share.
func (s *Server) handleRevokeShare(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	if err := s.sharing.Revoke(r.Context(), id, user.ID); err != nil {
		WriteError(w, r, translateSharingError(err))
		return
	}

	s.audit(r, audit.ActionShareRevoke, audit.Success,
		auditDetail(map[string]any{"share_id": id.String()}))

	WriteNoContent(w)
}

// handleSharedWithMe lists everything shared with the caller.
func (s *Server) handleSharedWithMe(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	shares, err := s.sharing.SharedWithMe(r.Context(), user.ID)
	if err != nil {
		WriteError(w, r, translateSharingError(err))
		return
	}

	WriteJSON(w, r, http.StatusOK, map[string]any{"shares": shares})
}

// handleSharingVocabulary publishes the permission and role names, so the
// interface does not hardcode a list that could drift from the server's.
func (s *Server) handleSharingVocabulary(w http.ResponseWriter, r *http.Request) {
	type roleInfo struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
		Description string   `json:"description"`
	}

	descriptions := map[string]string{
		"viewer":      "Can open and download, but not change anything.",
		"contributor": "Can add new files and folders, but not change or remove what is already there.",
		"editor":      "Can add, change, and remove content, but cannot share it with anyone else.",
		"manager":     "Can do everything an editor can, and also share it and manage who has access.",
	}

	roles := make([]roleInfo, 0, 4)
	for _, name := range permissions.RoleNames() {
		set := permissions.MustParse(name)
		roles = append(roles, roleInfo{
			Name:        name,
			Permissions: set.Names(),
			Description: descriptions[name],
		})
	}

	WriteJSON(w, r, http.StatusOK, map[string]any{
		"permissions": permissions.AllNames(),
		"roles":       roles,
		"enabled":     s.sharing.Enabled(),
	})
}

// translateSharingError maps domain errors to HTTP responses.
func translateSharingError(err error) error {
	switch {
	case errors.Is(err, sharing.ErrNotFound):
		return ErrNotFound()
	case errors.Is(err, sharing.ErrDisabled):
		return ErrForbidden("Sharing is switched off on this server.")
	case errors.Is(err, sharing.ErrCannotShareRoot):
		return ErrBadRequest("Your top-level folder cannot be shared. Share a folder inside it instead.")
	case errors.Is(err, sharing.ErrCannotDenyOwner):
		return ErrBadRequest(strings.TrimPrefix(err.Error(), "sharing: "))
	case errors.Is(err, sharing.ErrCannotShareWithOwner):
		return ErrConflict("That person already has access to this item.")
	case errors.Is(err, sharing.ErrSubjectNotFound):
		// A uniform message: probing this endpoint must not reveal which
		// usernames exist.
		return ErrValidation("No user or group with that name was found.").
			WithField("username", "Check the spelling and try again.")
	case errors.Is(err, sharing.ErrNotPermitted):
		return ErrForbidden(err.Error())
	case errors.Is(err, permissions.ErrNotFound):
		return ErrNotFound()
	default:
		return ErrInternal(err)
	}
}

// shareRecipient names who a share went to, for the audit trail.
func shareRecipient(share *sharing.Share) string {
	switch {
	case share.User != nil:
		return share.User.Username
	case share.Group != nil:
		return share.Group.Name
	default:
		return ""
	}
}
