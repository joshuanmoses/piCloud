package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/database"
)

// Store persists and queries the audit trail.
type Store struct {
	db     *database.DB
	logger *slog.Logger
}

// NewStore returns a Store backed by db.
func NewStore(db *database.DB, logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{db: db, logger: logger.With("component", "audit")}
}

// Limits on what one event may carry, so a hostile or buggy caller cannot
// write an unbounded row.
const (
	maxLabelLength  = 200
	maxAgentLength  = 300
	maxDetailLength = 4000
)

// Record writes an event.
//
// Failures are logged and swallowed. That is a deliberate trade and worth
// stating: an audit write that could fail a request would mean a full disk
// turns into an outage, and a caller tempted to skip the record "just this
// once" is a worse outcome than a gap. The gap is loud in the log.
//
// The caller must never pass a credential in Detail. Nothing here inspects the
// contents, because a filter that tried would give false confidence; the
// discipline lives at the call sites, which are few and reviewed.
func (s *Store) Record(ctx context.Context, e Event) {
	if e.Action == "" {
		s.logger.Error("audit event with no action was discarded")
		return
	}
	if !e.Outcome.Valid() {
		e.Outcome = Success
	}

	detail := json.RawMessage("{}")
	if len(e.Detail) > 0 {
		encoded, err := json.Marshal(e.Detail)
		if err != nil {
			s.logger.Warn("audit detail could not be encoded", "action", e.Action, "error", err)
		} else if len(encoded) > maxDetailLength {
			s.logger.Warn("audit detail was too large and has been dropped",
				"action", e.Action, "bytes", len(encoded))
		} else {
			detail = encoded
		}
	}

	// Detach the context. An audit record must survive the request being
	// cancelled — a client that hangs up mid-delete has still deleted the
	// file, and that is exactly the event worth keeping.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	_, err := s.db.Pool().Exec(writeCtx, `
		INSERT INTO audit_events
			(action, outcome, actor_id, actor_label, target_id, target_type,
			 target_label, ip, user_agent, request_id, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		e.Action, string(e.Outcome), e.ActorID, truncate(e.ActorLabel, maxLabelLength),
		e.TargetID, truncate(e.TargetType, 64), truncate(e.TargetLabel, maxLabelLength),
		truncate(e.IP, 64), truncate(e.UserAgent, maxAgentLength),
		truncate(e.RequestID, 64), detail)
	if err != nil {
		s.logger.Error("could not write an audit event",
			"action", e.Action, "outcome", e.Outcome, "error", err)
	}
}

// Query filters the trail.
type Query struct {
	// Action restricts to one action identifier.
	Action string
	// Outcome restricts to success, failure, or denied.
	Outcome Outcome
	// ActorID restricts to one person's actions.
	ActorID *uuid.UUID
	// TargetID restricts to everything done to one object.
	TargetID *uuid.UUID
	// Since and Until bound the window.
	Since *time.Time
	Until *time.Time
	// Search matches the actor and target labels, for "what happened to the
	// file called X" when the identifier is not to hand.
	Search string

	Limit  int
	Offset int
}

// Paging bounds. The trail is the largest table on a busy instance, so a
// listing is always bounded whatever the caller asks for.
const (
	DefaultLimit = 50
	MaxLimit     = 500
)

// Results is one page of the trail.
type Results struct {
	Events []Event `json:"events"`
	Total  int     `json:"total"`
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
}

const columns = `
	id, action, outcome, actor_id, actor_label, target_id, target_type,
	target_label, ip, user_agent, request_id, detail, created_at`

