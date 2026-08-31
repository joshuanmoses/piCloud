package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/malware"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/storage"
	"github.com/joshuanmoses/piCloud/backend/internal/users"
)

// Errors returned by this service.
var (
	// ErrQuotaExceeded means the write would exceed the owner's allowance.
	ErrQuotaExceeded = errors.New("files: storage quota exceeded")
	// ErrTooLarge means the file exceeds the configured maximum upload size.
	ErrTooLarge = errors.New("files: file exceeds the maximum upload size")
	// ErrNoSpace means the storage volume is too full to accept the write.
	ErrNoSpace = errors.New("files: not enough free space on the server")
)

// QuotaError carries the numbers needed to explain a rejection.
type QuotaError struct {
	Required  int64
	Available int64
	Limit     int64
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("files: need %d bytes but only %d of %d remain",
		e.Required, e.Available, e.Limit)
}
func (e *QuotaError) Unwrap() error { return ErrQuotaExceeded }

// Config holds the limits the service enforces.
type Config struct {
	MaxUploadSize int64
	ChunkSize     int64
	UploadTTL     time.Duration
}

// Service coordinates storage, metadata, and quota.
//
// The invariant it exists to maintain: content reaches disk before metadata
// references it, and quota is charged in the same transaction as the metadata.
// A crash can therefore leave an unreferenced blob — recoverable — but never a
// file record whose bytes are missing.
type Service struct {
	nodes   *nodes.Store
	users   *users.Store
	uploads *UploadStore
	storage *storage.Storage
	cfg     Config
	logger  *slog.Logger

	// scanner is optional. Nil means no malware scanning, which is the
	// default: ClamAV needs about a gigabyte of RAM, and an operator should
	// choose to spend a quarter of a Pi's memory on it knowingly.
	scanner    *malware.Scanner
	quarantine bool
}

// SetScanner attaches malware scanning. Called once at startup.
//
// quarantine keeps infected content under quarantine/ rather than discarding
// it — off by default, because keeping malware on a family file server is a
// real risk for value most operators will never collect.
func (s *Service) SetScanner(scanner *malware.Scanner, quarantine bool) {
	s.scanner = scanner
	s.quarantine = quarantine
}

// Scanner exposes the scanner for the health endpoint.
func (s *Service) Scanner() *malware.Scanner { return s.scanner }

// NewService wires the service.
func NewService(
	nodeStore *nodes.Store,
	userStore *users.Store,
	uploadStore *UploadStore,
	store *storage.Storage,
	cfg Config,
	logger *slog.Logger,
) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		nodes: nodeStore, users: userStore, uploads: uploadStore,
		storage: store, cfg: cfg, logger: logger,
	}
}

// Nodes exposes the node store for read operations the API performs directly.
func (s *Service) Nodes() *nodes.Store { return s.nodes }

// Uploads exposes the upload session store.
func (s *Service) Uploads() *UploadStore { return s.uploads }

// Config returns the limits in force.
func (s *Service) Config() Config { return s.cfg }

// CheckQuota verifies that a user can store size additional bytes.
//
// Called before accepting content, so an over-quota upload is refused before
// the bytes are transferred rather than after.
func (s *Service) CheckQuota(ctx context.Context, ownerID uuid.UUID, size int64) error {
	if size > s.cfg.MaxUploadSize {
		return fmt.Errorf("%w: %d bytes exceeds the %d byte limit",
			ErrTooLarge, size, s.cfg.MaxUploadSize)
	}

	user, err := s.users.GetByID(ctx, ownerID)
	if err != nil {
		return fmt.Errorf("files: load user: %w", err)
	}
	if !user.CanStore(size) {
		limit := int64(0)
		if user.QuotaBytes != nil {
			limit = *user.QuotaBytes
		}
		return &QuotaError{Required: size, Available: user.RemainingQuota(), Limit: limit}
	}

	// The volume must also have room, independently of the user's allowance.
	// Filling the disk would break the database and every other user.
	ok, err := s.storage.HasFreeSpaceFor(size)
	if err != nil {
		s.logger.Warn("could not determine free space", "error", err)
	}
	if !ok {
		return ErrNoSpace
	}

	return nil
}

