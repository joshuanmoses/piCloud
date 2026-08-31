package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Public link tests.
//
// The portal is the most exposed surface in the product: unauthenticated,
// reachable by anyone with a URL, and pointed at somebody's private files.
// These tests are about what a visitor must NOT be able to reach.

type linkResult struct {
	ID                uuid.UUID `json:"id"`
	Kind              string    `json:"kind"`
	URL               string    `json:"url"`
	HasPassword       bool      `json:"has_password"`
	UseCount          int       `json:"use_count"`
	MaxUses           *int      `json:"max_uses"`
	Live              bool      `json:"live"`
	AllowedExtensions []string  `json:"allowed_extensions"`
	Node              *struct {
		Name string `json:"name"`
	} `json:"node"`
}

// publish creates a link and returns it with its token extracted from the URL.
func (c *client) publish(nodeID uuid.UUID, body map[string]any) (linkResult, string) {
	c.t.Helper()
	body["node_id"] = nodeID.String()

	rec := c.do(http.MethodPost, "/api/v1/public-links", body)
	if rec.Code != http.StatusCreated {
		c.t.Fatalf("publish failed: %d %s", rec.Code, rec.Body)
	}

	var out struct {
		Link linkResult `json:"link"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		c.t.Fatal(err)
	}

	parts := strings.Split(out.Link.URL, "/")
	return out.Link, parts[len(parts)-1]
}

func (c *client) publishRaw(nodeID uuid.UUID, body map[string]any) *httptest.ResponseRecorder {
	c.t.Helper()
	body["node_id"] = nodeID.String()
	return c.do(http.MethodPost, "/api/v1/public-links", body)
}

// visitor is an anonymous client with no session, as a link recipient is.
type visitor struct {
	t       testing.TB
	harness *harness
	cookies map[string]string
}

func (h *harness) newVisitor() *visitor {
	return &visitor{t: h.t, harness: h, cookies: map[string]string{}}
}

func (v *visitor) request(method, path string, body any, contentType string) *httptest.ResponseRecorder {
	v.t.Helper()

	var reader *strings.Reader
	switch typed := body.(type) {
	case nil:
		reader = strings.NewReader("")
	case string:
		reader = strings.NewReader(typed)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			v.t.Fatal(err)
		}
		reader = strings.NewReader(string(encoded))
		contentType = "application/json"
	}

	req := httptest.NewRequest(method, path, reader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.RemoteAddr = "203.0.113.50:44444" // a public address: a real visitor
	for name, value := range v.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}

	rec := httptest.NewRecorder()
	v.harness.server.ServeHTTP(rec, req)

	for _, cookie := range rec.Result().Cookies() {
		if cookie.MaxAge < 0 {
			delete(v.cookies, cookie.Name)
			continue
		}
		v.cookies[cookie.Name] = cookie.Value
	}
	return rec
}

func (v *visitor) open(token string) *httptest.ResponseRecorder {
	return v.request(http.MethodGet, "/api/v1/portal/"+token, nil, "")
}

type portalView struct {
	NeedsPassword bool   `json:"needs_password"`
	Kind          string `json:"kind"`
	Title         string `json:"title"`
	Message       string `json:"message"`
	RequestedBy   string `json:"requested_by"`
	Destination   string `json:"destination"`
	Item          *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	} `json:"item"`
	Items []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"items"`
	CanDownload       bool     `json:"can_download"`
	CanUpload         bool     `json:"can_upload"`
	MaxUploadBytes    int64    `json:"max_upload_bytes"`
	AllowedExtensions []string `json:"allowed_extensions"`
	RemainingUses     int      `json:"remaining_uses"`
}

func decodePortal(t *testing.T, rec *httptest.ResponseRecorder) portalView {
	t.Helper()
	var out portalView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("portal response was not valid JSON (%s): %v", rec.Body, err)
	}
	return out
}

// --- Share links ------------------------------------------------------------

