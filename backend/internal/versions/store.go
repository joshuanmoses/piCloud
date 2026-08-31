package versions

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joshuanmoses/piCloud/backend/internal/database"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
)

// Store persists version history.
type Store struct {
	db *database.DB
}

// NewStore returns a Store backed by db.
func NewStore(db *database.DB) *Store { return &Store{db: db} }

// DB exposes the pool so a caller can run a snapshot inside its own
// transaction. Versioning has to be atomic with the replacement that causes it:
// a crash between the two would either lose the history or leave a version of
// content the node still holds.
func (s *Store) DB() *database.DB { return s.db }

const columns = `
	id, node_id, owner_id, version, content_hash, size_bytes, mime_type,
	name, created_by, created_at, reason`

type scanner interface{ Scan(dest ...any) error }

func scanVersion(sc scanner) (*Version, error) {
	var v Version
	err := sc.Scan(
		&v.ID, &v.NodeID, &v.OwnerID, &v.Number, &v.ContentHash, &v.SizeBytes,
		&v.MimeType, &v.Name, &v.CreatedBy, &v.CreatedAt, &v.Reason,
	)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// SnapshotParams describes the state being preserved.
type SnapshotParams struct {
	Node   *nodes.Node
	Reason Reason
	// ActorID is who caused the replacement.
	ActorID uuid.UUID
}

// Snapshot records a file's current content as a version, inside tx.
//
// It does **not** take a new blob reference, and the caller must not release
// the old one. The reference the node was holding transfers to the version:
// one reference in, one reference out, no window in which the count is wrong.
//
// Returns nil, nil when there is nothing to snapshot — a file with no content,
// which is an upload that never completed rather than an error.
func (s *Store) Snapshot(ctx context.Context, tx pgx.Tx, p SnapshotParams) (*Version, error) {
	node := p.Node
	if node == nil || !node.IsFile() {
		return nil, ErrNotAFile
	}
	if node.ContentHash == nil || *node.ContentHash == "" {
		return nil, nil
	}

	reason := p.Reason
	if !reason.Valid() {
		reason = ReasonReplaced
	}

	var actor *uuid.UUID
	if p.ActorID != uuid.Nil {
		actor = &p.ActorID
	}

	// The number is derived inside the insert, so two concurrent replacements
	// cannot both claim the same one. The unique index on (node_id, version)
	// is the backstop if they somehow do.
	row := tx.QueryRow(ctx, `
		INSERT INTO versions
			(node_id, owner_id, version, content_hash, size_bytes, mime_type, name, created_by, reason)
		VALUES (
			$1, $2,
			(SELECT coalesce(max(version), 0) + 1 FROM versions WHERE node_id = $1),
			$3, $4, $5, $6, $7, $8
		)
		RETURNING `+columns,
		node.ID, node.OwnerID, *node.ContentHash, node.SizeBytes,
		node.MimeType, node.Name, actor, string(reason))

	v, err := scanVersion(row)
	if err != nil {
		return nil, fmt.Errorf("versions: snapshot: %w", err)
	}
	return v, nil
}

// ListForNode returns a file's history, newest first.
func (s *Store) ListForNode(ctx context.Context, nodeID uuid.UUID) ([]*Version, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+columns+` FROM versions WHERE node_id = $1 ORDER BY version DESC`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("versions: list: %w", err)
	}
	defer rows.Close()

	out := make([]*Version, 0, 16)
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("versions: scan: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CountForNode returns how many versions a file has, so a listing can show a
// badge without fetching the history.
func (s *Store) CountForNode(ctx context.Context, nodeID uuid.UUID) (int, error) {
	var n int
	if err := s.db.Pool().QueryRow(ctx,
		`SELECT count(*) FROM versions WHERE node_id = $1`, nodeID).Scan(&n); err != nil {
		return 0, fmt.Errorf("versions: count: %w", err)
	}
	return n, nil
}

// CountForNodes is CountForNode batched, so a folder listing costs one query.
func (s *Store) CountForNodes(ctx context.Context, nodeIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	out := make(map[uuid.UUID]int, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return out, nil
	}

	rows, err := s.db.Pool().Query(ctx,
		`SELECT node_id, count(*) FROM versions WHERE node_id = ANY($1) GROUP BY node_id`,
		nodeIDs)
	if err != nil {
		return nil, fmt.Errorf("versions: count many: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id uuid.UUID
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("versions: scan count: %w", err)
		}
		out[id] = n
	}
	return out, rows.Err()
}

// Get returns one version.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (*Version, error) {
	row := s.db.Pool().QueryRow(ctx, `SELECT `+columns+` FROM versions WHERE id = $1`, id)
	v, err := scanVersion(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("versions: get: %w", err)
	}
	return v, nil
}

// UsageForOwner returns how many bytes a user's history occupies.
//
// Counted as the sum of version sizes, not of distinct blobs. Two versions
// pointing at the same content — a file reverted and then re-replaced — do not
// occupy two copies on disk, so this over-reports in that case. It is the
// conservative direction for a retention cap, and the honest one for a display:
// under-reporting would let history grow past a limit the operator set.
func (s *Store) UsageForOwner(ctx context.Context, ownerID uuid.UUID) (int64, int, error) {
	var bytes int64
	var count int
	err := s.db.Pool().QueryRow(ctx,
		`SELECT coalesce(sum(size_bytes), 0), count(*) FROM versions WHERE owner_id = $1`,
		ownerID).Scan(&bytes, &count)
	if err != nil {
		return 0, 0, fmt.Errorf("versions: usage: %w", err)
	}
	return bytes, count, nil
}

// Delete removes a version and releases its hold on the content.
//
// The blob is never unlinked here, only dereferenced. The reconciliation job
// removes bytes that nothing references, so a delete racing an upload of
// identical content cannot destroy the new file.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		var hash string
		err := tx.QueryRow(ctx,
			`DELETE FROM versions WHERE id = $1 RETURNING content_hash`, id).Scan(&hash)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("versions: delete: %w", err)
		}
		return releaseBlob(ctx, tx, hash)
	})
}

