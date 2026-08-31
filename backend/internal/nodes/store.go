package nodes

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joshuanmoses/piCloud/backend/internal/database"
)

// Versioner preserves a file's content before it is overwritten.
//
// The nodes package defines the interface and the versions package implements
// it, rather than the other way round: versioning needs to know what a node is,
// but a node does not need to know what a version is. The dependency points one
// way and there is no cycle.
//
// The two methods differ only in the reason they record, which is worth
// distinguishing because restoring must never look like an ordinary edit in the
// history.
//
// A implementation must NOT take a new blob reference. The reference the node
// was holding transfers to the version, so the caller does not release it —
// one reference in, one reference out, and no moment when the count is wrong.
type Versioner interface {
	SnapshotBeforeReplace(ctx context.Context, tx pgx.Tx, node *Node, actorID uuid.UUID) error
	SnapshotBeforeRestore(ctx context.Context, tx pgx.Tx, node *Node, actorID uuid.UUID) error
}

// Store persists the filesystem tree.
//
// Every operation that touches both metadata and quota runs in a transaction.
// The ordering rule throughout: content is written to disk and linked into
// place *before* the metadata commit, so a failure can leave an unreferenced
// blob — which the reconciliation job sweeps — but never a metadata row
// pointing at bytes that were never written.
type Store struct {
	db *database.DB
	// versioner is optional. Nil means version history is switched off, in
	// which case a replaced file's previous content is dereferenced and
	// eventually reclaimed, exactly as it was before Phase 8.
	versioner Versioner
}

// NewStore returns a Store backed by db.
func NewStore(db *database.DB) *Store { return &Store{db: db} }

// SetVersioner attaches version history. Called once at startup; nil switches
// it off.
func (s *Store) SetVersioner(v Versioner) { s.versioner = v }

const columns = `
	id, owner_id, parent_id, kind, name, size_bytes, mime_type, content_hash,
	created_at, updated_at, deleted_at, created_by, modified_by`

type scanner interface{ Scan(dest ...any) error }

func scanNode(s scanner) (*Node, error) {
	var n Node
	err := s.Scan(
		&n.ID, &n.OwnerID, &n.ParentID, &n.Kind, &n.Name, &n.SizeBytes,
		&n.MimeType, &n.ContentHash, &n.CreatedAt, &n.UpdatedAt, &n.DeletedAt,
		&n.CreatedBy, &n.ModifiedBy,
	)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// --- Reads ------------------------------------------------------------------

// Root returns a user's root folder.
func (s *Store) Root(ctx context.Context, ownerID uuid.UUID) (*Node, error) {
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+columns+` FROM nodes
		 WHERE owner_id = $1 AND parent_id IS NULL AND deleted_at IS NULL`, ownerID)
	n, err := scanNode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// The root is created by a trigger when the account is created, so its
		// absence is a data-integrity problem, not a normal condition.
		return nil, fmt.Errorf("nodes: user %s has no root folder: %w", ownerID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("nodes: load root: %w", err)
	}
	return n, nil
}

// Get returns a live node by identifier.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (*Node, error) {
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+columns+` FROM nodes WHERE id = $1 AND deleted_at IS NULL`, id)
	n, err := scanNode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("nodes: get: %w", err)
	}
	return n, nil
}

// GetIncludingDeleted returns a node whether or not it is in Trash.
func (s *Store) GetIncludingDeleted(ctx context.Context, id uuid.UUID) (*Node, error) {
	row := s.db.Pool().QueryRow(ctx, `SELECT `+columns+` FROM nodes WHERE id = $1`, id)
	n, err := scanNode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("nodes: get: %w", err)
	}
	return n, nil
}

// SortField names a column a listing may be ordered by.
type SortField string

const (
	SortByName     SortField = "name"
	SortBySize     SortField = "size"
	SortByModified SortField = "modified"
	SortByCreated  SortField = "created"
)

// Valid reports whether f is a recognised sort field.
func (f SortField) Valid() bool {
	switch f {
	case SortByName, SortBySize, SortByModified, SortByCreated:
		return true
	}
	return false
}

// ListParams controls a folder listing.
type ListParams struct {
	ParentID uuid.UUID
	Limit    int
	Offset   int
	Sort     SortField
	// Descending reverses the sort.
	Descending bool
	// KindFilter restricts to files or folders. Empty means both.
	KindFilter Kind
}

// Page is one page of a listing.
type Page struct {
	Nodes []*Node `json:"nodes"`
	Total int     `json:"total"`
	// HasMore lets the client show a "load more" affordance without arithmetic.
	HasMore bool `json:"has_more"`
}

