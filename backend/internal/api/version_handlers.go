package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/audit"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
	"github.com/joshuanmoses/piCloud/backend/internal/recent"
	"github.com/joshuanmoses/piCloud/backend/internal/users"
	"github.com/joshuanmoses/piCloud/backend/internal/versions"
)

// Version history.
//
// A version has no permissions of its own. It inherits the file's, and every
// handler here resolves the file first and checks against that — so a version
// identifier is never a route to content the caller could not otherwise reach,
// and revoking access to a file revokes access to its history in the same
// instant.

// versionResponse is a version as returned to a client.
//
// A distinct type from the domain struct so that adding a field cannot silently
// publish it. The content hash in particular stays server-side: it would let a
// client probe whether specific content exists.
type versionResponse struct {
	ID     uuid.UUID `json:"id"`
	Number int       `json:"version"`

	SizeBytes int64  `json:"size_bytes"`
	MimeType  string `json:"mime_type,omitempty"`
	// Name is what the file was called when this version was made, which may
	// differ from what it is called now.
	Name string `json:"name"`

	CreatedAt string        `json:"created_at"`
	CreatedBy *users.Public `json:"created_by,omitempty"`

	Reason string `json:"reason"`
	// ReasonLabel is the same fact written for a person, so the interface does
	// not have to carry its own copy of the vocabulary.
	ReasonLabel string `json:"reason_label"`
}

func toVersionResponse(v *versions.Version, author *users.Public) versionResponse {
	return versionResponse{
		ID:          v.ID,
		Number:      v.Number,
		SizeBytes:   v.SizeBytes,
		MimeType:    v.MimeType,
		Name:        v.Name,
		CreatedAt:   v.CreatedAt.UTC().Format(timeFormat),
		CreatedBy:   author,
		Reason:      string(v.Reason),
		ReasonLabel: v.Reason.Describe(),
	}
}

// handleListVersions returns a file's history.
//
// Read is the requirement. Seeing what a file used to contain is seeing the
// file, and anybody who can open it now could have kept a copy of any earlier
// state anyway.
func (s *Server) handleListVersions(w http.ResponseWriter, r *http.Request) {
	node, apiErr := s.resolveVersionTarget(r, permissions.Read)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	list, err := s.versions.List(r.Context(), node.ID)
	if err != nil {
		WriteError(w, r, translateVersionError(err))
		return
	}

	authors, err := s.versionAuthors(r, list)
	if err != nil {
		WriteError(w, r, err)
		return
	}

	items := make([]versionResponse, 0, len(list))
	for _, v := range list {
		var author *users.Public
		if v.CreatedBy != nil {
			author = authors[*v.CreatedBy]
		}
		items = append(items, toVersionResponse(v, author))
	}

	WriteJSON(w, r, http.StatusOK, map[string]any{
		"versions": items,
		"total":    len(items),
		// The current state is not a version, but the interface needs to show
		// it at the top of the list, so it is named rather than implied.
		"current": map[string]any{
			"size_bytes": node.SizeBytes,
			"mime_type":  node.MimeType,
			"name":       node.Name,
			"updated_at": node.UpdatedAt.UTC().Format(timeFormat),
		},
	})
}

