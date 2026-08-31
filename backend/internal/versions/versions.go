// Package versions keeps the previous contents of a file.
//
// A version is a reference, not a copy. When a file is replaced the outgoing
// content already exists as a blob; the version row takes over the reference
// that the node was holding, and no bytes move. Copying would double the
// storage cost of every edit on a machine chosen for its modest disk, and it
// would be wrong besides: a blob shared by two nodes cannot be moved out from
// under one of them.
//
// That is also why STORAGE_ROOT/versions/ stays empty. Content addressing made
// a separate directory unnecessary rather than the design overlooking it.
package versions

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Errors returned by this package.
var (
	// ErrNotFound means no such version, or none the caller may see.
	ErrNotFound = errors.New("versions: not found")
	// ErrDisabled means version history is switched off for this deployment.
	ErrDisabled = errors.New("versions: version history is switched off on this server")
	// ErrNotAFile means the target is a folder, which has no content to version.
	ErrNotAFile = errors.New("versions: folders have no version history")
	// ErrNoContent means the file has never had content to snapshot.
	ErrNoContent = errors.New("versions: the file has no content")
)

// Reason explains why a version exists. It is shown to people, so the values
// are chosen to read as an explanation rather than an event code.
type Reason string

const (
	// ReasonReplaced is an ordinary overwrite: somebody uploaded new content.
	ReasonReplaced Reason = "replaced"
	// ReasonRestored marks the snapshot taken of the *current* content just
	// before an older version was restored over it.
	//
	// This exists so that restoring is never destructive. Restore v3 over v7
	// and v7 becomes a version too; the mistake is one more restore away from
	// being undone, rather than gone.
	ReasonRestored Reason = "restored"
)

// Valid reports whether r is a recognised reason.
func (r Reason) Valid() bool {
	return r == ReasonReplaced || r == ReasonRestored
}

// Describe renders a reason for a person.
func (r Reason) Describe() string {
	switch r {
	case ReasonRestored:
		return "Superseded by a restore"
	default:
		return "Replaced"
	}
}

// Version is one previous state of a file.
type Version struct {
	ID     uuid.UUID `json:"id"`
	NodeID uuid.UUID `json:"node_id"`
	// Number is monotonic per file, starting at 1. It does not renumber when
	// an older version is pruned: a number a person has seen must keep meaning
	// the same thing.
	Number int `json:"version"`

	// ContentHash is the address of the stored bytes. Deliberately not
	// serialized, for the same reason a node's is not: it would let a client
	// probe whether particular content exists on the server.
	ContentHash string    `json:"-"`
	OwnerID     uuid.UUID `json:"-"`

	SizeBytes int64  `json:"size_bytes"`
	MimeType  string `json:"mime_type,omitempty"`
	// Name is what the file was called at the time. A file renamed between
	// versions should show the name it had then.
	Name string `json:"name"`

	CreatedBy *uuid.UUID `json:"-"`
	CreatedAt time.Time  `json:"created_at"`
	Reason    Reason     `json:"reason"`
}

// Retention describes how much history to keep.
//
// Every limit is optional and any combination applies; a version is pruned when
// it falls outside *any* of them. Zero means unlimited, which is how the
// configuration expresses "keep everything".
type Retention struct {
	// MaxPerFile caps how many versions one file may keep.
	MaxPerFile int
	// MaxAge discards versions older than this.
	MaxAge time.Duration
	// MaxTotalBytes caps what one user's history may occupy in total.
	MaxTotalBytes int64
}

// Unlimited reports whether nothing would ever be pruned.
func (r Retention) Unlimited() bool {
	return r.MaxPerFile <= 0 && r.MaxAge <= 0 && r.MaxTotalBytes <= 0
}
