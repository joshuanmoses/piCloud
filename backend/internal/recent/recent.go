// Package recent tracks what each user last touched.
//
// One row per (user, node, action), updated rather than appended. An
// append-only activity log would grow without bound and would show the same
// file twenty times after twenty downloads; what the Recent view actually
// wants is "when did I last touch this", which is one row.
package recent

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/database"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
)

// Action is how a node was touched.
type Action string

const (
	ActionViewed     Action = "viewed"
	ActionDownloaded Action = "downloaded"
	ActionUploaded   Action = "uploaded"
	ActionModified   Action = "modified"
)

// Valid reports whether a is a recognised action.
func (a Action) Valid() bool {
	switch a {
	case ActionViewed, ActionDownloaded, ActionUploaded, ActionModified:
		return true
	}
	return false
}

// Entry is one recent activity record.
type Entry struct {
	Node       *nodes.Node `json:"node"`
	Action     Action      `json:"action"`
	OccurredAt time.Time   `json:"occurred_at"`
	// Shared reports whether the caller reaches this through a grant.
	Shared bool `json:"shared"`
}

// Store persists recent activity.
type Store struct {
	db *database.DB
}

// NewStore returns a Store backed by db.
func NewStore(db *database.DB) *Store { return &Store{db: db} }

// Record notes that a user touched a node.
//
// Failures are the caller's to ignore: recording activity is a convenience,
// and a download must not fail because the history could not be written.
func (s *Store) Record(ctx context.Context, userID, nodeID uuid.UUID, action Action) error {
	if !action.Valid() {
		return fmt.Errorf("recent: %q is not a valid action", action)
	}

	_, err := s.db.Pool().Exec(ctx, `
		INSERT INTO recent_items (user_id, node_id, action, occurred_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (user_id, node_id, action)
		DO UPDATE SET occurred_at = now()`, userID, nodeID, action)
	if err != nil {
		if database.IsForeignKeyViolation(err) {
			// The node was deleted between the action and the record.
			return nil
		}
		return fmt.Errorf("recent: record: %w", err)
	}
	return nil
}

// List returns a user's recent activity, newest first.
//
// Only what they can still see: a file shared and then unshared must not stay
// visible here, and a listing that showed items the user can no longer open
// would be worse than useless.
func (s *Store) List(ctx context.Context, userID uuid.UUID, limit, offset int) ([]Entry, int, error) {
	if limit <= 0 || limit > nodes.MaxPageSize {
		limit = nodes.DefaultPageSize
	}

	const visibility = `
		FROM recent_items r
		JOIN nodes n ON n.id = r.node_id
		WHERE r.user_id = $1
		  AND n.deleted_at IS NULL
		  AND (
		    n.owner_id = $1
		    OR EXISTS (
		      WITH RECURSIVE ancestry AS (
		        SELECT n.id, n.parent_id, 0 AS depth
		        UNION ALL
		        SELECT a2.id, a2.parent_id, ancestry.depth + 1
		        FROM nodes a2 JOIN ancestry ON a2.id = ancestry.parent_id
		        WHERE ancestry.depth < 64
		      )
		      SELECT 1 FROM permissions p
		      JOIN ancestry a ON a.id = p.node_id
		      WHERE p.deny = FALSE
		        AND (a.depth = 0 OR p.inherit)
		        AND (p.expires_at IS NULL OR p.expires_at > now())
		        AND (
		          (p.subject_type = 'user' AND p.subject_id = $1)
		          OR (p.subject_type = 'group' AND p.subject_id IN (
		                SELECT group_id FROM group_members WHERE user_id = $1))
		        )
		    )
		  )`

	var total int
	if err := s.db.Pool().QueryRow(ctx, `SELECT count(*)`+visibility, userID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("recent: count: %w", err)
	}

	rows, err := s.db.Pool().Query(ctx, `
		SELECT n.id, n.owner_id, n.parent_id, n.kind, n.name, n.size_bytes, n.mime_type,
		       n.content_hash, n.created_at, n.updated_at, n.deleted_at, n.created_by, n.modified_by,
		       r.action, r.occurred_at, n.owner_id <> $1
		`+visibility+`
		ORDER BY r.occurred_at DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("recent: list: %w", err)
	}
	defer rows.Close()

	out := make([]Entry, 0, limit)
	for rows.Next() {
		var n nodes.Node
		var e Entry
		if err := rows.Scan(
			&n.ID, &n.OwnerID, &n.ParentID, &n.Kind, &n.Name, &n.SizeBytes, &n.MimeType,
			&n.ContentHash, &n.CreatedAt, &n.UpdatedAt, &n.DeletedAt, &n.CreatedBy, &n.ModifiedBy,
			&e.Action, &e.OccurredAt, &e.Shared,
		); err != nil {
			return nil, 0, fmt.Errorf("recent: scan: %w", err)
		}
		e.Node = &n
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// DeleteOlderThan prunes history. Called by the cleanup job.
func (s *Store) DeleteOlderThan(ctx context.Context, age time.Duration) (int64, error) {
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM recent_items WHERE occurred_at < now() - $1::interval`, age)
	if err != nil {
		return 0, fmt.Errorf("recent: prune: %w", err)
	}
	return tag.RowsAffected(), nil
}
