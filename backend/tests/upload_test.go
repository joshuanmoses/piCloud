package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/api"
	"github.com/joshuanmoses/piCloud/backend/internal/files"
)

// filesReconcileOptions builds reconciliation options with the safety window
// short enough for a test but still non-zero.
func filesReconcileOptions(repair bool) files.ReconcileOptions {
	return files.ReconcileOptions{Repair: repair, MinAge: time.Hour}
}

// Resumable upload tests.
//
// The property that matters: a file assembled from chunks, across interruptions
// and retries, must be byte-for-byte identical to the original. Anything less
// silently corrupts a user's data.

type uploadSession struct {
	ID            uuid.UUID `json:"id"`
	DeclaredSize  int64     `json:"declared_size"`
	ReceivedBytes int64     `json:"received_bytes"`
	ChunkSize     int64     `json:"chunk_size"`
	Complete      bool      `json:"complete"`
}

func (c *client) beginUpload(parentID, name string, size int64) uploadSession {
	c.t.Helper()
	rec := c.do(http.MethodPost, "/api/v1/uploads",
		map[string]any{"parent_id": parentID, "name": name, "size": size})
	if rec.Code != http.StatusCreated {
		c.t.Fatalf("begin upload failed: %d %s", rec.Code, rec.Body)
	}
	var session uploadSession
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		c.t.Fatal(err)
	}
	return session
}

func (c *client) sendChunk(id uuid.UUID, offset int64, data []byte) *httptest.ResponseRecorder {
	c.t.Helper()
	rec := c.raw(http.MethodPut,
		fmt.Sprintf("/api/v1/uploads/%s/chunk?offset=%d", id, offset),
		"application/octet-stream", bytes.NewReader(data))
	return rec
}

func TestResumableUploadAssemblesFileCorrectly(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	// Content large enough to span several chunks, with a pattern that makes
	// any misordering or duplication obvious in the digest.
	content := make([]byte, 300_000)
	for i := range content {
		content[i] = byte((i * 7) % 256)
	}

	session := c.beginUpload("root", "large.bin", int64(len(content)))

	const chunk = 65_536
	for offset := 0; offset < len(content); offset += chunk {
		end := min(offset+chunk, len(content))
		rec := c.sendChunk(session.ID, int64(offset), content[offset:end])
		if rec.Code != http.StatusOK {
			t.Fatalf("chunk at %d failed: %d %s", offset, rec.Code, rec.Body)
		}
	}

	rec := c.do(http.MethodPost, "/api/v1/uploads/"+session.ID.String()+"/complete",
		map[string]any{"checksum": sha256Hex(content)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("complete failed: %d %s", rec.Code, rec.Body)
	}

	var result struct {
		Node struct {
			ID   uuid.UUID `json:"id"`
			Size int64     `json:"size_bytes"`
		} `json:"node"`
	}
	json.Unmarshal(rec.Body.Bytes(), &result)

	if result.Node.Size != int64(len(content)) {
		t.Errorf("stored size = %d, want %d", result.Node.Size, len(content))
	}

	// The assembled file must be byte-for-byte identical.
	rec = c.do(http.MethodGet, "/api/v1/nodes/"+result.Node.ID.String()+"/download", nil)
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatal("the reassembled file does not match the original")
	}
}

// This is the whole point of resumable uploads: a dropped connection must not
// mean starting over.
func TestUploadCanResumeAfterInterruption(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	content := bytes.Repeat([]byte("resumable content block. "), 4000) // 100 KB
	session := c.beginUpload("root", "resumed.bin", int64(len(content)))

	// Send the first part, then "lose the connection".
	const firstPart = 40_000
	if rec := c.sendChunk(session.ID, 0, content[:firstPart]); rec.Code != http.StatusOK {
		t.Fatalf("first chunk failed: %d", rec.Code)
	}

	// A resuming client asks the server where it got to.
	rec := c.do(http.MethodGet, "/api/v1/uploads/"+session.ID.String(), nil)
	var progress uploadSession
	json.Unmarshal(rec.Body.Bytes(), &progress)

	if progress.ReceivedBytes != firstPart {
		t.Fatalf("server reports %d bytes received, want %d", progress.ReceivedBytes, firstPart)
	}
	if progress.Complete {
		t.Error("the upload should not be reported complete")
	}

	// Resume from exactly there.
	if rec := c.sendChunk(session.ID, progress.ReceivedBytes, content[progress.ReceivedBytes:]); rec.Code != http.StatusOK {
		t.Fatalf("resumed chunk failed: %d %s", rec.Code, rec.Body)
	}

	rec = c.do(http.MethodPost, "/api/v1/uploads/"+session.ID.String()+"/complete",
		map[string]any{"checksum": sha256Hex(content)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("complete failed: %d %s", rec.Code, rec.Body)
	}

	var result struct {
		Node struct {
			ID uuid.UUID `json:"id"`
		} `json:"node"`
	}
	json.Unmarshal(rec.Body.Bytes(), &result)

	rec = c.do(http.MethodGet, "/api/v1/nodes/"+result.Node.ID.String()+"/download", nil)
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatal("the resumed upload produced a corrupt file")
	}
}

