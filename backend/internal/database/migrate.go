package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// advisoryLockID is a fixed, arbitrary key used to serialize migrations. If two
// application instances start simultaneously — a rolling restart, or a stray
// `docker compose up --scale` — only one applies migrations; the other blocks
// and then observes them as already applied.
const advisoryLockID int64 = 7_314_902_551_003

// noTransactionDirective marks a migration that must run outside a transaction,
// for statements such as CREATE INDEX CONCURRENTLY.
const noTransactionDirective = "-- pifiles:no-transaction"

// Migration is one versioned schema change loaded from the embedded filesystem.
type Migration struct {
	Version  int
	Name     string
	Filename string
	SQL      string
	Checksum string
	// InTransaction is false when the file carries the no-transaction directive.
	InTransaction bool
}

// AppliedMigration is a row of the migration ledger.
type AppliedMigration struct {
	Version    int
	Name       string
	Checksum   string
	AppliedAt  time.Time
	DurationMs int64
}

// MigrationResult summarizes a migration run.
type MigrationResult struct {
	Applied         []Migration
	AlreadyCurrent  bool
	SchemaVersion   int
	TotalMigrations int
}

// LoadMigrations reads and validates every .sql file in fsys.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("database: cannot list migrations: %w", err)
	}
	if len(entries) == 0 {
		return nil, errors.New("database: no migrations found; the binary was built incorrectly")
	}

	migrations := make([]Migration, 0, len(entries))
	seen := make(map[int]string, len(entries))

	for _, name := range entries {
		version, label, err := parseMigrationFilename(name)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("database: duplicate migration version %d in %q and %q", version, prev, name)
		}
		seen[version] = name

		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("database: cannot read migration %q: %w", name, err)
		}
		if strings.TrimSpace(string(body)) == "" {
			return nil, fmt.Errorf("database: migration %q is empty", name)
		}

		sum := sha256.Sum256(body)
		migrations = append(migrations, Migration{
			Version:       version,
			Name:          label,
			Filename:      name,
			SQL:           string(body),
			Checksum:      hex.EncodeToString(sum[:]),
			InTransaction: !strings.Contains(string(body), noTransactionDirective),
		})
	}

	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

// parseMigrationFilename expects NNNN_snake_case_description.sql.
func parseMigrationFilename(name string) (int, string, error) {
	base := strings.TrimSuffix(path.Base(name), ".sql")
	numPart, label, found := strings.Cut(base, "_")
	if !found || numPart == "" || label == "" {
		return 0, "", fmt.Errorf("database: migration %q must be named NNNN_description.sql", name)
	}
	version, err := strconv.Atoi(numPart)
	if err != nil || version <= 0 {
		return 0, "", fmt.Errorf("database: migration %q has an invalid version prefix %q", name, numPart)
	}
	return version, label, nil
}

// Migrate applies every pending migration in order.
//
// Guarantees:
//   - Migrations are serialized across processes by a PostgreSQL advisory lock.
//   - Each migration and its ledger entry are committed together, so a crash
//     can never record a migration that did not fully apply.
//   - Previously applied migrations are checksum-verified; editing applied
//     history is refused rather than silently ignored.
func (db *DB) Migrate(ctx context.Context, fsys fs.FS) (MigrationResult, error) {
	logger := db.logger.With("component", "migrate")

	available, err := LoadMigrations(fsys)
	if err != nil {
		return MigrationResult{}, err
	}

	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return MigrationResult{}, fmt.Errorf("database: cannot acquire connection for migration: %w", err)
	}
	defer conn.Release()

	// Migrations can legitimately take longer than the default statement
	// timeout (a large index build, for example).
	if _, err := conn.Exec(ctx, "SET statement_timeout = 0"); err != nil {
		return MigrationResult{}, fmt.Errorf("database: cannot relax statement timeout: %w", err)
	}

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockID); err != nil {
		return MigrationResult{}, fmt.Errorf("database: cannot acquire migration lock: %w", err)
	}
	defer func() {
		// Release on a detached context so a cancelled request still frees the
		// lock for the next process.
		if _, err := conn.Exec(context.WithoutCancel(ctx),
			"SELECT pg_advisory_unlock($1)", advisoryLockID); err != nil {
			logger.Error("failed to release migration lock", "error", err)
		}
	}()

	if err := ensureLedger(ctx, conn.Conn()); err != nil {
		return MigrationResult{}, err
	}

	applied, err := loadLedger(ctx, conn.Conn())
	if err != nil {
		return MigrationResult{}, err
	}

	if err := verifyHistory(available, applied); err != nil {
		return MigrationResult{}, err
	}

	result := MigrationResult{TotalMigrations: len(available)}
	for _, m := range available {
		if _, done := applied[m.Version]; done {
			result.SchemaVersion = max(result.SchemaVersion, m.Version)
			continue
		}

		start := time.Now()
		logger.Info("applying migration", "version", m.Version, "name", m.Name)

		if err := applyOne(ctx, conn.Conn(), m); err != nil {
			return result, fmt.Errorf("database: migration %s failed: %w", m.Filename, err)
		}

		elapsed := time.Since(start)
		if err := recordApplied(ctx, conn.Conn(), m, elapsed); err != nil {
			return result, err
		}

		logger.Info("migration applied", "version", m.Version, "name", m.Name,
			"duration_ms", elapsed.Milliseconds())

		result.Applied = append(result.Applied, m)
		result.SchemaVersion = m.Version
	}

	result.AlreadyCurrent = len(result.Applied) == 0
	if result.AlreadyCurrent {
		logger.Info("database schema is current", "schema_version", result.SchemaVersion)
	} else {
		logger.Info("database schema updated",
			"applied", len(result.Applied), "schema_version", result.SchemaVersion)
	}
	return result, nil
}

