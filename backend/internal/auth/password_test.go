package auth

import (
	"errors"
	"strings"
	"testing"
)

// fastParams keep the test suite quick. Real hashing uses DefaultParams.
var fastParams = HashParams{Memory: 8 * 1024, Time: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func testHasher(t *testing.T) *Hasher {
	t.Helper()
	h, err := NewHasher(fastParams)
	if err != nil {
		t.Fatalf("NewHasher: %v", err)
	}
	return h
}

func TestHashAndVerifyRoundTrip(t *testing.T) {
	t.Parallel()
	h := testHasher(t)

	const password = "correct horse battery staple"
	encoded, err := h.Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if err := Verify(password, encoded); err != nil {
		t.Errorf("Verify rejected the correct password: %v", err)
	}
}

func TestVerifyRejectsWrongPassword(t *testing.T) {
	t.Parallel()
	h := testHasher(t)

	encoded, err := h.Hash("the right password")
	if err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []string{
		"the wrong password",
		"the right password ", // trailing space
		" the right password", // leading space
		"The Right Password",  // case
		"the right passwor",   // truncated
		"",
	} {
		if err := Verify(wrong, encoded); !errors.Is(err, ErrMismatch) {
			t.Errorf("Verify(%q) = %v, want ErrMismatch", wrong, err)
		}
	}
}

// Two users choosing the same password must not produce the same hash, or a
// breach would reveal every account sharing a common password at once.
func TestHashesAreSaltedUniquely(t *testing.T) {
	t.Parallel()
	h := testHasher(t)

	const password = "the same password for both"
	first, err := h.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.Hash(password)
	if err != nil {
		t.Fatal(err)
	}

	if first == second {
		t.Fatal("identical passwords produced identical hashes; the salt is not random")
	}
	// Both must still verify.
	if err := Verify(password, first); err != nil {
		t.Error(err)
	}
	if err := Verify(password, second); err != nil {
		t.Error(err)
	}
}

func TestHashFormatIsPHC(t *testing.T) {
	t.Parallel()
	h := testHasher(t)

	encoded, err := h.Hash("a password")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$") {
		t.Errorf("hash is not in PHC argon2id format: %q", encoded)
	}
	if parts := strings.Split(encoded, "$"); len(parts) != 6 {
		t.Errorf("expected 6 PHC segments, got %d: %q", len(parts), encoded)
	}

	// The parameters must travel with the hash, so the cost can be raised
	// later without invalidating existing passwords.
	params, salt, key, err := DecodeHash(encoded)
	if err != nil {
		t.Fatalf("DecodeHash: %v", err)
	}
	if params.Memory != fastParams.Memory || params.Time != fastParams.Time {
		t.Errorf("decoded params %+v do not match %+v", params, fastParams)
	}
	if len(salt) != int(fastParams.SaltLength) {
		t.Errorf("salt length = %d, want %d", len(salt), fastParams.SaltLength)
	}
	if len(key) != int(fastParams.KeyLength) {
		t.Errorf("key length = %d, want %d", len(key), fastParams.KeyLength)
	}
}

// A malformed hash must be reported as malformed, never treated as a match.
func TestDecodeHashRejectsMalformed(t *testing.T) {
	t.Parallel()
	valid, err := testHasher(t).Hash("x")
	if err != nil {
		t.Fatal(err)
	}

	malformed := []struct {
		name string
		hash string
	}{
		{"empty", ""},
		{"not a hash", "hunter2"},
		{"bcrypt", "$2a$10$abcdefghijklmnopqrstuv"},
		{"wrong algorithm", "$argon2i$v=19$m=8192,t=1,p=1$c2FsdA$aGFzaA"},
		{"missing segments", "$argon2id$v=19$m=8192,t=1,p=1"},
		{"bad base64 salt", "$argon2id$v=19$m=8192,t=1,p=1$!!!!$aGFzaA"},
		{"zero memory", "$argon2id$v=19$m=0,t=1,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2g"},
		{"future version", "$argon2id$v=99$m=8192,t=1,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2g"},
		// Truncation is the interesting case because the result still *looks*
		// like a hash: it parses, and its base64 often still decodes. What
		// catches it is the key length, which no configuration produces.
		{"truncated key", valid[:len(valid)-5]},
		{"truncated mid-structure", valid[:len(valid)/2]},
	}

	for _, tc := range malformed {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, err := DecodeHash(tc.hash); err == nil {
				t.Fatalf("DecodeHash(%q) succeeded; it must be rejected", tc.hash)
			}
			// Crucially, Verify must also fail rather than fall through.
			if err := Verify("anything", tc.hash); err == nil {
				t.Fatal("Verify accepted a malformed hash")
			}
		})
	}
}

