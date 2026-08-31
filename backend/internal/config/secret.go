package config

import (
	"encoding/json"
	"log/slog"
)

// Secret wraps a sensitive configuration value so that it cannot be leaked by
// accident through %v formatting, JSON encoding, or structured logging. The
// real value is only reachable through Reveal, which makes every legitimate
// use of a secret grep-able.
type Secret string

const redacted = "[REDACTED]"

// Reveal returns the underlying value. Call this only at the point of use.
func (s Secret) Reveal() string { return string(s) }

// IsZero reports whether the secret is unset.
func (s Secret) IsZero() bool { return s == "" }

// String implements fmt.Stringer and deliberately hides the value.
func (s Secret) String() string { return redacted }

// GoString hides the value from %#v as well.
func (s Secret) GoString() string { return redacted }

// MarshalJSON prevents a secret from being serialized into an API response or
// a JSON log line.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// LogValue implements slog.LogValuer so slog never renders the value.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

var (
	_ slog.LogValuer = Secret("")
	_ json.Marshaler = Secret("")
)
