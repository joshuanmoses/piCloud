package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newHandler(t *testing.T) *Handler {
	t.Helper()
	h, err := NewHandler()
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func get(t *testing.T, h *Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestServesIndexAtRoot(t *testing.T) {
	t.Parallel()
	rec := get(t, newHandler(t), "/")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Errorf("root should serve the SPA document: %s", rec.Body.String()[:min(120, rec.Body.Len())])
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
}

// A deep link into the application must load the SPA so the client router can
// take over; returning 404 would break every bookmarked folder URL.
func TestUnknownRouteFallsBackToIndex(t *testing.T) {
	t.Parallel()
	h := newHandler(t)
	for _, path := range []string{"/files", "/files/photos/2026", "/admin/users", "/trash"} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "<!doctype html>") {
			t.Errorf("%s did not serve the SPA", path)
		}
	}
}

// A missing .js file must 404 rather than receive HTML, which would surface as
// a baffling "Unexpected token '<'" in the browser console.
func TestMissingAssetReturns404NotHTML(t *testing.T) {
	t.Parallel()
	h := newHandler(t)
	for _, path := range []string{"/assets/missing.js", "/app.css", "/vendor.js.map", "/logo.png", "/favicon.ico"} {
		rec := get(t, h, path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "<!doctype html>") {
			t.Errorf("%s returned the SPA document instead of a 404", path)
		}
	}
}

func TestIndexIsNotCacheable(t *testing.T) {
	t.Parallel()
	rec := get(t, newHandler(t), "/")
	cc := rec.Header().Get("Cache-Control")
	// A cached index.html pins the browser to the previous release's hashed
	// bundles after an upgrade.
	if !strings.Contains(cc, "no-cache") {
		t.Errorf("index Cache-Control = %q, want no-cache", cc)
	}
}

func TestETagEnablesConditionalRequests(t *testing.T) {
	t.Parallel()
	h := newHandler(t)

	first := get(t, h, "/")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("responses should carry an ETag")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304 for a matching ETag", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Error("a 304 must not carry a body")
	}
}

func TestETagHandlesWeakAndListForms(t *testing.T) {
	t.Parallel()
	h := newHandler(t)
	etag := get(t, h, "/").Header().Get("ETag")

	for _, header := range []string{"*", `W/` + etag, `"other", ` + etag} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("If-None-Match", header)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotModified {
			t.Errorf("If-None-Match %q: status = %d, want 304", header, rec.Code)
		}
	}
}

// The map lookup is inherently traversal-proof — there is no filesystem access
// at all — but this locks in that no path shape can reach outside the bundle.
func TestPathTraversalIsHarmless(t *testing.T) {
	t.Parallel()
	h := newHandler(t)
	for _, path := range []string{
		"/../../../etc/passwd",
		"/assets/../../../../etc/shadow",
		"/./././etc/passwd",
	} {
		rec := get(t, h, path)
		if strings.Contains(rec.Body.String(), "root:") {
			t.Fatalf("%s returned system file content", path)
		}
		if rec.Code != http.StatusOK && rec.Code != http.StatusNotFound {
			t.Errorf("%s: unexpected status %d", path, rec.Code)
		}
	}
}

func TestOnlyReadMethodsAreAllowed(t *testing.T) {
	t.Parallel()
	h := newHandler(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", method, rec.Code)
		}
	}
}

func TestHeadRequestHasNoBody(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	newHandler(t).ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Error("HEAD must not return a body")
	}
}

func TestNosniffIsAlwaysSet(t *testing.T) {
	t.Parallel()
	rec := get(t, newHandler(t), "/")
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("static responses must be marked nosniff")
	}
}

func TestPlaceholderIsDetected(t *testing.T) {
	t.Parallel()
	h := newHandler(t)
	// In a clean checkout the committed placeholder is embedded. Built()
	// must report that honestly so startup can warn rather than silently
	// serving a broken interface.
	if h.Built() {
		t.Log("a real frontend build is embedded; placeholder detection not exercised")
	}
	if h.AssetCount() == 0 {
		t.Error("no assets were embedded")
	}
}

// TestCommittedIndexIsThePlaceholder guards the file on disk rather than the
// embedded copy, because the two differ exactly when this matters.
//
// Only dist/index.html is tracked; the hashed assets beside it are not. So if a
// real build is committed over the placeholder, a fresh clone embeds an
// index.html referencing JavaScript that was never committed. Built() then
// returns true — the marker is gone — the server logs "frontend assets loaded"
// instead of a warning, and every visitor gets a blank page with a handful of
// 404s. That is the worst kind of failure: silent, and only visible in a
// browser.
//
// This reads the working-tree file, so it fails after `make build-frontend`
// overwrites it locally. That is intended: `make clean` restores it, and a
// commit made in that state is precisely the mistake being guarded against.
func TestCommittedIndexIsThePlaceholder(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile(filepath.Join("dist", IndexPath))
	if err != nil {
		t.Fatalf("cannot read the committed %s: %v", IndexPath, err)
	}
	if !bytes.Contains(content, []byte(placeholderMarker)) {
		t.Fatalf("dist/%s is a real build artifact, not the placeholder.\n"+
			"A clean checkout would embed it without the assets it references, and the\n"+
			"server would start with no warning and serve a blank page.\n"+
			"Restore it with: git checkout -- backend/internal/web/dist/%s", IndexPath, IndexPath)
	}
}

func TestLooksLikeAsset(t *testing.T) {
	t.Parallel()
	assets := []string{"app.js", "assets/index-abc123.css", "vendor.js.map", "logo.svg", "font.woff2", "site.webmanifest"}
	for _, p := range assets {
		if !looksLikeAsset(p) {
			t.Errorf("%q should be treated as a static asset", p)
		}
	}
	// Application routes, including ones whose last segment contains a dot
	// because it names a user's file.
	routes := []string{"files", "files/photos", "admin/users", "files/Report.pdf", "s/token"}
	for _, p := range routes {
		if looksLikeAsset(p) {
			t.Errorf("%q should fall through to the SPA", p)
		}
	}
}
