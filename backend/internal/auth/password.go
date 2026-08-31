// Package auth implements password hashing, the sign-in flow, and account
// lockout. It knows nothing about HTTP; the api package adapts it.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Errors returned by password handling.
var (
	// ErrInvalidHash means a stored hash could not be parsed. It indicates
	// database corruption or tampering, not a wrong password.
	ErrInvalidHash = errors.New("auth: password hash is malformed")
	// ErrIncompatibleVersion means the hash was produced by a newer Argon2.
	ErrIncompatibleVersion = errors.New("auth: unsupported argon2 version")
	// ErrMismatch means the password does not match the hash.
	ErrMismatch = errors.New("auth: password does not match")
)

// HashParams are the Argon2id cost parameters.
//
// Argon2id is the deliberate choice over bcrypt and PBKDF2: it is memory-hard,
// so an attacker with a GPU or an ASIC gains far less advantage than raw
// hashing speed would suggest.
type HashParams struct {
	// Memory in KiB. This is the parameter that actually resists parallel
	// cracking hardware, so it is preferred over raising Time.
	Memory uint32
	// Time is the number of passes over memory.
	Time uint32
	// Parallelism is the number of lanes.
	Parallelism uint8
	// SaltLength in bytes.
	SaltLength uint32
	// KeyLength in bytes.
	KeyLength uint32
}

// DefaultParams are tuned for a Raspberry Pi 4.
//
// The OWASP baseline for Argon2id is m=19456 (19 MiB), t=2, p=1. We use 64 MiB
// with two passes, which is comfortably stronger and still completes in a few
// hundred milliseconds on a Pi 4 — fast enough that a person does not notice a
// login, slow enough that offline cracking is expensive.
//
// Memory is the binding constraint on this hardware: a 4 GB board running the
// database and the application cannot afford many concurrent 256 MiB hashes,
// and login is exactly where an attacker would try to force that. 64 MiB with
// the rate limiter in front of it is the right balance.
var DefaultParams = HashParams{
	Memory:      64 * 1024, // 64 MiB
	Time:        2,
	Parallelism: 2,
	SaltLength:  16,
	KeyLength:   32,
}

// Validate rejects parameters that would be unsafe or unusable.
func (p HashParams) Validate() error {
	switch {
	case p.Memory < 8*1024:
		return fmt.Errorf("auth: argon2 memory %d KiB is too low (minimum 8192)", p.Memory)
	case p.Time < 1:
		return errors.New("auth: argon2 time must be at least 1")
	case p.Parallelism < 1:
		return errors.New("auth: argon2 parallelism must be at least 1")
	case p.SaltLength < minSaltLength:
		return fmt.Errorf("auth: argon2 salt length %d is too short (minimum %d)",
			p.SaltLength, minSaltLength)
	case p.KeyLength < minKeyLength:
		return fmt.Errorf("auth: argon2 key length %d is too short (minimum %d)",
			p.KeyLength, minKeyLength)
	}
	return nil
}

// Hasher produces and verifies password hashes.
type Hasher struct {
	params HashParams
}

// NewHasher returns a Hasher using the given parameters.
func NewHasher(params HashParams) (*Hasher, error) {
	if err := params.Validate(); err != nil {
		return nil, err
	}
	if int(params.Parallelism) > runtime.NumCPU() {
		// More lanes than cores gains nothing and wastes scheduling.
		params.Parallelism = uint8(max(1, runtime.NumCPU()))
	}
	return &Hasher{params: params}, nil
}

// NewDefaultHasher returns a Hasher with the tuned defaults.
func NewDefaultHasher() *Hasher {
	h, err := NewHasher(DefaultParams)
	if err != nil {
		// DefaultParams is a compile-time constant that passes Validate; a
		// failure here is a programming error, not a runtime condition.
		panic("auth: DefaultParams is invalid: " + err.Error())
	}
	return h
}

// Params returns the parameters in use.
func (h *Hasher) Params() HashParams { return h.params }

