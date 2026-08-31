// Package favorites implements per-user bookmarks.
//
// Favourites are private to each user: two people may each favourite the same
// shared folder, and one removing it must not affect the other. That is why the
// table is keyed on (user, node) rather than living on the node itself.
package favorites

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/database"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
)

// Store persists favourites.
type Store struct {
	db *database.DB
}

// NewStore returns a Store backed by db.
func NewStore(db *database.DB) *Store { return &Store{db: db} }

// Add marks a node as a favourite. Adding twice is a no-op.
func (s *Store) Add(ctx context.Context, userID, nodeID uuid.UUID) error {
	_, err := s.db.Pool().Exec(ctx,
		`INSERT INTO favorites (user_id, node_id) VALUES ($1, $2)
		 ON CONFLICT (user_id, node_id) DO NOTHING`, userID, nodeID)
	if err != nil {
		if database.IsForeignKeyViolation(err) {
			return nodes.ErrNotFound
		}
		return fmt.Errorf("favorites: add: %w", err)
	}
	return nil
}

// Remove unmarks a node. Removing something that is not a favourite is a
// no-op: the caller wanted it gone, and it is.
func (s *Store) Remove(ctx context.Context, userID, nodeID uuid.UUID) error {
	if _, err := s.db.Pool().Exec(ctx,
		`DELETE FROM favorites WHERE user_id = $1 AND node_id = $2`, userID, nodeID); err != nil {
		return fmt.Errorf("favorites: remove: %w", err)
	}
	return nil
}

// IsFavorite reports whether a node is favourited.
func (s *Store) IsFavorite(ctx context.Context, userID, nodeID uuid.UUID) (bool, error) {
	var exists bool
	err := s.db.Pool().QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM favorites WHERE user_id = $1 AND node_id = $2)`,
		userID, nodeID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("favorites: check: %w", err)
	}
	return exists, nil
}

// Which returns the subset of the given nodes that the user has favourited.
//
// Batched, so a folder listing costs one query rather than one per row.
func (s *Store) Which(ctx context.Context, userID uuid.UUID, nodeIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	result := make(map[uuid.UUID]bool, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return result, nil
	}

	rows, err := s.db.Pool().Query(ctx,
		`SELECT node_id FROM favorites WHERE user_id = $1 AND node_id = ANY($2)`,
		userID, nodeIDs)
	if err != nil {
		return nil, fmt.Errorf("favorites: which: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("favorites: scan: %w", err)
		}
		result[id] = true
	}
	return result, rows.Err()
}

// List returns a user's favourites, most recently added first.
//
// Deleted content is excluded, and — importantly — so is anything the user can
// no longer see. Somebody who favourited a shared folder and then had the
// share revoked must not keep a working entry in their list.
func (s *Store) List(ctx context.Context, userID uuid.UUID, limit, offset int) ([]*nodes.Node, int, error) {
	if limit <= 0 || limit > nodes.MaxPageSize {
		limit = nodes.DefaultPageSize
	}

	const visibility = `
		FROM favorites f
		JOIN nodes n ON n.id = f.node_id
		WHERE f.user_id = $1
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
		return nil, 0, fmt.Errorf("favorites: count: %w", err)
	}

	rows, err := s.db.Pool().Query(ctx, `
		SELECT n.id, n.owner_id, n.parent_id, n.kind, n.name, n.size_bytes, n.mime_type,
		       n.content_hash, n.created_at, n.updated_at, n.deleted_at, n.created_by, n.modified_by
		`+visibility+`
		ORDER BY f.created_at DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("favorites: list: %w", err)
	}
	defer rows.Close()

	out := make([]*nodes.Node, 0, limit)
	for rows.Next() {
		var n nodes.Node
		if err := rows.Scan(
			&n.ID, &n.OwnerID, &n.ParentID, &n.Kind, &n.Name, &n.SizeBytes, &n.MimeType,
			&n.ContentHash, &n.CreatedAt, &n.UpdatedAt, &n.DeletedAt, &n.CreatedBy, &n.ModifiedBy,
		); err != nil {
			return nil, 0, fmt.Errorf("favorites: scan: %w", err)
		}
		out = append(out, &n)
	}
	return out, total, rows.Err()
}
