package tests

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/api"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
)

// Sharing and permission tests.
//
// These are the most important tests in the project. Each one describes a
// concrete way somebody could reach content they are not entitled to, or be
// denied content they are.

type shareResponse struct {
	ID          uuid.UUID `json:"id"`
	Permissions []string  `json:"permissions"`
	Role        string    `json:"role"`
	Inherit     bool      `json:"inherit"`
	Node        struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	} `json:"node"`
	User *struct {
		Username string `json:"username"`
	} `json:"user"`
	Group *struct {
		Name string `json:"name"`
	} `json:"group"`
	SharedBy *struct {
		Username string `json:"username"`
	} `json:"shared_by"`
}

// share creates a share and returns it, failing the test on a non-201.
func (c *client) share(nodeID uuid.UUID, body map[string]any) shareResponse {
	c.t.Helper()
	body["node_id"] = nodeID.String()

	rec := c.do(http.MethodPost, "/api/v1/shares", body)
	if rec.Code != http.StatusCreated {
		c.t.Fatalf("share failed: %d %s", rec.Code, rec.Body)
	}

	var out struct {
		Share shareResponse `json:"share"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		c.t.Fatal(err)
	}
	return out.Share
}

// shareRaw creates a share and returns the raw response, for failure cases.
func (c *client) shareRaw(nodeID uuid.UUID, body map[string]any) *httptest.ResponseRecorder {
	c.t.Helper()
	body["node_id"] = nodeID.String()
	return c.do(http.MethodPost, "/api/v1/shares", body)
}

// listNodes returns the names visible in a folder.
func (c *client) listNames(parentID string) []string {
	c.t.Helper()
	path := "/api/v1/nodes"
	if parentID != "" {
		path += "?parent_id=" + parentID
	}
	rec := c.do(http.MethodGet, path, nil)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("list failed: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		c.t.Fatal(err)
	}
	names := make([]string, 0, len(body.Nodes))
	for _, n := range body.Nodes {
		names = append(names, n.Name)
	}
	return names
}

// --- Basic sharing ----------------------------------------------------------

func TestShareFileWithUserGrantsAccess(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	fileID, rec := alice.upload("root", "budget.txt", []byte("the quarterly numbers"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup upload failed: %d", rec.Code)
	}

	// Before sharing, Bob cannot see it at all.
	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("before sharing, Bob got %d; want 404", rec.Code)
	}

	share := alice.share(fileID, map[string]any{"username": "bob", "permissions": []string{"viewer"}})
	if share.Role != "viewer" {
		t.Errorf("role = %q, want viewer", share.Role)
	}

	// Now he can read and download it.
	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusOK {
		t.Fatalf("after sharing, Bob got %d; want 200", rec.Code)
	}
	rec = bob.do(http.MethodGet, "/api/v1/nodes/"+fileID.String()+"/download", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "the quarterly numbers" {
		t.Fatalf("Bob could not download the shared file: %d", rec.Code)
	}
}

// A viewer must not be able to change anything.
func TestViewerCannotModify(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	fileID, _ := alice.upload("root", "readonly.txt", []byte("content"))
	alice.share(fileID, map[string]any{"username": "bob", "permissions": []string{"viewer"}})

	attempts := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"rename", http.MethodPatch, "/api/v1/nodes/" + fileID.String(), map[string]any{"name": "hijacked.txt"}},
		{"delete", http.MethodDelete, "/api/v1/nodes/" + fileID.String(), nil},
	}

	for _, attempt := range attempts {
		t.Run(attempt.name, func(t *testing.T) {
			rec := bob.do(attempt.method, attempt.path, attempt.body)
			// Bob can see the item, so a 403 naming the missing permission is
			// clearer than a 404 for something in front of him.
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s returned %d, want 403", attempt.name, rec.Code)
			}
		})
	}

	// And the file is untouched.
	rec := alice.do(http.MethodGet, "/api/v1/nodes/"+fileID.String(), nil)
	var body struct {
		Node struct {
			Name string `json:"name"`
		} `json:"node"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Node.Name != "readonly.txt" {
		t.Errorf("the file was modified: name is now %q", body.Node.Name)
	}
}

// A grant of Read without Download must not permit fetching the content.
func TestReadWithoutDownloadCannotFetchContent(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	fileID, _ := alice.upload("root", "secret.txt", []byte("confidential"))
	alice.share(fileID, map[string]any{"username": "bob", "permissions": []string{"read"}})

	// He can see that it exists...
	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusOK {
		t.Fatalf("Bob should be able to see the metadata: %d", rec.Code)
	}
	// ...but not read it.
	rec := bob.do(http.MethodGet, "/api/v1/nodes/"+fileID.String()+"/download", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("download returned %d, want 403", rec.Code)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("confidential")) {
		t.Fatal("the content was disclosed despite the download being refused")
	}
}

// --- Inheritance ------------------------------------------------------------

// A grant on a folder must reach everything inside it, at any depth.
func TestFolderShareInheritsToDescendants(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	outer := alice.mkdir("root", "Projects")
	inner := alice.mkdir(outer.String(), "PiFiles")
	deep := alice.mkdir(inner.String(), "Docs")
	fileID, _ := alice.upload(deep.String(), "notes.txt", []byte("deep content"))

	alice.share(outer, map[string]any{"username": "bob", "permissions": []string{"viewer"}})

	// Every level is reachable.
	for name, id := range map[string]uuid.UUID{
		"the shared folder": outer,
		"a child folder":    inner,
		"a grandchild":      deep,
		"a file three deep": fileID,
	} {
		if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+id.String(), nil); rec.Code != http.StatusOK {
			t.Errorf("%s returned %d, want 200", name, rec.Code)
		}
	}

	rec := bob.do(http.MethodGet, "/api/v1/nodes/"+fileID.String()+"/download", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "deep content" {
		t.Errorf("Bob could not download the inherited file: %d", rec.Code)
	}
}

// A grant with inherit=false must apply to exactly one node.
func TestNonInheritingShareDoesNotReachChildren(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	folder := alice.mkdir("root", "Folder")
	child, _ := alice.upload(folder.String(), "child.txt", []byte("content"))

	alice.share(folder, map[string]any{
		"username": "bob", "permissions": []string{"viewer"}, "inherit": false,
	})

	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+folder.String(), nil); rec.Code != http.StatusOK {
		t.Fatalf("the folder itself should be visible: %d", rec.Code)
	}
	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+child.String(), nil); rec.Code != http.StatusNotFound {
		t.Errorf("a non-inheriting grant reached a child: %d", rec.Code)
	}
}