// A chunk at the wrong offset must be refused rather than written at the wrong
// place, which would silently corrupt the file.
func TestChunkAtWrongOffsetIsRefused(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	content := bytes.Repeat([]byte("x"), 1000)
	session := c.beginUpload("root", "file.bin", int64(len(content)))

	c.sendChunk(session.ID, 0, content[:400])

	// Too far ahead: would leave a hole.
	rec := c.sendChunk(session.ID, 900, content[900:])
	if rec.Code != http.StatusConflict {
		t.Errorf("a chunk beyond the received position returned %d, want 409", rec.Code)
	}
	// The server tells the client where it actually is, so it can resume.
	if got := rec.Header().Get("X-Upload-Offset"); got != "400" {
		t.Errorf("X-Upload-Offset = %q, want 400", got)
	}

	// Behind: would duplicate bytes.
	rec = c.sendChunk(session.ID, 0, content[:400])
	if rec.Code != http.StatusConflict {
		t.Errorf("a repeated chunk returned %d, want 409", rec.Code)
	}

	// The correct offset still works, and the file is intact.
	if rec := c.sendChunk(session.ID, 400, content[400:]); rec.Code != http.StatusOK {
		t.Fatalf("the correct chunk was refused: %d %s", rec.Code, rec.Body)
	}
	rec = c.do(http.MethodPost, "/api/v1/uploads/"+session.ID.String()+"/complete", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("complete failed: %d %s", rec.Code, rec.Body)
	}
}

// A corrupted transfer must be detected, not stored.
func TestChecksumMismatchIsRejected(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	content := []byte("the content that was actually uploaded")
	session := c.beginUpload("root", "file.txt", int64(len(content)))
	c.sendChunk(session.ID, 0, content)

	rec := c.do(http.MethodPost, "/api/v1/uploads/"+session.ID.String()+"/complete",
		map[string]any{"checksum": sha256Hex([]byte("completely different content"))})
	if rec.Code == http.StatusCreated {
		t.Fatal("a checksum mismatch was accepted; a corrupted transfer would be stored silently")
	}
	// A corrupted transfer is the client's problem to retry. Reporting it as a
	// 500 would both misattribute the failure and hide the explanation, since
	// internal errors return a generic message.
	if rec.Code >= 500 {
		t.Errorf("status = %d; a checksum mismatch is a client error, not a server failure", rec.Code)
	}
	if body := decodeBody[api.ErrorResponse](t, rec); !strings.Contains(strings.ToLower(body.Error.Message), "checksum") {
		t.Errorf("the message should explain the checksum mismatch, got %q", body.Error.Message)
	}

	// Nothing was created.
	rec = c.do(http.MethodGet, "/api/v1/nodes", nil)
	var listing struct {
		Total int `json:"total"`
	}
	json.Unmarshal(rec.Body.Bytes(), &listing)
	if listing.Total != 0 {
		t.Error("a rejected upload created a file anyway")
	}
}

func TestIncompleteUploadCannotBeFinalized(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	session := c.beginUpload("root", "file.bin", 1000)
	c.sendChunk(session.ID, 0, bytes.Repeat([]byte("x"), 400))

	rec := c.do(http.MethodPost, "/api/v1/uploads/"+session.ID.String()+"/complete", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an incomplete upload", rec.Code)
	}
}