// List returns matching events, newest first.
func (s *Store) List(ctx context.Context, q Query) (*Results, error) {
	if q.Limit <= 0 || q.Limit > MaxLimit {
		q.Limit = DefaultLimit
	}
	if q.Offset < 0 {
		q.Offset = 0
	}

	var action *string
	if q.Action != "" {
		action = &q.Action
	}
	var outcome *string
	if q.Outcome.Valid() {
		o := string(q.Outcome)
		outcome = &o
	}
	var search *string
	if trimmed := strings.TrimSpace(q.Search); trimmed != "" {
		pattern := "%" + escapeLike(trimmed) + "%"
		search = &pattern
	}

	const filters = `
		WHERE ($1::text IS NULL OR action = $1)
		  AND ($2::text IS NULL OR outcome = $2)
		  AND ($3::uuid IS NULL OR actor_id = $3)
		  AND ($4::uuid IS NULL OR target_id = $4)
		  AND ($5::timestamptz IS NULL OR created_at >= $5)
		  AND ($6::timestamptz IS NULL OR created_at <= $6)
		  AND ($7::text IS NULL OR actor_label ILIKE $7 OR target_label ILIKE $7)`

	args := []any{action, outcome, q.ActorID, q.TargetID, q.Since, q.Until, search}

	var total int
	if err := s.db.Pool().QueryRow(ctx,
		`SELECT count(*) FROM audit_events`+filters, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("audit: count: %w", err)
	}

	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+columns+` FROM audit_events`+filters+`
		 ORDER BY created_at DESC, id DESC
		 LIMIT $8 OFFSET $9`,
		append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, fmt.Errorf("audit: list: %w", err)
	}
	defer rows.Close()

	out := &Results{
		Events: make([]Event, 0, q.Limit),
		Total:  total,
		Limit:  q.Limit,
		Offset: q.Offset,
	}
	for rows.Next() {
		var e Event
		var detail []byte
		if err := rows.Scan(
			&e.ID, &e.Action, &e.Outcome, &e.ActorID, &e.ActorLabel, &e.TargetID,
			&e.TargetType, &e.TargetLabel, &e.IP, &e.UserAgent, &e.RequestID,
			&detail, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("audit: scan: %w", err)
		}
		if len(detail) > 0 && string(detail) != "{}" {
			if err := json.Unmarshal(detail, &e.Detail); err != nil {
				// A row we cannot decode is still a row worth showing.
				s.logger.Warn("audit detail could not be decoded", "event_id", e.ID)
			}
		}
		out.Events = append(out.Events, e)
	}
	return out, rows.Err()
}

// Summary counts events by action over a window, for the administrator's
// overview.
type Summary struct {
	Action string `json:"action"`
	Label  string `json:"label"`
	Count  int    `json:"count"`
	Failed int    `json:"failed"`
	Denied int    `json:"denied"`
}

// Summarize returns per-action counts over the given window.
func (s *Store) Summarize(ctx context.Context, since time.Duration) ([]Summary, error) {
	rows, err := s.db.Pool().Query(ctx, `
		SELECT action,
		       count(*),
		       count(*) FILTER (WHERE outcome = 'failure'),
		       count(*) FILTER (WHERE outcome = 'denied')
		FROM audit_events
		WHERE created_at >= now() - $1::interval
		GROUP BY action
		ORDER BY count(*) DESC`, since)
	if err != nil {
		return nil, fmt.Errorf("audit: summarize: %w", err)
	}
	defer rows.Close()

	out := make([]Summary, 0, 32)
	for rows.Next() {
		var s Summary
		if err := rows.Scan(&s.Action, &s.Count, &s.Failed, &s.Denied); err != nil {
			return nil, fmt.Errorf("audit: scan summary: %w", err)
		}
		s.Label = Describe(s.Action)
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteOlderThan prunes the trail.
//
// Retention here is a policy decision an operator makes, not a default the
// software imposes: in some jurisdictions discarding this is the problem, and
// in others keeping it is. The configuration default keeps a year; zero keeps
// everything, and this refuses to run rather than reading zero as "delete all".
func (s *Store) DeleteOlderThan(ctx context.Context, age time.Duration) (int64, error) {
	if age <= 0 {
		return 0, fmt.Errorf("audit: pruning requires a positive retention period")
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM audit_events WHERE created_at < now() - $1::interval`, age)
	if err != nil {
		return 0, fmt.Errorf("audit: prune: %w", err)
	}
	return tag.RowsAffected(), nil
}

// truncate bounds a value at n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8Start(s[n]) {
		n--
	}
	return s[:n]
}

// utf8Start reports whether b begins a UTF-8 sequence.
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// escapeLike neutralises the wildcards in an ILIKE pattern, so a search for
// "100%" matches that text rather than everything.
func escapeLike(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}