// Content added to a shared folder afterwards must be covered by the existing
// grant, without re-sharing.
func TestNewContentInSharedFolderIsCovered(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	folder := alice.mkdir("root", "Shared")
	alice.share(folder, map[string]any{"username": "bob", "permissions": []string{"viewer"}})

	// Added after the share was created.
	later, _ := alice.upload(folder.String(), "added-later.txt", []byte("new content"))

	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+later.String(), nil); rec.Code != http.StatusOK {
		t.Errorf("content added after sharing was not covered: %d", rec.Code)
	}
}

// --- Contributor role -------------------------------------------------------

// A contributor can add content but must not be able to alter or remove what
// is already there.
func TestContributorCanUploadButNotDestroy(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	folder := alice.mkdir("root", "Dropbox")
	existing, _ := alice.upload(folder.String(), "existing.txt", []byte("do not touch"))

	alice.share(folder, map[string]any{"username": "bob", "permissions": []string{"contributor"}})

	// He can add.
	if _, rec := bob.upload(folder.String(), "submission.txt", []byte("my submission")); rec.Code != http.StatusCreated {
		t.Fatalf("a contributor could not upload: %d %s", rec.Code, rec.Body)
	}
	if rec := bob.do(http.MethodPost, "/api/v1/nodes/folders",
		map[string]any{"parent_id": folder.String(), "name": "My subfolder"}); rec.Code != http.StatusCreated {
		t.Errorf("a contributor could not create a folder: %d %s", rec.Code, rec.Body)
	}

	// He cannot destroy.
	if rec := bob.do(http.MethodDelete, "/api/v1/nodes/"+existing.String(), nil); rec.Code != http.StatusForbidden {
		t.Errorf("a contributor deleted existing content: %d", rec.Code)
	}
	if rec := bob.do(http.MethodPatch, "/api/v1/nodes/"+existing.String(),
		map[string]any{"name": "renamed.txt"}); rec.Code != http.StatusForbidden {
		t.Errorf("a contributor renamed existing content: %d", rec.Code)
	}
	// Nor overwrite it.
	rec := bob.raw(http.MethodPost,
		"/api/v1/files?name=existing.txt&parent_id="+folder.String()+"&replace_id="+existing.String(),
		"application/octet-stream", bytes.NewReader([]byte("overwritten")))
	if rec.Code != http.StatusForbidden {
		t.Errorf("a contributor overwrote existing content: %d", rec.Code)
	}

	// The original is intact.
	rec = alice.do(http.MethodGet, "/api/v1/nodes/"+existing.String()+"/download", nil)
	if rec.Body.String() != "do not touch" {
		t.Error("the existing file was modified")
	}
}

// Content uploaded into somebody else's shared folder belongs to that folder's
// owner, and counts against their quota — not the uploader's.
func TestUploadIntoSharedFolderBelongsToTheOwner(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	folder := alice.mkdir("root", "Dropbox")
	alice.share(folder, map[string]any{"username": "bob", "permissions": []string{"contributor"}})

	content := bytes.Repeat([]byte("x"), 1000)
	fileID, rec := bob.upload(folder.String(), "from-bob.txt", content)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload failed: %d %s", rec.Code, rec.Body)
	}

	// Alice owns it.
	node, err := h.nodes.Get(t.Context(), fileID)
	if err != nil {
		t.Fatal(err)
	}
	aliceUser, _ := h.users.GetByUsername(t.Context(), "alice")
	if node.OwnerID != aliceUser.ID {
		t.Error("content uploaded into a shared folder should belong to the folder's owner")
	}

	// And it counts against Alice's quota, not Bob's.
	aliceUser, _ = h.users.GetByUsername(t.Context(), "alice")
	bobUser, _ := h.users.GetByUsername(t.Context(), "bob")
	if aliceUser.UsedBytes != 1000 {
		t.Errorf("Alice's usage = %d, want 1000", aliceUser.UsedBytes)
	}
	if bobUser.UsedBytes != 0 {
		t.Errorf("Bob's usage = %d, want 0", bobUser.UsedBytes)
	}
}

// --- Privilege escalation ---------------------------------------------------

