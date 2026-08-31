// Package tags implements user-defined labels on files.
//
// Tags are owner-scoped: two users may each have a tag called "Urgent" without
// collision, and one renaming theirs does not touch the other's. A tag applied
// to a shared file is visible only to the person who applied it, which is what
// people expect from a personal label — a colleague's "Rejected" tag on a
// document you both use would be startling.
package tags

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/database"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
)

// Errors returned by this package.
var (
	// ErrNotFound means no such tag, or none belonging to this user.
	ErrNotFound = errors.New("tags: not found")
	// ErrNameTaken means the user already has a tag with this name.
	ErrNameTaken = errors.New("tags: you already have a tag with that name")
	// ErrInvalidName means the name failed validation.
	ErrInvalidName = errors.New("tags: invalid name")
	// ErrInvalidColor means the colour is not a six-digit hex value.
	ErrInvalidColor = errors.New("tags: colour must be a hex value such as #4a90d9")
	// ErrTooMany means the user has reached MaxPerUser.
	ErrTooMany = errors.New("tags: you have reached the maximum number of tags")
)

// Limits.
const (
	// MaxNameLength matches the database constraint.
	MaxNameLength = 40
	// MaxPerUser bounds how many tags one person may create. Generous for
	// organising, low enough that a script cannot fill the table.
	MaxPerUser = 200
	// MaxPerNode bounds how many tags may be applied to one file.
	MaxPerNode = 25
)

// Tag is a user's label.
type Tag struct {
	ID      uuid.UUID `json:"id"`
	OwnerID uuid.UUID `json:"-"`
	Name    string    `json:"name"`
	// Color is "#rrggbb", or empty for the interface default.
	Color     string    `json:"color,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// FileCount is populated by List, so the interface can show how much each
	// tag is used without a query per tag.
	FileCount int `json:"file_count"`
}

var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// NormalizeName trims and validates a tag name.
//
// Names are compared case-insensitively by the database (the column is CITEXT),
// so "urgent" and "Urgent" are one tag. The stored form keeps the user's
// capitalisation.
func NormalizeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	// Collapse internal whitespace, so "very  urgent" and "very urgent" do not
	// become two tags that look identical in a list.
	name = strings.Join(strings.Fields(name), " ")

	if name == "" {
		return "", fmt.Errorf("%w: a tag needs a name", ErrInvalidName)
	}
	if len([]rune(name)) > MaxNameLength {
		return "", fmt.Errorf("%w: a tag name may be at most %d characters", ErrInvalidName, MaxNameLength)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: a tag name cannot contain control characters", ErrInvalidName)
		}
	}
	return name, nil
}

// NormalizeColor validates a colour, accepting empty for "use the default".
func NormalizeColor(raw string) (string, error) {
	color := strings.TrimSpace(raw)
	if color == "" {
		return "", nil
	}
	if !colorPattern.MatchString(color) {
		return "", ErrInvalidColor
	}
	return strings.ToLower(color), nil
}

// Store persists tags and their assignments.
type Store struct {
	db *database.DB
}

// NewStore returns a Store backed by db.
func NewStore(db *database.DB) *Store { return &Store{db: db} }

// Create makes a new tag for a user.
func (s *Store) Create(ctx context.Context, ownerID uuid.UUID, rawName, rawColor string) (*Tag, error) {
	name, err := NormalizeName(rawName)
	if err != nil {
		return nil, err
	}
	color, err := NormalizeColor(rawColor)
	if err != nil {
		return nil, err
	}

	var count int
	if err := s.db.Pool().QueryRow(ctx,
		`SELECT count(*) FROM tags WHERE owner_id = $1`, ownerID).Scan(&count); err != nil {
		return nil, fmt.Errorf("tags: count: %w", err)
	}
	if count >= MaxPerUser {
		return nil, ErrTooMany
	}

	var t Tag
	err = s.db.Pool().QueryRow(ctx, `
		INSERT INTO tags (owner_id, name, color) VALUES ($1, $2, $3)
		RETURNING id, owner_id, name, color, created_at`,
		ownerID, name, color).Scan(&t.ID, &t.OwnerID, &t.Name, &t.Color, &t.CreatedAt)
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, ErrNameTaken
		}
		return nil, fmt.Errorf("tags: create: %w", err)
	}
	return &t, nil
}

// Get returns one of a user's tags.
//
// Scoped to the owner, so asking for somebody else's tag by ID reports not
// found rather than confirming it exists.
func (s *Store) Get(ctx context.Context, ownerID, tagID uuid.UUID) (*Tag, error) {
	var t Tag
	err := s.db.Pool().QueryRow(ctx,
		`SELECT id, owner_id, name, color, created_at FROM tags WHERE id = $1 AND owner_id = $2`,
		tagID, ownerID).Scan(&t.ID, &t.OwnerID, &t.Name, &t.Color, &t.CreatedAt)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("tags: get: %w", err)
	}
	return &t, nil
}

// List returns a user's tags with usage counts, alphabetically.
func (s *Store) List(ctx context.Context, ownerID uuid.UUID) ([]*Tag, error) {
	rows, err := s.db.Pool().Query(ctx, `
		SELECT t.id, t.owner_id, t.name, t.color, t.created_at,
		       count(ft.node_id) FILTER (WHERE n.deleted_at IS NULL) AS file_count
		FROM tags t
		LEFT JOIN file_tags ft ON ft.tag_id = t.id
		LEFT JOIN nodes n ON n.id = ft.node_id
		WHERE t.owner_id = $1
		GROUP BY t.id
		ORDER BY t.name`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("tags: list: %w", err)
	}
	defer rows.Close()

	out := make([]*Tag, 0, 16)
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.OwnerID, &t.Name, &t.Color, &t.CreatedAt, &t.FileCount); err != nil {
			return nil, fmt.Errorf("tags: scan: %w", err)
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// Update renames or recolours a tag.
func (s *Store) Update(ctx context.Context, ownerID, tagID uuid.UUID, rawName, rawColor string) (*Tag, error) {
	name, err := NormalizeName(rawName)
	if err != nil {
		return nil, err
	}
	color, err := NormalizeColor(rawColor)
	if err != nil {
		return nil, err
	}

	var t Tag
	err = s.db.Pool().QueryRow(ctx, `
		UPDATE tags SET name = $3, color = $4
		WHERE id = $1 AND owner_id = $2
		RETURNING id, owner_id, name, color, created_at`,
		tagID, ownerID, name, color).Scan(&t.ID, &t.OwnerID, &t.Name, &t.Color, &t.CreatedAt)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, ErrNotFound
		}
		if database.IsUniqueViolation(err) {
			return nil, ErrNameTaken
		}
		return nil, fmt.Errorf("tags: update: %w", err)
	}
	return &t, nil
}

// Delete removes a tag and every assignment of it.
func (s *Store) Delete(ctx context.Context, ownerID, tagID uuid.UUID) error {
	tag, err := s.db.Pool().Exec(ctx, `DELETE FROM tags WHERE id = $1 AND owner_id = $2`, tagID, ownerID)
	if err != nil {
		return fmt.Errorf("tags: delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Apply attaches a tag to a node. Applying twice is a no-op.
//
// The caller must already have established that the user can see the node;
// this checks only that the tag is theirs.
func (s *Store) Apply(ctx context.Context, ownerID, nodeID, tagID uuid.UUID) error {
	if _, err := s.Get(ctx, ownerID, tagID); err != nil {
		return err
	}

	var count int
	if err := s.db.Pool().QueryRow(ctx, `
		SELECT count(*) FROM file_tags ft
		JOIN tags t ON t.id = ft.tag_id
		WHERE ft.node_id = $1 AND t.owner_id = $2`, nodeID, ownerID).Scan(&count); err != nil {
		return fmt.Errorf("tags: count on node: %w", err)
	}
	if count >= MaxPerNode {
		return fmt.Errorf("%w: a file may carry at most %d of your tags", ErrTooMany, MaxPerNode)
	}

	_, err := s.db.Pool().Exec(ctx,
		`INSERT INTO file_tags (node_id, tag_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		nodeID, tagID)
	if err != nil {
		if database.IsForeignKeyViolation(err) {
			return nodes.ErrNotFound
		}
		return fmt.Errorf("tags: apply: %w", err)
	}
	return nil
}

