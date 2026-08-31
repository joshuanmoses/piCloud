package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joshuanmoses/piCloud/backend/internal/database"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/storage"
)

// Errors returned by the resumable upload flow.
var (
	// ErrUploadNotFound means no such upload session, or none belonging to the
	// caller. The two are deliberately indistinguishable.
	ErrUploadNotFound = errors.New("files: upload session not found")
	// ErrOffsetMismatch means the client sent a chunk at the wrong position.
	ErrOffsetMismatch = errors.New("files: chunk offset does not match what the server has received")
	// ErrUploadIncomplete means finalisation was attempted before all bytes
	// arrived.
	ErrUploadIncomplete = errors.New("files: upload is not complete")
	// ErrUploadExpired means the session was abandoned and swept.
	ErrUploadExpired = errors.New("files: upload session has expired")
	// ErrChecksumMismatch means the assembled content does not match the
	// checksum the client supplied, so the transfer was corrupted. This is a
	// client-side condition, not a server failure.
	ErrChecksumMismatch = errors.New("files: uploaded content does not match the supplied checksum")
)

// Upload is an in-flight resumable upload.
type Upload struct {
	ID       uuid.UUID `json:"id"`
	OwnerID  uuid.UUID `json:"-"`
	ParentID uuid.UUID `json:"parent_id"`
	Name     string    `json:"name"`

	DeclaredSize  int64 `json:"declared_size"`
	ReceivedBytes int64 `json:"received_bytes"`

	ReplacesID *uuid.UUID `json:"replaces_id,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// IsComplete reports whether every declared byte has arrived.
func (u *Upload) IsComplete() bool { return u.ReceivedBytes == u.DeclaredSize }

// Remaining returns how many bytes are still expected.
func (u *Upload) Remaining() int64 { return u.DeclaredSize - u.ReceivedBytes }

// UploadStore persists resumable upload sessions.
type UploadStore struct {
	db      *database.DB
	storage *storage.Storage
	ttl     time.Duration
	logger  *slog.Logger
}

// NewUploadStore returns an UploadStore.
func NewUploadStore(db *database.DB, store *storage.Storage, ttl time.Duration, logger *slog.Logger) *UploadStore {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &UploadStore{db: db, storage: store, ttl: ttl, logger: logger}
}

const uploadColumns = `
	id, owner_id, parent_id, name, declared_size, received_bytes,
	replaces_id, created_at, updated_at, expires_at`

type scanner interface{ Scan(dest ...any) error }

func scanUpload(s scanner) (*Upload, error) {
	var u Upload
	err := s.Scan(
		&u.ID, &u.OwnerID, &u.ParentID, &u.Name, &u.DeclaredSize,
		&u.ReceivedBytes, &u.ReplacesID, &u.CreatedAt, &u.UpdatedAt, &u.ExpiresAt,
	)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// BeginParams describes a new resumable upload.
type BeginParams struct {
	OwnerID    uuid.UUID
	ParentID   uuid.UUID
	Name       string
	Size       int64
	ReplacesID *uuid.UUID
}

// Begin creates an upload session and its temporary file.
func (s *UploadStore) Begin(ctx context.Context, p BeginParams) (*Upload, error) {
	name := nodes.NormalizeName(p.Name)
	if err := nodes.ValidateName(name); err != nil {
		return nil, err
	}
	if p.Size < 0 {
		return nil, fmt.Errorf("files: negative upload size")
	}

	row := s.db.Pool().QueryRow(ctx, `
		INSERT INTO uploads (owner_id, parent_id, name, declared_size, replaces_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, now() + $6::interval)
		RETURNING `+uploadColumns,
		p.OwnerID, p.ParentID, name, p.Size, p.ReplacesID, s.ttl)

	upload, err := scanUpload(row)
	if err != nil {
		return nil, fmt.Errorf("files: cannot create upload session: %w", err)
	}

	// The temporary file is created eagerly so a client that immediately sends
	// a chunk does not race its creation.
	writer, err := s.storage.NewTempWriter(tempID(upload.ID))
	if err != nil {
		// Roll back the session rather than leaving one that can never receive
		// anything.
		if _, delErr := s.db.Pool().Exec(ctx, `DELETE FROM uploads WHERE id = $1`, upload.ID); delErr != nil {
			s.logger.Warn("could not clean up an upload session", "upload_id", upload.ID, "error", delErr)
		}
		return nil, fmt.Errorf("files: cannot create temporary file: %w", err)
	}
	if err := writer.Close(); err != nil {
		s.logger.Warn("could not close new temporary file", "upload_id", upload.ID, "error", err)
	}

	s.logger.Info("upload session started",
		"upload_id", upload.ID, "owner_id", p.OwnerID, "name", name, "size", p.Size)
	return upload, nil
}

// Get returns an upload session owned by ownerID.
//
// Ownership is part of the query, not a check afterwards: a session belonging
// to somebody else is simply not found, so a guessed identifier reveals
// nothing.
func (s *UploadStore) Get(ctx context.Context, id, ownerID uuid.UUID) (*Upload, error) {
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+uploadColumns+` FROM uploads WHERE id = $1 AND owner_id = $2`, id, ownerID)
	upload, err := scanUpload(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUploadNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("files: load upload session: %w", err)
	}
	if time.Now().After(upload.ExpiresAt) {
		return nil, ErrUploadExpired
	}
	return upload, nil
}

// WriteChunk appends a chunk at the given offset.
//
// Chunks must arrive in order: offset must equal what the server already holds.
// Out-of-order chunks would require tracking which ranges arrived, and on a
// Raspberry Pi writing a single USB-attached disk, parallel chunk upload would
// cost more in seek thrashing than it gains anyway.
//
// A client that loses its connection asks for the session, learns
// received_bytes, and resumes from there.
func (s *UploadStore) WriteChunk(
	ctx context.Context, id, ownerID uuid.UUID, offset int64, body io.Reader, maxChunk int64,
) (*Upload, error) {
	upload, err := s.Get(ctx, id, ownerID)
	if err != nil {
		return nil, err
	}

	if offset != upload.ReceivedBytes {
		return nil, fmt.Errorf("%w: server has %d bytes, chunk claims offset %d",
			ErrOffsetMismatch, upload.ReceivedBytes, offset)
	}

	remaining := upload.Remaining()
	if remaining <= 0 {
		return upload, nil
	}
	limit := remaining
	if maxChunk > 0 && maxChunk < limit {
		limit = maxChunk
	}

	writer, err := s.storage.OpenTempWriter(tempID(id), upload.ReceivedBytes)
	if err != nil {
		return nil, fmt.Errorf("files: cannot resume temporary file: %w", err)
	}
	defer writer.Close()

	written, err := writer.CopyFrom(body, limit)
	if err != nil {
		// Whatever did land is still on disk. Recording it lets the client
		// resume from the true position rather than resending the chunk.
		if written > 0 {
			if syncErr := writer.Sync(); syncErr == nil {
				if _, updateErr := s.recordProgress(ctx, id, upload.ReceivedBytes+written); updateErr != nil {
					s.logger.Warn("could not record partial chunk", "upload_id", id, "error", updateErr)
				}
			}
		}
		if errors.Is(err, storage.ErrSizeMismatch) {
			return nil, fmt.Errorf("%w: chunk is larger than the remaining %d bytes", ErrOffsetMismatch, remaining)
		}
		return nil, fmt.Errorf("files: cannot write chunk: %w", err)
	}

	// Flush before recording progress. If the order were reversed, a power cut
	// would leave the database claiming bytes the disk never received, and the
	// resumed upload would silently produce a corrupt file.
	if err := writer.Sync(); err != nil {
		return nil, err
	}

	return s.recordProgress(ctx, id, upload.ReceivedBytes+written)
}

func (s *UploadStore) recordProgress(ctx context.Context, id uuid.UUID, received int64) (*Upload, error) {
	row := s.db.Pool().QueryRow(ctx,
		`UPDATE uploads SET received_bytes = $2 WHERE id = $1 RETURNING `+uploadColumns,
		id, received)
	upload, err := scanUpload(row)
	if err != nil {
		return nil, fmt.Errorf("files: cannot record upload progress: %w", err)
	}
	return upload, nil
}

// Abort cancels an upload session and removes its temporary file.
func (s *UploadStore) Abort(ctx context.Context, id, ownerID uuid.UUID) error {
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM uploads WHERE id = $1 AND owner_id = $2`, id, ownerID)
	if err != nil {
		return fmt.Errorf("files: cannot abort upload: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUploadNotFound
	}
	if err := s.storage.RemoveTemp(tempID(id)); err != nil {
		s.logger.Warn("could not remove temporary file for aborted upload", "upload_id", id, "error", err)
	}
	return nil
}

