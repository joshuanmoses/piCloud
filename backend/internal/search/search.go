// Package search finds files and folders.
//
// The implementation is PostgreSQL trigram matching, deliberately behind an
// interface. Elasticsearch was rejected for v1: it would add a second stateful
// service and roughly a gigabyte of resident memory to solve a problem that,
// at the scale a Raspberry Pi actually holds, an index on one column solves.
// If that stops being true, only this package changes.
package search

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/database"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
)

// Query describes a search.
type Query struct {
	// Text is matched against names. Required.
	Text string
	// UserID scopes the search to what this user may see.
	UserID uuid.UUID

	// Kind restricts to files or folders.
	Kind nodes.Kind
	// MimePrefix restricts by type, e.g. "image/".
	MimePrefix string
	// InFolder restricts to a subtree.
	InFolder *uuid.UUID
	// OwnedByMe restricts to the caller's own content.
	OwnedByMe bool

	// ModifiedAfter and ModifiedBefore bound the modification time.
	ModifiedAfter  *time.Time
	ModifiedBefore *time.Time

	// MinSize and MaxSize bound the file size.
	MinSize *int64
	MaxSize *int64

	// Tags restricts to nodes carrying all of these tag names.
	Tags []string

	Limit  int
	Offset int
}

// Result is one match.
type Result struct {
	Node *nodes.Node `json:"node"`
	// Path is the folder path, as names the caller may see. Empty for a node
	// reached through a share whose ancestors are hidden.
	Path []string `json:"path"`
	// Score is the trigram similarity, for ordering.
	Score float64 `json:"-"`
	// Shared reports whether the caller reaches this through a grant.
	Shared bool `json:"shared"`
}

// Results is a page of matches.
type Results struct {
	Results []Result `json:"results"`
	Total   int      `json:"total"`
	HasMore bool     `json:"has_more"`
}

// Backend is what a search implementation must provide.
//
// The interface exists so a different engine can be substituted without
// touching the API layer.
type Backend interface {
	Search(ctx context.Context, q Query) (*Results, error)
}

// MaxResults bounds a single page.
const (
	DefaultLimit = 50
	MaxLimit     = 200
	// MinQueryLength stops a one-character query from matching most of the
	// instance and doing a large amount of work for a useless answer.
	MinQueryLength = 2
	// MaxQueryLength bounds the pattern handed to the index.
	MaxQueryLength = 200
)

// Postgres searches using trigram similarity.
type Postgres struct {
	db *database.DB
}

// NewPostgres returns a PostgreSQL-backed search.
func NewPostgres(db *database.DB) *Postgres { return &Postgres{db: db} }

