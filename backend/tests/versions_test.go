package tests

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/files"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
	"github.com/joshuanmoses/piCloud/backend/internal/versions"
)

// Phase 8: file version history.
//
// The property that matters most here is that nothing is ever destroyed by
// accident. Replacing a file keeps what it was; restoring keeps what it is;
// pruning removes only what a stated policy says to remove. Each of those is
// one bug away from silently losing somebody's work, so each is tested for the
// loss rather than for the happy path.

// --- Snapshotting -----------------------------------------------------------

func TestReplacingAFileKeepsWhatItWas(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Report.txt", []byte("first draft"))
	c.replace(id, []byte("second draft"))
	c.replace(id, []byte("third draft"))

	history := c.versions(id)
	if len(history) != 2 {
		t.Fatalf("got %d versions after two replacements, want 2: %+v", len(history), history)
	}

	// Newest first, and numbered in the order they were made.
	if history[0].Number != 2 || history[1].Number != 1 {
		t.Errorf("version numbers = %d, %d; want 2, 1 (newest first)",
			history[0].Number, history[1].Number)
	}

	// And each one still holds its own bytes.
	if got := c.versionContent(id, history[1].ID); got != "first draft" {
		t.Errorf("version 1 content = %q, want the first draft", got)
	}
	if got := c.versionContent(id, history[0].ID); got != "second draft" {
		t.Errorf("version 2 content = %q, want the second draft", got)
	}
}

// The bytes are not copied. Two versions and a current file that share nothing
// should be three blobs; a version that duplicated content would be four.
func TestAVersionReferencesTheExistingContentRatherThanCopyingIt(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	before := h.blobCount()

	id, _ := c.upload("root", "Report.txt", []byte("one"))
	c.replace(id, []byte("two"))
	c.replace(id, []byte("three"))

	if got := h.blobCount() - before; got != 3 {
		t.Errorf("three distinct contents produced %d blobs, want 3 — a version is copying bytes", got)
	}

	// And every one of them is still referenced, so nothing will be reclaimed
	// out from under the history.
	if unref := h.unreferencedBlobs(); unref != 0 {
		t.Errorf("%d blobs have no references while versions still name them", unref)
	}
}

// Replacing a file with byte-identical content changes nothing, so a version
// of it would be an entry in the history that says nothing — noise in the one
// place a person is reading carefully.
func TestReplacingWithIdenticalContentDoesNotCreateAVersion(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Report.txt", []byte("unchanged"))
	c.replace(id, []byte("unchanged"))

	if got := len(c.versions(id)); got != 0 {
		t.Errorf("got %d versions after replacing with identical content, want 0", got)
	}
	// And the content is still readable, which it would not be if the
	// reference accounting had gone wrong.
	if got := c.download(id); got != "unchanged" {
		t.Errorf("content = %q after an identical replacement", got)
	}
}

func TestAVersionRecordsTheNameTheFileHadAtTheTime(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Draft.txt", []byte("v1"))
	c.replace(id, []byte("v2"))

	// Renamed after the version was made.
	if rec := c.do(http.MethodPatch, "/api/v1/nodes/"+id.String(),
		map[string]any{"name": "Final.txt"}); rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}

	history := c.versions(id)
	if len(history) != 1 {
		t.Fatalf("got %d versions, want 1", len(history))
	}
	if history[0].Name != "Draft.txt" {
		t.Errorf("version name = %q, want the name it had then", history[0].Name)
	}
}

// --- Restoring ---------------------------------------------------------------

// The whole point of history: restoring the wrong thing must be one more
// restore away from being undone, not gone.
func TestRestoringKeepsWhatItReplaced(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Report.txt", []byte("first"))
	c.replace(id, []byte("second"))
	c.replace(id, []byte("third"))

	history := c.versions(id)
	first := history[1] // version 1

	c.mustRestore(id, first.ID)

	if got := c.download(id); got != "first" {
		t.Fatalf("after restoring version 1 the file reads %q", got)
	}

	// "third" — what the restore displaced — is now a version of its own.
	after := c.versions(id)
	if len(after) != 3 {
		t.Fatalf("got %d versions after a restore, want 3: %+v", len(after), after)
	}
	if after[0].Reason != string(versions.ReasonRestored) {
		t.Errorf("the newest version's reason = %q, want %q",
			after[0].Reason, versions.ReasonRestored)
	}
	if got := c.versionContent(id, after[0].ID); got != "third" {
		t.Errorf("the displaced content = %q, want \"third\"", got)
	}
}

