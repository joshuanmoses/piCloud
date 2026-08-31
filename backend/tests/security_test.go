package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Access-control regression tests.
//
// These are the most important tests in the file layer. Every one of them
// describes a way one user could reach another user's content by changing an
// identifier in a request. The interface hides what a user may not see, but
// that is a usability affordance; these tests verify the server enforces it.

// twoUsers returns signed-in clients for a victim and an attacker, plus the
// victim's file and folder identifiers.
func twoUsers(t *testing.T) (h *harness, victim, attacker *client, fileID, folderID uuid.UUID) {
	t.Helper()
	h = newHarness(t)

	victim = h.signedIn("alice")
	attacker = h.signedIn("mallory")

	folderID = victim.mkdir("root", "Private")
	fileID, rec := victim.upload(folderID.String(), "secrets.txt", []byte("alice's confidential notes"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup upload failed: %d %s", rec.Code, rec.Body)
	}
	return h, victim, attacker, fileID, folderID
}

// The core IDOR test: every operation on another user's node must 404.
//
// 404 rather than 403 is deliberate. A distinguishable "forbidden" would
// confirm that a guessed UUID names a real object, turning the endpoint into an
// enumeration oracle.
func TestUserCannotReachAnotherUsersNodes(t *testing.T) {
	_, victim, attacker, fileID, folderID := twoUsers(t)

	attempts := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"read metadata", http.MethodGet, "/api/v1/nodes/" + fileID.String(), nil},
		{"download", http.MethodGet, "/api/v1/nodes/" + fileID.String() + "/download", nil},
		{"list a folder", http.MethodGet, "/api/v1/nodes?parent_id=" + folderID.String(), nil},
		{"rename", http.MethodPatch, "/api/v1/nodes/" + fileID.String(), map[string]any{"name": "stolen.txt"}},
		{"delete", http.MethodDelete, "/api/v1/nodes/" + fileID.String(), nil},
		{"permanently delete", http.MethodDelete, "/api/v1/nodes/" + fileID.String() + "?permanent=true", nil},
		{"restore", http.MethodPost, "/api/v1/nodes/" + fileID.String() + "/restore", nil},
		{"copy away", http.MethodPost, "/api/v1/nodes/" + fileID.String() + "/copy", map[string]any{"parent_id": "root"}},
		{"create inside", http.MethodPost, "/api/v1/nodes/folders", map[string]any{"parent_id": folderID.String(), "name": "intrusion"}},
	}

	for _, attempt := range attempts {
		t.Run(attempt.name, func(t *testing.T) {
			rec := attacker.do(attempt.method, attempt.path, attempt.body)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s returned %d, want 404; body: %s", attempt.name, rec.Code, rec.Body)
			}
			// The response must not leak anything about the real object.
			body := strings.ToLower(rec.Body.String())
			for _, leak := range []string{"secrets.txt", "private", "alice"} {
				if strings.Contains(body, leak) {
					t.Errorf("%s leaked %q: %s", attempt.name, leak, rec.Body)
				}
			}
		})
	}

	// The victim's file must be untouched by all of that.
	rec := victim.do(http.MethodGet, "/api/v1/nodes/"+fileID.String()+"/download", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "alice's confidential notes" {
		t.Error("the victim's file was damaged by the attempted attacks")
	}
}

// Moving a node into another user's folder would place the attacker's content
// where the victim can see it, and vice versa.
func TestUserCannotMoveIntoAnotherUsersFolder(t *testing.T) {
	_, _, attacker, _, victimFolder := twoUsers(t)

	ownFile, rec := attacker.upload("root", "mine.txt", []byte("mallory's file"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup upload failed: %d", rec.Code)
	}

	rec = attacker.do(http.MethodPatch, "/api/v1/nodes/"+ownFile.String(),
		map[string]any{"parent_id": victimFolder.String()})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when moving into another user's folder", rec.Code)
	}

	rec = attacker.do(http.MethodPost, "/api/v1/nodes/"+ownFile.String()+"/copy",
		map[string]any{"parent_id": victimFolder.String()})
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when copying into another user's folder", rec.Code)
	}
}