// DefaultPageSize and MaxPageSize bound a listing.
//
// A folder with tens of thousands of children must never be materialised in
// one response: on a Raspberry Pi that is both a memory problem for the server
// and an unusable payload for the browser.
const (
	DefaultPageSize = 100
	MaxPageSize     = 500
)

// List returns the children of a folder, sorted with folders first.
func (s *Store) List(ctx context.Context, p ListParams) (*Page, error) {
	if p.Limit <= 0 {
		p.Limit = DefaultPageSize
	}
	if p.Limit > MaxPageSize {
		p.Limit = MaxPageSize
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	if !p.Sort.Valid() {
		p.Sort = SortByName
	}

	var kindFilter *string
	if p.KindFilter.Valid() {
		k := string(p.KindFilter)
		kindFilter = &k
	}

	var total int
	err := s.db.Pool().QueryRow(ctx, `
		SELECT count(*) FROM nodes
		WHERE parent_id = $1 AND deleted_at IS NULL
		  AND ($2::text IS NULL OR kind::text = $2)`,
		p.ParentID, kindFilter).Scan(&total)
	if err != nil {
		return nil, fmt.Errorf("nodes: count children: %w", err)
	}

	// The ORDER BY is built from a validated enum, never from request text.
	// Folders always sort before files, which is what every file manager does
	// and what users expect regardless of the chosen column.
	orderBy := orderClause(p.Sort, p.Descending)

	rows, err := s.db.Pool().Query(ctx, `
		SELECT `+columns+` FROM nodes
		WHERE parent_id = $1 AND deleted_at IS NULL
		  AND ($2::text IS NULL OR kind::text = $2)
		ORDER BY `+orderBy+`
		LIMIT $3 OFFSET $4`,
		p.ParentID, kindFilter, p.Limit, p.Offset)
	if err != nil {
		return nil, fmt.Errorf("nodes: list children: %w", err)
	}
	defer rows.Close()

	page := &Page{Total: total, Nodes: make([]*Node, 0, p.Limit)}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("nodes: scan child: %w", err)
		}
		page.Nodes = append(page.Nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("nodes: list children: %w", err)
	}

	page.HasMore = p.Offset+len(page.Nodes) < total
	return page, nil
}

// orderClause maps a validated sort field onto SQL.
//
// This is the only place in the package that concatenates SQL, and it does so
// exclusively from constants selected by a validated enum — never from caller
// text.
func orderClause(field SortField, descending bool) string {
	direction := "ASC"
	if descending {
		direction = "DESC"
	}

	// Folders first, always.
	const foldersFirst = "(kind = 'folder') DESC, "

	switch field {
	case SortBySize:
		return foldersFirst + "size_bytes " + direction + ", name_folded ASC"
	case SortByModified:
		return foldersFirst + "updated_at " + direction + ", name_folded ASC"
	case SortByCreated:
		return foldersFirst + "created_at " + direction + ", name_folded ASC"
	default:
		return foldersFirst + "name_folded " + direction
	}
}

