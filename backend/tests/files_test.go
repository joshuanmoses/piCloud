package tests

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/api"
)

// --- Client helpers ---------------------------------------------------------

// raw sends a request with an arbitrary body and content type, which is how
// uploads and chunk transfers work.
func (c *client) raw(method, path, contentType string, body io.Reader) *httptest.ResponseRecorder {
	c.t.Helper()

	req := httptest.NewRequest(method, path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.RemoteAddr = "127.0.0.1:40000"
	for name, value := range c.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}

	rec := httptest.NewRecorder()
	c.server.ServeHTTP(rec, req)
	return rec
}

// upload sends a file in a single request and returns its node id.
func (c *client) upload(parentID, name string, content []byte) (uuid.UUID, *httptest.ResponseRecorder) {
	c.t.Helper()

	target := fmt.Sprintf("/api/v1/files?name=%s", urlEncode(name))
	if parentID != "" {
		target += "&parent_id=" + parentID
	}

	rec := c.raw(http.MethodPost, target, "application/octet-stream", bytes.NewReader(content))
	if rec.Code != http.StatusCreated {
		return uuid.Nil, rec
	}

	var body struct {
		Node struct {
			ID uuid.UUID `json:"id"`
		} `json:"node"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		c.t.Fatalf("upload response was not valid JSON (%s): %v", rec.Body, err)
	}
	return body.Node.ID, rec
}

// mkdir creates a folder and returns its id.
func (c *client) mkdir(parentID, name string) uuid.UUID {
	c.t.Helper()

	rec := c.do(http.MethodPost, "/api/v1/nodes/folders",
		map[string]any{"parent_id": parentID, "name": name})
	if rec.Code != http.StatusCreated {
		c.t.Fatalf("mkdir %q failed with %d: %s", name, rec.Code, rec.Body)
	}

	var body struct {
		Node struct {
			ID uuid.UUID `json:"id"`
		} `json:"node"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		c.t.Fatal(err)
	}
	return body.Node.ID
}

func urlEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		unreserved := (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == '~'
		if unreserved {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

// signedIn returns a client already authenticated as a fresh user.
func (h *harness) signedIn(username string) *client {
	h.t.Helper()
	const password = "a perfectly reasonable passphrase"
	h.createUser(username, password, false)

	c := h.newClient()
	if rec := c.login(username, password); rec.Code != http.StatusOK {
		h.t.Fatalf("sign in as %q failed: %d %s", username, rec.Code, rec.Body)
	}
	return c
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- Folders ----------------------------------------------------------------

func TestCreateAndListFolders(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	projectsID := c.mkdir("root", "Projects")
	c.mkdir("root", "Photos")
	c.mkdir(projectsID.String(), "PiFiles")

	rec := c.do(http.MethodGet, "/api/v1/nodes", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	var body struct {
		Nodes []struct {
			Name   string `json:"name"`
			Kind   string `json:"kind"`
			IsRoot bool   `json:"is_root"`
		} `json:"nodes"`
		Total  int `json:"total"`
		Folder struct {
			IsRoot bool `json:"is_root"`
		} `json:"folder"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	if body.Total != 2 {
		t.Errorf("root has %d children, want 2", body.Total)
	}
	if !body.Folder.IsRoot {
		t.Error("listing the default folder should return the user's root")
	}
	// Alphabetical by default.
	if len(body.Nodes) != 2 || body.Nodes[0].Name != "Photos" || body.Nodes[1].Name != "Projects" {
		t.Errorf("unexpected listing: %+v", body.Nodes)
	}
}

func TestDuplicateNameIsRejected(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	c.mkdir("root", "Documents")

	rec := c.do(http.MethodPost, "/api/v1/nodes/folders",
		map[string]any{"parent_id": "root", "name": "Documents"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}

	// Case-insensitively, too: "documents" and "Documents" must not coexist.
	rec = c.do(http.MethodPost, "/api/v1/nodes/folders",
		map[string]any{"parent_id": "root", "name": "documents"})
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 for a case-different duplicate", rec.Code)
	}
}

func TestInvalidFolderNamesAreRejected(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	for _, name := range []string{"", "..", ".", "a/b", "a\\b", "con", "star*", "  ", strings.Repeat("x", 300)} {
		rec := c.do(http.MethodPost, "/api/v1/nodes/folders",
			map[string]any{"parent_id": "root", "name": name})
		if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
			t.Errorf("name %q was accepted with status %d", name, rec.Code)
		}
	}
}

func TestBreadcrumbsDescribeThePath(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	projects := c.mkdir("root", "Projects")
	pifiles := c.mkdir(projects.String(), "PiFiles")
	docs := c.mkdir(pifiles.String(), "Docs")

	rec := c.do(http.MethodGet, "/api/v1/nodes/"+docs.String(), nil)
	var body struct {
		Breadcrumbs []struct {
			Name   string `json:"name"`
			IsRoot bool   `json:"is_root"`
		} `json:"breadcrumbs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	if len(body.Breadcrumbs) != 4 {
		t.Fatalf("expected 4 breadcrumbs (root + 3), got %d: %+v", len(body.Breadcrumbs), body.Breadcrumbs)
	}
	if !body.Breadcrumbs[0].IsRoot {
		t.Error("the first breadcrumb should be the root")
	}
	want := []string{"", "Projects", "PiFiles", "Docs"}
	for i, w := range want {
		if body.Breadcrumbs[i].Name != w {
			t.Errorf("breadcrumb %d = %q, want %q", i, body.Breadcrumbs[i].Name, w)
		}
	}
}

// --- Upload and download ----------------------------------------------------

func TestUploadAndDownloadRoundTrip(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	content := []byte("The quick brown fox jumps over the lazy dog.\n")
	id, rec := c.upload("root", "Report.txt", content)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", rec.Code, rec.Body)
	}

	rec = c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/download", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("download status = %d: %s", rec.Code, rec.Body)
	}
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Error("downloaded content does not match what was uploaded")
	}
	if got := rec.Header().Get("Content-Length"); got != fmt.Sprint(len(content)) {
		t.Errorf("Content-Length = %q, want %d", got, len(content))
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("downloads must be marked nosniff")
	}
	if rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Error("downloads should advertise range support")
	}
}

