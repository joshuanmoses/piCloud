package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshuanmoses/piCloud/backend/internal/config"
	"github.com/joshuanmoses/piCloud/backend/internal/health"
)

const validDigest = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

func testConfig(t *testing.T) config.Storage {
	t.Helper()
	return config.Storage{
		Root:         t.TempDir(),
		DirMode:      0o750,
		FileMode:     0o640,
		MinFreeBytes: 1024,
	}
}

func openTestStorage(t *testing.T) *Storage {
	t.Helper()
	s, err := Open(testConfig(t), discardLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenCreatesFullLayout(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	s, err := Open(cfg, discardLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	for _, dir := range cfg.Subdirs() {
		info, err := os.Stat(filepath.Join(cfg.Root, dir))
		if err != nil {
			t.Errorf("%s/ was not created: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s/ is not a directory", dir)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)

	first, err := Open(cfg, discardLogger())
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	first.Close()

	// Restarting the service must not fail because the layout already exists.
	second, err := Open(cfg, discardLogger())
	if err != nil {
		t.Fatalf("second Open on an existing root failed: %v", err)
	}
	second.Close()
}

func TestOpenRejectsRelativeRoot(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.Root = "relative/root"

	if _, err := Open(cfg, discardLogger()); err == nil {
		t.Fatal("a relative storage root must be rejected")
	}
}

func TestOpenRejectsRootThatIsAFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t)
	cfg.Root = filePath
	if _, err := Open(cfg, discardLogger()); err == nil {
		t.Fatal("a storage root pointing at a regular file must be rejected")
	}
}

func TestOpenDetectsUnwritableRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root; permission bits are not enforced")
	}
	t.Parallel()

	cfg := testConfig(t)
	if _, err := Open(cfg, discardLogger()); err != nil {
		t.Fatalf("setup Open: %v", err)
	}
	// Make files/ read-only to simulate a wrong-ownership bind mount, which is
	// the single most common Raspberry Pi installation mistake.
	filesDir := filepath.Join(cfg.Root, config.DirFiles)
	if err := os.Chmod(filesDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filesDir, 0o750) })

	_, err := Open(cfg, discardLogger())
	if err == nil {
		t.Fatal("an unwritable files/ directory must fail at startup, not at first upload")
	}
	if !strings.Contains(err.Error(), "writable") {
		t.Errorf("error should explain the writability problem; got: %v", err)
	}
}

// --- Content addressing ---------------------------------------------------

func TestBlobPathFansOut(t *testing.T) {
	t.Parallel()
	got, err := BlobPath(validDigest)
	if err != nil {
		t.Fatalf("BlobPath: %v", err)
	}
	want := "files/ab/cd/" + validDigest
	if got != want {
		t.Errorf("BlobPath = %q, want %q", got, want)
	}
}

func TestVersionAndQuarantinePaths(t *testing.T) {
	t.Parallel()
	v, err := VersionPath(validDigest)
	if err != nil {
		t.Fatal(err)
	}
	if v != "versions/ab/cd/"+validDigest {
		t.Errorf("VersionPath = %q", v)
	}
	q, err := QuarantinePath(validDigest)
	if err != nil {
		t.Fatal(err)
	}
	if q != "quarantine/ab/cd/"+validDigest {
		t.Errorf("QuarantinePath = %q", q)
	}
}

// TestBlobPathRejectsMaliciousDigests is a security regression test. The digest
// is the only value that reaches a filesystem path, so if it can be poisoned
// the whole content-addressing defence collapses.
func TestBlobPathRejectsMaliciousDigests(t *testing.T) {
	t.Parallel()
	malicious := []struct {
		name   string
		digest string
	}{
		{"traversal", "../../../../etc/passwd"},
		{"traversal padded to length", strings.Repeat("../", 21) + "a"},
		{"absolute path", "/etc/shadow" + strings.Repeat("a", 53)},
		{"separator inside", "abcdef0123456789/bcdef0123456789abcdef0123456789abcdef012345678"},
		{"null byte", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef012345678\x00"},
		{"uppercase hex", strings.ToUpper(validDigest)},
		{"too short", "abcdef"},
		{"too long", validDigest + "ff"},
		{"empty", ""},
		{"non-hex", strings.Repeat("z", 64)},
		{"newline injection", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef012345\ncat"},
		{"unicode homoglyph", strings.Repeat("а", 64)}, // Cyrillic 'а'
		{"shell metacharacters", "abcdef01;rm -rf /;89abcdef0123456789abcdef0123456789abcdef01234"},
	}

	for _, tc := range malicious {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := BlobPath(tc.digest)
			if err == nil {
				t.Fatalf("BlobPath(%q) returned %q; it must be rejected", tc.digest, got)
			}
			if !errors.Is(err, ErrInvalidDigest) {
				t.Errorf("error should wrap ErrInvalidDigest; got %v", err)
			}
			if got != "" {
				t.Errorf("a rejected digest must not yield a path; got %q", got)
			}
		})
	}
}

func TestValidateDigestAcceptsOnlyLowercaseHex(t *testing.T) {
	t.Parallel()
	if err := ValidateDigest(validDigest); err != nil {
		t.Errorf("valid digest rejected: %v", err)
	}
	if err := ValidateDigest(strings.Repeat("0", 64)); err != nil {
		t.Errorf("all-zero digest should be structurally valid: %v", err)
	}
	if err := ValidateDigest(strings.Repeat("f", 64)); err != nil {
		t.Errorf("all-f digest should be structurally valid: %v", err)
	}
}

// --- Root confinement -----------------------------------------------------

