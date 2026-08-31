package permissions

import (
	"strings"
	"testing"
)

func TestSetMembership(t *testing.T) {
	t.Parallel()

	set := Read | Download

	if !set.Has(Read) || !set.Has(Download) {
		t.Error("the set should contain what was put in it")
	}
	if set.Has(Delete) {
		t.Error("the set should not contain what was not")
	}
	// Has requires every requested bit, not just one.
	if set.Has(Read | Delete) {
		t.Error("Has must require all of the requested permissions")
	}
	if !set.HasAny(Read | Delete) {
		t.Error("HasAny should match on a single permission")
	}
	if None.HasAny(All) {
		t.Error("an empty set matches nothing")
	}
}

func TestSetArithmetic(t *testing.T) {
	t.Parallel()

	base := RoleViewer
	extended := base.With(Delete)
	if !extended.Has(Delete) || !extended.Has(Read) {
		t.Error("With should add without removing")
	}
	// The original must be untouched: sets are values, not references.
	if base.Has(Delete) {
		t.Error("With must not mutate the receiver")
	}

	reduced := extended.Without(Delete)
	if reduced.Has(Delete) {
		t.Error("Without should remove")
	}
	if !reduced.Has(Read) {
		t.Error("Without must remove only what was asked for")
	}
}

func TestAllContainsEveryPermission(t *testing.T) {
	t.Parallel()
	for _, name := range AllNames() {
		set, err := Parse([]string{name})
		if err != nil {
			t.Fatalf("Parse(%q): %v", name, err)
		}
		if !All.Has(set) {
			t.Errorf("All is missing %q", name)
		}
	}
}

func TestRolesAreOrdered(t *testing.T) {
	t.Parallel()

	// Each role should be a superset of the previous one, so the interface can
	// present them as increasing levels of trust.
	if !RoleContributor.Has(RoleViewer) {
		t.Error("a contributor should be able to do everything a viewer can")
	}
	if !RoleEditor.Has(RoleContributor) {
		t.Error("an editor should be able to do everything a contributor can")
	}
	if !RoleManager.Has(RoleEditor) {
		t.Error("a manager should be able to do everything an editor can")
	}
}

// Sharing is deliberately not transitive: without this, one share of one
// folder eventually reaches everyone.
func TestOnlyManagerCanShareOnward(t *testing.T) {
	t.Parallel()

	for name, role := range map[string]Set{
		"viewer":      RoleViewer,
		"contributor": RoleContributor,
		"editor":      RoleEditor,
	} {
		if role.Has(Share) {
			t.Errorf("%s must not be able to share onward", name)
		}
		if role.Has(Manage) {
			t.Errorf("%s must not be able to change who has access", name)
		}
	}

	if !RoleManager.Has(Share) || !RoleManager.Has(Manage) {
		t.Error("a manager should be able to share and manage access")
	}
}

// A contributor can add content but must not be able to alter or remove what
// is already there — the "drop box" arrangement.
func TestContributorCannotChangeExistingContent(t *testing.T) {
	t.Parallel()

	if !RoleContributor.Has(Upload) || !RoleContributor.Has(Create) {
		t.Error("a contributor should be able to add content")
	}
	for _, forbidden := range []struct {
		name string
		bit  Set
	}{
		{"modify", Modify},
		{"delete", Delete},
		{"rename", Rename},
		{"move", Move},
	} {
		if RoleContributor.Has(forbidden.bit) {
			t.Errorf("a contributor must not be able to %s existing content", forbidden.name)
		}
	}
}