func TestRestoringTwiceReturnsYouToWhereYouStarted(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Report.txt", []byte("original"))
	c.replace(id, []byte("edited"))

	// Restore the original by mistake...
	history := c.versions(id)
	c.mustRestore(id, history[0].ID)
	if got := c.download(id); got != "original" {
		t.Fatalf("after the first restore: %q", got)
	}

	// ...then undo the mistake.
	after := c.versions(id)
	c.mustRestore(id, after[0].ID)
	if got := c.download(id); got != "edited" {
		t.Errorf("after restoring the restore: %q, want \"edited\"", got)
	}
}

// A version identifier must never be a route to a file the caller could not
// otherwise open.
func TestAVersionCannotBeReachedThroughSomebodyElsesFile(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	stranger := h.signedIn("stranger")

	ownerFile, _ := owner.upload("root", "Private.txt", []byte("secret"))
	owner.replace(ownerFile, []byte("still secret"))
	history := owner.versions(ownerFile)

	strangerFile, _ := stranger.upload("root", "Mine.txt", []byte("mine"))

	// The identifier is real, but it belongs to a file the stranger cannot see.
	rec := stranger.do(http.MethodGet,
		fmt.Sprintf("/api/v1/nodes/%s/versions/%s/download", ownerFile, history[0].ID), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d for another user's version, want 404: %s", rec.Code, rec.Body)
	}

	// And naming it under a file the stranger *does* own must not work either,
	// or a version would be reachable through any file at all.
	rec = stranger.do(http.MethodGet,
		fmt.Sprintf("/api/v1/nodes/%s/versions/%s/download", strangerFile, history[0].ID), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d for a version of a different file, want 404: %s", rec.Code, rec.Body)
	}
	if rec.Body.String() != "" && contains(rec.Body.String(), "secret") {
		t.Error("the response contains the other file's content")
	}
}

// Being allowed to read a file is not being allowed to rewrite it from its own
// history.
func TestAViewerCannotRestoreAVersion(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	viewer := h.signedIn("viewer")

	folderID := owner.mkdir("root", "Team")
	fileID, _ := owner.upload(folderID.String(), "Report.txt", []byte("first"))
	owner.replace(fileID, []byte("second"))
	h.share(folderID, "viewer", permissions.Read|permissions.Download)

	history := viewer.versions(fileID)
	if len(history) != 1 {
		t.Fatalf("the viewer sees %d versions, want 1 — reading a file includes its history", len(history))
	}

	rec := viewer.do(http.MethodPost,
		fmt.Sprintf("/api/v1/nodes/%s/versions/%s/restore", fileID, history[0].ID), nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a viewer restoring: %s", rec.Code, rec.Body)
	}

	// And the file is untouched.
	if got := owner.download(fileID); got != "second" {
		t.Errorf("the file reads %q after a refused restore", got)
	}
}

// Editing a document is not the same as being entitled to erase the record of
// what it used to say.
func TestAnEditorCannotDeleteAVersion(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	editor := h.signedIn("editor")

	folderID := owner.mkdir("root", "Team")
	fileID, _ := owner.upload(folderID.String(), "Report.txt", []byte("first"))
	owner.replace(fileID, []byte("second"))
	// Everything an editor has, deliberately without Delete.
	h.share(folderID, "editor",
		permissions.Read|permissions.Download|permissions.Upload|permissions.Create|
			permissions.Modify|permissions.Rename|permissions.Move)

	history := editor.versions(fileID)
	rec := editor.do(http.MethodDelete,
		fmt.Sprintf("/api/v1/nodes/%s/versions/%s", fileID, history[0].ID), nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for an editor deleting history: %s", rec.Code, rec.Body)
	}

	// But restoring — which they may do — still works.
	if rec := editor.do(http.MethodPost,
		fmt.Sprintf("/api/v1/nodes/%s/versions/%s/restore", fileID, history[0].ID),
		nil); rec.Code != http.StatusOK {
		t.Errorf("an editor could not restore: %d %s", rec.Code, rec.Body)
	}
}

func TestAFolderHasNoVersionHistory(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	folderID := c.mkdir("root", "Project")

	rec := c.do(http.MethodGet, "/api/v1/nodes/"+folderID.String()+"/versions", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a folder: %s", rec.Code, rec.Body)
	}
}