// This is the central escalation test: a recipient must not be able to grant
// more than they hold.
func TestCannotGrantMoreThanYouHold(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")
	h.signedIn("carol")

	folder := alice.mkdir("root", "Documents")

	// Bob is a manager, so he may share onward — but only what he has.
	alice.share(folder, map[string]any{"username": "bob", "permissions": []string{"manager"}})

	// A manager holds everything except ownership, so this succeeds.
	if rec := bob.shareRaw(folder, map[string]any{
		"username": "carol", "permissions": []string{"viewer"},
	}); rec.Code != http.StatusCreated {
		t.Fatalf("a manager should be able to share onward: %d %s", rec.Code, rec.Body)
	}

	// Now the restricted case: give Dave read-only plus the right to share.
	h.signedIn("dave")
	fileID, _ := alice.upload("root", "file.txt", []byte("content"))
	alice.share(fileID, map[string]any{
		"username": "dave", "permissions": []string{"read", "download", "share"},
	})

	dave := h.signedIn("dave2")
	_ = dave
	daveClient := h.newClient()
	if rec := daveClient.login("dave", "a perfectly reasonable passphrase"); rec.Code != http.StatusOK {
		t.Fatalf("sign in as dave failed: %d", rec.Code)
	}

	// Dave may pass on what he has.
	if rec := daveClient.shareRaw(fileID, map[string]any{
		"username": "carol", "permissions": []string{"read", "download"},
	}); rec.Code != http.StatusCreated {
		t.Fatalf("Dave should be able to pass on what he holds: %d %s", rec.Code, rec.Body)
	}

	// But not more. He has no delete right, so he cannot grant one.
	rec := daveClient.shareRaw(fileID, map[string]any{
		"username": "carol", "permissions": []string{"editor"},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — Dave granted a permission he does not hold", rec.Code)
	}
}

// Holding read access does not imply being able to share it onward.
func TestViewerCannotShareOnward(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")
	h.signedIn("carol")

	fileID, _ := alice.upload("root", "file.txt", []byte("content"))
	alice.share(fileID, map[string]any{"username": "bob", "permissions": []string{"viewer"}})

	rec := bob.shareRaw(fileID, map[string]any{
		"username": "carol", "permissions": []string{"viewer"},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — a viewer must not be able to share onward", rec.Code)
	}
}

// An editor can change content but must not be able to change who has access.
func TestEditorCannotManageAccess(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")
	carol := h.signedIn("carol")

	folder := alice.mkdir("root", "Team")
	alice.share(folder, map[string]any{"username": "bob", "permissions": []string{"editor"}})
	carolShare := alice.share(folder, map[string]any{"username": "carol", "permissions": []string{"viewer"}})
	_ = carol

	// Bob cannot revoke Carol's access.
	rec := bob.do(http.MethodDelete, "/api/v1/shares/"+carolShare.ID.String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — an editor must not manage access", rec.Code)
	}

	// And Carol still has it.
	if rec := carol.do(http.MethodGet, "/api/v1/nodes/"+folder.String(), nil); rec.Code != http.StatusOK {
		t.Error("Carol's access was revoked by someone without permission")
	}
}

// A recipient must not be able to enumerate everyone else with access.
func TestRecipientSeesOnlyTheirOwnShare(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")
	h.signedIn("carol")

	folder := alice.mkdir("root", "Team")
	alice.share(folder, map[string]any{"username": "bob", "permissions": []string{"viewer"}})
	alice.share(folder, map[string]any{"username": "carol", "permissions": []string{"viewer"}})

	// The owner sees everyone.
	rec := alice.do(http.MethodGet, "/api/v1/nodes/"+folder.String()+"/shares", nil)
	var ownerView struct {
		Shares []shareResponse `json:"shares"`
	}
	json.Unmarshal(rec.Body.Bytes(), &ownerView)
	if len(ownerView.Shares) != 2 {
		t.Fatalf("the owner should see both shares, got %d", len(ownerView.Shares))
	}

	// Bob sees only his own.
	rec = bob.do(http.MethodGet, "/api/v1/nodes/"+folder.String()+"/shares", nil)
	var recipientView struct {
		Shares []shareResponse `json:"shares"`
	}
	json.Unmarshal(rec.Body.Bytes(), &recipientView)
	if len(recipientView.Shares) != 1 {
		t.Fatalf("a recipient should see only their own share, got %d", len(recipientView.Shares))
	}
	if recipientView.Shares[0].User == nil || recipientView.Shares[0].User.Username != "bob" {
		t.Errorf("the visible share should be Bob's own: %+v", recipientView.Shares[0])
	}
	// Carol's name must not appear anywhere in the response.
	if bytes.Contains(rec.Body.Bytes(), []byte("carol")) {
		t.Error("the response leaked another recipient's identity")
	}
}

// --- Revocation -------------------------------------------------------------

// Revoking a share must take effect on the very next request.
func TestRevokingShareRemovesAccessImmediately(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	fileID, _ := alice.upload("root", "file.txt", []byte("content"))
	share := alice.share(fileID, map[string]any{"username": "bob", "permissions": []string{"viewer"}})

	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusOK {
		t.Fatalf("setup: Bob should have access, got %d", rec.Code)
	}

	if rec := alice.do(http.MethodDelete, "/api/v1/shares/"+share.ID.String(), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke failed: %d %s", rec.Code, rec.Body)
	}

	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("access survived revocation: %d", rec.Code)
	}
	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+fileID.String()+"/download", nil); rec.Code != http.StatusNotFound {
		t.Errorf("download survived revocation: %d", rec.Code)
	}
}

