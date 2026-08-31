// Package nodes owns the user-visible filesystem tree.
//
// Files and folders are one type with a Kind discriminator. Permissions,
// sharing, trash, and search therefore have a single target type rather than
// two implementations that drift apart.
//
// Nothing in this package builds a filesystem path from a node's name. Names
// live in the database; content lives on disk under its own digest. See
// docs/architecture.md.
package nodes

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Errors returned by this package.
var (
	// ErrNotFound means no such node, or none the caller may see. The two are
	// deliberately indistinguishable.
	ErrNotFound = errors.New("nodes: not found")
	// ErrNameTaken means a live sibling already uses that name.
	ErrNameTaken = errors.New("nodes: a file or folder with that name already exists here")
	// ErrNotAFolder means the target of an operation must be a folder.
	ErrNotAFolder = errors.New("nodes: target is not a folder")
	// ErrNotAFile means the target must be a file.
	ErrNotAFile = errors.New("nodes: target is not a file")
	// ErrCycle means a move would place a folder inside itself.
	ErrCycle = errors.New("nodes: a folder cannot be moved into itself")
	// ErrRootImmutable means the operation is not valid on a user's root.
	ErrRootImmutable = errors.New("nodes: the root folder cannot be renamed, moved, or deleted")
	// ErrQuotaExceeded means the write would exceed the owner's allowance.
	ErrQuotaExceeded = errors.New("nodes: storage quota exceeded")
	// ErrNotDeleted means a restore was attempted on a node that is not in Trash.
	ErrNotDeleted = errors.New("nodes: node is not in the trash")
	// ErrFolderNotEmpty means the operation requires an empty folder.
	ErrFolderNotEmpty = errors.New("nodes: folder is not empty")
)

// Kind distinguishes a file from a folder.
type Kind string

const (
	KindFile   Kind = "file"
	KindFolder Kind = "folder"
)

// Valid reports whether k is a recognised kind.
func (k Kind) Valid() bool { return k == KindFile || k == KindFolder }

// Node is a file or a folder.
type Node struct {
	ID      uuid.UUID `json:"id"`
	OwnerID uuid.UUID `json:"owner_id"`
	// ParentID is nil for a user's root folder.
	ParentID *uuid.UUID `json:"parent_id"`
	Kind     Kind       `json:"kind"`
	Name     string     `json:"name"`

	SizeBytes int64  `json:"size_bytes"`
	MimeType  string `json:"mime_type,omitempty"`
	// ContentHash is the address of the stored bytes. It is deliberately not
	// serialized: it is an internal storage detail, and exposing it would let a
	// client confirm whether particular content exists on the server.
	ContentHash *string `json:"-"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`

	CreatedBy  *uuid.UUID `json:"created_by,omitempty"`
	ModifiedBy *uuid.UUID `json:"modified_by,omitempty"`
}

// IsFolder reports whether the node is a folder.
func (n *Node) IsFolder() bool { return n.Kind == KindFolder }

// IsFile reports whether the node is a file.
func (n *Node) IsFile() bool { return n.Kind == KindFile }

// IsRoot reports whether the node is a user's root folder.
func (n *Node) IsRoot() bool { return n.ParentID == nil }

// IsDeleted reports whether the node is in Trash.
func (n *Node) IsDeleted() bool { return n.DeletedAt != nil }

// Breadcrumb is one ancestor in a path, for navigation.
type Breadcrumb struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	// IsRoot marks the entry the interface should label "My Files".
	IsRoot bool `json:"is_root"`
}

// --- Name validation ------------------------------------------------------

// MaxNameLength matches the database constraint.
const MaxNameLength = 255

// NameError describes a rejected name, with a message written for the user.
type NameError struct{ Reason string }

func (e *NameError) Error() string { return e.Reason }

// reservedNames are meaningless or hazardous as filenames. The Windows device
// names are included because a file called CON or LPT1 breaks Windows clients
// syncing or extracting a downloaded archive, even though PiFiles itself never
// creates a path from a name.
var reservedNames = map[string]struct{}{
	"con": {}, "prn": {}, "aux": {}, "nul": {},
	"com1": {}, "com2": {}, "com3": {}, "com4": {}, "com5": {},
	"com6": {}, "com7": {}, "com8": {}, "com9": {},
	"lpt1": {}, "lpt2": {}, "lpt3": {}, "lpt4": {}, "lpt5": {},
	"lpt6": {}, "lpt7": {}, "lpt8": {}, "lpt9": {},
}

