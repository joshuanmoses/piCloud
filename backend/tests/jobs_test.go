package tests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joshuanmoses/piCloud/backend/internal/jobs"
	"github.com/joshuanmoses/piCloud/backend/internal/logging"
	"github.com/joshuanmoses/piCloud/backend/internal/maintenance"
)

// The background queue.
//
// It exists so that work which must survive a restart actually does, and so
// that two workers cannot run the same job. Both properties are cheap to state
// and easy to get wrong, so both are tested against a real database.

func TestAQueuedJobRunsAndIsMarkedSucceeded(t *testing.T) {
	h := newHarness(t)

	var ran bool
	runner := h.runnerWith("test.echo", func(context.Context, *jobs.Job) error {
		ran = true
		return nil
	})

	job, err := h.jobs.Enqueue(t.Context(), jobs.EnqueueParams{Kind: "test.echo"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if _, err := runner.Drain(t.Context(), 5); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if !ran {
		t.Fatal("the handler never ran")
	}

	after, err := h.jobs.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Status != jobs.StatusSucceeded {
		t.Errorf("status = %q, want succeeded", after.Status)
	}
}

// A file saved ten times in a minute should produce one thumbnail job, not ten.
func TestADeduplicationKeyCollapsesEquivalentWork(t *testing.T) {
	h := newHarness(t)

	if _, err := h.jobs.Enqueue(t.Context(), jobs.EnqueueParams{
		Kind: "test.thumb", DedupeKey: "node-123",
	}); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}

	_, err := h.jobs.Enqueue(t.Context(), jobs.EnqueueParams{
		Kind: "test.thumb", DedupeKey: "node-123",
	})
	if !errors.Is(err, jobs.ErrDuplicate) {
		t.Fatalf("second enqueue error = %v, want ErrDuplicate", err)
	}

	stats, err := h.jobs.Stats(t.Context())
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Pending != 1 {
		t.Errorf("pending = %d, want 1", stats.Pending)
	}
}

// This is what makes the queue safe with several workers. Without SKIP LOCKED
// two workers would either block on each other or run the same job twice.
func TestConcurrentWorkersEachClaimADifferentJob(t *testing.T) {
	h := newHarness(t)

	const count = 12
	for i := range count {
		if _, err := h.jobs.Enqueue(t.Context(), jobs.EnqueueParams{
			Kind:      "test.concurrent",
			DedupeKey: fmt.Sprintf("job-%d", i),
		}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	var mu sync.Mutex
	seen := map[string]int{}

	handler := func(_ context.Context, job *jobs.Job) error {
		mu.Lock()
		seen[job.ID.String()]++
		mu.Unlock()
		// Long enough that the workers genuinely overlap.
		time.Sleep(5 * time.Millisecond)
		return nil
	}

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runner := h.runnerWith("test.concurrent", handler)
			if _, err := runner.Drain(t.Context(), count); err != nil {
				t.Errorf("drain: %v", err)
			}
		}()
	}
	wg.Wait()

	if len(seen) != count {
		t.Errorf("%d distinct jobs ran, want %d", len(seen), count)
	}
	for id, times := range seen {
		if times != 1 {
			t.Errorf("job %s ran %d times, want exactly once", id, times)
		}
	}
}

func TestAFailingJobIsRetriedRatherThanSpinning(t *testing.T) {
	h := newHarness(t)

	attempts := 0
	runner := h.runnerWith("test.flaky", func(context.Context, *jobs.Job) error {
		attempts++
		return errors.New("still broken")
	})

	job, err := h.jobs.Enqueue(t.Context(), jobs.EnqueueParams{
		Kind: "test.flaky", MaxAttempts: 3,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// The first failure schedules a retry in the future, so a single drain
	// runs it exactly once. That delay is the point: without it a failing job
	// would occupy a worker continuously.
	if _, err := runner.Drain(t.Context(), 5); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("the handler ran %d times in one drain, want 1", attempts)
	}

	after, err := h.jobs.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Status != jobs.StatusPending {
		t.Errorf("status = %q after one of three attempts, want pending", after.Status)
	}
	if !after.RunAfter.After(time.Now()) {
		t.Error("the retry was not delayed; a failing job would spin")
	}
	if !strings.Contains(after.LastError, "still broken") {
		t.Errorf("last_error = %q, want the handler's message", after.LastError)
	}
}

// A decode failure will not become a decode success. Retrying it four more
// times wastes the only four cores the machine has.
func TestAPermanentFailureIsNotRetried(t *testing.T) {
	h := newHarness(t)

	runner := h.runnerWith("test.hopeless", func(context.Context, *jobs.Job) error {
		return jobs.PermanentError(errors.New("this file is not an image"))
	})

	job, err := h.jobs.Enqueue(t.Context(), jobs.EnqueueParams{
		Kind: "test.hopeless", MaxAttempts: 5,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if _, err := runner.Drain(t.Context(), 5); err != nil {
		t.Fatalf("drain: %v", err)
	}

	after, err := h.jobs.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Status != jobs.StatusFailed {
		t.Errorf("status = %q, want failed on the first permanent error", after.Status)
	}
}

// A handler that panics must not take the worker — and with it every other
// kind of background work — down with it.
func TestAPanickingHandlerFailsItsJobRatherThanTheProcess(t *testing.T) {
	h := newHarness(t)

	runner := h.runnerWith("test.panic", func(context.Context, *jobs.Job) error {
		panic("handler exploded")
	})

	job, err := h.jobs.Enqueue(t.Context(), jobs.EnqueueParams{Kind: "test.panic", MaxAttempts: 1})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if _, err := runner.Drain(t.Context(), 3); err != nil {
		t.Fatalf("drain: %v", err)
	}

	after, err := h.jobs.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Status != jobs.StatusFailed {
		t.Errorf("status = %q, want failed", after.Status)
	}
	if !strings.Contains(strings.ToLower(after.LastError), "panic") {
		t.Errorf("last_error = %q, want it to name the panic", after.LastError)
	}
}

// A job whose kind nothing handles must be parked, not retried: retrying cannot
// conjure a handler, and the churn hides the real problem from the operator.
func TestAJobWithNoHandlerIsParkedForAnOperatorToSee(t *testing.T) {
	h := newHarness(t)

	runner := jobs.NewRunner(h.jobs, jobs.Config{Workers: 1}, logging.Discard())

	job, err := h.jobs.Enqueue(t.Context(), jobs.EnqueueParams{Kind: "test.unknown"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := runner.Drain(t.Context(), 3); err != nil {
		t.Fatalf("drain: %v", err)
	}

	after, err := h.jobs.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Status != jobs.StatusFailed {
		t.Errorf("status = %q, want failed rather than an endless retry", after.Status)
	}
}

// --- Trash retention -------------------------------------------------------
//
// The one sweep that destroys user data, so it gets the most scrutiny.

func TestTheTrashSweepOnlyTouchesItemsPastTheRetentionWindow(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	recentID, _ := c.upload("root", "Deleted Today.txt", []byte("today"))
	oldID, _ := c.upload("root", "Deleted Long Ago.txt", []byte("old"))

	for _, id := range []string{recentID.String(), oldID.String()} {
		if rec := c.do("DELETE", "/api/v1/nodes/"+id, nil); rec.Code != 204 {
			t.Fatalf("delete %s: %d %s", id, rec.Code, rec.Body)
		}
	}

	// Age one of them past the window, directly, because waiting 30 days is
	// not an option.
	if _, err := h.db.Pool().Exec(t.Context(),
		`UPDATE nodes SET deleted_at = now() - interval '45 days' WHERE id = $1`, oldID); err != nil {
		t.Fatalf("ageing the trashed node: %v", err)
	}

	expired, err := h.nodes.ExpiredTrash(t.Context(), 30*24*time.Hour, 100)
	if err != nil {
		t.Fatalf("ExpiredTrash: %v", err)
	}

	if len(expired) != 1 {
		t.Fatalf("ExpiredTrash returned %d items, want only the aged one", len(expired))
	}
	// The point: today's deletion is untouched.
	if expired[0] != oldID {
		t.Errorf("ExpiredTrash returned %s, want the aged file %s", expired[0], oldID)
	}
}

// A retention period of zero means "keep forever". Reading it as "purge
// everything" would be catastrophic and is exactly the kind of misconfiguration
// that must fail safe.
func TestAZeroRetentionPeriodIsRefusedRatherThanPurgingEverything(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Precious.txt", []byte("x"))
	if rec := c.do("DELETE", "/api/v1/nodes/"+id.String(), nil); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}

	if _, err := h.nodes.ExpiredTrash(t.Context(), 0, 100); err == nil {
		t.Fatal("ExpiredTrash accepted a zero retention period")
	}
	if _, err := h.nodes.ExpiredTrash(t.Context(), -time.Hour, 100); err == nil {
		t.Fatal("ExpiredTrash accepted a negative retention period")
	}
}

// Purging a folder removes its descendants with it, so listing them separately
// would have the sweep try to purge rows that no longer exist.
func TestOnlyTheTopOfADeletedSubtreeIsListedForPurging(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	folderID := c.mkdir("root", "Project")
	c.upload(folderID.String(), "a.txt", []byte("a"))
	subID := c.mkdir(folderID.String(), "sub")
	c.upload(subID.String(), "b.txt", []byte("b"))

	if rec := c.do("DELETE", "/api/v1/nodes/"+folderID.String(), nil); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if _, err := h.db.Pool().Exec(t.Context(),
		`UPDATE nodes SET deleted_at = now() - interval '45 days' WHERE deleted_at IS NOT NULL`); err != nil {
		t.Fatalf("ageing the subtree: %v", err)
	}

	expired, err := h.nodes.ExpiredTrash(t.Context(), 30*24*time.Hour, 100)
	if err != nil {
		t.Fatalf("ExpiredTrash: %v", err)
	}
	if len(expired) != 1 {
		t.Fatalf("ExpiredTrash returned %d items, want only the deleted folder", len(expired))
	}
	if expired[0] != folderID {
		t.Errorf("ExpiredTrash returned %s, want the folder %s", expired[0], folderID)
	}
}

// A typo in a schedule's kind, or a handler registered under a different name,
// would silently park every run of that sweep.
func TestEveryScheduledSweepHasAHandler(t *testing.T) {
	h := newHarness(t)

	service := maintenance.New(maintenance.Deps{
		Config:      h.cfg,
		Logger:      logging.Discard(),
		Nodes:       h.nodes,
		Files:       h.files,
		Sessions:    h.sessions,
		Permissions: h.permissions,
		Recent:      h.recent,
		Previews:    h.previews,
		Storage:     h.storage,
		Queue:       h.jobs,
	})

	runner := jobs.NewRunner(h.jobs, jobs.Config{Workers: 1}, logging.Discard())
	service.Register(runner)

	registered := map[string]bool{}
	for _, kind := range runner.Kinds() {
		registered[kind] = true
	}

	for _, sched := range service.Schedules() {
		if !sched.Enabled {
			continue
		}
		if !registered[sched.Kind] {
			t.Errorf("%s is scheduled but has no handler; every run of it would be parked", sched.Kind)
		}
	}
	if len(registered) == 0 {
		t.Fatal("no maintenance handlers were registered at all")
	}
}

// --- Helpers ---------------------------------------------------------------

// runnerWith builds a runner with exactly one handler registered.
func (h *harness) runnerWith(kind string, handler jobs.Handler) *jobs.Runner {
	h.t.Helper()
	runner := jobs.NewRunner(h.jobs, jobs.Config{Workers: 1}, logging.Discard())
	runner.Register(kind, handler)
	return runner
}
