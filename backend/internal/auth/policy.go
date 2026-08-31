package auth

import (
	"fmt"
	"strings"
	"unicode"
)

// PasswordPolicy describes what makes an acceptable password.
//
// The rules follow NIST SP 800-63B: length is what matters, and forced
// composition rules ("must contain a symbol") measurably push people toward
// predictable patterns like "Password1!". So length is enforced, common and
// contextual passwords are blocked, and character-class quotas are not
// required.
type PasswordPolicy struct {
	MinLength int
	// MaxLength exists only to bound the work Argon2 is asked to do. It is set
	// far above any real password.
	MaxLength int
}

// DefaultMaxPasswordLength bounds hashing work from a hostile client.
const DefaultMaxPasswordLength = 256

// NewPasswordPolicy builds a policy, clamping the minimum to a safe floor.
func NewPasswordPolicy(minLength int) PasswordPolicy {
	if minLength < 8 {
		minLength = 8
	}
	return PasswordPolicy{MinLength: minLength, MaxLength: DefaultMaxPasswordLength}
}

// PasswordError describes why a password was rejected. The message is written
// to be shown directly to the user.
type PasswordError struct{ Reason string }

func (e *PasswordError) Error() string { return e.Reason }

// Validate checks a password against the policy.
//
// context should contain values the password must not resemble — the username,
// display name, and email. A password equal to the username passes every length
// rule and is still worthless.
func (p PasswordPolicy) Validate(password string, context ...string) error {
	// Count runes, not bytes: a passphrase of eight emoji is eight characters
	// to the person typing it, however many bytes it occupies.
	length := len([]rune(password))

	switch {
	case length == 0:
		return &PasswordError{"Enter a password."}
	case length < p.MinLength:
		return &PasswordError{fmt.Sprintf(
			"Password must be at least %d characters. A memorable phrase of several words is both stronger and easier to type than a short complex password.",
			p.MinLength)}
	case length > p.MaxLength:
		return &PasswordError{fmt.Sprintf("Password must be no more than %d characters.", p.MaxLength)}
	}

	// Leading or trailing whitespace is nearly always an accident of copying
	// and paste, and produces a password the user cannot reliably retype.
	if strings.TrimSpace(password) != password {
		return &PasswordError{"Password must not begin or end with a space."}
	}

	// A password consisting of one repeated character satisfies any length
	// rule and has almost no entropy.
	if isSingleRepeatedRune(password) {
		return &PasswordError{"Password must not be a single repeated character."}
	}

	lowered := strings.ToLower(password)

	if isCommonPassword(lowered) {
		return &PasswordError{"That password is among the most commonly used and would be guessed quickly. Choose something else."}
	}

	// Contextual matches: the username or email is public information.
	for _, value := range context {
		value = strings.ToLower(strings.TrimSpace(value))
		if len(value) < 4 {
			continue
		}
		if strings.Contains(lowered, value) {
			return &PasswordError{"Password must not contain your username, name, or email address."}
		}
	}

	if isSequential(lowered) {
		return &PasswordError{"Password must not be a simple sequence such as 12345678 or abcdefgh."}
	}

	if isShortPatternRepeated(lowered) {
		return &PasswordError{"Password must not be a short pattern repeated over and over."}
	}

	return nil
}

func isSingleRepeatedRune(s string) bool {
	runes := []rune(s)
	if len(runes) < 2 {
		return true
	}
	first := runes[0]
	for _, r := range runes[1:] {
		if r != first {
			return false
		}
	}
	return true
}

// isSequential detects a password that is entirely one ascending or descending
// run of adjacent characters.
//
// Digit sequences are compared modulo 10, so "123456789012" — which wraps from
// 9 back to 0 — is recognised as the trivial sequence it is. Without the
// wrap-around, that password passes a naive monotonic check while being one of
// the first an attacker would try.
func isSequential(s string) bool {
	runes := []rune(s)
	if len(runes) < 4 {
		return false
	}

	allDigits := true
	for _, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
		if !unicode.IsDigit(r) {
			allDigits = false
		}
	}

	step := func(prev, cur rune) rune {
		if allDigits {
			// Wrap 9 → 0 and 0 → 9.
			return ((cur-prev)%10 + 10) % 10
		}
		return cur - prev
	}

	ascending, descending := true, true
	for i := 1; i < len(runes); i++ {
		diff := step(runes[i-1], runes[i])
		if diff != 1 {
			ascending = false
		}
		descendingStep := diff == -1 || (allDigits && diff == 9)
		if !descendingStep {
			descending = false
		}
		if !ascending && !descending {
			return false
		}
	}
	return true
}

// isShortPatternRepeated detects a password built by repeating a short unit,
// such as "abcabcabcabc" or "1212121212". Such a password has the entropy of
// its unit, not of its length, so the length rule alone would be misleading.
func isShortPatternRepeated(s string) bool {
	runes := []rune(s)
	n := len(runes)
	if n < 6 {
		return false
	}

	// Only consider a unit short enough that the repetition is the dominant
	// structure: it must repeat at least three times.
	for period := 1; period <= n/3; period++ {
		if n%period != 0 {
			continue
		}
		repeated := true
		for i := period; i < n; i++ {
			if runes[i] != runes[i-period] {
				repeated = false
				break
			}
		}
		if repeated {
			return true
		}
	}
	return false
}

// commonPasswords holds the passwords that appear at the top of every breach
// corpus, plus the ones people reach for when a product is called PiFiles.
//
// This is a deliberately small list checked exactly, not a substitute for a
// full breach-corpus check. It catches the genuinely worst choices at no cost
// in memory — which matters on a 4 GB board, where loading a multi-megabyte
// wordlist for every password change would not be a reasonable trade.
var commonPasswords = map[string]struct{}{}

func init() {
	for _, p := range []string{
		"password", "password1", "password123", "passw0rd", "p@ssw0rd", "p@ssword",
		"12345678", "123456789", "1234567890", "1234567890", "qwertyuiop", "qwerty123",
		"letmein", "letmein123", "welcome", "welcome1", "welcome123",
		"admin", "administrator", "admin123", "adminadmin", "root", "toor", "rootroot",
		"changeme", "changeme123", "default", "secret", "secret123",
		"iloveyou", "sunshine", "princess", "football", "baseball", "monkey",
		"dragon", "master", "superman", "trustno1", "starwars", "whatever",
		"raspberry", "raspberrypi", "pifiles", "pifiles123", "nextcloud", "owncloud",
		"abcd1234", "abc12345", "a1b2c3d4", "qazwsxedc", "1qaz2wsx", "zaq12wsx",
		"11111111", "00000000", "aaaaaaaa", "asdfghjkl", "qwertyui",
	} {
		commonPasswords[p] = struct{}{}
	}
}

func isCommonPassword(lowered string) bool {
	_, found := commonPasswords[lowered]
	return found
}
