package previews

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/database"
)

// Record is the database's account of a node's preview.
type Record struct {
	NodeID uuid.UUID `json:"node_id"`
	// ContentHash is the digest of the content the preview was made from.
	// Comparing it against the node's current hash is how a stale thumbnail —
	// left over from a replaced file — is detected rather than served.
	ContentHash string    `json:"-"`
	Status      Status    `json:"status"`
	Width       *int      `json:"width,omitempty"`
	Height      *int      `json:"height,omitempty"`
	SizeBytes   *int64    `json:"size_bytes,omitempty"`
	Detail      string    `json:"-"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Fresh reports whether this record describes the given content.
func (r *Record) Fresh(contentHash string) bool {
	return r != nil && r.ContentHash == contentHash
}

// Store persists preview records.
type Store struct {
	db *database.DB
}

// NewStore returns a Store backed by db.
func NewStore(db *database.DB) *Store { return &Store{db: db} }

const previewColumns = `node_id, content_hash, status, width, height, size_bytes, detail, created_at, updated_at`

// Get returns the preview record for a node, or ErrNotFound.
func (s *Store) Get(ctx context.Context, nodeID uuid.UUID) (*Record, error) {
	var r Record
	err := s.db.Pool().QueryRow(ctx,
		`SELECT `+previewColumns+` FROM previews WHERE node_id = $1`, nodeID).
		Scan(&r.NodeID, &r.ContentHash, &r.Status, &r.Width, &r.Height, &r.SizeBytes,
			&r.Detail, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("previews: get: %w", err)
	}
	return &r, nil
}

// Which returns the preview status of each of the given nodes that has a
// record. Batched, so a folder listing costs one query rather than one per row.
func (s *Store) Which(ctx context.Context, nodeIDs []uuid.UUID) (map[uuid.UUID]*Record, error) {
	out := make(map[uuid.UUID]*Record, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return out, nil
	}

	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+previewColumns+` FROM previews WHERE node_id = ANY($1)`, nodeIDs)
	if err != nil {
		return nil, fmt.Errorf("previews: which: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.NodeID, &r.ContentHash, &r.Status, &r.Width, &r.Height,
			&r.SizeBytes, &r.Detail, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("previews: scan: %w", err)
		}
		out[r.NodeID] = &r
	}
	return out, rows.Err()
}

// MarkPending records that generation has been queued for this content.
//
// The content hash is part of the update, so re-uploading different content
// over a file resets the record rather than leaving the old one's dimensions.
func (s *Store) MarkPending(ctx context.Context, nodeID uuid.UUID, contentHash string) error {
	_, err := s.db.Pool().Exec(ctx, `
		INSERT INTO previews (node_id, content_hash, status)
		VALUES ($1, $2, 'pending')
		ON CONFLICT (node_id) DO UPDATE
		SET content_hash = EXCLUDED.content_hash,
		    status       = 'pending',
		    width = NULL, height = NULL, size_bytes = NULL, detail = ''`,
		nodeID, contentHash)
	if err != nil {
		if database.IsForeignKeyViolation(err) {
			return ErrNotFound
		}
		return fmt.Errorf("previews: mark pending: %w", err)
	}
	return nil
}

// MarkReady records a successful generation.
//
// The content hash is included in the WHERE clause: if the file was replaced
// while the job was running, this update matches nothing and the thumbnail we
// just made — of content that is no longer there — is not advertised.
func (s *Store) MarkReady(ctx context.Context, nodeID uuid.UUID, contentHash string, t *Thumbnail) error {
	_, err := s.db.Pool().Exec(ctx, `
		UPDATE previews
		SET status = 'ready', width = $3, height = $4, size_bytes = $5, detail = ''
		WHERE node_id = $1 AND content_hash = $2`,
		nodeID, contentHash, t.Width, t.Height, int64(len(t.Data)))
	if err != nil {
		return fmt.Errorf("previews: mark ready: %w", err)
	}
	return nil
}

// MarkUnavailable records that no thumbnail can be made for this content.
//
// detail is for the administrator's eyes; it is never returned to a client,
// because it can describe why a decoder rejected a file and that is more
// internal detail than an end user needs.
func (s *Store) MarkUnavailable(ctx context.Context, nodeID uuid.UUID, contentHash, detail string) error {
	const maxDetail = 500
	if len(detail) > maxDetail {
		detail = detail[:maxDetail]
	}
	_, err := s.db.Pool().Exec(ctx, `
		UPDATE previews
		SET status = 'unavailable', width = NULL, height = NULL, size_bytes = NULL, detail = $3
		WHERE node_id = $1 AND content_hash = $2`,
		nodeID, contentHash, detail)
	if err != nil {
		return fmt.Errorf("previews: mark unavailable: %w", err)
	}
	return nil
}

// Delete removes a preview record.
func (s *Store) Delete(ctx context.Context, nodeID uuid.UUID) error {
	if _, err := s.db.Pool().Exec(ctx, `DELETE FROM previews WHERE node_id = $1`, nodeID); err != nil {
		return fmt.Errorf("previews: delete: %w", err)
	}
	return nil
}

// OrphanedHashes returns content digests that have a preview on disk but no
// live node referring to them, so the cleanup job can reclaim the space.
//
// A digest is only orphaned when *no* node uses it: two users may have uploaded
// identical images and be sharing one thumbnail.
func (s *Store) OrphanedHashes(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.db.Pool().Query(ctx, `
		SELECT DISTINCT p.content_hash
		FROM previews p
		WHERE NOT EXISTS (
		    SELECT 1 FROM nodes live
		    WHERE live.content_hash = p.content_hash AND live.deleted_at IS NULL
		)
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("previews: orphans: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("previews: scan orphan: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// PendingOlderThan returns previews stuck pending, so a generation lost to a
// crash between enqueue and completion is picked up again rather than showing a
// spinner forever.
func (s *Store) PendingOlderThan(ctx context.Context, age time.Duration, limit int) ([]*Record, error) {
	rows, err := s.db.Pool().Query(ctx, `
		SELECT `+previewColumns+`
		FROM previews
		WHERE status = 'pending' AND updated_at < now() - $1::interval
		ORDER BY updated_at
		LIMIT $2`, age, limit)
	if err != nil {
		return nil, fmt.Errorf("previews: stale pending: %w", err)
	}
	defer rows.Close()

	var out []*Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.NodeID, &r.ContentHash, &r.Status, &r.Width, &r.Height,
			&r.SizeBytes, &r.Detail, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("previews: scan: %w", err)
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}
