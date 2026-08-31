package auth

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRateLimiterAllowsUpToTheLimit(t *testing.T) {
	t.Parallel()
	r := NewRateLimiter(3, time.Minute)

	for i := 1; i <= 3; i++ {
		if ok, _ := r.Allow("key"); !ok {
			t.Fatalf("attempt %d was refused but is within the limit", i)
		}
	}
	if ok, retry := r.Allow("key"); ok {
		t.Error("the fourth attempt exceeded the limit and should be refused")
	} else if retry <= 0 {
		t.Error("a refusal should report when the caller may retry")
	}
}

func TestRateLimiterIsPerKey(t *testing.T) {
	t.Parallel()
	r := NewRateLimiter(1, time.Minute)

	if ok, _ := r.Allow("first"); !ok {
		t.Fatal("first key should be allowed")
	}
	// One key exhausting its budget must not affect another, or a single
	// noisy client would lock everyone else out.
	if ok, _ := r.Allow("second"); !ok {
		t.Error("a different key must have its own budget")
	}
	if ok, _ := r.Allow("first"); ok {
		t.Error("the first key should now be over its limit")
	}
}

func TestRateLimiterWindowResets(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := NewRateLimiter(2, time.Minute)
	r.SetClock(func() time.Time { return now })

	r.Allow("key")
	r.Allow("key")
	if ok, _ := r.Allow("key"); ok {
		t.Fatal("should be over the limit")
	}

	now = now.Add(time.Minute + time.Second)
	if ok, _ := r.Allow("key"); !ok {
		t.Error("the budget should be restored once the window has passed")
	}
}

func TestRateLimiterResetClearsAKey(t *testing.T) {
	t.Parallel()
	r := NewRateLimiter(1, time.Minute)
	r.Allow("key")
	if ok, _ := r.Allow("key"); ok {
		t.Fatal("should be over the limit")
	}

	// A successful sign-in resets the counter, so a user who mistyped twice is
	// not held back afterwards.
	r.Reset("key")
	if ok, _ := r.Allow("key"); !ok {
		t.Error("Reset should restore the budget")
	}
}

// An attacker rotating source addresses must not be able to grow the limiter's
// memory without bound — the defence becoming the denial of service.
func TestRateLimiterBoundsMemory(t *testing.T) {
	t.Parallel()
	r := NewRateLimiter(5, time.Minute)
	r.maxKeys = 100

	for i := range 500 {
		r.Allow(fmt.Sprintf("attacker-%d", i))
	}
	if size := r.Size(); size > r.maxKeys {
		t.Errorf("limiter grew to %d keys, above the %d cap", size, r.maxKeys)
	}
}

func TestRateLimiterCleanupReclaimsStaleKeys(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := NewRateLimiter(5, time.Minute)
	r.SetClock(func() time.Time { return now })

	for i := range 10 {
		r.Allow(fmt.Sprintf("key-%d", i))
	}
	if r.Size() != 10 {
		t.Fatalf("size = %d, want 10", r.Size())
	}

	now = now.Add(2 * time.Minute)
	r.Cleanup()
	if r.Size() != 0 {
		t.Errorf("size after cleanup = %d, want 0", r.Size())
	}
}

func TestRateLimiterIsConcurrencySafe(t *testing.T) {
	t.Parallel()
	r := NewRateLimiter(1000, time.Minute)

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				r.Allow("shared")
			}
		}()
	}
	wg.Wait()

	// 50 × 20 = 1000 attempts, exactly the limit; the next must be refused.
	if ok, _ := r.Allow("shared"); ok {
		t.Error("the counter lost increments under concurrency")
	}
}