// applyOne runs a single migration, wrapping it in a transaction unless the
// file opted out.
func applyOne(ctx context.Context, conn *pgx.Conn, m Migration) error {
	if !m.InTransaction {
		_, err := conn.Exec(ctx, m.SQL)
		return err
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("cannot begin: %w", err)
	}
	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return errors.Join(err, fmt.Errorf("rollback failed: %w", rbErr))
		}
		return err
	}
	return tx.Commit(ctx)
}

func ensureLedger(ctx context.Context, conn *pgx.Conn) error {
	const ddl = `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version     INTEGER     PRIMARY KEY,
			name        TEXT        NOT NULL,
			checksum    TEXT        NOT NULL,
			applied_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			duration_ms BIGINT      NOT NULL DEFAULT 0
		)`
	if _, err := conn.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("database: cannot create schema_migrations: %w", err)
	}
	return nil
}

func loadLedger(ctx context.Context, conn *pgx.Conn) (map[int]AppliedMigration, error) {
	rows, err := conn.Query(ctx,
		`SELECT version, name, checksum, applied_at, duration_ms FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("database: cannot read schema_migrations: %w", err)
	}
	defer rows.Close()

	out := make(map[int]AppliedMigration)
	for rows.Next() {
		var a AppliedMigration
		if err := rows.Scan(&a.Version, &a.Name, &a.Checksum, &a.AppliedAt, &a.DurationMs); err != nil {
			return nil, fmt.Errorf("database: cannot scan schema_migrations: %w", err)
		}
		out[a.Version] = a
	}
	return out, rows.Err()
}

// verifyHistory refuses to proceed when applied history and the embedded files
// disagree. Silently tolerating a mismatch means two deployments of the same
// version can have different schemas, which is far worse than a startup error.
func verifyHistory(available []Migration, applied map[int]AppliedMigration) error {
	known := make(map[int]Migration, len(available))
	for _, m := range available {
		known[m.Version] = m
	}

	for version, a := range applied {
		m, ok := known[version]
		if !ok {
			return fmt.Errorf(
				"database: migration %d (%s) is recorded as applied but is not present in this build; "+
					"this binary is older than the database schema", version, a.Name)
		}
		if m.Checksum != a.Checksum {
			return fmt.Errorf(
				"database: migration %s has been modified since it was applied "+
					"(recorded %s, found %s); applied migrations must never be edited — "+
					"add a new migration instead",
				m.Filename, shortSum(a.Checksum), shortSum(m.Checksum))
		}
	}

	// Detect a pending migration that sorts below an already-applied one, which
	// means two branches added migrations concurrently and one was renumbered.
	highestApplied := 0
	for version := range applied {
		highestApplied = max(highestApplied, version)
	}
	for _, m := range available {
		if _, done := applied[m.Version]; !done && m.Version < highestApplied {
			return fmt.Errorf(
				"database: migration %s is pending but version %d has already been applied; "+
					"renumber it above %d so migration order is unambiguous",
				m.Filename, highestApplied, highestApplied)
		}
	}
	return nil
}

func recordApplied(ctx context.Context, conn *pgx.Conn, m Migration, d time.Duration) error {
	_, err := conn.Exec(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, duration_ms) VALUES ($1, $2, $3, $4)`,
		m.Version, m.Name, m.Checksum, d.Milliseconds())
	if err != nil {
		return fmt.Errorf("database: cannot record migration %d: %w", m.Version, err)
	}
	return nil
}

// SchemaVersion returns the highest applied migration version, or 0 when the
// ledger does not yet exist.
func (db *DB) SchemaVersion(ctx context.Context) (int, error) {
	var version *int
	err := db.pool.QueryRow(ctx,
		`SELECT max(version) FROM schema_migrations`).Scan(&version)
	if err != nil {
		if ConstraintName(err) == "" && strings.Contains(err.Error(), "does not exist") {
			return 0, nil
		}
		return 0, err
	}
	if version == nil {
		return 0, nil
	}
	return *version, nil
}

func shortSum(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