// Uploading into someone else's folder would let an attacker plant content, or
// consume the victim's quota.
func TestUserCannotUploadIntoAnotherUsersFolder(t *testing.T) {
	_, _, attacker, _, victimFolder := twoUsers(t)

	_, rec := attacker.upload(victimFolder.String(), "planted.txt", []byte("not yours"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when uploading into another user's folder", rec.Code)
	}
}

// An administrator manages accounts and quotas. That is not the same as being
// entitled to read what people store, and the audit log would have no way to
// tell legitimate support from snooping.
func TestAdministratorCannotReadOtherUsersFiles(t *testing.T) {
	h := newHarness(t)

	victim := h.signedIn("alice")
	fileID, rec := victim.upload("root", "private.txt", []byte("alice's private notes"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d", rec.Code)
	}

	h.createUser("admin.user", "an administrator passphrase", true)
	admin := h.newClient()
	if rec := admin.login("admin.user", "an administrator passphrase"); rec.Code != http.StatusOK {
		t.Fatalf("admin sign-in failed: %d", rec.Code)
	}

	rec = admin.do(http.MethodGet, "/api/v1/nodes/"+fileID.String()+"/download", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("an administrator read another user's file: status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "private notes") {
		t.Fatal("the file's content was disclosed to an administrator")
	}
}

// Every content route must reject an unauthenticated caller.
func TestContentRoutesRequireAuthentication(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("alice")
	fileID, _ := owner.upload("root", "file.txt", []byte("content"))

	anonymous := h.newClient()

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/nodes"},
		{http.MethodGet, "/api/v1/nodes/" + fileID.String()},
		{http.MethodGet, "/api/v1/nodes/" + fileID.String() + "/download"},
		{http.MethodPost, "/api/v1/nodes/folders"},
		{http.MethodPatch, "/api/v1/nodes/" + fileID.String()},
		{http.MethodDelete, "/api/v1/nodes/" + fileID.String()},
		{http.MethodGet, "/api/v1/trash"},
		{http.MethodGet, "/api/v1/uploads"},
		{http.MethodPost, "/api/v1/uploads"},
	}

	for _, route := range routes {
		rec := anonymous.do(route.method, route.path, map[string]any{})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s returned %d, want 401", route.method, route.path, rec.Code)
		}
	}
}

// A guessed identifier for a node that does not exist must be indistinguishable
// from one that exists but belongs to someone else.
func TestNonexistentAndForbiddenAreIndistinguishable(t *testing.T) {
	_, _, attacker, victimFile, _ := twoUsers(t)

	forbidden := attacker.do(http.MethodGet, "/api/v1/nodes/"+victimFile.String(), nil)
	missing := attacker.do(http.MethodGet, "/api/v1/nodes/"+uuid.NewString(), nil)

	if forbidden.Code != missing.Code {
		t.Errorf("status codes differ: %d for another user's node, %d for a nonexistent one",
			forbidden.Code, missing.Code)
	}

	var a, b struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(forbidden.Body.Bytes(), &a)
	json.Unmarshal(missing.Body.Bytes(), &b)

	if a.Error.Code != b.Error.Code || a.Error.Message != b.Error.Message {
		t.Errorf("responses differ and can be used to probe for real objects:\n  forbidden: %+v\n  missing:   %+v",
			a.Error, b.Error)
	}
}

// A malformed identifier must not produce a different answer either.
func TestMalformedIdentifiersAreRejectedUniformly(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	for _, id := range []string{
		"not-a-uuid", "../../etc/passwd", "1", "' OR 1=1--",
		"00000000-0000-0000-0000-000000000000", strings.Repeat("a", 200),
	} {
		rec := c.do(http.MethodGet, "/api/v1/nodes/"+urlEncode(id), nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("id %q returned %d, want 404", id, rec.Code)
		}
	}
}

// A user must not be able to resume or finalize another user's upload session.
func TestUserCannotTouchAnotherUsersUpload(t *testing.T) {
	h := newHarness(t)
	victim := h.signedIn("alice")
	attacker := h.signedIn("mallory")

	rec := victim.do(http.MethodPost, "/api/v1/uploads",
		map[string]any{"parent_id": "root", "name": "big.bin", "size": 1000})
	if rec.Code != http.StatusCreated {
		t.Fatalf("begin upload failed: %d %s", rec.Code, rec.Body)
	}
	var session struct {
		ID uuid.UUID `json:"id"`
	}
	json.Unmarshal(rec.Body.Bytes(), &session)

	attempts := []struct {
		name   string
		method string
		path   string
	}{
		{"read progress", http.MethodGet, "/api/v1/uploads/" + session.ID.String()},
		{"finalize", http.MethodPost, "/api/v1/uploads/" + session.ID.String() + "/complete"},
		{"abort", http.MethodDelete, "/api/v1/uploads/" + session.ID.String()},
	}
	for _, attempt := range attempts {
		rec := attacker.do(attempt.method, attempt.path, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s returned %d, want 404", attempt.name, rec.Code)
		}
	}

	// Writing a chunk into it must fail too.
	rec = attacker.raw(http.MethodPut,
		"/api/v1/uploads/"+session.ID.String()+"/chunk?offset=0",
		"application/octet-stream", bytes.NewReader([]byte("hostile chunk")))
	if rec.Code != http.StatusNotFound {
		t.Errorf("writing a chunk to another user's upload returned %d, want 404", rec.Code)
	}

	// The victim's session must be intact.
	rec = victim.do(http.MethodGet, "/api/v1/uploads/"+session.ID.String(), nil)
	if rec.Code != http.StatusOK {
		t.Errorf("the victim's upload session was damaged: %d", rec.Code)
	}
}

// Content routes must reject a request without a CSRF token, exactly as the
// auth routes do.
func TestContentWritesRequireCSRF(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")
	fileID, _ := c.upload("root", "file.txt", []byte("content"))

	// Drop the token, simulating a cross-site request where the attacker can
	// send the cookie but cannot read the CSRF value.
	c.csrf = ""

	writes := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "/api/v1/nodes/folders", map[string]any{"parent_id": "root", "name": "x"}},
		{http.MethodPatch, "/api/v1/nodes/" + fileID.String(), map[string]any{"name": "y"}},
		{http.MethodDelete, "/api/v1/nodes/" + fileID.String(), nil},
		{http.MethodPost, "/api/v1/uploads", map[string]any{"parent_id": "root", "name": "z", "size": 1}},
	}
	for _, w := range writes {
		rec := c.do(w.method, w.path, w.body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s returned %d without a CSRF token, want 403", w.method, w.path, rec.Code)
		}
	}

	// The upload endpoint takes a raw body, so it is checked separately.
	rec := c.raw(http.MethodPost, "/api/v1/files?name=x.txt&parent_id=root",
		"application/octet-stream", bytes.NewReader([]byte("content")))
	if rec.Code != http.StatusForbidden {
		t.Errorf("upload without a CSRF token returned %d, want 403", rec.Code)
	}

	// A read is exempt, or every page load would break.
	if rec := c.do(http.MethodGet, "/api/v1/nodes", nil); rec.Code != http.StatusOK {
		t.Errorf("a read should not require a CSRF token: %d", rec.Code)
	}
}

