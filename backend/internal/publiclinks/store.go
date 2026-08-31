package publiclinks

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joshuanmoses/piCloud/backend/internal/database"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
)

// Store persists public links.
type Store struct {
	db  *database.DB
	now func() time.Time
}

// NewStore returns a Store backed by db.
func NewStore(db *database.DB) *Store { return &Store{db: db, now: time.Now} }

// SetClock overrides the time source. For tests only.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

const columns = `
	id, node_id, permissions, password_hash IS NOT NULL, expires_at,
	max_uses, use_count, max_upload_bytes, allowed_extensions,
	title, message, created_by, created_at, updated_at, revoked_at, last_used_at`

type scanner interface{ Scan(dest ...any) error }

func scanLink(sc scanner) (*Link, error) {
	var l Link
	var bits int32
	err := sc.Scan(
		&l.ID, &l.NodeID, &bits, &l.HasPassword, &l.ExpiresAt,
		&l.MaxUses, &l.UseCount, &l.MaxUploadBytes, &l.AllowedExtensions,
		&l.Title, &l.Message, &l.CreatedBy, &l.CreatedAt, &l.UpdatedAt,
		&l.RevokedAt, &l.LastUsedAt,
	)
	if err != nil {
		return nil, err
	}
	l.Permissions = permissions.Set(bits)
	return &l, nil
}

// InsertParams describes the row to insert.
//
// It is distinct from the service's CreateParams because that one carries a
// plaintext password and a kind name, neither of which this layer should see:
// hashing and policy belong to the service, storage belongs here.
type InsertParams struct {
	NodeID      uuid.UUID
	Permissions permissions.Set
	// PasswordHash is already hashed; this package never sees plaintext.
	PasswordHash      string
	ExpiresAt         *time.Time
	MaxUses           *int
	MaxUploadBytes    *int64
	AllowedExtensions []string
	Title             string
	Message           string
	CreatedBy         uuid.UUID
}

// Create mints a link and returns both the record and the raw token.
//
// The token is returned exactly once. It is never recoverable afterwards,
// because only its hash is stored — which means a leaked backup yields no
// usable links, and also that the interface must show the URL immediately.
func (s *Store) Create(ctx context.Context, p InsertParams) (*Link, Token, error) {
	token, err := GenerateToken()
	if err != nil {
		return nil, "", err
	}
	if p.Permissions == permissions.None {
		return nil, "", fmt.Errorf("publiclinks: refusing to create a link that grants nothing")
	}

	var passwordHash *string
	if p.PasswordHash != "" {
		passwordHash = &p.PasswordHash
	}
	extensions := NormalizeExtensions(p.AllowedExtensions)
	if extensions == nil {
		extensions = []string{}
	}

	var createdBy *uuid.UUID
	if p.CreatedBy != uuid.Nil {
		createdBy = &p.CreatedBy
	}

	row := s.db.Pool().QueryRow(ctx, `
		INSERT INTO public_links (
			node_id, token_hash, permissions, password_hash, expires_at,
			max_uses, max_upload_bytes, allowed_extensions, title, message, created_by
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING `+columns,
		p.NodeID, HashToken(token.Reveal()), int32(p.Permissions), passwordHash, p.ExpiresAt,
		p.MaxUses, p.MaxUploadBytes, extensions, p.Title, p.Message, createdBy)

	link, err := scanLink(row)
	if err != nil {
		return nil, "", fmt.Errorf("publiclinks: create: %w", err)
	}
	return link, token, nil
}

// Resolved is a link together with the password hash needed to verify access.
type Resolved struct {
	Link *Link
	// PasswordHash is empty when the link is not protected.
	PasswordHash string
}

// Resolve looks a token up and confirms the link is currently usable.
//
// Every failure — unknown token, revoked, expired, used up — returns
// ErrNotFound. Distinguishing them would tell somebody probing tokens that
// they had found a real one, which is the whole game for an unauthenticated
// endpoint.
func (s *Store) Resolve(ctx context.Context, rawToken string) (*Resolved, error) {
	if err := ValidateTokenShape(rawToken); err != nil {
		return nil, ErrNotFound
	}

	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+columns+`, coalesce(password_hash, '')
		 FROM public_links WHERE token_hash = $1`,
		HashToken(rawToken))

	var l Link
	var bits int32
	var passwordHash string
	err := row.Scan(
		&l.ID, &l.NodeID, &bits, &l.HasPassword, &l.ExpiresAt,
		&l.MaxUses, &l.UseCount, &l.MaxUploadBytes, &l.AllowedExtensions,
		&l.Title, &l.Message, &l.CreatedBy, &l.CreatedAt, &l.UpdatedAt,
		&l.RevokedAt, &l.LastUsedAt, &passwordHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("publiclinks: resolve: %w", err)
	}
	l.Permissions = permissions.Set(bits)

	if !l.IsLive(s.now()) {
		return nil, ErrNotFound
	}

	return &Resolved{Link: &l, PasswordHash: passwordHash}, nil
}