// --- Deleting ----------------------------------------------------------------

func TestDeletingAVersionReleasesItsContentWithoutTouchingTheOthers(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Report.txt", []byte("one"))
	c.replace(id, []byte("two"))
	c.replace(id, []byte("three"))

	history := c.versions(id)
	if rec := c.do(http.MethodDelete,
		fmt.Sprintf("/api/v1/nodes/%s/versions/%s", id, history[1].ID),
		nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete version: %d %s", rec.Code, rec.Body)
	}

	after := c.versions(id)
	if len(after) != 1 {
		t.Fatalf("got %d versions after deleting one, want 1", len(after))
	}
	// The survivor is intact.
	if got := c.versionContent(id, after[0].ID); got != "two" {
		t.Errorf("the remaining version reads %q, want \"two\"", got)
	}
	// And so is the file.
	if got := c.download(id); got != "three" {
		t.Errorf("the current file reads %q, want \"three\"", got)
	}
}

// Numbers are shown to people. Renumbering after a prune would make a version
// somebody had seen mean something else.
func TestVersionNumbersDoNotRenumberWhenOneIsRemoved(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Report.txt", []byte("one"))
	c.replace(id, []byte("two"))
	c.replace(id, []byte("three"))
	c.replace(id, []byte("four"))

	history := c.versions(id) // versions 3, 2, 1
	// Remove version 2.
	if rec := c.do(http.MethodDelete,
		fmt.Sprintf("/api/v1/nodes/%s/versions/%s", id, history[1].ID),
		nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}

	after := c.versions(id)
	numbers := make([]int, 0, len(after))
	for _, v := range after {
		numbers = append(numbers, v.Number)
	}
	if len(numbers) != 2 || numbers[0] != 3 || numbers[1] != 1 {
		t.Errorf("numbers after removing version 2 = %v, want [3 1]", numbers)
	}

	// And the next replacement continues from the highest, not from a gap.
	c.replace(id, []byte("five"))
	next := c.versions(id)
	if next[0].Number != 4 {
		t.Errorf("the next version is numbered %d, want 4", next[0].Number)
	}
}

// --- Retention ---------------------------------------------------------------