func TestAbortRemovesPartialContent(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	session := c.beginUpload("root", "abandoned.bin", 1000)
	c.sendChunk(session.ID, 0, bytes.Repeat([]byte("x"), 400))

	tempPath := filepath.Join(h.storageRoot, "temp", strings.ReplaceAll(session.ID.String(), "-", ""))
	if _, err := os.Stat(tempPath); err != nil {
		t.Fatalf("the partial file should exist before abort: %v", err)
	}

	if rec := c.do(http.MethodDelete, "/api/v1/uploads/"+session.ID.String(), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("abort failed: %d %s", rec.Code, rec.Body)
	}

	// The partial content must be gone: an abandoned multi-gigabyte upload
	// left on a Pi's drive is a real problem.
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Error("the partial file survived abort")
	}
	if rec := c.do(http.MethodGet, "/api/v1/uploads/"+session.ID.String(), nil); rec.Code != http.StatusNotFound {
		t.Error("the aborted session is still readable")
	}
}

func TestUploadQuotaCheckedBeforeTransfer(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	user, _ := h.users.GetByUsername(t.Context(), "josh")
	quota := int64(1000)
	quotaPtr := &quota
	h.users.Update(t.Context(), user.ID, usersUpdateQuota(&quotaPtr))

	// Beginning an oversized upload must be refused immediately, rather than
	// after the client has spent an hour transferring.
	rec := c.do(http.MethodPost, "/api/v1/uploads",
		map[string]any{"parent_id": "root", "name": "toobig.bin", "size": 5000})
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507 before any bytes are sent", rec.Code)
	}
}

