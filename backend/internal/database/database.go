// Package database owns the PostgreSQL connection pool and schema migrations.
//
// Pool sizing is deliberately conservative. A Raspberry Pi running PostgreSQL
// with max_connections=50 cannot usefully service dozens of concurrent
// application connections; a small pool with queuing produces better latency
// than a large pool that thrashes.
package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/tracelog"

	"github.com/joshuanmoses/piCloud/backend/internal/config"
	"github.com/joshuanmoses/piCloud/backend/internal/health"
)

// DB wraps a pgx pool with the behaviour the rest of the application needs.
type DB struct {
	pool   *pgxpool.Pool
	cfg    config.Database
	logger *slog.Logger
}

// Connect opens the pool and verifies connectivity. It retries, because under
// Docker Compose the application frequently starts before PostgreSQL finishes
// its first-run initialization, and a health-check dependency alone does not
// always cover the gap on slower boards.
func Connect(ctx context.Context, cfg config.Database, logger *slog.Logger) (*DB, error) {
	if logger == nil {
		logger = slog.Default()
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		// The DSN contains the password. Report the redacted form.
		return nil, fmt.Errorf("database: invalid connection settings (%s): %w", cfg.SafeDSN(), err)
	}

	poolCfg.MaxConns = int32(cfg.MaxConns)
	poolCfg.MinConns = int32(cfg.MinConns)
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	// Jitter prevents the whole pool from recycling in lockstep, which on a Pi
	// shows up as a periodic latency spike.
	poolCfg.MaxConnLifetimeJitter = cfg.MaxConnLifetime / 10
	poolCfg.HealthCheckPeriod = 30 * time.Second

	// A statement timeout bounds any single query, so one pathological request
	// cannot pin a connection indefinitely.
	if cfg.StatementTimeout > 0 {
		poolCfg.ConnConfig.RuntimeParams["statement_timeout"] =
			fmt.Sprintf("%d", cfg.StatementTimeout.Milliseconds())
	}
	// Idle transactions hold locks and bloat; nothing in PiFiles legitimately
	// keeps one open, so kill them aggressively.
	poolCfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "60000"

	poolCfg.ConnConfig.Tracer = &tracelog.TraceLog{
		Logger:   pgxSlogAdapter{logger: logger.With("component", "pgx")},
		LogLevel: tracelog.LogLevelWarn,
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("database: cannot create connection pool: %w", err)
	}

	db := &DB{pool: pool, cfg: cfg, logger: logger}

	if err := db.waitForReady(ctx); err != nil {
		pool.Close()
		return nil, err
	}

	var serverVersion string
	if err := pool.QueryRow(ctx, "SELECT current_setting('server_version')").Scan(&serverVersion); err != nil {
		serverVersion = "unknown"
	}

	logger.Info("database connected",
		"host", cfg.Host,
		"port", cfg.Port,
		"database", cfg.Name,
		"server_version", serverVersion,
		"max_conns", cfg.MaxConns,
	)
	return db, nil
}

// waitForReady pings with backoff until the server answers or ctx expires.
func (db *DB) waitForReady(ctx context.Context) error {
	const (
		maxAttempts  = 30
		initialDelay = 250 * time.Millisecond
		maxDelay     = 3 * time.Second
	)

	delay := initialDelay
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, db.cfg.ConnectTimeout)
		err := db.pool.Ping(pingCtx)
		cancel()

		if err == nil {
			if attempt > 1 {
				db.logger.Info("database became available", "attempts", attempt)
			}
			return nil
		}
		lastErr = err

		if ctx.Err() != nil {
			return fmt.Errorf("database: startup cancelled while waiting for %s:%d: %w",
				db.cfg.Host, db.cfg.Port, ctx.Err())
		}
		if attempt == 1 || attempt%5 == 0 {
			db.logger.Warn("waiting for database",
				"host", db.cfg.Host, "port", db.cfg.Port,
				"attempt", attempt, "max_attempts", maxAttempts, "error", err.Error())
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("database: startup cancelled: %w", ctx.Err())
		case <-time.After(delay):
		}
		if delay < maxDelay {
			delay *= 2
		}
	}
	return fmt.Errorf("database: unreachable at %s:%d after %d attempts: %w",
		db.cfg.Host, db.cfg.Port, maxAttempts, lastErr)
}

// Pool exposes the underlying pool for query execution.
func (db *DB) Pool() *pgxpool.Pool { return db.pool }

// Close shuts the pool down, waiting for in-flight queries.
func (db *DB) Close() {
	if db != nil && db.pool != nil {
		db.pool.Close()
	}
}

// Ping verifies the connection is live.
func (db *DB) Ping(ctx context.Context) error { return db.pool.Ping(ctx) }

// InTx runs fn inside a transaction, committing on success and rolling back on
// error or panic. Every multi-statement mutation in PiFiles goes through this,
// so that a partial failure can never leave inconsistent metadata behind.
func (db *DB) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return db.InTxOptions(ctx, pgx.TxOptions{}, fn)
}