func TestRetentionKeepsTheNewestVersionsAndDropsTheRest(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Report.txt", []byte("v0"))
	for i := 1; i <= 6; i++ {
		c.replace(id, []byte(fmt.Sprintf("v%d", i)))
	}
	if got := len(c.versions(id)); got != 6 {
		t.Fatalf("got %d versions before pruning, want 6", got)
	}

	prunable, err := h.versions.Store().FindPrunable(
		t.Context(), versions.Retention{MaxPerFile: 3}, 100)
	if err != nil {
		t.Fatalf("FindPrunable: %v", err)
	}
	if len(prunable) != 3 {
		t.Fatalf("FindPrunable returned %d of 6 with a limit of 3, want 3", len(prunable))
	}

	for _, p := range prunable {
		if err := h.versions.Store().Delete(t.Context(), p.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}

	after := c.versions(id)
	if len(after) != 3 {
		t.Fatalf("got %d versions after pruning to 3, want 3", len(after))
	}
	// The *newest* three survived.
	for i, want := range []int{6, 5, 4} {
		if after[i].Number != want {
			t.Errorf("surviving version %d is numbered %d, want %d", i, after[i].Number, want)
		}
	}
	// And the current file is untouched, which is the failure that would
	// matter: retention must never reach the live content.
	if got := c.download(id); got != "v6" {
		t.Errorf("the current file reads %q after pruning", got)
	}
}

// A policy with nothing set means "keep everything". Reading it as "prune
// everything" would be catastrophic and is exactly the misconfiguration that
// must fail safe.
func TestAnUnlimitedPolicyPrunesNothing(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Report.txt", []byte("one"))
	c.replace(id, []byte("two"))
	c.replace(id, []byte("three"))

	prunable, err := h.versions.Store().FindPrunable(t.Context(), versions.Retention{}, 100)
	if err != nil {
		t.Fatalf("FindPrunable: %v", err)
	}
	if len(prunable) != 0 {
		t.Errorf("an unlimited policy marked %d versions for removal", len(prunable))
	}
}

func TestRetentionByAgeLeavesRecentVersionsAlone(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Report.txt", []byte("old"))
	c.replace(id, []byte("recent"))
	c.replace(id, []byte("current"))

	history := c.versions(id)
	// Age the older of the two past the window.
	if _, err := h.db.Pool().Exec(t.Context(),
		`UPDATE versions SET created_at = now() - interval '200 days' WHERE id = $1`,
		history[1].ID); err != nil {
		t.Fatalf("ageing: %v", err)
	}

	prunable, err := h.versions.Store().FindPrunable(
		t.Context(), versions.Retention{MaxAge: 90 * 24 * time.Hour}, 100)
	if err != nil {
		t.Fatalf("FindPrunable: %v", err)
	}
	if len(prunable) != 1 {
		t.Fatalf("got %d prunable versions, want only the aged one", len(prunable))
	}
	if prunable[0].ID != history[1].ID {
		t.Error("FindPrunable selected the recent version rather than the aged one")
	}
}

// Purging a file must reclaim its history's hold on storage. The foreign key
// cascades the rows, but a cascade cannot decrement a reference count, and a
// blob nothing references but that the count says is referenced is never
// reclaimed.
func TestPurgingAFileReleasesItsHistorysHoldOnContent(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Report.txt", []byte("one"))
	c.replace(id, []byte("two"))
	c.replace(id, []byte("three"))

	if rec := c.do(http.MethodDelete, "/api/v1/nodes/"+id.String(), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("trash: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do(http.MethodDelete, "/api/v1/nodes/"+id.String()+"?permanent=true", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("purge: %d %s", rec.Code, rec.Body)
	}

	// The purge itself releases the history's references — the foreign key
	// would cascade the rows away without ever decrementing a count, which is
	// how a blob nothing can reach stays "referenced" forever.
	var remaining int
	if err := h.db.Pool().QueryRow(t.Context(),
		`SELECT count(*) FROM versions WHERE node_id = $1`, id).Scan(&remaining); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d version rows survived the purge", remaining)
	}

	// Every blob the file and its history used is now unreferenced, so the
	// reconciliation job can reclaim the bytes.
	var referenced int
	if err := h.db.Pool().QueryRow(t.Context(),
		`SELECT count(*) FROM blobs WHERE ref_count > 0`).Scan(&referenced); err != nil {
		t.Fatalf("count: %v", err)
	}
	if referenced != 0 {
		t.Errorf("%d blobs are still referenced after the file and its history were purged", referenced)
	}
}

// Reconciliation counts references to decide what can be reclaimed. Counting
// only nodes would report every versioned file as an error — and, worse, a
// repairing pass would "fix" the count downward and hand still-referenced bytes
// to the sweeper.
func TestReconciliationCountsAVersionAsAReference(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Report.txt", []byte("one"))
	c.replace(id, []byte("two"))
	c.replace(id, []byte("three"))

	report, err := h.files.Reconcile(t.Context(), files.ReconcileOptions{Repair: false})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if report.RefCountErrors != 0 {
		t.Errorf("reconciliation reported %d reference-count errors on a healthy versioned file",
			report.RefCountErrors)
	}
	if len(report.MissingContent) != 0 {
		t.Errorf("reconciliation reported missing content: %v", report.MissingContent)
	}
	// The version's content is referenced, so it must not be listed as
	// reclaimable.
	if len(report.UnreferencedBlobs) != 0 {
		t.Errorf("reconciliation would reclaim %d blobs that versions still name",
			len(report.UnreferencedBlobs))
	}
}

// --- Listings ----------------------------------------------------------------

func TestAListingReportsHowManyVersionsAFileHas(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Versioned.txt", []byte("one"))
	c.replace(id, []byte("two"))
	c.replace(id, []byte("three"))
	c.upload("root", "Untouched.txt", []byte("only ever this"))

	rec := c.do(http.MethodGet, "/api/v1/nodes", nil)
	var body struct {
		Nodes []struct {
			ID           uuid.UUID `json:"id"`
			Name         string    `json:"name"`
			VersionCount int       `json:"version_count"`
		} `json:"nodes"`
	}
	decodeInto(t, rec, &body)

	counts := map[string]int{}
	for _, n := range body.Nodes {
		counts[n.Name] = n.VersionCount
	}
	if counts["Versioned.txt"] != 2 {
		t.Errorf("Versioned.txt reports %d versions, want 2", counts["Versioned.txt"])
	}
	if counts["Untouched.txt"] != 0 {
		t.Errorf("Untouched.txt reports %d versions, want 0", counts["Untouched.txt"])
	}
}

// A version is uploaded content, so it gets every protection an ordinary
// download gets. None of them may be skipped because it is old.
func TestAVersionDownloadCarriesTheSameProtectionsAsAnyOtherDownload(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "page.html", []byte("<html><script>alert(1)</script></html>"))
	c.replace(id, []byte("<html>different</html>"))

	history := c.versions(id)
	rec := c.do(http.MethodGet,
		fmt.Sprintf("/api/v1/nodes/%s/versions/%s/download", id, history[0].ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("download: %d %s", rec.Code, rec.Body)
	}

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !contains(csp, "sandbox") {
		t.Errorf("CSP = %q, want the sandbox directive", csp)
	}
	if ct := rec.Header().Get("Content-Type"); contains(ct, "html") {
		t.Errorf("Content-Type = %q — an old HTML upload is served as HTML", ct)
	}
	// The filename identifies which version it is.
	if cd := rec.Header().Get("Content-Disposition"); !contains(cd, "v1") {
		t.Errorf("Content-Disposition = %q, want the version number in the name", cd)
	}
}

// --- Helpers ---------------------------------------------------------------

type versionRow struct {
	ID     uuid.UUID `json:"id"`
	Number int       `json:"version"`
	Name   string    `json:"name"`
	Size   int64     `json:"size_bytes"`
	Reason string    `json:"reason"`
}

// versions fetches a file's history, newest first.
func (c *client) versions(nodeID uuid.UUID) []versionRow {
	c.t.Helper()
	rec := c.do(http.MethodGet, "/api/v1/nodes/"+nodeID.String()+"/versions", nil)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("versions of %s: %d %s", nodeID, rec.Code, rec.Body)
	}
	var body struct {
		Versions []versionRow `json:"versions"`
	}
	decodeInto(c.t, rec, &body)
	return body.Versions
}

