package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"

	"github.com/joshuanmoses/piCloud/backend/internal/config"
)

// CopyBufferSize is the fixed buffer used for every streaming copy.
//
// This is the number that makes a 20 GB upload cost the same memory as a 20 KB
// one. It is deliberately modest: on a Raspberry Pi serving several concurrent
// transfers, a large per-transfer buffer is what turns a busy moment into an
// out-of-memory kill.
const CopyBufferSize = 256 * 1024

// ErrSizeMismatch means the byte count did not match what was expected.
var ErrSizeMismatch = errors.New("storage: size mismatch")

// ErrChecksumMismatch means the content did not hash to the expected digest.
var ErrChecksumMismatch = errors.New("storage: checksum mismatch")

// TempWriter streams incoming content to a temporary file while hashing it.
//
// Content is never buffered in memory and never written directly to its final
// location: the digest is not known until the last byte arrives, and a partial
// file under files/ would be indistinguishable from a complete one.
type TempWriter struct {
	storage *Storage
	relPath string
	file    *os.File
	hasher  hash.Hash
	written int64
	closed  bool
}

// NewTempWriter creates a temporary file to receive content.
//
// id must be a caller-generated opaque identifier; it is validated to contain
// only characters that are safe in a path.
func (s *Storage) NewTempWriter(id string) (*TempWriter, error) {
	if err := validateTempID(id); err != nil {
		return nil, err
	}

	relPath := path.Join(config.DirTemp, id)
	f, err := s.root.OpenFile(relPath,
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(s.fileMode()))
	if err != nil {
		return nil, fmt.Errorf("storage: cannot create temporary file: %w", err)
	}

	return &TempWriter{storage: s, relPath: relPath, file: f, hasher: sha256.New()}, nil
}

// OpenTempWriter reopens an existing temporary file for appending, which is how
// a resumed upload continues.
//
// The existing content is re-read to rebuild the running hash. That costs one
// sequential pass over what has already been received, which is far cheaper
// than requiring the client to resend it, and it doubles as verification that
// the partial file is intact.
func (s *Storage) OpenTempWriter(id string, expectedBytes int64) (*TempWriter, error) {
	if err := validateTempID(id); err != nil {
		return nil, err
	}

	relPath := path.Join(config.DirTemp, id)

	info, err := s.root.Stat(relPath)
	if err != nil {
		return nil, fmt.Errorf("storage: cannot stat temporary file: %w", err)
	}
	if info.Size() != expectedBytes {
		// The database and the filesystem disagree about how much was
		// received. Refusing is the only safe answer: appending would produce
		// a file that is silently wrong.
		return nil, fmt.Errorf("%w: temporary file holds %d bytes, expected %d",
			ErrSizeMismatch, info.Size(), expectedBytes)
	}

	readHandle, err := s.root.Open(relPath)
	if err != nil {
		return nil, fmt.Errorf("storage: cannot reopen temporary file: %w", err)
	}
	hasher := sha256.New()
	buf := make([]byte, CopyBufferSize)
	written, err := io.CopyBuffer(hasher, readHandle, buf)
	closeErr := readHandle.Close()
	if err != nil {
		return nil, fmt.Errorf("storage: cannot rebuild hash of partial upload: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("storage: cannot close partial upload: %w", closeErr)
	}

	f, err := s.root.OpenFile(relPath, os.O_WRONLY|os.O_APPEND, fs.FileMode(s.fileMode()))
	if err != nil {
		return nil, fmt.Errorf("storage: cannot open temporary file for append: %w", err)
	}

	return &TempWriter{
		storage: s, relPath: relPath, file: f, hasher: hasher, written: written,
	}, nil
}

// Write implements io.Writer, hashing as it goes.
func (w *TempWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errors.New("storage: write to a closed temporary file")
	}
	n, err := w.file.Write(p)
	if n > 0 {
		// The hash must see exactly the bytes that reached the file, so it is
		// updated with the prefix actually written, not with all of p.
		w.hasher.Write(p[:n])
		w.written += int64(n)
	}
	return n, err
}

