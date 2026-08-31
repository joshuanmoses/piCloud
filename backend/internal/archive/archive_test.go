package archive

import (
	"strings"
	"testing"
)

// A ZIP entry name is attacker-controlled data on the *extracting* machine.
// "../../.ssh/authorized_keys" as an entry name is the Zip Slip vulnerability,
// and an archive built here must never be the vector.
func TestNoEntryNameCanEscapeTheExtractionDirectory(t *testing.T) {
	dangerous := []string{
		"../../../etc/passwd",
		"..\\..\\windows\\system32\\config",
		"/etc/shadow",
		"....//....//etc/passwd",
		"..",
		".",
		"...",
		"foo/../../bar",
		"C:\\Windows\\system.ini",
	}

	for _, name := range dangerous {
		got := sanitizeSegment(name)

		if strings.ContainsAny(got, `/\`) {
			t.Errorf("sanitizeSegment(%q) = %q, which still contains a separator", name, got)
		}
		if got == ".." || got == "." || strings.Trim(got, ".") == "" {
			t.Errorf("sanitizeSegment(%q) = %q, which is a traversal segment", name, got)
		}
		if got == "" {
			t.Errorf("sanitizeSegment(%q) produced an empty name", name)
		}
	}
}

func TestControlCharactersAndNulsAreStrippedFromEntryNames(t *testing.T) {
	got := sanitizeSegment("report\x00.pdf\n\r")
	if strings.ContainsRune(got, 0) {
		t.Errorf("sanitizeSegment left a NUL in %q", got)
	}
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("sanitizeSegment left a newline in %q", got)
	}
}

func TestAnOrdinaryNameIsLeftAlone(t *testing.T) {
	for _, name := range []string{"Report.pdf", "Contrat à signer.pdf", "photo (1).jpg", "日本語.txt"} {
		if got := sanitizeSegment(name); got != name {
			t.Errorf("sanitizeSegment(%q) = %q, want it unchanged", name, got)
		}
	}
}

// PiFiles allows two siblings that differ only by case. Both are legal on the
// server's filesystem and neither is on a case-insensitive one, so an archive
// containing both would extract with one silently overwriting the other.
func TestNamesThatCollideOnACaseInsensitiveFilesystemAreDisambiguated(t *testing.T) {
	used := map[string]int{}

	first := uniqueName(used, "Report.pdf")
	second := uniqueName(used, "report.pdf")
	third := uniqueName(used, "REPORT.PDF")

	if first != "Report.pdf" {
		t.Errorf("the first name changed: %q", first)
	}
	if strings.EqualFold(second, first) {
		t.Errorf("second = %q collides with first = %q", second, first)
	}
	if strings.EqualFold(third, first) || strings.EqualFold(third, second) {
		t.Errorf("third = %q collides with an earlier name", third)
	}
	if !strings.HasSuffix(second, ".pdf") {
		t.Errorf("second = %q lost its extension", second)
	}
}

func TestDisambiguationKeepsTheExtensionWhereAnExtractorExpectsIt(t *testing.T) {
	used := map[string]int{}
	uniqueName(used, "archive.tar.gz")
	second := uniqueName(used, "archive.tar.gz")

	if !strings.HasSuffix(second, ".gz") {
		t.Errorf("second = %q, want the extension preserved at the end", second)
	}
}

// Already-compressed content is the common case in a photo or video folder.
// Deflating it costs seconds of ARM CPU per file and saves almost nothing.
func TestAlreadyCompressedTypesAreStoredRatherThanDeflated(t *testing.T) {
	stored := []string{"image/jpeg", "image/png", "video/mp4", "audio/mpeg", "application/zip", "application/gzip"}
	for _, mimeType := range stored {
		if got := compressionForMIME(mimeType); got != storeMethod {
			t.Errorf("compression for %s = %d, want Store", mimeType, got)
		}
	}

	deflated := []string{"text/plain", "application/pdf", "application/json", ""}
	for _, mimeType := range deflated {
		if got := compressionForMIME(mimeType); got != deflateMethod {
			t.Errorf("compression for %s = %d, want Deflate", mimeType, got)
		}
	}
}

func TestTheArchiveNameIsSafeAndDated(t *testing.T) {
	if got := FileName("Holiday Photos", false); got != "Holiday Photos.zip" {
		t.Errorf("FileName = %q, want the folder's name", got)
	}
	// A multi-item selection has no single name to take.
	multi := FileName("", true)
	if !strings.HasPrefix(multi, "pifiles-") || !strings.HasSuffix(multi, ".zip") {
		t.Errorf("FileName for a selection = %q, want a dated pifiles-*.zip", multi)
	}
	// And a hostile folder name must not become a hostile filename.
	if got := FileName("../../etc", false); strings.Contains(got, "/") {
		t.Errorf("FileName(%q) = %q, which contains a separator", "../../etc", got)
	}
}
