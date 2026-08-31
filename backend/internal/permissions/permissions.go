// Package permissions defines what a user may do to a node, and evaluates it.
//
// Authorization is kept separate from authentication throughout: this package
// knows nothing about how somebody proved who they are, only about what the
// resulting identity is allowed to touch.
package permissions

import (
	"fmt"
	"sort"
	"strings"
)

// Set is a bitmask of permissions.
//
// A bitmask rather than a list because effective access is the union of every
// grant that applies — directly, through an ancestor folder, and through each
// group the user belongs to. A bitwise OR computes that in one operation
// whether the user holds one grant or twenty.
type Set uint32

// The individual permissions.
//
// These are deliberately fine-grained. Coarse roles are built from them below,
// but the storage and the evaluation work at this level so that an unusual
// arrangement — "may upload but not see what is already here" for a drop box —
// is expressible without a new concept.
const (
	// Read allows seeing that an item exists and reading its metadata.
	// Everything else implies it; a grant without Read is meaningless.
	Read Set = 1 << iota
	// Download allows retrieving file content.
	Download
	// Upload allows adding new files to a folder.
	Upload
	// Create allows creating subfolders.
	Create
	// Modify allows replacing the content of an existing file.
	Modify
	// Rename allows changing an item's name.
	Rename
	// Move allows relocating an item.
	Move
	// Delete allows moving an item to Trash.
	Delete
	// Share allows granting access to others.
	Share
	// Manage allows changing who else has access.
	Manage
)

// None is the empty set.
const None Set = 0

// All is every permission. Held implicitly by a node's owner.
const All = Read | Download | Upload | Create | Modify | Rename | Move | Delete | Share | Manage

// Named roles, which is what the interface offers rather than ten checkboxes.
const (
	// Viewer can look but not change anything.
	RoleViewer = Read | Download
	// Contributor can add new content but not alter or remove what is there.
	// This is the "drop box" arrangement: useful for collecting submissions.
	RoleContributor = Read | Download | Upload | Create
	// Editor can do everything with the content itself, but cannot re-share it.
	//
	// Withholding Share is deliberate: sharing is not transitive by default,
	// or one share of one folder eventually reaches everyone.
	RoleEditor = Read | Download | Upload | Create | Modify | Rename | Move | Delete
	// Manager can do everything an editor can, and also share it onward and
	// change who else has access.
	RoleManager = RoleEditor | Share | Manage
)

// Has reports whether every permission in want is present.
func (s Set) Has(want Set) bool { return s&want == want }

// HasAny reports whether at least one permission in want is present.
func (s Set) HasAny(want Set) bool { return s&want != 0 }

// With returns a copy with the given permissions added.
func (s Set) With(add Set) Set { return s | add }

// Without returns a copy with the given permissions removed.
func (s Set) Without(remove Set) Set { return s &^ remove }

// IsEmpty reports whether nothing is granted.
func (s Set) IsEmpty() bool { return s == None }

// permissionNames maps each bit to its API name, in the order they are listed
// to a client.
var permissionNames = []struct {
	bit  Set
	name string
}{
	{Read, "read"},
	{Download, "download"},
	{Upload, "upload"},
	{Create, "create"},
	{Modify, "modify"},
	{Rename, "rename"},
	{Move, "move"},
	{Delete, "delete"},
	{Share, "share"},
	{Manage, "manage"},
}

var nameToPermission = func() map[string]Set {
	m := make(map[string]Set, len(permissionNames))
	for _, p := range permissionNames {
		m[p.name] = p.bit
	}
	return m
}()

var roleNames = map[string]Set{
	"viewer":      RoleViewer,
	"contributor": RoleContributor,
	"editor":      RoleEditor,
	"manager":     RoleManager,
}

// Names returns the permission names in a set, for an API response.
func (s Set) Names() []string {
	out := make([]string, 0, len(permissionNames))
	for _, p := range permissionNames {
		if s.Has(p.bit) {
			out = append(out, p.name)
		}
	}
	return out
}

// String renders the set for logs.
func (s Set) String() string {
	if s == None {
		return "none"
	}
	if s == All {
		return "all"
	}
	return strings.Join(s.Names(), "+")
}

// RoleName returns the name of the role a set exactly matches, or "" when it
// does not match one. The interface shows a role where it can and a list of
// individual permissions otherwise.
func (s Set) RoleName() string {
	// Sorted so the answer is deterministic when two roles coincide.
	names := make([]string, 0, len(roleNames))
	for name := range roleNames {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if roleNames[name] == s {
			return name
		}
	}
	return ""
}

// Parse builds a set from permission names or a role name.
//
// Unknown names are an error rather than being ignored: silently dropping a
// permission the caller asked for would produce a share that does less than
// they were told, which they would not discover until it failed.
func Parse(values []string) (Set, error) {
	if len(values) == 0 {
		return None, fmt.Errorf("permissions: no permissions given")
	}

	// A single value may name a role.
	if len(values) == 1 {
		if role, ok := roleNames[strings.ToLower(strings.TrimSpace(values[0]))]; ok {
			return role, nil
		}
	}

	var set Set
	for _, raw := range values {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		bit, ok := nameToPermission[name]
		if !ok {
			if role, isRole := roleNames[name]; isRole {
				set |= role
				continue
			}
			return None, fmt.Errorf("permissions: %q is not a permission", raw)
		}
		set |= bit
	}

	if set == None {
		return None, fmt.Errorf("permissions: no permissions given")
	}

	// Every meaningful permission implies being able to see the item at all.
	// A grant of Download without Read would otherwise let somebody fetch a
	// file they cannot find.
	return set | Read, nil
}

// ParseExact is Parse without the implied Read.
//
// For denials, where implying Read is not merely unhelpful but dangerous:
// denying Read removes everything, so "you may not delete this" would silently
// become "you may not see this at all". A denial must take away exactly what it
// names and nothing more.
//
// Roles are still expanded — denying "editor" is a reasonable thing to mean —
// but a role that does not itself contain Read does not acquire it here.
func ParseExact(values []string) (Set, error) {
	if len(values) == 0 {
		return None, fmt.Errorf("permissions: no permissions given")
	}

	if len(values) == 1 {
		if role, ok := roleNames[strings.ToLower(strings.TrimSpace(values[0]))]; ok {
			return role, nil
		}
	}

	var set Set
	for _, raw := range values {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		bit, ok := nameToPermission[name]
		if !ok {
			if role, isRole := roleNames[name]; isRole {
				set |= role
				continue
			}
			return None, fmt.Errorf("permissions: %q is not a permission", raw)
		}
		set |= bit
	}

	if set == None {
		return None, fmt.Errorf("permissions: no permissions given")
	}
	return set, nil
}

// MustParse is Parse for constants known to be valid. It panics otherwise.
func MustParse(values ...string) Set {
	set, err := Parse(values)
	if err != nil {
		panic(err)
	}
	return set
}

// RoleNames lists the role names, for API documentation and validation errors.
func RoleNames() []string {
	out := make([]string, 0, len(roleNames))
	for name := range roleNames {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// AllNames lists every permission name.
func AllNames() []string {
	out := make([]string, 0, len(permissionNames))
	for _, p := range permissionNames {
		out = append(out, p.name)
	}
	return out
}
