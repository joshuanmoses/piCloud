// Package maintenance holds the recurring housekeeping jobs.
//
// Everything here runs on the background queue rather than on a bare
// time.Ticker, which buys three things: the work survives a restart, two
// instances cannot run the same sweep concurrently, and an administrator can
// see in the job table what ran, when, and whether it failed.
//
// Each handler is written to be interruptible and to bound its own work. A
// sweep that tried to purge 400,000 trashed files in one transaction would
// hold locks for minutes on a Raspberry Pi; instead each run does a bounded
// batch and re-queues itself if there is more.
package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/joshuanmoses/piCloud/backend/internal/audit"
	"github.com/joshuanmoses/piCloud/backend/internal/config"
	"github.com/joshuanmoses/piCloud/backend/internal/files"
	"github.com/joshuanmoses/piCloud/backend/internal/jobs"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
	"github.com/joshuanmoses/piCloud/backend/internal/previews"
	"github.com/joshuanmoses/piCloud/backend/internal/publiclinks"
	"github.com/joshuanmoses/piCloud/backend/internal/recent"
	"github.com/joshuanmoses/piCloud/backend/internal/sessions"
	"github.com/joshuanmoses/piCloud/backend/internal/storage"
	"github.com/joshuanmoses/piCloud/backend/internal/versions"
)

// Job kinds. Each is also its own dedupe key, since only one of each should
// ever be queued at a time.
const (
	KindPurgeTrash       = "maintenance.purge_trash"
	KindExpireSessions   = "maintenance.expire_sessions"
	KindExpireGrants     = "maintenance.expire_grants"
	KindExpireLinks      = "maintenance.expire_links"
	KindSweepUploads     = "maintenance.sweep_uploads"
	KindPruneRecent      = "maintenance.prune_recent"
	KindCleanPreviews    = "maintenance.clean_previews"
	KindRequeuePreviews  = "maintenance.requeue_previews"
	KindReconcileStorage = "maintenance.reconcile_storage"
	KindPruneJobs        = "maintenance.prune_jobs"
	KindPruneVersions    = "maintenance.prune_versions"
	KindPruneAudit       = "maintenance.prune_audit"
)

// Batch bounds one run of a sweep. Small enough that a run finishes in well
// under a second on a Pi, large enough that a backlog drains in minutes.
const Batch = 200

// Retention windows for records that are kept past their usefulness so an
// administrator can still see what happened.
const (
	// SessionRetention keeps ended sessions visible in the session list.
	SessionRetention = 7 * 24 * time.Hour
	// LinkRetention keeps expired public links listable, so their owner can
	// see that a link expired rather than wondering where it went.
	LinkRetention = 30 * 24 * time.Hour
	// RecentRetention bounds the activity history.
	RecentRetention = 90 * 24 * time.Hour
	// JobRetention keeps finished jobs for the administrator's job view.
	JobRetention = 3 * 24 * time.Hour
	// StalledPreviewAge is how long a preview may sit pending before it is
	// assumed lost to a crash and re-queued.
	StalledPreviewAge = 30 * time.Minute
)

// Deps are the stores the handlers operate on. Optional ones may be nil, in
// which case their handler is not registered — a deployment with sharing
// switched off has no link expiry to run.
type Deps struct {
	Config      *config.Config
	Logger      *slog.Logger
	Nodes       *nodes.Store
	Files       *files.Service
	Sessions    *sessions.Store
	Permissions *permissions.Store
	PublicLinks *publiclinks.Store
	Recent      *recent.Store
	Previews    *previews.Service
	Versions    *versions.Service
	Audit       *audit.Store
	Storage     *storage.Storage
	Queue       *jobs.Store
}

// Service runs the housekeeping handlers.
type Service struct {
	deps   Deps
	logger *slog.Logger
}

// New wires the housekeeping service.
func New(deps Deps) *Service {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{deps: deps, logger: logger.With("component", "maintenance")}
}

// Schedule describes when a recurring job should next run.
type Schedule struct {
	Kind     string
	Interval time.Duration
	// Enabled is false when the feature the job maintains is switched off.
	Enabled bool
}

// Schedules returns the recurring work this deployment needs.
//
// Intervals are deliberately unsynchronised — 11 minutes, 17 minutes, 23 —
// so the sweeps do not all fire in the same second and produce a periodic
// stall on a single-board computer.
func (s *Service) Schedules() []Schedule {
	cfg := s.deps.Config
	return []Schedule{
		{KindExpireSessions, 17 * time.Minute, s.deps.Sessions != nil},
		{KindExpireGrants, 23 * time.Minute, s.deps.Permissions != nil},
		{KindExpireLinks, 29 * time.Minute, s.deps.PublicLinks != nil},
		{KindSweepUploads, 11 * time.Minute, s.deps.Files != nil},
		{KindPurgeTrash, 6 * time.Hour, s.deps.Nodes != nil && cfg != nil && cfg.Trash.RetentionDays > 0},
		{KindPruneRecent, 24 * time.Hour, s.deps.Recent != nil},
		{KindRequeuePreviews, 31 * time.Minute, s.deps.Previews != nil},
		{KindCleanPreviews, 12 * time.Hour, s.deps.Previews != nil && s.deps.Storage != nil},
		{KindPruneJobs, 6 * time.Hour, s.deps.Queue != nil},
		{KindPruneVersions, 4 * time.Hour, s.deps.Versions != nil && s.deps.Versions.Enabled()},
		{KindPruneAudit, 12 * time.Hour, s.deps.Audit != nil && cfg != nil && cfg.Audit.Retention > 0},
		{KindReconcileStorage, 24 * time.Hour, s.deps.Files != nil},
	}
}

