package maintenance

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// Scheduler keeps the recurring sweeps queued.
//
// It is deliberately thin: it enqueues, and the runner executes. Splitting the
// two means a sweep that takes four minutes does not delay the next tick, and
// a restart mid-sweep leaves a queued job rather than losing the round.
//
// One instance per process. With more than one PiFiles process against the same
// database — not a supported topology, but not one that should corrupt
// anything either — the dedupe key means duplicate enqueues collapse into one
// job rather than running the sweep twice.
type Scheduler struct {
	service *Service

	mu      sync.Mutex
	running bool
	stop    chan struct{}
	done    sync.WaitGroup
}

// NewScheduler returns a Scheduler for the given service.
func NewScheduler(service *Service) *Scheduler {
	return &Scheduler{service: service, stop: make(chan struct{})}
}

// Start begins scheduling. It returns immediately.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	enabled := 0
	for _, sched := range s.service.Schedules() {
		if !sched.Enabled {
			continue
		}
		enabled++
		s.done.Add(1)
		go s.loop(ctx, sched)
	}

	s.service.logger.Info("maintenance scheduler started", "sweeps", enabled)
}

// loop enqueues one kind on its interval.
func (s *Scheduler) loop(ctx context.Context, sched Schedule) {
	defer s.done.Done()

	// Stagger the first run. Queueing every sweep in the first second after
	// boot would put the heaviest load on the machine at exactly the moment a
	// person is waiting for the interface to appear.
	initial := time.Duration(rand.Int64N(int64(90 * time.Second)))
	select {
	case <-ctx.Done():
		return
	case <-s.stop:
		return
	case <-time.After(initial):
	}

	ticker := time.NewTicker(sched.Interval)
	defer ticker.Stop()

	s.service.enqueue(ctx, sched.Kind, 0)

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-ticker.C:
			s.service.enqueue(ctx, sched.Kind, 0)
		}
	}
}

// Stop halts scheduling and waits for the loops to exit. In-flight jobs are
// unaffected; the runner drains those.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	close(s.stop)
	s.mu.Unlock()

	s.done.Wait()
	s.service.logger.Info("maintenance scheduler stopped")
}

// Kinds returns the job kinds this scheduler manages, for the administrator's
// job view.
func (s *Scheduler) Kinds() []string {
	var out []string
	for _, sched := range s.service.Schedules() {
		if sched.Enabled {
			out = append(out, sched.Kind)
		}
	}
	return out
}
