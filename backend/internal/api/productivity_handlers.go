package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
	"github.com/joshuanmoses/piCloud/backend/internal/previews"
	"github.com/joshuanmoses/piCloud/backend/internal/recent"
	"github.com/joshuanmoses/piCloud/backend/internal/tags"
)

// Favourites, recent activity, and tags.
//
// All three are personal metadata: they belong to the caller, not to the file.
// Favouriting a shared folder, or tagging a colleague's document, is invisible
// to everyone else — which is what people expect from a personal bookmark and
// is also what keeps these endpoints from becoming a side channel for saying
// things to other users.

// --- Favourites -----------------------------------------------------------

// handleListFavorites returns the caller's bookmarks.
//
// Items the caller can no longer see are excluded by the store's query. A
// favourite on a folder that was later unshared simply stops appearing, rather
// than sitting in the list as a link that 404s.
func (s *Server) handleListFavorites(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())
	query := r.URL.Query()
	limit := parseIntDefault(query.Get("limit"), nodes.DefaultPageSize)
	offset := parseIntDefault(query.Get("offset"), 0)

	list, total, err := s.favorites.List(r.Context(), user.ID, limit, offset)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	items, err := s.decorateNodes(r, list)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	for i := range items {
		items[i].Favorite = true
	}

	WriteJSON(w, r, http.StatusOK, map[string]any{
		"nodes":    items,
		"total":    total,
		"limit":    limit,
		"offset":   offset,
		"has_more": offset+len(items) < total,
	})
}

// handleAddFavorite bookmarks a node.
//
// Read permission is the requirement: if you can see it, you may bookmark it.
// Nothing about the node changes, so no stronger right is needed.
func (s *Server) handleAddFavorite(w http.ResponseWriter, r *http.Request) {
	node, apiErr := s.resolveForPersonalMetadata(r)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	if err := s.favorites.Add(r.Context(), MustUser(r.Context()).ID, node.ID); err != nil {
		if errors.Is(err, nodes.ErrNotFound) {
			WriteError(w, r, ErrNotFound())
			return
		}
		WriteError(w, r, ErrInternal(err))
		return
	}
	WriteJSON(w, r, http.StatusOK, map[string]any{"favorite": true})
}

// handleRemoveFavorite removes a bookmark.
//
// Unlike adding, this does not require the node to still be visible. Somebody
// whose access was revoked must still be able to clear their own list.
func (s *Server) handleRemoveFavorite(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	if err := s.favorites.Remove(r.Context(), MustUser(r.Context()).ID, id); err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}
	WriteJSON(w, r, http.StatusOK, map[string]any{"favorite": false})
}

// --- Recent ---------------------------------------------------------------

// handleListRecent returns what the caller last touched.
func (s *Server) handleListRecent(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())
	query := r.URL.Query()
	limit := parseIntDefault(query.Get("limit"), nodes.DefaultPageSize)
	offset := parseIntDefault(query.Get("offset"), 0)

	entries, total, err := s.recent.List(r.Context(), user.ID, limit, offset)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	list := make([]*nodes.Node, 0, len(entries))
	for _, e := range entries {
		list = append(list, e.Node)
	}
	decorated, err := s.decorateNodes(r, list)
	if err != nil {
		WriteError(w, r, err)
		return
	}

	type recentItem struct {
		nodeResponse
		Action     string `json:"action"`
		OccurredAt string `json:"occurred_at"`
	}

	items := make([]recentItem, 0, len(entries))
	for i, e := range entries {
		if i >= len(decorated) {
			break
		}
		items = append(items, recentItem{
			nodeResponse: decorated[i],
			Action:       string(e.Action),
			OccurredAt:   e.OccurredAt.UTC().Format(timeFormat),
		})
	}

	WriteJSON(w, r, http.StatusOK, map[string]any{
		"items":    items,
		"total":    total,
		"limit":    limit,
		"offset":   offset,
		"has_more": offset+len(items) < total,
	})
}