// Register attaches every applicable handler to a runner.
func (s *Service) Register(r *jobs.Runner) {
	for _, sched := range s.Schedules() {
		if !sched.Enabled {
			continue
		}
		switch sched.Kind {
		case KindExpireSessions:
			r.Register(sched.Kind, s.expireSessions)
		case KindExpireGrants:
			r.Register(sched.Kind, s.expireGrants)
		case KindExpireLinks:
			r.Register(sched.Kind, s.expireLinks)
		case KindSweepUploads:
			r.Register(sched.Kind, s.sweepUploads)
		case KindPurgeTrash:
			r.Register(sched.Kind, s.purgeTrash)
		case KindPruneRecent:
			r.Register(sched.Kind, s.pruneRecent)
		case KindRequeuePreviews:
			r.Register(sched.Kind, s.requeuePreviews)
		case KindCleanPreviews:
			r.Register(sched.Kind, s.cleanPreviews)
		case KindPruneJobs:
			r.Register(sched.Kind, s.pruneJobs)
		case KindPruneVersions:
			r.Register(sched.Kind, s.pruneVersions)
		case KindPruneAudit:
			r.Register(sched.Kind, s.pruneAudit)
		case KindReconcileStorage:
			r.Register(sched.Kind, s.reconcileStorage)
		}
	}
}

// EnqueueAll queues one round of every enabled sweep.
//
// Called at startup and then on each interval by the scheduler. Because each
// kind is its own dedupe key, calling it while a sweep is still queued is a
// no-op rather than a pile-up.
func (s *Service) EnqueueAll(ctx context.Context) {
	for _, sched := range s.Schedules() {
		if !sched.Enabled {
			continue
		}
		s.enqueue(ctx, sched.Kind, 0)
	}
}

// enqueue queues one sweep, tolerating the duplicate case.
func (s *Service) enqueue(ctx context.Context, kind string, delay time.Duration) {
	params := jobs.EnqueueParams{
		Kind:      kind,
		DedupeKey: kind,
		// Housekeeping runs above thumbnails and below anything a person is
		// waiting on.
		Priority:    -5,
		MaxAttempts: 3,
	}
	if delay > 0 {
		params.RunAfter = time.Now().Add(delay)
	}

	if _, err := s.deps.Queue.Enqueue(ctx, params); err != nil && !errors.Is(err, jobs.ErrDuplicate) {
		s.logger.Warn("cannot queue a maintenance sweep", "kind", kind, "error", err)
	}
}

// again re-queues the current sweep immediately, for when a batch filled up and
// there is more to do.
func (s *Service) again(ctx context.Context, kind string) {
	// A detached context: the job's own context is about to be cancelled as it
	// completes, and the follow-up must still be recorded.
	s.enqueue(context.WithoutCancel(ctx), kind, time.Second)
}

// --- Handlers -------------------------------------------------------------

func (s *Service) expireSessions(ctx context.Context, _ *jobs.Job) error {
	n, err := s.deps.Sessions.DeleteExpired(ctx, SessionRetention)
	if err != nil {
		return err
	}
	if n > 0 {
		s.logger.Info("expired sessions removed", "count", n)
	}
	return nil
}

func (s *Service) expireGrants(ctx context.Context, _ *jobs.Job) error {
	n, err := s.deps.Permissions.DeleteExpired(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		s.logger.Info("expired share grants removed", "count", n)
	}
	return nil
}

func (s *Service) expireLinks(ctx context.Context, _ *jobs.Job) error {
	n, err := s.deps.PublicLinks.DeleteExpired(ctx, LinkRetention)
	if err != nil {
		return err
	}
	if n > 0 {
		s.logger.Info("expired public links removed", "count", n)
	}
	return nil
}

func (s *Service) sweepUploads(ctx context.Context, _ *jobs.Job) error {
	n, err := s.deps.Files.Uploads().SweepExpired(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		s.logger.Info("abandoned uploads discarded", "count", n)
	}
	return nil
}

func (s *Service) pruneRecent(ctx context.Context, _ *jobs.Job) error {
	n, err := s.deps.Recent.DeleteOlderThan(ctx, RecentRetention)
	if err != nil {
		return err
	}
	if n > 0 {
		s.logger.Info("old activity history pruned", "count", n)
	}
	return nil
}

