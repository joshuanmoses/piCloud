// Package logging configures structured application logging.
//
// Two output formats are supported: a compact human-readable console format for
// development, and JSON for production log aggregation. Both run through a
// redaction handler that removes values whose attribute keys look sensitive,
// as a defence-in-depth backstop behind config.Secret.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Options controls logger construction.
type Options struct {
	// Level is one of debug, info, warning, error. Unknown values fall back to info.
	Level string
	// Format is "console" or "json".
	Format string
	// Output defaults to os.Stdout.
	Output io.Writer
	// AddSource includes file:line. Useful for debugging, costly in hot paths.
	AddSource bool
}

// New builds a logger from Options.
func New(opts Options) *slog.Logger {
	out := opts.Output
	if out == nil {
		out = os.Stdout
	}

	handlerOpts := &slog.HandlerOptions{
		Level:       ParseLevel(opts.Level),
		AddSource:   opts.AddSource,
		ReplaceAttr: replaceAttr,
	}

	var h slog.Handler
	if strings.EqualFold(opts.Format, "console") {
		h = newConsoleHandler(out, handlerOpts)
	} else {
		h = slog.NewJSONHandler(out, handlerOpts)
	}

	return slog.New(&redactingHandler{inner: h})
}

// ParseLevel maps a configuration string to an slog level.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// replaceAttr normalizes level names and timestamp format.
func replaceAttr(_ []string, a slog.Attr) slog.Attr {
	switch a.Key {
	case slog.LevelKey:
		if lvl, ok := a.Value.Any().(slog.Level); ok && lvl == slog.LevelWarn {
			// "warning" reads better than slog's default "WARN" and matches
			// the documented LOG_LEVEL vocabulary.
			return slog.String(slog.LevelKey, "WARNING")
		}
	case slog.TimeKey:
		if t, ok := a.Value.Any().(time.Time); ok {
			return slog.String(slog.TimeKey, t.UTC().Format(time.RFC3339Nano))
		}
	}
	return a
}

// sensitiveKeys are attribute names whose values must never be written to a
// log, at any level, in any environment.
var sensitiveKeys = map[string]struct{}{
	"password": {}, "passwd": {}, "pass": {}, "password_hash": {}, "new_password": {},
	"old_password": {}, "current_password": {}, "confirm_password": {},
	"token": {}, "access_token": {}, "refresh_token": {}, "session_token": {},
	"recovery_token": {}, "reset_token": {}, "csrf_token": {}, "share_token": {},
	"secret": {}, "secret_key": {}, "api_key": {}, "apikey": {}, "api_secret": {},
	"authorization": {}, "cookie": {}, "set-cookie": {}, "private_key": {},
	"credentials": {}, "dsn": {}, "database_url": {}, "connection_string": {},
}

// IsSensitiveKey reports whether an attribute key must be redacted.
func IsSensitiveKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if _, ok := sensitiveKeys[k]; ok {
		return true
	}
	// Catch composed names such as "user_password" or "link_token_hash".
	for _, suffix := range []string{"_password", "_token", "_secret", "_key", "_hash"} {
		if strings.HasSuffix(k, suffix) && k != "checksum_hash" && k != "content_hash" {
			return true
		}
	}
	return false
}

const redactedValue = "[REDACTED]"

// redactingHandler strips sensitive attribute values before delegating.
type redactingHandler struct{ inner slog.Handler }

func (h *redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	clone := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clone.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, clone)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = redactAttr(a)
	}
	return &redactingHandler{inner: h.inner.WithAttrs(out)}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	if IsSensitiveKey(a.Key) {
		return slog.String(a.Key, redactedValue)
	}
	if a.Value.Kind() == slog.KindGroup {
		sub := a.Value.Group()
		out := make([]any, 0, len(sub))
		for _, s := range sub {
			out = append(out, redactAttr(s))
		}
		return slog.Group(a.Key, out...)
	}
	return a
}

// consoleHandler renders a compact, aligned, human-readable line:
//
//	15:04:05.000 INFO  server listening  addr=0.0.0.0:8080 pid=1
type consoleHandler struct {
	inner slog.Handler
	out   io.Writer
	opts  *slog.HandlerOptions
	attrs []slog.Attr
	group string
}

func newConsoleHandler(out io.Writer, opts *slog.HandlerOptions) slog.Handler {
	return &consoleHandler{out: out, opts: opts}
}

func (h *consoleHandler) Enabled(_ context.Context, l slog.Level) bool {
	min := slog.LevelInfo
	if h.opts != nil && h.opts.Level != nil {
		min = h.opts.Level.Level()
	}
	return l >= min
}

func (h *consoleHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format("15:04:05.000"))
	b.WriteByte(' ')
	b.WriteString(fmt.Sprintf("%-7s", levelName(r.Level)))
	b.WriteString(r.Message)

	writeAttr := func(a slog.Attr) {
		if a.Equal(slog.Attr{}) {
			return
		}
		b.WriteByte(' ')
		if h.group != "" {
			b.WriteString(h.group)
			b.WriteByte('.')
		}
		b.WriteString(a.Key)
		b.WriteByte('=')
		b.WriteString(quoteIfNeeded(a.Value.Resolve().String()))
	}
	for _, a := range h.attrs {
		writeAttr(a)
	}
	r.Attrs(func(a slog.Attr) bool { writeAttr(a); return true })

	b.WriteByte('\n')
	_, err := io.WriteString(h.out, b.String())
	return err
}

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &clone
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	clone := *h
	if h.group == "" {
		clone.group = name
	} else {
		clone.group = h.group + "." + name
	}
	return &clone
}

func levelName(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "DEBUG"
	case l < slog.LevelWarn:
		return "INFO"
	case l < slog.LevelError:
		return "WARNING"
	default:
		return "ERROR"
	}
}

func quoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\"\n") {
		return fmt.Sprintf("%q", s)
	}
	return s
}

// Discard returns a logger that writes nothing. Useful in tests.
func Discard() *slog.Logger { return slog.New(slog.DiscardHandler) }
