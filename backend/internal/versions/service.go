package versions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/storage"
)

// Config controls version history.
type Config struct {
	Enabled   bool
	Retention Retention
}

// Service applies the version policy.
type Service struct {
	store   *Store
	nodes   *nodes.Store
	storage *storage.Storage
	cfg     Config
	logger  *slog.Logger
}

// NewService wires the parts together.
func NewService(store *Store, nodeStore *nodes.Store, st *storage.Storage, cfg Config, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:   store,
		nodes:   nodeStore,
		storage: st,
		cfg:     cfg,
		logger:  logger.With("component", "versions"),
	}
}

// Store exposes the record store for read-only callers.
func (s *Service) Store() *Store { return s.store }

// Enabled reports whether version history is switched on.
func (s *Service) Enabled() bool { return s.cfg.Enabled }

// Retention returns the configured policy.
func (s *Service) Retention() Retention { return s.cfg.Retention }

// --- nodes.Versioner ------------------------------------------------------

// SnapshotBeforeReplace records the outgoing content of a file being
// overwritten. Part of the caller's transaction.
func (s *Service) SnapshotBeforeReplace(
	ctx context.Context, tx pgx.Tx, node *nodes.Node, actorID uuid.UUID,
) error {
	_, err := s.store.Snapshot(ctx, tx, SnapshotParams{
		Node: node, Reason: ReasonReplaced, ActorID: actorID,
	})
	return err
}

// SnapshotBeforeRestore records the content a restore is about to displace.
//
// Without this, restoring an old version would throw away the current one, and
// a person who restored the wrong thing would have destroyed exactly what they
// were trying to protect.
func (s *Service) SnapshotBeforeRestore(
	ctx context.Context, tx pgx.Tx, node *nodes.Node, actorID uuid.UUID,
) error {
	_, err := s.store.Snapshot(ctx, tx, SnapshotParams{
		Node: node, Reason: ReasonRestored, ActorID: actorID,
	})
	return err
}

// --- Operations -----------------------------------------------------------

// List returns a file's history, newest first.
func (s *Service) List(ctx context.Context, nodeID uuid.UUID) ([]*Version, error) {
	if !s.cfg.Enabled {
		return nil, ErrDisabled
	}
	return s.store.ListForNode(ctx, nodeID)
}

// Get returns one version, confirming it belongs to the expected file.
//
// The node is part of the lookup rather than trusted from the version row,
// because the caller's permission was checked against the node. Looking up a
// version by identifier alone and then acting on whatever node it names would
// let a valid identifier reach a file the caller never had rights to.
func (s *Service) Get(ctx context.Context, nodeID, versionID uuid.UUID) (*Version, error) {
	if !s.cfg.Enabled {
		return nil, ErrDisabled
	}
	v, err := s.store.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if v.NodeID != nodeID {
		// Indistinguishable from a version that does not exist, because
		// saying "wrong file" would confirm the identifier names something.
		return nil, ErrNotFound
	}
	return v, nil
}

// Open streams a version's content.
func (s *Service) Open(v *Version) (io.ReadSeekCloser, int64, error) {
	if v == nil {
		return nil, 0, ErrNotFound
	}
	f, err := s.storage.OpenBlob(v.ContentHash)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			// Metadata says this content exists and the disk disagrees. Real,
			// and worth an operator's attention.
			s.logger.Error("version content is missing from storage",
				"version_id", v.ID, "node_id", v.NodeID, "content", v.ContentHash[:12])
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("versions: cannot stat content: %w", err)
	}
	return f, info.Size(), nil
}

// Restore points a file back at an earlier version's content.
func (s *Service) Restore(ctx context.Context, nodeID, versionID, actorID uuid.UUID) (*nodes.Node, error) {
	if !s.cfg.Enabled {
		return nil, ErrDisabled
	}

	v, err := s.Get(ctx, nodeID, versionID)
	if err != nil {
		return nil, err
	}

	// Refuse before touching anything if the bytes are gone. Restoring to
	// content that is not on disk would replace a working file with a broken
	// one, which is worse than refusing.
	if !s.storage.BlobExists(v.ContentHash) {
		s.logger.Error("cannot restore a version whose content is missing",
			"version_id", v.ID, "node_id", v.NodeID)
		return nil, ErrNotFound
	}

	restored, err := s.nodes.RestoreContent(ctx, nodes.RestoreContentParams{
		NodeID:      nodeID,
		ContentHash: v.ContentHash,
		SizeBytes:   v.SizeBytes,
		MimeType:    v.MimeType,
		ActorID:     actorID,
	})
	if err != nil {
		return nil, err
	}

	s.logger.Info("version restored",
		"node_id", nodeID, "version", v.Number, "actor_id", actorID)
	return restored, nil
}

// Delete removes one version.
func (s *Service) Delete(ctx context.Context, nodeID, versionID uuid.UUID) error {
	if !s.cfg.Enabled {
		return ErrDisabled
	}
	if _, err := s.Get(ctx, nodeID, versionID); err != nil {
		return err
	}
	return s.store.Delete(ctx, versionID)
}

// Usage reports how much a user's history occupies.
func (s *Service) Usage(ctx context.Context, ownerID uuid.UUID) (int64, int, error) {
	return s.store.UsageForOwner(ctx, ownerID)
}

// Prune applies the retention policy, returning how many versions were removed.
//
// Bounded per call and re-runnable: the sweep that drives it re-queues itself
// while there is more to do, so one pass never holds a transaction open long
// enough to matter on a Pi.
func (s *Service) Prune(ctx context.Context, limit int) (int, error) {
	if !s.cfg.Enabled {
		return 0, nil
	}

	// Orphans first: they belong to files that no longer exist, so they are
	// unambiguously reclaimable and their blob references are currently
	// holding bytes that nothing can reach.
	candidates, err := s.store.OrphanedForDeletedNodes(ctx, limit)
	if err != nil {
		return 0, err
	}

	if len(candidates) < limit && !s.cfg.Retention.Unlimited() {
		byPolicy, err := s.store.FindPrunable(ctx, s.cfg.Retention, limit-len(candidates))
		if err != nil {
			return 0, err
		}
		candidates = append(candidates, byPolicy...)
	}

	removed := 0
	var freed int64
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if err := s.store.Delete(ctx, c.ID); err != nil {
			if errors.Is(err, ErrNotFound) {
				// Removed by its owner in the meantime.
				continue
			}
			s.logger.Error("cannot prune a version",
				"version_id", c.ID, "reason", c.Why, "error", err)
			continue
		}
		removed++
		freed += c.Size
	}

	if removed > 0 {
		s.logger.Info("version history pruned",
			"versions", removed, "bytes", freed,
			"max_per_file", s.cfg.Retention.MaxPerFile,
			"max_age", s.cfg.Retention.MaxAge.String())
	}
	return removed, nil
}

// MoreToPrune reports whether the last pass filled its batch, so the caller
// knows to run again.
func (s *Service) MoreToPrune(removed, limit int) bool { return removed >= limit }
