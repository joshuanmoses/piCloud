package previews

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/jobs"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/storage"
)

// JobKind is the queue kind this package handles.
const JobKind = "preview.generate"

// jobPayload is what an enqueued generation carries.
//
// Both the node and the content hash: the node says where to record the
// result, and the hash says which bytes to render. If the file is replaced
// between enqueue and execution, the hash no longer matches and the stale work
// is discarded instead of producing a thumbnail of the wrong content.
type jobPayload struct {
	NodeID      uuid.UUID `json:"node_id"`
	ContentHash string    `json:"content_hash"`
}

// Service generates and serves previews.
type Service struct {
	store   *Store
	nodes   *nodes.Store
	storage *storage.Storage
	queue   *jobs.Store
	logger  *slog.Logger
}

// NewService wires the parts together.
func NewService(store *Store, nodeStore *nodes.Store, st *storage.Storage, queue *jobs.Store, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: store, nodes: nodeStore, storage: st, queue: queue, logger: logger.With("component", "previews")}
}

// Store exposes the record store for callers that only read status.
func (s *Service) Store() *Store { return s.store }

// Request queues thumbnail generation for a file, if one is warranted.
//
// Safe to call on every upload and every overwrite: unsupported types are
// ignored, an already-current preview is left alone, and the queue deduplicates
// so a file saved ten times in a minute produces one job.
//
// Errors are returned but callers on the upload path should log rather than
// fail: a missing thumbnail is a cosmetic problem, and an upload that succeeded
// must not be reported as failed because a convenience could not be scheduled.
func (s *Service) Request(ctx context.Context, node *nodes.Node) error {
	if node == nil || node.Kind != nodes.KindFile || node.ContentHash == nil || *node.ContentHash == "" {
		return nil
	}
	if !Supportable(node.MimeType) {
		return nil
	}
	if node.SizeBytes > MaxSourceBytes {
		return nil
	}

	hash := *node.ContentHash

	// Already done for exactly this content, or already known impossible.
	existing, err := s.store.Get(ctx, node.ID)
	switch {
	case err == nil && existing.Fresh(hash) && existing.Status == StatusReady && s.storage.PreviewExists(hash):
		return nil
	case err == nil && existing.Fresh(hash) && existing.Status == StatusUnavailable:
		return nil
	case err != nil && !errors.Is(err, ErrNotFound):
		return err
	}

	if err := s.store.MarkPending(ctx, node.ID, hash); err != nil {
		return err
	}

	_, err = s.queue.Enqueue(ctx, jobs.EnqueueParams{
		Kind:    JobKind,
		Payload: jobPayload{NodeID: node.ID, ContentHash: hash},
		// Below the default, so a thumbnail never delays real work such as a
		// virus scan or a trash sweep.
		Priority: -10,
		// One pending job per (node, content). A different upload to the same
		// node gets a different key and so is not swallowed by the dedupe.
		DedupeKey: JobKind + ":" + node.ID.String() + ":" + hash,
		// Two attempts. A decode failure is permanent and stops immediately;
		// the retry exists for a transient disk error.
		MaxAttempts: 2,
	})
	if err != nil && !errors.Is(err, jobs.ErrDuplicate) {
		return err
	}
	return nil
}

// RegisterHandler attaches the generation handler to a runner.
func (s *Service) RegisterHandler(r *jobs.Runner) {
	r.Register(JobKind, s.handleGenerate)
}

