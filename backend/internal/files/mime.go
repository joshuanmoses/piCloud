// Package files coordinates the storage layer, the node tree, and quota
// accounting into the operations the API exposes.
package files

import (
	"net/http"
	"path"
	"strings"
)

// DetectMimeType determines a file's type from its content, using the name only
// as a tiebreaker.
//
// The client's declared Content-Type is never consulted. A browser that trusts
// a client-declared type can be made to render an uploaded file as HTML, which
// turns any upload endpoint into stored XSS.
//
// header should be the first 512 bytes of the content — what
// http.DetectContentType examines.
func DetectMimeType(header []byte, filename string) string {
	return neutralize(detect(header, filename))
}

// detect performs the sniffing, before neutralisation.
func detect(header []byte, filename string) string {
	sniffed := http.DetectContentType(header)

	// http.DetectContentType returns octet-stream for anything it does not
	// recognise, which includes most modern formats. The extension is a useful
	// hint there, but only for types that are safe to serve.
	if sniffed == "application/octet-stream" || sniffed == "text/plain; charset=utf-8" {
		if byExt := mimeFromExtension(filename); byExt != "" {
			// A text sniff plus a text-ish extension: trust the extension for
			// the more specific type (Markdown, CSV, JSON).
			if sniffed == "text/plain; charset=utf-8" && !strings.HasPrefix(byExt, "text/") &&
				byExt != "application/json" && byExt != "text/markdown" {
				return sniffed
			}
			return byExt
		}
	}

	return sniffed
}

// dangerousTypes can execute script when a browser renders them.
//
// Content that sniffs as one of these is stored under a neutral type instead.
// The serving layer already refuses to send them inline, so this is defence in
// depth — but it is the layer that matters most: it means a future code path
// that serves by stored MIME type, or a preview feature added in a later phase,
// cannot reintroduce stored XSS by forgetting a check.
//
// Storing HTML as text/plain also produces the behaviour a file manager should
// have: previewing an uploaded .html file shows its source, rather than
// executing it against the signed-in user's session.
var dangerousTypes = []string{
	"text/html",
	"application/xhtml+xml",
	"image/svg+xml",
	"application/xml",
	"text/xml",
	"application/javascript",
	"text/javascript",
	"application/ecmascript",
	"text/vbscript",
	"application/x-shockwave-flash",
}

// neutralize replaces a script-capable type with an inert one.
func neutralize(mimeType string) string {
	base, _, _ := strings.Cut(mimeType, ";")
	base = strings.ToLower(strings.TrimSpace(base))

	for _, dangerous := range dangerousTypes {
		if base == dangerous {
			// Text-based markup becomes plain text, so a preview shows the
			// source. Anything else becomes an opaque download.
			if strings.HasPrefix(base, "text/") || strings.HasSuffix(base, "+xml") ||
				base == "application/xml" || base == "application/javascript" ||
				base == "application/ecmascript" {
				return "text/plain; charset=utf-8"
			}
			return "application/octet-stream"
		}
	}
	return mimeType
}

// safeExtensionTypes maps extensions to types PiFiles is willing to record.
//
// The list is an allowlist rather than a lookup in the system MIME database.
// Anything absent is stored as octet-stream, which browsers download rather
// than render — the safe default for content a stranger uploaded.
//
// Note what is deliberately missing: .html, .htm, .svg, .xhtml, and .xml.
// Serving those inline from the same origin would let uploaded content run
// scripts against a signed-in user's session. Phase 7 serves previews with
// Content-Disposition and a restrictive CSP for exactly this reason.
var safeExtensionTypes = map[string]string{
	// Text and documents
	".txt":  "text/plain; charset=utf-8",
	".md":   "text/markdown; charset=utf-8",
	".csv":  "text/csv; charset=utf-8",
	".log":  "text/plain; charset=utf-8",
	".json": "application/json",
	".pdf":  "application/pdf",
	".rtf":  "application/rtf",

	// Images
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	".bmp":  "image/bmp",
	".ico":  "image/vnd.microsoft.icon",
	".heic": "image/heic",
	".tif":  "image/tiff",
	".tiff": "image/tiff",

	// Audio
	".mp3":  "audio/mpeg",
	".m4a":  "audio/mp4",
	".aac":  "audio/aac",
	".ogg":  "audio/ogg",
	".oga":  "audio/ogg",
	".opus": "audio/opus",
	".wav":  "audio/wav",
	".flac": "audio/flac",

	// Video
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".webm": "video/webm",
	".mkv":  "video/x-matroska",
	".mov":  "video/quicktime",
	".avi":  "video/x-msvideo",

	// Archives
	".zip": "application/zip",
	".gz":  "application/gzip",
	".tar": "application/x-tar",
	".7z":  "application/x-7z-compressed",
	".rar": "application/vnd.rar",
	".bz2": "application/x-bzip2",
	".xz":  "application/x-xz",

	// Office formats. Recorded so the interface can show the right icon;
	// previewing them is deferred beyond v1.
	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".ppt":  "application/vnd.ms-powerpoint",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".odt":  "application/vnd.oasis.opendocument.text",
	".ods":  "application/vnd.oasis.opendocument.spreadsheet",
	".odp":  "application/vnd.oasis.opendocument.presentation",

	// Fonts
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
}

func mimeFromExtension(filename string) string {
	return safeExtensionTypes[strings.ToLower(path.Ext(filename))]
}

// IsInlineSafe reports whether a type may be served with an inline
// Content-Disposition.
//
// Anything else is sent as an attachment. The distinction matters: a type the
// browser renders in the page context can execute script, and uploaded content
// must never do that on a signed-in user's origin.
func IsInlineSafe(mimeType string) bool {
	base, _, _ := strings.Cut(mimeType, ";")
	base = strings.ToLower(strings.TrimSpace(base))

	switch base {
	case "application/pdf",
		"text/plain", "text/markdown", "text/csv",
		"application/json":
		return true
	}

	// Images, audio, and video are inert as far as script execution goes —
	// with the deliberate exception of SVG, which is a document format that
	// can carry script.
	if base == "image/svg+xml" {
		return false
	}
	return strings.HasPrefix(base, "image/") ||
		strings.HasPrefix(base, "audio/") ||
		strings.HasPrefix(base, "video/")
}
