package auth

import (
	"sync"
	"time"
)

// RateLimiter is a fixed-window counter keyed by an arbitrary string.
//
// It is in-memory and per-process on purpose. PiFiles runs as a single
// application container, so a shared store would add a dependency for no
// benefit. If the process restarts, counters reset — acceptable, because
// account lockout is persisted in the database and is the durable half of
// brute-force protection. This limiter defends the *server* against a flood;
// lockout defends the *account* against guessing.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	limit   int
	window  time.Duration
	now     func() time.Time

	// maxKeys bounds memory. An attacker rotating source addresses could
	// otherwise grow this map without limit — the defence itself becoming the
	// denial of service.
	maxKeys int
}

type bucket struct {
	count       int
	windowStart time.Time
}

// DefaultMaxKeys bounds the number of tracked keys.
const DefaultMaxKeys = 10_000

// NewRateLimiter returns a limiter allowing limit events per window.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	if limit < 1 {
		limit = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	return &RateLimiter{
		buckets: make(map[string]*bucket),
		limit:   limit,
		window:  window,
		now:     time.Now,
		maxKeys: DefaultMaxKeys,
	}
}

// SetClock overrides the time source. For tests only.
func (r *RateLimiter) SetClock(now func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = now
}

// Allow records an event for key and reports whether it is within the limit.
// It also returns how long until the current window resets.
func (r *RateLimiter) Allow(key string) (bool, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()

	b, exists := r.buckets[key]
	if !exists || now.Sub(b.windowStart) >= r.window {
		if !exists && len(r.buckets) >= r.maxKeys {
			// Reclaim space before admitting a new key. If nothing is stale,
			// refuse rather than grow without bound.
			r.evictStaleLocked(now)
			if len(r.buckets) >= r.maxKeys {
				return false, r.window
			}
		}
		r.buckets[key] = &bucket{count: 1, windowStart: now}
		return true, r.window
	}

	b.count++
	remaining := r.window - now.Sub(b.windowStart)
	return b.count <= r.limit, remaining
}

// Reset clears the counter for a key, called after a successful sign-in so a
// legitimate user who mistyped a few times is not held back.
func (r *RateLimiter) Reset(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.buckets, key)
}

// evictStaleLocked drops buckets whose window has passed. The caller holds the
// lock.
func (r *RateLimiter) evictStaleLocked(now time.Time) {
	for key, b := range r.buckets {
		if now.Sub(b.windowStart) >= r.window {
			delete(r.buckets, key)
		}
	}
}

// Cleanup removes stale buckets. Safe to call periodically.
func (r *RateLimiter) Cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evictStaleLocked(r.now())
}

// Size returns the number of tracked keys, for metrics and tests.
func (r *RateLimiter) Size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buckets)
}