// replace overwrites a file's content, which is what creates a version.
func (c *client) replace(nodeID uuid.UUID, content []byte) {
	c.t.Helper()
	target := fmt.Sprintf("/api/v1/files?name=%s&replace_id=%s",
		urlEncode("replacement"), nodeID)
	rec := c.raw(http.MethodPost, target, "application/octet-stream", bytes.NewReader(content))
	if rec.Code != http.StatusCreated {
		c.t.Fatalf("replace %s: %d %s", nodeID, rec.Code, rec.Body)
	}
}

// download returns a file's current content as a string.
func (c *client) download(nodeID uuid.UUID) string {
	c.t.Helper()
	rec := c.do(http.MethodGet, "/api/v1/nodes/"+nodeID.String()+"/download", nil)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("download %s: %d %s", nodeID, rec.Code, rec.Body)
	}
	return rec.Body.String()
}

// versionContent returns one version's bytes as a string.
func (c *client) versionContent(nodeID, versionID uuid.UUID) string {
	c.t.Helper()
	rec := c.do(http.MethodGet,
		fmt.Sprintf("/api/v1/nodes/%s/versions/%s/download", nodeID, versionID), nil)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("download version %s: %d %s", versionID, rec.Code, rec.Body)
	}
	return rec.Body.String()
}

// mustRestore points a file back at a version and fails if it does not work.
func (c *client) mustRestore(nodeID, versionID uuid.UUID) {
	c.t.Helper()
	rec := c.do(http.MethodPost,
		fmt.Sprintf("/api/v1/nodes/%s/versions/%s/restore", nodeID, versionID), nil)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("restore %s: %d %s", versionID, rec.Code, rec.Body)
	}
}

// blobCount is how many distinct contents are stored.
func (h *harness) blobCount() int {
	h.t.Helper()
	var n int
	if err := h.db.Pool().QueryRow(h.t.Context(), `SELECT count(*) FROM blobs`).Scan(&n); err != nil {
		h.t.Fatalf("counting blobs: %v", err)
	}
	return n
}

// unreferencedBlobs is how many stored contents nothing points at.
func (h *harness) unreferencedBlobs() int {
	h.t.Helper()
	var n int
	if err := h.db.Pool().QueryRow(h.t.Context(),
		`SELECT count(*) FROM blobs WHERE ref_count = 0`).Scan(&n); err != nil {
		h.t.Fatalf("counting unreferenced blobs: %v", err)
	}
	return n
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
