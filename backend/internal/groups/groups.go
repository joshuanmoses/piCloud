// Package groups manages named collections of users, so a folder can be shared
// with "Accounting" rather than with eleven people individually — and so that
// adding somebody to the team gives them access without revisiting every share.
package groups

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joshuanmoses/piCloud/backend/internal/database"
	"github.com/joshuanmoses/piCloud/backend/internal/users"
)

// Errors returned by this package.
var (
	ErrNotFound  = errors.New("groups: not found")
	ErrNameTaken = errors.New("groups: a group with that name already exists")
	ErrNotMember = errors.New("groups: user is not a member")
)

// Role describes a member's standing within a group.
type Role string

const (
	// RoleMember belongs to the group and inherits its access.
	RoleMember Role = "member"
	// RoleManager may additionally add and remove members, without being a
	// full administrator. This is what lets a team lead manage their own team.
	RoleManager Role = "manager"
)

// Valid reports whether r is a recognised role.
func (r Role) Valid() bool { return r == RoleMember || r == RoleManager }

// Group is a named collection of users.
type Group struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// MemberCount is populated by listing queries.
	MemberCount int `json:"member_count"`
}

// Member is a user's membership of a group.
type Member struct {
	User    users.Public `json:"user"`
	Role    Role         `json:"role"`
	AddedAt time.Time    `json:"added_at"`
}

// Limits matching the database constraint.
const (
	MinNameLength     = 2
	MaxNameLength     = 64
	MaxDescriptionLen = 500
)

// ValidationError describes a rejected field, with a message for the user.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Reason }

// ValidateName enforces the same rules as the database constraint.
//
// The permitted set is narrow for the same reason usernames are: a group name
// appears in share dialogs and audit logs, and arbitrary Unicode would let one
// group be made to look like another.
func ValidateName(name string) error {
	trimmed := strings.TrimSpace(name)
	n := len([]rune(trimmed))

	switch {
	case n == 0:
		return &ValidationError{"name", "Enter a group name."}
	case n < MinNameLength:
		return &ValidationError{"name", fmt.Sprintf("Group name must be at least %d characters.", MinNameLength)}
	case n > MaxNameLength:
		return &ValidationError{"name", fmt.Sprintf("Group name must be no more than %d characters.", MaxNameLength)}
	}

	for i, r := range trimmed {
		if r > unicode.MaxASCII {
			return &ValidationError{"name", "Group name may contain only letters, numbers, spaces, dots, hyphens, and underscores."}
		}
		isAlnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if i == 0 && !isAlnum {
			return &ValidationError{"name", "Group name must begin with a letter or a number."}
		}
		if !isAlnum && r != ' ' && r != '.' && r != '_' && r != '-' {
			return &ValidationError{"name", "Group name may contain only letters, numbers, spaces, dots, hyphens, and underscores."}
		}
	}
	return nil
}

// Store persists groups and their membership.
type Store struct {
	db *database.DB
}

// NewStore returns a Store backed by db.
func NewStore(db *database.DB) *Store { return &Store{db: db} }