func TestParseNames(t *testing.T) {
	t.Parallel()

	set, err := Parse([]string{"read", "download", "delete"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !set.Has(Read | Download | Delete) {
		t.Errorf("parsed set = %s", set)
	}
}

func TestParseRoleName(t *testing.T) {
	t.Parallel()

	set, err := Parse([]string{"editor"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if set != RoleEditor {
		t.Errorf("parsed %s, want the editor role", set)
	}
}

// Every meaningful permission implies being able to see the item. A grant of
// Download without Read would let somebody fetch a file they cannot find.
func TestParseAlwaysImpliesRead(t *testing.T) {
	t.Parallel()

	set, err := Parse([]string{"download"})
	if err != nil {
		t.Fatal(err)
	}
	if !set.Has(Read) {
		t.Error("a parsed set must always include read")
	}
}

// Silently dropping a permission the caller asked for would produce a share
// that does less than they were told, which they would not discover until it
// failed.
func TestParseRejectsUnknownNames(t *testing.T) {
	t.Parallel()

	for _, values := range [][]string{
		{"read", "teleport"},
		{"admin"},
		{"*"},
		{"'; DROP TABLE permissions--"},
		{},
		{""},
	} {
		if set, err := Parse(values); err == nil {
			t.Errorf("Parse(%v) = %s, want an error", values, set)
		}
	}
}

func TestNamesRoundTrip(t *testing.T) {
	t.Parallel()

	original := RoleEditor
	reparsed, err := Parse(original.Names())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if reparsed != original {
		t.Errorf("round trip changed the set: %s became %s", original, reparsed)
	}
}

func TestRoleName(t *testing.T) {
	t.Parallel()

	cases := map[Set]string{
		RoleViewer:      "viewer",
		RoleContributor: "contributor",
		RoleEditor:      "editor",
		RoleManager:     "manager",
	}
	for set, want := range cases {
		if got := set.RoleName(); got != want {
			t.Errorf("RoleName(%s) = %q, want %q", set, got, want)
		}
	}

	// A set that matches no role reports none, so the interface falls back to
	// listing individual permissions.
	if got := (Read | Delete).RoleName(); got != "" {
		t.Errorf("an ad-hoc set should not claim a role, got %q", got)
	}
}

func TestStringIsReadable(t *testing.T) {
	t.Parallel()

	if got := None.String(); got != "none" {
		t.Errorf("None = %q", got)
	}
	if got := All.String(); got != "all" {
		t.Errorf("All = %q", got)
	}
	if got := (Read | Delete).String(); !strings.Contains(got, "read") || !strings.Contains(got, "delete") {
		t.Errorf("String = %q, should name its permissions", got)
	}
}

// The bit values are persisted in the database. Changing one would silently
// re-interpret every existing grant, which is why they are pinned here.
func TestBitValuesAreStable(t *testing.T) {
	t.Parallel()

	expected := map[string]Set{
		"read": 1, "download": 2, "upload": 4, "create": 8, "modify": 16,
		"rename": 32, "move": 64, "delete": 128, "share": 256, "manage": 512,
	}
	for name, want := range expected {
		got, err := Parse([]string{name})
		if err != nil {
			t.Fatal(err)
		}
		// Parse adds Read, so compare after removing it except for read itself.
		if name != "read" {
			got = got.Without(Read)
		}
		if got != want {
			t.Errorf("%q = %d, want %d — changing a bit value silently "+
				"re-interprets every stored grant", name, got, want)
		}
	}
}

// A grant of Download without Read would let somebody fetch a file they cannot
// find, so Parse supplies it. A *denial* of Download must not: implying Read
// there turns "you may not download this" into "you may not see this at all",
// which is a much larger thing than was asked for.
func TestOnlyGrantsImplyRead(t *testing.T) {
	granted, err := Parse([]string{"download"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !granted.Has(Read) {
		t.Error("a grant of download does not imply read; the file would be unfindable")
	}

	denied, err := ParseExact([]string{"download"})
	if err != nil {
		t.Fatalf("ParseExact: %v", err)
	}
	if denied.Has(Read) {
		t.Error("a denial of download implies read — it would remove all access")
	}
	if !denied.Has(Download) {
		t.Error("ParseExact lost the permission it was given")
	}
}

func TestParseExactStillExpandsRoles(t *testing.T) {
	set, err := ParseExact([]string{"editor"})
	if err != nil {
		t.Fatalf("ParseExact: %v", err)
	}
	if set != roleNames["editor"] {
		t.Errorf("ParseExact(editor) = %s, want the editor role unchanged", set)
	}
}

func TestParseExactRefusesNonsense(t *testing.T) {
	for _, values := range [][]string{nil, {}, {""}, {"teleport"}} {
		if _, err := ParseExact(values); err == nil {
			t.Errorf("ParseExact(%v) was accepted", values)
		}
	}
}
