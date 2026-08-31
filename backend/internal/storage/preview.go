package storage

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/joshuanmoses/piCloud/backend/internal/config"
)

// PreviewPath is the address of a generated thumbnail, relative to the storage
// root. It is keyed on the digest of the *source* content, so identical files
// share one thumbnail and a replaced file cannot serve its predecessor's.
//
//	"abcdef01..." -> "previews/ab/cd/abcdef01..."
func PreviewPath(sourceDigest string) (string, error) {
	return addressedPath(config.DirPreviews, sourceDigest)
}

// WritePreview stores a generated thumbnail.
//
// The write goes to a temporary name and is renamed into place, so a reader
// never sees a half-written image. Unlike a blob, a preview may legitimately be
// replaced (a regeneration with different dimensions), so this overwrites.
func (s *Storage) WritePreview(sourceDigest string, data []byte) error {
	relPath, err := PreviewPath(sourceDigest)
	if err != nil {
		return err
	}
	if err := s.EnsureBlobDir(relPath); err != nil {
		return err
	}

	// The suffix keeps a concurrent regeneration of the same content from
	// clobbering this one mid-write; whichever renames last wins, and both
	// wrote the same bytes.
	tmpPath := relPath + ".tmp"
	f, err := s.root.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fs.FileMode(s.fileMode()))
	if err != nil {
		return fmt.Errorf("storage: cannot create preview: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = s.root.Remove(tmpPath)
		return fmt.Errorf("storage: cannot write preview: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = s.root.Remove(tmpPath)
		return fmt.Errorf("storage: cannot flush preview: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = s.root.Remove(tmpPath)
		return fmt.Errorf("storage: cannot close preview: %w", err)
	}

	if err := s.root.Rename(tmpPath, relPath); err != nil {
		_ = s.root.Remove(tmpPath)
		return fmt.Errorf("storage: cannot commit preview: %w", err)
	}
	return nil
}

// OpenPreview opens a stored thumbnail for reading.
func (s *Storage) OpenPreview(sourceDigest string) (*os.File, error) {
	relPath, err := PreviewPath(sourceDigest)
	if err != nil {
		return nil, err
	}
	f, err := s.root.Open(relPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: preview for %s", ErrNotFound, sourceDigest[:12])
		}
		return nil, fmt.Errorf("storage: cannot open preview: %w", err)
	}
	return f, nil
}

// PreviewExists reports whether a thumbnail is present on disk.
//
// The database is the record of whether a preview *should* exist; this answers
// whether it actually does, which is how a preview lost to a disk problem gets
// noticed and regenerated rather than served as a 404 forever.
func (s *Storage) PreviewExists(sourceDigest string) bool {
	relPath, err := PreviewPath(sourceDigest)
	if err != nil {
		return false
	}
	_, err = s.root.Stat(relPath)
	return err == nil
}

// RemovePreview deletes a thumbnail. Missing is success: previews are derived
// data, and the caller wanted it gone.
func (s *Storage) RemovePreview(sourceDigest string) error {
	relPath, err := PreviewPath(sourceDigest)
	if err != nil {
		return err
	}
	if err := s.root.Remove(relPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("storage: cannot remove preview: %w", err)
	}
	return nil
}

// ReadPreviewInto streams a thumbnail to w and returns the number of bytes
// copied. It uses the same fixed buffer as every other copy in PiFiles, so a
// large preview cannot balloon memory on a small board.
func (s *Storage) ReadPreviewInto(w io.Writer, sourceDigest string) (int64, error) {
	f, err := s.OpenPreview(sourceDigest)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	buf := make([]byte, CopyBufferSize)
	n, err := io.CopyBuffer(w, f, buf)
	if err != nil {
		return n, fmt.Errorf("storage: cannot read preview: %w", err)
	}
	return n, nil
}