func TestUploadDetectsTypeFromContent(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	png := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte{0}, 100)...)

	// The name claims PDF; the content is a PNG.
	id, rec := c.upload("root", "not-really.pdf", png)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload failed: %d %s", rec.Code, rec.Body)
	}

	rec = c.do(http.MethodGet, "/api/v1/nodes/"+id.String(), nil)
	var body struct {
		Node struct {
			MimeType string `json:"mime_type"`
		} `json:"node"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)

	if !strings.HasPrefix(body.Node.MimeType, "image/png") {
		t.Errorf("mime type = %q; content must win over the extension", body.Node.MimeType)
	}
}

// Uploaded HTML must never be stored or served as a renderable type.
func TestUploadedHTMLIsNeutralized(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	hostile := []byte("<!DOCTYPE html><html><script>alert(document.cookie)</script></html>")
	id, rec := c.upload("root", "attack.html", hostile)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload failed: %d %s", rec.Code, rec.Body)
	}

	rec = c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/download", nil)
	contentType := rec.Header().Get("Content-Type")
	if strings.Contains(strings.ToLower(contentType), "html") {
		t.Fatalf("Content-Type = %q; uploaded HTML must never be served as HTML", contentType)
	}

	disposition := rec.Header().Get("Content-Disposition")
	if strings.HasPrefix(disposition, "inline") && strings.Contains(contentType, "html") {
		t.Errorf("hostile content would render inline: %q", disposition)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff is required so the browser cannot re-sniff this as HTML")
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Errorf("downloads should carry a sandboxing CSP, got %q", csp)
	}
}

func TestDownloadSupportsRangeRequests(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	content := []byte("0123456789ABCDEFGHIJ")
	id, _ := c.upload("root", "data.bin", content)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/"+id.String()+"/download", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	for name, value := range c.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	req.Header.Set("Range", "bytes=5-9")

	rec := httptest.NewRecorder()
	c.server.ServeHTTP(rec, req)

	// Range support is what makes media seeking and resumed downloads work.
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got := rec.Body.String(); got != "56789" {
		t.Errorf("body = %q, want %q", got, "56789")
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 5-9/20" {
		t.Errorf("Content-Range = %q", got)
	}
}

func TestDownloadHandlesNonASCIIFilenames(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, rec := c.upload("root", "Contrat à signer.txt", []byte("contenu"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload failed: %d %s", rec.Code, rec.Body)
	}

	rec = c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/download", nil)
	disposition := rec.Header().Get("Content-Disposition")

	// RFC 6266: an ASCII fallback plus an RFC 5987 encoded form.
	if !strings.Contains(disposition, "filename*=UTF-8''") {
		t.Errorf("Content-Disposition lacks the encoded form: %q", disposition)
	}
	if !strings.Contains(disposition, `filename="`) {
		t.Errorf("Content-Disposition lacks an ASCII fallback: %q", disposition)
	}
	// A raw newline or quote would let a filename split the response.
	if strings.ContainsAny(disposition, "\r\n") {
		t.Errorf("Content-Disposition contains a line break: %q", disposition)
	}
}

