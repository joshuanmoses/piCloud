package files

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/joshuanmoses/piCloud/backend/internal/config"
)

// Reconciliation detects and repairs disagreement between the database and the
// filesystem.
//
// The two halves are updated in a deliberate order — content is linked into
// place before its metadata is committed — so only one kind of inconsistency is
// expected in normal operation: an orphaned blob, left behind when a metadata
// commit failed after the bytes were written. The opposite, metadata pointing
// at content that was never written, means something is genuinely wrong and is
// reported as a problem rather than repaired automatically.

// ReconcileReport summarises what a reconciliation pass found.
type ReconcileReport struct {
	// BlobsChecked is how many blob records were examined.
	BlobsChecked int `json:"blobs_checked"`
	// MissingContent are blobs the database knows about whose bytes are absent.
	// This is data loss: the files referencing them cannot be downloaded.
	MissingContent []string `json:"missing_content,omitempty"`
	// OrphanedFiles are files on disk with no blob record. Safe to remove.
	OrphanedFiles []string `json:"orphaned_files,omitempty"`
	// UnreferencedBlobs are blob records whose reference count is zero.
	UnreferencedBlobs []string `json:"unreferenced_blobs,omitempty"`
	// StaleTempFiles are leftovers in temp/ with no upload session.
	StaleTempFiles []string `json:"stale_temp_files,omitempty"`
	// RefCountErrors are blobs whose stored count disagreed with reality.
	RefCountErrors int `json:"ref_count_errors"`

	// BytesReclaimed counts what a repairing pass actually freed.
	BytesReclaimed int64 `json:"bytes_reclaimed"`
	// Repaired reports whether changes were made.
	Repaired bool   `json:"repaired"`
	Duration string `json:"duration"`
}

// HasProblems reports whether anything needs an administrator's attention.
func (r *ReconcileReport) HasProblems() bool {
	return len(r.MissingContent) > 0 || r.RefCountErrors > 0
}

// ReconcileOptions controls a pass.
type ReconcileOptions struct {
	// Repair applies fixes. When false the pass only reports, which is what an
	// administrator should run first.
	Repair bool
	// MinAge protects recently created content from being judged orphaned.
	// Without it, a pass running concurrently with an upload would see a blob
	// that has been linked but whose metadata has not yet committed, and
	// delete content that was about to become a perfectly good file.
	MinAge time.Duration
}

// DefaultReconcileMinAge is a conservative window for the race described above.
const DefaultReconcileMinAge = time.Hour

// Reconcile compares the database against the filesystem.
func (s *Service) Reconcile(ctx context.Context, opts ReconcileOptions) (*ReconcileReport, error) {
	started := time.Now()
	if opts.MinAge <= 0 {
		opts.MinAge = DefaultReconcileMinAge
	}
	cutoff := started.Add(-opts.MinAge)

	report := &ReconcileReport{Repaired: opts.Repair}

	if err := s.checkBlobRecords(ctx, report, opts, cutoff); err != nil {
		return nil, err
	}
	if err := s.findOrphanedFiles(ctx, report, opts, cutoff); err != nil {
		return nil, err
	}
	if err := s.findStaleTempFiles(ctx, report, opts, cutoff); err != nil {
		return nil, err
	}
	if err := s.verifyRefCounts(ctx, report, opts); err != nil {
		return nil, err
	}

	report.Duration = time.Since(started).String()

	if report.HasProblems() {
		s.logger.Error("reconciliation found problems requiring attention",
			"missing_content", len(report.MissingContent),
			"ref_count_errors", report.RefCountErrors)
	} else {
		s.logger.Info("reconciliation complete",
			"blobs_checked", report.BlobsChecked,
			"orphaned_files", len(report.OrphanedFiles),
			"unreferenced_blobs", len(report.UnreferencedBlobs),
			"stale_temp_files", len(report.StaleTempFiles),
			"bytes_reclaimed", report.BytesReclaimed,
			"repaired", opts.Repair,
			"duration", report.Duration)
	}

	return report, nil
}

