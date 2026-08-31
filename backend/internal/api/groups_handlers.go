package api

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/groups"
	"github.com/joshuanmoses/piCloud/backend/internal/users"
)

// Group handlers.
//
// Creating and deleting groups is an administrator action: a group is a
// building block of access control across the whole instance, and letting any
// user mint them would make the sharing dialog unusable and the audit trail
// meaningless.
//
// Membership is looser. A group manager — set per group, not instance-wide —
// can add and remove members without being an administrator, so a team lead
// can run their own team.

// handleListGroups returns groups matching a search term.
//
// Available to any signed-in user, because sharing with a group requires being
// able to find it. Only names, descriptions, and member counts are returned;
// membership itself is not, so this cannot be used to map who works with whom.
func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	list, err := s.groups.List(r.Context(), r.URL.Query().Get("search"), 100)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}
	if list == nil {
		list = []*groups.Group{}
	}
	WriteJSON(w, r, http.StatusOK, map[string]any{"groups": list})
}

// handleListMyGroups returns the groups the caller belongs to.
func (s *Server) handleListMyGroups(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	list, err := s.groups.ListForUser(r.Context(), user.ID)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}
	if list == nil {
		list = []*groups.Group{}
	}
	WriteJSON(w, r, http.StatusOK, map[string]any{"groups": list})
}

type createGroupRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// handleCreateGroup creates a group. Administrators only.
func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var req createGroupRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}

	group, err := s.groups.Create(r.Context(), req.Name, req.Description)
	if err != nil {
		WriteError(w, r, translateGroupError(err))
		return
	}

	s.logger.Info("group created",
		"group_id", group.ID, "name", group.Name, "actor_id", MustUser(r.Context()).ID)

	WriteJSON(w, r, http.StatusCreated, map[string]any{"group": group})
}

// handleGetGroup returns one group with its members.
//
// Membership is visible to the group's own members and to administrators.
// Anyone else gets a uniform 404: who belongs to a team is not public
// information on a shared server.
func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	group, err := s.groups.Get(r.Context(), id)
	if err != nil {
		WriteError(w, r, translateGroupError(err))
		return
	}

	if !user.IsAdmin {
		member, err := s.groups.IsMember(r.Context(), id, user.ID)
		if err != nil {
			WriteError(w, r, ErrInternal(err))
			return
		}
		if !member {
			WriteError(w, r, ErrNotFound())
			return
		}
	}

	members, err := s.groups.ListMembers(r.Context(), id)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}
	if members == nil {
		members = []groups.Member{}
	}

	WriteJSON(w, r, http.StatusOK, map[string]any{"group": group, "members": members})
}

type updateGroupRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

// handleUpdateGroup renames a group or changes its description.
func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	var req updateGroupRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}

	group, err := s.groups.Update(r.Context(), id, req.Name, req.Description)
	if err != nil {
		WriteError(w, r, translateGroupError(err))
		return
	}
	WriteJSON(w, r, http.StatusOK, map[string]any{"group": group})
}

// handleDeleteGroup removes a group.
//
// Its grants go with it, through a database trigger: access must never survive
// the group it was given to.
func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	if err := s.groups.Delete(r.Context(), id); err != nil {
		WriteError(w, r, translateGroupError(err))
		return
	}

	s.logger.Warn("group deleted; its access grants were removed with it",
		"group_id", id, "actor_id", MustUser(r.Context()).ID)

	WriteNoContent(w)
}

type addMemberRequest struct {
	Username string `json:"username,omitempty"`
	UserID   string `json:"user_id,omitempty"`
	Role     string `json:"role,omitempty"`
}

// handleAddGroupMember adds a user to a group.
func (s *Server) handleAddGroupMember(w http.ResponseWriter, r *http.Request) {
	actor := MustUser(r.Context())

	groupID, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	if apiErr := s.requireGroupControl(r, groupID); apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	var req addMemberRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}

	target, apiErr := s.resolveUser(r, req.UserID, req.Username)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	role := groups.Role(req.Role)
	if req.Role == "" {
		role = groups.RoleMember
	}
	if !role.Valid() {
		WriteError(w, r, ErrValidation("That is not a valid role.").
			WithField("role", `Use "member" or "manager".`))
		return
	}

	if err := s.groups.AddMember(r.Context(), groupID, target.ID, role, actor.ID); err != nil {
		WriteError(w, r, translateGroupError(err))
		return
	}

	s.logger.Info("group member added",
		"group_id", groupID, "user_id", target.ID, "role", role, "actor_id", actor.ID)

	WriteNoContent(w)
}

// handleRemoveGroupMember removes a user from a group.
func (s *Server) handleRemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	actor := MustUser(r.Context())

	groupID, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}
	userID, err := uuid.Parse(chiURLParam(r, "userID"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	// Leaving a group yourself needs no special standing.
	if userID != actor.ID {
		if apiErr := s.requireGroupControl(r, groupID); apiErr != nil {
			WriteError(w, r, apiErr)
			return
		}
	}

	if err := s.groups.RemoveMember(r.Context(), groupID, userID); err != nil {
		WriteError(w, r, translateGroupError(err))
		return
	}

	s.logger.Info("group member removed",
		"group_id", groupID, "user_id", userID, "actor_id", actor.ID)

	WriteNoContent(w)
}

// requireGroupControl verifies the caller may change a group's membership:
// either an administrator, or a manager of that specific group.
func (s *Server) requireGroupControl(r *http.Request, groupID uuid.UUID) error {
	user := MustUser(r.Context())
	if user.IsAdmin {
		return nil
	}

	isManager, err := s.groups.IsManager(r.Context(), groupID, user.ID)
	if err != nil {
		return ErrInternal(err)
	}
	if !isManager {
		// 404 rather than 403: a caller who is not in the group should not
		// learn whether it exists.
		return ErrNotFound()
	}
	return nil
}

// resolveUser finds a user by identifier or username.
func (s *Server) resolveUser(r *http.Request, rawID, username string) (*users.User, error) {
	if rawID != "" {
		id, err := uuid.Parse(rawID)
		if err != nil {
			return nil, ErrValidation("That is not a valid user.").WithField("user_id", "Invalid identifier.")
		}
		user, err := s.userStore.GetByID(r.Context(), id)
		if err != nil {
			return nil, ErrValidation("No such user.").WithField("user_id", "No user with that identifier.")
		}
		return user, nil
	}

	if username == "" {
		return nil, ErrValidation("Name the user to add.").WithField("username", "Enter a username.")
	}

	user, err := s.userStore.GetByUsername(r.Context(), username)
	if err != nil {
		return nil, ErrValidation("No such user.").WithField("username", "No user with that name.")
	}
	return user, nil
}

// translateGroupError maps domain errors to HTTP responses.
func translateGroupError(err error) error {
	var validationErr *groups.ValidationError
	if errors.As(err, &validationErr) {
		return ErrValidation("Check the details you entered.").
			WithField(validationErr.Field, validationErr.Reason)
	}

	switch {
	case errors.Is(err, groups.ErrNotFound):
		return ErrNotFound()
	case errors.Is(err, groups.ErrNameTaken):
		return ErrConflict("A group with that name already exists.")
	case errors.Is(err, groups.ErrNotMember):
		return ErrNotFound()
	default:
		return ErrInternal(err)
	}
}