// UploadParams describes a single-request upload.
type UploadParams struct {
	OwnerID  uuid.UUID
	ParentID uuid.UUID
	Name     string
	// DeclaredSize is the client's Content-Length. It is used for the quota
	// pre-check only; the authoritative size is what actually arrives.
	DeclaredSize int64
	Content      io.Reader
	ActorID      uuid.UUID
	// ReplaceID names an existing file to overwrite in place.
	ReplaceID *uuid.UUID
}

// UploadResult reports a completed upload.
type UploadResult struct {
	Node *nodes.Node
	// Deduplicated is true when the content already existed on disk.
	Deduplicated bool
}

// Upload streams content to storage and records the file.
//
// The sequence is deliberate:
//
//  1. Pre-check the quota, so an over-quota upload is refused before transfer.
//  2. Stream to a temporary file, hashing as bytes arrive. Never buffered.
//  3. Re-check the quota against the *actual* size, since the declared size
//     was only a claim.
//  4. Link the content into place. It now exists at its final address.
//  5. Commit the metadata and charge the quota in one transaction.
//
// A failure at step 4 or 5 leaves an unreferenced blob, which the
// reconciliation job removes. The reverse — metadata without content — cannot
// happen, because the link precedes the commit.
func (s *Service) Upload(ctx context.Context, p UploadParams) (*UploadResult, error) {
	name := nodes.NormalizeName(p.Name)
	if err := nodes.ValidateName(name); err != nil {
		return nil, err
	}

	if p.DeclaredSize > 0 {
		if err := s.CheckQuota(ctx, p.OwnerID, p.DeclaredSize); err != nil {
			return nil, err
		}
	}

	tempName := uuid.NewString()
	writer, err := s.storage.NewTempWriter(tempName)
	if err != nil {
		return nil, fmt.Errorf("files: cannot begin upload: %w", err)
	}

	// Anything that leaves this function without a successful commit must not
	// leave a temporary file behind.
	committed := false
	defer func() {
		if !committed {
			if err := writer.Discard(); err != nil {
				s.logger.Warn("could not discard temporary upload", "error", err)
			}
		}
	}()

	// Read a header first so the type can be sniffed from content rather than
	// trusted from the client.
	header := make([]byte, 512)
	headerLen, err := io.ReadFull(p.Content, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("files: cannot read content: %w", err)
	}
	header = header[:headerLen]

	if _, err := writer.Write(header); err != nil {
		return nil, fmt.Errorf("files: cannot write content: %w", err)
	}

	remaining := s.cfg.MaxUploadSize - int64(headerLen)
	if remaining < 0 {
		remaining = 0
	}
	if _, err := writer.CopyFrom(p.Content, remaining); err != nil {
		if errors.Is(err, storage.ErrSizeMismatch) {
			return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, s.cfg.MaxUploadSize)
		}
		return nil, fmt.Errorf("files: cannot write content: %w", err)
	}

	actualSize := writer.Written()

	// The declared size was a claim; this is the real one.
	if err := s.CheckQuota(ctx, p.OwnerID, actualSize); err != nil {
		return nil, err
	}

	mimeType := DetectMimeType(header, name)

	// Scan before committing, so infected content never reaches the
	// content-addressed store and never gets a metadata row. The temporary
	// file must be flushed first: the scanner reads it back from disk.
	if err := writer.Sync(); err != nil {
		return nil, err
	}
	scan, err := s.scanTemp(ctx, tempName, actualSize)
	if err != nil {
		if errors.Is(err, ErrInfected) {
			s.quarantineTemp(tempName, writer.Digest())
			// Quarantine renamed the file away, so the deferred Discard would
			// fail on a path that no longer exists.
			committed = s.quarantine
		}
		return nil, err
	}
	if scan.Verdict != "" {
		s.logger.Debug("upload scanned", "verdict", scan.Verdict, "bytes", scan.Bytes)
	}

	existed := s.storage.BlobExists(writer.Digest())

	digest, size, err := writer.Commit()
	if err != nil {
		return nil, fmt.Errorf("files: cannot store content: %w", err)
	}
	committed = true

	node, err := s.nodes.CreateFile(ctx, nodes.CreateFileParams{
		OwnerID:     p.OwnerID,
		ParentID:    p.ParentID,
		Name:        name,
		ContentHash: digest,
		SizeBytes:   size,
		MimeType:    mimeType,
		ActorID:     p.ActorID,
		ReplaceID:   p.ReplaceID,
	})
	if err != nil {
		// The blob is now unreferenced. It is left for the reconciliation job
		// rather than unlinked here: another upload may have taken a reference
		// on the same content in the meantime.
		s.logger.Warn("upload committed to storage but metadata failed; blob is unreferenced",
			"digest", digest[:12], "error", err)
		return nil, err
	}

	s.logger.Info("file uploaded",
		"node_id", node.ID, "owner_id", p.OwnerID, "name", name,
		"size", size, "mime", mimeType, "deduplicated", existed)

	return &UploadResult{Node: node, Deduplicated: existed}, nil
}