func TestResolveWithinRootRejectsEscapes(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	escapes := []string{
		"../etc/passwd",
		"files/../../etc/passwd",
		"/etc/passwd",
		"/",
		"..",
		".",
		"files/../..",
		"files\\..\\..\\windows",
		"files/x\x00.txt",
		"",
		// Not a recognized subdirectory: refuse rather than create one.
		"unknown/thing",
		"etc/passwd",
	}

	for _, p := range escapes {
		t.Run(p, func(t *testing.T) {
			t.Parallel()
			got, err := s.ResolveWithinRoot(p)
			if err == nil {
				t.Fatalf("ResolveWithinRoot(%q) = %q; it must be rejected", p, got)
			}
			if !errors.Is(err, ErrOutsideRoot) {
				t.Errorf("error should wrap ErrOutsideRoot; got %v", err)
			}
		})
	}
}

func TestResolveWithinRootAcceptsLegitimatePaths(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	cases := map[string]string{
		"files/ab/cd/" + validDigest: "files/ab/cd/" + validDigest,
		"temp/upload-123":            "temp/upload-123",
		"./previews/ab/thumb.webp":   "previews/ab/thumb.webp",
		"backups/dump.sql":           "backups/dump.sql",
	}
	for in, want := range cases {
		got, err := s.ResolveWithinRoot(in)
		if err != nil {
			t.Errorf("ResolveWithinRoot(%q) failed: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ResolveWithinRoot(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRootedHandleBlocksSymlinkEscape verifies the os.Root guarantee the whole
// design leans on: a symlink planted inside the storage root must not grant
// access to anything outside it.
func TestRootedHandleBlocksSymlinkEscape(t *testing.T) {
	t.Parallel()

	outside := t.TempDir()
	secretPath := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t)
	s, err := Open(cfg, discardLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Plant a symlink pointing out of the root, as a hostile or careless
	// process with filesystem access might.
	link := filepath.Join(cfg.Root, config.DirFiles, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable on this filesystem: %v", err)
	}

	if _, err := s.Dir().Open("files/escape/secret.txt"); err == nil {
		t.Fatal("reading through a symlink out of the storage root must fail")
	}

	// A symlink to an absolute path outside the root must also be refused.
	fileLink := filepath.Join(cfg.Root, config.DirFiles, "direct")
	if err := os.Symlink(secretPath, fileLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := s.Dir().Open("files/direct"); err == nil {
		t.Fatal("following a symlink to a file outside the root must fail")
	}
}

func TestEnsureBlobDirCreatesFanout(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	rel, err := BlobPath(validDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureBlobDir(rel); err != nil {
		t.Fatalf("EnsureBlobDir: %v", err)
	}
	// Idempotent: an existing fanout directory is not an error.
	if err := s.EnsureBlobDir(rel); err != nil {
		t.Fatalf("EnsureBlobDir is not idempotent: %v", err)
	}

	info, err := os.Stat(filepath.Join(s.Root(), "files", "ab", "cd"))
	if err != nil || !info.IsDir() {
		t.Fatalf("fanout directory missing: %v", err)
	}
}

// --- Health ----------------------------------------------------------------

func TestCheckReportsHealthy(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	h := s.Check()
	if h.Status != health.Healthy {
		t.Errorf("status = %q (%s), want healthy", h.Status, h.Detail)
	}
	if !h.Writable {
		t.Error("a fresh storage root should be writable")
	}
}

func TestCheckDoesNotLeakFilesystemPaths(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	// /health is unauthenticated; it must not describe the server's layout.
	h := s.Check()
	if strings.Contains(h.Detail, s.Root()) {
		t.Errorf("health detail leaked the storage path: %q", h.Detail)
	}
}

func TestCheckFlagsInsufficientFreeSpace(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	// A floor larger than any real disk forces the low-space branch.
	cfg.MinFreeBytes = 1 << 62

	s, err := Open(cfg, discardLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	h := s.Check()
	if h.Status != health.Unhealthy {
		t.Errorf("status = %q, want unhealthy when below the free-space floor", h.Status)
	}
	if h.Detail == "" {
		t.Error("an unhealthy result should explain itself to the administrator")
	}
}

func TestHasFreeSpaceForRespectsFloor(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.MinFreeBytes = 1 << 62

	s, err := Open(cfg, discardLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ok, err := s.HasFreeSpaceFor(1)
	if err != nil {
		t.Fatalf("HasFreeSpaceFor: %v", err)
	}
	if ok {
		t.Error("writes must be refused once the free-space floor would be breached")
	}
}

func TestUsageReportsCapacity(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	u, err := s.Usage()
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if !u.Supported() {
		t.Skip("capacity reporting is unsupported on this platform")
	}
	if u.TotalBytes <= 0 {
		t.Error("total capacity should be positive")
	}
	if u.FreeBytes < 0 || u.FreeBytes > u.TotalBytes {
		t.Errorf("free bytes %d is not within [0, %d]", u.FreeBytes, u.TotalBytes)
	}
	if p := u.UsedPercent(); p < 0 || p > 100 {
		t.Errorf("used percent %.2f is out of range", p)
	}
	if u.FilesystemType == "" {
		t.Error("filesystem type should be reported")
	}
}

func TestFileModeIsNeverExecutable(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	// Even if an operator sets an executable mode, stored content must not be
	// executable: uploaded files are never run.
	cfg.FileMode = 0o777

	s, err := Open(cfg, discardLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if got := s.fileMode(); got&0o111 != 0 {
		t.Errorf("file mode %#o has execute bits set", got)
	}
}