// RecordUse increments the usage counter and stamps the access time.
//
// The increment is conditional on the cap in the same statement, so two
// simultaneous downloads cannot both slip past a limit of one.
func (s *Store) RecordUse(ctx context.Context, id uuid.UUID, clientIP string) error {
	var addr any
	if ip, err := netip.ParseAddr(clientIP); err == nil {
		addr = ip.String()
	}
	_ = addr // reserved for the audit trail in Phase 9

	tag, err := s.db.Pool().Exec(ctx, `
		UPDATE public_links
		SET use_count = use_count + 1, last_used_at = now()
		WHERE id = $1
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())
		  AND (max_uses IS NULL OR use_count < max_uses)`, id)
	if err != nil {
		return fmt.Errorf("publiclinks: record use: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Something changed between resolving and using it.
		return ErrUsedUp
	}
	return nil
}

// Get returns a link by identifier.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (*Link, error) {
	row := s.db.Pool().QueryRow(ctx, `SELECT `+columns+` FROM public_links WHERE id = $1`, id)
	link, err := scanLink(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("publiclinks: get: %w", err)
	}
	return link, nil
}

// ListForCreator returns the links a user has published, newest first.
func (s *Store) ListForCreator(ctx context.Context, userID uuid.UUID) ([]*Link, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+columns+` FROM public_links
		 WHERE created_by = $1 ORDER BY created_at DESC LIMIT 500`, userID)
	if err != nil {
		return nil, fmt.Errorf("publiclinks: list for creator: %w", err)
	}
	defer rows.Close()

	var out []*Link
	for rows.Next() {
		link, err := scanLink(rows)
		if err != nil {
			return nil, fmt.Errorf("publiclinks: scan: %w", err)
		}
		out = append(out, link)
	}
	return out, rows.Err()
}

// ListForNode returns the links published on one node.
func (s *Store) ListForNode(ctx context.Context, nodeID uuid.UUID) ([]*Link, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+columns+` FROM public_links
		 WHERE node_id = $1 AND revoked_at IS NULL ORDER BY created_at DESC`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("publiclinks: list for node: %w", err)
	}
	defer rows.Close()

	var out []*Link
	for rows.Next() {
		link, err := scanLink(rows)
		if err != nil {
			return nil, fmt.Errorf("publiclinks: scan: %w", err)
		}
		out = append(out, link)
	}
	return out, rows.Err()
}

// LinkedNodes reports which of the given nodes currently carry a usable public
// link.
//
// Revoked, expired, and used-up links are excluded, because a link nobody can
// follow any more does not make an item public. The usage cap is applied in
// SQL rather than by loading the rows: a listing asks this for every row on the
// page, and the answer is one boolean per node.
func (s *Store) LinkedNodes(ctx context.Context, nodeIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := make(map[uuid.UUID]bool, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return out, nil
	}

	rows, err := s.db.Pool().Query(ctx,
		`SELECT DISTINCT node_id
		   FROM public_links
		  WHERE node_id = ANY($1)
		    AND revoked_at IS NULL
		    AND (expires_at IS NULL OR expires_at > $2)
		    AND (max_uses IS NULL OR use_count < max_uses)`,
		nodeIDs, s.now())
	if err != nil {
		return nil, fmt.Errorf("publiclinks: linked nodes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("publiclinks: scan linked node: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// Revoke withdraws a link.
//
// The row is kept rather than deleted so the owner's list can still show that
// the link existed and was withdrawn — and so a revoked token can never be
// reissued by chance.
func (s *Store) Revoke(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE public_links SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("publiclinks: revoke: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a link permanently.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Pool().Exec(ctx, `DELETE FROM public_links WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("publiclinks: delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExpired removes links that expired more than retain ago.
//
// Expired links stop working immediately — Resolve checks the deadline — so
// this only reclaims rows.
func (s *Store) DeleteExpired(ctx context.Context, retain time.Duration) (int64, error) {
	tag, err := s.db.Pool().Exec(ctx, `
		DELETE FROM public_links
		WHERE (expires_at IS NOT NULL AND expires_at < now() - $1::interval)
		   OR (revoked_at IS NOT NULL AND revoked_at < now() - $1::interval)`, retain)
	if err != nil {
		return 0, fmt.Errorf("publiclinks: delete expired: %w", err)
	}
	return tag.RowsAffected(), nil
}
