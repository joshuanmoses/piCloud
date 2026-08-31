package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func digestOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func TestTempWriterCommitStoresContent(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	content := []byte("the quick brown fox jumps over the lazy dog")

	w, err := s.NewTempWriter("upload1")
	if err != nil {
		t.Fatalf("NewTempWriter: %v", err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatalf("Write: %v", err)
	}

	digest, size, err := w.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if digest != digestOf(content) {
		t.Errorf("digest = %s, want %s", digest, digestOf(content))
	}
	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}

	// The content must be readable back, byte for byte.
	f, err := s.OpenBlob(digest)
	if err != nil {
		t.Fatalf("OpenBlob: %v", err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Error("stored content does not match what was written")
	}

	// The temporary file must be gone.
	if _, err := s.Dir().Stat("temp/upload1"); err == nil {
		t.Error("the temporary file was left behind after commit")
	}
}

// The blob's path must be derived from its digest, not from any name.
func TestCommittedBlobIsContentAddressed(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	content := []byte("addressed by content")
	w, _ := s.NewTempWriter("upload1")
	w.Write(content)
	digest, _, err := w.Commit()
	if err != nil {
		t.Fatal(err)
	}

	expected := filepath.Join(s.Root(), "files", digest[0:2], digest[2:4], digest)
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("blob is not at its content address %s: %v", expected, err)
	}
}

// Two uploads of identical content must share one blob on disk.
func TestCommitDeduplicatesIdenticalContent(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	content := []byte("identical content uploaded twice")

	first, _ := s.NewTempWriter("upload1")
	first.Write(content)
	digestA, sizeA, err := first.Commit()
	if err != nil {
		t.Fatal(err)
	}

	second, _ := s.NewTempWriter("upload2")
	second.Write(content)
	digestB, sizeB, err := second.Commit()
	if err != nil {
		t.Fatalf("committing duplicate content failed: %v", err)
	}

	if digestA != digestB || sizeA != sizeB {
		t.Fatal("identical content produced different addresses")
	}

	// Exactly one file, and both temporary files cleaned up.
	var blobCount int
	filepath.Walk(filepath.Join(s.Root(), "files"), func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			blobCount++
		}
		return nil
	})
	if blobCount != 1 {
		t.Errorf("found %d blobs on disk, want 1 — content was not deduplicated", blobCount)
	}
}

func TestCopyFromEnforcesTheLimit(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	w, _ := s.NewTempWriter("upload1")
	defer w.Discard()

	// Exceeding the limit must be an error, not a silent truncation: storing
	// a partial file as if it were complete would be far worse.
	oversized := bytes.Repeat([]byte("x"), 1000)
	_, err := w.CopyFrom(bytes.NewReader(oversized), 100)
	if !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("CopyFrom = %v, want ErrSizeMismatch", err)
	}
}

func TestCopyFromAcceptsExactlyTheLimit(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	w, _ := s.NewTempWriter("upload1")
	defer w.Discard()

	content := bytes.Repeat([]byte("x"), 100)
	n, err := w.CopyFrom(bytes.NewReader(content), 100)
	if err != nil {
		t.Fatalf("content exactly at the limit was rejected: %v", err)
	}
	if n != 100 {
		t.Errorf("wrote %d bytes, want 100", n)
	}
}

// This is what makes a resumed upload produce the correct file.
func TestOpenTempWriterResumesTheHash(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	first := []byte("the first half of the content, ")
	second := []byte("and the second half.")
	whole := append(append([]byte{}, first...), second...)

	w, _ := s.NewTempWriter("resumable")
	w.Write(first)
	if err := w.Sync(); err != nil {
		t.Fatal(err)
	}
	w.Close()

	// A new process picks the upload up where it left off.
	resumed, err := s.OpenTempWriter("resumable", int64(len(first)))
	if err != nil {
		t.Fatalf("OpenTempWriter: %v", err)
	}
	if resumed.Written() != int64(len(first)) {
		t.Errorf("Written = %d, want %d", resumed.Written(), len(first))
	}
	if _, err := resumed.Write(second); err != nil {
		t.Fatal(err)
	}

	digest, size, err := resumed.Commit()
	if err != nil {
		t.Fatal(err)
	}

	// The digest must match a single-shot upload of the whole content.
	if digest != digestOf(whole) {
		t.Fatal("the resumed upload produced a different digest; the hash was not rebuilt correctly")
	}
	if size != int64(len(whole)) {
		t.Errorf("size = %d, want %d", size, len(whole))
	}
}