func (s *Service) pruneJobs(ctx context.Context, _ *jobs.Job) error {
	n, err := s.deps.Queue.DeleteFinished(ctx, JobRetention)
	if err != nil {
		return err
	}
	if n > 0 {
		s.logger.Debug("finished job records pruned", "count", n)
	}
	return nil
}

// pruneVersions applies the version retention policy.
//
// The second sweep that destroys user data, and treated with the same caution
// as the trash purge: a policy with no limits set prunes nothing, the batch is
// bounded, and what was removed is logged with the reason.
func (s *Service) pruneVersions(ctx context.Context, _ *jobs.Job) error {
	removed, err := s.deps.Versions.Prune(ctx, Batch)
	if err != nil {
		return err
	}
	if s.deps.Versions.MoreToPrune(removed, Batch) {
		s.again(ctx, KindPruneVersions)
	}
	return nil
}

// pruneAudit applies the audit retention policy.
//
// The retention window is an operator's decision rather than a default the
// software imposes: in some places discarding this is the problem, and in
// others keeping it is. A retention of zero means "keep everything", and the
// store refuses to run rather than reading it as "delete everything".
func (s *Service) pruneAudit(ctx context.Context, _ *jobs.Job) error {
	retention := s.deps.Config.Audit.Retention
	if retention <= 0 {
		return nil
	}

	n, err := s.deps.Audit.DeleteOlderThan(ctx, retention)
	if err != nil {
		return err
	}
	if n > 0 {
		s.logger.Info("audit events pruned past their retention",
			"count", n, "retention", retention.String())
	}
	return nil
}

func (s *Service) requeuePreviews(ctx context.Context, _ *jobs.Job) error {
	n, err := s.deps.Previews.Requeue(ctx, StalledPreviewAge, Batch)
	if err != nil {
		return err
	}
	if n > 0 {
		s.logger.Info("stalled previews re-queued", "count", n)
	}
	return nil
}

// cleanPreviews removes thumbnails whose source content no longer exists.
//
// Previews are keyed on the content digest, so a thumbnail is only orphaned
// once *no* live node holds that content — two users with identical images
// share one thumbnail, and one of them deleting theirs must not break the
// other's.
func (s *Service) cleanPreviews(ctx context.Context, _ *jobs.Job) error {
	orphans, err := s.deps.Previews.Store().OrphanedHashes(ctx, Batch)
	if err != nil {
		return err
	}

	removed := 0
	for _, digest := range orphans {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.deps.Storage.RemovePreview(digest); err != nil {
			// A preview we cannot delete wastes a few kilobytes. Log it and
			// carry on rather than abandoning the rest of the batch.
			s.logger.Warn("cannot remove an orphaned preview", "error", err)
			continue
		}
		removed++
	}

	if removed > 0 {
		s.logger.Info("orphaned previews removed", "count", removed)
	}
	if len(orphans) == Batch {
		s.again(ctx, KindCleanPreviews)
	}
	return nil
}

// purgeTrash permanently removes content deleted longer ago than the retention
// window.
//
// This is the one sweep that destroys user data, so it is deliberately
// conservative: it only ever touches nodes whose deleted_at is older than the
// configured window, it works in bounded batches, and it logs what it removed.
func (s *Service) purgeTrash(ctx context.Context, _ *jobs.Job) error {
	days := s.deps.Config.Trash.RetentionDays
	if days <= 0 {
		// Retention was switched off after this job was queued. Purging
		// everything would be catastrophic and is obviously not what the
		// administrator meant.
		return nil
	}
	cutoff := time.Duration(days) * 24 * time.Hour

	expired, err := s.deps.Nodes.ExpiredTrash(ctx, cutoff, Batch)
	if err != nil {
		return err
	}

	purged, freed := 0, 0
	for _, id := range expired {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := s.deps.Nodes.Purge(ctx, id)
		if err != nil {
			if errors.Is(err, nodes.ErrNotFound) {
				// Purged by its owner in the meantime.
				continue
			}
			s.logger.Error("cannot purge an expired trash item", "node_id", id, "error", err)
			continue
		}
		purged += result.NodesDeleted
		freed += len(result.UnreferencedBlobs)
	}

	if purged > 0 {
		s.logger.Info("trash purged after retention period",
			"nodes", purged, "unreferenced_blobs", freed, "retention_days", days)
	}
	if len(expired) == Batch {
		s.again(ctx, KindPurgeTrash)
	}
	return nil
}

// reconcileStorage checks that metadata and bytes agree.
//
// Read-only by default. Deleting a file because a nightly sweep believed it was
// unreferenced is precisely the failure that must never happen, so the sweep
// reports and an administrator decides.
func (s *Service) reconcileStorage(ctx context.Context, _ *jobs.Job) error {
	report, err := s.deps.Files.Reconcile(ctx, files.ReconcileOptions{Repair: false})
	if err != nil {
		return err
	}
	if report.HasProblems() {
		s.logger.Warn("storage reconciliation found discrepancies",
			"report", fmt.Sprintf("%+v", report),
			"hint", "run 'pifiles reconcile' to review and repair")
	}
	return nil
}
