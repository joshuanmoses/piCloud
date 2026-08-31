package storage

import (
	"log/slog"
)

// discardLogger silences storage's startup logging in tests.
func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
