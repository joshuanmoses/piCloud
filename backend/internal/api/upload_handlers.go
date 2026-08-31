package api

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/files"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
	"github.com/joshuanmoses/piCloud/backend/internal/recent"
)

// Upload handlers.
//
// Two paths exist, and both stream:
//
//   - A single request for ordinary files. Simple, one round trip.
//   - A chunked session for large files, which survives a dropped connection
//     and can be resumed from the exact byte the server received.
//
// Neither ever buffers a file in memory. multipart/form-data is deliberately
// NOT used for the file body: Go's multipart reader spills large parts to
// temporary files, which would mean writing every upload to disk twice.
// Instead the file name and target folder travel as headers or query
// parameters, and the raw request body is the content.

// handleUpload accepts a file in a single request.
//
// The body is the file. Metadata comes from the query string:
//
//	POST /api/v1/files?parent_id=<uuid>&name=Report.pdf
//	Content-Length: 2411724
//	<raw bytes>
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())
	query := r.URL.Query()

	parent, apiErr := s.resolveFolder(r, query.Get("parent_id"), permissions.Upload)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	name := query.Get("name")
	if name == "" {
		// Fall back to Content-Disposition, which is what a browser's
		// drag-and-drop upload naturally supplies.
		if _, params, err := mime.ParseMediaType(r.Header.Get("Content-Disposition")); err == nil {
			name = params["filename"]
		}
	}
	if name == "" {
		WriteError(w, r, ErrBadRequest("Provide the file name in the 'name' query parameter."))
		return
	}

	var replaceID *uuid.UUID
	if raw := query.Get("replace_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			WriteError(w, r, ErrBadRequest("replace_id is not a valid identifier."))
			return
		}
		// Replacing existing content is a distinct permission from adding new
		// content: a contributor may drop files into a folder without being
		// able to overwrite what is already there.
		existing, apiErr := s.requireAccess(r, id, permissions.Modify, false)
		if apiErr != nil {
			WriteError(w, r, apiErr)
			return
		}
		if !existing.node.IsFile() {
			WriteError(w, r, ErrBadRequest("Only a file can be replaced."))
			return
		}
		replaceID = &id
	}

	// Content-Length is a claim, used only for the pre-flight quota check so
	// an over-quota upload is refused before the bytes are transferred. The
	// authoritative size is what actually arrives.
	declared := r.ContentLength
	if declared > s.files.Config().MaxUploadSize {
		WriteError(w, r, ErrTooLarge(fmt.Sprintf(
			"That file is larger than the %s this server accepts.",
			humanSize(s.files.Config().MaxUploadSize))))
		return
	}

	result, err := s.files.Upload(r.Context(), files.UploadParams{
		// Content uploaded into a shared folder belongs to that folder's
		// owner and counts against their quota, not the uploader's.
		OwnerID:      parent.OwnerID,
		ParentID:     parent.ID,
		Name:         name,
		DeclaredSize: declared,
		Content:      r.Body,
		ActorID:      user.ID,
		ReplaceID:    replaceID,
	})
	if err != nil {
		WriteError(w, r, translateNodeError(err))
		return
	}

	s.afterUpload(r, result.Node)

	owner := user.Public()
	WriteJSON(w, r, http.StatusCreated, map[string]any{
		"node":         toNodeResponse(result.Node, &owner, user.ID),
		"deduplicated": result.Deduplicated,
	})
}

// --- Resumable uploads ------------------------------------------------------