func TestPublicLinkGivesAnonymousAccess(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	content := []byte("the shared document")
	fileID, rec := alice.upload("root", "Report.txt", content)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup upload failed: %d", rec.Code)
	}

	link, token := alice.publish(fileID, map[string]any{"kind": "download"})
	if link.Kind != "download" {
		t.Errorf("kind = %q", link.Kind)
	}
	if !strings.Contains(link.URL, "/s/") {
		t.Errorf("a share link should use /s/: %q", link.URL)
	}

	// A visitor with no account can see and fetch it.
	v := h.newVisitor()
	view := decodePortal(t, v.open(token))
	if view.NeedsPassword {
		t.Error("an unprotected link should not ask for a password")
	}
	if view.Item == nil || view.Item.Name != "Report.txt" {
		t.Fatalf("the portal did not describe the file: %+v", view.Item)
	}
	if !view.CanDownload {
		t.Error("a download link should permit downloading")
	}

	download := v.request(http.MethodGet, "/api/v1/portal/"+token+"/download", nil, "")
	if download.Code != http.StatusOK {
		t.Fatalf("download returned %d", download.Code)
	}
	if !bytes.Equal(download.Body.Bytes(), content) {
		t.Error("the downloaded content is wrong")
	}
}

// The token is shown once and never again, because only a hash is stored.
func TestTokenIsNeverStoredOrReturnedAgain(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	fileID, _ := alice.upload("root", "file.txt", []byte("content"))
	link, token := alice.publish(fileID, map[string]any{"kind": "download"})

	// Not in the database.
	var matches int
	if err := h.db.Pool().QueryRow(t.Context(),
		`SELECT count(*) FROM public_links WHERE encode(token_hash, 'escape') LIKE '%' || $1 || '%'`,
		token).Scan(&matches); err != nil {
		t.Fatal(err)
	}
	if matches != 0 {
		t.Fatal("the raw token appears in the public_links table")
	}

	// Not in any later listing.
	rec := alice.do(http.MethodGet, "/api/v1/public-links", nil)
	if strings.Contains(rec.Body.String(), token) {
		t.Error("the token was returned again in the links listing")
	}
	_ = link
}

// A view-only link must not permit fetching the bytes.
func TestViewOnlyLinkCannotDownload(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	fileID, _ := alice.upload("root", "file.txt", []byte("secret content"))
	_, token := alice.publish(fileID, map[string]any{"kind": "view"})

	v := h.newVisitor()
	view := decodePortal(t, v.open(token))
	if view.CanDownload {
		t.Error("a view-only link should not report downloading as available")
	}

	rec := v.request(http.MethodGet, "/api/v1/portal/"+token+"/download", nil, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("download returned %d, want 403", rec.Code)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("secret content")) {
		t.Fatal("the content was disclosed despite the download being refused")
	}
}

