package api

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/audit"
	"github.com/joshuanmoses/piCloud/backend/internal/jobs"
	"github.com/joshuanmoses/piCloud/backend/internal/users"
)

// The audit trail, the job queue, and the instance overview.
//
// All administrator-only. The audit trail in particular is: it names who did
// what, and a trail readable by everybody would be a directory of other
// people's activity.

// auditEventResponse is one recorded event as shown to an administrator.
type auditEventResponse struct {
	ID     uuid.UUID `json:"id"`
	Action string    `json:"action"`
	// Label is the action written for a person, supplied by the server so the
	// interface does not carry its own copy of the vocabulary.
	Label   string `json:"label"`
	Outcome string `json:"outcome"`

	Actor      *users.Public `json:"actor,omitempty"`
	ActorLabel string        `json:"actor_label,omitempty"`

	TargetID    *uuid.UUID `json:"target_id,omitempty"`
	TargetType  string     `json:"target_type,omitempty"`
	TargetLabel string     `json:"target_label,omitempty"`

	IP        string `json:"ip,omitempty"`
	Device    string `json:"device,omitempty"`
	RequestID string `json:"request_id,omitempty"`

	Detail map[string]any `json:"detail,omitempty"`

	CreatedAt string `json:"created_at"`
}

// handleAdminListAudit queries the trail.
func (s *Server) handleAdminListAudit(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	q := audit.Query{
		Action: strings.TrimSpace(query.Get("action")),
		Search: strings.TrimSpace(query.Get("q")),
		Limit:  parseIntDefault(query.Get("limit"), audit.DefaultLimit),
		Offset: parseIntDefault(query.Get("offset"), 0),
	}
	if outcome := audit.Outcome(query.Get("outcome")); outcome.Valid() {
		q.Outcome = outcome
	}
	if raw := query.Get("actor_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			WriteError(w, r, ErrBadRequest("That account identifier is not valid."))
			return
		}
		q.ActorID = &id
	}
	if raw := query.Get("target_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			WriteError(w, r, ErrBadRequest("That target identifier is not valid."))
			return
		}
		q.TargetID = &id
	}
	if since, ok := parseTimeParam(query.Get("since")); ok {
		q.Since = &since
	}
	if until, ok := parseTimeParam(query.Get("until")); ok {
		q.Until = &until
	}

	results, err := s.auditLog.List(r.Context(), q)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	// Actor profiles, batched. An event whose actor has since been deleted
	// keeps its label and simply has no profile — which is the whole reason
	// the label is stored alongside the identifier.
	ids := make([]uuid.UUID, 0, len(results.Events))
	seen := make(map[uuid.UUID]bool, len(results.Events))
	for _, e := range results.Events {
		if e.ActorID != nil && !seen[*e.ActorID] {
			seen[*e.ActorID] = true
			ids = append(ids, *e.ActorID)
		}
	}
	actors, err := s.userStore.GetManyPublic(r.Context(), ids)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	items := make([]auditEventResponse, 0, len(results.Events))
	for _, e := range results.Events {
		item := auditEventResponse{
			ID:          e.ID,
			Action:      e.Action,
			Label:       audit.Describe(e.Action),
			Outcome:     string(e.Outcome),
			ActorLabel:  e.ActorLabel,
			TargetID:    e.TargetID,
			TargetType:  e.TargetType,
			TargetLabel: e.TargetLabel,
			IP:          e.IP,
			Device:      describeUserAgent(e.UserAgent),
			RequestID:   e.RequestID,
			Detail:      e.Detail,
			CreatedAt:   e.CreatedAt.UTC().Format(timeFormat),
		}
		if e.ActorID != nil {
			if actor, ok := actors[*e.ActorID]; ok {
				item.Actor = &actor
			}
		}
		items = append(items, item)
	}

	WriteJSON(w, r, http.StatusOK, map[string]any{
		"events":   items,
		"total":    results.Total,
		"limit":    results.Limit,
		"offset":   results.Offset,
		"has_more": results.Offset+len(items) < results.Total,
	})
}

// handleAdminAuditVocabulary returns the action identifiers and their labels,
// so the filter in the interface is built from the server's list rather than a
// second copy that drifts.
func (s *Server) handleAdminAuditVocabulary(w http.ResponseWriter, r *http.Request) {
	known := audit.KnownActions()
	sort.Strings(known)

	type entry struct {
		Action string `json:"action"`
		Label  string `json:"label"`
	}
	items := make([]entry, 0, len(known))
	for _, action := range known {
		items = append(items, entry{Action: action, Label: audit.Describe(action)})
	}

	WriteJSON(w, r, http.StatusOK, map[string]any{
		"actions":  items,
		"outcomes": []string{string(audit.Success), string(audit.Failure), string(audit.Denied)},
	})
}

