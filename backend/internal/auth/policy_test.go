package auth

import (
	"strings"
	"testing"
)

func TestPolicyAcceptsGoodPasswords(t *testing.T) {
	t.Parallel()
	policy := NewPasswordPolicy(12)

	good := []string{
		"correct horse battery staple",
		"my-very-secure-passphrase",
		"Tr0ub4dor&3xyzzy!",
		"а очень длинный пароль здесь", // non-ASCII is fine
		"🔐🔑🗝️ keys and locks forever",
	}
	for _, p := range good {
		if err := policy.Validate(p, "josh", "Josh Moses", "josh@example.com"); err != nil {
			t.Errorf("Validate(%q) rejected a good password: %v", p, err)
		}
	}
}

func TestPolicyEnforcesLength(t *testing.T) {
	t.Parallel()
	policy := NewPasswordPolicy(12)

	if err := policy.Validate("short"); err == nil {
		t.Error("a password below the minimum must be rejected")
	}
	if err := policy.Validate(""); err == nil {
		t.Error("an empty password must be rejected")
	}
	// The upper bound exists to bound Argon2 work from a hostile client.
	if err := policy.Validate(strings.Repeat("a", DefaultMaxPasswordLength+1)); err == nil {
		t.Error("an absurdly long password must be rejected")
	}
}

// The minimum is clamped so a misconfiguration cannot drop it below a safe
// floor.
func TestPolicyClampsMinimum(t *testing.T) {
	t.Parallel()
	if got := NewPasswordPolicy(3).MinLength; got < 8 {
		t.Errorf("MinLength = %d; the floor must not be lowered below 8", got)
	}
	if got := NewPasswordPolicy(20).MinLength; got != 20 {
		t.Errorf("MinLength = %d, want 20", got)
	}
}

func TestPolicyRejectsCommonPasswords(t *testing.T) {
	t.Parallel()
	policy := NewPasswordPolicy(8)

	for _, p := range []string{
		"password", "password123", "12345678", "qwertyuiop",
		"letmein123", "administrator", "raspberrypi", "pifiles123",
		"PASSWORD", "Password123",
	} {
		if err := policy.Validate(p); err == nil {
			t.Errorf("Validate(%q) accepted a password from every breach corpus", p)
		}
	}
}

// A password containing the username is worthless however long it is: the
// username is public information.
func TestPolicyRejectsContextualPasswords(t *testing.T) {
	t.Parallel()
	policy := NewPasswordPolicy(8)

	cases := []struct{ password, why string }{
		{"joshmoses1234", "contains the username"},
		{"myJOSHpassword", "contains the username, different case"},
		{"josh.moses@example.com!", "contains the email"},
		{"Josh Moses is here", "contains the display name"},
	}
	for _, tc := range cases {
		err := policy.Validate(tc.password, "josh", "Josh Moses", "josh.moses@example.com")
		if err == nil {
			t.Errorf("Validate(%q) accepted a password that %s", tc.password, tc.why)
		}
	}

	// A short context value must not cause false positives — "a" appearing in
	// a password should not disqualify it.
	if err := policy.Validate("a perfectly fine passphrase", "a", "A", ""); err != nil {
		t.Errorf("a short context value caused a false rejection: %v", err)
	}
}

func TestPolicyRejectsLowEntropyShapes(t *testing.T) {
	t.Parallel()
	policy := NewPasswordPolicy(8)

	cases := map[string]string{
		"aaaaaaaaaaaa":     "a single repeated character",
		"111111111111":     "a single repeated digit",
		"abcdefghijkl":     "an ascending letter sequence",
		"lkjihgfedcba":     "a descending letter sequence",
		"123456789012":     "a digit sequence that wraps 9 to 0",
		"098765432109":     "a descending digit sequence that wraps",
		"abcabcabcabc":     "a three-character pattern repeated",
		"121212121212":     "a two-character pattern repeated",
		"  spaces around ": "leading and trailing whitespace",
	}
	for password, why := range cases {
		if err := policy.Validate(password); err == nil {
			t.Errorf("Validate(%q) accepted %s", password, why)
		}
	}
}

// Composition rules ("must contain a symbol") are deliberately NOT enforced:
// NIST SP 800-63B found they push people toward predictable patterns.
func TestPolicyDoesNotRequireCharacterClasses(t *testing.T) {
	t.Parallel()
	policy := NewPasswordPolicy(12)

	// All lowercase, no digits, no symbols — but long and unpredictable.
	if err := policy.Validate("the quick brown fox jumped"); err != nil {
		t.Errorf("a long all-lowercase passphrase should be accepted: %v", err)
	}
}

// The pattern checks must not produce false positives on real passphrases that
// happen to contain a short ascending run or a repeated word.
func TestPolicyPatternChecksDoNotOverreach(t *testing.T) {
	t.Parallel()
	policy := NewPasswordPolicy(12)

	fine := []string{
		"abc is where it starts",     // contains "abc" but is not a sequence
		"my password is 1234 secure", // contains a run, is not one
		"never never never say that", // repeated word, but not an exact period
		"tomato tomato tomato!",      // repetition broken by the suffix
	}
	for _, p := range fine {
		if err := policy.Validate(p); err != nil {
			t.Errorf("Validate(%q) falsely rejected a reasonable passphrase: %v", p, err)
		}
	}
}

func TestPolicyErrorsAreUserFacing(t *testing.T) {
	t.Parallel()
	policy := NewPasswordPolicy(12)

	err := policy.Validate("short")
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	// The message is shown directly to a nontechnical user, so it must read as
	// guidance rather than as a validator's internal complaint.
	if !strings.Contains(msg, "12 characters") {
		t.Errorf("message should state the requirement: %q", msg)
	}
	for _, jargon := range []string{"regex", "policy.Validate", "entropy bits", "nil"} {
		if strings.Contains(strings.ToLower(msg), jargon) {
			t.Errorf("message contains jargon %q: %q", jargon, msg)
		}
	}
}