// Remove detaches a tag from a node.
func (s *Store) Remove(ctx context.Context, ownerID, nodeID, tagID uuid.UUID) error {
	_, err := s.db.Pool().Exec(ctx, `
		DELETE FROM file_tags
		WHERE node_id = $1 AND tag_id = $2
		  AND EXISTS (SELECT 1 FROM tags WHERE id = $2 AND owner_id = $3)`,
		nodeID, tagID, ownerID)
	if err != nil {
		return fmt.Errorf("tags: remove: %w", err)
	}
	return nil
}

// ForNode returns the caller's tags on one node.
func (s *Store) ForNode(ctx context.Context, ownerID, nodeID uuid.UUID) ([]*Tag, error) {
	rows, err := s.db.Pool().Query(ctx, `
		SELECT t.id, t.owner_id, t.name, t.color, t.created_at
		FROM file_tags ft
		JOIN tags t ON t.id = ft.tag_id
		WHERE ft.node_id = $1 AND t.owner_id = $2
		ORDER BY t.name`, nodeID, ownerID)
	if err != nil {
		return nil, fmt.Errorf("tags: for node: %w", err)
	}
	defer rows.Close()

	out := make([]*Tag, 0, 8)
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.OwnerID, &t.Name, &t.Color, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("tags: scan: %w", err)
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// ForNodes returns the caller's tags on many nodes at once, so a folder listing
// costs one query rather than one per row.
func (s *Store) ForNodes(ctx context.Context, ownerID uuid.UUID, nodeIDs []uuid.UUID) (map[uuid.UUID][]*Tag, error) {
	out := make(map[uuid.UUID][]*Tag, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return out, nil
	}

	rows, err := s.db.Pool().Query(ctx, `
		SELECT ft.node_id, t.id, t.owner_id, t.name, t.color, t.created_at
		FROM file_tags ft
		JOIN tags t ON t.id = ft.tag_id
		WHERE ft.node_id = ANY($1) AND t.owner_id = $2
		ORDER BY t.name`, nodeIDs, ownerID)
	if err != nil {
		return nil, fmt.Errorf("tags: for nodes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var nodeID uuid.UUID
		var t Tag
		if err := rows.Scan(&nodeID, &t.ID, &t.OwnerID, &t.Name, &t.Color, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("tags: scan: %w", err)
		}
		out[nodeID] = append(out[nodeID], &t)
	}
	return out, rows.Err()
}

// ResolveNames maps tag names to the caller's tag IDs, for search filters.
// Names that match nothing are skipped: filtering by a tag you do not have
// should return nothing, not everything.
func (s *Store) ResolveNames(ctx context.Context, ownerID uuid.UUID, names []string) ([]uuid.UUID, error) {
	if len(names) == 0 {
		return nil, nil
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT id FROM tags WHERE owner_id = $1 AND name = ANY($2)`, ownerID, names)
	if err != nil {
		return nil, fmt.Errorf("tags: resolve: %w", err)
	}
	defer rows.Close()

	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("tags: scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