// InTxOptions is InTx with explicit transaction options.
func (db *DB) InTxOptions(ctx context.Context, opts pgx.TxOptions, fn func(tx pgx.Tx) error) (err error) {
	tx, err := db.pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("database: cannot begin transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			// Roll back before re-panicking so the connection is not returned
			// to the pool mid-transaction.
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
		if err != nil {
			// Use a detached context: if the request was cancelled, the
			// rollback must still be delivered.
			if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
				err = errors.Join(err, fmt.Errorf("rollback failed: %w", rbErr))
			}
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("database: commit failed: %w", err)
	}
	return nil
}

// Stats summarizes pool utilization for the admin dashboard.
type Stats struct {
	AcquiredConns        int32 `json:"acquired_conns"`
	IdleConns            int32 `json:"idle_conns"`
	TotalConns           int32 `json:"total_conns"`
	MaxConns             int32 `json:"max_conns"`
	AcquireCount         int64 `json:"acquire_count"`
	EmptyAcquireCount    int64 `json:"empty_acquire_count"`
	CanceledAcquireCount int64 `json:"canceled_acquire_count"`
}

// Stats returns a snapshot of pool utilization.
func (db *DB) Stats() Stats {
	s := db.pool.Stat()
	return Stats{
		AcquiredConns:        s.AcquiredConns(),
		IdleConns:            s.IdleConns(),
		TotalConns:           s.TotalConns(),
		MaxConns:             s.MaxConns(),
		AcquireCount:         s.AcquireCount(),
		EmptyAcquireCount:    s.EmptyAcquireCount(),
		CanceledAcquireCount: s.CanceledAcquireCount(),
	}
}

// Health describes the database for the health endpoint. It intentionally
// omits host, port, credentials, and server version: /health is unauthenticated
// and must not describe the infrastructure to strangers.
type Health struct {
	Status    health.Status `json:"status"`
	LatencyMs int64         `json:"latency_ms"`
	Detail    string        `json:"detail,omitempty"`
	OpenConns int32         `json:"open_conns"`
	MaxConns  int32         `json:"max_conns"`
}

// Check probes the database and summarizes its condition.
func (db *DB) Check(ctx context.Context) Health {
	stats := db.Stats()
	h := Health{Status: health.Healthy, OpenConns: stats.TotalConns, MaxConns: stats.MaxConns}

	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	start := time.Now()
	var one int
	err := db.pool.QueryRow(probeCtx, "SELECT 1").Scan(&one)
	h.LatencyMs = time.Since(start).Milliseconds()

	switch {
	case err != nil:
		// The caller-facing detail is deliberately generic; the real error is
		// logged with full context by the health handler.
		h.Status = health.Unhealthy
		h.Detail = "database query failed"
	case one != 1:
		h.Status = health.Unhealthy
		h.Detail = "database returned an unexpected result"
	case h.LatencyMs > 1000:
		h.Status = health.Degraded
		h.Detail = "database is responding slowly"
	case stats.MaxConns > 0 && stats.AcquiredConns >= stats.MaxConns:
		h.Status = health.Degraded
		h.Detail = "connection pool is saturated"
	}
	return h
}

// IsUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation, so callers can translate it into a domain-specific 409 rather than
// leaking the constraint name to the client.
func IsUniqueViolation(err error) bool { return hasSQLState(err, "23505") }

// IsForeignKeyViolation reports whether err is a foreign-key violation.
func IsForeignKeyViolation(err error) bool { return hasSQLState(err, "23503") }

// IsCheckViolation reports whether err is a check-constraint violation.
func IsCheckViolation(err error) bool { return hasSQLState(err, "23514") }

// IsNotFound reports whether err indicates no rows matched.
func IsNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// ConstraintName returns the violated constraint, or "" when err is not a
// constraint violation. Use it for server-side branching only; never return it
// to a client.
func ConstraintName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

func hasSQLState(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

// pgxSlogAdapter bridges pgx's tracelog to slog. Query text is included only at
// debug level; parameter values are never logged, because they routinely
// contain password hashes and session tokens.
type pgxSlogAdapter struct{ logger *slog.Logger }

func (a pgxSlogAdapter) Log(ctx context.Context, level tracelog.LogLevel, msg string, data map[string]any) {
	attrs := make([]any, 0, 6)
	for k, v := range data {
		switch k {
		case "args", "arguments", "params":
			// Never log query arguments.
			continue
		case "sql":
			if level > tracelog.LogLevelDebug {
				attrs = append(attrs, slog.String("sql", fmt.Sprint(v)))
			}
		case "time":
			attrs = append(attrs, slog.String("duration", fmt.Sprint(v)))
		case "err":
			attrs = append(attrs, slog.String("error", fmt.Sprint(v)))
		case "pid", "rowCount", "commandTag":
			attrs = append(attrs, slog.Any(k, v))
		}
	}

	switch level {
	case tracelog.LogLevelTrace, tracelog.LogLevelDebug:
		a.logger.Debug(msg, attrs...)
	case tracelog.LogLevelInfo:
		a.logger.Info(msg, attrs...)
	case tracelog.LogLevelWarn:
		a.logger.Warn(msg, attrs...)
	default:
		a.logger.Error(msg, attrs...)
	}
}
