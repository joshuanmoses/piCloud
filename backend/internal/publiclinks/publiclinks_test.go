package publiclinks

import (
	"strings"
	"testing"
	"time"

	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
)

func TestGenerateTokenIsUnpredictable(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, 200)
	for range 200 {
		token, err := GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken: %v", err)
		}
		raw := token.Reveal()

		if len(raw) != EncodedTokenLength {
			t.Fatalf("token is %d characters, want %d", len(raw), EncodedTokenLength)
		}
		if _, duplicate := seen[raw]; duplicate {
			t.Fatal("a token was generated twice")
		}
		seen[raw] = struct{}{}
	}
}

// The token is the entire credential for a public link, so it must never
// appear in a log line by accident.
func TestTokenCannotBePrintedByAccident(t *testing.T) {
	t.Parallel()

	token, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	raw := token.Reveal()

	if strings.Contains(token.String(), raw) {
		t.Error("String() exposed the token")
	}
	if got := token.String(); got != "[REDACTED]" {
		t.Errorf("String() = %q", got)
	}
}

func TestHashTokenIsStableAndOneWay(t *testing.T) {
	t.Parallel()

	const token = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"

	first := HashToken(token)
	second := HashToken(token)

	if len(first) != 32 {
		t.Errorf("hash is %d bytes, want 32", len(first))
	}
	if string(first) != string(second) {
		t.Error("hashing must be deterministic")
	}
	if strings.Contains(string(first), token) {
		t.Error("the hash contains the token")
	}
	if string(HashToken(token+"x")) == string(first) {
		t.Error("different tokens produced the same hash")
	}
}

// Malformed tokens are rejected on shape, so a flood of garbage cannot be
// turned into database load.
func TestValidateTokenShape(t *testing.T) {
	t.Parallel()

	valid, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateTokenShape(valid.Reveal()); err != nil {
		t.Errorf("a generated token was rejected: %v", err)
	}

	bad := []string{
		"",
		"short",
		strings.Repeat("a", EncodedTokenLength-1),
		strings.Repeat("a", EncodedTokenLength+1),
		strings.Repeat("a", EncodedTokenLength-1) + "/", // not URL-safe
		strings.Repeat("a", EncodedTokenLength-1) + "+",
		strings.Repeat("a", EncodedTokenLength-1) + "=",
		"../../../etc/passwd" + strings.Repeat("a", 24),
		strings.Repeat("a", EncodedTokenLength-1) + "\x00",
	}
	for _, token := range bad {
		if err := ValidateTokenShape(token); err == nil {
			t.Errorf("ValidateTokenShape(%q) should fail", token)
		}
	}
}

func TestKindDerivedFromPermissions(t *testing.T) {
	t.Parallel()

	cases := map[Kind]permissions.Set{
		KindView:          PresetView,
		KindDownload:      PresetDownload,
		KindUpload:        PresetUpload,
		KindUploadAndView: PresetUploadAndView,
	}
	for want, set := range cases {
		link := &Link{Permissions: set}
		if got := link.Kind(); got != want {
			t.Errorf("Kind() = %q for %s, want %q", got, set, want)
		}
	}
}

// A file request must not let its visitor see what is already there, or
// somebody handed the link could read everyone else's submissions.
func TestUploadPresetGrantsNoReading(t *testing.T) {
	t.Parallel()

	if PresetUpload.Has(permissions.Read) {
		t.Error("an upload-only link must not grant read")
	}
	if PresetUpload.Has(permissions.Download) {
		t.Error("an upload-only link must not grant download")
	}
	if !PresetUpload.HasAny(permissions.Upload | permissions.Create) {
		t.Error("an upload link must grant uploading")
	}
	// And no preset ever grants management of the item itself.
	for name, preset := range map[string]permissions.Set{
		"view": PresetView, "download": PresetDownload,
		"upload": PresetUpload, "upload+view": PresetUploadAndView,
	} {
		if preset.HasAny(permissions.Share | permissions.Manage | permissions.Delete) {
			t.Errorf("the %s preset grants more than it should: %s", name, preset)
		}
	}
}

