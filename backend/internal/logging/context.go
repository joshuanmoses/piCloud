package logging

import (
	"context"
	"log/slog"
)

type ctxKey struct{}

// WithLogger returns a context carrying logger. Middleware attaches a
// request-scoped logger (already tagged with the request ID) so that handlers
// deep in the call stack can log without threading a logger parameter.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	if logger == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, logger)
}

// FromContext returns the request-scoped logger, or the default logger when
// none is attached. It never returns nil.
func FromContext(ctx context.Context) *slog.Logger {
	if ctx != nil {
		if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
			return l
		}
	}
	return slog.Default()
}
