package files

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
)

// FinalizeParams describes the completion of a resumable upload.
type FinalizeParams struct {
	UploadID uuid.UUID
	OwnerID  uuid.UUID
	ActorID  uuid.UUID
	// ExpectedChecksum, when supplied by the client, is compared against what
	// the server computed. A mismatch means the transfer corrupted the data,
	// and the upload is rejected rather than silently storing a damaged file.
	ExpectedChecksum string
}

// Finalize completes a resumable upload and records the file.
//
// The same ordering rule as a single-request upload applies: content is linked
// into place before the metadata commit, so a failure can leave an unreferenced
// blob but never a file record with missing bytes.
func (s *Service) Finalize(ctx context.Context, p FinalizeParams) (*UploadResult, error) {
	upload, err := s.uploads.Get(ctx, p.UploadID, p.OwnerID)
	if err != nil {
		return nil, err
	}

	if !upload.IsComplete() {
		return nil, fmt.Errorf("%w: received %d of %d bytes",
			ErrUploadIncomplete, upload.ReceivedBytes, upload.DeclaredSize)
	}

	// Reopening rebuilds the hash from what is actually on disk, which also
	// verifies the partial file is intact and the expected length.
	writer, err := s.storage.OpenTempWriter(tempID(upload.ID), upload.ReceivedBytes)
	if err != nil {
		return nil, fmt.Errorf("files: cannot finalize upload: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			if err := writer.Discard(); err != nil {
				s.logger.Warn("could not discard temporary upload", "upload_id", upload.ID, "error", err)
			}
		}
	}()

	digest := writer.Digest()

	if p.ExpectedChecksum != "" && !equalFold(p.ExpectedChecksum, digest) {
		s.logger.Warn("upload checksum mismatch; rejecting",
			"upload_id", upload.ID, "expected", truncate(p.ExpectedChecksum), "actual", digest[:12])
		return nil, fmt.Errorf("%w (expected %s, got %s)",
			ErrChecksumMismatch, truncate(p.ExpectedChecksum), digest[:12])
	}

	// Re-check against the real size before committing anything.
	if err := s.CheckQuota(ctx, p.OwnerID, upload.ReceivedBytes); err != nil {
		return nil, err
	}

	mimeType, err := s.sniffTempContent(upload.ID, upload.Name)
	if err != nil {
		return nil, err
	}

	// The same scan as a single-request upload, in the same place: after the
	// bytes are staged and before anything commits.
	scan, err := s.scanTemp(ctx, tempID(upload.ID), upload.ReceivedBytes)
	if err != nil {
		if errors.Is(err, ErrInfected) {
			s.quarantineTemp(tempID(upload.ID), digest)
			committed = s.quarantine
		}
		// The session is removed either way: leaving it would let the client
		// retry Finalize forever against content already judged.
		if delErr := s.uploads.Delete(ctx, upload.ID); delErr != nil {
			s.logger.Warn("could not remove a refused upload session",
				"upload_id", upload.ID, "error", delErr)
		}
		return nil, err
	}
	if scan.Verdict != "" {
		s.logger.Debug("resumable upload scanned",
			"upload_id", upload.ID, "verdict", scan.Verdict, "bytes", scan.Bytes)
	}

	existed := s.storage.BlobExists(digest)

	finalDigest, size, err := writer.Commit()
	if err != nil {
		return nil, fmt.Errorf("files: cannot store content: %w", err)
	}
	committed = true

	node, err := s.nodes.CreateFile(ctx, nodes.CreateFileParams{
		OwnerID:     p.OwnerID,
		ParentID:    upload.ParentID,
		Name:        upload.Name,
		ContentHash: finalDigest,
		SizeBytes:   size,
		MimeType:    mimeType,
		ActorID:     p.ActorID,
		ReplaceID:   upload.ReplacesID,
	})
	if err != nil {
		s.logger.Warn("upload committed to storage but metadata failed; blob is unreferenced",
			"digest", finalDigest[:12], "error", err)
		return nil, err
	}

	// The session has served its purpose. A failure here leaves a stale row
	// that the sweeper removes; it does not affect the stored file.
	if err := s.uploads.Delete(ctx, upload.ID); err != nil {
		s.logger.Warn("could not remove completed upload session", "upload_id", upload.ID, "error", err)
	}

	s.logger.Info("resumable upload finalized",
		"node_id", node.ID, "upload_id", upload.ID, "size", size, "deduplicated", existed)

	return &UploadResult{Node: node, Deduplicated: existed}, nil
}

// sniffTempContent determines the MIME type from the first bytes on disk.
func (s *Service) sniffTempContent(uploadID uuid.UUID, name string) (string, error) {
	f, err := s.storage.OpenTemp(tempID(uploadID))
	if err != nil {
		return "", fmt.Errorf("files: cannot inspect uploaded content: %w", err)
	}
	defer f.Close()

	header := make([]byte, 512)
	n, err := io.ReadFull(f, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", fmt.Errorf("files: cannot read uploaded content: %w", err)
	}
	return DetectMimeType(header[:n], name), nil
}

// equalFold compares two hex strings case-insensitively, so a client sending an
// uppercase checksum is not rejected for it.
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func truncate(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
