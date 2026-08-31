package users

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joshuanmoses/piCloud/backend/internal/database"
)

// Store persists users.
type Store struct {
	db *database.DB
}

// NewStore returns a Store backed by db.
func NewStore(db *database.DB) *Store { return &Store{db: db} }

// columns lists the selected fields in a fixed order shared by every scan, so
// a schema change cannot silently misalign one query's results.
const columns = `
	id, username, display_name, coalesce(email, ''), password_hash,
	is_enabled, is_admin, quota_bytes, used_bytes,
	created_at, updated_at, last_login_at,
	failed_login_count, locked_until, must_change_password`

// scanner is satisfied by both pgx.Row and pgx.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanUser(s scanner) (*User, error) {
	var u User
	err := s.Scan(
		&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.PasswordHash,
		&u.IsEnabled, &u.IsAdmin, &u.QuotaBytes, &u.UsedBytes,
		&u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt,
		&u.FailedLoginCount, &u.LockedUntil, &u.MustChangePassword,
	)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// Create inserts a new account.
func (s *Store) Create(ctx context.Context, p CreateParams) (*User, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}

	var email *string
	if p.Email != "" {
		email = &p.Email
	}

	row := s.db.Pool().QueryRow(ctx, `
		INSERT INTO users (
			username, display_name, email, password_hash,
			is_enabled, is_admin, quota_bytes, must_change_password
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+columns,
		p.Username, p.DisplayName, email, p.PasswordHash,
		p.IsEnabled, p.IsAdmin, p.QuotaBytes, p.MustChangePassword,
	)

	u, err := scanUser(row)
	if err != nil {
		return nil, translateConstraint(err)
	}
	return u, nil
}

// GetByID returns a user by identifier.
func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (*User, error) {
	row := s.db.Pool().QueryRow(ctx, `SELECT `+columns+` FROM users WHERE id = $1`, id)
	u, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("users: get by id: %w", err)
	}
	return u, nil
}

// GetByUsername returns a user by name. The column is citext, so the lookup is
// case-insensitive.
func (s *Store) GetByUsername(ctx context.Context, username string) (*User, error) {
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+columns+` FROM users WHERE username = $1`, NormalizeUsername(username))
	u, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("users: get by username: %w", err)
	}
	return u, nil
}

// Count returns the total number of accounts. Setup uses this to decide whether
// a first administrator still needs creating.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.db.Pool().QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("users: count: %w", err)
	}
	return n, nil
}

// CountAdmins returns the number of enabled administrators.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx,
		`SELECT count(*) FROM users WHERE is_admin AND is_enabled`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("users: count admins: %w", err)
	}
	return n, nil
}

// ListParams controls pagination and filtering of the user list.
type ListParams struct {
	Search  string
	Limit   int
	Offset  int
	Enabled *bool
}

// List returns a page of users and the total matching count.
func (s *Store) List(ctx context.Context, p ListParams) ([]*User, int, error) {
	if p.Limit <= 0 || p.Limit > 200 {
		p.Limit = 50
	}
	if p.Offset < 0 {
		p.Offset = 0
	}

	// A NULL-able parameter pattern keeps this a single prepared statement
	// rather than string-built SQL.
	var search *string
	if s := p.Search; s != "" {
		pattern := "%" + s + "%"
		search = &pattern
	}

	const where = `
		WHERE ($1::text IS NULL OR username ILIKE $1 OR display_name ILIKE $1 OR email ILIKE $1)
		  AND ($2::boolean IS NULL OR is_enabled = $2)`

	var total int
	if err := s.db.Pool().QueryRow(ctx,
		`SELECT count(*) FROM users`+where, search, p.Enabled).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("users: list count: %w", err)
	}

	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+columns+` FROM users`+where+`
		 ORDER BY username LIMIT $3 OFFSET $4`,
		search, p.Enabled, p.Limit, p.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("users: list: %w", err)
	}
	defer rows.Close()

	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("users: scan: %w", err)
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

// UpdateParams describes a partial update. A nil field is left unchanged.
type UpdateParams struct {
	DisplayName        *string
	Email              *string
	IsEnabled          *bool
	IsAdmin            *bool
	QuotaBytes         **int64 // double pointer: distinguishes "unset" from "set to unlimited"
	MustChangePassword *bool
}

// Update applies a partial update.
//
// It refuses to disable or demote the last enabled administrator: doing so
// would permanently lock everyone out of administration with no recovery path
// short of editing the database by hand.
func (s *Store) Update(ctx context.Context, id uuid.UUID, p UpdateParams) (*User, error) {
	var updated *User

	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		// Lock the row so a concurrent update cannot race the last-admin check.
		var current User
		row := tx.QueryRow(ctx, `SELECT `+columns+` FROM users WHERE id = $1 FOR UPDATE`, id)
		u, err := scanUser(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("users: load for update: %w", err)
		}
		current = *u

		losingAdmin := current.IsAdmin && ((p.IsAdmin != nil && !*p.IsAdmin) ||
			(p.IsEnabled != nil && !*p.IsEnabled))
		if losingAdmin && current.IsEnabled {
			var others int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM users WHERE is_admin AND is_enabled AND id <> $1`,
				id).Scan(&others); err != nil {
				return fmt.Errorf("users: count other admins: %w", err)
			}
			if others == 0 {
				return ErrLastAdmin
			}
		}

		if p.DisplayName != nil {
			name := strings.TrimSpace(*p.DisplayName)
			if err := ValidateDisplayName(name); err != nil {
				return err
			}
			current.DisplayName = name
		}
		if p.Email != nil {
			email := strings.TrimSpace(strings.ToLower(*p.Email))
			if err := ValidateEmail(email); err != nil {
				return err
			}
			current.Email = email
		}
		if p.IsEnabled != nil {
			current.IsEnabled = *p.IsEnabled
		}
		if p.IsAdmin != nil {
			current.IsAdmin = *p.IsAdmin
		}
		if p.QuotaBytes != nil {
			current.QuotaBytes = *p.QuotaBytes
		}
		if p.MustChangePassword != nil {
			current.MustChangePassword = *p.MustChangePassword
		}

		var email *string
		if current.Email != "" {
			email = &current.Email
		}

		row = tx.QueryRow(ctx, `
			UPDATE users SET
				display_name = $2, email = $3, is_enabled = $4,
				is_admin = $5, quota_bytes = $6, must_change_password = $7
			WHERE id = $1
			RETURNING `+columns,
			id, current.DisplayName, email, current.IsEnabled,
			current.IsAdmin, current.QuotaBytes, current.MustChangePassword)

		updated, err = scanUser(row)
		if err != nil {
			return translateConstraint(err)
		}
		return nil
	})

	return updated, err
}

