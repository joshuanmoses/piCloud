// Package storage owns the on-disk data root.
//
// Two rules define this package:
//
//  1. No path handed to the filesystem is ever derived from user-supplied text.
//     Content is addressed by the SHA-256 digest of its bytes, so the only
//     characters that reach a syscall are [0-9a-f/].
//
//  2. Every filesystem operation goes through an *os.Root handle rooted at the
//     storage root. The kernel-assisted *os.Root API refuses to traverse ".."
//     or follow a symlink out of the root, which closes path-traversal and
//     symlink-escape attacks structurally rather than by string inspection.
package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/joshuanmoses/piCloud/backend/internal/config"
	"github.com/joshuanmoses/piCloud/backend/internal/health"
)

// Errors returned by this package.
var (
	// ErrInvalidDigest is returned when a content address is malformed.
	ErrInvalidDigest = errors.New("storage: invalid content digest")
	// ErrOutsideRoot is returned when a path would escape the storage root.
	ErrOutsideRoot = errors.New("storage: path escapes storage root")
	// ErrNotFound is returned when a blob does not exist.
	ErrNotFound = errors.New("storage: blob not found")
	// ErrInsufficientSpace is returned when a write would breach the free-space floor.
	ErrInsufficientSpace = errors.New("storage: insufficient free space")
)

// DigestLength is the hex length of a SHA-256 content address.
const DigestLength = 64

// fanoutDepth is how many two-character directory levels a blob path uses.
// Two levels yields 65,536 leaf directories, which keeps per-directory entry
// counts reasonable on ext4 even with millions of files.
const fanoutDepth = 2

// DiskUsage reports filesystem capacity for the volume holding the data root.
type DiskUsage struct {
	TotalBytes     int64  `json:"total_bytes"`
	UsedBytes      int64  `json:"used_bytes"`
	FreeBytes      int64  `json:"free_bytes"`
	FilesystemType string `json:"filesystem_type"`
	InodesTotal    int64  `json:"inodes_total"`
	InodesFree     int64  `json:"inodes_free"`
	BlockSizeBytes int64  `json:"block_size_bytes"`

	// usageSupported records whether the platform actually produced numbers.
	usageSupported bool
}

// Supported reports whether capacity figures are meaningful on this platform.
func (d DiskUsage) Supported() bool { return d.usageSupported }

// UsedPercent returns used capacity as a percentage, or 0 when unknown.
func (d DiskUsage) UsedPercent() float64 {
	if d.TotalBytes <= 0 {
		return 0
	}
	return float64(d.UsedBytes) / float64(d.TotalBytes) * 100
}

// Storage is a handle on the initialized data root.
type Storage struct {
	cfg    config.Storage
	root   *os.Root
	logger *slog.Logger
}

// Open prepares the storage root for use: it creates the root and the expected
// subdirectories, verifies that the process can actually write there, and
// returns a traversal-safe handle.
//
// It never formats, partitions, or erases anything. The volume is expected to
// already be mounted by the operating system.
func Open(cfg config.Storage, logger *slog.Logger) (*Storage, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if !filepath.IsAbs(cfg.Root) {
		return nil, fmt.Errorf("storage: root %q must be an absolute path", cfg.Root)
	}

	dirMode := fs.FileMode(cfg.DirMode)
	if dirMode == 0 {
		dirMode = 0o750
	}

	// MkdirAll on the root itself must happen before we can obtain an os.Root
	// handle. This is the only path built outside the rooted handle, and it
	// comes from validated configuration, never from a request.
	if err := os.MkdirAll(cfg.Root, dirMode); err != nil {
		return nil, fmt.Errorf("storage: cannot create root %q: %w", cfg.Root, err)
	}

	info, err := os.Stat(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("storage: cannot stat root %q: %w", cfg.Root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("storage: root %q exists but is not a directory", cfg.Root)
	}

	root, err := os.OpenRoot(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("storage: cannot open root %q: %w", cfg.Root, err)
	}

	s := &Storage{cfg: cfg, root: root, logger: logger}

	if err := s.ensureLayout(dirMode); err != nil {
		root.Close()
		return nil, err
	}
	if err := s.verifyWritable(); err != nil {
		root.Close()
		return nil, err
	}

	usage, _ := s.Usage()
	logger.Info("storage ready",
		"root", cfg.Root,
		"filesystem", usage.FilesystemType,
		"total", config.FormatSize(usage.TotalBytes),
		"free", config.FormatSize(usage.FreeBytes),
	)
	return s, nil
}

// Close releases the rooted directory handle.
func (s *Storage) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.Close()
}

