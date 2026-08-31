package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Runner executes queued work.
//
// It polls rather than listening on a notification channel. Polling is a
// database round trip every few seconds, which is nothing; LISTEN/NOTIFY would
// hold a dedicated connection open permanently, which on a pool sized for a
// Raspberry Pi is a meaningful fraction of the budget.
type Runner struct {
	store    *Store
	handlers map[string]Handler
	logger   *slog.Logger

	workers      int
	pollInterval time.Duration

	mu      sync.RWMutex
	running bool
	stop    chan struct{}
	done    sync.WaitGroup
}

// Config controls the runner.
type Config struct {
	// Workers is how many jobs run concurrently. Deliberately small: background
	// work must never starve interactive requests.
	Workers int
	// PollInterval is how often an idle worker checks for work.
	PollInterval time.Duration
}

// NewRunner returns a Runner.
func NewRunner(store *Store, cfg Config, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	if cfg.Workers > 16 {
		cfg.Workers = 16
	}
	if cfg.PollInterval < time.Second {
		cfg.PollInterval = 5 * time.Second
	}

	return &Runner{
		store:        store,
		handlers:     make(map[string]Handler),
		logger:       logger,
		workers:      cfg.Workers,
		pollInterval: cfg.PollInterval,
		stop:         make(chan struct{}),
	}
}

// Register associates a handler with a job kind. Must be called before Start.
func (r *Runner) Register(kind string, handler Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		panic("jobs: handlers must be registered before the runner starts")
	}
	r.handlers[kind] = handler
}

// Kinds lists the registered job kinds.
func (r *Runner) Kinds() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.handlers))
	for kind := range r.handlers {
		out = append(out, kind)
	}
	return out
}

// Start begins processing. It returns immediately.
func (r *Runner) Start(ctx context.Context) {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return
	}
	r.running = true
	r.mu.Unlock()

	// Anything left running when the process last died would sit there
	// forever. Five minutes is comfortably longer than any job should take
	// and short enough that a restart recovers quickly.
	if released, err := r.store.ReleaseStale(ctx, 5*time.Minute); err != nil {
		r.logger.Warn("could not requeue interrupted jobs", "error", err)
	} else if released > 0 {
		r.logger.Info("requeued jobs interrupted by a previous shutdown", "count", released)
	}

	for i := range r.workers {
		r.done.Add(1)
		go r.work(ctx, i)
	}

	r.logger.Info("background job runner started",
		"workers", r.workers, "poll_interval", r.pollInterval.String(), "kinds", r.Kinds())
}

// Stop halts processing and waits for in-flight jobs.
func (r *Runner) Stop() {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	r.running = false
	close(r.stop)
	r.mu.Unlock()

	r.done.Wait()
	r.logger.Info("background job runner stopped")
}

// work is one worker's loop.
func (r *Runner) work(ctx context.Context, index int) {
	defer r.done.Done()

	// Stagger the workers so they do not all poll in the same instant, which
	// would produce a periodic burst of identical queries.
	jitter := time.Duration(index) * r.pollInterval / time.Duration(r.workers)
	timer := time.NewTimer(jitter)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-timer.C:
		}

		// Drain the queue before sleeping again: after a burst of uploads
		// there may be many thumbnails waiting, and waiting a full interval
		// between each would make them appear one at a time.
		for {
			claimed, err := r.runOne(ctx)
			if err != nil || !claimed {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-r.stop:
				return
			default:
			}
		}

		timer.Reset(r.pollInterval)
	}
}

// runOne claims and executes a single job, reporting whether one was found.
func (r *Runner) runOne(ctx context.Context) (bool, error) {
	job, err := r.store.Claim(ctx)
	if err != nil {
		if err == ErrNotFound {
			return false, nil
		}
		// A claim failure is usually the database being briefly unavailable.
		// Log and back off rather than spinning.
		r.logger.Warn("could not claim a job", "error", err)
		return false, err
	}

	r.mu.RLock()
	handler, known := r.handlers[job.Kind]
	r.mu.RUnlock()

	if !known {
		// Retrying cannot conjure a handler. Park it so an administrator sees
		// the problem rather than letting it churn.
		r.logger.Error("no handler for job kind; parking it",
			"job_id", job.ID, "kind", job.Kind)
		if err := r.store.Fail(ctx, job, ErrNoHandler, true); err != nil {
			r.logger.Error("could not park a job", "job_id", job.ID, "error", err)
		}
		return true, nil
	}

	started := time.Now()
	err = r.execute(ctx, handler, job)
	elapsed := time.Since(started)

	if err != nil {
		permanent := IsPermanent(err)
		level := r.logger.Warn
		if permanent || job.Attempts >= job.MaxAttempts {
			level = r.logger.Error
		}
		level("job failed",
			"job_id", job.ID, "kind", job.Kind, "attempt", job.Attempts,
			"max_attempts", job.MaxAttempts, "permanent", permanent,
			"duration_ms", elapsed.Milliseconds(), "error", err.Error())

		if failErr := r.store.Fail(ctx, job, err, permanent); failErr != nil {
			r.logger.Error("could not record a job failure", "job_id", job.ID, "error", failErr)
		}
		return true, nil
	}

	if err := r.store.Succeed(ctx, job.ID); err != nil {
		r.logger.Error("could not record a job success", "job_id", job.ID, "error", err)
	}
	r.logger.Debug("job completed",
		"job_id", job.ID, "kind", job.Kind, "duration_ms", elapsed.Milliseconds())
	return true, nil
}

// RunOnce claims and executes at most one job, returning whether it found one.
//
// It exists so a caller can drain the queue synchronously: a test that started
// a worker would race its own assertions, and a one-shot maintenance command
// wants to finish rather than poll. It uses exactly the path Start uses, so
// what it exercises is the real thing.
func (r *Runner) RunOnce(ctx context.Context) (bool, error) {
	return r.runOne(ctx)
}

// Drain runs queued jobs until the queue is empty or limit jobs have run.
//
// The limit is a backstop: a handler that re-queues itself could otherwise
// spin here forever.
func (r *Runner) Drain(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	ran := 0
	for range limit {
		found, err := r.RunOnce(ctx)
		if err != nil {
			return ran, err
		}
		if !found {
			return ran, nil
		}
		ran++
	}
	return ran, nil
}

// execute runs a handler, converting a panic into a failure.
//
// A panicking handler must not take the worker — and with it every other kind
// of background work — down with it.
func (r *Runner) execute(ctx context.Context, handler Handler, job *Job) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			r.logger.Error("job handler panicked",
				"job_id", job.ID, "kind", job.Kind, "panic", recovered)
			err = fmt.Errorf("handler panicked: %v", recovered)
		}
	}()
	return handler(ctx, job)
}
