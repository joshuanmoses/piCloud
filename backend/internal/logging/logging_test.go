package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func capture(t *testing.T, format string, fn func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	fn(New(Options{Level: "debug", Format: format, Output: &buf}))
	return buf.String()
}

// TestSecretsAreNeverLogged is a security regression test. Passwords, session
// tokens, and link tokens reaching a log file would survive rotation, backups,
// and log shipping.
func TestSecretsAreNeverLogged(t *testing.T) {
	t.Parallel()
	const secret = "hunter2-do-not-log-this"

	sensitive := []string{
		"password", "Password", "PASSWORD", "passwd", "new_password",
		"token", "session_token", "refresh_token", "csrf_token", "share_token",
		"secret", "api_key", "secret_key", "authorization", "cookie",
		"user_password", "link_token", "recovery_token", "dsn",
	}

	for _, key := range sensitive {
		for _, format := range []string{"json", "console"} {
			out := capture(t, format, func(l *slog.Logger) {
				l.Info("login attempt", key, secret)
			})
			if strings.Contains(out, secret) {
				t.Errorf("%s handler leaked %q: %s", format, key, out)
			}
			if !strings.Contains(out, "REDACTED") {
				t.Errorf("%s handler did not mark %q as redacted: %s", format, key, out)
			}
		}
	}
}

func TestSecretsRedactedInGroupsAndWithAttrs(t *testing.T) {
	t.Parallel()
	const secret = "top-secret-value"

	out := capture(t, "json", func(l *slog.Logger) {
		l.With("password", secret).Info("with-attrs")
	})
	if strings.Contains(out, secret) {
		t.Errorf("With() bypassed redaction: %s", out)
	}

	out = capture(t, "json", func(l *slog.Logger) {
		l.Info("grouped", slog.Group("auth", slog.String("username", "josh"), slog.String("token", secret)))
	})
	if strings.Contains(out, secret) {
		t.Errorf("grouped attributes bypassed redaction: %s", out)
	}
	if !strings.Contains(out, "josh") {
		t.Errorf("non-sensitive grouped attributes should survive: %s", out)
	}
}

func TestNonSensitiveFieldsAreKept(t *testing.T) {
	t.Parallel()
	out := capture(t, "json", func(l *slog.Logger) {
		l.Info("upload finished",
			"filename", "Report.pdf",
			"size_bytes", 2411724,
			"content_hash", "abcdef0123",
			"checksum_hash", "abcdef0123",
			"username", "josh")
	})
	for _, want := range []string{"Report.pdf", "2411724", "josh", "abcdef0123"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output: %s", want, out)
		}
	}
}

func TestIsSensitiveKey(t *testing.T) {
	t.Parallel()
	sensitive := []string{"password", "TOKEN", "api_key", "user_password", "reset_token", "private_key", "password_hash"}
	for _, k := range sensitive {
		if !IsSensitiveKey(k) {
			t.Errorf("%q should be treated as sensitive", k)
		}
	}
	// Content hashes are not secrets, and redacting them would make audit and
	// reconciliation logs useless.
	safe := []string{"username", "filename", "size", "content_hash", "checksum_hash", "request_id", "path"}
	for _, k := range safe {
		if IsSensitiveKey(k) {
			t.Errorf("%q should not be redacted", k)
		}
	}
}

func TestJSONOutputIsValidJSON(t *testing.T) {
	t.Parallel()
	out := capture(t, "json", func(l *slog.Logger) {
		l.Info("request", "method", "GET", "path", "/api/v1/files", "status", 200)
	})

	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed); err != nil {
		t.Fatalf("JSON handler produced invalid JSON (%s): %v", out, err)
	}
	if parsed["msg"] != "request" {
		t.Errorf("msg = %v", parsed["msg"])
	}
	if parsed["method"] != "GET" {
		t.Errorf("method = %v", parsed["method"])
	}
	if _, ok := parsed["time"]; !ok {
		t.Error("log lines must carry a timestamp")
	}
}

func TestConsoleOutputIsReadable(t *testing.T) {
	t.Parallel()
	out := capture(t, "console", func(l *slog.Logger) {
		l.Info("server listening", "addr", "0.0.0.0:8080")
	})
	if !strings.Contains(out, "server listening") {
		t.Errorf("message missing: %s", out)
	}
	if !strings.Contains(out, "addr=0.0.0.0:8080") {
		t.Errorf("attribute missing or misformatted: %s", out)
	}
	if !strings.Contains(out, "INFO") {
		t.Errorf("level missing: %s", out)
	}
}

func TestConsoleQuotesValuesContainingSpaces(t *testing.T) {
	t.Parallel()
	out := capture(t, "console", func(l *slog.Logger) {
		l.Info("event", "detail", "two words")
	})
	if !strings.Contains(out, `detail="two words"`) {
		t.Errorf("values with spaces must be quoted so the line stays parseable: %s", out)
	}
}

func TestLevelFiltering(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	l := New(Options{Level: "warning", Format: "console", Output: &buf})

	l.Debug("debug message")
	l.Info("info message")
	l.Warn("warn message")
	l.Error("error message")

	out := buf.String()
	if strings.Contains(out, "debug message") || strings.Contains(out, "info message") {
		t.Errorf("messages below the configured level were emitted: %s", out)
	}
	if !strings.Contains(out, "warn message") || !strings.Contains(out, "error message") {
		t.Errorf("messages at or above the level were dropped: %s", out)
	}
}

func TestParseLevel(t *testing.T) {
	t.Parallel()
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug, "info": slog.LevelInfo,
		"warn": slog.LevelWarn, "warning": slog.LevelWarn, "WARNING": slog.LevelWarn,
		"error": slog.LevelError, "nonsense": slog.LevelInfo, "": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestWarningLevelIsSpelledOut(t *testing.T) {
	t.Parallel()
	out := capture(t, "json", func(l *slog.Logger) { l.Warn("careful") })
	if !strings.Contains(out, "WARNING") {
		t.Errorf("level should render as WARNING to match the LOG_LEVEL vocabulary: %s", out)
	}
}

func TestContextLoggerRoundTrip(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := New(Options{Level: "debug", Format: "console", Output: &buf}).With("request_id", "abc123")

	ctx := WithLogger(t.Context(), logger)
	FromContext(ctx).Info("handled")

	if !strings.Contains(buf.String(), "request_id=abc123") {
		t.Errorf("the request-scoped logger was not propagated: %s", buf.String())
	}
}

func TestFromContextNeverReturnsNil(t *testing.T) {
	t.Parallel()
	if FromContext(t.Context()) == nil {
		t.Error("FromContext must fall back to the default logger")
	}
	//nolint:staticcheck // deliberately passing nil to prove it is handled
	if FromContext(nil) == nil {
		t.Error("FromContext(nil) must not panic or return nil")
	}
}