func TestParseKind(t *testing.T) {
	t.Parallel()

	for _, name := range KindNames() {
		if _, err := ParseKind(name); err != nil {
			t.Errorf("ParseKind(%q): %v", name, err)
		}
	}
	// An empty kind defaults to the ordinary share link.
	if set, err := ParseKind(""); err != nil || set != PresetDownload {
		t.Errorf("ParseKind(\"\") = %s, %v", set, err)
	}
	for _, bad := range []string{"admin", "everything", "read;drop table", "🙂"} {
		if _, err := ParseKind(bad); err == nil {
			t.Errorf("ParseKind(%q) should fail", bad)
		}
	}
}

func TestLinkLiveness(t *testing.T) {
	t.Parallel()

	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	live := &Link{}
	if !live.IsLive(now) {
		t.Error("a link with no constraints should be live")
	}

	revoked := &Link{RevokedAt: &past}
	if revoked.IsLive(now) {
		t.Error("a revoked link is not live")
	}

	expired := &Link{ExpiresAt: &past}
	if expired.IsLive(now) {
		t.Error("an expired link is not live")
	}

	notYet := &Link{ExpiresAt: &future}
	if !notYet.IsLive(now) {
		t.Error("a link expiring in the future is live")
	}

	// An expiry exactly now has passed: the boundary must not be usable.
	boundary := &Link{ExpiresAt: &now}
	if boundary.IsLive(now) {
		t.Error("a link expiring exactly now must not be live")
	}

	three := 3
	usedUp := &Link{MaxUses: &three, UseCount: 3}
	if usedUp.IsLive(now) {
		t.Error("a link at its usage cap is not live")
	}
	if usedUp.RemainingUses() != 0 {
		t.Errorf("RemainingUses = %d, want 0", usedUp.RemainingUses())
	}

	partial := &Link{MaxUses: &three, UseCount: 1}
	if !partial.IsLive(now) || partial.RemainingUses() != 2 {
		t.Errorf("a partially used link should be live with 2 uses left, got %d", partial.RemainingUses())
	}

	unlimited := &Link{}
	if unlimited.RemainingUses() != -1 {
		t.Error("an uncapped link should report -1 remaining")
	}
}

func TestNormalizeExtensions(t *testing.T) {
	t.Parallel()

	got := NormalizeExtensions([]string{"PDF", ".jpg", "  .PNG  ", "jpg", "", ".pdf"})
	want := []string{".pdf", ".jpg", ".png"}

	if len(got) != len(want) {
		t.Fatalf("NormalizeExtensions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The extension list reaches a comparison against a filename, so anything
// path-like or wildcard-like is dropped rather than being given a meaning
// nobody expects.
func TestNormalizeExtensionsRejectsOddEntries(t *testing.T) {
	t.Parallel()

	got := NormalizeExtensions([]string{
		"../../etc", ".a/b", ".*", ".pdf ", "." + strings.Repeat("x", 40), ".ok",
	})
	for _, ext := range got {
		if strings.ContainsAny(ext[1:], "./\\*? ") {
			t.Errorf("kept an unsafe entry: %q", ext)
		}
		if len(ext) > 16 {
			t.Errorf("kept an over-long entry: %q", ext)
		}
	}
	// The one good entry survives.
	found := false
	for _, ext := range got {
		if ext == ".ok" {
			found = true
		}
	}
	if !found {
		t.Errorf("dropped a valid entry: %v", got)
	}
}

func TestNormalizeExtensionsIsBounded(t *testing.T) {
	t.Parallel()

	many := make([]string, 500)
	for i := range many {
		many[i] = ".e" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))
	}
	if got := NormalizeExtensions(many); len(got) > 50 {
		t.Errorf("kept %d extensions; the list must be bounded", len(got))
	}
}

func TestExtensionAllowed(t *testing.T) {
	t.Parallel()

	// No filter means anything.
	open := &Link{}
	if !open.ExtensionAllowed("anything.exe") {
		t.Error("an empty filter should accept anything")
	}

	filtered := &Link{AllowedExtensions: []string{".pdf", ".jpg"}}
	for _, name := range []string{"report.pdf", "REPORT.PDF", "photo.jpg", "a.b.pdf"} {
		if !filtered.ExtensionAllowed(name) {
			t.Errorf("%q should be allowed", name)
		}
	}
	for _, name := range []string{"script.exe", "archive.zip", "noextension", "pdf"} {
		if filtered.ExtensionAllowed(name) {
			t.Errorf("%q should be rejected", name)
		}
	}
}
