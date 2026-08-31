package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
	"github.com/joshuanmoses/piCloud/backend/internal/previews"
	"github.com/joshuanmoses/piCloud/backend/internal/storage"
)

// Thumbnails.
//
// Only raster images have server-generated previews. PDFs, text, Markdown,
// audio and video are rendered by the browser from the ordinary download
// endpoint, which costs the server nothing — a decision made for a board that
// has four cores and no hardware video encoder.

// handlePreviewStatus reports whether a thumbnail exists, and queues one if it
// should but does not.
//
// The interface polls this after an upload, which is why a pending response is
// a normal 200 rather than a 404: the client needs to distinguish "not yet"
// from "never".
func (s *Server) handlePreviewStatus(w http.ResponseWriter, r *http.Request) {
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

	if !previews.Supportable(node.MimeType) {
		WriteJSON(w, r, http.StatusOK, map[string]any{
			"status":    string(previews.StatusUnavailable),
			"available": false,
		})
		return
	}

	record, err := s.previews.Store().Get(r.Context(), node.ID)
	switch {
	case errors.Is(err, previews.ErrNotFound):
		// Never asked for. Ask now, and tell the client to come back.
		if err := s.previews.Request(r.Context(), node); err != nil {
			s.logger.Warn("cannot queue a preview", "node_id", node.ID, "error", err)
		}
		WriteJSON(w, r, http.StatusOK, map[string]any{
			"status":    string(previews.StatusPending),
			"available": false,
		})
		return
	case err != nil:
		WriteError(w, r, ErrInternal(err))
		return
	}

	// A record from before the file was replaced describes content that is no
	// longer there. Queue a fresh one rather than advertising the old one.
	if node.ContentHash != nil && !record.Fresh(*node.ContentHash) {
		if err := s.previews.Request(r.Context(), node); err != nil {
			s.logger.Warn("cannot queue a refreshed preview", "node_id", node.ID, "error", err)
		}
		WriteJSON(w, r, http.StatusOK, map[string]any{
			"status":    string(previews.StatusPending),
			"available": false,
		})
		return
	}

	body := map[string]any{
		"status":    string(record.Status),
		"available": record.Status == previews.StatusReady,
	}
	if record.Width != nil && record.Height != nil {
		body["width"] = *record.Width
		body["height"] = *record.Height
	}
	// record.Detail is deliberately not included. It explains why a decoder
	// rejected a file, which is for the operator's log and not for a client.
	WriteJSON(w, r, http.StatusOK, body)
}

// handlePreview streams a node's thumbnail.
//
// The permission required is Read, not Download: a thumbnail is what a listing
// shows, and a viewer who may see a folder may see its thumbnails. It is not a
// route to the original bytes — the image is re-encoded at at most 480 pixels,
// and metadata does not survive the round trip.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
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

	if node.ContentHash == nil || *node.ContentHash == "" {
		WriteError(w, r, ErrNotFound())
		return
	}
	hash := *node.ContentHash

	record, err := s.previews.Store().Get(r.Context(), node.ID)
	if err != nil || record.Status != previews.StatusReady || !record.Fresh(hash) {
		if errors.Is(err, previews.ErrNotFound) || (err == nil && !record.Fresh(hash)) {
			// Not made yet. Queue it, and answer 404 so the interface falls
			// back to a generic icon instead of a broken image.
			if reqErr := s.previews.Request(r.Context(), node); reqErr != nil {
				s.logger.Warn("cannot queue a preview", "node_id", node.ID, "error", reqErr)
			}
		} else if err != nil && !errors.Is(err, previews.ErrNotFound) {
			WriteError(w, r, ErrInternal(err))
			return
		}
		WriteError(w, r, ErrNotFound())
		return
	}

	file, err := s.blobs.OpenPreview(hash)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			// The database says ready and the disk disagrees. Reset the record
			// so the next request regenerates rather than 404ing forever.
			s.logger.Warn("a preview recorded as ready is missing from disk",
				"node_id", node.ID)
			if markErr := s.previews.Store().MarkPending(r.Context(), node.ID, hash); markErr != nil {
				s.logger.Warn("cannot reset a missing preview", "node_id", node.ID, "error", markErr)
			}
			if reqErr := s.previews.Request(r.Context(), node); reqErr != nil {
				s.logger.Warn("cannot re-queue a missing preview", "node_id", node.ID, "error", reqErr)
			}
			WriteError(w, r, ErrNotFound())
			return
		}
		WriteError(w, r, ErrInternal(err))
		return
	}
	defer file.Close()

	header := w.Header()
	header.Set("Content-Type", previews.ContentType)
	header.Set("X-Content-Type-Options", "nosniff")
	// A thumbnail is only ever rendered as an <img> by PiFiles itself, so it
	// keeps the stricter 'none': there is no legitimate reason for any origin,
	// this one included, to frame it.
	header.Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
	// The thumbnail is derived from the content, so the content's digest
	// identifies it exactly: a replaced file gets a new ETag automatically.
	header.Set("ETag", `"preview-`+hash+`"`)
	// Private and long-lived. The URL carries the node ID, not the digest, so
	// this relies on the ETag for correctness after a replacement — which is
	// why must-revalidate is not used: the client should reuse its copy until
	// the node's own listing tells it the file changed.
	header.Set("Cache-Control", "private, max-age=3600")
	if record.SizeBytes != nil {
		header.Set("Content-Length", strconv.FormatInt(*record.SizeBytes, 10))
	}

	// ServeContent handles If-None-Match and HEAD, so a browser scrolling a
	// folder of photographs re-fetches nothing.
	http.ServeContent(w, r, "", node.UpdatedAt, file)
}

// previewsEnabled reports whether thumbnailing is available. It needs the
// concrete storage handle as well as the service: the status endpoint can
// answer from the database alone, but serving the image requires the disk.
func (s *Server) previewsEnabled() bool {
	return s.previews != nil && s.blobs != nil && s.filesEnabled()
}
