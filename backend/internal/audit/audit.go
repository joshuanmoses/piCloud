// Package audit records security-relevant events in a queryable trail.
//
// Deliberately separate from the structured log. A log answers "what did the
// software do", is rotated, and is read while debugging. An audit trail answers
// "who did this, and when" — a question somebody asks months later, under
// pressure, often about an account that no longer exists. The two need
// different retention and different access, so they are different stores.
//
// The one rule that admits no exception: nothing here ever holds a credential.
// There is no path in this package that writes a password, a session token, a
// link token, or a hash of any of them.
package audit

import (
	"time"

	"github.com/google/uuid"
)

// Outcome is whether the attempt succeeded.
//
// Failures are recorded as carefully as successes. A trail that held only what
// worked would be missing exactly the events that matter during an incident.
type Outcome string

const (
	// Success means the action completed.
	Success Outcome = "success"
	// Failure means it was attempted and did not work — a wrong password, a
	// name already taken.
	Failure Outcome = "failure"
	// Denied means the caller was refused on authorization grounds. Kept
	// distinct from Failure because a run of denials is a different signal
	// from a run of mistakes.
	Denied Outcome = "denied"
)

// Valid reports whether o is a recognised outcome.
func (o Outcome) Valid() bool {
	return o == Success || o == Failure || o == Denied
}

// Action identifiers.
//
// Dotted, namespaced by subsystem, and stable: these strings appear in an
// administrator's saved filters and in whatever they export, so renaming one
// silently breaks somebody's monitoring. Add rather than rename.
const (
	// Authentication.
	ActionLogin          = "auth.login"
	ActionLogout         = "auth.logout"
	ActionLoginFailed    = "auth.login_failed"
	ActionLockout        = "auth.lockout"
	ActionPasswordChange = "auth.password_change"
	ActionSessionRevoke  = "auth.session_revoke"
	ActionSetup          = "auth.setup"

	// Accounts, all administrator actions.
	ActionUserCreate  = "user.create"
	ActionUserUpdate  = "user.update"
	ActionUserDisable = "user.disable"
	ActionUserEnable  = "user.enable"
	ActionUserDelete  = "user.delete"
	ActionUserReset   = "user.password_reset"
	ActionQuotaChange = "user.quota_change"

	// Content. Reads are deliberately absent: recording every download on a
	// file server would drown the trail in noise and turn the audit table into
	// the largest thing in the database.
	ActionNodeRename  = "node.rename"
	ActionNodeMove    = "node.move"
	ActionNodeCopy    = "node.copy"
	ActionNodeDelete  = "node.delete"
	ActionNodePurge   = "node.purge"
	ActionNodeRestore = "node.restore"

	// Sharing and publication — the events most likely to be asked about.
	ActionShareGrant    = "share.grant"
	ActionShareDeny     = "share.deny"
	ActionShareRevoke   = "share.revoke"
	ActionLinkCreate    = "link.create"
	ActionLinkRevoke    = "link.revoke"
	ActionLinkAccess    = "link.access"
	ActionLinkUpload    = "link.upload"
	ActionPortalRefused = "link.refused"

	// Version history.
	ActionVersionRestore = "version.restore"
	ActionVersionDelete  = "version.delete"

	// Groups.
	ActionGroupCreate       = "group.create"
	ActionGroupDelete       = "group.delete"
	ActionGroupMemberAdd    = "group.member_add"
	ActionGroupMemberRemove = "group.member_remove"

	// Administration of the instance itself.
	ActionSettingChange = "admin.setting_change"
	ActionReconcile     = "admin.reconcile"
	ActionJobRetry      = "admin.job_retry"
)

// Event is one recorded action.
type Event struct {
	ID      uuid.UUID `json:"id"`
	Action  string    `json:"action"`
	Outcome Outcome   `json:"outcome"`

	// ActorID is nil for an anonymous actor — a public-link visitor, or a
	// failed sign-in naming an account that does not exist — and also for an
	// account that has since been deleted.
	ActorID *uuid.UUID `json:"actor_id,omitempty"`
	// ActorLabel is a readable name captured at the time. It exists precisely
	// so that deleting an account does not render its history anonymous.
	ActorLabel string `json:"actor_label,omitempty"`

	TargetID    *uuid.UUID `json:"target_id,omitempty"`
	TargetType  string     `json:"target_type,omitempty"`
	TargetLabel string     `json:"target_label,omitempty"`

	IP        string `json:"ip,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
	RequestID string `json:"request_id,omitempty"`

	// Detail carries whatever else the action needs. Never a credential.
	Detail map[string]any `json:"detail,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// Describe renders an action for a person.
//
// Kept here rather than in the interface so the vocabulary has one definition:
// an administrator reading the trail and an administrator reading an exported
// copy of it should see the same words.
func Describe(action string) string {
	if label, ok := actionLabels[action]; ok {
		return label
	}
	return action
}

var actionLabels = map[string]string{
	ActionLogin:          "Signed in",
	ActionLogout:         "Signed out",
	ActionLoginFailed:    "Sign-in failed",
	ActionLockout:        "Account locked out",
	ActionPasswordChange: "Password changed",
	ActionSessionRevoke:  "Session revoked",
	ActionSetup:          "Instance set up",

	ActionUserCreate:  "Account created",
	ActionUserUpdate:  "Account updated",
	ActionUserDisable: "Account disabled",
	ActionUserEnable:  "Account enabled",
	ActionUserDelete:  "Account deleted",
	ActionUserReset:   "Password reset by an administrator",
	ActionQuotaChange: "Quota changed",

	ActionNodeRename:  "Renamed",
	ActionNodeMove:    "Moved",
	ActionNodeCopy:    "Copied",
	ActionNodeDelete:  "Moved to trash",
	ActionNodePurge:   "Deleted permanently",
	ActionNodeRestore: "Restored from trash",

	ActionShareGrant:    "Shared",
	ActionShareDeny:     "Access taken away",
	ActionShareRevoke:   "Sharing revoked",
	ActionLinkCreate:    "Public link created",
	ActionLinkRevoke:    "Public link revoked",
	ActionLinkAccess:    "Public link opened",
	ActionLinkUpload:    "File submitted through a link",
	ActionPortalRefused: "Public link refused",

	ActionVersionRestore: "Version restored",
	ActionVersionDelete:  "Version deleted",

	ActionGroupCreate:       "Group created",
	ActionGroupDelete:       "Group deleted",
	ActionGroupMemberAdd:    "Added to a group",
	ActionGroupMemberRemove: "Removed from a group",

	ActionSettingChange: "Setting changed",
	ActionReconcile:     "Storage reconciled",
	ActionJobRetry:      "Background job retried",
}

// KnownActions returns every action identifier, so the interface can offer a
// filter without hardcoding its own copy of the list.
func KnownActions() []string {
	out := make([]string, 0, len(actionLabels))
	for action := range actionLabels {
		out = append(out, action)
	}
	return out
}