// The node payload must not expose the content hash: it would let a client
// probe whether specific content exists anywhere on the server.
func TestNodeResponseDoesNotLeakContentHash(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	content := []byte("content whose hash must not be published")
	id, _ := c.upload("root", "file.txt", content)
	digest := sha256Hex(content)

	for _, path := range []string{
		"/api/v1/nodes/" + id.String(),
		"/api/v1/nodes",
	} {
		rec := c.do(http.MethodGet, path, nil)
		body := rec.Body.String()
		if strings.Contains(body, digest) {
			t.Errorf("%s leaked the content hash", path)
		}
		if strings.Contains(body, "content_hash") {
			t.Errorf("%s includes a content_hash field", path)
		}
		// Nor any filesystem path.
		if strings.Contains(body, h.storageRoot) || strings.Contains(body, "/files/") {
			t.Errorf("%s leaked a filesystem path: %s", path, body)
		}
	}
}

// A name that survives validation must still be rendered safely. The API
// returns JSON, so the test is that the value round-trips exactly rather than
// being interpreted.
func TestHostileFilenamesAreStoredVerbatimAndEncoded(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	// Names containing < > " : * ? | are rejected outright by validation, so
	// they are covered separately below. These are names that ARE legal and
	// that a naive client might still mis-render or mis-interpret.
	names := []string{
		"'; DROP TABLE nodes;--.txt",
		"{{constructor.constructor('alert(1)')()}}.txt",
		"file&amp;.txt",
		"';alert(String.fromCharCode(88,83,83));.txt",
		"＜script＞alert(1)＜script＞.txt", // fullwidth: legal, and must stay inert
		"-rf ~ --no-preserve-root.txt",
	}

	for _, name := range names {
		id, rec := c.upload("root", name, []byte("content"))
		if rec.Code != http.StatusCreated {
			t.Errorf("name %q was rejected: %d %s", name, rec.Code, rec.Body)
			continue
		}

		rec = c.do(http.MethodGet, "/api/v1/nodes/"+id.String(), nil)
		var body struct {
			Node struct {
				Name string `json:"name"`
			} `json:"node"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("response was not valid JSON for name %q: %v", name, err)
		}
		// Stored and returned exactly, with JSON encoding doing the escaping.
		if body.Node.Name != name {
			t.Errorf("name round-trip failed: got %q, want %q", body.Node.Name, name)
		}
	}

	// And the database still works, which it would not if the SQL-looking name
	// had been interpreted.
	rec := c.do(http.MethodGet, "/api/v1/nodes", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing broke after storing hostile names: %d", rec.Code)
	}

	var listing struct {
		Total int `json:"total"`
	}
	json.Unmarshal(rec.Body.Bytes(), &listing)
	if listing.Total != len(names) {
		t.Errorf("stored %d of %d hostile names", listing.Total, len(names))
	}
}

// Characters that break clients, archives, or Windows are refused at the API
// boundary rather than stored and dealt with later.
func TestMarkupCharactersAreRejectedInNames(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	rejected := []string{
		"<script>alert(1)</script>.txt",
		`quo"te.txt`,
		"colon:name.txt",
		"star*.txt",
		"question?.txt",
		"pipe|name.txt",
		`back\slash.txt`,
		"for/ward.txt",
	}
	for _, name := range rejected {
		_, rec := c.upload("root", name, []byte("x"))
		if rec.Code == http.StatusCreated {
			t.Errorf("name %q was accepted; it should be rejected", name)
		}
	}

	// A right-to-left override can disguise "invoice[RLO]fdp.exe" as
	// "invoiceexe.pdf".
	_, rec := c.upload("root", "invoice\u202efdp.exe", []byte("x"))
	if rec.Code == http.StatusCreated {
		t.Error("a name containing a bidirectional override was accepted")
	}
}

// Pagination must be bounded: a request for a million rows would exhaust memory
// on a Raspberry Pi.
func TestListingPaginationIsBounded(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	for i := range 12 {
		c.mkdir("root", fmt.Sprintf("folder-%02d", i))
	}

	rec := c.do(http.MethodGet, "/api/v1/nodes?limit=5", nil)
	var page struct {
		Nodes   []json.RawMessage `json:"nodes"`
		Total   int               `json:"total"`
		HasMore bool              `json:"has_more"`
	}
	json.Unmarshal(rec.Body.Bytes(), &page)

	if len(page.Nodes) != 5 {
		t.Errorf("returned %d nodes, want 5", len(page.Nodes))
	}
	if page.Total != 12 {
		t.Errorf("total = %d, want 12", page.Total)
	}
	if !page.HasMore {
		t.Error("has_more should be true")
	}

	// An absurd limit must be clamped, not honoured.
	rec = c.do(http.MethodGet, "/api/v1/nodes?limit=100000", nil)
	json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Nodes) > 500 {
		t.Errorf("returned %d nodes; the limit must be clamped", len(page.Nodes))
	}

	// Garbage must fall back to the default rather than erroring or being used.
	for _, limit := range []string{"-1", "abc", "9999999999999999999999"} {
		rec = c.do(http.MethodGet, "/api/v1/nodes?limit="+limit, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("limit=%q returned %d", limit, rec.Code)
		}
	}
}

// The sort parameter reaches an ORDER BY clause, so it must never be
// interpolated.
func TestSortParameterCannotInjectSQL(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")
	c.mkdir("root", "Folder")

	hostile := []string{
		"name; DROP TABLE nodes--",
		"(SELECT password_hash FROM users)",
		"random()",
		"owner_id",
		"1=1",
	}
	for _, sort := range hostile {
		rec := c.do(http.MethodGet, "/api/v1/nodes?sort="+urlEncode(sort), nil)
		if rec.Code != http.StatusOK {
			t.Errorf("sort=%q returned %d; it should fall back to the default", sort, rec.Code)
		}
	}

	// The table must still exist.
	rec := c.do(http.MethodGet, "/api/v1/nodes", nil)
	if rec.Code != http.StatusOK {
		t.Fatal("the nodes table was damaged by a sort parameter")
	}
}