// DeleteForNode removes a file's whole history, releasing every reference.
func (s *Store) DeleteForNode(ctx context.Context, nodeID uuid.UUID) (int, error) {
	var removed int
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`DELETE FROM versions WHERE node_id = $1 RETURNING content_hash`, nodeID)
		if err != nil {
			return fmt.Errorf("versions: delete for node: %w", err)
		}

		var hashes []string
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return fmt.Errorf("versions: scan: %w", err)
			}
			hashes = append(hashes, h)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		for _, h := range hashes {
			if err := releaseBlob(ctx, tx, h); err != nil {
				return err
			}
		}
		removed = len(hashes)
		return nil
	})
	return removed, err
}

// Prunable is a version that retention says should go, with the reason.
type Prunable struct {
	ID      uuid.UUID
	NodeID  uuid.UUID
	OwnerID uuid.UUID
	Size    int64
	Why     string
}

// FindPrunable returns versions that fall outside the retention policy.
//
// Deliberately a query that *finds* rather than a statement that deletes: the
// caller logs what it is about to remove, and a policy misconfigured to zero
// cannot become "delete everything" by accident. Retention.Unlimited() short
// circuits before any of this runs.
func (s *Store) FindPrunable(ctx context.Context, r Retention, limit int) ([]Prunable, error) {
	if r.Unlimited() {
		return nil, nil
	}
	if limit <= 0 {
		limit = 500
	}

	var out []Prunable
	seen := make(map[uuid.UUID]bool)

	add := func(rows pgx.Rows, why string) error {
		defer rows.Close()
		for rows.Next() {
			var p Prunable
			if err := rows.Scan(&p.ID, &p.NodeID, &p.OwnerID, &p.Size); err != nil {
				return fmt.Errorf("versions: scan prunable: %w", err)
			}
			if seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			p.Why = why
			out = append(out, p)
		}
		return rows.Err()
	}

	// Over the per-file count: keep the newest MaxPerFile, drop the rest.
	if r.MaxPerFile > 0 {
		rows, err := s.db.Pool().Query(ctx, `
			SELECT id, node_id, owner_id, size_bytes FROM (
				SELECT id, node_id, owner_id, size_bytes,
				       row_number() OVER (PARTITION BY node_id ORDER BY version DESC) AS rank
				FROM versions
			) ranked
			WHERE rank > $1
			ORDER BY node_id, rank DESC
			LIMIT $2`, r.MaxPerFile, limit)
		if err != nil {
			return nil, fmt.Errorf("versions: prunable by count: %w", err)
		}
		if err := add(rows, "exceeds the per-file limit"); err != nil {
			return nil, err
		}
	}

	// Older than the age limit.
	if r.MaxAge > 0 && len(out) < limit {
		rows, err := s.db.Pool().Query(ctx, `
			SELECT id, node_id, owner_id, size_bytes FROM versions
			WHERE created_at < now() - $1::interval
			ORDER BY created_at
			LIMIT $2`, r.MaxAge, limit-len(out))
		if err != nil {
			return nil, fmt.Errorf("versions: prunable by age: %w", err)
		}
		if err := add(rows, "older than the retention period"); err != nil {
			return nil, err
		}
	}

	// Over a user's total. Oldest first, until the running total is back
	// under the cap — computed in SQL so the sweep does not have to load a
	// user's entire history to decide.
	if r.MaxTotalBytes > 0 && len(out) < limit {
		rows, err := s.db.Pool().Query(ctx, `
			SELECT id, node_id, owner_id, size_bytes FROM (
				SELECT id, node_id, owner_id, size_bytes,
				       sum(size_bytes) OVER (
				           PARTITION BY owner_id ORDER BY created_at DESC
				           ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
				       ) AS running
				FROM versions
			) totals
			WHERE running > $1
			ORDER BY owner_id, running DESC
			LIMIT $2`, r.MaxTotalBytes, limit-len(out))
		if err != nil {
			return nil, fmt.Errorf("versions: prunable by size: %w", err)
		}
		if err := add(rows, "exceeds the per-user history limit"); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// OrphanedForDeletedNodes returns versions belonging to files that are in the
// trash past their retention, so purging a file also reclaims its history.
//
// Not strictly required — the foreign key cascades — but the cascade would
// leave blob reference counts too high, and a blob nothing references but that
// the count says is referenced is never reclaimed.
func (s *Store) OrphanedForDeletedNodes(ctx context.Context, limit int) ([]Prunable, error) {
	rows, err := s.db.Pool().Query(ctx, `
		SELECT v.id, v.node_id, v.owner_id, v.size_bytes
		FROM versions v
		LEFT JOIN nodes n ON n.id = v.node_id
		WHERE n.id IS NULL
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("versions: orphans: %w", err)
	}
	defer rows.Close()

	var out []Prunable
	for rows.Next() {
		var p Prunable
		if err := rows.Scan(&p.ID, &p.NodeID, &p.OwnerID, &p.Size); err != nil {
			return nil, fmt.Errorf("versions: scan orphan: %w", err)
		}
		p.Why = "its file no longer exists"
		out = append(out, p)
	}
	return out, rows.Err()
}

// releaseBlob decrements a blob's reference count.
//
// Duplicated from the nodes package rather than exported from it: the two
// packages would otherwise have to import each other, and the statement is one
// line whose meaning is entirely local.
func releaseBlob(ctx context.Context, tx pgx.Tx, hash string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE blobs SET ref_count = greatest(0, ref_count - 1) WHERE hash = $1`,
		hash); err != nil {
		return fmt.Errorf("versions: release blob: %w", err)
	}
	return nil
}
