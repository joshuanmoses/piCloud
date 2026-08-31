package users

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestValidateUsernameAcceptsReasonableNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"josh", "j.moses", "user_1", "a1", "Josh-Moses", "x.y_z-1"} {
		if err := ValidateUsername(name); err != nil {
			t.Errorf("ValidateUsername(%q) rejected a reasonable name: %v", name, err)
		}
	}
}

// The permitted character set is deliberately narrow. A username appears in
// audit logs and share dialogs, so allowing arbitrary Unicode would invite
// homoglyph impersonation.
func TestValidateUsernameRejectsDangerousNames(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                      "empty",
		"a":                     "too short",
		strings.Repeat("a", 65): "too long",
		"_leading":              "does not start with alphanumeric",
		".hidden":               "does not start with alphanumeric",
		"-dash":                 "does not start with alphanumeric",
		"has space":             "contains a space",
		"has/slash":             "contains a path separator",
		"has\\backslash":        "contains a backslash",
		"has@at":                "contains an at sign",
		"has\x00null":           "contains a NUL byte",
		"has\nnewline":          "contains a newline",
		"аdmin":                 "Cyrillic homoglyph of admin",
		"josh©":                 "non-ASCII symbol",
		"用户名":                   "non-ASCII",
		"<script>":              "HTML metacharacters",
	}
	for name, why := range cases {
		if err := ValidateUsername(name); err == nil {
			t.Errorf("ValidateUsername(%q) accepted a name that is %s", name, why)
		}
	}
}

func TestValidateUsernameRejectsReservedNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"admin", "Admin", "ADMINISTRATOR", "root", "system", "api", "public", "me", "everyone"} {
		if err := ValidateUsername(name); err == nil {
			t.Errorf("ValidateUsername(%q) accepted a reserved name", name)
		}
	}
}

func TestValidateDisplayName(t *testing.T) {
	t.Parallel()
	// Display names may contain anything printable, including non-ASCII: this
	// is the field people write their real name in.
	for _, name := range []string{"Josh Moses", "José Muñoz", "山田太郎", "O'Brien", "Ann-Marie"} {
		if err := ValidateDisplayName(name); err != nil {
			t.Errorf("ValidateDisplayName(%q) rejected a real name: %v", name, err)
		}
	}
	for _, name := range []string{"", "   ", "has\x00null", "line\nbreak", strings.Repeat("a", 129)} {
		if err := ValidateDisplayName(name); err == nil {
			t.Errorf("ValidateDisplayName(%q) should be rejected", name)
		}
	}
}

func TestValidateEmail(t *testing.T) {
	t.Parallel()
	// An empty email is valid: the field is optional.
	if err := ValidateEmail(""); err != nil {
		t.Errorf("an empty email should be permitted: %v", err)
	}
	for _, email := range []string{"a@b.co", "josh.moses+tag@example.co.uk", "x_y@sub.domain.org"} {
		if err := ValidateEmail(email); err != nil {
			t.Errorf("ValidateEmail(%q) rejected a valid address: %v", email, err)
		}
	}
	for _, email := range []string{"noatsign", "@nolocal.com", "nodomain@", "no@dot", "has space@x.com", "a@b.", "a@.b"} {
		if err := ValidateEmail(email); err == nil {
			t.Errorf("ValidateEmail(%q) accepted an invalid address", email)
		}
	}
}