// Breadcrumbs returns the ancestors of a node, root first.
func (s *Store) Breadcrumbs(ctx context.Context, id uuid.UUID) ([]Breadcrumb, error) {
	// A recursive CTE walks up the tree in one round trip. The depth guard
	// bounds the walk even if the data somehow contains a cycle.
	rows, err := s.db.Pool().Query(ctx, `
		WITH RECURSIVE ancestry AS (
			SELECT id, parent_id, name, 0 AS depth
			FROM nodes WHERE id = $1
			UNION ALL
			SELECT n.id, n.parent_id, n.name, a.depth + 1
			FROM nodes n
			JOIN ancestry a ON n.id = a.parent_id
			WHERE a.depth < 128
		)
		SELECT id, name, parent_id IS NULL FROM ancestry ORDER BY depth DESC`, id)
	if err != nil {
		return nil, fmt.Errorf("nodes: breadcrumbs: %w", err)
	}
	defer rows.Close()

	var out []Breadcrumb
	for rows.Next() {
		var b Breadcrumb
		if err := rows.Scan(&b.ID, &b.Name, &b.IsRoot); err != nil {
			return nil, fmt.Errorf("nodes: scan breadcrumb: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// --- Writes -----------------------------------------------------------------

// CreateFolderParams describes a new folder.
type CreateFolderParams struct {
	OwnerID  uuid.UUID
	ParentID uuid.UUID
	Name     string
	ActorID  uuid.UUID
}

// CreateFolder creates a folder inside a parent.
func (s *Store) CreateFolder(ctx context.Context, p CreateFolderParams) (*Node, error) {
	name := NormalizeName(p.Name)
	if err := ValidateName(name); err != nil {
		return nil, err
	}

	var created *Node
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		parent, err := loadForUpdate(ctx, tx, p.ParentID)
		if err != nil {
			return err
		}
		if !parent.IsFolder() {
			return ErrNotAFolder
		}

		row := tx.QueryRow(ctx, `
			INSERT INTO nodes (owner_id, parent_id, kind, name, created_by, modified_by)
			VALUES ($1, $2, 'folder', $3, $4, $4)
			RETURNING `+columns,
			p.OwnerID, p.ParentID, name, p.ActorID)

		created, err = scanNode(row)
		if err != nil {
			return translateConstraint(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// CreateFileParams describes a file whose content is already committed to
// storage.
type CreateFileParams struct {
	OwnerID     uuid.UUID
	ParentID    uuid.UUID
	Name        string
	ContentHash string
	SizeBytes   int64
	MimeType    string
	ActorID     uuid.UUID
	// ReplaceID names an existing file this content supersedes. When set, the
	// existing node is updated in place rather than a new one created, so the
	// file keeps its identity, its shares, and its favourites.
	ReplaceID *uuid.UUID
}

// CreateFile records a file and takes a reference on its content.
//
// The caller must already have committed the bytes to storage. Ordering
// matters: the blob exists before this row does, so a failure here leaves an
// unreferenced blob for the reconciliation job, never a row pointing at
// nothing.
func (s *Store) CreateFile(ctx context.Context, p CreateFileParams) (*Node, error) {
	name := NormalizeName(p.Name)
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	if p.SizeBytes < 0 {
		return nil, fmt.Errorf("nodes: negative size")
	}

	var result *Node
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		parent, err := loadForUpdate(ctx, tx, p.ParentID)
		if err != nil {
			return err
		}
		if !parent.IsFolder() {
			return ErrNotAFolder
		}

		// Record the blob and take a reference. ON CONFLICT handles the
		// deduplication case where the content already exists.
		if _, err := tx.Exec(ctx, `
			INSERT INTO blobs (hash, size_bytes, ref_count) VALUES ($1, $2, 1)
			ON CONFLICT (hash) DO UPDATE SET ref_count = blobs.ref_count + 1`,
			p.ContentHash, p.SizeBytes); err != nil {
			return fmt.Errorf("nodes: record blob: %w", err)
		}

		if p.ReplaceID != nil {
			result, err = s.replaceFileContent(ctx, tx, *p.ReplaceID, p)
			return err
		}

		row := tx.QueryRow(ctx, `
			INSERT INTO nodes (
				owner_id, parent_id, kind, name, size_bytes, mime_type,
				content_hash, created_by, modified_by
			) VALUES ($1, $2, 'file', $3, $4, $5, $6, $7, $7)
			RETURNING `+columns,
			p.OwnerID, p.ParentID, name, p.SizeBytes, p.MimeType, p.ContentHash, p.ActorID)

		result, err = scanNode(row)
		if err != nil {
			return translateConstraint(err)
		}

		return adjustUsage(ctx, tx, p.OwnerID, p.SizeBytes)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// replaceFileContent points an existing file at new content, releasing the
// reference on the old blob.
func (s *Store) replaceFileContent(
	ctx context.Context, tx pgx.Tx, id uuid.UUID, p CreateFileParams,
) (*Node, error) {
	existing, err := loadForUpdateIncludingDeleted(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if !existing.IsFile() {
		return nil, ErrNotAFile
	}

	if err := s.retireContent(ctx, tx, existing, p.ContentHash, p.ActorID, false); err != nil {
		return nil, err
	}

	row := tx.QueryRow(ctx, `
		UPDATE nodes SET
			size_bytes = $2, mime_type = $3, content_hash = $4, modified_by = $5
		WHERE id = $1
		RETURNING `+columns,
		id, p.SizeBytes, p.MimeType, p.ContentHash, p.ActorID)

	updated, err := scanNode(row)
	if err != nil {
		return nil, fmt.Errorf("nodes: replace content: %w", err)
	}

	// Only the difference counts against the quota.
	if err := adjustUsage(ctx, tx, existing.OwnerID, p.SizeBytes-existing.SizeBytes); err != nil {
		return nil, err
	}
	return updated, nil
}

// retireContent decides what becomes of the content a node is about to stop
// pointing at.
//
// With versioning on and the content actually changing, it becomes a version
// and keeps the reference the node was holding. Otherwise the reference is
// released and the reconciliation job reclaims the bytes once nothing needs
// them.
//
// Replacing a file with byte-identical content is deliberately not versioned.
// The digest is the same, so there is nothing to go back to; a version of it
// would be an entry in the history that changes nothing, which is noise in the
// one place a person is trying to read carefully.
func (s *Store) retireContent(
	ctx context.Context,
	tx pgx.Tx,
	existing *Node,
	incomingHash string,
	actorID uuid.UUID,
	restoring bool,
) error {
	if existing.ContentHash == nil || *existing.ContentHash == "" {
		return nil
	}
	unchanged := incomingHash == *existing.ContentHash

	if s.versioner == nil || unchanged {
		return releaseBlob(ctx, tx, *existing.ContentHash)
	}

	if restoring {
		return s.versioner.SnapshotBeforeRestore(ctx, tx, existing, actorID)
	}
	return s.versioner.SnapshotBeforeReplace(ctx, tx, existing, actorID)
}

// RestoreContentParams describes putting an earlier state back.
type RestoreContentParams struct {
	NodeID      uuid.UUID
	ContentHash string
	SizeBytes   int64
	MimeType    string
	ActorID     uuid.UUID
}

// RestoreContent points a file back at content it previously held.
//
// Restoring is never destructive: the content being replaced becomes a version
// of its own first, so restoring the wrong thing is one more restore away from
// being undone rather than gone. That is the whole reason version history is
// worth having, and it is why this is a write rather than a rewind.
//
// The restored blob gains a reference — the node's — while the version that
// named it keeps its own. Two references to one set of bytes, which is exactly
// what content addressing is for.
func (s *Store) RestoreContent(ctx context.Context, p RestoreContentParams) (*Node, error) {
	if p.ContentHash == "" {
		return nil, fmt.Errorf("nodes: restore needs content to restore")
	}

	var restored *Node
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		existing, err := loadForUpdate(ctx, tx, p.NodeID)
		if err != nil {
			return err
		}
		if !existing.IsFile() {
			return ErrNotAFile
		}

		if err := s.retireContent(ctx, tx, existing, p.ContentHash, p.ActorID, true); err != nil {
			return err
		}

		// Take a reference for the node. The blob is certain to exist — a
		// version cannot name content that was never stored — so this is an
		// increment rather than an insert, and a missing row would mean the
		// history is already broken.
		tag, err := tx.Exec(ctx,
			`UPDATE blobs SET ref_count = ref_count + 1 WHERE hash = $1`, p.ContentHash)
		if err != nil {
			return fmt.Errorf("nodes: retain restored blob: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("nodes: the content this version names is no longer stored")
		}

		row := tx.QueryRow(ctx, `
			UPDATE nodes SET
				size_bytes = $2, mime_type = $3, content_hash = $4, modified_by = $5
			WHERE id = $1
			RETURNING `+columns,
			p.NodeID, p.SizeBytes, p.MimeType, p.ContentHash, p.ActorID)

		restored, err = scanNode(row)
		if err != nil {
			return fmt.Errorf("nodes: restore content: %w", err)
		}

		return adjustUsage(ctx, tx, existing.OwnerID, p.SizeBytes-existing.SizeBytes)
	})
	if err != nil {
		return nil, err
	}
	return restored, nil
}

// RenameParams describes a rename.
type RenameParams struct {
	ID      uuid.UUID
	Name    string
	ActorID uuid.UUID
}

// Rename changes a node's name.
func (s *Store) Rename(ctx context.Context, p RenameParams) (*Node, error) {
	name := NormalizeName(p.Name)
	if err := ValidateName(name); err != nil {
		return nil, err
	}

	var renamed *Node
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		node, err := loadForUpdate(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		if node.IsRoot() {
			return ErrRootImmutable
		}

		row := tx.QueryRow(ctx,
			`UPDATE nodes SET name = $2, modified_by = $3 WHERE id = $1 RETURNING `+columns,
			p.ID, name, p.ActorID)

		renamed, err = scanNode(row)
		if err != nil {
			return translateConstraint(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return renamed, nil
}

// MoveParams describes a move.
type MoveParams struct {
	ID          uuid.UUID
	NewParentID uuid.UUID
	// NewName optionally renames during the move, which is how a client
	// resolves a name collision at the destination.
	NewName string
	ActorID uuid.UUID
}

// Move relocates a node to a different folder.
func (s *Store) Move(ctx context.Context, p MoveParams) (*Node, error) {
	var moved *Node

	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		node, err := loadForUpdate(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		if node.IsRoot() {
			return ErrRootImmutable
		}

		destination, err := loadForUpdate(ctx, tx, p.NewParentID)
		if err != nil {
			return err
		}
		if !destination.IsFolder() {
			return ErrNotAFolder
		}

		// A folder moved into its own subtree would detach that subtree from
		// the root entirely: unreachable, uncountable against quota, and
		// impossible to delete through the interface.
		if node.IsFolder() {
			inside, err := isDescendant(ctx, tx, p.NewParentID, p.ID)
			if err != nil {
				return err
			}
			if inside || p.NewParentID == p.ID {
				return ErrCycle
			}
		}

		name := node.Name
		if p.NewName != "" {
			name = NormalizeName(p.NewName)
			if err := ValidateName(name); err != nil {
				return err
			}
		}

		row := tx.QueryRow(ctx, `
			UPDATE nodes SET parent_id = $2, name = $3, modified_by = $4
			WHERE id = $1
			RETURNING `+columns,
			p.ID, p.NewParentID, name, p.ActorID)

		moved, err = scanNode(row)
		if err != nil {
			return translateConstraint(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return moved, nil
}

// isDescendant reports whether candidate lies within the subtree rooted at
// ancestor.
func isDescendant(ctx context.Context, tx pgx.Tx, candidate, ancestor uuid.UUID) (bool, error) {
	var found bool
	err := tx.QueryRow(ctx, `
		WITH RECURSIVE ancestry AS (
			SELECT id, parent_id, 0 AS depth FROM nodes WHERE id = $1
			UNION ALL
			SELECT n.id, n.parent_id, a.depth + 1
			FROM nodes n JOIN ancestry a ON n.id = a.parent_id
			WHERE a.depth < 128
		)
		SELECT EXISTS (SELECT 1 FROM ancestry WHERE id = $2)`,
		candidate, ancestor).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("nodes: cycle check: %w", err)
	}
	return found, nil
}

// SoftDelete moves a node into Trash.
//
// The node and its whole subtree are marked deleted, and the owner's usage is
// reduced by the subtree's size. Content on disk is untouched: restoring must
// be possible, and it is the retention sweep that eventually releases blobs.
func (s *Store) SoftDelete(ctx context.Context, id, actorID uuid.UUID) (int64, error) {
	var freed int64

	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		node, err := loadForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if node.IsRoot() {
			return ErrRootImmutable
		}

		// One statement marks the entire subtree, so a large folder is a single
		// round trip rather than one per descendant.
		err = tx.QueryRow(ctx, `
			WITH RECURSIVE subtree AS (
				SELECT id FROM nodes WHERE id = $1 AND deleted_at IS NULL
				UNION ALL
				SELECT n.id FROM nodes n JOIN subtree s ON n.parent_id = s.id
				WHERE n.deleted_at IS NULL
			),
			marked AS (
				UPDATE nodes SET deleted_at = now(), modified_by = $2
				WHERE id IN (SELECT id FROM subtree)
				RETURNING size_bytes
			)
			SELECT coalesce(sum(size_bytes), 0) FROM marked`,
			id, actorID).Scan(&freed)
		if err != nil {
			return fmt.Errorf("nodes: soft delete: %w", err)
		}

		return adjustUsage(ctx, tx, node.OwnerID, -freed)
	})

	return freed, err
}

// Restore returns a node and its subtree from Trash.
func (s *Store) Restore(ctx context.Context, id, actorID uuid.UUID) (*Node, error) {
	var restored *Node

	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		node, err := loadForUpdateIncludingDeleted(ctx, tx, id)
		if err != nil {
			return err
		}
		if !node.IsDeleted() {
			return ErrNotDeleted
		}

		// Restoring into a parent that is itself deleted would produce a node
		// that is live but unreachable. The caller must restore the parent
		// first, or move this node somewhere live.
		if node.ParentID != nil {
			var parentDeleted bool
			err := tx.QueryRow(ctx,
				`SELECT deleted_at IS NOT NULL FROM nodes WHERE id = $1`, *node.ParentID).Scan(&parentDeleted)
			if err != nil {
				return fmt.Errorf("nodes: check parent: %w", err)
			}
			if parentDeleted {
				return fmt.Errorf("%w: its folder is also in the trash", ErrNotFound)
			}
		}

		// The original name may have been taken while this was in Trash.
		var taken bool
		err = tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM nodes
				WHERE parent_id = $1 AND name_folded = lower($2)
				  AND deleted_at IS NULL AND id <> $3
			)`, node.ParentID, node.Name, id).Scan(&taken)
		if err != nil {
			return fmt.Errorf("nodes: check name: %w", err)
		}
		if taken {
			return ErrNameTaken
		}

		// Only descendants deleted at the same moment are restored, so a file
		// deleted separately *before* its folder stays in Trash rather than
		// reappearing unexpectedly.
		var restoredBytes int64
		err = tx.QueryRow(ctx, `
			WITH RECURSIVE subtree AS (
				SELECT id, deleted_at FROM nodes WHERE id = $1
				UNION ALL
				SELECT n.id, n.deleted_at FROM nodes n JOIN subtree s ON n.parent_id = s.id
				WHERE n.deleted_at = (SELECT deleted_at FROM nodes WHERE id = $1)
			),
			restored AS (
				UPDATE nodes SET deleted_at = NULL, modified_by = $2
				WHERE id IN (SELECT id FROM subtree)
				RETURNING size_bytes
			)
			SELECT coalesce(sum(size_bytes), 0) FROM restored`,
			id, actorID).Scan(&restoredBytes)
		if err != nil {
			return fmt.Errorf("nodes: restore: %w", err)
		}

		if err := adjustUsage(ctx, tx, node.OwnerID, restoredBytes); err != nil {
			return err
		}

		restored, err = loadForUpdate(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return restored, nil
}

// PurgeResult reports what a permanent delete released.
type PurgeResult struct {
	NodesDeleted int
	// UnreferencedBlobs are digests whose reference count reached zero. The
	// caller hands these to the reconciliation job; the bytes are never
	// unlinked inline, because a concurrent upload may have just taken a new
	// reference.
	UnreferencedBlobs []string
}

// Purge permanently removes a node and its subtree.
func (s *Store) Purge(ctx context.Context, id uuid.UUID) (*PurgeResult, error) {
	result := &PurgeResult{}

	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		node, err := loadForUpdateIncludingDeleted(ctx, tx, id)
		if err != nil {
			return err
		}
		if node.IsRoot() {
			return ErrRootImmutable
		}

		// Collect the subtree's content hashes before deleting the rows.
		rows, err := tx.Query(ctx, `
			WITH RECURSIVE subtree AS (
				SELECT id, content_hash, size_bytes FROM nodes WHERE id = $1
				UNION ALL
				SELECT n.id, n.content_hash, n.size_bytes
				FROM nodes n JOIN subtree s ON n.parent_id = s.id
			)
			SELECT id, content_hash, size_bytes FROM subtree`, id)
		if err != nil {
			return fmt.Errorf("nodes: collect subtree: %w", err)
		}

		var ids []uuid.UUID
		var hashes []string
		var liveBytes int64
		for rows.Next() {
			var nodeID uuid.UUID
			var hash *string
			var size int64
			if err := rows.Scan(&nodeID, &hash, &size); err != nil {
				rows.Close()
				return fmt.Errorf("nodes: scan subtree: %w", err)
			}
			ids = append(ids, nodeID)
			if hash != nil {
				hashes = append(hashes, *hash)
			}
			liveBytes += size
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("nodes: collect subtree: %w", err)
		}

		// Release the subtree's version history before the nodes go.
		//
		// The foreign key cascades those rows away, but a cascade cannot
		// decrement a reference count — so without this the counts stay high
		// forever and the bytes are never reclaimed. Deleting the rows here
		// rather than letting the cascade do it is what makes the accounting
		// observable and correct.
		versionHashes, err := releaseVersionReferences(ctx, tx, ids)
		if err != nil {
			return err
		}
		hashes = append(hashes, versionHashes...)

		// Delete deepest-first so the parent_id foreign key is never violated.
		if _, err := tx.Exec(ctx, `
			WITH RECURSIVE subtree AS (
				SELECT id, 0 AS depth FROM nodes WHERE id = $1
				UNION ALL
				SELECT n.id, s.depth + 1 FROM nodes n JOIN subtree s ON n.parent_id = s.id
			)
			DELETE FROM nodes WHERE id IN (SELECT id FROM subtree)`, id); err != nil {
			return fmt.Errorf("nodes: purge: %w", err)
		}
		result.NodesDeleted = len(ids)

		for _, hash := range hashes {
			if err := releaseBlob(ctx, tx, hash); err != nil {
				return err
			}
		}

		// Find which of those blobs are now unreferenced.
		if len(hashes) > 0 {
			unreferenced, err := tx.Query(ctx,
				`SELECT hash FROM blobs WHERE hash = ANY($1) AND ref_count = 0`, hashes)
			if err != nil {
				return fmt.Errorf("nodes: find unreferenced blobs: %w", err)
			}
			defer unreferenced.Close()
			for unreferenced.Next() {
				var hash string
				if err := unreferenced.Scan(&hash); err != nil {
					return fmt.Errorf("nodes: scan unreferenced blob: %w", err)
				}
				result.UnreferencedBlobs = append(result.UnreferencedBlobs, hash)
			}
			if err := unreferenced.Err(); err != nil {
				return fmt.Errorf("nodes: find unreferenced blobs: %w", err)
			}
		}

		// A node already in Trash was subtracted from usage when it was
		// deleted; only a live node needs subtracting now.
		if !node.IsDeleted() {
			return adjustUsage(ctx, tx, node.OwnerID, -liveBytes)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ListTrash returns a user's deleted nodes, most recently deleted first.
//
// Only the top of each deleted subtree is listed: showing every descendant
// separately would bury the folder the user actually deleted under its own
// contents.
func (s *Store) ListTrash(ctx context.Context, ownerID uuid.UUID, limit, offset int) (*Page, error) {
	if limit <= 0 || limit > MaxPageSize {
		limit = DefaultPageSize
	}

	const where = `
		WHERE n.owner_id = $1 AND n.deleted_at IS NOT NULL
		  AND (
			n.parent_id IS NULL
			OR NOT EXISTS (
				SELECT 1 FROM nodes p
				WHERE p.id = n.parent_id AND p.deleted_at = n.deleted_at
			)
		  )`

	var total int
	if err := s.db.Pool().QueryRow(ctx,
		`SELECT count(*) FROM nodes n`+where, ownerID).Scan(&total); err != nil {
		return nil, fmt.Errorf("nodes: count trash: %w", err)
	}

	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+prefixColumns("n")+` FROM nodes n`+where+`
		 ORDER BY n.deleted_at DESC, n.name_folded ASC LIMIT $2 OFFSET $3`,
		ownerID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("nodes: list trash: %w", err)
	}
	defer rows.Close()

	page := &Page{Total: total, Nodes: make([]*Node, 0, limit)}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("nodes: scan trash: %w", err)
		}
		page.Nodes = append(page.Nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("nodes: list trash: %w", err)
	}
	page.HasMore = offset+len(page.Nodes) < total
	return page, nil
}

// prefixColumns qualifies the column list with a table alias.
func prefixColumns(alias string) string {
	cols := []string{
		"id", "owner_id", "parent_id", "kind", "name", "size_bytes", "mime_type",
		"content_hash", "created_at", "updated_at", "deleted_at", "created_by", "modified_by",
	}
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = alias + "." + c
	}
	return joinComma(out)
}

func joinComma(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	total := 0
	for _, p := range parts {
		total += len(p) + 2
	}
	buf := make([]byte, 0, total)
	for i, p := range parts {
		if i > 0 {
			buf = append(buf, ',', ' ')
		}
		buf = append(buf, p...)
	}
	return string(buf)
}

// ExpiredTrash returns the IDs of trashed nodes whose retention period has
// elapsed, oldest first, bounded by limit.
//
// Only the top of each deleted subtree is returned. Purging a folder removes
// its descendants with it, so returning them separately would have the sweep
// try to purge rows that no longer exist.
func (s *Store) ExpiredTrash(ctx context.Context, retention time.Duration, limit int) ([]uuid.UUID, error) {
	if retention <= 0 {
		// Refuse rather than interpret zero as "everything". A misconfiguration
		// must not become a mass deletion.
		return nil, fmt.Errorf("nodes: expired trash requires a positive retention period")
	}
	if limit <= 0 || limit > MaxPageSize {
		limit = DefaultPageSize
	}

	rows, err := s.db.Pool().Query(ctx, `
		SELECT n.id
		FROM nodes n
		WHERE n.deleted_at IS NOT NULL
		  AND n.deleted_at < now() - $1::interval
		  -- The top of the deleted subtree: either the parent is still live, or
		  -- it was deleted at a different moment (a separate deletion).
		  AND (
		    n.parent_id IS NULL
		    OR NOT EXISTS (
		      SELECT 1 FROM nodes p
		      WHERE p.id = n.parent_id AND p.deleted_at IS NOT NULL
		    )
		  )
		ORDER BY n.deleted_at
		LIMIT $2`, retention, limit)
	if err != nil {
		return nil, fmt.Errorf("nodes: expired trash: %w", err)
	}
	defer rows.Close()

	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("nodes: scan expired trash: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// NameExists reports whether a live sibling already uses a name.
func (s *Store) NameExists(ctx context.Context, parentID uuid.UUID, name string) (bool, error) {
	var exists bool
	err := s.db.Pool().QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM nodes
			WHERE parent_id = $1 AND name_folded = lower($2) AND deleted_at IS NULL
		)`, parentID, name).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("nodes: name check: %w", err)
	}
	return exists, nil
}

// SubtreeSize returns the total live bytes beneath a folder, inclusive.
func (s *Store) SubtreeSize(ctx context.Context, id uuid.UUID) (int64, int, error) {
	var bytes int64
	var count int
	err := s.db.Pool().QueryRow(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT id, size_bytes, kind FROM nodes WHERE id = $1 AND deleted_at IS NULL
			UNION ALL
			SELECT n.id, n.size_bytes, n.kind FROM nodes n
			JOIN subtree s ON n.parent_id = s.id WHERE n.deleted_at IS NULL
		)
		SELECT coalesce(sum(size_bytes), 0), count(*) FILTER (WHERE kind = 'file')
		FROM subtree`, id).Scan(&bytes, &count)
	if err != nil {
		return 0, 0, fmt.Errorf("nodes: subtree size: %w", err)
	}
	return bytes, count, nil
}

// RecalculateUsage recomputes a user's storage total from their live content.
//
// The denormalised counter is authoritative for quota checks because summing
// on every upload would not scale; this rebuilds it from the actual rows, and
// is what the quota-recalculation job runs.
func (s *Store) RecalculateUsage(ctx context.Context, ownerID uuid.UUID) (int64, error) {
	var total int64
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(sum(size_bytes), 0) FROM nodes
			WHERE owner_id = $1 AND kind = 'file' AND deleted_at IS NULL`,
			ownerID).Scan(&total); err != nil {
			return fmt.Errorf("nodes: sum usage: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE users SET used_bytes = $2 WHERE id = $1`, ownerID, total); err != nil {
			return fmt.Errorf("nodes: store usage: %w", err)
		}
		return nil
	})
	return total, err
}

// --- Helpers ----------------------------------------------------------------

// loadForUpdate reads a live node and locks its row for the transaction.
//
// The lock is what makes the name-uniqueness and cycle checks reliable: without
// it, two concurrent requests could each observe a free name and then both
// insert.
func loadForUpdate(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Node, error) {
	row := tx.QueryRow(ctx,
		`SELECT `+columns+` FROM nodes WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, id)
	n, err := scanNode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("nodes: load: %w", err)
	}
	return n, nil
}

func loadForUpdateIncludingDeleted(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Node, error) {
	row := tx.QueryRow(ctx, `SELECT `+columns+` FROM nodes WHERE id = $1 FOR UPDATE`, id)
	n, err := scanNode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("nodes: load: %w", err)
	}
	return n, nil
}

// adjustUsage moves a user's stored total by delta, clamped at zero.
func adjustUsage(ctx context.Context, tx pgx.Tx, ownerID uuid.UUID, delta int64) error {
	if delta == 0 {
		return nil
	}
	// greatest(...) guards the CHECK constraint: a counter that has drifted
	// low must not make a legitimate delete fail. The recalculation job
	// repairs drift.
	if _, err := tx.Exec(ctx,
		`UPDATE users SET used_bytes = greatest(0, used_bytes + $2) WHERE id = $1`,
		ownerID, delta); err != nil {
		return fmt.Errorf("nodes: adjust usage: %w", err)
	}
	return nil
}

// releaseVersionReferences deletes the version rows for a set of nodes and
// gives back every reference they held.
//
// Written here, in the package that owns blob accounting, rather than in the
// versions package: the purge has to happen inside the same transaction as the
// node deletion, and reaching across for it would mean either an import cycle
// or a second transaction that a crash could leave half-applied.
func releaseVersionReferences(ctx context.Context, tx pgx.Tx, nodeIDs []uuid.UUID) ([]string, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}

	rows, err := tx.Query(ctx,
		`DELETE FROM versions WHERE node_id = ANY($1) RETURNING content_hash`, nodeIDs)
	if err != nil {
		return nil, fmt.Errorf("nodes: release version references: %w", err)
	}

	var hashes []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			rows.Close()
			return nil, fmt.Errorf("nodes: scan version hash: %w", err)
		}
		hashes = append(hashes, hash)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("nodes: release version references: %w", err)
	}
	return hashes, nil
}

// releaseBlob decrements a blob's reference count.
func releaseBlob(ctx context.Context, tx pgx.Tx, hash string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE blobs SET ref_count = greatest(0, ref_count - 1) WHERE hash = $1`,
		hash); err != nil {
		return fmt.Errorf("nodes: release blob: %w", err)
	}
	return nil
}

// translateConstraint converts a database constraint violation into a domain
// error, so a constraint name never reaches a client.
func translateConstraint(err error) error {
	if database.IsUniqueViolation(err) {
		switch database.ConstraintName(err) {
		case "nodes_unique_sibling_name_idx":
			return ErrNameTaken
		case "nodes_one_root_per_owner_idx":
			return fmt.Errorf("nodes: user already has a root folder")
		}
		return ErrNameTaken
	}
	if database.IsCheckViolation(err) {
		switch database.ConstraintName(err) {
		case "nodes_name_safe", "nodes_name_length":
			return &NameError{"That name cannot be used."}
		}
	}
	return fmt.Errorf("nodes: %w", err)
}

// DB exposes the database handle for maintenance queries that legitimately
// span tables this package does not own — reconciliation, primarily.
func (s *Store) DB() *database.DB { return s.db }