// Search finds nodes matching the query that the user may see.
//
// Visibility is enforced in the query itself rather than by filtering
// afterwards. Fetching matches and then discarding the ones the caller cannot
// see would make the result count wrong, make pagination incoherent, and — if
// the filter were ever missed — leak the existence of other people's files.
func (p *Postgres) Search(ctx context.Context, q Query) (*Results, error) {
	text := strings.TrimSpace(q.Text)
	if len([]rune(text)) < MinQueryLength {
		return &Results{Results: []Result{}}, nil
	}
	if len(text) > MaxQueryLength {
		text = text[:MaxQueryLength]
	}

	if q.Limit <= 0 || q.Limit > MaxLimit {
		q.Limit = DefaultLimit
	}
	if q.Offset < 0 {
		q.Offset = 0
	}

	pattern := "%" + escapeLike(text) + "%"

	var kindFilter *string
	if q.Kind.Valid() {
		k := string(q.Kind)
		kindFilter = &k
	}
	var mimeFilter *string
	if q.MimePrefix != "" {
		m := escapeLike(q.MimePrefix) + "%"
		mimeFilter = &m
	}
	var tagFilter []string
	if len(q.Tags) > 0 {
		tagFilter = q.Tags
	}

	// The visibility clause is the security boundary. A node is visible when
	// the user owns it, or holds a non-denied grant on it or on an ancestor.
	const visible = `
		AND (
			n.owner_id = $2
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
				    (p.subject_type = 'user' AND p.subject_id = $2)
				    OR (p.subject_type = 'group' AND p.subject_id IN (
				          SELECT group_id FROM group_members WHERE user_id = $2))
				  )
			)
		)`

	const filters = `
		WHERE n.deleted_at IS NULL
		  AND n.parent_id IS NOT NULL
		  AND n.name ILIKE $1
		  AND ($3::text IS NULL OR n.kind::text = $3)
		  AND ($4::text IS NULL OR n.mime_type ILIKE $4)
		  AND ($5::boolean IS NOT TRUE OR n.owner_id = $2)
		  AND ($6::timestamptz IS NULL OR n.updated_at >= $6)
		  AND ($7::timestamptz IS NULL OR n.updated_at <= $7)
		  AND ($8::bigint IS NULL OR n.size_bytes >= $8)
		  AND ($9::bigint IS NULL OR n.size_bytes <= $9)
		  AND ($10::uuid IS NULL OR EXISTS (
		        WITH RECURSIVE up AS (
		            SELECT n.id, n.parent_id, 0 AS depth
		            UNION ALL
		            SELECT m.id, m.parent_id, up.depth + 1
		            FROM nodes m JOIN up ON m.id = up.parent_id
		            WHERE up.depth < 64
		        )
		        SELECT 1 FROM up WHERE up.id = $10))
		  -- Tags are scoped to the caller's own. Matching on name alone would
		  -- let one user's filter be satisfied by a colleague's identically
		  -- named tag on a file they happen to share.
		  AND ($11::text[] IS NULL OR (
		        SELECT count(DISTINCT t.name::text) FROM file_tags ft
		        JOIN tags t ON t.id = ft.tag_id
		        WHERE ft.node_id = n.id AND t.owner_id = $2 AND t.name::text = ANY($11)
		      ) = cardinality($11))` + visible

	args := []any{
		pattern, q.UserID, kindFilter, mimeFilter, q.OwnedByMe,
		q.ModifiedAfter, q.ModifiedBefore, q.MinSize, q.MaxSize, q.InFolder, tagFilter,
	}

	var total int
	if err := p.db.Pool().QueryRow(ctx,
		`SELECT count(*) FROM nodes n`+filters, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("search: count: %w", err)
	}

	// Ordering: an exact prefix match first, then trigram similarity, then
	// recency. Somebody typing "rep" wants "Report.pdf" before "Quarterly
	// Reporting Notes.docx".
	rows, err := p.db.Pool().Query(ctx, `
		SELECT
			n.id, n.owner_id, n.parent_id, n.kind, n.name, n.size_bytes, n.mime_type,
			n.content_hash, n.created_at, n.updated_at, n.deleted_at, n.created_by, n.modified_by,
			similarity(n.name, $12) AS score,
			n.owner_id <> $2 AS shared
		FROM nodes n`+filters+`
		ORDER BY
			(lower(n.name) LIKE lower($13)) DESC,
			similarity(n.name, $12) DESC,
			n.updated_at DESC
		LIMIT $14 OFFSET $15`,
		append(args, text, escapeLike(text)+"%", q.Limit, q.Offset)...)
	if err != nil {
		return nil, fmt.Errorf("search: query: %w", err)
	}
	defer rows.Close()

	out := &Results{Total: total, Results: make([]Result, 0, q.Limit)}
	for rows.Next() {
		var n nodes.Node
		var result Result
		if err := rows.Scan(
			&n.ID, &n.OwnerID, &n.ParentID, &n.Kind, &n.Name, &n.SizeBytes, &n.MimeType,
			&n.ContentHash, &n.CreatedAt, &n.UpdatedAt, &n.DeletedAt, &n.CreatedBy, &n.ModifiedBy,
			&result.Score, &result.Shared,
		); err != nil {
			return nil, fmt.Errorf("search: scan: %w", err)
		}
		result.Node = &n
		out.Results = append(out.Results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search: query: %w", err)
	}

	out.HasMore = q.Offset+len(out.Results) < total
	return out, nil
}

// escapeLike neutralises the wildcards in an ILIKE pattern.
//
// Without this, a search for "100%" would match everything, and one for "_"
// would match every single-character name — surprising, and a way to make the
// server do far more work than the query looks like it should.
func escapeLike(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '%', '_', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

var _ Backend = (*Postgres)(nil)