// --- Background jobs ---------------------------------------------------------

// handleAdminJobs reports the queue's state.
//
// Read-only apart from retry. An administrator needs to know that thumbnails
// have stopped generating, or that a sweep has been failing for a week, and
// without this the only evidence is in the log.
func (s *Server) handleAdminJobs(w http.ResponseWriter, r *http.Request) {
	stats, err := s.jobs.Stats(r.Context())
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	// The failures are what an administrator is actually looking for.
	failed, err := s.jobs.ListByStatus(r.Context(), jobs.StatusFailed, 50)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}
	running, err := s.jobs.ListByStatus(r.Context(), jobs.StatusRunning, 50)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	WriteJSON(w, r, http.StatusOK, map[string]any{
		"stats":   stats,
		"failed":  toJobResponses(failed),
		"running": toJobResponses(running),
	})
}

type jobResponse struct {
	ID          uuid.UUID `json:"id"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	Attempts    int       `json:"attempts"`
	MaxAttempts int       `json:"max_attempts"`
	// LastError is the handler's own message. Safe to show an administrator:
	// handlers are written not to put user content or secrets in an error.
	LastError string `json:"last_error,omitempty"`
	RunAfter  string `json:"run_after"`
	CreatedAt string `json:"created_at"`
}

func toJobResponses(list []*jobs.Job) []jobResponse {
	out := make([]jobResponse, 0, len(list))
	for _, j := range list {
		out = append(out, jobResponse{
			ID:          j.ID,
			Kind:        j.Kind,
			Status:      string(j.Status),
			Attempts:    j.Attempts,
			MaxAttempts: j.MaxAttempts,
			LastError:   j.LastError,
			RunAfter:    j.RunAfter.UTC().Format(timeFormat),
			CreatedAt:   j.CreatedAt.UTC().Format(timeFormat),
		})
	}
	return out
}

// handleAdminRetryJob puts a failed job back in the queue.
func (s *Server) handleAdminRetryJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	job, err := s.jobs.Retry(r.Context(), id)
	if err != nil {
		WriteError(w, r, translateJobError(err))
		return
	}

	s.audit(r, audit.ActionJobRetry, audit.Success,
		auditTarget(job.ID, "job", job.Kind))

	WriteJSON(w, r, http.StatusOK, map[string]any{"job": toJobResponses([]*jobs.Job{job})[0]})
}

func translateJobError(err error) error {
	switch {
	case err == jobs.ErrNotFound:
		return ErrNotFound()
	default:
		return ErrInternal(err)
	}
}

// --- Instance overview -------------------------------------------------------

// handleAdminOverview is the administration landing page's data.
//
// Everything here is about the *instance*, never about anybody's content: how
// many accounts, how much disk, how the queue is doing. There is deliberately
// no "recently uploaded files" or similar, because that would make the
// administration page a window into other people's work.
func (s *Server) handleAdminOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userCount, err := s.userStore.Count(ctx)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}
	adminCount, err := s.userStore.CountAdmins(ctx)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}
	liveSessions, err := s.sessionStore.CountLive(ctx)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	overview := map[string]any{
		"users": map[string]any{
			"total":          userCount,
			"administrators": adminCount,
		},
		"sessions":       map[string]any{"live": liveSessions},
		"storage":        s.storage.Check(),
		"database":       s.db.Check(ctx),
		"uptime_seconds": int64(time.Since(s.startedAt).Seconds()),
	}

	if s.jobs != nil {
		stats, err := s.jobs.Stats(ctx)
		if err != nil {
			WriteError(w, r, ErrInternal(err))
			return
		}
		overview["jobs"] = stats
	}

	if s.auditLog != nil {
		// A day's worth. Long enough to notice a run of failures, short enough
		// that the page loads on a Pi.
		summary, err := s.auditLog.Summarize(ctx, 24*time.Hour)
		if err != nil {
			WriteError(w, r, ErrInternal(err))
			return
		}
		overview["audit_24h"] = summary
	}

	WriteJSON(w, r, http.StatusOK, overview)
}

// auditEnabled reports whether the trail is available.
func (s *Server) auditEnabled() bool { return s.auditLog != nil && s.authEnabled() }

// adminEnabled reports whether the administration API can be served.
func (s *Server) adminEnabled() bool { return s.authEnabled() }