// Delete removes a session record without touching the temporary file. Used
// after successful finalisation, where Commit already consumed the file.
func (s *UploadStore) Delete(ctx context.Context, id uuid.UUID) error {
	if _, err := s.db.Pool().Exec(ctx, `DELETE FROM uploads WHERE id = $1`, id); err != nil {
		return fmt.Errorf("files: cannot delete upload session: %w", err)
	}
	return nil
}

// ListForOwner returns a user's in-flight uploads, so a client that lost its
// state can find and resume them.
func (s *UploadStore) ListForOwner(ctx context.Context, ownerID uuid.UUID) ([]*Upload, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+uploadColumns+` FROM uploads
		 WHERE owner_id = $1 AND expires_at > now() ORDER BY created_at DESC`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("files: list uploads: %w", err)
	}
	defer rows.Close()

	var out []*Upload
	for rows.Next() {
		u, err := scanUpload(rows)
		if err != nil {
			return nil, fmt.Errorf("files: scan upload: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SweepExpired removes abandoned sessions and their temporary files.
//
// Without this, an interrupted upload would occupy disk space forever — and on
// a Pi with a modest drive, a handful of abandoned multi-gigabyte uploads is
// enough to fill it.
func (s *UploadStore) SweepExpired(ctx context.Context) (int, error) {
	rows, err := s.db.Pool().Query(ctx,
		`DELETE FROM uploads WHERE expires_at < now() RETURNING id`)
	if err != nil {
		return 0, fmt.Errorf("files: sweep uploads: %w", err)
	}

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("files: scan swept upload: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("files: sweep uploads: %w", err)
	}

	for _, id := range ids {
		if err := s.storage.RemoveTemp(tempID(id)); err != nil {
			s.logger.Warn("could not remove temporary file for expired upload",
				"upload_id", id, "error", err)
		}
	}

	if len(ids) > 0 {
		s.logger.Info("swept abandoned uploads", "count", len(ids))
	}
	return len(ids), nil
}

// tempID derives the temporary filename for an upload session.
//
// The UUID's hyphens are stripped so the result is plain alphanumerics, which
// the storage layer validates independently.
func tempID(id uuid.UUID) string {
	hex := id.String()
	out := make([]byte, 0, 32)
	for i := 0; i < len(hex); i++ {
		if hex[i] != '-' {
			out = append(out, hex[i])
		}
	}
	return string(out)
}
