// Package jobs is a durable background work queue backed by PostgreSQL.
//
// PostgreSQL rather than Redis: `FOR UPDATE SKIP LOCKED` gives safe concurrent
// claiming, jobs survive a restart, and it adds no second stateful service to
// a 4 GB board. Redis would cost roughly 50 MB of resident memory to solve a
// problem the database already solves.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joshuanmoses/piCloud/backend/internal/database"
)

// Errors returned by this package.
var (
	// ErrNotFound means no such job.
	ErrNotFound = errors.New("jobs: not found")
	// ErrNoHandler means nothing is registered for a job's kind. The job is
	// parked rather than retried, because retrying cannot help.
	ErrNoHandler = errors.New("jobs: no handler registered for this kind")
	// ErrDuplicate means a pending job already holds the deduplication key.
	ErrDuplicate = errors.New("jobs: an equivalent job is already queued")
)

// Status is a job's lifecycle state.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Job is a unit of background work.
type Job struct {
	ID      uuid.UUID       `json:"id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`

	Status   Status `json:"status"`
	Priority int    `json:"priority"`

	Attempts    int `json:"attempts"`
	MaxAttempts int `json:"max_attempts"`

	RunAfter   time.Time  `json:"run_after"`
	DedupeKey  *string    `json:"dedupe_key,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Decode unmarshals the payload into dst.
func (j *Job) Decode(dst any) error {
	if len(j.Payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(j.Payload, dst); err != nil {
		return fmt.Errorf("jobs: cannot decode payload for %s: %w", j.Kind, err)
	}
	return nil
}

// Handler performs one job.
//
// Returning an error schedules a retry with backoff, up to MaxAttempts. A
// handler that knows retrying cannot help — a corrupt file, an unsupported
// format — should return a Permanent error instead.
type Handler func(ctx context.Context, job *Job) error

// Permanent wraps an error that must not be retried.
type Permanent struct{ Err error }

func (p *Permanent) Error() string { return "permanent: " + p.Err.Error() }
func (p *Permanent) Unwrap() error { return p.Err }

// PermanentError marks an error as not worth retrying.
func PermanentError(err error) error { return &Permanent{Err: err} }

// IsPermanent reports whether an error should skip retries.
func IsPermanent(err error) bool {
	var p *Permanent
	return errors.As(err, &p)
}

// DefaultMaxAttempts is used when a caller states no preference and the store
// was constructed without one.
const DefaultMaxAttempts = 5

// Store persists jobs.
type Store struct {
	db          *database.DB
	maxAttempts int
}

// NewStore returns a Store backed by db, using DefaultMaxAttempts.
func NewStore(db *database.DB) *Store {
	return &Store{db: db, maxAttempts: DefaultMaxAttempts}
}

// NewStoreWithAttempts returns a Store whose default retry budget is the
// operator's configured value. A caller that states its own MaxAttempts still
// wins: a thumbnail that failed to decode should stop after two tries whatever
// the global setting says.
func NewStoreWithAttempts(db *database.DB, maxAttempts int) *Store {
	if maxAttempts < 1 {
		maxAttempts = DefaultMaxAttempts
	}
	return &Store{db: db, maxAttempts: maxAttempts}
}

const columns = `
	id, kind, payload, status, priority, attempts, max_attempts,
	run_after, dedupe_key, last_error, created_at, updated_at, started_at, finished_at`

type scanner interface{ Scan(dest ...any) error }

func scanJob(sc scanner) (*Job, error) {
	var j Job
	err := sc.Scan(
		&j.ID, &j.Kind, &j.Payload, &j.Status, &j.Priority, &j.Attempts, &j.MaxAttempts,
		&j.RunAfter, &j.DedupeKey, &j.LastError, &j.CreatedAt, &j.UpdatedAt,
		&j.StartedAt, &j.FinishedAt,
	)
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// EnqueueParams describes work to schedule.
type EnqueueParams struct {
	Kind    string
	Payload any
	// Priority orders the queue; higher runs first. A thumbnail should not
	// delay a quota recalculation the administrator is waiting on.
	Priority int
	// RunAfter delays the job. Zero means immediately.
	RunAfter time.Time
	// DedupeKey collapses equivalent work. A file edited ten times in a minute
	// should produce one thumbnail job, not ten.
	DedupeKey   string
	MaxAttempts int
}

// Enqueue schedules work.
//
// When a pending job already holds the deduplication key, this is a no-op
// returning ErrDuplicate — which callers routinely ignore, because "the work
// is already queued" is success from their point of view.
func (s *Store) Enqueue(ctx context.Context, p EnqueueParams) (*Job, error) {
	if p.Kind == "" {
		return nil, fmt.Errorf("jobs: a job needs a kind")
	}
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = s.maxAttempts
		if p.MaxAttempts <= 0 {
			p.MaxAttempts = DefaultMaxAttempts
		}
	}
	if p.RunAfter.IsZero() {
		p.RunAfter = time.Now()
	}

	payload := json.RawMessage("{}")
	if p.Payload != nil {
		encoded, err := json.Marshal(p.Payload)
		if err != nil {
			return nil, fmt.Errorf("jobs: cannot encode payload: %w", err)
		}
		payload = encoded
	}

	var dedupe *string
	if p.DedupeKey != "" {
		dedupe = &p.DedupeKey
	}

	row := s.db.Pool().QueryRow(ctx, `
		INSERT INTO background_jobs (kind, payload, priority, run_after, dedupe_key, max_attempts)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+columns,
		p.Kind, payload, p.Priority, p.RunAfter, dedupe, p.MaxAttempts)

	job, err := scanJob(row)
	if err != nil {
		if database.IsUniqueViolation(err) {
			return nil, ErrDuplicate
		}
		return nil, fmt.Errorf("jobs: enqueue: %w", err)
	}
	return job, nil
}