// Sharing a folder lets a visitor fetch the files inside it — and nothing else.
func TestFolderLinkExposesOnlyItsOwnContents(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	shared := alice.mkdir("root", "Public")
	insideID, _ := alice.upload(shared.String(), "inside.txt", []byte("shared file"))

	// Something the link must never reach.
	privateID, _ := alice.upload("root", "private.txt", []byte("not shared"))

	_, token := alice.publish(shared, map[string]any{"kind": "download"})

	v := h.newVisitor()
	view := decodePortal(t, v.open(token))

	if len(view.Items) != 1 || view.Items[0].Name != "inside.txt" {
		t.Fatalf("the portal listed %+v, want just inside.txt", view.Items)
	}

	// The file inside is fetchable.
	rec := v.request(http.MethodGet,
		"/api/v1/portal/"+token+"/download?item="+insideID.String(), nil, "")
	if rec.Code != http.StatusOK || rec.Body.String() != "shared file" {
		t.Fatalf("could not fetch the file inside the shared folder: %d", rec.Code)
	}

	// A node outside the folder must not be reachable, even with a valid token.
	// This is the containment guarantee: without it, any link would be a way to
	// read the whole instance.
	rec = v.request(http.MethodGet,
		"/api/v1/portal/"+token+"/download?item="+privateID.String(), nil, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — a link must not reach outside its folder", rec.Code)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("not shared")) {
		t.Fatal("content outside the link was disclosed")
	}

	// Nor a nonexistent one.
	rec = v.request(http.MethodGet,
		"/api/v1/portal/"+token+"/download?item="+uuid.NewString(), nil, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// --- Password protection ----------------------------------------------------

func TestPasswordProtectedLink(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	fileID, _ := alice.upload("root", "confidential.txt", []byte("the secret contents"))
	link, token := alice.publish(fileID, map[string]any{
		"kind": "download", "password": "a reasonably long link password",
	})
	if !link.HasPassword {
		t.Error("the link should report that it is protected")
	}

	v := h.newVisitor()

	// Before the password, the portal reveals nothing at all.
	view := decodePortal(t, v.open(token))
	if !view.NeedsPassword {
		t.Fatal("the portal should ask for a password")
	}
	if view.Item != nil || view.Kind != "" || view.Title != "" {
		t.Errorf("the portal leaked detail before the password: %+v", view)
	}
	body := v.open(token).Body.String()
	if strings.Contains(body, "confidential") {
		t.Error("the filename leaked before the password was entered")
	}

	// Downloading without it is refused.
	if rec := v.request(http.MethodGet, "/api/v1/portal/"+token+"/download", nil, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("download without the password returned %d, want 401", rec.Code)
	}

	// A wrong password is refused.
	rec := v.request(http.MethodPost, "/api/v1/portal/"+token+"/password",
		map[string]any{"password": "not the password"}, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong password returned %d, want 401", rec.Code)
	}

	// The right one issues an access grant.
	rec = v.request(http.MethodPost, "/api/v1/portal/"+token+"/password",
		map[string]any{"password": "a reasonably long link password"}, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("the correct password returned %d, want 204; body: %s", rec.Code, rec.Body)
	}

	// Now everything works.
	view = decodePortal(t, v.open(token))
	if view.NeedsPassword {
		t.Fatal("the portal still asks for a password after a successful entry")
	}
	if view.Item == nil || view.Item.Name != "confidential.txt" {
		t.Fatalf("the portal did not describe the file: %+v", view.Item)
	}

	download := v.request(http.MethodGet, "/api/v1/portal/"+token+"/download", nil, "")
	if download.Body.String() != "the secret contents" {
		t.Error("the download after the password is wrong")
	}
}

// An access grant is bound to one link, so entering the password for one does
// not open another.
func TestAccessGrantIsBoundToItsLink(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	firstID, _ := alice.upload("root", "first.txt", []byte("first"))
	secondID, _ := alice.upload("root", "second.txt", []byte("second"))

	_, firstToken := alice.publish(firstID, map[string]any{
		"kind": "download", "password": "the first link password",
	})
	_, secondToken := alice.publish(secondID, map[string]any{
		"kind": "download", "password": "the second link password",
	})

	v := h.newVisitor()
	rec := v.request(http.MethodPost, "/api/v1/portal/"+firstToken+"/password",
		map[string]any{"password": "the first link password"}, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("password entry failed: %d", rec.Code)
	}

	// Take the grant from the first link and present it to the second.
	grant := v.cookies["pifiles_link_access"]
	if grant == "" {
		t.Fatal("no access grant was issued")
	}

	forged := h.newVisitor()
	forged.cookies["pifiles_link_access"] = grant

	view := decodePortal(t, forged.open(secondToken))
	if !view.NeedsPassword {
		t.Fatal("a grant for one link opened another")
	}
}

// --- Expiry and usage limits ------------------------------------------------

func TestExpiredLinkIsIndistinguishableFromUnknown(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	fileID, _ := alice.upload("root", "file.txt", []byte("content"))
	link, token := alice.publish(fileID, map[string]any{
		"kind": "download", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	})

	v := h.newVisitor()
	if rec := v.open(token); rec.Code != http.StatusOK {
		t.Fatalf("setup: the link should work, got %d", rec.Code)
	}

	if _, err := h.db.Pool().Exec(t.Context(),
		`UPDATE public_links SET expires_at = now() - interval '1 minute' WHERE id = $1`,
		link.ID); err != nil {
		t.Fatal(err)
	}

	expired := v.open(token)
	unknown := v.open(strings.Repeat("a", 43))

	if expired.Code != http.StatusNotFound {
		t.Fatalf("an expired link returned %d, want 404", expired.Code)
	}
	// The two must be byte-identical, or probing tells an attacker when they
	// have found a real token.
	if expired.Code != unknown.Code {
		t.Errorf("status differs: expired %d, unknown %d", expired.Code, unknown.Code)
	}
	var a, b struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(expired.Body.Bytes(), &a)
	json.Unmarshal(unknown.Body.Bytes(), &b)
	if a.Error != b.Error {
		t.Errorf("an expired link is distinguishable from an unknown one:\n  expired: %+v\n  unknown: %+v", a.Error, b.Error)
	}
}

func TestLinkRejectsPastExpiry(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	fileID, _ := alice.upload("root", "file.txt", []byte("content"))
	rec := alice.publishRaw(fileID, map[string]any{
		"kind": "download", "expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339),
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 for an expiry in the past", rec.Code)
	}
}

func TestDownloadLimitIsEnforced(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	fileID, _ := alice.upload("root", "file.txt", []byte("content"))
	_, token := alice.publish(fileID, map[string]any{"kind": "download", "max_uses": 2})

	v := h.newVisitor()
	for i := 1; i <= 2; i++ {
		if rec := v.request(http.MethodGet, "/api/v1/portal/"+token+"/download", nil, ""); rec.Code != http.StatusOK {
			t.Fatalf("download %d returned %d, want 200", i, rec.Code)
		}
	}

	// The third is refused, and the link becomes indistinguishable from an
	// unknown one.
	if rec := v.request(http.MethodGet, "/api/v1/portal/"+token+"/download", nil, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("the third download returned %d, want 404", rec.Code)
	}
	if rec := v.open(token); rec.Code != http.StatusNotFound {
		t.Errorf("a used-up link is still openable: %d", rec.Code)
	}
}

// --- Revocation -------------------------------------------------------------

func TestRevokingALinkStopsItImmediately(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	fileID, _ := alice.upload("root", "file.txt", []byte("content"))
	link, token := alice.publish(fileID, map[string]any{"kind": "download"})

	v := h.newVisitor()
	if rec := v.open(token); rec.Code != http.StatusOK {
		t.Fatalf("setup: the link should work, got %d", rec.Code)
	}

	if rec := alice.do(http.MethodDelete, "/api/v1/public-links/"+link.ID.String(), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke failed: %d %s", rec.Code, rec.Body)
	}

	if rec := v.open(token); rec.Code != http.StatusNotFound {
		t.Fatalf("the link still works after revocation: %d", rec.Code)
	}
	if rec := v.request(http.MethodGet, "/api/v1/portal/"+token+"/download", nil, ""); rec.Code != http.StatusNotFound {
		t.Errorf("the download still works after revocation: %d", rec.Code)
	}
}

// Deleting the content must take the link with it.
func TestDeletingContentDisablesItsLink(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	fileID, _ := alice.upload("root", "file.txt", []byte("content"))
	_, token := alice.publish(fileID, map[string]any{"kind": "download"})

	alice.do(http.MethodDelete, "/api/v1/nodes/"+fileID.String(), nil)

	v := h.newVisitor()
	if rec := v.open(token); rec.Code != http.StatusNotFound {
		t.Fatalf("a link to trashed content still works: %d", rec.Code)
	}
}

func TestAnotherUserCannotRevokeYourLink(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	mallory := h.signedIn("mallory")

	fileID, _ := alice.upload("root", "file.txt", []byte("content"))
	link, token := alice.publish(fileID, map[string]any{"kind": "download"})

	rec := mallory.do(http.MethodDelete, "/api/v1/public-links/"+link.ID.String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	// And the link still works.
	if rec := h.newVisitor().open(token); rec.Code != http.StatusOK {
		t.Error("the link was revoked by somebody else")
	}
}

// --- File requests ----------------------------------------------------------

func TestFileRequestAcceptsUploadsWithoutAnAccount(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	folder := alice.mkdir("root", "2026 Tax Documents")
	link, token := alice.publish(folder, map[string]any{
		"kind":    "upload",
		"title":   "Upload your documents",
		"message": "Please send this year's receipts.",
	})
	if link.Kind != "upload" {
		t.Errorf("kind = %q, want upload", link.Kind)
	}
	if !strings.Contains(link.URL, "/u/") {
		t.Errorf("a file request should use /u/: %q", link.URL)
	}

	v := h.newVisitor()
	view := decodePortal(t, v.open(token))

	if !view.CanUpload {
		t.Error("a file request should permit uploading")
	}
	if view.Destination != "2026 Tax Documents" {
		t.Errorf("destination = %q", view.Destination)
	}
	if view.RequestedBy != "alice" {
		t.Errorf("requested_by = %q, want the publisher's display name", view.RequestedBy)
	}
	if view.Title != "Upload your documents" || view.Message == "" {
		t.Errorf("the portal did not carry its title and message: %+v", view)
	}
	if view.MaxUploadBytes <= 0 {
		t.Error("the portal should publish the size limit")
	}

	content := []byte("my receipts for the year")
	rec := v.request(http.MethodPost,
		"/api/v1/portal/"+token+"/upload?name=receipts.txt", string(content), "application/octet-stream")
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload returned %d: %s", rec.Code, rec.Body)
	}

	// The file landed in the folder, owned by Alice.
	names := alice.listNames(folder.String())
	if len(names) != 1 || names[0] != "receipts.txt" {
		t.Fatalf("the folder contains %v", names)
	}

	// And it counts against Alice's quota, since the uploader has no account.
	aliceUser, _ := h.users.GetByUsername(t.Context(), "alice")
	if aliceUser.UsedBytes != int64(len(content)) {
		t.Errorf("Alice's usage = %d, want %d", aliceUser.UsedBytes, len(content))
	}
}

// This is the central file-request guarantee: somebody handed the link must
// not be able to see what anyone else submitted.
func TestFileRequestVisitorCannotSeeAnything(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	folder := alice.mkdir("root", "Submissions")
	existingID, _ := alice.upload(folder.String(), "someone-elses.txt", []byte("private submission"))
	_, token := alice.publish(folder, map[string]any{"kind": "upload"})

	v := h.newVisitor()
	rec := v.open(token)
	view := decodePortal(t, rec)

	// Nothing about the contents.
	if len(view.Items) != 0 {
		t.Errorf("the portal listed contents: %+v", view.Items)
	}
	if view.Item != nil {
		t.Errorf("the portal described the folder's contents: %+v", view.Item)
	}
	if view.CanDownload {
		t.Error("an upload-only portal must not report downloading as available")
	}
	if strings.Contains(rec.Body.String(), "someone-elses") {
		t.Error("the portal leaked another submission's filename")
	}

	// And downloading is refused, both for the folder and for a known child.
	if rec := v.request(http.MethodGet, "/api/v1/portal/"+token+"/download", nil, ""); rec.Code != http.StatusForbidden {
		t.Errorf("download returned %d, want 403", rec.Code)
	}
	rec = v.request(http.MethodGet,
		"/api/v1/portal/"+token+"/download?item="+existingID.String(), nil, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("downloading a known child returned %d, want 403", rec.Code)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("private submission")) {
		t.Fatal("an upload portal disclosed an existing submission")
	}
}

// Two visitors uploading the same filename must both succeed; the second is
// renamed rather than refused, because a visitor cannot see what is there.
func TestFileRequestRenamesCollidingUploads(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	folder := alice.mkdir("root", "Submissions")
	_, token := alice.publish(folder, map[string]any{"kind": "upload"})

	for i := range 3 {
		v := h.newVisitor()
		rec := v.request(http.MethodPost,
			"/api/v1/portal/"+token+"/upload?name=form.txt",
			fmt.Sprintf("submission %d", i), "application/octet-stream")
		if rec.Code != http.StatusCreated {
			t.Fatalf("upload %d returned %d: %s", i, rec.Code, rec.Body)
		}
	}

	names := alice.listNames(folder.String())
	if len(names) != 3 {
		t.Fatalf("expected 3 files, got %v", names)
	}
	// The names are distinct.
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			t.Errorf("duplicate name %q", name)
		}
		seen[name] = true
	}
}

func TestFileRequestEnforcesTypeFilter(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	folder := alice.mkdir("root", "PDFs only")
	_, token := alice.publish(folder, map[string]any{
		"kind": "upload", "allowed_extensions": []string{"pdf", ".jpg"},
	})

	v := h.newVisitor()
	view := decodePortal(t, v.open(token))
	if len(view.AllowedExtensions) != 2 {
		t.Errorf("the portal should publish the filter: %+v", view.AllowedExtensions)
	}

	rec := v.request(http.MethodPost,
		"/api/v1/portal/"+token+"/upload?name=malware.exe", "payload", "application/octet-stream")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a filtered type returned %d, want 422", rec.Code)
	}

	rec = v.request(http.MethodPost,
		"/api/v1/portal/"+token+"/upload?name=report.pdf", "content", "application/octet-stream")
	if rec.Code != http.StatusCreated {
		t.Errorf("an allowed type returned %d: %s", rec.Code, rec.Body)
	}
}