// handleGenerate is the queue handler. It decodes the source, writes the
// thumbnail, and records the outcome.
func (s *Service) handleGenerate(ctx context.Context, job *jobs.Job) error {
	var p jobPayload
	if err := job.Decode(&p); err != nil {
		// A payload we cannot parse will never become parseable.
		return jobs.PermanentError(fmt.Errorf("previews: unreadable payload: %w", err))
	}
	if p.NodeID == uuid.Nil || p.ContentHash == "" {
		return jobs.PermanentError(errors.New("previews: payload names no node or content"))
	}

	node, err := s.nodes.Get(ctx, p.NodeID)
	if err != nil {
		if errors.Is(err, nodes.ErrNotFound) {
			// Deleted between enqueue and execution. Nothing to do, and
			// nothing wrong.
			return nil
		}
		return err
	}
	if node.ContentHash == nil || *node.ContentHash != p.ContentHash {
		// Replaced. A newer job was enqueued for the new content.
		return nil
	}

	src, err := s.storage.OpenBlob(p.ContentHash)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			// The metadata says there should be content and there is not. That
			// is a real problem, but not one a retry fixes.
			s.logger.Error("preview source is missing from storage",
				"node_id", p.NodeID, "content", p.ContentHash[:12])
			s.recordUnavailable(ctx, p, "source content is missing")
			return jobs.PermanentError(err)
		}
		return err
	}
	defer src.Close()

	thumb, err := Generate(src)
	if err != nil {
		if errors.Is(err, ErrUnsupported) || errors.Is(err, ErrTooLarge) {
			// A file that cannot be rendered is recorded as such, so the
			// interface stops asking and the queue stops trying. This is the
			// expected outcome for a file whose extension lied about its type.
			s.recordUnavailable(ctx, p, err.Error())
			return nil
		}
		return err
	}

	if err := s.storage.WritePreview(p.ContentHash, thumb.Data); err != nil {
		return err
	}
	if err := s.store.MarkReady(ctx, p.NodeID, p.ContentHash, thumb); err != nil {
		return err
	}

	s.logger.Debug("preview generated",
		"node_id", p.NodeID, "source", fmt.Sprintf("%dx%d", thumb.SourceWidth, thumb.SourceHeight),
		"thumbnail", fmt.Sprintf("%dx%d", thumb.Width, thumb.Height), "bytes", len(thumb.Data))
	return nil
}

// recordUnavailable notes a permanent failure. A failure to record it is logged
// and swallowed: the job outcome is already decided, and returning an error
// here would retry work that cannot succeed.
func (s *Service) recordUnavailable(ctx context.Context, p jobPayload, detail string) {
	if err := s.store.MarkUnavailable(ctx, p.NodeID, p.ContentHash, detail); err != nil {
		s.logger.Warn("cannot record that a preview is unavailable",
			"node_id", p.NodeID, "error", err)
	}
}

// Invalidate drops a node's preview record. Called when content is replaced or
// the node is destroyed.
//
// The thumbnail file itself is left for the cleanup job, because the digest may
// still be in use by another node holding identical content.
func (s *Service) Invalidate(ctx context.Context, nodeID uuid.UUID) error {
	return s.store.Delete(ctx, nodeID)
}

// Requeue re-enqueues previews that have been pending too long, which happens
// when the process dies between marking pending and finishing the job.
func (s *Service) Requeue(ctx context.Context, olderThan time.Duration, limit int) (int, error) {
	stale, err := s.store.PendingOlderThan(ctx, olderThan, limit)
	if err != nil {
		return 0, err
	}

	requeued := 0
	for _, rec := range stale {
		payload, err := json.Marshal(jobPayload{NodeID: rec.NodeID, ContentHash: rec.ContentHash})
		if err != nil {
			continue
		}
		_, err = s.queue.Enqueue(ctx, jobs.EnqueueParams{
			Kind:        JobKind,
			Payload:     json.RawMessage(payload),
			Priority:    -10,
			DedupeKey:   JobKind + ":" + rec.NodeID.String() + ":" + rec.ContentHash,
			MaxAttempts: 2,
		})
		if err == nil {
			requeued++
		} else if !errors.Is(err, jobs.ErrDuplicate) {
			s.logger.Warn("cannot requeue a stalled preview", "node_id", rec.NodeID, "error", err)
		}
	}
	return requeued, nil
}