// Claim takes the next due job, marking it running.
//
// `FOR UPDATE SKIP LOCKED` is what makes this safe with several workers: each
// takes a different row rather than blocking on the same one.
func (s *Store) Claim(ctx context.Context) (*Job, error) {
	row := s.db.Pool().QueryRow(ctx, `
		UPDATE background_jobs SET
			status = 'running',
			attempts = attempts + 1,
			started_at = now()
		WHERE id = (
			SELECT id FROM background_jobs
			WHERE status = 'pending' AND run_after <= now()
			ORDER BY priority DESC, run_after
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING `+columns)

	job, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("jobs: claim: %w", err)
	}
	return job, nil
}

// Succeed marks a job complete.
func (s *Store) Succeed(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.Pool().Exec(ctx,
		`UPDATE background_jobs SET status = 'succeeded', finished_at = now(), last_error = ''
		 WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("jobs: succeed: %w", err)
	}
	return nil
}

// Fail records a failure and schedules a retry, or parks the job.
//
// Backoff is exponential and capped: a job failing because a dependency is
// down should not hammer it, and on a Pi a tight retry loop is a real CPU
// cost.
func (s *Store) Fail(ctx context.Context, job *Job, cause error, permanent bool) error {
	message := cause.Error()
	if len(message) > 2000 {
		message = message[:2000]
	}

	if permanent || job.Attempts >= job.MaxAttempts {
		_, err := s.db.Pool().Exec(ctx,
			`UPDATE background_jobs SET status = 'failed', finished_at = now(), last_error = $2
			 WHERE id = $1`, job.ID, message)
		if err != nil {
			return fmt.Errorf("jobs: fail: %w", err)
		}
		return nil
	}

	delay := backoff(job.Attempts)
	_, err := s.db.Pool().Exec(ctx, `
		UPDATE background_jobs SET
			status = 'pending', run_after = now() + $2::interval,
			last_error = $3, started_at = NULL
		WHERE id = $1`, job.ID, delay, message)
	if err != nil {
		return fmt.Errorf("jobs: schedule retry: %w", err)
	}
	return nil
}

// backoff returns the delay before retry n.
func backoff(attempt int) time.Duration {
	const (
		base = 10 * time.Second
		max  = 30 * time.Minute
	)
	delay := base << min(attempt, 10)
	if delay > max {
		return max
	}
	return delay
}

// Get returns a job by identifier.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (*Job, error) {
	row := s.db.Pool().QueryRow(ctx, `SELECT `+columns+` FROM background_jobs WHERE id = $1`, id)
	job, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("jobs: get: %w", err)
	}
	return job, nil
}

// Stats summarises the queue for the administrator's dashboard.
type Stats struct {
	Pending   int `json:"pending"`
	Running   int `json:"running"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	// Oldest is the age of the oldest pending job, in seconds. A number that
	// keeps climbing means the workers are not keeping up.
	OldestPendingSeconds int64 `json:"oldest_pending_seconds"`
}

// Stats returns queue counts.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var out Stats
	err := s.db.Pool().QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status = 'pending'),
			count(*) FILTER (WHERE status = 'running'),
			count(*) FILTER (WHERE status = 'succeeded'),
			count(*) FILTER (WHERE status = 'failed'),
			coalesce(extract(epoch FROM now() - min(created_at) FILTER (WHERE status = 'pending')), 0)::bigint
		FROM background_jobs`).Scan(
		&out.Pending, &out.Running, &out.Succeeded, &out.Failed, &out.OldestPendingSeconds)
	if err != nil {
		return Stats{}, fmt.Errorf("jobs: stats: %w", err)
	}
	return out, nil
}

// ListByStatus returns jobs in one state, newest first.
//
// For the administrator's view. Bounded, because a failing handler can produce
// thousands of rows and an unbounded listing would make the page that reports
// the problem the second problem.
func (s *Store) ListByStatus(ctx context.Context, status Status, limit int) ([]*Job, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+columns+` FROM background_jobs
		 WHERE status = $1
		 ORDER BY updated_at DESC
		 LIMIT $2`, string(status), limit)
	if err != nil {
		return nil, fmt.Errorf("jobs: list by status: %w", err)
	}
	defer rows.Close()

	out := make([]*Job, 0, limit)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("jobs: scan: %w", err)
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

// Retry puts a failed job back in the queue.
//
// The attempt counter is reset, because an administrator retrying a job has
// usually just fixed whatever was wrong and wants the full budget again rather
// than the one attempt the old count would allow.
//
// Only a failed or cancelled job may be retried. Re-queuing a running one would
// let two workers execute it at once, which is the single thing the claim
// protocol exists to prevent.
func (s *Store) Retry(ctx context.Context, id uuid.UUID) (*Job, error) {
	row := s.db.Pool().QueryRow(ctx, `
		UPDATE background_jobs SET
			status = 'pending',
			attempts = 0,
			run_after = now(),
			last_error = '',
			started_at = NULL,
			finished_at = NULL
		WHERE id = $1 AND status IN ('failed', 'cancelled')
		RETURNING `+columns, id)

	job, err := scanJob(row)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("jobs: retry: %w", err)
	}
	return job, nil
}

// ReleaseStale returns jobs that were running when the process died.
//
// A job marked running with no worker alive would sit there forever. On
// startup, anything running longer than the threshold is put back.
func (s *Store) ReleaseStale(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.db.Pool().Exec(ctx, `
		UPDATE background_jobs SET status = 'pending', started_at = NULL,
			last_error = 'interrupted; requeued'
		WHERE status = 'running' AND started_at < now() - $1::interval`, olderThan)
	if err != nil {
		return 0, fmt.Errorf("jobs: release stale: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteFinished removes completed jobs older than retain.
func (s *Store) DeleteFinished(ctx context.Context, retain time.Duration) (int64, error) {
	tag, err := s.db.Pool().Exec(ctx, `
		DELETE FROM background_jobs
		WHERE status IN ('succeeded', 'cancelled') AND finished_at < now() - $1::interval`, retain)
	if err != nil {
		return 0, fmt.Errorf("jobs: delete finished: %w", err)
	}
	return tag.RowsAffected(), nil
}
