package files

import (
	"strings"
	"testing"
)

func TestDetectMimeTypeUsesContentNotExtension(t *testing.T) {
	t.Parallel()

	pngHeader := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 13}

	// The extension claims PDF; the content is a PNG. Content wins.
	if got := DetectMimeType(pngHeader, "definitely-a.pdf"); !strings.HasPrefix(got, "image/png") {
		t.Errorf("DetectMimeType = %q, want image/png — content must win over the extension", got)
	}
}

// This is the stored-XSS defence. A file whose content is HTML must never be
// typed as HTML, because the browser would then execute it on the user's
// origin.
func TestHTMLContentIsNeverTypedAsHTML(t *testing.T) {
	t.Parallel()

	hostile := [][]byte{
		[]byte("<!DOCTYPE html><html><script>alert(document.cookie)</script>"),
		[]byte("<html><body onload=\"fetch('//evil')\">"),
		[]byte("<svg xmlns=\"http://www.w3.org/2000/svg\"><script>alert(1)</script></svg>"),
	}
	names := []string{"page.html", "page.htm", "image.svg", "doc.xhtml", "innocent.txt"}

	for _, content := range hostile {
		for _, name := range names {
			got := DetectMimeType(content, name)
			if strings.Contains(got, "html") || strings.Contains(got, "svg") {
				t.Errorf("DetectMimeType(%q) = %q — HTML and SVG must never be recorded as renderable types",
					name, got)
			}
			if IsInlineSafe(got) && strings.HasPrefix(got, "text/html") {
				t.Errorf("type %q would be served inline", got)
			}
		}
	}
}

func TestDetectMimeTypeUsesExtensionForUnrecognisedContent(t *testing.T) {
	t.Parallel()

	// http.DetectContentType cannot identify these from their bytes, so the
	// extension allowlist supplies the type.
	opaque := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05}
	cases := map[string]string{
		"video.mp4":  "video/mp4",
		"song.flac":  "audio/flac",
		"book.epub":  "application/octet-stream", // not on the allowlist
		"sheet.xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	}
	for name, want := range cases {
		if got := DetectMimeType(opaque, name); got != want {
			t.Errorf("DetectMimeType(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestDetectMimeTypeRefinesPlainText(t *testing.T) {
	t.Parallel()

	// Sniffing sees plain text; the extension names something more specific.
	markdown := []byte("# Heading\n\nSome text.\n")
	if got := DetectMimeType(markdown, "notes.md"); !strings.HasPrefix(got, "text/markdown") {
		t.Errorf("DetectMimeType = %q, want text/markdown", got)
	}

	json := []byte(`{"key": "value"}`)
	if got := DetectMimeType(json, "data.json"); got != "application/json" {
		t.Errorf("DetectMimeType = %q, want application/json", got)
	}

	// A .exe extension on text content must not upgrade it to something
	// executable-looking.
	if got := DetectMimeType([]byte("just some text"), "thing.exe"); strings.Contains(got, "executable") {
		t.Errorf("DetectMimeType = %q", got)
	}
}

func TestDetectMimeTypeHandlesEmptyContent(t *testing.T) {
	t.Parallel()
	if got := DetectMimeType(nil, "empty.txt"); got == "" {
		t.Error("an empty file must still get a type")
	}
	if got := DetectMimeType([]byte{}, ""); got == "" {
		t.Error("a nameless empty file must still get a type")
	}
}

func TestIsInlineSafe(t *testing.T) {
	t.Parallel()

	safe := []string{
		"application/pdf",
		"text/plain; charset=utf-8",
		"text/markdown",
		"text/csv",
		"application/json",
		"image/png", "image/jpeg", "image/webp", "image/gif",
		"audio/mpeg", "audio/flac",
		"video/mp4", "video/webm",
	}
	for _, mt := range safe {
		if !IsInlineSafe(mt) {
			t.Errorf("%q should be safe to render inline", mt)
		}
	}

	// Anything that can execute script in the page context must be downloaded.
	unsafe := []string{
		"text/html",
		"text/html; charset=utf-8",
		"application/xhtml+xml",
		// SVG is an image, but it is a document format that can carry script.
		"image/svg+xml",
		"application/xml",
		"application/javascript",
		"text/javascript",
		"application/octet-stream",
		"application/zip",
		"application/x-msdownload",
	}
	for _, mt := range unsafe {
		if IsInlineSafe(mt) {
			t.Errorf("%q must NOT be served inline — it can execute in the page context", mt)
		}
	}
}

func TestIsInlineSafeIgnoresCaseAndParameters(t *testing.T) {
	t.Parallel()
	if !IsInlineSafe("IMAGE/PNG") {
		t.Error("the check must be case-insensitive")
	}
	if IsInlineSafe("TEXT/HTML; charset=utf-8") {
		t.Error("case must not be a way around the HTML rule")
	}
	if !IsInlineSafe("  application/pdf  ") {
		t.Error("surrounding whitespace must not matter")
	}
}

// The allowlist must not contain a type the browser renders as a document.
func TestExtensionAllowlistContainsNoRenderableDocuments(t *testing.T) {
	t.Parallel()
	for ext, mimeType := range safeExtensionTypes {
		lower := strings.ToLower(mimeType)
		if strings.Contains(lower, "html") || strings.Contains(lower, "svg") ||
			strings.Contains(lower, "javascript") {
			t.Errorf("extension %q maps to %q, which can execute script", ext, mimeType)
		}
	}
	for _, ext := range []string{".html", ".htm", ".svg", ".xhtml", ".xml", ".js", ".mjs"} {
		if mt, present := safeExtensionTypes[ext]; present {
			t.Errorf("%q must not be on the allowlist (maps to %q)", ext, mt)
		}
	}
}