func TestNeedsRehashDetectsWeakerParameters(t *testing.T) {
	t.Parallel()

	weak, err := NewHasher(HashParams{Memory: 8 * 1024, Time: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	if err != nil {
		t.Fatal(err)
	}
	strong, err := NewHasher(HashParams{Memory: 32 * 1024, Time: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	if err != nil {
		t.Fatal(err)
	}

	weakHash, err := weak.Hash("password one two three")
	if err != nil {
		t.Fatal(err)
	}

	if !strong.NeedsRehash(weakHash) {
		t.Error("a hash made with weaker parameters should be flagged for upgrade")
	}
	if weak.NeedsRehash(weakHash) {
		t.Error("a hash at current parameters should not be flagged")
	}

	strongHash, err := strong.Hash("password one two three")
	if err != nil {
		t.Fatal(err)
	}
	if weak.NeedsRehash(strongHash) {
		t.Error("a stronger hash must not be downgraded")
	}
	// An unusable hash should be replaced at the next opportunity.
	if !strong.NeedsRehash("garbage") {
		t.Error("an unparseable hash should be flagged for replacement")
	}
}

func TestHashParamsValidation(t *testing.T) {
	t.Parallel()
	if err := DefaultParams.Validate(); err != nil {
		t.Fatalf("DefaultParams must be valid: %v", err)
	}

	weak := []HashParams{
		{Memory: 1024, Time: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32},  // too little memory
		{Memory: 65536, Time: 0, Parallelism: 1, SaltLength: 16, KeyLength: 32}, // no passes
		{Memory: 65536, Time: 1, Parallelism: 0, SaltLength: 16, KeyLength: 32}, // no lanes
		{Memory: 65536, Time: 1, Parallelism: 1, SaltLength: 8, KeyLength: 32},  // short salt
		{Memory: 65536, Time: 1, Parallelism: 1, SaltLength: 16, KeyLength: 16}, // short key
	}
	for i, p := range weak {
		if err := p.Validate(); err == nil {
			t.Errorf("params[%d] %+v should be rejected", i, p)
		}
		if _, err := NewHasher(p); err == nil {
			t.Errorf("NewHasher should reject params[%d]", i)
		}
	}
}

// The default parameters must meet the OWASP Argon2id baseline.
func TestDefaultParamsMeetBaseline(t *testing.T) {
	t.Parallel()
	if DefaultParams.Memory < 19*1024 {
		t.Errorf("memory %d KiB is below the OWASP baseline of 19456", DefaultParams.Memory)
	}
	if DefaultParams.Time < 2 {
		t.Errorf("time = %d, want at least 2", DefaultParams.Time)
	}
	if DefaultParams.SaltLength < 16 {
		t.Errorf("salt length = %d, want at least 16", DefaultParams.SaltLength)
	}
	if DefaultParams.KeyLength < 32 {
		t.Errorf("key length = %d, want at least 32", DefaultParams.KeyLength)
	}
}

func TestHashRejectsEmptyPassword(t *testing.T) {
	t.Parallel()
	if _, err := testHasher(t).Hash(""); err == nil {
		t.Fatal("hashing an empty password must be refused")
	}
}

// BurnTime exists so a sign-in against a nonexistent account costs the same as
// a real verification, hiding which usernames exist.
func TestBurnTimePerformsWork(t *testing.T) {
	t.Parallel()
	// It must not panic and must not reveal anything. The timing property
	// itself is covered by the service-level test.
	BurnTime("any password at all")
	BurnTime("")
}

func BenchmarkHashDefaultParams(b *testing.B) {
	h := NewDefaultHasher()
	b.ResetTimer()
	for range b.N {
		if _, err := h.Hash("a reasonably long benchmark password"); err != nil {
			b.Fatal(err)
		}
	}
}