// Re-sharing must replace the existing grant, not add a second one that would
// have to be revoked separately.
func TestResharingReplacesTheExistingGrant(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	fileID, _ := alice.upload("root", "file.txt", []byte("content"))

	first := alice.share(fileID, map[string]any{"username": "bob", "permissions": []string{"editor"}})
	second := alice.share(fileID, map[string]any{"username": "bob", "permissions": []string{"viewer"}})

	if first.ID != second.ID {
		t.Error("re-sharing created a second grant; revoking one would leave the other")
	}

	// The narrower permission now applies.
	if rec := bob.do(http.MethodDelete, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusForbidden {
		t.Errorf("the downgraded permission did not take effect: %d", rec.Code)
	}

	// One revocation removes all access.
	alice.do(http.MethodDelete, "/api/v1/shares/"+second.ID.String(), nil)
	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusNotFound {
		t.Errorf("access survived a single revocation: %d", rec.Code)
	}
}

// --- Expiry -----------------------------------------------------------------

func TestExpiredShareGrantsNothing(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	fileID, _ := alice.upload("root", "file.txt", []byte("content"))

	// Create with a future expiry through the API, then move it into the past
	// directly: the API refuses a past date, which is itself correct.
	future := time.Now().Add(time.Hour)
	share := alice.share(fileID, map[string]any{
		"username": "bob", "permissions": []string{"viewer"},
		"expires_at": future.Format(time.RFC3339),
	})

	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusOK {
		t.Fatalf("an unexpired share should grant access: %d", rec.Code)
	}

	if _, err := h.db.Pool().Exec(t.Context(),
		`UPDATE permissions SET expires_at = now() - interval '1 minute' WHERE id = $1`,
		share.ID); err != nil {
		t.Fatal(err)
	}

	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("an expired share still granted access: %d", rec.Code)
	}
}

func TestShareRejectsPastExpiry(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	h.signedIn("bob")

	fileID, _ := alice.upload("root", "file.txt", []byte("content"))

	rec := alice.shareRaw(fileID, map[string]any{
		"username": "bob", "permissions": []string{"viewer"},
		"expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339),
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 for an expiry in the past", rec.Code)
	}
}

// --- Shared with me ---------------------------------------------------------

func TestSharedWithMeListsWhatOthersShared(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	fileID, _ := alice.upload("root", "from-alice.txt", []byte("content"))
	folderID := alice.mkdir("root", "Alice's folder")
	alice.share(fileID, map[string]any{"username": "bob", "permissions": []string{"viewer"}})
	alice.share(folderID, map[string]any{"username": "bob", "permissions": []string{"editor"}})

	// Bob's own content must not appear.
	bob.upload("root", "my-own.txt", []byte("mine"))

	rec := bob.do(http.MethodGet, "/api/v1/shared-with-me", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var body struct {
		Shares []shareResponse `json:"shares"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)

	if len(body.Shares) != 2 {
		t.Fatalf("expected 2 shared items, got %d", len(body.Shares))
	}

	names := map[string]bool{}
	for _, share := range body.Shares {
		names[share.Node.Name] = true
		if share.SharedBy == nil || share.SharedBy.Username != "alice" {
			t.Errorf("share should record who shared it: %+v", share.SharedBy)
		}
	}
	if !names["from-alice.txt"] || !names["Alice's folder"] {
		t.Errorf("unexpected contents: %v", names)
	}
	if names["my-own.txt"] {
		t.Error("the caller's own content appeared in shared-with-me")
	}
}

func TestSharedWithMeExcludesRevokedAndDeleted(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	keptID, _ := alice.upload("root", "kept.txt", []byte("content"))
	revokedID, _ := alice.upload("root", "revoked.txt", []byte("content"))
	deletedID, _ := alice.upload("root", "deleted.txt", []byte("content"))

	alice.share(keptID, map[string]any{"username": "bob", "permissions": []string{"viewer"}})
	revokedShare := alice.share(revokedID, map[string]any{"username": "bob", "permissions": []string{"viewer"}})
	alice.share(deletedID, map[string]any{"username": "bob", "permissions": []string{"viewer"}})

	alice.do(http.MethodDelete, "/api/v1/shares/"+revokedShare.ID.String(), nil)
	alice.do(http.MethodDelete, "/api/v1/nodes/"+deletedID.String(), nil)

	rec := bob.do(http.MethodGet, "/api/v1/shared-with-me", nil)
	var body struct {
		Shares []shareResponse `json:"shares"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)

	if len(body.Shares) != 1 {
		t.Fatalf("expected 1 share, got %d: %s", len(body.Shares), rec.Body)
	}
	if body.Shares[0].Node.Name != "kept.txt" {
		t.Errorf("wrong item survived: %q", body.Shares[0].Node.Name)
	}
}

// --- Group sharing ----------------------------------------------------------

// createGroup makes a group as an administrator and returns its id.
func (h *harness) createGroup(name string) uuid.UUID {
	h.t.Helper()
	group, err := h.groups.Create(h.t.Context(), name, "")
	if err != nil {
		h.t.Fatalf("create group %q: %v", name, err)
	}
	return group.ID
}

func (h *harness) addToGroup(groupID uuid.UUID, username string) {
	h.t.Helper()
	user, err := h.users.GetByUsername(h.t.Context(), username)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.groups.AddMember(h.t.Context(), groupID, user.ID, "member", user.ID); err != nil {
		h.t.Fatal(err)
	}
}

func TestSharingWithAGroupGrantsEveryMember(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")
	carol := h.signedIn("carol")
	dave := h.signedIn("dave")

	groupID := h.createGroup("Accounting")
	h.addToGroup(groupID, "bob")
	h.addToGroup(groupID, "carol")
	// Dave is deliberately not a member.

	folderID := alice.mkdir("root", "Ledgers")
	fileID, _ := alice.upload(folderID.String(), "2026.txt", []byte("the numbers"))

	share := alice.share(folderID, map[string]any{
		"group_name": "Accounting", "permissions": []string{"viewer"},
	})
	if share.Group == nil || share.Group.Name != "Accounting" {
		t.Fatalf("the share should record the group: %+v", share.Group)
	}

	for name, c := range map[string]*client{"bob": bob, "carol": carol} {
		if rec := c.do(http.MethodGet, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusOK {
			t.Errorf("%s is a member and should have access, got %d", name, rec.Code)
		}
	}
	if rec := dave.do(http.MethodGet, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusNotFound {
		t.Errorf("Dave is not a member and got %d, want 404", rec.Code)
	}
}

// Adding somebody to a group must give them the group's access immediately,
// without revisiting every share.
func TestJoiningAGroupGrantsItsAccess(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	groupID := h.createGroup("Accounting")
	folderID := alice.mkdir("root", "Ledgers")
	alice.share(folderID, map[string]any{"group_name": "Accounting", "permissions": []string{"viewer"}})

	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+folderID.String(), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("before joining, Bob got %d; want 404", rec.Code)
	}

	h.addToGroup(groupID, "bob")

	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+folderID.String(), nil); rec.Code != http.StatusOK {
		t.Fatalf("after joining, Bob got %d; want 200", rec.Code)
	}
}

// Removing somebody from a group must revoke the group's access immediately.
func TestLeavingAGroupRemovesItsAccess(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	groupID := h.createGroup("Accounting")
	h.addToGroup(groupID, "bob")

	folderID := alice.mkdir("root", "Ledgers")
	alice.share(folderID, map[string]any{"group_name": "Accounting", "permissions": []string{"viewer"}})

	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+folderID.String(), nil); rec.Code != http.StatusOK {
		t.Fatalf("setup: Bob should have access, got %d", rec.Code)
	}

	bobUser, _ := h.users.GetByUsername(t.Context(), "bob")
	if err := h.groups.RemoveMember(t.Context(), groupID, bobUser.ID); err != nil {
		t.Fatal(err)
	}

	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+folderID.String(), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("access survived leaving the group: %d", rec.Code)
	}
}