func TestFileRequestEnforcesItsSizeLimit(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	folder := alice.mkdir("root", "Small files")
	_, token := alice.publish(folder, map[string]any{
		"kind": "upload", "max_upload_bytes": 100,
	})

	v := h.newVisitor()
	rec := v.request(http.MethodPost,
		"/api/v1/portal/"+token+"/upload?name=big.txt",
		strings.Repeat("x", 500), "application/octet-stream")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("an oversized upload returned %d, want 413", rec.Code)
	}

	rec = v.request(http.MethodPost,
		"/api/v1/portal/"+token+"/upload?name=small.txt",
		strings.Repeat("x", 50), "application/octet-stream")
	if rec.Code != http.StatusCreated {
		t.Errorf("an acceptable upload returned %d: %s", rec.Code, rec.Body)
	}
}

// A share link must not accept uploads.
func TestDownloadLinkRejectsUploads(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	folder := alice.mkdir("root", "Read only")
	_, token := alice.publish(folder, map[string]any{"kind": "download"})

	v := h.newVisitor()
	rec := v.request(http.MethodPost,
		"/api/v1/portal/"+token+"/upload?name=planted.txt", "hostile", "application/octet-stream")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if names := alice.listNames(folder.String()); len(names) != 0 {
		t.Errorf("a file was planted through a download link: %v", names)
	}
}