func TestCreateParamsNormalizesAndDefaults(t *testing.T) {
	t.Parallel()
	p := CreateParams{
		Username:     "  Josh  ",
		Email:        "  JOSH@Example.COM ",
		PasswordHash: "$argon2id$...",
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if p.Username != "Josh" {
		t.Errorf("username = %q, want trimmed %q", p.Username, "Josh")
	}
	// An omitted display name falls back to the username rather than being empty.
	if p.DisplayName != "Josh" {
		t.Errorf("display name = %q, want the username as a fallback", p.DisplayName)
	}
	if p.Email != "josh@example.com" {
		t.Errorf("email = %q, want lowercased and trimmed", p.Email)
	}
}

func TestCreateParamsRequiresAHash(t *testing.T) {
	t.Parallel()
	// This package must never see a plaintext password; an empty hash is
	// therefore always a programming error worth catching loudly.
	p := CreateParams{Username: "josh", DisplayName: "Josh"}
	if err := p.Validate(); err == nil {
		t.Fatal("Validate accepted params with no password hash")
	}
}

func TestUserLockState(t *testing.T) {
	t.Parallel()
	now := time.Now()
	future := now.Add(10 * time.Minute)
	past := now.Add(-10 * time.Minute)

	u := &User{IsEnabled: true}
	if u.IsLocked(now) {
		t.Error("a user with no lock deadline is not locked")
	}
	if !u.CanSignIn(now) {
		t.Error("an enabled, unlocked user should be able to sign in")
	}

	u.LockedUntil = &future
	if !u.IsLocked(now) {
		t.Error("a future lock deadline means locked")
	}
	if u.CanSignIn(now) {
		t.Error("a locked user must not be able to sign in")
	}

	u.LockedUntil = &past
	if u.IsLocked(now) {
		t.Error("an expired lock deadline means unlocked")
	}

	u.LockedUntil = nil
	u.IsEnabled = false
	if u.CanSignIn(now) {
		t.Error("a disabled user must not be able to sign in")
	}
}

func TestQuotaAccounting(t *testing.T) {
	t.Parallel()

	unlimited := &User{QuotaBytes: nil, UsedBytes: 1 << 40}
	if !unlimited.HasUnlimitedQuota() {
		t.Error("a nil quota means unlimited")
	}
	if unlimited.RemainingQuota() != -1 {
		t.Errorf("RemainingQuota = %d, want -1 for unlimited", unlimited.RemainingQuota())
	}
	if !unlimited.CanStore(1 << 50) {
		t.Error("an unlimited user can store anything")
	}
	if unlimited.QuotaPercent() != 0 {
		t.Error("percent is meaningless without a quota")
	}

	limit := int64(1000)
	limited := &User{QuotaBytes: &limit, UsedBytes: 600}
	if got := limited.RemainingQuota(); got != 400 {
		t.Errorf("RemainingQuota = %d, want 400", got)
	}
	if !limited.CanStore(400) {
		t.Error("storing exactly the remaining amount must be allowed")
	}
	if limited.CanStore(401) {
		t.Error("storing more than remaining must be refused")
	}
	if got := limited.QuotaPercent(); got != 60 {
		t.Errorf("QuotaPercent = %v, want 60", got)
	}

	// An over-quota user — possible after an administrator lowers a limit —
	// reports zero remaining rather than a negative number.
	over := &User{QuotaBytes: &limit, UsedBytes: 1500}
	if got := over.RemainingQuota(); got != 0 {
		t.Errorf("RemainingQuota = %d, want 0 when over quota", got)
	}
	if got := over.QuotaPercent(); got != 100 {
		t.Errorf("QuotaPercent = %v, want it clamped to 100", got)
	}
	if over.CanStore(1) {
		t.Error("an over-quota user must not be able to store more")
	}

	// A zero quota is meaningful: the account may store nothing.
	zero := int64(0)
	none := &User{QuotaBytes: &zero}
	if none.CanStore(1) {
		t.Error("a zero quota must permit nothing")
	}
	if !none.CanStore(0) {
		t.Error("a zero-byte write is within a zero quota")
	}
}

// The public projection is what other users see. It must not carry quota,
// enablement, email, or the password hash.
func TestPublicProjectionOmitsSensitiveFields(t *testing.T) {
	t.Parallel()
	quota := int64(100)
	u := &User{
		ID:           uuid.New(),
		Username:     "josh",
		DisplayName:  "Josh Moses",
		Email:        "josh@example.com",
		PasswordHash: "$argon2id$secret",
		IsAdmin:      true,
		QuotaBytes:   &quota,
		UsedBytes:    50,
	}

	pub := u.Public()
	if pub.Username != "josh" || pub.DisplayName != "Josh Moses" || pub.ID != u.ID {
		t.Error("the public projection lost identifying fields")
	}

	// Structural check by reflection: adding a field to Public must be a
	// deliberate decision reviewed here, not something that happens because a
	// field was added upstream and copied along.
	allowed := map[string]bool{"ID": true, "Username": true, "DisplayName": true}
	typ := reflect.TypeOf(pub)
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if !allowed[name] {
			t.Errorf("users.Public exposes an unreviewed field %q; other users must not see it", name)
		}
	}
	if typ.NumField() != len(allowed) {
		t.Errorf("users.Public has %d fields, expected %d", typ.NumField(), len(allowed))
	}
}

// The full User struct is returned by admin and self endpoints. The password
// hash must never be serialized, whatever else changes about the type.
func TestUserJSONNeverIncludesPasswordHash(t *testing.T) {
	t.Parallel()
	u := &User{
		ID:           uuid.New(),
		Username:     "josh",
		DisplayName:  "Josh",
		PasswordHash: "$argon2id$v=19$m=65536,t=2,p=2$c2FsdA$aGFzaA",
	}

	encoded, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(encoded), "argon2id") || strings.Contains(string(encoded), "PasswordHash") {
		t.Fatalf("the password hash was serialized into JSON: %s", encoded)
	}

	// Confirm the tag is what protects it, not the value happening to be empty.
	field, ok := reflect.TypeOf(*u).FieldByName("PasswordHash")
	if !ok {
		t.Fatal("PasswordHash field is missing")
	}
	if field.Tag.Get("json") != "-" {
		t.Errorf(`PasswordHash json tag = %q, want "-"`, field.Tag.Get("json"))
	}
}

func TestValidationErrorCarriesField(t *testing.T) {
	t.Parallel()
	err := ValidateUsername("")
	var vErr *ValidationError
	if !errors.As(err, &vErr) {
		t.Fatalf("expected a *ValidationError, got %T", err)
	}
	if vErr.Field != "username" {
		t.Errorf("Field = %q, want username", vErr.Field)
	}
	if vErr.Reason == "" {
		t.Error("Reason should be a message shown to the user")
	}
}
