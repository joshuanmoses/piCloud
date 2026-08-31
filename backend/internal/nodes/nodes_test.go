package nodes

import (
	"strings"
	"testing"
)

func TestValidateNameAcceptsRealFilenames(t *testing.T) {
	t.Parallel()
	good := []string{
		"Report.pdf",
		"Tax Documents 2026",
		"holiday-photos",
		"résumé.docx",
		"日本語のファイル.txt",
		"file (1).zip",
		"a",
		"Ünïcödé ïs fïne.md",
		".hidden", // leading dot is a normal Unix convention
		"file.with.many.dots",
		"emoji 🎉 party.gif",
		strings.Repeat("a", 255),
	}
	for _, name := range good {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) rejected a reasonable name: %v", name, err)
		}
	}
}

// These names would break clients, corrupt archives, or disguise content, even
// though content addressing means none of them can reach a filesystem path.
func TestValidateNameRejectsDangerousNames(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                       "empty",
		"   ":                    "whitespace only",
		"..":                     "parent directory",
		".":                      "current directory",
		"a/b":                    "forward slash",
		"a\\b":                   "backslash",
		"../etc/passwd":          "traversal",
		"/etc/passwd":            "absolute path",
		"file\x00.txt":           "NUL byte",
		"file\n.txt":             "newline",
		"file\r.txt":             "carriage return",
		"file\t.txt":             "tab",
		"col:on.txt":             "colon",
		"star*.txt":              "asterisk",
		"quest?.txt":             "question mark",
		`quo"te.txt`:             "double quote",
		"lt<gt>.txt":             "angle brackets",
		"pipe|.txt":              "pipe",
		" leading.txt":           "leading space",
		"trailing.txt ":          "trailing space",
		strings.Repeat("a", 256): "too long",
		"CON":                    "Windows device name",
		"con.txt":                "Windows device name with extension",
		"LPT1":                   "Windows device name",
		"NUL":                    "Windows device name",
	}
	for name, why := range cases {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) accepted a name that is %s", name, why)
		}
	}
}

// A right-to-left override can make "report‮fdp.exe" render as
// "reportexe.pdf" — a classic disguise for a hostile attachment.
func TestValidateNameRejectsBidiOverrides(t *testing.T) {
	t.Parallel()
	disguised := "invoice‮fdp.exe"
	if err := ValidateName(disguised); err == nil {
		t.Fatal("a name containing a right-to-left override must be rejected")
	}
	for _, r := range []rune{'‪', '‫', '‬', '‭', '‮', '⁦', '⁧', '⁨', '⁩'} {
		if err := ValidateName("file" + string(r) + "name.txt"); err == nil {
			t.Errorf("name containing U+%04X must be rejected", r)
		}
	}
}

func TestNormalizeNameTrimsOnly(t *testing.T) {
	t.Parallel()
	if got := NormalizeName("  Report.pdf  "); got != "Report.pdf" {
		t.Errorf("NormalizeName = %q", got)
	}
	// Internal spacing and case are preserved exactly as typed.
	if got := NormalizeName("My  File.PDF"); got != "My  File.PDF" {
		t.Errorf("NormalizeName altered the interior: %q", got)
	}
	// Unicode normalisation is deliberately NOT applied: the name never
	// becomes a filesystem path, so the NFC/NFD problem does not arise, and
	// silently changing what somebody typed would be surprising.
	decomposed := "café.txt" // e + combining acute
	if got := NormalizeName(decomposed); got != decomposed {
		t.Error("NormalizeName must not apply Unicode normalisation")
	}
}

func TestExtensionAndBaseName(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, ext, base string }{
		{"Report.pdf", ".pdf", "Report"},
		{"archive.tar.gz", ".gz", "archive.tar"},
		{"noextension", "", "noextension"},
		{".hidden", ".hidden", ""},
		{"UPPER.PDF", ".pdf", "UPPER"},
	}
	for _, tc := range cases {
		if got := Extension(tc.name); got != tc.ext {
			t.Errorf("Extension(%q) = %q, want %q", tc.name, got, tc.ext)
		}
		if got := BaseName(tc.name); got != tc.base {
			t.Errorf("BaseName(%q) = %q, want %q", tc.name, got, tc.base)
		}
	}
}