// If the database and the filesystem disagree about how much was received,
// appending would produce a silently wrong file.
func TestOpenTempWriterRefusesSizeMismatch(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	w, _ := s.NewTempWriter("resumable")
	w.Write([]byte("ten bytes!"))
	w.Sync()
	w.Close()

	_, err := s.OpenTempWriter("resumable", 999)
	if !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("OpenTempWriter = %v, want ErrSizeMismatch", err)
	}
}

func TestDiscardRemovesTheTemporaryFile(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	w, _ := s.NewTempWriter("abandoned")
	w.Write([]byte("partial content"))

	if err := w.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := s.Dir().Stat("temp/abandoned"); err == nil {
		t.Error("the temporary file survived Discard")
	}
	// Discard must be safe to call more than once, since it runs in a defer.
	if err := w.Discard(); err != nil {
		t.Errorf("a second Discard should be a no-op, got %v", err)
	}
}

// The temporary identifier reaches a filesystem path, so it is validated
// independently of whoever produced it.
func TestTempIDIsValidated(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	hostile := []string{
		"", "../escape", "../../etc/passwd", "/absolute",
		"has/slash", "has\\backslash", "with space", "null\x00byte",
		"dot.dot", strings.Repeat("a", 65),
	}
	for _, id := range hostile {
		if _, err := s.NewTempWriter(id); err == nil {
			t.Errorf("NewTempWriter(%q) must be rejected", id)
		}
		if err := s.RemoveTemp(id); err == nil {
			t.Errorf("RemoveTemp(%q) must be rejected", id)
		}
	}
}

func TestVerifyBlobDetectsCorruption(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	content := []byte("content that will be corrupted on disk")
	w, _ := s.NewTempWriter("upload1")
	w.Write(content)
	digest, _, err := w.Commit()
	if err != nil {
		t.Fatal(err)
	}

	if err := s.VerifyBlob(digest); err != nil {
		t.Fatalf("a freshly stored blob failed verification: %v", err)
	}

	// Corrupt the stored bytes behind the application's back, as a failing
	// disk or a filesystem bug would.
	blobPath := filepath.Join(s.Root(), "files", digest[0:2], digest[2:4], digest)
	if err := os.WriteFile(blobPath, []byte("tampered content of a different length"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := s.VerifyBlob(digest); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("VerifyBlob = %v, want ErrChecksumMismatch", err)
	}
}

func TestRemoveBlobIsIdempotent(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	content := []byte("to be removed")
	w, _ := s.NewTempWriter("upload1")
	w.Write(content)
	digest, _, _ := w.Commit()

	if err := s.RemoveBlob(digest); err != nil {
		t.Fatalf("RemoveBlob: %v", err)
	}
	// Removing again must not be an error; the reconciliation job may race
	// with a purge.
	if err := s.RemoveBlob(digest); err != nil {
		t.Errorf("removing an absent blob should be a no-op, got %v", err)
	}
	if s.BlobExists(digest) {
		t.Error("BlobExists reports a removed blob as present")
	}
}

func TestOpenBlobRejectsMalformedDigest(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	for _, digest := range []string{"", "../../etc/passwd", "not-hex", strings.Repeat("z", 64)} {
		if _, err := s.OpenBlob(digest); !errors.Is(err, ErrInvalidDigest) {
			t.Errorf("OpenBlob(%q) = %v, want ErrInvalidDigest", digest, err)
		}
	}
}

// An empty file is legitimate and must round-trip.
func TestEmptyContentRoundTrips(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	w, _ := s.NewTempWriter("empty")
	digest, size, err := w.Commit()
	if err != nil {
		t.Fatalf("committing empty content failed: %v", err)
	}
	if size != 0 {
		t.Errorf("size = %d, want 0", size)
	}
	// The SHA-256 of the empty string.
	if digest != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("digest = %s, not the empty-string hash", digest)
	}
	if !s.BlobExists(digest) {
		t.Error("the empty blob was not stored")
	}
}

// A large file must not be buffered in memory. This writes 8 MB through the
// fixed 256 KB buffer; the assertion is that it completes and hashes
// correctly, which it cannot do if the copy path is broken.
func TestLargeContentStreamsCorrectly(t *testing.T) {
	t.Parallel()
	s := openTestStorage(t)

	const size = 8 << 20
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i % 251)
	}

	w, _ := s.NewTempWriter("large")
	n, err := w.CopyFrom(bytes.NewReader(content), size)
	if err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if n != size {
		t.Fatalf("wrote %d bytes, want %d", n, size)
	}

	digest, written, err := w.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if written != size {
		t.Errorf("committed %d bytes, want %d", written, size)
	}
	if digest != digestOf(content) {
		t.Error("the streamed digest does not match the content")
	}
}
