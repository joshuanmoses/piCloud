package nodes

import (
	"context"
	"fmt"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CopyParams describes a copy.
type CopyParams struct {
	ID          uuid.UUID
	NewParentID uuid.UUID
	// NewName overrides the copy's name. When empty, a non-colliding name is
	// derived automatically ("Report.pdf" becomes "Report (2).pdf").
	NewName string
	OwnerID uuid.UUID
	ActorID uuid.UUID
}

// CopyResult reports what a copy produced.
type CopyResult struct {
	Node        *Node
	NodesCopied int
	BytesAdded  int64
}

// maxCopyNodes bounds a single copy operation.
//
// Copying a folder duplicates one row per descendant. On a Raspberry Pi an
// unbounded copy of a very large tree would hold a transaction open long
// enough to block everything else, so it is refused with a clear message
// rather than allowed to degrade the whole instance.
const maxCopyNodes = 10_000

// Copy duplicates a node, and for a folder its entire subtree.
//
// Only metadata is duplicated. Content is shared through the blob reference
// count, so copying a 4 GB video costs one row and one increment, not 4 GB of
// disk. Quota is still charged for the copy: from the user's point of view they
// now have two files, and deduplication is an implementation detail they should
// not have to reason about — nor one that should let them exceed their
// allowance by copying the same file repeatedly.
func (s *Store) Copy(ctx context.Context, p CopyParams) (*CopyResult, error) {
	result := &CopyResult{}

	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		source, err := loadForUpdate(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		if source.IsRoot() {
			return ErrRootImmutable
		}

		destination, err := loadForUpdate(ctx, tx, p.NewParentID)
		if err != nil {
			return err
		}
		if !destination.IsFolder() {
			return ErrNotAFolder
		}

		// Copying a folder into its own subtree would recurse forever.
		if source.IsFolder() {
			inside, err := isDescendant(ctx, tx, p.NewParentID, p.ID)
			if err != nil {
				return err
			}
			if inside || p.NewParentID == p.ID {
				return ErrCycle
			}
		}

		name, err := resolveCopyName(ctx, tx, p, source)
		if err != nil {
			return err
		}

		copied, err := copyNode(ctx, tx, source, p.NewParentID, name, p.OwnerID, p.ActorID)
		if err != nil {
			return err
		}
		result.Node = copied
		result.NodesCopied = 1
		result.BytesAdded = copied.SizeBytes

		if source.IsFolder() {
			nodes, bytes, err := copySubtree(ctx, tx, p.ID, copied.ID, p.OwnerID, p.ActorID)
			if err != nil {
				return err
			}
			result.NodesCopied += nodes
			result.BytesAdded += bytes
		}

		return adjustUsage(ctx, tx, p.OwnerID, result.BytesAdded)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// resolveCopyName picks the copy's name, deriving a non-colliding one when the
// caller did not supply one.
func resolveCopyName(ctx context.Context, tx pgx.Tx, p CopyParams, source *Node) (string, error) {
	if p.NewName != "" {
		name := NormalizeName(p.NewName)
		if err := ValidateName(name); err != nil {
			return "", err
		}
		return name, nil
	}

	// Read the destination's existing names once, then derive locally. Probing
	// the database per candidate would be a round trip per collision.
	rows, err := tx.Query(ctx,
		`SELECT name_folded FROM nodes WHERE parent_id = $1 AND deleted_at IS NULL`,
		p.NewParentID)
	if err != nil {
		return "", fmt.Errorf("nodes: read destination names: %w", err)
	}
	defer rows.Close()

	existing := map[string]struct{}{}
	for rows.Next() {
		var folded string
		if err := rows.Scan(&folded); err != nil {
			return "", fmt.Errorf("nodes: scan destination name: %w", err)
		}
		existing[folded] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("nodes: read destination names: %w", err)
	}

	return UniqueName(source.Name, func(candidate string) bool {
		_, taken := existing[lower(candidate)]
		return taken
	}), nil
}

// copyNode duplicates a single node under a new parent.
func copyNode(
	ctx context.Context, tx pgx.Tx,
	source *Node, parentID uuid.UUID, name string, ownerID, actorID uuid.UUID,
) (*Node, error) {
	if source.ContentHash != nil {
		if _, err := tx.Exec(ctx,
			`UPDATE blobs SET ref_count = ref_count + 1 WHERE hash = $1`,
			*source.ContentHash); err != nil {
			return nil, fmt.Errorf("nodes: reference blob: %w", err)
		}
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO nodes (
			owner_id, parent_id, kind, name, size_bytes, mime_type,
			content_hash, created_by, modified_by
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
		RETURNING `+columns,
		ownerID, parentID, source.Kind, name, source.SizeBytes,
		source.MimeType, source.ContentHash, actorID)

	copied, err := scanNode(row)
	if err != nil {
		return nil, translateConstraint(err)
	}
	return copied, nil
}

// copySubtree recursively duplicates the children of a folder.
//
// It walks breadth-first with an explicit queue rather than recursing, so a
// deep tree cannot exhaust the goroutine stack, and so the node budget is
// checked as the walk proceeds rather than after the damage is done.
func copySubtree(
	ctx context.Context, tx pgx.Tx,
	sourceID, targetID uuid.UUID, ownerID, actorID uuid.UUID,
) (int, int64, error) {
	type pair struct{ source, target uuid.UUID }

	queue := []pair{{source: sourceID, target: targetID}}
	var copied int
	var bytes int64

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		rows, err := tx.Query(ctx,
			`SELECT `+columns+` FROM nodes
			 WHERE parent_id = $1 AND deleted_at IS NULL ORDER BY id`, current.source)
		if err != nil {
			return copied, bytes, fmt.Errorf("nodes: read children: %w", err)
		}

		var children []*Node
		for rows.Next() {
			child, err := scanNode(rows)
			if err != nil {
				rows.Close()
				return copied, bytes, fmt.Errorf("nodes: scan child: %w", err)
			}
			children = append(children, child)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return copied, bytes, fmt.Errorf("nodes: read children: %w", err)
		}

		for _, child := range children {
			if copied >= maxCopyNodes {
				return copied, bytes, fmt.Errorf(
					"nodes: this folder contains more than %d items, which is too many to copy in one operation",
					maxCopyNodes)
			}

			// Names within a freshly created folder cannot collide, so the
			// source name is kept verbatim.
			newChild, err := copyNode(ctx, tx, child, current.target, child.Name, ownerID, actorID)
			if err != nil {
				return copied, bytes, err
			}
			copied++
			bytes += newChild.SizeBytes

			if child.IsFolder() {
				queue = append(queue, pair{source: child.ID, target: newChild.ID})
			}
		}
	}

	return copied, bytes, nil
}

// lower is a local ASCII-and-Unicode lowercase matching PostgreSQL's lower()
// closely enough for collision detection. The database's unique index is the
// authority; this only avoids obvious round trips.
func lower(s string) string {
	b := []rune(s)
	for i, r := range b {
		if r >= 'A' && r <= 'Z' {
			b[i] = r + 32
		} else if r > 127 {
			b[i] = unicode.ToLower(r)
		}
	}
	return string(b)
}
