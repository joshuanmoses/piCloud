package permissions

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joshuanmoses/piCloud/backend/internal/database"
)

// Errors returned by this package.
var (
	// ErrNotFound means no such grant.
	ErrNotFound = errors.New("permissions: grant not found")
	// ErrSelfShare means somebody tried to share with themselves.
	ErrSelfShare = errors.New("permissions: cannot share with the owner")
)

// SubjectType distinguishes a user grant from a group grant.
type SubjectType string

const (
	SubjectUser  SubjectType = "user"
	SubjectGroup SubjectType = "group"
)

// Valid reports whether t is a recognised subject type.
func (t SubjectType) Valid() bool { return t == SubjectUser || t == SubjectGroup }

// Grant is one access-control entry.
type Grant struct {
	ID          uuid.UUID   `json:"id"`
	NodeID      uuid.UUID   `json:"node_id"`
	SubjectType SubjectType `json:"subject_type"`
	SubjectID   uuid.UUID   `json:"subject_id"`
	Permissions Set         `json:"-"`
	Deny        bool        `json:"deny"`
	Inherit     bool        `json:"inherit"`
	ExpiresAt   *time.Time  `json:"expires_at,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	CreatedBy   *uuid.UUID  `json:"-"`
	Message     string      `json:"message,omitempty"`
}

// IsActive reports whether the grant applies at the given moment.
func (g *Grant) IsActive(now time.Time) bool {
	return g.ExpiresAt == nil || g.ExpiresAt.After(now)
}

// Store persists and evaluates grants.
type Store struct {
	db  *database.DB
	now func() time.Time
}

// NewStore returns a Store backed by db.
func NewStore(db *database.DB) *Store {
	return &Store{db: db, now: time.Now}
}

// SetClock overrides the time source. For tests only.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

const grantColumns = `
	id, node_id, subject_type, subject_id, permissions, deny, inherit,
	expires_at, created_at, updated_at, created_by, message`

type scanner interface{ Scan(dest ...any) error }

func scanGrant(sc scanner) (*Grant, error) {
	var g Grant
	var bits int32
	err := sc.Scan(
		&g.ID, &g.NodeID, &g.SubjectType, &g.SubjectID, &bits, &g.Deny, &g.Inherit,
		&g.ExpiresAt, &g.CreatedAt, &g.UpdatedAt, &g.CreatedBy, &g.Message,
	)
	if err != nil {
		return nil, err
	}
	g.Permissions = Set(bits)
	return &g, nil
}

// --- Evaluation -------------------------------------------------------------

// Decision is the outcome of evaluating a node for a user.
type Decision struct {
	// Effective is what the user may do.
	Effective Set
	// IsOwner is true when the user owns the node, which implies everything.
	IsOwner bool
	// Inherited reports whether access came from an ancestor rather than a
	// grant on the node itself. Shown in the interface so a user can tell why
	// they can see something.
	Inherited bool
	// SourceNodeID names the node the deciding grant sits on.
	SourceNodeID *uuid.UUID
}

// Allows reports whether the decision permits the given permissions.
func (d Decision) Allows(want Set) bool { return d.Effective.Has(want) }

// Evaluate computes what userID may do to nodeID.
//
// The rules, in order:
//
//  1. The owner may do everything. Ownership is not expressible as a grant and
//     cannot be revoked by one.
//  2. Otherwise, collect every grant that applies: those on the node itself,
//     and those on any ancestor marked inheritable.
//  3. Grants apply to the user directly and through every group they belong to.
//     Effective access is the union — a user in two groups gets both.
//  4. A deny removes its permissions, at any depth, and always wins.
//
// Union rather than nearest-wins is deliberate. With group membership in play,
// "nearest" is ambiguous — two grants at the same depth from different groups
// have no natural precedence — and a rule nobody can predict is worse than a
// permissive one. Denials provide the escape hatch for carving exceptions.
func (s *Store) Evaluate(ctx context.Context, nodeID, userID uuid.UUID) (Decision, error) {
	now := s.now()

	// One query walks the ancestry and joins the grants that apply, so
	// evaluating a deeply nested node still costs a single round trip.
	rows, err := s.db.Pool().Query(ctx, `
		WITH RECURSIVE ancestry AS (
			SELECT id, parent_id, owner_id, 0 AS depth
			FROM nodes WHERE id = $1
			UNION ALL
			SELECT n.id, n.parent_id, n.owner_id, a.depth + 1
			FROM nodes n JOIN ancestry a ON n.id = a.parent_id
			WHERE a.depth < 128
		),
		subject_ids AS (
			SELECT 'user'::permission_subject AS subject_type, $2::uuid AS subject_id
			UNION ALL
			SELECT 'group'::permission_subject, gm.group_id
			FROM group_members gm WHERE gm.user_id = $2
		)
		SELECT
			a.depth,
			a.owner_id = $2 AS is_owner,
			p.permissions,
			p.deny,
			p.node_id
		FROM ancestry a
		LEFT JOIN permissions p
			ON p.node_id = a.id
			-- A grant on an ancestor only reaches here if it is inheritable.
			AND (a.depth = 0 OR p.inherit)
			AND (p.expires_at IS NULL OR p.expires_at > $3)
			AND EXISTS (
				SELECT 1 FROM subject_ids s
				WHERE s.subject_type = p.subject_type AND s.subject_id = p.subject_id
			)
		ORDER BY a.depth`,
		nodeID, userID, now)
	if err != nil {
		return Decision{}, fmt.Errorf("permissions: evaluate: %w", err)
	}
	defer rows.Close()

	var (
		found        bool
		granted      Set
		denied       Set
		isOwner      bool
		sourceNodeID *uuid.UUID
		sourceDepth  = -1
	)

	for rows.Next() {
		var depth int
		var rowIsOwner bool
		var bits *int32
		var deny *bool
		var grantNodeID *uuid.UUID

		if err := rows.Scan(&depth, &rowIsOwner, &bits, &deny, &grantNodeID); err != nil {
			return Decision{}, fmt.Errorf("permissions: scan grant: %w", err)
		}
		found = true

		// Ownership of the node itself, or of any ancestor. Owning a parent
		// folder means owning what is inside it.
		if rowIsOwner {
			isOwner = true
		}

		if bits == nil {
			continue
		}

		if deny != nil && *deny {
			denied |= Set(*bits)
			continue
		}

		granted |= Set(*bits)
		// Record the nearest granting node, for the "why can I see this?"
		// explanation in the interface.
		if sourceDepth == -1 || depth < sourceDepth {
			sourceDepth = depth
			sourceNodeID = grantNodeID
		}
	}
	if err := rows.Err(); err != nil {
		return Decision{}, fmt.Errorf("permissions: evaluate: %w", err)
	}

	if !found {
		// No such node. An empty decision denies everything, which is the
		// correct answer either way.
		return Decision{}, nil
	}

	if isOwner {
		return Decision{Effective: All, IsOwner: true}, nil
	}

	effective := granted &^ denied
	// A grant that survives its denial still implies being able to see the
	// item; a deny of Read alone removes everything.
	if effective != None && !denied.Has(Read) {
		effective |= Read
	}
	if denied.Has(Read) {
		effective = None
	}

	return Decision{
		Effective:    effective,
		Inherited:    sourceDepth > 0,
		SourceNodeID: sourceNodeID,
	}, nil
}

// EvaluateMany computes decisions for a set of nodes at once.
//
// Used when listing a folder: evaluating each child separately would be one
// query per row, which on a Raspberry Pi is the difference between a listing
// that appears instantly and one that takes a visible moment.
func (s *Store) EvaluateMany(
	ctx context.Context, nodeIDs []uuid.UUID, userID uuid.UUID,
) (map[uuid.UUID]Decision, error) {
	result := make(map[uuid.UUID]Decision, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return result, nil
	}

	now := s.now()

	rows, err := s.db.Pool().Query(ctx, `
		WITH RECURSIVE seeds AS (
			SELECT unnest($1::uuid[]) AS root_id
		),
		ancestry AS (
			SELECT s.root_id, n.id, n.parent_id, n.owner_id, 0 AS depth
			FROM seeds s JOIN nodes n ON n.id = s.root_id
			UNION ALL
			SELECT a.root_id, n.id, n.parent_id, n.owner_id, a.depth + 1
			FROM nodes n JOIN ancestry a ON n.id = a.parent_id
			WHERE a.depth < 128
		),
		subject_ids AS (
			SELECT 'user'::permission_subject AS subject_type, $2::uuid AS subject_id
			UNION ALL
			SELECT 'group'::permission_subject, gm.group_id
			FROM group_members gm WHERE gm.user_id = $2
		)
		SELECT
			a.root_id,
			a.depth,
			bool_or(a.owner_id = $2) AS is_owner,
			coalesce(bit_or(CASE WHEN NOT p.deny THEN p.permissions END), 0) AS granted,
			coalesce(bit_or(CASE WHEN p.deny THEN p.permissions END), 0) AS denied
		FROM ancestry a
		LEFT JOIN permissions p
			ON p.node_id = a.id
			AND (a.depth = 0 OR p.inherit)
			AND (p.expires_at IS NULL OR p.expires_at > $3)
			AND EXISTS (
				SELECT 1 FROM subject_ids s
				WHERE s.subject_type = p.subject_type AND s.subject_id = p.subject_id
			)
		GROUP BY a.root_id, a.depth
		ORDER BY a.root_id, a.depth`,
		nodeIDs, userID, now)
	if err != nil {
		return nil, fmt.Errorf("permissions: evaluate many: %w", err)
	}
	defer rows.Close()

	type accumulator struct {
		granted     Set
		denied      Set
		isOwner     bool
		sourceDepth int
	}
	acc := make(map[uuid.UUID]*accumulator, len(nodeIDs))

	for rows.Next() {
		var rootID uuid.UUID
		var depth int
		var isOwner bool
		var granted, denied int64

		if err := rows.Scan(&rootID, &depth, &isOwner, &granted, &denied); err != nil {
			return nil, fmt.Errorf("permissions: scan decision: %w", err)
		}

		entry, exists := acc[rootID]
		if !exists {
			entry = &accumulator{sourceDepth: -1}
			acc[rootID] = entry
		}
		if isOwner {
			entry.isOwner = true
		}
		entry.granted |= Set(granted)
		entry.denied |= Set(denied)
		if granted != 0 && (entry.sourceDepth == -1 || depth < entry.sourceDepth) {
			entry.sourceDepth = depth
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("permissions: evaluate many: %w", err)
	}

	for id, entry := range acc {
		if entry.isOwner {
			result[id] = Decision{Effective: All, IsOwner: true}
			continue
		}
		effective := entry.granted &^ entry.denied
		if entry.denied.Has(Read) {
			effective = None
		} else if effective != None {
			effective |= Read
		}
		result[id] = Decision{Effective: effective, Inherited: entry.sourceDepth > 0}
	}

	return result, nil
}

// --- Grant management -------------------------------------------------------

// GrantParams describes a grant to create or replace.
type GrantParams struct {
	NodeID      uuid.UUID
	SubjectType SubjectType
	SubjectID   uuid.UUID
	Permissions Set
	Deny        bool
	Inherit     bool
	ExpiresAt   *time.Time
	CreatedBy   uuid.UUID
	Message     string
}

// Grant creates or replaces a grant.
//
// Re-sharing with the same subject updates the existing row rather than adding
// a second one. Two grants for the same subject would have to be unioned on
// every evaluation and revoked separately, and a user who "removed" a share
// only to find access persisting would rightly consider that a bug.
func (s *Store) Grant(ctx context.Context, p GrantParams) (*Grant, error) {
	if !p.SubjectType.Valid() {
		return nil, fmt.Errorf("permissions: %q is not a valid subject type", p.SubjectType)
	}
	if p.Permissions == None {
		return nil, fmt.Errorf("permissions: refusing to create an empty grant")
	}
	if len(p.Message) > 500 {
		p.Message = p.Message[:500]
	}

	// A zero identifier means "no particular person" — a grant made by the
	// system rather than by a user — and must be stored as NULL, not as a
	// UUID that references nobody.
	var createdBy *uuid.UUID
	if p.CreatedBy != uuid.Nil {
		createdBy = &p.CreatedBy
	}

	row := s.db.Pool().QueryRow(ctx, `
		INSERT INTO permissions (
			node_id, subject_type, subject_id, permissions,
			deny, inherit, expires_at, created_by, message
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		-- deny is part of the key: a subject may hold one grant and one denial
		-- on a node, and re-sharing updates the grant while re-denying updates
		-- the denial. Without it a denial would overwrite the grant, so
		-- "you may read this but not delete it" would become "you may do
		-- nothing" — see migrations/0009.
		ON CONFLICT (node_id, subject_type, subject_id, deny) DO UPDATE SET
			permissions = EXCLUDED.permissions,
			inherit     = EXCLUDED.inherit,
			expires_at  = EXCLUDED.expires_at,
			message     = EXCLUDED.message
		RETURNING `+grantColumns,
		p.NodeID, p.SubjectType, p.SubjectID, int32(p.Permissions),
		p.Deny, p.Inherit, p.ExpiresAt, createdBy, p.Message)

	grant, err := scanGrant(row)
	if err != nil {
		return nil, fmt.Errorf("permissions: grant: %w", err)
	}
	return grant, nil
}

// Revoke removes a grant by identifier.
func (s *Store) Revoke(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Pool().Exec(ctx, `DELETE FROM permissions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("permissions: revoke: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeSubject removes a subject's grant on a node.
func (s *Store) RevokeSubject(
	ctx context.Context, nodeID uuid.UUID, subjectType SubjectType, subjectID uuid.UUID,
) error {
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM permissions
		 WHERE node_id = $1 AND subject_type = $2 AND subject_id = $3`,
		nodeID, subjectType, subjectID)
	if err != nil {
		return fmt.Errorf("permissions: revoke subject: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Get returns a grant by identifier.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (*Grant, error) {
	row := s.db.Pool().QueryRow(ctx, `SELECT `+grantColumns+` FROM permissions WHERE id = $1`, id)
	grant, err := scanGrant(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("permissions: get: %w", err)
	}
	return grant, nil
}

// ListForNode returns the grants on one node, most recent first.
func (s *Store) ListForNode(ctx context.Context, nodeID uuid.UUID) ([]*Grant, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+grantColumns+` FROM permissions WHERE node_id = $1 ORDER BY created_at DESC`,
		nodeID)
	if err != nil {
		return nil, fmt.Errorf("permissions: list for node: %w", err)
	}
	defer rows.Close()

	var out []*Grant
	for rows.Next() {
		grant, err := scanGrant(rows)
		if err != nil {
			return nil, fmt.Errorf("permissions: scan grant: %w", err)
		}
		out = append(out, grant)
	}
	return out, rows.Err()
}

// SharedNodes reports which of the given nodes carry a live grant to somebody
// other than their owner.
//
// This answers "have I shared this out?", which is the opposite question from
// Evaluate's "may I reach this?". A listing needs both: one decides what to
// offer the caller, the other tells an owner that an item is no longer private.
//
// Denials are excluded. A denial is a grant row, but publishing one does not
// make an item shared — it takes access away.
func (s *Store) SharedNodes(ctx context.Context, nodeIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := make(map[uuid.UUID]bool, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return out, nil
	}

	rows, err := s.db.Pool().Query(ctx,
		`SELECT DISTINCT node_id
		   FROM permissions
		  WHERE node_id = ANY($1)
		    AND NOT deny
		    AND (expires_at IS NULL OR expires_at > $2)`,
		nodeIDs, s.now())
	if err != nil {
		return nil, fmt.Errorf("permissions: shared nodes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("permissions: scan shared node: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// DeleteExpired removes grants whose expiry has passed. Called by the cleanup
// job; evaluation already ignores them, so this only reclaims rows.
func (s *Store) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM permissions WHERE expires_at IS NOT NULL AND expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("permissions: delete expired: %w", err)
	}
	return tag.RowsAffected(), nil
}