// checkBlobRecords verifies every blob record has its bytes, and collects
// records that are no longer referenced.
func (s *Service) checkBlobRecords(
	ctx context.Context, report *ReconcileReport, opts ReconcileOptions, cutoff time.Time,
) error {
	rows, err := s.nodes.DB().Pool().Query(ctx,
		`SELECT hash, size_bytes, ref_count, created_at FROM blobs ORDER BY created_at`)
	if err != nil {
		return fmt.Errorf("files: read blobs: %w", err)
	}
	defer rows.Close()

	type blobRow struct {
		hash      string
		size      int64
		refCount  int
		createdAt time.Time
	}
	var unreferenced []blobRow

	for rows.Next() {
		var b blobRow
		if err := rows.Scan(&b.hash, &b.size, &b.refCount, &b.createdAt); err != nil {
			return fmt.Errorf("files: scan blob: %w", err)
		}
		report.BlobsChecked++

		if !s.storage.BlobExists(b.hash) {
			// Metadata without content. This is data loss, not something to
			// repair automatically: deleting the record would silently discard
			// the user's file, and the administrator needs to know so they can
			// restore from backup.
			report.MissingContent = append(report.MissingContent, b.hash)
			continue
		}

		if b.refCount == 0 && b.createdAt.Before(cutoff) {
			unreferenced = append(unreferenced, b)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("files: read blobs: %w", err)
	}

	for _, b := range unreferenced {
		report.UnreferencedBlobs = append(report.UnreferencedBlobs, b.hash)
		if !opts.Repair {
			continue
		}

		// Re-check the reference count inside the delete, so a node created
		// since the scan began cannot have its content removed underneath it.
		tag, err := s.nodes.DB().Pool().Exec(ctx,
			`DELETE FROM blobs WHERE hash = $1 AND ref_count = 0`, b.hash)
		if err != nil {
			s.logger.Warn("could not remove blob record", "digest", b.hash[:12], "error", err)
			continue
		}
		if tag.RowsAffected() == 0 {
			// Something took a reference between the scan and now.
			continue
		}
		if err := s.storage.RemoveBlob(b.hash); err != nil {
			s.logger.Warn("could not remove blob content", "digest", b.hash[:12], "error", err)
			continue
		}
		report.BytesReclaimed += b.size
	}

	return nil
}

// findOrphanedFiles walks files/ looking for content with no blob record.
func (s *Service) findOrphanedFiles(
	ctx context.Context, report *ReconcileReport, opts ReconcileOptions, cutoff time.Time,
) error {
	known := map[string]struct{}{}
	rows, err := s.nodes.DB().Pool().Query(ctx, `SELECT hash FROM blobs`)
	if err != nil {
		return fmt.Errorf("files: read blob hashes: %w", err)
	}
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			rows.Close()
			return fmt.Errorf("files: scan blob hash: %w", err)
		}
		known[hash] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("files: read blob hashes: %w", err)
	}

	err = fs.WalkDir(s.storage.Dir().FS(), config.DirFiles, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		name := path.Base(p)
		// The write-check probe and anything else non-blob-shaped is ignored.
		if len(name) != 64 || !isHex(name) {
			return nil
		}
		if _, exists := known[name]; exists {
			return nil
		}

		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		// A blob linked into place moments ago may not have its record yet.
		if info.ModTime().After(cutoff) {
			return nil
		}

		report.OrphanedFiles = append(report.OrphanedFiles, name)
		if opts.Repair {
			if err := s.storage.RemoveBlob(name); err != nil {
				s.logger.Warn("could not remove orphaned file", "digest", name[:12], "error", err)
				return nil
			}
			report.BytesReclaimed += info.Size()
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("files: walk content: %w", err)
	}
	return nil
}

// findStaleTempFiles looks for leftovers in temp/ with no upload session.
func (s *Service) findStaleTempFiles(
	ctx context.Context, report *ReconcileReport, opts ReconcileOptions, cutoff time.Time,
) error {
	active := map[string]struct{}{}
	rows, err := s.nodes.DB().Pool().Query(ctx, `SELECT id FROM uploads`)
	if err != nil {
		return fmt.Errorf("files: read upload sessions: %w", err)
	}
	for rows.Next() {
		var id [16]byte
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("files: scan upload id: %w", err)
		}
		active[hexNoDashes(id)] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("files: read upload sessions: %w", err)
	}

	entries, err := fs.ReadDir(s.storage.Dir().FS(), config.DirTemp)
	if err != nil {
		return fmt.Errorf("files: read temp directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if _, live := active[entry.Name()]; live {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}

		report.StaleTempFiles = append(report.StaleTempFiles, entry.Name())
		if opts.Repair {
			if err := s.storage.RemoveTemp(entry.Name()); err != nil {
				s.logger.Warn("could not remove stale temporary file", "name", entry.Name(), "error", err)
				continue
			}
			report.BytesReclaimed += info.Size()
		}
	}
	return nil
}

// verifyRefCounts compares each blob's stored count against the real number of
// referencing nodes.
func (s *Service) verifyRefCounts(ctx context.Context, report *ReconcileReport, opts ReconcileOptions) error {
	// Both kinds of reference: a node pointing at the content, and a version of
	// a file that used to. Counting only nodes would report every versioned
	// file as an error — and, under repair, would "fix" the count downward and
	// hand still-referenced bytes to the sweeper.
	rows, err := s.nodes.DB().Pool().Query(ctx, `
		SELECT b.hash, b.ref_count, live.total
		FROM blobs b
		CROSS JOIN LATERAL (
			SELECT (SELECT count(*) FROM nodes n WHERE n.content_hash = b.hash)
			     + (SELECT count(*) FROM versions v WHERE v.content_hash = b.hash) AS total
		) live
		WHERE b.ref_count <> live.total`)
	if err != nil {
		return fmt.Errorf("files: verify reference counts: %w", err)
	}
	defer rows.Close()

	type fix struct {
		hash   string
		actual int
	}
	var fixes []fix

	for rows.Next() {
		var hash string
		var stored, actual int
		if err := rows.Scan(&hash, &stored, &actual); err != nil {
			return fmt.Errorf("files: scan reference count: %w", err)
		}
		report.RefCountErrors++
		s.logger.Warn("blob reference count is wrong",
			"digest", hash[:12], "stored", stored, "actual", actual)
		fixes = append(fixes, fix{hash: hash, actual: actual})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("files: verify reference counts: %w", err)
	}

	if opts.Repair {
		for _, f := range fixes {
			if _, err := s.nodes.DB().Pool().Exec(ctx,
				`UPDATE blobs SET ref_count = $2 WHERE hash = $1`, f.hash, f.actual); err != nil {
				s.logger.Warn("could not repair reference count", "digest", f.hash[:12], "error", err)
			}
		}
	}
	return nil
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func hexNoDashes(id [16]byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 32)
	for i, b := range id {
		out[i*2] = hexDigits[b>>4]
		out[i*2+1] = hexDigits[b&0x0f]
	}
	return string(out)
}
