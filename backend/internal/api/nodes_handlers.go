package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/audit"
	"github.com/joshuanmoses/piCloud/backend/internal/files"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
	"github.com/joshuanmoses/piCloud/backend/internal/recent"
	"github.com/joshuanmoses/piCloud/backend/internal/tags"
	"github.com/joshuanmoses/piCloud/backend/internal/users"
)

// Node handlers.
//
// Every handler routes its access check through requireAccess, which evaluates
// the full permission model: ownership, direct grants, grants inherited from
// ancestor folders, grants held through group membership, and explicit denials.
//
// The rule these handlers follow without exception: a node the caller may not
// see returns 404, never 403. A distinguishable "forbidden" would confirm that
// a guessed UUID names a real object, which is an enumeration oracle.
//
// The one deliberate exception is when the caller can already *see* the node
// but lacks the specific permission for what they asked to do. Returning 404
// there would be actively confusing — the item is visible in their listing —
// so that case returns 403 with a message naming the missing permission.

// nodeResponse is a node as returned to a client.
//
// It is a distinct type rather than the domain struct so that adding a field to
// nodes.Node cannot silently publish it. content_hash in particular is never
// exposed: it would let a client probe whether specific content exists.
type nodeResponse struct {
	ID       uuid.UUID  `json:"id"`
	ParentID *uuid.UUID `json:"parent_id"`
	Kind     string     `json:"kind"`
	Name     string     `json:"name"`

	SizeBytes int64  `json:"size_bytes"`
	MimeType  string `json:"mime_type,omitempty"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	DeletedAt string `json:"deleted_at,omitempty"`

	// Owner is included so a shared listing can show who owns each item.
	Owner *users.Public `json:"owner,omitempty"`

	// IsRoot marks the folder the interface labels "My Files".
	IsRoot bool `json:"is_root"`

	// Permissions is what the caller may do to this item. The interface uses
	// it to decide which actions to offer; it is a usability affordance, and
	// the server re-checks on every request regardless.
	Permissions []string `json:"permissions,omitempty"`
	// IsOwner is true when the caller owns the item.
	IsOwner bool `json:"is_owner"`
	// Shared is true when the caller reaches this item through a grant rather
	// than by owning it.
	Shared bool `json:"shared"`
	// SharedOut is true when this item has been shared with somebody else or
	// published as a link. It is the owner's side of the same fact: without
	// it a listing can only say "not shared with me", which for an owner is
	// every row, and an interface reading that as "private" would tell people
	// an item they published is not published. Like Favorite and Tags it is
	// filled in only by the listings that batch-load it.
	SharedOut bool `json:"shared_out,omitempty"`

	// Favorite, HasPreview, and Tags are the caller's own view of the item.
	// They are populated only by the listings that batch-load them; a handler
	// that returns a bare node leaves them at their zero values rather than
	// issuing a query per row.
	Favorite   bool        `json:"favorite,omitempty"`
	HasPreview bool        `json:"has_preview,omitempty"`
	Tags       []*tags.Tag `json:"tags,omitempty"`
	// VersionCount is how many earlier states this file has. Zero is omitted,
	// so a listing on a server with history switched off is byte-identical to
	// one from before Phase 8.
	VersionCount int `json:"version_count,omitempty"`
}

// toNodeResponse converts a node for the caller identified by callerID.
//
// The caller is a parameter rather than something the listings patch in
// afterwards, because is_owner and shared are statements about *this* caller
// and a response that omitted them would silently claim "not the owner" — the
// zero value of a bool with no omitempty. A handler returning a node the caller
// just created would then tell them they do not own it.
func toNodeResponse(n *nodes.Node, owner *users.Public, callerID uuid.UUID) nodeResponse {
	r := nodeResponse{
		ID:        n.ID,
		ParentID:  n.ParentID,
		Kind:      string(n.Kind),
		Name:      n.Name,
		SizeBytes: n.SizeBytes,
		MimeType:  n.MimeType,
		CreatedAt: n.CreatedAt.UTC().Format(timeFormat),
		UpdatedAt: n.UpdatedAt.UTC().Format(timeFormat),
		Owner:     owner,
		IsRoot:    n.IsRoot(),
		IsOwner:   n.OwnerID == callerID,
		Shared:    n.OwnerID != callerID,
	}
	if n.DeletedAt != nil {
		r.DeletedAt = n.DeletedAt.UTC().Format(timeFormat)
	}
	return r
}

const timeFormat = "2006-01-02T15:04:05.000Z"

// access is a node together with what the caller may do to it.
type access struct {
	node     *nodes.Node
	decision permissions.Decision
}

// requireAccess loads a node and verifies the caller holds the given
// permissions on it.
//
// This is the single authorization chokepoint for content. Every handler calls
// it; none performs its own check.
//
// An administrator is deliberately NOT granted blanket access to other
// people's files. Administering a server is not the same as being entitled to
// read what people store on it, and the audit log would have no way to
// distinguish legitimate support from snooping. Administrators manage quotas
// and accounts, not contents.
func (s *Server) requireAccess(
	r *http.Request, id uuid.UUID, want permissions.Set, includeDeleted bool,
) (*access, error) {
	user := MustUser(r.Context())

	var node *nodes.Node
	var err error
	if includeDeleted {
		node, err = s.files.Nodes().GetIncludingDeleted(r.Context(), id)
	} else {
		node, err = s.files.Nodes().Get(r.Context(), id)
	}
	if err != nil {
		if errors.Is(err, nodes.ErrNotFound) {
			return nil, ErrNotFound()
		}
		return nil, ErrInternal(err)
	}

	decision, err := s.sharing.Permissions().Evaluate(r.Context(), id, user.ID)
	if err != nil {
		return nil, ErrInternal(err)
	}

	// Cannot even see it: indistinguishable from not existing.
	if !decision.Allows(permissions.Read) {
		s.logger.Warn("denied access to a node the caller cannot see",
			"user_id", user.ID, "node_id", id, "owner_id", node.OwnerID,
			"method", r.Method, "path", r.URL.Path,
			"ip", ClientIPFromContext(r.Context()))
		return nil, ErrNotFound()
	}

	// Can see it, but not do this. Saying so is clearer than a 404 for an item
	// the user is looking at, and reveals nothing they did not already know.
	if !decision.Allows(want) {
		missing := want &^ decision.Effective
		s.logger.Info("denied an operation on a visible node",
			"user_id", user.ID, "node_id", id,
			"missing", missing.String(), "method", r.Method, "path", r.URL.Path)
		return nil, ErrForbidden(fmt.Sprintf(
			"You do not have permission to do that. This requires: %s.",
			strings.Join(missing.Names(), ", ")))
	}

	return &access{node: node, decision: decision}, nil
}

// resolveFolder loads a folder the caller holds the given permissions on,
// treating the literal "root" as the caller's own root folder.
func (s *Server) resolveFolder(r *http.Request, raw string, want permissions.Set) (*nodes.Node, error) {
	access, err := s.resolveFolderAccess(r, raw, want)
	if err != nil {
		return nil, err
	}
	return access.node, nil
}

// resolveFolderAccess is resolveFolder, keeping the permission decision.
//
// A listing needs both the folder and what the caller may do to it. Calling
// Evaluate again afterwards — which is what the listing used to do — was a
// second recursive ancestry query per request for an answer already computed,
// and it showed up as about 8% of a folder listing's fixed cost.
func (s *Server) resolveFolderAccess(
	r *http.Request, raw string, want permissions.Set,
) (*access, error) {
	user := MustUser(r.Context())

	if raw == "" || raw == "root" {
		// A user always has full rights over their own root, so there is
		// nothing to evaluate: the answer is known without asking.
		root, err := s.files.Nodes().Root(r.Context(), user.ID)
		if err != nil {
			return nil, ErrInternal(err)
		}
		return &access{
			node:     root,
			decision: permissions.Decision{Effective: permissions.All, IsOwner: true},
		}, nil
	}

	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, ErrNotFound()
	}

	result, apiErr := s.requireAccess(r, id, want, false)
	if apiErr != nil {
		return nil, apiErr
	}
	if !result.node.IsFolder() {
		return nil, ErrBadRequest("That is a file, not a folder.")
	}
	return result, nil
}

// visibleBreadcrumbs returns the path to a node, truncated to the ancestors the
// caller may actually see.
//
// Walking the full ancestry would do two wrong things at once for a shared
// folder: it would disclose the names of folders the caller has no access to,
// and it would label somebody else's root "My Files" — implying the item lives
// in the caller's own storage when it does not.
func (s *Server) visibleBreadcrumbs(r *http.Request, nodeID uuid.UUID) ([]nodes.Breadcrumb, error) {
	user := MustUser(r.Context())

	full, err := s.files.Nodes().Breadcrumbs(r.Context(), nodeID)
	if err != nil {
		return nil, ErrInternal(err)
	}
	if len(full) == 0 {
		return full, nil
	}

	ids := make([]uuid.UUID, 0, len(full))
	for _, crumb := range full {
		ids = append(ids, crumb.ID)
	}
	decisions, err := s.sharing.Permissions().EvaluateMany(r.Context(), ids, user.ID)
	if err != nil {
		return nil, ErrInternal(err)
	}

	// The path is root-first. Keep the longest visible suffix: everything from
	// the highest ancestor the caller can read down to the node itself.
	start := 0
	for i, crumb := range full {
		decision, ok := decisions[crumb.ID]
		if !ok || !decision.Allows(permissions.Read) {
			start = i + 1
		}
	}
	if start >= len(full) {
		// Should not happen — the caller can read the node itself — but a
		// single crumb is a safe answer.
		return full[len(full)-1:], nil
	}

	visible := make([]nodes.Breadcrumb, len(full[start:]))
	copy(visible, full[start:])

	// Only the caller's own root is "My Files". Somebody else's root reached
	// through a share is not, and is dropped above anyway; this guards the
	// case where a shared folder happens to be a root the caller can read.
	for i := range visible {
		if visible[i].IsRoot && start > 0 {
			visible[i].IsRoot = false
		}
	}
	return visible, nil
}

// ownerOf returns the public profile of a node's owner.
//
// A shared listing shows items belonging to several people, so the owner
// cannot be assumed to be the caller.
func (s *Server) ownerOf(r *http.Request, node *nodes.Node) (*users.Public, error) {
	caller := MustUser(r.Context())
	if node.OwnerID == caller.ID {
		public := caller.Public()
		return &public, nil
	}

	owner, err := s.userStore.GetByID(r.Context(), node.OwnerID)
	if err != nil {
		// The owner having vanished must not break the listing.
		return nil, nil
	}
	public := owner.Public()
	return &public, nil
}

// --- Listing ----------------------------------------------------------------

// handleListNodes returns the children of a folder.
func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	folderAccess, err := s.resolveFolderAccess(r, r.URL.Query().Get("parent_id"), permissions.Read)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	folder := folderAccess.node

	query := r.URL.Query()
	limit := parseIntDefault(query.Get("limit"), nodes.DefaultPageSize)
	offset := parseIntDefault(query.Get("offset"), 0)

	sort := nodes.SortField(query.Get("sort"))
	if !sort.Valid() {
		sort = nodes.SortByName
	}

	var kindFilter nodes.Kind
	if k := nodes.Kind(query.Get("kind")); k.Valid() {
		kindFilter = k
	}

	page, err := s.files.Nodes().List(r.Context(), nodes.ListParams{
		ParentID:   folder.ID,
		Limit:      limit,
		Offset:     offset,
		Sort:       sort,
		Descending: strings.EqualFold(query.Get("order"), "desc"),
		KindFilter: kindFilter,
	})
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	breadcrumbs, err := s.visibleBreadcrumbs(r, folder.ID)
	if err != nil {
		WriteError(w, r, err)
		return
	}

	user := MustUser(r.Context())

	// Owners, effective permissions, favourite state, preview availability and
	// the caller's tags — all batched. The interface uses the permissions to
	// decide which actions to offer; the server checks again on every request
	// regardless.
	items, err := s.decorateNodes(r, page.Nodes)
	if err != nil {
		WriteError(w, r, err)
		return
	}

	owners, err := s.ownersFor(r, []*nodes.Node{folder})
	if err != nil {
		WriteError(w, r, err)
		return
	}

	// The decision came back with the folder; evaluating it again here would
	// be a second recursive ancestry query for an answer already in hand.
	folderResponse := toNodeResponse(folder, owners[folder.OwnerID], user.ID)
	folderResponse.Permissions = folderAccess.decision.Effective.Names()
	folderResponse.IsOwner = folderAccess.decision.IsOwner

	WriteJSON(w, r, http.StatusOK, map[string]any{
		"folder":      folderResponse,
		"breadcrumbs": breadcrumbs,
		"nodes":       items,
		"total":       page.Total,
		"has_more":    page.HasMore,
		"limit":       limit,
		"offset":      offset,
	})
}

// ownersFor batch-loads the public profiles of a set of nodes' owners.
func (s *Server) ownersFor(r *http.Request, list []*nodes.Node) (map[uuid.UUID]*users.Public, error) {
	caller := MustUser(r.Context())
	callerPublic := caller.Public()

	result := map[uuid.UUID]*users.Public{caller.ID: &callerPublic}

	missing := make([]uuid.UUID, 0)
	for _, n := range list {
		if n == nil {
			continue
		}
		if _, known := result[n.OwnerID]; !known {
			result[n.OwnerID] = nil
			missing = append(missing, n.OwnerID)
		}
	}
	if len(missing) == 0 {
		return result, nil
	}

	found, err := s.userStore.GetManyPublic(r.Context(), missing)
	if err != nil {
		return nil, ErrInternal(err)
	}
	for id, public := range found {
		copied := public
		result[id] = &copied
	}
	return result, nil
}

// handleGetNode returns one node's metadata.
func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	result, apiErr := s.requireAccess(r, id, permissions.Read, false)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}
	node := result.node

	owner, err := s.ownerOf(r, node)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	response := map[string]any{
		"node":        toNodeResponse(node, owner, MustUser(r.Context()).ID),
		"permissions": result.decision.Effective.Names(),
		"is_owner":    result.decision.IsOwner,
	}

	// A folder's size is computed on demand rather than stored, so it can
	// never drift from the truth.
	if node.IsFolder() {
		size, fileCount, err := s.files.Nodes().SubtreeSize(r.Context(), node.ID)
		if err != nil {
			WriteError(w, r, ErrInternal(err))
			return
		}
		response["subtree_size_bytes"] = size
		response["subtree_file_count"] = fileCount
	}

	breadcrumbs, err := s.visibleBreadcrumbs(r, node.ID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	response["breadcrumbs"] = breadcrumbs

	// Personal metadata, batched with everything else the detail view needs.
	if s.favorites != nil {
		favorite, err := s.favorites.IsFavorite(r.Context(), MustUser(r.Context()).ID, node.ID)
		if err != nil {
			WriteError(w, r, ErrInternal(err))
			return
		}
		response["favorite"] = favorite
	}
	if s.tags != nil {
		applied, err := s.tags.ForNode(r.Context(), MustUser(r.Context()).ID, node.ID)
		if err != nil {
			WriteError(w, r, ErrInternal(err))
			return
		}
		response["tags"] = applied
	}
	if s.versionsEnabled() && node.IsFile() {
		count, err := s.versions.Store().CountForNode(r.Context(), node.ID)
		if err != nil {
			WriteError(w, r, ErrInternal(err))
			return
		}
		response["version_count"] = count
	}

	// Opening a file's detail view is a view; opening a folder is navigation,
	// and recording every folder a person walks through would bury the files
	// they actually cared about.
	if node.IsFile() {
		s.recordActivity(r, node.ID, recent.ActionViewed)
	}

	WriteJSON(w, r, http.StatusOK, response)
}

// --- Create -----------------------------------------------------------------

type createFolderRequest struct {
	ParentID string `json:"parent_id"`
	Name     string `json:"name"`
}

// handleCreateFolder creates a folder.
func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var req createFolderRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}

	parent, apiErr := s.resolveFolder(r, req.ParentID, permissions.Create)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	user := MustUser(r.Context())
	// A folder created inside somebody else's shared folder belongs to that
	// folder's owner, not to whoever created it. Otherwise the owner would
	// lose control of their own tree, and the contents would count against
	// the wrong person's quota.
	folder, err := s.files.Nodes().CreateFolder(r.Context(), nodes.CreateFolderParams{
		OwnerID:  parent.OwnerID,
		ParentID: parent.ID,
		Name:     req.Name,
		ActorID:  user.ID,
	})
	if err != nil {
		WriteError(w, r, translateNodeError(err))
		return
	}

	owner := user.Public()
	WriteJSON(w, r, http.StatusCreated, map[string]any{"node": toNodeResponse(folder, &owner, user.ID)})
}

// --- Update -----------------------------------------------------------------

type updateNodeRequest struct {
	// Name renames the node.
	Name *string `json:"name,omitempty"`
	// ParentID moves the node.
	ParentID *string `json:"parent_id,omitempty"`
}

// handleUpdateNode renames or moves a node.
func (s *Server) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	var req updateNodeRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}
	if req.Name == nil && req.ParentID == nil {
		WriteError(w, r, ErrBadRequest("Provide a new name, a new parent folder, or both."))
		return
	}

	// The permission required depends on what is being changed.
	want := permissions.None
	if req.Name != nil {
		want |= permissions.Rename
	}
	if req.ParentID != nil {
		want |= permissions.Move
	}

	result, apiErr := s.requireAccess(r, id, want, false)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	user := MustUser(r.Context())
	updated := result.node

	// A move plus a rename is one operation, so a client resolving a name
	// collision at the destination does not need two round trips.
	if req.ParentID != nil {
		// Moving needs the right to add to the destination as well as to
		// remove from the source.
		destination, apiErr := s.resolveFolder(r, *req.ParentID, permissions.Create|permissions.Upload)
		if apiErr != nil {
			WriteError(w, r, apiErr)
			return
		}

		newName := ""
		if req.Name != nil {
			newName = *req.Name
		}

		updated, err = s.files.Nodes().Move(r.Context(), nodes.MoveParams{
			ID: id, NewParentID: destination.ID, NewName: newName, ActorID: user.ID,
		})
		if err != nil {
			WriteError(w, r, translateNodeError(err))
			return
		}

		// The old name is recorded as well as the new one: "what happened to
		// the file called X" is the question somebody asks weeks later, and it
		// is unanswerable from the new name alone.
		s.audit(r, audit.ActionNodeMove, audit.Success,
			auditTarget(updated.ID, string(updated.Kind), updated.Name),
			auditDetail(map[string]any{
				"from_name": result.node.Name,
				"to_folder": destination.Name,
			}))
	} else {
		previousName := result.node.Name

		updated, err = s.files.Nodes().Rename(r.Context(), nodes.RenameParams{
			ID: id, Name: *req.Name, ActorID: user.ID,
		})
		if err != nil {
			WriteError(w, r, translateNodeError(err))
			return
		}

		s.audit(r, audit.ActionNodeRename, audit.Success,
			auditTarget(updated.ID, string(updated.Kind), updated.Name),
			auditDetail(map[string]any{"from_name": previousName}))
	}

	owner := user.Public()
	WriteJSON(w, r, http.StatusOK, map[string]any{"node": toNodeResponse(updated, &owner, user.ID)})
}

type copyNodeRequest struct {
	ParentID string `json:"parent_id"`
	Name     string `json:"name,omitempty"`
}

// handleCopyNode duplicates a node.
func (s *Server) handleCopyNode(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	// Copying needs to be able to read the content, not merely see that it
	// exists — otherwise a read-only listing entry could be duplicated into a
	// folder the caller controls and downloaded from there.
	sourceAccess, apiErr := s.requireAccess(r, id, permissions.Read|permissions.Download, false)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}
	source := sourceAccess.node

	var req copyNodeRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}

	destination, apiErr := s.resolveFolder(r, req.ParentID, permissions.Create|permissions.Upload)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	user := MustUser(r.Context())

	// A copy is charged against the quota even though content is shared on
	// disk: the user now has two files, and deduplication is an implementation
	// detail that should not become a way to exceed an allowance.
	size := source.SizeBytes
	if source.IsFolder() {
		subtree, _, err := s.files.Nodes().SubtreeSize(r.Context(), source.ID)
		if err != nil {
			WriteError(w, r, ErrInternal(err))
			return
		}
		size = subtree
	}
	if err := s.files.CheckQuota(r.Context(), destination.OwnerID, size); err != nil {
		WriteError(w, r, translateFileError(err))
		return
	}

	result, err := s.files.Nodes().Copy(r.Context(), nodes.CopyParams{
		ID: id, NewParentID: destination.ID, NewName: req.Name,
		// The copy belongs to whoever owns the destination folder, and
		// counts against their quota.
		OwnerID: destination.OwnerID, ActorID: user.ID,
	})
	if err != nil {
		WriteError(w, r, translateNodeError(err))
		return
	}

	s.logger.Info("node copied",
		"source_id", id, "new_id", result.Node.ID,
		"nodes", result.NodesCopied, "bytes", result.BytesAdded, "user_id", user.ID)

	// Copying is how content leaves one person's folder and enters another's,
	// so it belongs in the trail even though nothing was destroyed.
	s.audit(r, audit.ActionNodeCopy, audit.Success,
		auditTarget(result.Node.ID, string(result.Node.Kind), result.Node.Name),
		auditDetail(map[string]any{
			"from_id":   id.String(),
			"to_folder": destination.Name,
			"nodes":     result.NodesCopied,
		}))

	owner := user.Public()
	WriteJSON(w, r, http.StatusCreated, map[string]any{
		"node":         toNodeResponse(result.Node, &owner, user.ID),
		"nodes_copied": result.NodesCopied,
	})
}

// --- Delete -----------------------------------------------------------------

// handleDeleteNode moves a node to Trash, or purges it when ?permanent=true.
func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	permanent := strings.EqualFold(r.URL.Query().Get("permanent"), "true")

	// A permanent delete may target something already in Trash, so deleted
	// nodes are visible to this lookup in that case.
	result, apiErr := s.requireAccess(r, id, permissions.Delete, permanent)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}
	node := result.node

	user := MustUser(r.Context())

	if permanent {
		if err := s.files.Purge(r.Context(), id); err != nil {
			WriteError(w, r, translateNodeError(err))
			return
		}
		s.logger.Info("node permanently deleted",
			"node_id", id, "name", node.Name, "user_id", user.ID)
		// Recorded because it is irreversible. The name is captured here
		// precisely because the node is gone and the identifier alone would
		// answer nothing later.
		s.audit(r, audit.ActionNodePurge, audit.Success,
			auditTarget(id, string(node.Kind), node.Name),
			auditDetail(map[string]any{"size_bytes": node.SizeBytes}))
		WriteNoContent(w)
		return
	}

	if err := s.files.Delete(r.Context(), id, user.ID); err != nil {
		WriteError(w, r, translateNodeError(err))
		return
	}
	s.audit(r, audit.ActionNodeDelete, audit.Success,
		auditTarget(id, string(node.Kind), node.Name))
	WriteNoContent(w)
}

// handleRestoreNode returns a node from Trash.
func (s *Server) handleRestoreNode(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	if _, apiErr := s.requireAccess(r, id, permissions.Delete, true); apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	user := MustUser(r.Context())
	restored, err := s.files.Nodes().Restore(r.Context(), id, user.ID)
	if err != nil {
		WriteError(w, r, translateNodeError(err))
		return
	}

	s.audit(r, audit.ActionNodeRestore, audit.Success,
		auditTarget(restored.ID, string(restored.Kind), restored.Name))

	owner := user.Public()
	WriteJSON(w, r, http.StatusOK, map[string]any{"node": toNodeResponse(restored, &owner, user.ID)})
}

// handleListTrash returns the caller's deleted items.
func (s *Server) handleListTrash(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	limit := parseIntDefault(r.URL.Query().Get("limit"), nodes.DefaultPageSize)
	offset := parseIntDefault(r.URL.Query().Get("offset"), 0)

	page, err := s.files.Nodes().ListTrash(r.Context(), user.ID, limit, offset)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	owner := user.Public()
	items := make([]nodeResponse, 0, len(page.Nodes))
	for _, n := range page.Nodes {
		item := toNodeResponse(n, &owner, user.ID)
		item.IsOwner = true
		items = append(items, item)
	}

	WriteJSON(w, r, http.StatusOK, map[string]any{
		"nodes": items, "total": page.Total, "has_more": page.HasMore,
	})
}

// --- Error translation ------------------------------------------------------

// translateNodeError maps domain errors to HTTP responses.
func translateNodeError(err error) error {
	// Malware first: an infected upload is refused, and the message names the
	// signature so a person can tell a false positive from a real one. The
	// content is not stored, and nothing about it is retained beyond the audit
	// record.
	if errors.Is(err, files.ErrInfected) {
		return &APIError{
			Status:  http.StatusUnprocessableEntity,
			Code:    CodeValidationFailed,
			Message: strings.TrimPrefix(err.Error(), "files: "),
		}
	}
	if errors.Is(err, files.ErrScannerUnavailable) {
		// 503, not 422: nothing is wrong with the file, and retrying later is
		// exactly the right thing for the client to do.
		return ErrUnavailable(
			"Uploads are paused because the malware scanner cannot be reached. Try again shortly.")
	}

	var nameErr *nodes.NameError
	if errors.As(err, &nameErr) {
		return ErrValidation("That name cannot be used.").WithField("name", nameErr.Reason)
	}

	switch {
	case errors.Is(err, nodes.ErrNotFound):
		return ErrNotFound()
	case errors.Is(err, nodes.ErrNameTaken):
		return ErrConflict("An item with that name already exists here.")
	case errors.Is(err, nodes.ErrNotAFolder):
		return ErrBadRequest("That is a file, not a folder.")
	case errors.Is(err, nodes.ErrNotAFile):
		return ErrBadRequest("That is a folder, not a file.")
	case errors.Is(err, nodes.ErrCycle):
		return ErrBadRequest("A folder cannot be moved or copied into itself.")
	case errors.Is(err, nodes.ErrRootImmutable):
		return ErrBadRequest("Your top-level folder cannot be renamed, moved, or deleted.")
	case errors.Is(err, nodes.ErrNotDeleted):
		return ErrBadRequest("That item is not in the trash.")
	case errors.Is(err, nodes.ErrFolderNotEmpty):
		return ErrConflict("That folder is not empty.")
	default:
		return translateFileError(err)
	}
}

// translateFileError maps storage and quota errors to HTTP responses.
func translateFileError(err error) error {
	var quotaErr *files.QuotaError
	if errors.As(err, &quotaErr) {
		return &APIError{
			Status:  http.StatusInsufficientStorage,
			Code:    CodeQuotaExceeded,
			Message: "You do not have enough storage space left. Delete something, or empty your trash, and try again.",
		}
	}

	switch {
	case errors.Is(err, files.ErrTooLarge):
		return ErrTooLarge("That file is larger than this server accepts.")
	case errors.Is(err, files.ErrNoSpace):
		return ErrUnavailable("The server is out of disk space. Contact an administrator.")
	case errors.Is(err, files.ErrQuotaExceeded):
		return &APIError{
			Status:  http.StatusInsufficientStorage,
			Code:    CodeQuotaExceeded,
			Message: "You do not have enough storage space left.",
		}
	case errors.Is(err, files.ErrUploadNotFound), errors.Is(err, files.ErrUploadExpired):
		return ErrNotFound()
	case errors.Is(err, files.ErrOffsetMismatch):
		return ErrConflict(err.Error())
	case errors.Is(err, files.ErrUploadIncomplete):
		return ErrBadRequest("The upload is not complete yet.")
	case errors.Is(err, files.ErrChecksumMismatch):
		// The transfer was corrupted. This is the client's problem to retry,
		// not a server failure, so it must not be reported as a 500.
		return ErrValidation(
			"The uploaded file does not match the checksum you provided. " +
				"The transfer was corrupted; please upload it again.")
	case errors.Is(err, users.ErrNotFound):
		return ErrNotFound()
	default:
		return ErrInternal(err)
	}
}

// parseIntDefault parses a query parameter, falling back on anything invalid.
func parseIntDefault(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}