// A filename containing a quote must not be able to break out of the header.
func TestDownloadFilenameCannotSplitResponse(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, rec := c.upload("root", `evil".txt`, []byte("x"))
	if rec.Code != http.StatusCreated {
		t.Skipf("the name was rejected at upload, which is also acceptable: %d", rec.Code)
	}

	rec = c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/download", nil)
	disposition := rec.Header().Get("Content-Disposition")
	if strings.Count(disposition, `"`) != 2 {
		t.Errorf("the quote was not neutralised: %q", disposition)
	}
}

func TestEmptyFileRoundTrips(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, rec := c.upload("root", "empty.txt", []byte{})
	if rec.Code != http.StatusCreated {
		t.Fatalf("an empty file must be accepted: %d %s", rec.Code, rec.Body)
	}

	rec = c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/download", nil)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("empty download: status %d, %d bytes", rec.Code, rec.Body.Len())
	}
}

// --- Rename, move, copy -----------------------------------------------------

func TestRenameFile(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "old-name.txt", []byte("content"))

	rec := c.do(http.MethodPatch, "/api/v1/nodes/"+id.String(),
		map[string]any{"name": "new-name.txt"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	var body struct {
		Node struct {
			Name string `json:"name"`
		} `json:"node"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Node.Name != "new-name.txt" {
		t.Errorf("name = %q", body.Node.Name)
	}

	// The content must be unaffected by a rename.
	rec = c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/download", nil)
	if rec.Body.String() != "content" {
		t.Error("renaming changed the content")
	}
}

func TestMoveFileBetweenFolders(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	source := c.mkdir("root", "Source")
	destination := c.mkdir("root", "Destination")
	id, _ := c.upload(source.String(), "file.txt", []byte("content"))

	rec := c.do(http.MethodPatch, "/api/v1/nodes/"+id.String(),
		map[string]any{"parent_id": destination.String()})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	// It is in the destination and gone from the source.
	rec = c.do(http.MethodGet, "/api/v1/nodes?parent_id="+destination.String(), nil)
	var dest struct {
		Total int `json:"total"`
	}
	json.Unmarshal(rec.Body.Bytes(), &dest)
	if dest.Total != 1 {
		t.Errorf("destination has %d children, want 1", dest.Total)
	}

	rec = c.do(http.MethodGet, "/api/v1/nodes?parent_id="+source.String(), nil)
	var src struct {
		Total int `json:"total"`
	}
	json.Unmarshal(rec.Body.Bytes(), &src)
	if src.Total != 0 {
		t.Errorf("source still has %d children", src.Total)
	}
}

// Moving a folder into its own subtree would detach that subtree from the root
// entirely: unreachable, uncountable, and impossible to delete.
func TestFolderCannotBeMovedIntoItself(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	outer := c.mkdir("root", "Outer")
	inner := c.mkdir(outer.String(), "Inner")
	deeper := c.mkdir(inner.String(), "Deeper")

	// Directly into itself.
	rec := c.do(http.MethodPatch, "/api/v1/nodes/"+outer.String(),
		map[string]any{"parent_id": outer.String()})
	if rec.Code == http.StatusOK {
		t.Error("a folder was moved into itself")
	}

	// Into its own child.
	rec = c.do(http.MethodPatch, "/api/v1/nodes/"+outer.String(),
		map[string]any{"parent_id": inner.String()})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when moving a folder into its child", rec.Code)
	}

	// Into a deeper descendant.
	rec = c.do(http.MethodPatch, "/api/v1/nodes/"+outer.String(),
		map[string]any{"parent_id": deeper.String()})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when moving a folder into a descendant", rec.Code)
	}

	// The tree must be intact afterwards.
	rec = c.do(http.MethodGet, "/api/v1/nodes?parent_id="+outer.String(), nil)
	var body struct {
		Total int `json:"total"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Total != 1 {
		t.Errorf("the tree was damaged: Outer has %d children, want 1", body.Total)
	}
}

func TestRootFolderCannotBeModified(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	rec := c.do(http.MethodGet, "/api/v1/nodes", nil)
	var listing struct {
		Folder struct {
			ID uuid.UUID `json:"id"`
		} `json:"folder"`
	}
	json.Unmarshal(rec.Body.Bytes(), &listing)
	rootID := listing.Folder.ID.String()

	if rec := c.do(http.MethodPatch, "/api/v1/nodes/"+rootID, map[string]any{"name": "Hacked"}); rec.Code == http.StatusOK {
		t.Error("the root folder was renamed")
	}
	if rec := c.do(http.MethodDelete, "/api/v1/nodes/"+rootID, nil); rec.Code == http.StatusNoContent {
		t.Error("the root folder was deleted")
	}
}

func TestCopyFileDerivesUniqueName(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	content := []byte("shared content")
	id, _ := c.upload("root", "Report.pdf", content)

	rec := c.do(http.MethodPost, "/api/v1/nodes/"+id.String()+"/copy",
		map[string]any{"parent_id": "root"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	var body struct {
		Node struct {
			ID   uuid.UUID `json:"id"`
			Name string    `json:"name"`
		} `json:"node"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)

	if body.Node.Name != "Report (2).pdf" {
		t.Errorf("copy name = %q, want %q", body.Node.Name, "Report (2).pdf")
	}
	if body.Node.ID == id {
		t.Error("the copy must be a distinct node")
	}

	// The copy's content must be readable and identical.
	rec = c.do(http.MethodGet, "/api/v1/nodes/"+body.Node.ID.String()+"/download", nil)
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Error("the copy's content differs from the original")
	}
}

func TestCopyFolderDuplicatesSubtree(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	source := c.mkdir("root", "Source")
	nested := c.mkdir(source.String(), "Nested")
	c.upload(source.String(), "top.txt", []byte("top"))
	c.upload(nested.String(), "deep.txt", []byte("deep"))

	destination := c.mkdir("root", "Destination")

	rec := c.do(http.MethodPost, "/api/v1/nodes/"+source.String()+"/copy",
		map[string]any{"parent_id": destination.String()})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	var body struct {
		Node struct {
			ID uuid.UUID `json:"id"`
		} `json:"node"`
		NodesCopied int `json:"nodes_copied"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)

	// Source + Nested + top.txt + deep.txt.
	if body.NodesCopied != 4 {
		t.Errorf("copied %d nodes, want 4", body.NodesCopied)
	}

	// Walk into the copy and confirm the nested file survived.
	rec = c.do(http.MethodGet, "/api/v1/nodes?parent_id="+body.Node.ID.String(), nil)
	var children struct {
		Nodes []struct {
			ID   uuid.UUID `json:"id"`
			Name string    `json:"name"`
			Kind string    `json:"kind"`
		} `json:"nodes"`
	}
	json.Unmarshal(rec.Body.Bytes(), &children)
	if len(children.Nodes) != 2 {
		t.Fatalf("the copy has %d children, want 2", len(children.Nodes))
	}

	var copiedNested uuid.UUID
	for _, n := range children.Nodes {
		if n.Kind == "folder" {
			copiedNested = n.ID
		}
	}
	rec = c.do(http.MethodGet, "/api/v1/nodes?parent_id="+copiedNested.String(), nil)
	var deep struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	}
	json.Unmarshal(rec.Body.Bytes(), &deep)
	if len(deep.Nodes) != 1 || deep.Nodes[0].Name != "deep.txt" {
		t.Errorf("the nested file was not copied: %+v", deep.Nodes)
	}
}

// Copying shares content on disk but must still charge the quota, or copying
// one file repeatedly would be a way around an allowance.
func TestCopySharesContentOnDisk(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	content := []byte("deduplicated content for the copy test")
	id, _ := c.upload("root", "Original.txt", content)

	rec := c.do(http.MethodPost, "/api/v1/nodes/"+id.String()+"/copy",
		map[string]any{"parent_id": "root"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("copy failed: %d %s", rec.Code, rec.Body)
	}

	// One blob on disk, two references.
	digest := sha256Hex(content)
	var refCount int
	if err := h.db.Pool().QueryRow(t.Context(),
		`SELECT ref_count FROM blobs WHERE hash = $1`, digest).Scan(&refCount); err != nil {
		t.Fatal(err)
	}
	if refCount != 2 {
		t.Errorf("ref_count = %d, want 2", refCount)
	}

	blobPath := filepath.Join(h.storageRoot, "files", digest[0:2], digest[2:4], digest)
	if _, err := os.Stat(blobPath); err != nil {
		t.Errorf("the shared blob is missing: %v", err)
	}
}

// --- Trash ------------------------------------------------------------------

func TestDeleteMovesToTrashAndRestoreBringsItBack(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	content := []byte("content that will be trashed")
	id, _ := c.upload("root", "Doomed.txt", content)

	if rec := c.do(http.MethodDelete, "/api/v1/nodes/"+id.String(), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d: %s", rec.Code, rec.Body)
	}

	// Gone from the listing.
	rec := c.do(http.MethodGet, "/api/v1/nodes", nil)
	var listing struct {
		Total int `json:"total"`
	}
	json.Unmarshal(rec.Body.Bytes(), &listing)
	if listing.Total != 0 {
		t.Errorf("the deleted file is still listed")
	}

	// Present in Trash.
	rec = c.do(http.MethodGet, "/api/v1/trash", nil)
	var trash struct {
		Nodes []struct {
			ID   uuid.UUID `json:"id"`
			Name string    `json:"name"`
		} `json:"nodes"`
	}
	json.Unmarshal(rec.Body.Bytes(), &trash)
	if len(trash.Nodes) != 1 || trash.Nodes[0].Name != "Doomed.txt" {
		t.Fatalf("trash = %+v", trash.Nodes)
	}

	// Content must still be on disk: a soft delete is reversible.
	digest := sha256Hex(content)
	blobPath := filepath.Join(h.storageRoot, "files", digest[0:2], digest[2:4], digest)
	if _, err := os.Stat(blobPath); err != nil {
		t.Errorf("soft delete removed the content from disk: %v", err)
	}

	if rec := c.do(http.MethodPost, "/api/v1/nodes/"+id.String()+"/restore", nil); rec.Code != http.StatusOK {
		t.Fatalf("restore status = %d: %s", rec.Code, rec.Body)
	}

	rec = c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/download", nil)
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Error("the restored file's content is wrong")
	}
}

func TestDeletingFolderTrashesItsSubtree(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	folder := c.mkdir("root", "Folder")
	nested := c.mkdir(folder.String(), "Nested")
	fileID, _ := c.upload(nested.String(), "deep.txt", []byte("deep content"))

	if rec := c.do(http.MethodDelete, "/api/v1/nodes/"+folder.String(), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete failed: %d", rec.Code)
	}

	// The descendant must be gone too.
	if rec := c.do(http.MethodGet, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusNotFound {
		t.Errorf("a descendant of a deleted folder is still reachable: %d", rec.Code)
	}

	// Trash lists the folder, not each descendant separately.
	rec := c.do(http.MethodGet, "/api/v1/trash", nil)
	var trash struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	}
	json.Unmarshal(rec.Body.Bytes(), &trash)
	if len(trash.Nodes) != 1 || trash.Nodes[0].Name != "Folder" {
		t.Errorf("trash should list only the deleted folder, got %+v", trash.Nodes)
	}

	// Restoring the folder brings the whole subtree back.
	if rec := c.do(http.MethodPost, "/api/v1/nodes/"+folder.String()+"/restore", nil); rec.Code != http.StatusOK {
		t.Fatalf("restore failed: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do(http.MethodGet, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusOK {
		t.Errorf("the descendant was not restored: %d", rec.Code)
	}
}

func TestPermanentDeleteRemovesContent(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	content := []byte("content to be destroyed permanently")
	id, _ := c.upload("root", "Doomed.txt", content)
	digest := sha256Hex(content)

	c.do(http.MethodDelete, "/api/v1/nodes/"+id.String(), nil)

	rec := c.do(http.MethodDelete, "/api/v1/nodes/"+id.String()+"?permanent=true", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("permanent delete status = %d: %s", rec.Code, rec.Body)
	}

	// Metadata gone.
	var nodeCount int
	h.db.Pool().QueryRow(t.Context(), `SELECT count(*) FROM nodes WHERE id = $1`, id).Scan(&nodeCount)
	if nodeCount != 0 {
		t.Error("the node row survived a permanent delete")
	}

	// Blob record gone, and the bytes with it.
	var blobCount int
	h.db.Pool().QueryRow(t.Context(), `SELECT count(*) FROM blobs WHERE hash = $1`, digest).Scan(&blobCount)
	if blobCount != 0 {
		t.Error("the blob record survived a permanent delete")
	}
	blobPath := filepath.Join(h.storageRoot, "files", digest[0:2], digest[2:4], digest)
	if _, err := os.Stat(blobPath); !os.IsNotExist(err) {
		t.Error("the content is still on disk after a permanent delete")
	}
}

// Deleting one copy must not destroy the content the other copy still uses.
func TestPermanentDeleteKeepsSharedContent(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	content := []byte("content shared by two nodes")
	first, _ := c.upload("root", "First.txt", content)
	second, _ := c.upload("root", "Second.txt", content)
	digest := sha256Hex(content)

	c.do(http.MethodDelete, "/api/v1/nodes/"+first.String()+"?permanent=true", nil)

	// The surviving file must still download correctly.
	rec := c.do(http.MethodGet, "/api/v1/nodes/"+second.String()+"/download", nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("the surviving copy was broken by deleting its twin: status %d", rec.Code)
	}

	var refCount int
	h.db.Pool().QueryRow(t.Context(), `SELECT ref_count FROM blobs WHERE hash = $1`, digest).Scan(&refCount)
	if refCount != 1 {
		t.Errorf("ref_count = %d, want 1", refCount)
	}
}

// --- Quota ------------------------------------------------------------------

func TestUploadIsRefusedWhenOverQuota(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	user, err := h.users.GetByUsername(t.Context(), "josh")
	if err != nil {
		t.Fatal(err)
	}
	quota := int64(100)
	quotaPtr := &quota
	if _, err := h.users.Update(t.Context(), user.ID, usersUpdateQuota(&quotaPtr)); err != nil {
		t.Fatal(err)
	}

	// Within the quota.
	if _, rec := c.upload("root", "small.txt", bytes.Repeat([]byte("a"), 50)); rec.Code != http.StatusCreated {
		t.Fatalf("an upload within quota was refused: %d %s", rec.Code, rec.Body)
	}

	// Over it.
	_, rec := c.upload("root", "big.txt", bytes.Repeat([]byte("b"), 200))
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507 when over quota; body: %s", rec.Code, rec.Body)
	}
	if body := decodeBody[api.ErrorResponse](t, rec); body.Error.Code != "quota_exceeded" {
		t.Errorf("code = %q, want quota_exceeded", body.Error.Code)
	}
}

func TestQuotaIsReleasedOnDelete(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "file.txt", bytes.Repeat([]byte("a"), 500))

	user, _ := h.users.GetByUsername(t.Context(), "josh")
	if user.UsedBytes != 500 {
		t.Fatalf("used_bytes = %d after upload, want 500", user.UsedBytes)
	}

	c.do(http.MethodDelete, "/api/v1/nodes/"+id.String(), nil)

	user, _ = h.users.GetByUsername(t.Context(), "josh")
	if user.UsedBytes != 0 {
		t.Errorf("used_bytes = %d after moving to trash, want 0", user.UsedBytes)
	}

	// Restoring charges it again.
	c.do(http.MethodPost, "/api/v1/nodes/"+id.String()+"/restore", nil)
	user, _ = h.users.GetByUsername(t.Context(), "josh")
	if user.UsedBytes != 500 {
		t.Errorf("used_bytes = %d after restore, want 500", user.UsedBytes)
	}
}

// The denormalised counter must be rebuildable from the actual rows.
func TestUsageCanBeRecalculated(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	c.upload("root", "a.txt", bytes.Repeat([]byte("a"), 100))
	c.upload("root", "b.txt", bytes.Repeat([]byte("b"), 250))

	user, _ := h.users.GetByUsername(t.Context(), "josh")

	// Corrupt the counter, as a bug or a crash mid-transaction might.
	if _, err := h.db.Pool().Exec(t.Context(),
		`UPDATE users SET used_bytes = 999999 WHERE id = $1`, user.ID); err != nil {
		t.Fatal(err)
	}

	total, err := h.nodes.RecalculateUsage(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if total != 350 {
		t.Errorf("recalculated usage = %d, want 350", total)
	}

	user, _ = h.users.GetByUsername(t.Context(), "josh")
	if user.UsedBytes != 350 {
		t.Errorf("stored usage = %d, want 350", user.UsedBytes)
	}
}
