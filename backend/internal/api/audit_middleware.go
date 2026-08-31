package api

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/audit"
)

// Recording an audit event from a handler.
//
// Everything an event needs about *who* and *from where* is already in the
// request, so the helpers below take it from there rather than making each call
// site remember to pass six fields — a call site that forgets the IP produces a
// record that cannot answer the question it exists for.

// auditEvent builds an event pre-filled from the request.
func (s *Server) auditEvent(r *http.Request, action string, outcome audit.Outcome) audit.Event {
	e := audit.Event{
		Action:    action,
		Outcome:   outcome,
		IP:        ClientIPFromContext(r.Context()),
		UserAgent: r.UserAgent(),
		RequestID: RequestIDFromContext(r.Context()),
	}

	// The actor's name is captured as well as their identifier, because
	// deleting an account must not render its history anonymous.
	if user := UserFromContext(r.Context()); user != nil {
		id := user.ID
		e.ActorID = &id
		e.ActorLabel = user.Username
	}
	return e
}

// audit records an event, if the trail is enabled.
//
// Never returns an error and never fails a request: an audit write that could
// fail the operation would turn a full disk into an outage, and a caller
// tempted to skip the record is worse than a gap. Gaps are loud in the log.
func (s *Server) audit(
	r *http.Request, action string, outcome audit.Outcome, adjust ...func(*audit.Event),
) {
	if s.auditLog == nil {
		return
	}
	e := s.auditEvent(r, action, outcome)
	for _, fn := range adjust {
		fn(&e)
	}
	s.auditLog.Record(r.Context(), e)
}

// auditTarget fills in what an event was done to.
func auditTarget(id uuid.UUID, kind, label string) func(*audit.Event) {
	return func(e *audit.Event) {
		if id != uuid.Nil {
			target := id
			e.TargetID = &target
		}
		e.TargetType = kind
		e.TargetLabel = label
	}
}

// auditDetail attaches extra fields.
//
// The caller is responsible for what goes in here, and the rule admits no
// exception: never a password, a token, a hash, or anything derived from one.
func auditDetail(detail map[string]any) func(*audit.Event) {
	return func(e *audit.Event) { e.Detail = detail }
}

// auditActor overrides the actor, for events where the request has no session
// yet — a failed sign-in names an account the caller has not authenticated as.
func auditActor(id *uuid.UUID, label string) func(*audit.Event) {
	return func(e *audit.Event) {
		e.ActorID = id
		e.ActorLabel = label
	}
}
