package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/files"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
	"github.com/joshuanmoses/piCloud/backend/internal/recent"
)

// handleDownload streams a file to the client.
//
// http.ServeContent does the heavy lifting: it handles Range requests, If-Range,
// If-None-Match, HEAD, and the 206/416 responses correctly. Reimplementing that
// is a well-known source of subtle bugs, and getting Range right is what makes
// video seeking and interrupted-download resumption work.
//
// The content is streamed from an *os.File, so a 4 GB download costs a small
// buffer rather than 4 GB of memory.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	result, apiErr := s.requireAccess(r, id, permissions.Download, false)
	if apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}
	node := result.node

	if !node.IsFile() {
		// A folder is downloadable, as a streamed ZIP, on its own endpoint.
		// Redirecting rather than erroring means a client that guessed wrong
		// still gets the download the user asked for.
		if s.archivesEnabled() {
			http.Redirect(w, r, APIPrefix+"/nodes/"+node.ID.String()+"/archive", http.StatusSeeOther)
			return
		}
		WriteError(w, r, ErrBadRequest("That is a folder. Folder downloads are not available on this server."))
		return
	}

	content, size, err := s.files.OpenContent(node)
	if err != nil {
		s.logger.Error("cannot open stored content",
			"node_id", node.ID, "error", err)
		WriteError(w, r, ErrInternal(err))
		return
	}
	defer content.Close()

	s.writeDownloadHeaders(w, r, node)

	s.logger.Info("file downloaded",
		"node_id", node.ID, "size", size, "range", r.Header.Get("Range") != "",
		"user_id", MustUser(r.Context()).ID)

	// Recorded before the copy, because ServeContent writes the body and this
	// is the last point at which the request context is certainly still live.
	// A Range request for the last kilobyte of a video still counts as
	// "touched this file", which is what the Recent view is for.
	s.recordActivity(r, node.ID, recent.ActionDownloaded)

	// ServeContent sets Content-Length and handles Range itself.
	http.ServeContent(w, r, node.Name, node.UpdatedAt, content)
}

// writeDownloadHeaders sets the headers every content response needs.
//
// Shared by the authenticated download and the public portal, so the
// protections cannot drift apart — the portal is the more exposed of the two,
// and is exactly where a forgotten nosniff would matter most.
func (s *Server) writeDownloadHeaders(w http.ResponseWriter, r *http.Request, node *nodes.Node) {
	// Whether the browser may render this inline is decided from the
	// server-sniffed type, never from anything the uploader supplied. A type
	// that can execute script is always sent as an attachment.
	inline := files.IsInlineSafe(node.MimeType) && r.URL.Query().Get("download") != "1"
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}

	header := w.Header()
	header.Set("Content-Type", node.MimeType)
	header.Set("Content-Disposition", contentDisposition(disposition, node.Name))

	// Uploaded content must never be sniffed into a different type by the
	// browser: that is how a .txt file becomes executable HTML.
	header.Set("X-Content-Type-Options", "nosniff")
	// A restrictive CSP on the response itself, so even if a type slips
	// through the inline allowlist it cannot run script or load anything.
	//
	// frame-ancestors is 'self' rather than 'none' because the directive exists
	// to stop *other* origins framing a user's content — clickjacking, and
	// reading it through a frame. PiFiles framing it in its own preview is the
	// intended use, and 'none' blocks that too: the preview renders an empty
	// box with no error the user can act on. The `sandbox` directive is what
	// actually contains the document, and it is unchanged: no scripts, no
	// forms, no popups, no same-origin access.
	header.Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'self'")

	// The content hash makes a perfect ETag: it changes exactly when the bytes
	// change, so a client's cached copy is revalidated correctly after a file
	// is replaced.
	if node.ContentHash != nil {
		header.Set("ETag", `"`+*node.ContentHash+`"`)
	}
	// Private, because this is somebody's content and no shared cache should
	// ever hold it — including a proxy in front of a public link.
	header.Set("Cache-Control", "private, max-age=0, must-revalidate")
	header.Set("Accept-Ranges", "bytes")
}

// contentDisposition builds a header that survives non-ASCII filenames.
//
// RFC 6266: a plain `filename` for legacy clients, plus `filename*` in
// RFC 5987 encoding for anything modern. Without the latter, a file called
// "Contrat_à_signer.pdf" downloads with a mangled name.
//
// The ASCII fallback is sanitised because a quote or a newline in the value
// would let a filename break out of the header — a response-splitting vector.
func contentDisposition(disposition, filename string) string {
	ascii := make([]rune, 0, len(filename))
	for _, r := range filename {
		switch {
		case r < 0x20 || r == 0x7f:
			// Control characters are dropped entirely.
		case r == '"' || r == '\\':
			ascii = append(ascii, '_')
		case r > 0x7f:
			ascii = append(ascii, '_')
		default:
			ascii = append(ascii, r)
		}
	}
	fallback := strings.TrimSpace(string(ascii))
	if fallback == "" {
		fallback = "download"
	}

	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`,
		disposition, fallback, percentEncode(filename))
}

// percentEncode implements the RFC 5987 attr-char escaping.
func percentEncode(s string) string {
	const hex = "0123456789ABCDEF"
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		unreserved := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') ||
			c == '!' || c == '#' || c == '$' || c == '&' || c == '+' || c == '-' ||
			c == '.' || c == '^' || c == '_' || c == '`' || c == '|' || c == '~'
		if unreserved {
			out = append(out, c)
			continue
		}
		out = append(out, '%', hex[c>>4], hex[c&0x0f])
	}
	return string(out)
}
