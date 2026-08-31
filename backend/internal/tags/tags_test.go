package tags

import (
	"errors"
	"strings"
	"testing"
)

func TestATagNameIsTrimmedAndItsWhitespaceCollapsed(t *testing.T) {
	cases := map[string]string{
		"  Urgent  ":     "Urgent",
		"very  urgent":   "very urgent",
		"tab\tseparated": "tab separated",
		"Q4 Report":      "Q4 Report",
	}
	for input, want := range cases {
		got, err := NormalizeName(input)
		if err != nil {
			t.Errorf("NormalizeName(%q): %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAnEmptyOrWhitespaceOnlyNameIsRejected(t *testing.T) {
	for _, input := range []string{"", "   ", "\t\n"} {
		if _, err := NormalizeName(input); !errors.Is(err, ErrInvalidName) {
			t.Errorf("NormalizeName(%q) error = %v, want ErrInvalidName", input, err)
		}
	}
}

func TestAnOverlongNameIsRejectedByRuneCountNotByteCount(t *testing.T) {
	// 40 multi-byte runes is 120 bytes but exactly the limit in characters,
	// which is the limit a person is thinking in.
	atLimit := strings.Repeat("日", MaxNameLength)
	if _, err := NormalizeName(atLimit); err != nil {
		t.Errorf("a name of exactly %d characters was rejected: %v", MaxNameLength, err)
	}

	tooLong := strings.Repeat("日", MaxNameLength+1)
	if _, err := NormalizeName(tooLong); !errors.Is(err, ErrInvalidName) {
		t.Errorf("a name of %d characters was accepted", MaxNameLength+1)
	}
}

func TestControlCharactersAreRejectedInATagName(t *testing.T) {
	for _, input := range []string{"urgent\x00", "urgent\x07", "line\x1bbreak"} {
		if _, err := NormalizeName(input); !errors.Is(err, ErrInvalidName) {
			t.Errorf("NormalizeName(%q) accepted a control character", input)
		}
	}
}

func TestAColourMustBeSixHexDigitsOrNothingAtAll(t *testing.T) {
	valid := map[string]string{
		"#4A90D9": "#4a90d9",
		"#ffffff": "#ffffff",
		"  ":      "",
		"":        "",
	}
	for input, want := range valid {
		got, err := NormalizeColor(input)
		if err != nil {
			t.Errorf("NormalizeColor(%q): %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeColor(%q) = %q, want %q", input, got, want)
		}
	}

	// Anything that is not a plain hex colour is refused rather than sanitised:
	// this value reaches a CSS context in the browser.
	invalid := []string{"red", "#fff", "#12345g", "javascript:alert(1)", "#4a90d9; background:url(x)", "rgb(0,0,0)"}
	for _, input := range invalid {
		if _, err := NormalizeColor(input); !errors.Is(err, ErrInvalidColor) {
			t.Errorf("NormalizeColor(%q) was accepted", input)
		}
	}
}