// Deleting a group must not leave its grants behind.
func TestDeletingAGroupRemovesItsGrants(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	groupID := h.createGroup("Temporary")
	h.addToGroup(groupID, "bob")

	folderID := alice.mkdir("root", "Folder")
	alice.share(folderID, map[string]any{"group_name": "Temporary", "permissions": []string{"viewer"}})

	if err := h.groups.Delete(t.Context(), groupID); err != nil {
		t.Fatal(err)
	}

	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+folderID.String(), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("access survived the group being deleted: %d", rec.Code)
	}

	var remaining int
	h.db.Pool().QueryRow(t.Context(),
		`SELECT count(*) FROM permissions WHERE subject_type = 'group' AND subject_id = $1`,
		groupID).Scan(&remaining)
	if remaining != 0 {
		t.Errorf("%d grants outlived the group", remaining)
	}
}

// A user in two groups gets the union of what both grant.
func TestPermissionsUnionAcrossGroups(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	readers := h.createGroup("Readers")
	editors := h.createGroup("Editors")
	h.addToGroup(readers, "bob")
	h.addToGroup(editors, "bob")

	folderID := alice.mkdir("root", "Folder")
	fileID, _ := alice.upload(folderID.String(), "file.txt", []byte("content"))

	alice.share(folderID, map[string]any{"group_name": "Readers", "permissions": []string{"read"}})
	alice.share(folderID, map[string]any{"group_name": "Editors", "permissions": []string{"read", "download", "delete"}})

	// The union includes delete, which only one group granted.
	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+fileID.String()+"/download", nil); rec.Code != http.StatusOK {
		t.Errorf("download from the union failed: %d", rec.Code)
	}
	if rec := bob.do(http.MethodDelete, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete from the union failed: %d", rec.Code)
	}
}

// --- Denials ----------------------------------------------------------------

// A denial must carve an exception out of an otherwise-shared tree.
func TestDenyOverridesAnInheritedGrant(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	outer := alice.mkdir("root", "Projects")
	private := alice.mkdir(outer.String(), "Private")
	publicFile, _ := alice.upload(outer.String(), "public.txt", []byte("shared"))
	privateFile, _ := alice.upload(private.String(), "secret.txt", []byte("not shared"))

	alice.share(outer, map[string]any{"username": "bob", "permissions": []string{"editor"}})

	// Bob can reach everything so far.
	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+privateFile.String(), nil); rec.Code != http.StatusOK {
		t.Fatalf("setup: Bob should reach the nested file, got %d", rec.Code)
	}

	// Carve out the Private subfolder.
	bobUser, _ := h.users.GetByUsername(t.Context(), "bob")
	if _, err := h.permissions.Grant(t.Context(), permissionsGrantDeny(private, bobUser.ID)); err != nil {
		t.Fatal(err)
	}

	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+privateFile.String(), nil); rec.Code != http.StatusNotFound {
		t.Errorf("the denial did not reach the nested file: %d", rec.Code)
	}
	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+private.String(), nil); rec.Code != http.StatusNotFound {
		t.Errorf("the denial did not apply to the folder itself: %d", rec.Code)
	}

	// The rest of the tree is unaffected.
	if rec := bob.do(http.MethodGet, "/api/v1/nodes/"+publicFile.String(), nil); rec.Code != http.StatusOK {
		t.Errorf("the denial leaked to a sibling: %d", rec.Code)
	}
}