// recordActivity notes that the caller touched a node.
//
// Deliberately best-effort and never on the response path's critical route: a
// download that succeeded must not be reported as failed because the history
// could not be written. Failures are logged for the operator and swallowed for
// the user.
func (s *Server) recordActivity(r *http.Request, nodeID uuid.UUID, action recent.Action) {
	if s.recent == nil {
		return
	}
	user := UserFromContext(r.Context())
	if user == nil {
		return
	}
	if err := s.recent.Record(r.Context(), user.ID, nodeID, action); err != nil {
		s.logger.Warn("cannot record recent activity",
			"user_id", user.ID, "node_id", nodeID, "action", action, "error", err)
	}
}

// --- Tags -----------------------------------------------------------------

// handleListTags returns the caller's tags with usage counts.
func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	list, err := s.tags.List(r.Context(), MustUser(r.Context()).ID)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}
	WriteJSON(w, r, http.StatusOK, map[string]any{"tags": list})
}

type tagRequest struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// handleCreateTag makes a new tag.
func (s *Server) handleCreateTag(w http.ResponseWriter, r *http.Request) {
	var req tagRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}

	tag, err := s.tags.Create(r.Context(), MustUser(r.Context()).ID, req.Name, req.Color)
	if err != nil {
		WriteError(w, r, translateTagError(err))
		return
	}
	WriteJSON(w, r, http.StatusCreated, tag)
}

// handleUpdateTag renames or recolours a tag.
func (s *Server) handleUpdateTag(w http.ResponseWriter, r *http.Request) {
	tagID, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	var req tagRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}

	tag, err := s.tags.Update(r.Context(), MustUser(r.Context()).ID, tagID, req.Name, req.Color)
	if err != nil {
		WriteError(w, r, translateTagError(err))
		return
	}
	WriteJSON(w, r, http.StatusOK, tag)
}

// handleDeleteTag removes a tag and every assignment of it.
func (s *Server) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	tagID, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	if err := s.tags.Delete(r.Context(), MustUser(r.Context()).ID, tagID); err != nil {
		WriteError(w, r, translateTagError(err))
		return
	}
	WriteNoContent(w)
}

// handleListNodeTags returns the caller's tags on one node.
func (s *Server) handleListNodeTags(w http.ResponseWriter, r *http.Request) {
	node, apiErr := s.resolveForPersonalMetadata(r)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	list, err := s.tags.ForNode(r.Context(), MustUser(r.Context()).ID, node.ID)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}
	WriteJSON(w, r, http.StatusOK, map[string]any{"tags": list})
}

// handleApplyTag attaches one of the caller's tags to a node.
func (s *Server) handleApplyTag(w http.ResponseWriter, r *http.Request) {
	node, apiErr := s.resolveForPersonalMetadata(r)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}
	tagID, err := uuid.Parse(chiURLParam(r, "tagID"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	if err := s.tags.Apply(r.Context(), MustUser(r.Context()).ID, node.ID, tagID); err != nil {
		WriteError(w, r, translateTagError(err))
		return
	}
	WriteNoContent(w)
}

// handleRemoveTag detaches a tag from a node.
func (s *Server) handleRemoveTag(w http.ResponseWriter, r *http.Request) {
	nodeID, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}
	tagID, err := uuid.Parse(chiURLParam(r, "tagID"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	// No visibility check, for the same reason as unfavouriting: clearing your
	// own metadata must keep working after access is revoked.
	if err := s.tags.Remove(r.Context(), MustUser(r.Context()).ID, nodeID, tagID); err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}
	WriteNoContent(w)
}

// translateTagError maps domain errors to HTTP responses.
//
// The messages are the domain's own, minus the package prefix: they are written
// for a person, they describe the caller's own tags, and they disclose nothing
// about anybody else.
func translateTagError(err error) error {
	switch {
	case errors.Is(err, tags.ErrNotFound):
		return ErrNotFound()
	case errors.Is(err, tags.ErrNameTaken):
		return ErrConflict(strings.TrimPrefix(err.Error(), "tags: "))
	case errors.Is(err, tags.ErrInvalidName), errors.Is(err, tags.ErrInvalidColor):
		return ErrValidation(strings.TrimPrefix(err.Error(), "tags: "))
	case errors.Is(err, tags.ErrTooMany):
		return ErrConflict(strings.TrimPrefix(err.Error(), "tags: "))
	case errors.Is(err, nodes.ErrNotFound):
		return ErrNotFound()
	default:
		return ErrInternal(err)
	}
}

// --- Shared helpers -------------------------------------------------------

// resolveForPersonalMetadata loads the node named by {id}, requiring only that
// the caller can see it.
func (s *Server) resolveForPersonalMetadata(r *http.Request) (*nodes.Node, error) {
	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		return nil, ErrNotFound()
	}
	result, apiErr := s.requireAccess(r, id, permissions.Read, false)
	if apiErr != nil {
		return nil, apiErr
	}
	return result.node, nil
}