// CopyFrom streams from r, bounded by limit bytes.
//
// The limit is enforced here rather than by the caller so that no path can
// accidentally accept an unbounded stream. Exceeding it is an error, not a
// truncation: silently storing a partial file would be worse.
func (w *TempWriter) CopyFrom(r io.Reader, limit int64) (int64, error) {
	// One byte beyond the limit is read deliberately, so an oversized stream
	// is detected rather than quietly cut short.
	bounded := io.LimitReader(r, limit+1)
	buf := make([]byte, CopyBufferSize)

	start := w.written
	if _, err := io.CopyBuffer(w, bounded, buf); err != nil {
		return w.written - start, err
	}

	received := w.written - start
	if received > limit {
		return received, fmt.Errorf("%w: stream exceeded the %d byte limit", ErrSizeMismatch, limit)
	}
	return received, nil
}

// Written returns the number of bytes written so far.
func (w *TempWriter) Written() int64 { return w.written }

// Digest returns the hex SHA-256 of everything written so far.
func (w *TempWriter) Digest() string { return hex.EncodeToString(w.hasher.Sum(nil)) }

// Sync flushes the file to durable storage.
//
// Called after each chunk of a resumable upload so that a power cut cannot
// leave the database claiming bytes the filesystem never received — the exact
// inconsistency that would make a resumed upload produce a corrupt file.
func (w *TempWriter) Sync() error {
	if w.closed {
		return nil
	}
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("storage: cannot flush temporary file: %w", err)
	}
	return nil
}

// Close closes the temporary file without removing it.
func (w *TempWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	return w.file.Close()
}

// Discard closes and removes the temporary file. Safe to call more than once.
func (w *TempWriter) Discard() error {
	closeErr := w.Close()
	removeErr := w.storage.root.Remove(w.relPath)
	if removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
		return errors.Join(closeErr, removeErr)
	}
	return closeErr
}

// Commit moves the temporary file into content-addressed storage and returns
// its digest.
//
// The move is a hard link followed by an unlink, so the blob appears at its
// final path atomically and completely, or not at all. A reader can never
// observe a partially written blob.
//
// If the blob already exists — two users uploading identical content, or a
// retried upload — the existing file is kept and the temporary one discarded.
// That is deduplication, and it is safe precisely because the path is derived
// from the content.
func (w *TempWriter) Commit() (digest string, size int64, err error) {
	if err := w.Sync(); err != nil {
		return "", 0, err
	}
	if err := w.Close(); err != nil {
		return "", 0, fmt.Errorf("storage: cannot close temporary file: %w", err)
	}

	digest = w.Digest()
	size = w.written

	blobPath, err := BlobPath(digest)
	if err != nil {
		return "", 0, err
	}
	if err := w.storage.EnsureBlobDir(blobPath); err != nil {
		return "", 0, err
	}

	err = w.storage.root.Link(w.relPath, blobPath)
	switch {
	case err == nil:
		// Linked into place.
	case errors.Is(err, fs.ErrExist):
		// Already stored. Verify the size matches before trusting the existing
		// copy: a mismatch would mean either a SHA-256 collision or a corrupted
		// blob, and both warrant refusing rather than silently linking to it.
		info, statErr := w.storage.root.Stat(blobPath)
		if statErr != nil {
			return "", 0, fmt.Errorf("storage: cannot verify existing blob: %w", statErr)
		}
		if info.Size() != size {
			return "", 0, fmt.Errorf(
				"%w: stored blob %s is %d bytes but the new content is %d",
				ErrChecksumMismatch, digest[:12], info.Size(), size)
		}
	default:
		return "", 0, fmt.Errorf("storage: cannot commit content: %w", err)
	}

	// The temporary file is now redundant. Failing to remove it wastes space
	// but does not corrupt anything, so it is reported rather than fatal — the
	// reconciliation job sweeps leftovers.
	if err := w.storage.root.Remove(w.relPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return digest, size, fmt.Errorf("storage: content committed but the temporary file remains: %w", err)
	}

	return digest, size, nil
}

