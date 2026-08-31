// Package web serves the compiled single-page application from the binary.
//
// The frontend is embedded rather than mounted from a volume or baked into the
// reverse-proxy image, so that a deployment can never run a frontend and a
// backend that disagree about the API contract. See docs/architecture.md.
//
// The build copies frontend/dist over internal/web/dist. A placeholder
// index.html is committed so that `go build` succeeds in a clean checkout;
// it renders an explanatory page rather than pretending to be the app.
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var distFS embed.FS

// IndexPath is the SPA entry document.
const IndexPath = "index.html"

// placeholderMarker distinguishes the committed placeholder from a real Vite
// build. Vite never emits this string, and the placeholder must always contain
// it: without it the server cannot tell that its interface is missing, and
// would report a healthy start while serving a blank page.
const placeholderMarker = "frontend not built"

// hashedAssetDir holds files whose names contain a content hash, which Vite
// generates. Those may be cached forever; everything else may not.
const hashedAssetDir = "assets/"

// asset is one precomputed static file.
//
// Assets are held in memory. The production bundle is a few hundred kilobytes,
// so this costs less than the repeated decompression of reading from the
// embedded (deflate-compressed) filesystem on every request — a trade that
// matters on a Raspberry Pi.
type asset struct {
	content     []byte
	etag        string
	contentType string
	immutable   bool
}

// Handler serves the embedded SPA.
type Handler struct {
	assets map[string]asset
	index  asset
	// built reports whether a real frontend build is present, as opposed to
	// the committed placeholder.
	built   bool
	modTime time.Time
}

// NewHandler loads the embedded assets and returns a ready http.Handler.
func NewHandler() (*Handler, error) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, fmt.Errorf("web: cannot open embedded assets: %w", err)
	}

	h := &Handler{
		assets: make(map[string]asset),
		// embed.FS reports a zero modification time; a fixed non-zero time
		// makes conditional requests behave predictably.
		modTime: time.Unix(0, 0),
	}

	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		content, err := fs.ReadFile(sub, p)
		if err != nil {
			return fmt.Errorf("web: cannot read %q: %w", p, err)
		}
		sum := sha256.Sum256(content)
		h.assets[p] = asset{
			content:     content,
			etag:        `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`,
			contentType: contentTypeFor(p),
			immutable:   strings.HasPrefix(p, hashedAssetDir),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	idx, ok := h.assets[IndexPath]
	if !ok {
		return nil, fmt.Errorf("web: embedded assets are missing %s", IndexPath)
	}
	h.index = idx
	// The placeholder is identifiable by a marker the real build never emits.
	h.built = !bytes.Contains(idx.content, []byte(placeholderMarker))

	return h, nil
}

// Built reports whether a real frontend build is embedded. The server logs a
// warning when it is not, so an incomplete image is obvious at startup rather
// than only when somebody opens a browser.
func (h *Handler) Built() bool { return h.built }

// AssetCount reports how many files are embedded.
func (h *Handler) AssetCount() int { return len(h.assets) }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// path.Clean plus the leading-slash trim keeps "../" out of the lookup
	// key. The map lookup itself is already traversal-proof — there is no
	// filesystem access here at all — but a normalized key means "/./app.js"
	// and "/app.js" hit the same cache entry.
	upath := path.Clean("/" + r.URL.Path)
	key := strings.TrimPrefix(upath, "/")

	if key == "" || strings.HasSuffix(upath, "/") {
		h.serveIndex(w, r)
		return
	}

	a, ok := h.assets[key]
	if !ok {
		// Unknown path: hand the SPA its own router. A deep link such as
		// /files/photos must load the app, not 404.
		//
		// Requests that look like a missing static asset get a real 404
		// instead; returning HTML for a missing .js file produces a baffling
		// "Unexpected token '<'" in the console.
		if looksLikeAsset(key) {
			http.NotFound(w, r)
			return
		}
		h.serveIndex(w, r)
		return
	}
	h.serveAsset(w, r, a)
}

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	// index.html must never be cached: it names the hashed asset bundles, so a
	// stale copy pins the browser to a previous release after an upgrade.
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	h.serveAsset(w, r, h.index)
}

func (h *Handler) serveAsset(w http.ResponseWriter, r *http.Request, a asset) {
	header := w.Header()
	header.Set("Content-Type", a.contentType)
	header.Set("ETag", a.etag)
	header.Set("X-Content-Type-Options", "nosniff")

	if a.immutable {
		// Content-hashed filenames change whenever the content does, so a
		// one-year immutable cache is safe and removes revalidation round
		// trips entirely.
		header.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else if header.Get("Cache-Control") == "" {
		header.Set("Cache-Control", "public, max-age=0, must-revalidate")
	}

	if matchesETag(r.Header.Get("If-None-Match"), a.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// ServeContent handles range requests and HEAD correctly, which matters for
	// the embedded media the preview UI will load in later phases.
	http.ServeContent(w, r, "", h.modTime, bytes.NewReader(a.content))
}

// matchesETag implements the If-None-Match comparison, including the "*" form
// and weak-comparison prefixes.
func matchesETag(header, etag string) bool {
	if header == "" {
		return false
	}
	if strings.TrimSpace(header) == "*" {
		return true
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == etag {
			return true
		}
	}
	return false
}

// looksLikeAsset reports whether a path should 404 rather than fall back to the
// SPA. Anything with a file extension that is not a route-like word qualifies.
func looksLikeAsset(key string) bool {
	ext := path.Ext(key)
	if ext == "" {
		return false
	}
	// Application routes can legitimately end in a dotted segment (a filename
	// in a breadcrumb URL, for example), so only treat well-known static
	// extensions as assets.
	switch strings.ToLower(ext) {
	case ".js", ".mjs", ".css", ".map", ".json", ".ico", ".png", ".jpg", ".jpeg",
		".gif", ".svg", ".webp", ".avif", ".woff", ".woff2", ".ttf", ".otf",
		".eot", ".wasm", ".webmanifest", ".txt", ".xml":
		return true
	}
	return false
}

func contentTypeFor(p string) string {
	if ct := mime.TypeByExtension(path.Ext(p)); ct != "" {
		return ct
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".webmanifest":
		return "application/manifest+json"
	case ".map":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}