// Root returns the absolute path of the data root. It is used for operator
// facing output such as the admin storage page; it is never sent to
// non-administrators, and never used to build a path from user input.
func (s *Storage) Root() string { return s.cfg.Root }

// Dir returns the traversal-safe handle. Later phases perform all of their file
// I/O through it.
func (s *Storage) Dir() *os.Root { return s.root }

// Config returns the storage configuration in effect.
func (s *Storage) Config() config.Storage { return s.cfg }

// ensureLayout creates the fixed subdirectories beneath the root.
func (s *Storage) ensureLayout(mode fs.FileMode) error {
	for _, d := range s.cfg.Subdirs() {
		if err := s.root.Mkdir(d, mode); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("storage: cannot create %s/: %w", d, err)
		}
	}
	return nil
}

// verifyWritable proves the process can create and remove a file in each
// directory it will actually write to. Discovering a read-only mount or a
// permissions mistake at startup is far better than discovering it during a
// user's first upload.
func (s *Storage) verifyWritable() error {
	for _, d := range []string{config.DirFiles, config.DirVersions, config.DirTemp, config.DirPreviews, config.DirQuarantine} {
		probe := path.Join(d, ".pifiles-write-check")
		f, err := s.root.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(s.fileMode()))
		if err != nil {
			return fmt.Errorf("storage: %s/ is not writable by this process (uid %d): %w", d, os.Getuid(), err)
		}
		_, writeErr := f.WriteString("ok")
		closeErr := f.Close()
		rmErr := s.root.Remove(probe)
		if err := errors.Join(writeErr, closeErr, rmErr); err != nil {
			return fmt.Errorf("storage: write check failed in %s/: %w", d, err)
		}
	}
	return nil
}

func (s *Storage) fileMode() uint32 {
	if s.cfg.FileMode == 0 {
		return 0o640
	}
	// Content is never executable, regardless of what was configured.
	return s.cfg.FileMode &^ 0o111
}

// Usage reports capacity for the filesystem holding the data root.
func (s *Storage) Usage() (DiskUsage, error) { return diskUsage(s.cfg.Root) }

// HasFreeSpaceFor reports whether n additional bytes can be written while
// preserving the configured free-space floor. When capacity cannot be measured
// the answer is yes; refusing all uploads because statfs is unavailable would
// be worse than the risk it guards against.
func (s *Storage) HasFreeSpaceFor(n int64) (bool, error) {
	u, err := s.Usage()
	if err != nil {
		return true, err
	}
	if !u.Supported() {
		return true, nil
	}
	return u.FreeBytes-n >= s.cfg.MinFreeBytes, nil
}

// Health describes the storage subsystem for the health endpoint. It contains
// no absolute paths, because /health is reachable without authentication and
// filesystem layout is not something to hand to anonymous callers.
type Health struct {
	Status         health.Status `json:"status"`
	Writable       bool          `json:"writable"`
	FilesystemType string        `json:"filesystem_type,omitempty"`
	TotalBytes     int64         `json:"total_bytes,omitempty"`
	FreeBytes      int64         `json:"free_bytes,omitempty"`
	UsedPercent    float64       `json:"used_percent,omitempty"`
	Detail         string        `json:"detail,omitempty"`
}

// Check probes the storage root and summarizes its condition.
func (s *Storage) Check() Health {
	h := Health{Status: health.Healthy, Writable: true}

	if err := s.verifyWritable(); err != nil {
		return Health{
			Status:   health.Unhealthy,
			Writable: false,
			Detail:   "storage root is not writable",
		}
	}

	u, err := s.Usage()
	if err != nil {
		h.Status = health.Degraded
		h.Detail = "capacity could not be determined"
		return h
	}
	if !u.Supported() {
		return h
	}

	h.FilesystemType = u.FilesystemType
	h.TotalBytes = u.TotalBytes
	h.FreeBytes = u.FreeBytes
	h.UsedPercent = u.UsedPercent()

	switch {
	case u.FreeBytes < s.cfg.MinFreeBytes:
		h.Status = health.Unhealthy
		h.Detail = fmt.Sprintf("free space (%s) is below the configured minimum (%s)",
			config.FormatSize(u.FreeBytes), config.FormatSize(s.cfg.MinFreeBytes))
	case u.FreeBytes < s.cfg.MinFreeBytes*2:
		h.Status = health.Degraded
		h.Detail = fmt.Sprintf("free space (%s) is approaching the configured minimum (%s)",
			config.FormatSize(u.FreeBytes), config.FormatSize(s.cfg.MinFreeBytes))
	case u.InodesTotal > 0 && u.InodesFree*100 < u.InodesTotal*5:
		h.Status = health.Degraded
		h.Detail = "fewer than 5% of filesystem inodes remain"
	}
	return h
}