// OpenBlob opens stored content for reading.
func (s *Storage) OpenBlob(digest string) (*os.File, error) {
	blobPath, err := BlobPath(digest)
	if err != nil {
		return nil, err
	}
	f, err := s.root.Open(blobPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, digest[:12])
		}
		return nil, fmt.Errorf("storage: cannot open content: %w", err)
	}
	return f, nil
}

// StatBlob returns information about stored content.
func (s *Storage) StatBlob(digest string) (fs.FileInfo, error) {
	blobPath, err := BlobPath(digest)
	if err != nil {
		return nil, err
	}
	info, err := s.root.Stat(blobPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, digest[:12])
		}
		return nil, fmt.Errorf("storage: cannot stat content: %w", err)
	}
	return info, nil
}

// BlobExists reports whether content is present on disk.
func (s *Storage) BlobExists(digest string) bool {
	_, err := s.StatBlob(digest)
	return err == nil
}

// RemoveBlob deletes stored content.
//
// Only ever called for a blob whose reference count has reached zero, and only
// by the reconciliation job — never inline during a user-facing delete, where
// a concurrent upload could have just taken a new reference.
func (s *Storage) RemoveBlob(digest string) error {
	blobPath, err := BlobPath(digest)
	if err != nil {
		return err
	}
	if err := s.root.Remove(blobPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("storage: cannot remove content: %w", err)
	}
	return nil
}

// VerifyBlob re-reads stored content and confirms it still hashes to its
// address. Used by the integrity checker; it is a full read, so it is never
// run on the request path.
func (s *Storage) VerifyBlob(digest string) error {
	f, err := s.OpenBlob(digest)
	if err != nil {
		return err
	}
	defer f.Close()

	hasher := sha256.New()
	buf := make([]byte, CopyBufferSize)
	if _, err := io.CopyBuffer(hasher, f, buf); err != nil {
		return fmt.Errorf("storage: cannot read content for verification: %w", err)
	}

	actual := hex.EncodeToString(hasher.Sum(nil))
	if actual != digest {
		return fmt.Errorf("%w: content at %s hashes to %s", ErrChecksumMismatch, digest[:12], actual[:12])
	}
	return nil
}

// RemoveTemp deletes a temporary upload file by identifier.
// MoveTempTo relocates a temporary file to another path inside the root.
//
// Used for quarantine, and deliberately not for anything else: ordinary
// content reaches its home through Commit, which links rather than renames so
// a reader can never observe a partial blob. Quarantine has no readers, so a
// rename is both sufficient and simpler.
//
// The destination is resolved against the root the same way every other path
// is, so a caller cannot direct content outside it.
func (s *Storage) MoveTempTo(id, destination string) error {
	if err := validateTempID(id); err != nil {
		return err
	}
	target, err := s.ResolveWithinRoot(destination)
	if err != nil {
		return err
	}

	source := path.Join(config.DirTemp, id)
	if err := s.root.Rename(source, target); err != nil {
		return fmt.Errorf("storage: cannot move temporary file: %w", err)
	}
	return nil
}

func (s *Storage) RemoveTemp(id string) error {
	if err := validateTempID(id); err != nil {
		return err
	}
	if err := s.root.Remove(path.Join(config.DirTemp, id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("storage: cannot remove temporary file: %w", err)
	}
	return nil
}

// validateTempID enforces that a temporary filename is a plain identifier.
//
// The caller supplies a UUID, but this package does not assume that: anything
// reaching a filesystem call is validated here, so a future caller passing
// user input cannot open a traversal.
func validateTempID(id string) error {
	if len(id) == 0 || len(id) > 64 {
		return fmt.Errorf("%w: temporary id must be 1-64 characters", ErrOutsideRoot)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') || c == '-' || c == '_'
		if !ok {
			return fmt.Errorf("%w: temporary id %q contains an unsafe character", ErrOutsideRoot, id)
		}
	}
	return nil
}

// OpenTemp opens a temporary upload file for reading.
func (s *Storage) OpenTemp(id string) (*os.File, error) {
	if err := validateTempID(id); err != nil {
		return nil, err
	}
	f, err := s.root.Open(path.Join(config.DirTemp, id))
	if err != nil {
		return nil, fmt.Errorf("storage: cannot open temporary file: %w", err)
	}
	return f, nil
}