const columns = `g.id, g.name, g.description, g.created_at, g.updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanGroup(sc scanner, withCount bool) (*Group, error) {
	var g Group
	var err error
	if withCount {
		err = sc.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt, &g.MemberCount)
	} else {
		err = sc.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt)
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// Create makes a new group.
func (s *Store) Create(ctx context.Context, name, description string) (*Group, error) {
	name = strings.TrimSpace(name)
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	description = strings.TrimSpace(description)
	if len(description) > MaxDescriptionLen {
		return nil, &ValidationError{"description", fmt.Sprintf("Description must be no more than %d characters.", MaxDescriptionLen)}
	}

	row := s.db.Pool().QueryRow(ctx,
		`INSERT INTO groups (name, description) VALUES ($1, $2)
		 RETURNING id, name, description, created_at, updated_at`,
		name, description)

	group, err := scanGroup(row, false)
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, ErrNameTaken
		}
		return nil, fmt.Errorf("groups: create: %w", err)
	}
	return group, nil
}

// Get returns a group by identifier.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (*Group, error) {
	row := s.db.Pool().QueryRow(ctx, `
		SELECT `+columns+`, (SELECT count(*) FROM group_members m WHERE m.group_id = g.id)
		FROM groups g WHERE g.id = $1`, id)

	group, err := scanGroup(row, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("groups: get: %w", err)
	}
	return group, nil
}

// List returns groups matching a search term, with member counts.
func (s *Store) List(ctx context.Context, search string, limit int) ([]*Group, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}

	var pattern *string
	if search = strings.TrimSpace(search); search != "" {
		p := "%" + search + "%"
		pattern = &p
	}

	rows, err := s.db.Pool().Query(ctx, `
		SELECT `+columns+`, (SELECT count(*) FROM group_members m WHERE m.group_id = g.id)
		FROM groups g
		WHERE ($1::text IS NULL OR g.name ILIKE $1 OR g.description ILIKE $1)
		ORDER BY g.name LIMIT $2`, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("groups: list: %w", err)
	}
	defer rows.Close()

	var out []*Group
	for rows.Next() {
		group, err := scanGroup(rows, true)
		if err != nil {
			return nil, fmt.Errorf("groups: scan: %w", err)
		}
		out = append(out, group)
	}
	return out, rows.Err()
}

// ListForUser returns the groups a user belongs to.
func (s *Store) ListForUser(ctx context.Context, userID uuid.UUID) ([]*Group, error) {
	rows, err := s.db.Pool().Query(ctx, `
		SELECT `+columns+`, (SELECT count(*) FROM group_members m2 WHERE m2.group_id = g.id)
		FROM groups g
		JOIN group_members m ON m.group_id = g.id
		WHERE m.user_id = $1
		ORDER BY g.name`, userID)
	if err != nil {
		return nil, fmt.Errorf("groups: list for user: %w", err)
	}
	defer rows.Close()

	var out []*Group
	for rows.Next() {
		group, err := scanGroup(rows, true)
		if err != nil {
			return nil, fmt.Errorf("groups: scan: %w", err)
		}
		out = append(out, group)
	}
	return out, rows.Err()
}

// Update changes a group's name or description.
func (s *Store) Update(ctx context.Context, id uuid.UUID, name, description *string) (*Group, error) {
	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if err := ValidateName(trimmed); err != nil {
			return nil, err
		}
		name = &trimmed
	}

	row := s.db.Pool().QueryRow(ctx, `
		UPDATE groups SET
			name = coalesce($2, name),
			description = coalesce($3, description)
		WHERE id = $1
		RETURNING id, name, description, created_at, updated_at`,
		id, name, description)

	group, err := scanGroup(row, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, ErrNameTaken
		}
		return nil, fmt.Errorf("groups: update: %w", err)
	}
	return group, nil
}

// Delete removes a group.
//
// Its grants go with it, through the trigger installed by the permissions
// migration: access must not survive the group it was given to.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Pool().Exec(ctx, `DELETE FROM groups WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("groups: delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AddMember adds a user to a group, or updates their role.
func (s *Store) AddMember(
	ctx context.Context, groupID, userID uuid.UUID, role Role, addedBy uuid.UUID,
) error {
	if !role.Valid() {
		return fmt.Errorf("groups: %q is not a valid role", role)
	}

	_, err := s.db.Pool().Exec(ctx, `
		INSERT INTO group_members (group_id, user_id, role, added_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, group_id) DO UPDATE SET role = EXCLUDED.role`,
		groupID, userID, role, addedBy)
	if err != nil {
		if database.IsForeignKeyViolation(err) {
			return ErrNotFound
		}
		return fmt.Errorf("groups: add member: %w", err)
	}
	return nil
}

// RemoveMember removes a user from a group.
func (s *Store) RemoveMember(ctx context.Context, groupID, userID uuid.UUID) error {
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM group_members WHERE group_id = $1 AND user_id = $2`, groupID, userID)
	if err != nil {
		return fmt.Errorf("groups: remove member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotMember
	}
	return nil
}

// ListMembers returns a group's members.
func (s *Store) ListMembers(ctx context.Context, groupID uuid.UUID) ([]Member, error) {
	rows, err := s.db.Pool().Query(ctx, `
		SELECT u.id, u.username, u.display_name, m.role, m.added_at
		FROM group_members m
		JOIN users u ON u.id = m.user_id
		WHERE m.group_id = $1
		ORDER BY u.username`, groupID)
	if err != nil {
		return nil, fmt.Errorf("groups: list members: %w", err)
	}
	defer rows.Close()

	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.User.ID, &m.User.Username, &m.User.DisplayName, &m.Role, &m.AddedAt); err != nil {
			return nil, fmt.Errorf("groups: scan member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// IsMember reports whether a user belongs to a group.
func (s *Store) IsMember(ctx context.Context, groupID, userID uuid.UUID) (bool, error) {
	var exists bool
	err := s.db.Pool().QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM group_members WHERE group_id = $1 AND user_id = $2)`,
		groupID, userID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("groups: is member: %w", err)
	}
	return exists, nil
}

// IsManager reports whether a user may administer a group's membership.
func (s *Store) IsManager(ctx context.Context, groupID, userID uuid.UUID) (bool, error) {
	var isManager bool
	err := s.db.Pool().QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM group_members
			WHERE group_id = $1 AND user_id = $2 AND role = 'manager'
		)`, groupID, userID).Scan(&isManager)
	if err != nil {
		return false, fmt.Errorf("groups: is manager: %w", err)
	}
	return isManager, nil
}
