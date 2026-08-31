package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Byte-size multipliers. PiFiles uses binary multiples (1 KB == 1024 bytes)
// because that is what filesystem tools on Linux report.
const (
	Byte int64 = 1
	KB         = 1024 * Byte
	MB         = 1024 * KB
	GB         = 1024 * MB
	TB         = 1024 * GB
)

var sizeUnits = []struct {
	suffix string
	mult   int64
}{
	// Longest suffixes first so "KB" is not matched as "B".
	{"TIB", TB}, {"GIB", GB}, {"MIB", MB}, {"KIB", KB},
	{"TB", TB}, {"GB", GB}, {"MB", MB}, {"KB", KB},
	{"T", TB}, {"G", GB}, {"M", MB}, {"K", KB},
	{"B", Byte},
}

// ParseSize converts a human-readable byte size such as "20GB", "16 MiB", or
// "1048576" into a byte count. Parsing is case-insensitive and tolerates
// internal whitespace. Fractional values such as "1.5GB" are supported.
func ParseSize(raw string) (int64, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return 0, fmt.Errorf("empty size value")
	}

	mult := Byte
	for _, u := range sizeUnits {
		if strings.HasSuffix(s, u.suffix) {
			mult = u.mult
			s = strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			break
		}
	}
	if s == "" {
		return 0, fmt.Errorf("size %q has a unit but no number", raw)
	}

	// Integer path first: avoids float rounding for the common case, and keeps
	// values above 2^53 exact.
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("size %q must not be negative", raw)
		}
		if mult != Byte && n > (1<<62)/mult {
			return 0, fmt.Errorf("size %q overflows int64", raw)
		}
		return n * mult, nil
	}

	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("size %q is not a valid number", raw)
	}
	if f < 0 {
		return 0, fmt.Errorf("size %q must not be negative", raw)
	}
	scaled := f * float64(mult)
	if scaled > float64(1<<62) {
		return 0, fmt.Errorf("size %q overflows int64", raw)
	}
	return int64(scaled), nil
}

// FormatSize renders a byte count using binary units, for logs and admin UI.
func FormatSize(n int64) string {
	switch {
	case n >= TB:
		return trimZeros(float64(n)/float64(TB)) + " TB"
	case n >= GB:
		return trimZeros(float64(n)/float64(GB)) + " GB"
	case n >= MB:
		return trimZeros(float64(n)/float64(MB)) + " MB"
	case n >= KB:
		return trimZeros(float64(n)/float64(KB)) + " KB"
	default:
		return strconv.FormatInt(n, 10) + " B"
	}
}

func trimZeros(f float64) string {
	s := strconv.FormatFloat(f, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// ParseDuration accepts Go duration syntax ("24h", "30m", "1h30m") and
// additionally supports a "d" suffix for whole days ("30d"), which Go's parser
// rejects but which reads far more naturally in configuration files.
func ParseDuration(raw string) (time.Duration, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return 0, fmt.Errorf("empty duration value")
	}
	if rest, ok := strings.CutSuffix(s, "d"); ok {
		// Only treat "d" as days when the remainder is purely numeric;
		// otherwise fall through so Go durations ending in "d" still error
		// clearly rather than being silently mangled.
		if days, err := strconv.ParseFloat(rest, 64); err == nil {
			if days < 0 {
				return 0, fmt.Errorf("duration %q must not be negative", raw)
			}
			return time.Duration(days * float64(24*time.Hour)), nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("duration %q is not valid (expected forms: 30s, 15m, 24h, 30d)", raw)
	}
	if d < 0 {
		return 0, fmt.Errorf("duration %q must not be negative", raw)
	}
	return d, nil
}

// ParseBool accepts the spellings people actually write in .env files.
func ParseBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "t", "true", "y", "yes", "on", "enabled":
		return true, nil
	case "0", "f", "false", "n", "no", "off", "disabled":
		return false, nil
	default:
		return false, fmt.Errorf("%q is not a valid boolean (use true or false)", raw)
	}
}