type beginUploadRequest struct {
	ParentID  string `json:"parent_id"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	ReplaceID string `json:"replace_id,omitempty"`
}

type uploadSessionResponse struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	DeclaredSize  int64     `json:"declared_size"`
	ReceivedBytes int64     `json:"received_bytes"`
	ChunkSize     int64     `json:"chunk_size"`
	ExpiresAt     string    `json:"expires_at"`
	Complete      bool      `json:"complete"`
}

func (s *Server) toUploadResponse(u *files.Upload) uploadSessionResponse {
	return uploadSessionResponse{
		ID:            u.ID,
		Name:          u.Name,
		DeclaredSize:  u.DeclaredSize,
		ReceivedBytes: u.ReceivedBytes,
		ChunkSize:     s.files.Config().ChunkSize,
		ExpiresAt:     u.ExpiresAt.UTC().Format(timeFormat),
		Complete:      u.IsComplete(),
	}
}

// handleBeginUpload starts a resumable upload session.
func (s *Server) handleBeginUpload(w http.ResponseWriter, r *http.Request) {
	var req beginUploadRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}

	parent, apiErr := s.resolveFolder(r, req.ParentID, permissions.Upload)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	if req.Size < 0 {
		WriteError(w, r, ErrBadRequest("Size must not be negative."))
		return
	}

	// Refuse now rather than after the client has spent an hour uploading.
	if err := s.files.CheckQuota(r.Context(), parent.OwnerID, req.Size); err != nil {
		WriteError(w, r, translateFileError(err))
		return
	}

	var replacesID *uuid.UUID
	if req.ReplaceID != "" {
		id, err := uuid.Parse(req.ReplaceID)
		if err != nil {
			WriteError(w, r, ErrBadRequest("replace_id is not a valid identifier."))
			return
		}
		if _, apiErr := s.requireAccess(r, id, permissions.Modify, false); apiErr != nil {
			WriteError(w, r, apiErr)
			return
		}
		replacesID = &id
	}

	upload, err := s.files.Uploads().Begin(r.Context(), files.BeginParams{
		OwnerID:    parent.OwnerID,
		ParentID:   parent.ID,
		Name:       req.Name,
		Size:       req.Size,
		ReplacesID: replacesID,
	})
	if err != nil {
		WriteError(w, r, translateNodeError(err))
		return
	}

	WriteJSON(w, r, http.StatusCreated, s.toUploadResponse(upload))
}

// handleGetUpload reports an upload session's progress.
//
// This is how a client resumes: it asks how much the server has, then sends
// from that offset.
func (s *Server) handleGetUpload(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	// Ownership is part of the query, so another user's session is simply not
	// found.
	upload, err := s.files.Uploads().Get(r.Context(), id, user.ID)
	if err != nil {
		WriteError(w, r, translateFileError(err))
		return
	}

	WriteJSON(w, r, http.StatusOK, s.toUploadResponse(upload))
}

// handleUploadChunk appends a chunk to an upload session.
//
//	PUT /api/v1/uploads/{id}/chunk
//	Content-Range: bytes 16777216-33554431/104857600
//
// or, more simply, ?offset=16777216. The body is the raw chunk.
func (s *Server) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	offset, apiErr := chunkOffset(r)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	upload, err := s.files.Uploads().WriteChunk(
		r.Context(), id, user.ID, offset, r.Body, s.files.Config().ChunkSize*2)
	if err != nil {
		if errors.Is(err, files.ErrOffsetMismatch) {
			// Tell the client where the server actually is, so it can resume
			// correctly instead of guessing.
			if current, getErr := s.files.Uploads().Get(r.Context(), id, user.ID); getErr == nil {
				w.Header().Set("X-Upload-Offset", strconv.FormatInt(current.ReceivedBytes, 10))
			}
		}
		WriteError(w, r, translateFileError(err))
		return
	}

	WriteJSON(w, r, http.StatusOK, s.toUploadResponse(upload))
}

// chunkOffset reads the chunk's position from Content-Range or the query.
func chunkOffset(r *http.Request) (int64, error) {
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || offset < 0 {
			return 0, ErrBadRequest("offset must be a non-negative integer.")
		}
		return offset, nil
	}

	// Content-Range: bytes <start>-<end>/<total>
	header := r.Header.Get("Content-Range")
	if header == "" {
		return 0, ErrBadRequest("Provide the chunk position as an 'offset' query parameter or a Content-Range header.")
	}

	spec, ok := strings.CutPrefix(strings.TrimSpace(header), "bytes ")
	if !ok {
		return 0, ErrBadRequest("Content-Range must be of the form 'bytes start-end/total'.")
	}
	rangePart, _, _ := strings.Cut(spec, "/")
	startText, _, found := strings.Cut(rangePart, "-")
	if !found {
		return 0, ErrBadRequest("Content-Range must be of the form 'bytes start-end/total'.")
	}

	offset, err := strconv.ParseInt(strings.TrimSpace(startText), 10, 64)
	if err != nil || offset < 0 {
		return 0, ErrBadRequest("Content-Range start must be a non-negative integer.")
	}
	return offset, nil
}

type completeUploadRequest struct {
	// Checksum is the client's SHA-256 of the file, hex encoded. Optional;
	// when present the server verifies it and rejects a corrupted transfer.
	Checksum string `json:"checksum,omitempty"`
}

// handleCompleteUpload finalizes a resumable upload.
func (s *Server) handleCompleteUpload(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	var req completeUploadRequest
	// The body is optional: a client with no checksum may send nothing.
	if r.ContentLength > 0 {
		if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
			WriteError(w, r, err)
			return
		}
	}

	result, err := s.files.Finalize(r.Context(), files.FinalizeParams{
		UploadID:         id,
		OwnerID:          user.ID,
		ActorID:          user.ID,
		ExpectedChecksum: req.Checksum,
	})
	if err != nil {
		WriteError(w, r, translateNodeError(err))
		return
	}

	s.afterUpload(r, result.Node)

	owner := user.Public()
	WriteJSON(w, r, http.StatusCreated, map[string]any{
		"node":         toNodeResponse(result.Node, &owner, user.ID),
		"deduplicated": result.Deduplicated,
	})
}

// handleAbortUpload cancels an upload and removes its partial content.
func (s *Server) handleAbortUpload(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	if err := s.files.Uploads().Abort(r.Context(), id, user.ID); err != nil {
		WriteError(w, r, translateFileError(err))
		return
	}
	WriteNoContent(w)
}

// handleListUploads returns the caller's in-flight uploads, so a client that
// lost its state can find and resume them.
func (s *Server) handleListUploads(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	list, err := s.files.Uploads().ListForOwner(r.Context(), user.ID)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	out := make([]uploadSessionResponse, 0, len(list))
	for _, u := range list {
		out = append(out, s.toUploadResponse(u))
	}
	WriteJSON(w, r, http.StatusOK, map[string]any{"uploads": out})
}

// humanSize renders a byte count for a user-facing message.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit && exp < 4; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.0f %cB", float64(n)/float64(div), "KMGTP"[exp])
}

// afterUpload runs the bookkeeping that follows a stored file.
//
// Everything here is best-effort by design. The bytes are on disk and the
// metadata is committed; an upload that succeeded must never be reported as
// failed because a thumbnail could not be queued or a history row could not be
// written. Failures are logged for the operator and invisible to the user.
func (s *Server) afterUpload(r *http.Request, node *nodes.Node) {
	if node == nil {
		return
	}

	s.recordActivity(r, node.ID, recent.ActionUploaded)

	if s.previews != nil {
		if err := s.previews.Request(r.Context(), node); err != nil {
			s.logger.Warn("cannot queue a preview for an uploaded file",
				"node_id", node.ID, "error", err)
		}
	}
}