// handleDownloadVersion streams an earlier state of a file.
func (s *Server) handleDownloadVersion(w http.ResponseWriter, r *http.Request) {
	node, apiErr := s.resolveVersionTarget(r, permissions.Read|permissions.Download)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	versionID, err := uuid.Parse(chiURLParam(r, "versionID"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	v, err := s.versions.Get(r.Context(), node.ID, versionID)
	if err != nil {
		WriteError(w, r, translateVersionError(err))
		return
	}

	content, size, err := s.versions.Open(v)
	if err != nil {
		WriteError(w, r, translateVersionError(err))
		return
	}
	defer content.Close()

	// The same headers as any other download — nosniff, the sandboxing CSP,
	// the inline allowlist — because a version is uploaded content like any
	// other and none of those protections may be skipped for it.
	//
	// The name carries the version number so a person who downloads three of
	// them does not end up with "Report.pdf", "Report (1).pdf", "Report
	// (2).pdf" and no idea which is which.
	s.writeDownloadHeaders(w, r, &nodes.Node{
		ID:          node.ID,
		Name:        versionFileName(v),
		MimeType:    v.MimeType,
		ContentHash: &v.ContentHash,
		UpdatedAt:   v.CreatedAt,
	})

	s.logger.Info("version downloaded",
		"node_id", node.ID, "version", v.Number, "size", size,
		"user_id", MustUser(r.Context()).ID)

	http.ServeContent(w, r, versionFileName(v), v.CreatedAt, content)
}

// versionFileName inserts the version number before the extension, so the
// downloaded file is identifiable on disk.
func versionFileName(v *versions.Version) string {
	name := v.Name
	dot := strings.LastIndex(name, ".")
	if dot <= 0 {
		return fmt.Sprintf("%s (v%d)", name, v.Number)
	}
	return fmt.Sprintf("%s (v%d)%s", name[:dot], v.Number, name[dot:])
}

// handleRestoreVersion points a file back at an earlier state.
//
// Modify is the requirement: this changes the file, and somebody who may only
// read it must not be able to rewrite it from its own history.
func (s *Server) handleRestoreVersion(w http.ResponseWriter, r *http.Request) {
	node, apiErr := s.resolveVersionTarget(r, permissions.Modify)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	versionID, err := uuid.Parse(chiURLParam(r, "versionID"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	user := MustUser(r.Context())
	restored, err := s.versions.Restore(r.Context(), node.ID, versionID, user.ID)
	if err != nil {
		WriteError(w, r, translateVersionError(err))
		return
	}

	s.audit(r, audit.ActionVersionRestore, audit.Success,
		auditTarget(node.ID, "file", node.Name),
		auditDetail(map[string]any{"version_id": versionID.String()}))

	s.recordActivity(r, node.ID, recent.ActionModified)
	if s.previews != nil {
		if reqErr := s.previews.Request(r.Context(), restored); reqErr != nil {
			s.logger.Warn("cannot queue a preview for restored content",
				"node_id", restored.ID, "error", reqErr)
		}
	}

	owner, err := s.ownerOf(r, restored)
	if err != nil {
		WriteError(w, r, err)
		return
	}

	WriteJSON(w, r, http.StatusOK, map[string]any{
		"node": toNodeResponse(restored, owner, user.ID),
	})
}

// handleDeleteVersion discards one entry from a file's history.
//
// Delete is the requirement, not Modify: this destroys data and nothing else
// undoes it. Somebody who may edit a file is not thereby entitled to erase the
// record of what it used to say.
func (s *Server) handleDeleteVersion(w http.ResponseWriter, r *http.Request) {
	node, apiErr := s.resolveVersionTarget(r, permissions.Delete)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	versionID, err := uuid.Parse(chiURLParam(r, "versionID"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	if err := s.versions.Delete(r.Context(), node.ID, versionID); err != nil {
		WriteError(w, r, translateVersionError(err))
		return
	}

	s.logger.Info("version deleted",
		"node_id", node.ID, "version_id", versionID, "user_id", MustUser(r.Context()).ID)
	s.audit(r, audit.ActionVersionDelete, audit.Success,
		auditTarget(node.ID, "file", node.Name),
		auditDetail(map[string]any{"version_id": versionID.String()}))
	WriteNoContent(w)
}

// resolveVersionTarget loads the file named by {id} with the given permission.
func (s *Server) resolveVersionTarget(r *http.Request, want permissions.Set) (*nodes.Node, error) {
	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		return nil, ErrNotFound()
	}
	result, apiErr := s.requireAccess(r, id, want, false)
	if apiErr != nil {
		return nil, apiErr
	}
	if !result.node.IsFile() {
		return nil, ErrBadRequest("Folders do not have version history.")
	}
	return result.node, nil
}

// versionAuthors batch-loads the profiles of everybody who made a version.
func (s *Server) versionAuthors(r *http.Request, list []*versions.Version) (map[uuid.UUID]*users.Public, error) {
	ids := make([]uuid.UUID, 0, len(list))
	seen := make(map[uuid.UUID]bool, len(list))
	for _, v := range list {
		if v.CreatedBy == nil || seen[*v.CreatedBy] {
			continue
		}
		seen[*v.CreatedBy] = true
		ids = append(ids, *v.CreatedBy)
	}
	if len(ids) == 0 {
		return map[uuid.UUID]*users.Public{}, nil
	}

	found, err := s.userStore.GetManyPublic(r.Context(), ids)
	if err != nil {
		return nil, ErrInternal(err)
	}

	out := make(map[uuid.UUID]*users.Public, len(found))
	for id, public := range found {
		out[id] = &public
	}
	return out, nil
}

// translateVersionError maps domain errors to HTTP responses.
func translateVersionError(err error) error {
	switch {
	case errors.Is(err, versions.ErrNotFound):
		return ErrNotFound()
	case errors.Is(err, versions.ErrDisabled):
		return ErrForbidden("Version history is switched off on this server.")
	case errors.Is(err, versions.ErrNotAFile):
		return ErrBadRequest("Folders do not have version history.")
	case errors.Is(err, nodes.ErrNotFound):
		return ErrNotFound()
	case errors.Is(err, nodes.ErrQuotaExceeded):
		return ErrConflict("Restoring this version would exceed the storage quota.")
	default:
		return ErrInternal(err)
	}
}

// versionsEnabled reports whether version history is available.
func (s *Server) versionsEnabled() bool {
	return s.versions != nil && s.versions.Enabled() && s.filesEnabled()
}