// --- Owner and administrator --------------------------------------------------

// The owner keeps full control regardless of what grants exist.
func TestOwnerAlwaysRetainsControl(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	fileID, _ := alice.upload("root", "file.txt", []byte("content"))
	alice.share(fileID, map[string]any{"username": "bob", "permissions": []string{"viewer"}})

	// Even a denial aimed at the owner cannot take away ownership.
	aliceUser, _ := h.users.GetByUsername(t.Context(), "alice")
	if _, err := h.permissions.Grant(t.Context(), permissionsGrantDeny(fileID, aliceUser.ID)); err != nil {
		t.Fatal(err)
	}

	if rec := alice.do(http.MethodGet, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusOK {
		t.Fatalf("the owner lost access to their own file: %d", rec.Code)
	}
	if rec := alice.do(http.MethodDelete, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusNoContent {
		t.Errorf("the owner could not delete their own file: %d", rec.Code)
	}
	_ = bob
}

// Administering a server is not the same as being entitled to read what people
// store on it.
func TestAdministratorStillCannotReadUnsharedContent(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")

	fileID, _ := alice.upload("root", "private.txt", []byte("alice's private notes"))

	h.createUser("admin.user", "an administrator passphrase", true)
	admin := h.newClient()
	if rec := admin.login("admin.user", "an administrator passphrase"); rec.Code != http.StatusOK {
		t.Fatalf("admin sign-in failed: %d", rec.Code)
	}

	if rec := admin.do(http.MethodGet, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("an administrator reached unshared content: %d", rec.Code)
	}
	rec := admin.do(http.MethodGet, "/api/v1/nodes/"+fileID.String()+"/download", nil)
	if rec.Code != http.StatusNotFound || bytes.Contains(rec.Body.Bytes(), []byte("private notes")) {
		t.Fatal("an administrator downloaded unshared content")
	}
}

// --- Group management authority ----------------------------------------------

func TestOnlyAdministratorsCanCreateGroups(t *testing.T) {
	h := newHarness(t)
	ordinary := h.signedIn("alice")

	rec := ordinary.do(http.MethodPost, "/api/v1/groups", map[string]any{"name": "Rogue"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — creating groups is an administrator action", rec.Code)
	}

	h.createUser("admin.user", "an administrator passphrase", true)
	admin := h.newClient()
	admin.login("admin.user", "an administrator passphrase")

	if rec := admin.do(http.MethodPost, "/api/v1/groups", map[string]any{"name": "Accounting"}); rec.Code != http.StatusCreated {
		t.Fatalf("an administrator should be able to create a group: %d %s", rec.Code, rec.Body)
	}
}

// A group manager can run their own team without being an administrator.
func TestGroupManagerCanManageTheirOwnGroup(t *testing.T) {
	h := newHarness(t)
	manager := h.signedIn("manager.user")
	h.signedIn("newbie")

	groupID := h.createGroup("Accounting")
	managerUser, _ := h.users.GetByUsername(t.Context(), "manager.user")
	if err := h.groups.AddMember(t.Context(), groupID, managerUser.ID, "manager", managerUser.ID); err != nil {
		t.Fatal(err)
	}

	rec := manager.do(http.MethodPost, "/api/v1/groups/"+groupID.String()+"/members",
		map[string]any{"username": "newbie"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("a group manager could not add a member: %d %s", rec.Code, rec.Body)
	}

	// But an ordinary member cannot.
	member := h.signedIn("member.user")
	h.addToGroup(groupID, "member.user")
	h.signedIn("outsider")

	rec = member.do(http.MethodPost, "/api/v1/groups/"+groupID.String()+"/members",
		map[string]any{"username": "outsider"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("an ordinary member changed the membership: %d", rec.Code)
	}
}

// Who belongs to a team is not public information on a shared server.
func TestGroupMembershipIsNotPublic(t *testing.T) {
	h := newHarness(t)
	h.signedIn("member.user")
	outsider := h.signedIn("outsider")

	groupID := h.createGroup("Accounting")
	h.addToGroup(groupID, "member.user")

	rec := outsider.do(http.MethodGet, "/api/v1/groups/"+groupID.String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a non-member read the group's membership: %d", rec.Code)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("member.user")) {
		t.Error("the response leaked a member's name")
	}

	// Searching by name is allowed, because sharing requires finding a group.
	// Only names and counts are returned.
	rec = outsider.do(http.MethodGet, "/api/v1/groups?search=Accounting", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("searching groups should be permitted: %d", rec.Code)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("member.user")) {
		t.Error("the group search leaked membership")
	}
}

// --- Miscellaneous ------------------------------------------------------------

// Sharing a root folder would hand over the user's entire account.
func TestCannotShareYourRootFolder(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	h.signedIn("bob")

	rec := alice.do(http.MethodGet, "/api/v1/nodes", nil)
	var listing struct {
		Folder struct {
			ID uuid.UUID `json:"id"`
		} `json:"folder"`
	}
	json.Unmarshal(rec.Body.Bytes(), &listing)

	result := alice.shareRaw(listing.Folder.ID, map[string]any{
		"username": "bob", "permissions": []string{"viewer"},
	})
	if result.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — sharing your root hands over the whole account", result.Code)
	}
}

// The share endpoint must not become a way to discover which usernames exist.
func TestSharingWithAnUnknownUserRevealsNothing(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	h.signedIn("bob")

	fileID, _ := alice.upload("root", "file.txt", []byte("content"))

	unknown := alice.shareRaw(fileID, map[string]any{
		"username": "nosuchperson", "permissions": []string{"viewer"},
	})
	if unknown.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", unknown.Code)
	}

	body := decodeBody[api.ErrorResponse](t, unknown)
	// The message must not confirm or deny anything about real accounts.
	if body.Error.Message == "" {
		t.Error("expected a message")
	}
}

// A share must not be creatable on a node the caller cannot even see.
func TestCannotShareSomeoneElsesContent(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	mallory := h.signedIn("mallory")
	h.signedIn("accomplice")

	fileID, _ := alice.upload("root", "private.txt", []byte("alice's file"))

	rec := mallory.shareRaw(fileID, map[string]any{
		"username": "accomplice", "permissions": []string{"viewer"},
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — Mallory shared a file she cannot see", rec.Code)
	}

	// And no grant was created.
	var count int
	h.db.Pool().QueryRow(t.Context(),
		`SELECT count(*) FROM permissions WHERE node_id = $1`, fileID).Scan(&count)
	if count != 0 {
		t.Errorf("%d grants were created", count)
	}
}

// A shared folder's listing must show the real owner, not the viewer.
func TestSharedListingShowsTheRealOwner(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	folderID := alice.mkdir("root", "Shared")
	alice.upload(folderID.String(), "alices-file.txt", []byte("content"))
	alice.share(folderID, map[string]any{"username": "bob", "permissions": []string{"viewer"}})

	rec := bob.do(http.MethodGet, "/api/v1/nodes?parent_id="+folderID.String(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var body struct {
		Nodes []struct {
			Name  string `json:"name"`
			Owner struct {
				Username string `json:"username"`
			} `json:"owner"`
			Permissions []string `json:"permissions"`
			IsOwner     bool     `json:"is_owner"`
			Shared      bool     `json:"shared"`
		} `json:"nodes"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)

	if len(body.Nodes) != 1 {
		t.Fatalf("expected 1 item, got %d", len(body.Nodes))
	}
	item := body.Nodes[0]
	if item.Owner.Username != "alice" {
		t.Errorf("owner = %q, want alice", item.Owner.Username)
	}
	if item.IsOwner {
		t.Error("is_owner should be false for a shared item")
	}
	if !item.Shared {
		t.Error("shared should be true")
	}
	// The published permissions must match what Bob actually holds.
	perms := map[string]bool{}
	for _, p := range item.Permissions {
		perms[p] = true
	}
	if !perms["read"] || !perms["download"] {
		t.Errorf("permissions = %v, want at least read and download", item.Permissions)
	}
	if perms["delete"] {
		t.Errorf("permissions = %v; a viewer must not be told they can delete", item.Permissions)
	}
}

// permissionsGrantDeny builds a denial covering everything, for a subject on a
// node. Denials are not exposed through the API in this phase — they are an
// administrator-level construct — so tests build them directly.
func permissionsGrantDeny(nodeID, subjectID uuid.UUID) permissions.GrantParams {
	return permissions.GrantParams{
		NodeID:      nodeID,
		SubjectType: permissions.SubjectUser,
		SubjectID:   subjectID,
		Permissions: permissions.All,
		Deny:        true,
		Inherit:     true,
	}
}

// The breadcrumb for a shared folder must not disclose ancestors the caller
// cannot see, nor label somebody else's root "My Files".
func TestBreadcrumbsAreTruncatedToWhatYouCanSee(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	outer := alice.mkdir("root", "Alice Private")
	middle := alice.mkdir(outer.String(), "Projects")
	shared := alice.mkdir(middle.String(), "Shared Folder")
	inner := alice.mkdir(shared.String(), "Inside")

	alice.share(shared, map[string]any{"username": "bob", "permissions": []string{"viewer"}})

	rec := bob.do(http.MethodGet, "/api/v1/nodes?parent_id="+inner.String(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var body struct {
		Breadcrumbs []struct {
			ID     uuid.UUID `json:"id"`
			Name   string    `json:"name"`
			IsRoot bool      `json:"is_root"`
		} `json:"breadcrumbs"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)

	// Bob may see exactly the shared folder and what is inside it.
	if len(body.Breadcrumbs) != 2 {
		t.Fatalf("expected 2 breadcrumbs (the shared folder and the child), got %d: %+v",
			len(body.Breadcrumbs), body.Breadcrumbs)
	}
	if body.Breadcrumbs[0].Name != "Shared Folder" || body.Breadcrumbs[1].Name != "Inside" {
		t.Errorf("unexpected path: %+v", body.Breadcrumbs)
	}
	// Nothing above the share is named.
	for _, crumb := range body.Breadcrumbs {
		if crumb.Name == "Alice Private" || crumb.Name == "Projects" {
			t.Errorf("the breadcrumb disclosed an ancestor Bob cannot see: %q", crumb.Name)
		}
		if crumb.IsRoot {
			t.Error("somebody else's folder must not be labelled as the caller's root")
		}
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("Alice Private")) {
		t.Error("the response leaked a folder name Bob cannot see")
	}

	// Alice's own view is unaffected: she sees the full path.
	rec = alice.do(http.MethodGet, "/api/v1/nodes?parent_id="+inner.String(), nil)
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Breadcrumbs) != 5 {
		t.Errorf("the owner should see the full path, got %d crumbs", len(body.Breadcrumbs))
	}
	if !body.Breadcrumbs[0].IsRoot {
		t.Error("the owner's own root should still be marked as such")
	}
}

// --- Telling an owner what is no longer private ------------------------------

// listSharedOut returns each visible name mapped to its shared_out flag.
func (c *client) listSharedOut(parentID string) map[string]bool {
	c.t.Helper()
	path := "/api/v1/nodes"
	if parentID != "" {
		path += "?parent_id=" + parentID
	}
	rec := c.do(http.MethodGet, path, nil)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("list failed: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Nodes []struct {
			Name      string `json:"name"`
			SharedOut bool   `json:"shared_out"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		c.t.Fatal(err)
	}
	out := make(map[string]bool, len(body.Nodes))
	for _, n := range body.Nodes {
		out[n.Name] = n.SharedOut
	}
	return out
}

// An owner's listing has to say which of their items have left their hands.
// Without this the interface can only report "not shared with me", which for
// an owner is every row — so a file published to the whole internet reads as
// private.
func TestListingTellsTheOwnerWhatIsSharedOut(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	h.signedIn("bob")

	alice.upload("root", "private.txt", []byte("mine alone"))
	sharedID, _ := alice.upload("root", "shared.txt", []byte("for bob"))
	publishedID, _ := alice.upload("root", "published.txt", []byte("for anyone"))
	deniedID, _ := alice.upload("root", "denied.txt", []byte("not for bob"))

	alice.share(sharedID, map[string]any{"username": "bob", "permissions": []string{"viewer"}})
	alice.publish(publishedID, map[string]any{"kind": "download"})
	alice.share(deniedID, map[string]any{
		"username": "bob", "permissions": []string{"read"}, "deny": true,
	})

	flags := alice.listSharedOut("root")

	if flags["private.txt"] {
		t.Error("an item nobody has been given reported as shared")
	}
	if !flags["shared.txt"] {
		t.Error("an item shared with a person reported as private")
	}
	if !flags["published.txt"] {
		t.Error("an item published as a link reported as private")
	}
	// A denial is a grant row, but publishing one takes access away rather
	// than giving it. Counting it would tell the owner they had shared
	// something with the very person they had just shut out.
	if flags["denied.txt"] {
		t.Error("a denial reported as sharing the item out")
	}
}

// Revoking has to put the item back to private, and a link nobody can follow
// any more must not keep claiming the file is public.
func TestSharedOutClearsWhenAccessEnds(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	h.signedIn("bob")

	revokedID, _ := alice.upload("root", "revoked.txt", []byte("content"))
	withdrawnID, _ := alice.upload("root", "withdrawn-link.txt", []byte("content"))
	usedUpID, _ := alice.upload("root", "used-up.txt", []byte("content"))

	share := alice.share(revokedID, map[string]any{"username": "bob", "permissions": []string{"viewer"}})
	if rec := alice.do(http.MethodDelete, "/api/v1/shares/"+share.ID.String(), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke failed: %d %s", rec.Code, rec.Body)
	}

	withdrawn, _ := alice.publish(withdrawnID, map[string]any{"kind": "download"})
	if rec := alice.do(http.MethodDelete, "/api/v1/public-links/"+withdrawn.ID.String(), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("withdraw failed: %d %s", rec.Code, rec.Body)
	}

	_, token := alice.publish(usedUpID, map[string]any{"kind": "download", "max_uses": 1})
	if rec := h.newVisitor().request(http.MethodGet, "/api/v1/portal/"+token+"/download", nil, ""); rec.Code != http.StatusOK {
		t.Fatalf("download failed: %d %s", rec.Code, rec.Body)
	}

	flags := alice.listSharedOut("root")
	if flags["revoked.txt"] {
		t.Error("a revoked share still reported the item as shared")
	}
	if flags["withdrawn-link.txt"] {
		t.Error("a withdrawn link still reported the item as public")
	}
	if flags["used-up.txt"] {
		t.Error("a used-up link still reported the item as public")
	}
}

// Whom an owner has shared with is the owner's business. A recipient can see
// that the item was shared *with them*; they must not learn that it was also
// shared with somebody else.
func TestSharedOutIsNotShownToRecipients(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")
	h.signedIn("carol")

	folderID := alice.mkdir("root", "Team")
	fileID, _ := alice.upload(folderID.String(), "plan.txt", []byte("content"))

	alice.share(folderID, map[string]any{"username": "bob", "permissions": []string{"editor"}})
	alice.share(fileID, map[string]any{"username": "carol", "permissions": []string{"viewer"}})

	if flags := bob.listSharedOut(folderID.String()); flags["plan.txt"] {
		t.Error("a recipient was told the owner had also shared the item with somebody else")
	}
}