// Hash derives a password hash in PHC string format:
//
//	$argon2id$v=19$m=65536,t=2,p=2$<base64 salt>$<base64 hash>
//
// The parameters travel with the hash, so the cost can be raised later without
// invalidating existing passwords — NeedsRehash detects the difference at the
// next successful sign-in.
func (h *Hasher) Hash(password string) (string, error) {
	if password == "" {
		return "", errors.New("auth: refusing to hash an empty password")
	}

	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		// Without secure randomness we cannot produce a safe hash, and
		// proceeding with a predictable salt would be far worse than failing.
		return "", fmt.Errorf("auth: cannot generate salt: %w", err)
	}

	key := argon2.IDKey(
		[]byte(password), salt,
		h.params.Time, h.params.Memory, h.params.Parallelism, h.params.KeyLength,
	)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		h.params.Memory, h.params.Time, h.params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verify reports whether password matches encodedHash.
//
// The comparison is constant-time. A timing difference between "wrong password"
// and "nearly right password" would let an attacker recover the hash byte by
// byte.
func Verify(password, encodedHash string) error {
	params, salt, want, err := DecodeHash(encodedHash)
	if err != nil {
		return err
	}

	got := argon2.IDKey(
		[]byte(password), salt,
		params.Time, params.Memory, params.Parallelism, params.KeyLength,
	)

	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

// NeedsRehash reports whether a stored hash was produced with weaker
// parameters than the current policy. Callers re-hash on successful sign-in,
// which is the only moment the plaintext is available.
func (h *Hasher) NeedsRehash(encodedHash string) bool {
	params, _, _, err := DecodeHash(encodedHash)
	if err != nil {
		// An unparseable hash cannot be verified against anyway; replacing it
		// at the next opportunity is the right answer.
		return true
	}
	return params.Memory < h.params.Memory ||
		params.Time < h.params.Time ||
		params.KeyLength < h.params.KeyLength ||
		params.SaltLength < h.params.SaltLength
}

// DecodeHash parses a PHC-format Argon2id hash.
func DecodeHash(encoded string) (HashParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=...,t=...,p=...", salt, hash
	if len(parts) != 6 || parts[0] != "" {
		return HashParams{}, nil, nil, ErrInvalidHash
	}
	if parts[1] != "argon2id" {
		return HashParams{}, nil, nil, fmt.Errorf("%w: algorithm %q is not argon2id", ErrInvalidHash, parts[1])
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return HashParams{}, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return HashParams{}, nil, nil, fmt.Errorf("%w: %d", ErrIncompatibleVersion, version)
	}

	var params HashParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d",
		&params.Memory, &params.Time, &params.Parallelism); err != nil {
		return HashParams{}, nil, nil, ErrInvalidHash
	}
	if params.Memory == 0 || params.Time == 0 || params.Parallelism == 0 {
		return HashParams{}, nil, nil, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return HashParams{}, nil, nil, ErrInvalidHash
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return HashParams{}, nil, nil, ErrInvalidHash
	}

	// The salt and key lengths are checked against the same floors Validate
	// applies, because they describe the *shape* of the value rather than its
	// cost. A 28-byte tag was produced by no configuration this system accepts,
	// so it is corruption — a truncated database column, a bad restore — and
	// saying so is far more useful than silently treating every password as
	// wrong forever.
	//
	// The cost parameters are deliberately not checked here. A hash stored
	// under a weaker policy is still a valid hash; needsRehash upgrades it on
	// the owner's next sign-in, and rejecting it outright would lock them out
	// of their account for an administrator's configuration change.
	if len(salt) < minSaltLength || len(key) < minKeyLength {
		return HashParams{}, nil, nil, ErrInvalidHash
	}

	params.SaltLength = uint32(len(salt))
	params.KeyLength = uint32(len(key))
	return params, salt, key, nil
}

// The structural minimums, shared by Validate and DecodeHash so the two cannot
// disagree about what a well-formed hash looks like.
const (
	minSaltLength = 16
	minKeyLength  = 32
)

// dummyHash is verified against when a sign-in names an account that does not
// exist. Without it, a missing account would return far faster than a wrong
// password, and the difference reveals which usernames are real.
//
// It is generated once at startup from a random password nobody knows.
var dummyHash = func() string {
	h := NewDefaultHasher()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic("auth: cannot initialise dummy hash: " + err.Error())
	}
	encoded, err := h.Hash(base64.RawStdEncoding.EncodeToString(secret))
	if err != nil {
		panic("auth: cannot initialise dummy hash: " + err.Error())
	}
	return encoded
}()

// BurnTime performs a hash computation whose result is discarded, so that a
// sign-in attempt against a nonexistent account costs the same as a real one.
func BurnTime(password string) {
	_ = Verify(password, dummyHash)
}
