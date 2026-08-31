package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseSize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"512", 512},
		{"1B", 1},
		{"16MB", 16 * MB},
		{"16 MB", 16 * MB},
		{"16mb", 16 * MB},
		{"16MiB", 16 * MB},
		{"20GB", 20 * GB},
		{"  20GB  ", 20 * GB},
		{"1.5GB", 1536 * MB},
		{"2T", 2 * TB},
		{"100K", 100 * KB},
		// Exceeds float64's exact-integer range; the integer path must keep it exact.
		{"9007199254740993", 9007199254740993},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := ParseSize(tc.in)
			if err != nil {
				t.Fatalf("ParseSize(%q) returned error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseSize(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseSizeRejectsInvalid(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "   ", "GB", "-5GB", "-1", "twenty", "12XB", "1.2.3MB", "0x10"} {
		if got, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) = %d, want an error", in, got)
		}
	}
}

func TestParseSizeRejectsOverflow(t *testing.T) {
	t.Parallel()
	if _, err := ParseSize("99999999999TB"); err == nil {
		t.Fatal("expected overflow to be rejected rather than silently wrapping")
	}
}

func TestFormatSizeRoundTrips(t *testing.T) {
	t.Parallel()
	cases := map[int64]string{
		0:         "0 B",
		512:       "512 B",
		1024:      "1 KB",
		16 * MB:   "16 MB",
		20 * GB:   "20 GB",
		1536 * MB: "1.5 GB",
		2 * TB:    "2 TB",
	}
	for in, want := range cases {
		if got := FormatSize(in); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestParseDuration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"30s", 30 * time.Second},
		{"15m", 15 * time.Minute},
		{"24h", 24 * time.Hour},
		{"1h30m", 90 * time.Minute},
		{"30d", 30 * 24 * time.Hour},
		{"1d", 24 * time.Hour},
		{"0.5d", 12 * time.Hour},
		{"  24H  ", 24 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := ParseDuration(tc.in)
			if err != nil {
				t.Fatalf("ParseDuration(%q) returned error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseDuration(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseDurationRejectsInvalid(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "soon", "-5m", "5x", "d"} {
		if got, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) = %v, want an error", in, got)
		}
	}
}

func TestParseBool(t *testing.T) {
	t.Parallel()
	truthy := []string{"1", "t", "true", "TRUE", "yes", "Y", "on", "enabled"}
	falsy := []string{"0", "f", "false", "FALSE", "no", "N", "off", "disabled"}

	for _, in := range truthy {
		got, err := ParseBool(in)
		if err != nil || !got {
			t.Errorf("ParseBool(%q) = %v, %v; want true, nil", in, got, err)
		}
	}
	for _, in := range falsy {
		got, err := ParseBool(in)
		if err != nil || got {
			t.Errorf("ParseBool(%q) = %v, %v; want false, nil", in, got, err)
		}
	}
	for _, in := range []string{"", "maybe", "2"} {
		if _, err := ParseBool(in); err == nil {
			t.Errorf("ParseBool(%q) should have failed", in)
		}
	}
}

func TestSecretIsNeverRendered(t *testing.T) {
	t.Parallel()
	const value = "super-secret-password-value"
	s := Secret(value)

	// Every path by which a value could reach a log or a response must hide it.
	rendered := []string{
		s.String(),
		s.GoString(),
		strings.TrimSpace(mustJSON(t, s)),
		s.LogValue().String(),
	}
	for i, out := range rendered {
		if strings.Contains(out, value) {
			t.Errorf("rendering %d leaked the secret: %q", i, out)
		}
	}
	if s.Reveal() != value {
		t.Error("Reveal must return the real value")
	}
	if !Secret("").IsZero() {
		t.Error("empty secret should report IsZero")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := v.(Secret).MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	return string(b)
}