// OpenContent opens a file's stored bytes for reading.
func (s *Service) OpenContent(node *nodes.Node) (io.ReadSeekCloser, int64, error) {
	if !node.IsFile() {
		return nil, 0, nodes.ErrNotAFile
	}
	if node.ContentHash == nil {
		return nil, 0, fmt.Errorf("files: node %s has no content", node.ID)
	}

	f, err := s.storage.OpenBlob(*node.ContentHash)
	if err != nil {
		return nil, 0, err
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("files: cannot stat content: %w", err)
	}

	// A mismatch means the database and the disk disagree. Serving the file
	// anyway would send a wrong Content-Length and corrupt the download.
	if info.Size() != node.SizeBytes {
		f.Close()
		s.logger.Error("stored size does not match metadata",
			"node_id", node.ID, "recorded", node.SizeBytes, "on_disk", info.Size())
		return nil, 0, fmt.Errorf("files: stored content size does not match metadata")
	}

	return f, info.Size(), nil
}

// Delete moves a node to Trash and releases its quota.
func (s *Service) Delete(ctx context.Context, id, actorID uuid.UUID) error {
	freed, err := s.nodes.SoftDelete(ctx, id, actorID)
	if err != nil {
		return err
	}
	s.logger.Info("node moved to trash", "node_id", id, "bytes_freed", freed)
	return nil
}

// Purge permanently deletes a node and unlinks any content that is now
// unreferenced.
func (s *Service) Purge(ctx context.Context, id uuid.UUID) error {
	result, err := s.nodes.Purge(ctx, id)
	if err != nil {
		return err
	}

	// The metadata is gone and committed. Releasing the content is safe now:
	// the reference count reached zero inside that transaction, so no live
	// node points at these blobs.
	//
	// The blob record is removed BEFORE the bytes, and only while its count is
	// still zero. That ordering matters twice over:
	//
	//   - The conditional delete is the guard against a concurrent upload that
	//     took a new reference between the purge transaction and now. If it
	//     did, the row survives and the bytes are left alone.
	//   - If the unlink then fails, what remains is a file with no record —
	//     an orphan the reconciliation job removes. The reverse order would
	//     leave a record with no bytes, which reconciliation correctly reports
	//     as data loss and refuses to repair.
	removed := 0
	for _, digest := range result.UnreferencedBlobs {
		tag, err := s.nodes.DB().Pool().Exec(ctx,
			`DELETE FROM blobs WHERE hash = $1 AND ref_count = 0`, digest)
		if err != nil {
			s.logger.Warn("could not remove blob record", "digest", digest[:12], "error", err)
			continue
		}
		if tag.RowsAffected() == 0 {
			// Something referenced this content again. Leave it alone.
			s.logger.Info("content was referenced again during purge; keeping it",
				"digest", digest[:12])
			continue
		}
		if err := s.storage.RemoveBlob(digest); err != nil {
			s.logger.Warn("blob record removed but content remains on disk; reconciliation will reclaim it",
				"digest", digest[:12], "error", err)
			continue
		}
		removed++
	}

	s.logger.Info("node permanently deleted",
		"node_id", id, "nodes", result.NodesDeleted, "blobs_removed", removed)
	return nil
}