// NormalizeName trims surrounding whitespace.
//
// The name is otherwise preserved exactly as typed. Unicode normalisation is
// deliberately not applied: the name never becomes a filesystem path, so the
// NFC/NFD problem that plagues path-based servers does not arise, and altering
// what somebody typed would be surprising.
func NormalizeName(name string) string {
	return strings.Trim(name, " \t\r\n")
}

// ValidateName enforces the same rules as the database constraint, so a bad
// name produces a clear message rather than a constraint violation.
//
// None of these rules protect the filesystem — content addressing already does
// that. They exist so names render correctly in a browser, survive a round trip
// through a ZIP archive, and cannot be used to spoof interface text.
func ValidateName(name string) error {
	if name == "" {
		return &NameError{"Enter a name."}
	}
	if !utf8.ValidString(name) {
		return &NameError{"Name contains invalid characters."}
	}
	if len(name) > MaxNameLength {
		return &NameError{fmt.Sprintf("Name must be no more than %d bytes.", MaxNameLength)}
	}
	if name != strings.Trim(name, " \t\r\n") {
		return &NameError{"Name must not begin or end with a space."}
	}
	if name == "." || name == ".." {
		return &NameError{`"." and ".." cannot be used as names.`}
	}

	for _, r := range name {
		switch r {
		case '/', '\\':
			return &NameError{"Name must not contain slashes."}
		case ':', '*', '?', '"', '<', '>', '|':
			return &NameError{`Name must not contain any of : * ? " < > |`}
		}
		if unicode.IsControl(r) {
			return &NameError{"Name must not contain control characters."}
		}
	}

	// Bidirectional override characters can make "report[RLO]fdp.exe" render as
	// "reportexe.pdf" — a classic disguise for a hostile attachment.
	for _, r := range name {
		switch r {
		case '‪', '‫', '‬', '‭', '‮',
			'⁦', '⁧', '⁨', '⁩':
			return &NameError{"Name must not contain text-direction override characters."}
		}
	}

	base := strings.ToLower(name)
	if dot := strings.IndexByte(base, '.'); dot > 0 {
		base = base[:dot]
	}
	if _, reserved := reservedNames[base]; reserved {
		return &NameError{fmt.Sprintf("%q is reserved by Windows and cannot be used as a name.", name)}
	}

	return nil
}

// Extension returns the lowercase extension including the dot, or "".
func Extension(name string) string { return strings.ToLower(path.Ext(name)) }

// BaseName returns the name without its extension.
func BaseName(name string) string {
	ext := path.Ext(name)
	return strings.TrimSuffix(name, ext)
}

// UniqueName derives a non-colliding name by appending a counter, the way a
// desktop file manager does: "Report.pdf" becomes "Report (2).pdf".
//
// taken reports whether a candidate is already in use.
func UniqueName(name string, taken func(string) bool) string {
	if !taken(name) {
		return name
	}

	base, ext := BaseName(name), path.Ext(name)

	// If the name already ends in " (n)", continue that sequence rather than
	// producing "Report (2) (2).pdf".
	start := 2
	if open := strings.LastIndex(base, " ("); open >= 0 && strings.HasSuffix(base, ")") {
		inner := base[open+2 : len(base)-1]
		if n, err := parsePositiveInt(inner); err == nil {
			base = base[:open]
			start = n + 1
		}
	}

	for i := start; i < start+10_000; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		// Truncate rather than exceed the length limit. Losing the tail of a
		// very long name is better than refusing the copy outright.
		if len(candidate) > MaxNameLength {
			overflow := len(candidate) - MaxNameLength
			if len(base) > overflow {
				base = base[:len(base)-overflow]
				candidate = fmt.Sprintf("%s (%d)%s", base, i, ext)
			}
		}
		if !taken(candidate) {
			return candidate
		}
	}

	// Astronomically unlikely; a unique suffix is still better than failing.
	return fmt.Sprintf("%s (%s)%s", base, uuid.NewString()[:8], ext)
}

func parsePositiveInt(s string) (int, error) {
	if s == "" || len(s) > 6 {
		return 0, errors.New("not a small positive integer")
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int(r-'0')
	}
	if n <= 0 {
		return 0, errors.New("not positive")
	}
	return n, nil
}
