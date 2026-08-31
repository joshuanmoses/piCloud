package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/archive"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
	"github.com/joshuanmoses/piCloud/backend/internal/recent"
)

// ZIP downloads.
//
// The archive is streamed, so the response has no Content-Length and no
// Content-Range: the total is unknowable until the last entry is written.
// A consequence worth stating plainly, because it shapes the error handling
// below: once the first byte is on the wire the status code is already 200, and
// a failure partway through cannot be turned into a 500. What the client gets
// instead is a truncated ZIP, which every extractor rejects — the correct
// signal that the download failed, and the reason the failure is logged
// server-side with the detail the client will never see.

// handleDownloadFolder streams a folder and its contents as a ZIP.
func (s *Server) handleDownloadFolder(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	result, apiErr := s.requireAccess(r, id, permissions.Read|permissions.Download, false)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}
	node := result.node

	if node.Kind != nodes.KindFolder {
		WriteError(w, r, ErrBadRequest("That is a file. Download it directly instead."))
		return
	}

	user := MustUser(r.Context())
	name := archive.FileName(node.Name, false)
	if node.IsRoot() {
		// "My Files.zip" would be a strange name for somebody's entire
		// account, and the root's stored name is an internal detail.
		name = archive.FileName("", true)
	}

	writeArchiveHeaders(w, name)

	built, err := s.archives.WriteFolder(r.Context(), w, node.ID, user.ID)
	s.finishArchive(r, err, built, "folder", node.ID.String())

	if err == nil {
		s.recordActivity(r, node.ID, recent.ActionDownloaded)
	}
}

// handleDownloadSelection streams an explicit set of items as one ZIP.
//
// A GET with repeated ?id= parameters rather than a POST, so the browser can
// drive it with a plain navigation and the user gets the native download
// progress indicator. It mutates nothing, so GET is also the honest verb.
func (s *Server) handleDownloadSelection(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query()["id"]
	if len(raw) == 0 {
		WriteError(w, r, ErrBadRequest("Select at least one item to download."))
		return
	}
	if len(raw) > archive.MaxEntries {
		WriteError(w, r, ErrBadRequest("That is more items than one archive can hold."))
		return
	}

	ids := make([]uuid.UUID, 0, len(raw))
	seen := make(map[uuid.UUID]bool, len(raw))
	for _, value := range raw {
		id, err := uuid.Parse(value)
		if err != nil {
			WriteError(w, r, ErrBadRequest("One of the selected items is not valid."))
			return
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}

	user := MustUser(r.Context())

	// Resolve a name before the headers go out. A single-item selection is
	// named after the item; anything else gets a dated archive name.
	name := archive.FileName("", true)
	if len(ids) == 1 {
		if node, err := s.files.Nodes().Get(r.Context(), ids[0]); err == nil {
			name = archive.FileName(node.Name, false)
		}
	}

	writeArchiveHeaders(w, name)

	built, err := s.archives.WriteSelection(r.Context(), w, ids, user.ID)
	if errors.Is(err, archive.ErrEmpty) {
		// Nothing readable in the selection. The headers are already written,
		// so this cannot become a 404 — but an empty archive is a clear enough
		// answer, and it reveals nothing about which of the requested items
		// exist.
		s.logger.Info("archive request matched nothing the caller may read",
			"user_id", user.ID, "requested", len(ids))
		return
	}
	s.finishArchive(r, err, built, "selection", strings.Join(raw, ","))
}

// writeArchiveHeaders sets up a streaming ZIP response.
func writeArchiveHeaders(w http.ResponseWriter, filename string) {
	header := w.Header()
	header.Set("Content-Type", "application/zip")
	header.Set("Content-Disposition", contentDisposition("attachment", filename))
	// A ZIP built from someone's files must never be cached anywhere shared,
	// and its contents can change between requests, so it is never reusable.
	header.Set("Cache-Control", "private, no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	// Range requests cannot be honoured on a stream whose length is unknown,
	// and a client that tried would get a corrupt archive.
	header.Set("Accept-Ranges", "none")
	w.WriteHeader(http.StatusOK)
}

// finishArchive logs the outcome of a streamed archive.
//
// It cannot change the response — the status was decided when the first byte
// went out — so its whole job is to leave a record an operator can act on.
func (s *Server) finishArchive(r *http.Request, err error, result *archive.Result, kind, subject string) {
	user := UserFromContext(r.Context())
	userID := "anonymous"
	if user != nil {
		userID = user.ID.String()
	}

	if err != nil {
		if errors.Is(err, r.Context().Err()) {
			// The client navigated away or cancelled. Ordinary, not a fault.
			s.logger.Info("archive download cancelled by the client",
				"kind", kind, "user_id", userID)
			return
		}
		s.logger.Error("archive download failed partway through",
			"kind", kind, "subject", subject, "user_id", userID, "error", err,
			"note", "the client received a truncated archive")
		return
	}

	if result == nil {
		return
	}
	if result.Incomplete || result.Skipped > 0 {
		// Worth an operator's attention: a skipped entry means metadata and
		// storage disagree, or an item became unreadable mid-request.
		s.logger.Warn("archive completed with items omitted",
			"kind", kind, "user_id", userID,
			"files", result.Files, "folders", result.Folders, "skipped", result.Skipped)
		return
	}
	s.logger.Info("archive downloaded",
		"kind", kind, "user_id", userID,
		"files", result.Files, "folders", result.Folders, "bytes", result.Bytes)
}

// archivesEnabled reports whether ZIP downloads are available.
func (s *Server) archivesEnabled() bool { return s.archives != nil && s.filesEnabled() }