// --- Content addressing ---------------------------------------------------

// BlobPath converts a hex SHA-256 digest into the fanned-out path of the blob,
// relative to the storage root. The digest is validated strictly: only 64
// lowercase hex characters are accepted, so the result cannot contain a path
// separator, a dot segment, or any other metacharacter.
//
//	"abcdef01..." -> "files/ab/cd/abcdef01..."
func BlobPath(digest string) (string, error) { return addressedPath(config.DirFiles, digest) }

// VersionPath is the equivalent address inside versions/.
func VersionPath(digest string) (string, error) { return addressedPath(config.DirVersions, digest) }

// QuarantinePath is the equivalent address inside quarantine/.
func QuarantinePath(digest string) (string, error) {
	return addressedPath(config.DirQuarantine, digest)
}

func addressedPath(prefix, digest string) (string, error) {
	if err := ValidateDigest(digest); err != nil {
		return "", err
	}
	parts := make([]string, 0, fanoutDepth+2)
	parts = append(parts, prefix)
	for i := 0; i < fanoutDepth; i++ {
		parts = append(parts, digest[i*2:i*2+2])
	}
	parts = append(parts, digest)
	return path.Join(parts...), nil
}

// ValidateDigest enforces the exact shape of a content address.
func ValidateDigest(digest string) error {
	if len(digest) != DigestLength {
		return fmt.Errorf("%w: expected %d hex characters, got %d", ErrInvalidDigest, DigestLength, len(digest))
	}
	for i := 0; i < len(digest); i++ {
		c := digest[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%w: %q is not lowercase hexadecimal", ErrInvalidDigest, digest)
		}
	}
	return nil
}

// EnsureBlobDir creates the fanned-out parent directories for a blob path.
func (s *Storage) EnsureBlobDir(relPath string) error {
	dir := path.Dir(relPath)
	if dir == "." || dir == "/" {
		return nil
	}
	mode := fs.FileMode(s.cfg.DirMode)
	if mode == 0 {
		mode = 0o750
	}
	if err := s.root.MkdirAll(dir, mode); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("storage: cannot create blob directory: %w", err)
	}
	return nil
}

// ResolveWithinRoot validates that a caller-supplied relative path stays inside
// the storage root and refers to one of the known subdirectories.
//
// Ordinary file operations should use the content-addressed constructors above
// and never call this. It exists for maintenance tooling — reconciliation,
// backups — that legitimately walks the tree, and it is deliberately strict.
func (s *Storage) ResolveWithinRoot(relPath string) (string, error) {
	if relPath == "" {
		return "", fmt.Errorf("%w: empty path", ErrOutsideRoot)
	}
	if filepath.IsAbs(relPath) || strings.HasPrefix(relPath, "/") {
		return "", fmt.Errorf("%w: %q is absolute", ErrOutsideRoot, relPath)
	}
	if strings.ContainsRune(relPath, 0) {
		return "", fmt.Errorf("%w: path contains a NUL byte", ErrOutsideRoot)
	}
	// Reject Windows-style separators outright rather than trying to normalize
	// them; they have no legitimate use here and only create ambiguity.
	if strings.ContainsRune(relPath, '\\') {
		return "", fmt.Errorf("%w: backslashes are not permitted", ErrOutsideRoot)
	}

	cleaned := path.Clean(relPath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: %q traverses above the root", ErrOutsideRoot, relPath)
	}

	top, _, _ := strings.Cut(cleaned, "/")
	if !s.isKnownSubdir(top) {
		return "", fmt.Errorf("%w: %q is not a recognized storage subdirectory", ErrOutsideRoot, top)
	}
	return cleaned, nil
}

func (s *Storage) isKnownSubdir(name string) bool {
	for _, d := range s.cfg.Subdirs() {
		if d == name {
			return true
		}
	}
	return false
}