// An upload portal must point at a folder; a file makes no sense.
func TestFileRequestRequiresAFolder(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	fileID, _ := alice.upload("root", "file.txt", []byte("content"))
	rec := alice.publishRaw(fileID, map[string]any{"kind": "upload"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a file request on a file", rec.Code)
	}
}

// --- Authority to publish ---------------------------------------------------

func TestCannotPublishSomebodyElsesContent(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	mallory := h.signedIn("mallory")

	fileID, _ := alice.upload("root", "private.txt", []byte("alice's file"))

	rec := mallory.publishRaw(fileID, map[string]any{"kind": "download"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — Mallory published a file she cannot see", rec.Code)
	}

	var count int
	h.db.Pool().QueryRow(t.Context(),
		`SELECT count(*) FROM public_links WHERE node_id = $1`, fileID).Scan(&count)
	if count != 0 {
		t.Errorf("%d links were created", count)
	}
}

// A recipient without the share permission cannot publish what they were given.
func TestViewerCannotPublishALink(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	fileID, _ := alice.upload("root", "file.txt", []byte("content"))
	alice.share(fileID, map[string]any{"username": "bob", "permissions": []string{"viewer"}})

	rec := bob.publishRaw(fileID, map[string]any{"kind": "download"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — a viewer must not publish a public link", rec.Code)
	}
}

// Publishing must not grant more than the publisher holds.
func TestCannotPublishMoreThanYouHold(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	folder := alice.mkdir("root", "Folder")
	// Read plus share, but no download.
	alice.share(folder, map[string]any{
		"username": "bob", "permissions": []string{"read", "share"},
	})

	// A view link is within Bob's rights.
	if rec := bob.publishRaw(folder, map[string]any{"kind": "view"}); rec.Code != http.StatusCreated {
		t.Fatalf("Bob should be able to publish what he holds: %d %s", rec.Code, rec.Body)
	}

	// A download link is not.
	rec := bob.publishRaw(folder, map[string]any{"kind": "download"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — Bob published a permission he does not hold", rec.Code)
	}
}

func TestCannotPublishYourRootFolder(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	rec := alice.do(http.MethodGet, "/api/v1/nodes", nil)
	var listing struct {
		Folder struct {
			ID uuid.UUID `json:"id"`
		} `json:"folder"`
	}
	json.Unmarshal(rec.Body.Bytes(), &listing)

	result := alice.publishRaw(listing.Folder.ID, map[string]any{"kind": "download"})
	if result.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", result.Code)
	}
}

// --- Portal hygiene ---------------------------------------------------------

// The portal is unauthenticated and pointed at private files. It must leak
// nothing about the instance.
func TestPortalLeaksNothingAboutTheInstance(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	folder := alice.mkdir("root", "Public")
	alice.upload(folder.String(), "file.txt", []byte("content"))
	_, token := alice.publish(folder, map[string]any{"kind": "download"})

	v := h.newVisitor()
	body := v.open(token).Body.String()

	// No username, no storage path, no owner identifier, no content hash.
	for _, forbidden := range []string{
		"alice", h.storageRoot, "content_hash", "owner_id", "/files/", "password_hash",
	} {
		if forbidden != "" && strings.Contains(body, forbidden) {
			t.Errorf("the portal leaked %q: %s", forbidden, body)
		}
	}
}

// Uploaded content served through a link must carry the same protections as
// through the authenticated API.
func TestPortalDownloadCarriesSecurityHeaders(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	hostile := []byte("<!DOCTYPE html><script>alert(document.cookie)</script>")
	fileID, _ := alice.upload("root", "attack.html", hostile)
	_, token := alice.publish(fileID, map[string]any{"kind": "download"})

	v := h.newVisitor()
	rec := v.request(http.MethodGet, "/api/v1/portal/"+token+"/download", nil, "")

	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("a portal download must be marked nosniff")
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Errorf("a portal download should carry a sandboxing CSP, got %q", csp)
	}
	if ct := strings.ToLower(rec.Header().Get("Content-Type")); strings.Contains(ct, "html") {
		t.Errorf("uploaded HTML was served as %q through a public link", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "private") {
		t.Errorf("Cache-Control = %q; a shared cache must not hold this", cc)
	}
}

// A malformed token must be rejected without reaching the database.
func TestMalformedTokensAreRejectedUniformly(t *testing.T) {
	h := newHarness(t)
	v := h.newVisitor()

	// Percent-encoded so each is a valid request target; the server still sees
	// the decoded value as the token.
	for _, token := range []string{
		"short", "../../../etc/passwd", "' OR 1=1--",
		strings.Repeat("a", 200), "..%2f..%2fetc", "null", "",
		strings.Repeat("a", 42) + "/", "\x00",
	} {
		rec := v.request(http.MethodGet, "/api/v1/portal/"+urlEncode(token), nil, "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("token %q returned %d, want 404", token, rec.Code)
		}
		// And nothing about the instance comes back.
		if strings.Contains(rec.Body.String(), "root:") || strings.Contains(rec.Body.String(), "passwd") {
			t.Errorf("token %q produced a suspicious response: %s", token, rec.Body)
		}
	}
}

// The portal must not require or accept a session: it exists for people who
// have no account.
func TestPortalWorksWithNoSessionAndNoCSRF(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	folder := alice.mkdir("root", "Drop")
	_, token := alice.publish(folder, map[string]any{"kind": "upload"})

	// A visitor with no cookies at all, and no CSRF header.
	v := h.newVisitor()
	rec := v.request(http.MethodPost,
		"/api/v1/portal/"+token+"/upload?name=file.txt", "content", "application/octet-stream")
	if rec.Code != http.StatusCreated {
		t.Fatalf("an anonymous upload returned %d: %s", rec.Code, rec.Body)
	}
}

// The owner's listing shows their links without ever re-exposing a token.
func TestOwnerCanListTheirLinks(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	fileID, _ := alice.upload("root", "file.txt", []byte("content"))
	alice.publish(fileID, map[string]any{"kind": "download"})

	rec := alice.do(http.MethodGet, "/api/v1/public-links", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Links []linkResult `json:"links"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)

	if len(body.Links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(body.Links))
	}
	if body.Links[0].URL != "" {
		t.Error("the listing re-exposed a link URL, which contains the token")
	}
	if body.Links[0].Node == nil || body.Links[0].Node.Name != "file.txt" {
		t.Errorf("the listing should name the content: %+v", body.Links[0].Node)
	}

	// Somebody else's listing is empty.
	rec = bob.do(http.MethodGet, "/api/v1/public-links", nil)
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Links) != 0 {
		t.Errorf("another user saw %d links", len(body.Links))
	}
}