func TestListUploadsShowsResumableSessions(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	first := c.beginUpload("root", "one.bin", 1000)
	c.beginUpload("root", "two.bin", 2000)
	c.sendChunk(first.ID, 0, bytes.Repeat([]byte("x"), 100))

	rec := c.do(http.MethodGet, "/api/v1/uploads", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var body struct {
		Uploads []uploadSession `json:"uploads"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)

	if len(body.Uploads) != 2 {
		t.Fatalf("listed %d sessions, want 2", len(body.Uploads))
	}
	// A client that lost its state can find where to resume from.
	for _, u := range body.Uploads {
		if u.ID == first.ID && u.ReceivedBytes != 100 {
			t.Errorf("received_bytes = %d, want 100", u.ReceivedBytes)
		}
	}
}

// Replacing a file must keep its identity, so shares and favourites survive.
func TestReplaceKeepsNodeIdentity(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	original := []byte("version one of the document")
	id, _ := c.upload("root", "Document.txt", original)

	replacement := []byte("version two, which is longer than the first")
	rec := c.raw(http.MethodPost,
		"/api/v1/files?name=Document.txt&parent_id=root&replace_id="+id.String(),
		"application/octet-stream", bytes.NewReader(replacement))
	if rec.Code != http.StatusCreated {
		t.Fatalf("replace failed: %d %s", rec.Code, rec.Body)
	}

	var body struct {
		Node struct {
			ID   uuid.UUID `json:"id"`
			Size int64     `json:"size_bytes"`
		} `json:"node"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)

	if body.Node.ID != id {
		t.Errorf("the node identity changed on replace: %s became %s", id, body.Node.ID)
	}
	if body.Node.Size != int64(len(replacement)) {
		t.Errorf("size = %d, want %d", body.Node.Size, len(replacement))
	}

	rec = c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/download", nil)
	if !bytes.Equal(rec.Body.Bytes(), replacement) {
		t.Error("the replaced content is wrong")
	}

	// Only one node exists, and the quota reflects the new size only.
	user, _ := h.users.GetByUsername(t.Context(), "josh")
	if user.UsedBytes != int64(len(replacement)) {
		t.Errorf("used_bytes = %d, want %d", user.UsedBytes, len(replacement))
	}
}

// --- Consistency ------------------------------------------------------------

// Every file record must have its bytes, and every blob must be referenced.
// This is the invariant the whole storage design exists to maintain.
func TestDatabaseAndFilesystemStayConsistent(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	folder := c.mkdir("root", "Documents")
	ids := make([]uuid.UUID, 0, 6)
	for i := range 5 {
		id, rec := c.upload(folder.String(), fmt.Sprintf("file-%d.txt", i),
			[]byte(fmt.Sprintf("content number %d", i)))
		if rec.Code != http.StatusCreated {
			t.Fatalf("upload %d failed: %d", i, rec.Code)
		}
		ids = append(ids, id)
	}

	// Exercise the operations that move content around.
	c.do(http.MethodPost, "/api/v1/nodes/"+ids[0].String()+"/copy", map[string]any{"parent_id": "root"})
	c.do(http.MethodDelete, "/api/v1/nodes/"+ids[1].String(), nil)
	c.do(http.MethodDelete, "/api/v1/nodes/"+ids[2].String()+"?permanent=true", nil)
	c.do(http.MethodPatch, "/api/v1/nodes/"+ids[3].String(), map[string]any{"name": "renamed.txt"})

	assertConsistent(t, h)
}

// assertConsistent verifies the two halves of the storage design agree.
func assertConsistent(t *testing.T, h *harness) {
	t.Helper()
	ctx := t.Context()

	// 1. Every blob record must have its bytes on disk. The reverse — a record
	//    without content — is unrecoverable data loss.
	rows, err := h.db.Pool().Query(ctx, `SELECT hash, size_bytes FROM blobs`)
	if err != nil {
		t.Fatal(err)
	}
	type blob struct {
		hash string
		size int64
	}
	var blobs []blob
	for rows.Next() {
		var b blob
		if err := rows.Scan(&b.hash, &b.size); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		blobs = append(blobs, b)
	}
	rows.Close()

	for _, b := range blobs {
		path := filepath.Join(h.storageRoot, "files", b.hash[0:2], b.hash[2:4], b.hash)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("blob %s is recorded but missing from disk: %v", b.hash[:12], err)
			continue
		}
		if info.Size() != b.size {
			t.Errorf("blob %s is %d bytes on disk but %d in the database",
				b.hash[:12], info.Size(), b.size)
		}
	}

	// 2. Every file node must point at a blob that exists.
	var danglingNodes int
	err = h.db.Pool().QueryRow(ctx, `
		SELECT count(*) FROM nodes n
		WHERE n.kind = 'file' AND n.content_hash IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM blobs b WHERE b.hash = n.content_hash)`).Scan(&danglingNodes)
	if err != nil {
		t.Fatal(err)
	}
	if danglingNodes != 0 {
		t.Errorf("%d file nodes reference a blob that does not exist", danglingNodes)
	}

	// 3. Reference counts must match reality, or content is either deleted too
	//    early or never reclaimed.
	rows, err = h.db.Pool().Query(ctx, `
		SELECT b.hash, b.ref_count, count(n.id)
		FROM blobs b LEFT JOIN nodes n ON n.content_hash = b.hash
		GROUP BY b.hash, b.ref_count
		HAVING b.ref_count <> count(n.id)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		var stored, actual int
		rows.Scan(&hash, &stored, &actual)
		t.Errorf("blob %s has ref_count %d but %d referencing nodes", hash[:12], stored, actual)
	}

	// 4. Quota must equal the sum of live content.
	var mismatches int
	err = h.db.Pool().QueryRow(ctx, `
		SELECT count(*) FROM users u
		WHERE u.used_bytes <> (
			SELECT coalesce(sum(n.size_bytes), 0) FROM nodes n
			WHERE n.owner_id = u.id AND n.kind = 'file' AND n.deleted_at IS NULL
		)`).Scan(&mismatches)
	if err != nil {
		t.Fatal(err)
	}
	if mismatches != 0 {
		t.Errorf("%d users have a used_bytes total that disagrees with their content", mismatches)
	}
}

// A failed upload must not leave a file record whose bytes are missing. An
// unreferenced blob is acceptable; the reconciliation job reclaims it.
func TestFailedUploadLeavesNoDanglingMetadata(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	c.upload("root", "existing.txt", []byte("first file"))

	// A name collision fails after the content is already committed to disk,
	// which is exactly the window this test cares about.
	_, rec := c.upload("root", "existing.txt", []byte("a different file with the same name"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}

	// No dangling metadata. There may be an unreferenced blob, which is the
	// recoverable direction and what reconciliation exists for.
	var dangling int
	h.db.Pool().QueryRow(t.Context(), `
		SELECT count(*) FROM nodes n
		WHERE n.kind = 'file' AND n.content_hash IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM blobs b WHERE b.hash = n.content_hash)`).Scan(&dangling)
	if dangling != 0 {
		t.Errorf("%d nodes reference missing content", dangling)
	}

	// And the original is undamaged.
	rec = c.do(http.MethodGet, "/api/v1/nodes", nil)
	var listing struct {
		Total int `json:"total"`
	}
	json.Unmarshal(rec.Body.Bytes(), &listing)
	if listing.Total != 1 {
		t.Errorf("root has %d children, want 1", listing.Total)
	}
}

// Reconciliation must find an orphaned blob and, when asked, reclaim it.
func TestReconciliationFindsAndRepairsOrphans(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	c.upload("root", "real.txt", []byte("a real file"))

	// Plant an orphan: content on disk that no record references, as a failed
	// metadata commit would leave behind.
	orphanContent := []byte("orphaned content with no database record")
	orphanDigest := sha256Hex(orphanContent)
	orphanDir := filepath.Join(h.storageRoot, "files", orphanDigest[0:2], orphanDigest[2:4])
	if err := os.MkdirAll(orphanDir, 0o750); err != nil {
		t.Fatal(err)
	}
	orphanPath := filepath.Join(orphanDir, orphanDigest)
	if err := os.WriteFile(orphanPath, orphanContent, 0o640); err != nil {
		t.Fatal(err)
	}
	// Backdate it past the safety window that protects in-flight uploads.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(orphanPath, old, old); err != nil {
		t.Fatal(err)
	}

	// A reporting pass must find it and change nothing.
	report, err := h.files.Reconcile(t.Context(), filesReconcileOptions(false))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(report.OrphanedFiles) != 1 || report.OrphanedFiles[0] != orphanDigest {
		t.Errorf("orphan not detected: %+v", report.OrphanedFiles)
	}
	if len(report.MissingContent) != 0 {
		t.Errorf("reported missing content that does not exist: %v", report.MissingContent)
	}
	if _, err := os.Stat(orphanPath); err != nil {
		t.Error("a reporting pass removed the file; it must only report")
	}

	// A repairing pass must reclaim it.
	report, err = h.files.Reconcile(t.Context(), filesReconcileOptions(true))
	if err != nil {
		t.Fatalf("reconcile with repair: %v", err)
	}
	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Error("the orphan was not reclaimed")
	}
	if report.BytesReclaimed != int64(len(orphanContent)) {
		t.Errorf("reclaimed %d bytes, want %d", report.BytesReclaimed, len(orphanContent))
	}

	// The real file must be untouched throughout.
	rec := c.do(http.MethodGet, "/api/v1/nodes", nil)
	var listing struct {
		Total int `json:"total"`
	}
	json.Unmarshal(rec.Body.Bytes(), &listing)
	if listing.Total != 1 {
		t.Error("reconciliation damaged real content")
	}
	assertConsistent(t, h)
}

// Missing content is data loss. It must be reported, never "repaired" by
// deleting the record, which would silently discard the user's file.
func TestReconciliationReportsMissingContentWithoutDeletingIt(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	content := []byte("content that will vanish from disk")
	id, _ := c.upload("root", "vanishing.txt", content)
	digest := sha256Hex(content)

	// Simulate a failing disk losing the file.
	blobPath := filepath.Join(h.storageRoot, "files", digest[0:2], digest[2:4], digest)
	if err := os.Remove(blobPath); err != nil {
		t.Fatal(err)
	}

	report, err := h.files.Reconcile(t.Context(), filesReconcileOptions(true))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if len(report.MissingContent) != 1 || report.MissingContent[0] != digest {
		t.Fatalf("missing content not reported: %+v", report.MissingContent)
	}
	if !report.HasProblems() {
		t.Error("a missing blob must be flagged as a problem needing attention")
	}

	// The metadata must survive so an administrator can restore from backup.
	var count int
	h.db.Pool().QueryRow(t.Context(), `SELECT count(*) FROM nodes WHERE id = $1`, id).Scan(&count)
	if count != 1 {
		t.Error("the node record was deleted; that discards the user's file rather than reporting the loss")
	}
}
