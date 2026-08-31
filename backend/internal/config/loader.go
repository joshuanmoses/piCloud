package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Lookup resolves an environment variable. It exists so tests can supply a
// deterministic environment without mutating the process.
type Lookup func(key string) (string, bool)

// OSLookup reads from the real process environment.
func OSLookup(key string) (string, bool) { return os.LookupEnv(key) }

// MapLookup reads from an in-memory map.
func MapLookup(m map[string]string) Lookup {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

// loader collects every configuration problem instead of stopping at the first
// one. A user fixing their .env file should see all of their mistakes at once.
type loader struct {
	lookup Lookup
	errs   []error
}

func (l *loader) fail(key string, err error) {
	l.errs = append(l.errs, fmt.Errorf("%s: %w", key, err))
}

// raw returns the trimmed value of key, and whether it was set to a non-empty
// value. An empty environment variable is treated as unset so that a stray
// `KEY=` line in a .env file falls back to the documented default rather than
// producing a confusing validation error.
func (l *loader) raw(key string) (string, bool) {
	v, ok := l.lookup(key)
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	return v, true
}

func (l *loader) str(key, def string) string {
	if v, ok := l.raw(key); ok {
		return v
	}
	return def
}

func (l *loader) secret(key string) Secret {
	v, _ := l.raw(key)
	return Secret(v)
}

func (l *loader) boolean(key string, def bool) bool {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	b, err := ParseBool(v)
	if err != nil {
		l.fail(key, err)
		return def
	}
	return b
}

func (l *loader) integer(key string, def int) int {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.fail(key, fmt.Errorf("%q is not a valid integer", v))
		return def
	}
	return n
}

func (l *loader) size(key string, def int64) int64 {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	n, err := ParseSize(v)
	if err != nil {
		l.fail(key, err)
		return def
	}
	return n
}

func (l *loader) duration(key string, def time.Duration) time.Duration {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	d, err := ParseDuration(v)
	if err != nil {
		l.fail(key, err)
		return def
	}
	return d
}

// oneOf validates that the value is a member of allowed, comparing lowercase.
func (l *loader) oneOf(key, def string, allowed ...string) string {
	v := strings.ToLower(l.str(key, def))
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	sorted := append([]string(nil), allowed...)
	sort.Strings(sorted)
	l.fail(key, fmt.Errorf("%q is not valid (expected one of: %s)", v, strings.Join(sorted, ", ")))
	return def
}

// csv splits a comma-separated list, discarding empty entries.
func (l *loader) csv(key string, def []string) []string {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// require registers a validation failure when cond is false.
func (l *loader) require(cond bool, key string, msg string) {
	if !cond {
		l.errs = append(l.errs, fmt.Errorf("%s: %s", key, msg))
	}
}

func (l *loader) err() error {
	if len(l.errs) == 0 {
		return nil
	}
	return &Error{Problems: l.errs}
}

// Error aggregates every configuration problem found during loading.
type Error struct {
	Problems []error
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "invalid configuration (%d problem", len(e.Problems))
	if len(e.Problems) != 1 {
		b.WriteByte('s')
	}
	b.WriteString("):")
	for _, p := range e.Problems {
		b.WriteString("\n  - ")
		b.WriteString(p.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() []error { return e.Problems }

// IsInvalid reports whether err is a configuration validation failure.
func IsInvalid(err error) bool {
	var ce *Error
	return errors.As(err, &ce)
}