func TestUniqueNameDerivesNonCollidingNames(t *testing.T) {
	t.Parallel()

	taken := map[string]bool{"Report.pdf": true}
	check := func(n string) bool { return taken[n] }

	if got := UniqueName("Report.pdf", check); got != "Report (2).pdf" {
		t.Errorf("UniqueName = %q, want %q", got, "Report (2).pdf")
	}

	// A free name is returned unchanged.
	if got := UniqueName("Other.pdf", check); got != "Other.pdf" {
		t.Errorf("UniqueName = %q, want it unchanged", got)
	}

	// Repeated copies continue the sequence rather than nesting suffixes.
	taken["Report (2).pdf"] = true
	if got := UniqueName("Report.pdf", check); got != "Report (3).pdf" {
		t.Errorf("UniqueName = %q, want %q", got, "Report (3).pdf")
	}

	// Copying a name that already ends in " (n)" continues that sequence
	// instead of producing "Report (2) (2).pdf".
	if got := UniqueName("Report (2).pdf", check); got != "Report (3).pdf" {
		t.Errorf("UniqueName = %q, want %q", got, "Report (3).pdf")
	}
}

func TestUniqueNameHandlesNoExtension(t *testing.T) {
	t.Parallel()
	taken := map[string]bool{"Documents": true}
	got := UniqueName("Documents", func(n string) bool { return taken[n] })
	if got != "Documents (2)" {
		t.Errorf("UniqueName = %q, want %q", got, "Documents (2)")
	}
}

// The derived name must still satisfy the length limit, or the copy would be
// rejected by the database.
func TestUniqueNameRespectsLengthLimit(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 250) + ".pdf" // 254 characters
	taken := map[string]bool{long: true}

	got := UniqueName(long, func(n string) bool { return taken[n] })
	if len(got) > MaxNameLength {
		t.Errorf("derived name is %d bytes, over the %d limit: %q", len(got), MaxNameLength, got)
	}
	if got == long {
		t.Error("a colliding name must be changed")
	}
}

func TestKindValidation(t *testing.T) {
	t.Parallel()
	if !KindFile.Valid() || !KindFolder.Valid() {
		t.Error("file and folder are valid kinds")
	}
	if Kind("symlink").Valid() || Kind("").Valid() {
		t.Error("unknown kinds must be rejected")
	}
}

func TestSortFieldValidation(t *testing.T) {
	t.Parallel()
	for _, f := range []SortField{SortByName, SortBySize, SortByModified, SortByCreated} {
		if !f.Valid() {
			t.Errorf("%q should be a valid sort field", f)
		}
	}
	// Anything else must be refused: the value reaches an ORDER BY clause.
	for _, f := range []SortField{"", "name; DROP TABLE nodes", "random()", "owner_id"} {
		if SortField(f).Valid() {
			t.Errorf("%q must not be accepted as a sort field", f)
		}
	}
}

// The ORDER BY clause is the one place this package concatenates SQL. It must
// only ever emit fixed strings selected by a validated enum.
func TestOrderClauseNeverEmitsCallerText(t *testing.T) {
	t.Parallel()
	hostile := SortField("name; DROP TABLE nodes--")
	clause := orderClause(hostile, false)

	if strings.Contains(clause, "DROP") || strings.Contains(clause, "--") {
		t.Fatalf("orderClause passed caller text through: %q", clause)
	}
	// An unrecognised field falls back to the default ordering.
	if clause != orderClause(SortByName, false) {
		t.Errorf("an invalid sort field should fall back to the default: %q", clause)
	}

	// Folders sort before files whatever the column.
	for _, f := range []SortField{SortByName, SortBySize, SortByModified, SortByCreated} {
		if !strings.HasPrefix(orderClause(f, false), "(kind = 'folder') DESC") {
			t.Errorf("sort by %q does not put folders first", f)
		}
	}
}
