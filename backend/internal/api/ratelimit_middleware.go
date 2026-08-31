package api

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/joshuanmoses/piCloud/backend/internal/auth"
	"github.com/joshuanmoses/piCloud/backend/internal/logging"
)

// API rate limiting.
//
// Sign-in has had its own limiter since Phase 2, protecting the account. This
// protects the *server*: a signed-in user, or a script holding one valid
// session, could otherwise hammer any endpoint as fast as the network allows.
// On a Raspberry Pi that is not a theoretical concern — a loop calling search
// will saturate four cores long before it saturates the link.
//
// Keyed on the account where there is one and the client address otherwise.
// Per-account rather than per-address is deliberate: a household behind one
// NAT address shares an IP, and limiting them collectively would punish the
// family for one person's sync client.

// LimitClass names a budget. Operations differ by orders of magnitude in cost,
// so one number for all of them would be either useless or crippling.
type LimitClass string

const (
	// LimitRead covers listings, metadata, and downloads.
	LimitRead LimitClass = "read"
	// LimitWrite covers anything that changes state.
	LimitWrite LimitClass = "write"
	// LimitUpload covers upload sessions and chunk transfers, which are
	// legitimately numerous for one large file.
	LimitUpload LimitClass = "upload"
	// LimitSearch is its own budget: a trigram scan across a shared tree is
	// the most expensive read the API offers.
	LimitSearch LimitClass = "search"
	// LimitAnonymous covers the public-link portals — the only endpoints a
	// stranger can reach, and the ones with no account to key on.
	LimitAnonymous LimitClass = "anonymous"
)

// rateLimiters holds one limiter per class.
type rateLimiters struct {
	limiters map[LimitClass]*auth.RateLimiter
	enabled  bool

	// cleanupOnce guards the background eviction goroutine, so a server built
	// twice in a test process does not start two of them.
	cleanupOnce sync.Once
}

// newRateLimiters builds the limiters a configuration asks for.
//
// A class configured to zero gets no limiter at all rather than a limiter of
// zero, which would refuse everything — a misconfiguration must not become an
// outage.
func newRateLimiters(cfg RateLimitConfig) *rateLimiters {
	r := &rateLimiters{
		limiters: make(map[LimitClass]*auth.RateLimiter, 5),
		enabled:  cfg.Enabled,
	}
	if !cfg.Enabled {
		return r
	}

	const window = time.Minute
	for class, limit := range map[LimitClass]int{
		LimitRead:      cfg.Read,
		LimitWrite:     cfg.Write,
		LimitUpload:    cfg.Upload,
		LimitSearch:    cfg.Search,
		LimitAnonymous: cfg.Anonymous,
	} {
		if limit > 0 {
			r.limiters[class] = auth.NewRateLimiter(limit, window)
		}
	}
	return r
}

// RateLimitConfig is the transport layer's view of the limits.
type RateLimitConfig struct {
	Enabled                                bool
	Read, Write, Upload, Search, Anonymous int
}

// startCleanup evicts stale buckets periodically, so the limiter's own memory
// cannot grow without bound on a long-running instance.
func (r *rateLimiters) startCleanup(stop <-chan struct{}) {
	r.cleanupOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					for _, limiter := range r.limiters {
						limiter.Cleanup()
					}
				}
			}
		}()
	})
}

// Limit returns middleware enforcing one class's budget.
//
// A class with no limiter configured returns the handler untouched, so a
// disabled limit costs nothing per request rather than a map lookup and a
// mutex.
func (s *Server) Limit(class LimitClass) func(http.Handler) http.Handler {
	limiter, ok := s.limits.limiters[class]
	if !ok {
		return func(next http.Handler) http.Handler { return next }
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := rateLimitKey(r, class)

			allowed, retryAfter := limiter.Allow(key)
			if !allowed {
				// Retry-After is a courtesy that turns a wall into an
				// instruction: a well-behaved client waits rather than
				// retrying immediately and making the problem worse.
				seconds := int(retryAfter.Seconds())
				if seconds < 1 {
					seconds = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(seconds))

				logging.FromContext(r.Context()).Warn("rate limit exceeded",
					"class", string(class), "method", r.Method, "path", r.URL.Path,
					"ip", ClientIPFromContext(r.Context()))

				WriteError(w, r, ErrRateLimited(
					"You are making requests too quickly. Wait a moment and try again."))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// rateLimitKey identifies the caller for limiting purposes.
//
// The account where there is one, the address otherwise. Per-account matters:
// a household behind one NAT address shares an IP, and limiting them
// collectively would punish the family for one person's sync client.
//
// The class is part of the key so the budgets are genuinely independent —
// exhausting the search budget must not also block a download.
func rateLimitKey(r *http.Request, class LimitClass) string {
	if user := UserFromContext(r.Context()); user != nil {
		return string(class) + ":u:" + user.ID.String()
	}
	return string(class) + ":ip:" + ClientIPFromContext(r.Context())
}