// SetPassword replaces the password hash and clears both the lockout state and
// the must-change flag.
func (s *Store) SetPassword(ctx context.Context, id uuid.UUID, hash string) error {
	tag, err := s.db.Pool().Exec(ctx, `
		UPDATE users
		SET password_hash = $2,
		    must_change_password = FALSE,
		    failed_login_count = 0,
		    locked_until = NULL
		WHERE id = $1`, id, hash)
	if err != nil {
		return fmt.Errorf("users: set password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes an account. Refuses to remove the last enabled administrator.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		var isAdmin, isEnabled bool
		err := tx.QueryRow(ctx,
			`SELECT is_admin, is_enabled FROM users WHERE id = $1 FOR UPDATE`,
			id).Scan(&isAdmin, &isEnabled)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("users: load for delete: %w", err)
		}

		if isAdmin && isEnabled {
			var others int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM users WHERE is_admin AND is_enabled AND id <> $1`,
				id).Scan(&others); err != nil {
				return fmt.Errorf("users: count other admins: %w", err)
			}
			if others == 0 {
				return ErrLastAdmin
			}
		}

		if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, id); err != nil {
			return fmt.Errorf("users: delete: %w", err)
		}
		return nil
	})
}

// RecordSuccessfulLogin stamps the login time and clears the failure counter.
func (s *Store) RecordSuccessfulLogin(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := s.db.Pool().Exec(ctx, `
		UPDATE users
		SET last_login_at = $2, failed_login_count = 0, locked_until = NULL
		WHERE id = $1`, id, at)
	if err != nil {
		return fmt.Errorf("users: record login: %w", err)
	}
	return nil
}

// RecordFailedLogin increments the failure counter and locks the account once
// the threshold is reached. It returns the new count and the lock deadline.
//
// The increment and the threshold test happen in one statement so that
// concurrent attempts cannot race past the limit.
func (s *Store) RecordFailedLogin(
	ctx context.Context, id uuid.UUID, maxAttempts int, lockDuration time.Duration,
) (count int, lockedUntil *time.Time, err error) {
	err = s.db.Pool().QueryRow(ctx, `
		UPDATE users
		SET failed_login_count = failed_login_count + 1,
		    locked_until = CASE
		        WHEN failed_login_count + 1 >= $2 THEN now() + $3::interval
		        ELSE locked_until
		    END
		WHERE id = $1
		RETURNING failed_login_count, locked_until`,
		id, maxAttempts, lockDuration).Scan(&count, &lockedUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, ErrNotFound
	}
	if err != nil {
		return 0, nil, fmt.Errorf("users: record failed login: %w", err)
	}
	return count, lockedUntil, nil
}

// Unlock clears a lockout, used by an administrator.
func (s *Store) Unlock(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE users SET failed_login_count = 0, locked_until = NULL WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("users: unlock: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// translateConstraint converts a database constraint violation into a domain
// error, so callers never see — and never leak — the constraint name.
func translateConstraint(err error) error {
	if !database.IsUniqueViolation(err) {
		return fmt.Errorf("users: %w", err)
	}
	switch database.ConstraintName(err) {
	case "users_username_key":
		return ErrUsernameTaken
	case "users_email_key":
		return ErrEmailTaken
	default:
		return fmt.Errorf("users: %w", err)
	}
}

// GetManyPublic loads the public profiles of several users at once.
//
// Used when rendering a listing that may contain items owned by different
// people: one query rather than one per row.
func (s *Store) GetManyPublic(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]Public, error) {
	result := make(map[uuid.UUID]Public, len(ids))
	if len(ids) == 0 {
		return result, nil
	}

	rows, err := s.db.Pool().Query(ctx,
		`SELECT id, username, display_name FROM users WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("users: get many: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var p Public
		if err := rows.Scan(&p.ID, &p.Username, &p.DisplayName); err != nil {
			return nil, fmt.Errorf("users: scan public: %w", err)
		}
		result[p.ID] = p
	}
	return result, rows.Err()
}