// permissionRead is a small indirection so search_handlers.go does not need to
// import the permissions package for one constant.
func permissionRead() permissions.Set { return permissions.Read }

// decorateNodes turns domain nodes into responses with owners, effective
// permissions, favourite state, and preview availability filled in.
//
// Everything is batched. A 100-item listing that issued four queries per row
// would be the slowest page in the application on a Raspberry Pi.
func (s *Server) decorateNodes(r *http.Request, list []*nodes.Node) ([]nodeResponse, error) {
	user := MustUser(r.Context())

	owners, err := s.ownersFor(r, list)
	if err != nil {
		return nil, err
	}

	ids := make([]uuid.UUID, 0, len(list))
	for _, n := range list {
		ids = append(ids, n.ID)
	}

	decisions, err := s.sharing.Permissions().EvaluateMany(r.Context(), ids, user.ID)
	if err != nil {
		return nil, ErrInternal(err)
	}

	favorited := map[uuid.UUID]bool{}
	if s.favorites != nil {
		favorited, err = s.favorites.Which(r.Context(), user.ID, ids)
		if err != nil {
			return nil, ErrInternal(err)
		}
	}

	previewed := map[uuid.UUID]bool{}
	if s.previews != nil {
		records, err := s.previews.Store().Which(r.Context(), ids)
		if err != nil {
			return nil, ErrInternal(err)
		}
		for id, rec := range records {
			previewed[id] = rec.Status == previews.StatusReady
		}
	}

	versionCounts := map[uuid.UUID]int{}
	if s.versionsEnabled() {
		versionCounts, err = s.versions.Store().CountForNodes(r.Context(), ids)
		if err != nil {
			return nil, ErrInternal(err)
		}
	}

	// Whether each item is shared *out*. Two sources, because an owner can
	// expose something either way: a grant to a person or group, or a public
	// link. Both are one query for the whole page.
	sharedOut := map[uuid.UUID]bool{}
	if s.sharing != nil {
		sharedOut, err = s.sharing.Permissions().SharedNodes(r.Context(), ids)
		if err != nil {
			return nil, ErrInternal(err)
		}
	}
	if s.publicLinksEnabled() {
		linked, err := s.publicLinks.Store().LinkedNodes(r.Context(), ids)
		if err != nil {
			return nil, ErrInternal(err)
		}
		for id := range linked {
			sharedOut[id] = true
		}
	}

	byID := make(map[uuid.UUID][]*tags.Tag)
	if s.tags != nil {
		applied, err := s.tags.ForNodes(r.Context(), user.ID, ids)
		if err != nil {
			return nil, ErrInternal(err)
		}
		for id, list := range applied {
			byID[id] = list
		}
	}

	out := make([]nodeResponse, 0, len(list))
	for _, n := range list {
		item := toNodeResponse(n, owners[n.OwnerID], user.ID)
		if decision, ok := decisions[n.ID]; ok {
			item.Permissions = decision.Effective.Names()
			item.IsOwner = decision.IsOwner
			item.Shared = !decision.IsOwner
		}
		// Only the owner is told an item is shared out. To anybody else the
		// existence of other recipients is not their business, and a viewer
		// who could see it would learn something about the owner's sharing
		// that no permission grants them.
		item.SharedOut = item.IsOwner && sharedOut[n.ID]
		item.Favorite = favorited[n.ID]
		item.HasPreview = previewed[n.ID]
		item.Tags = byID[n.ID]
		item.VersionCount = versionCounts[n.ID]
		out = append(out, item)
	}
	return out, nil
}
