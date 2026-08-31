package tests

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/jobs"
	"github.com/joshuanmoses/piCloud/backend/internal/logging"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
	"github.com/joshuanmoses/piCloud/backend/internal/previews"
	"github.com/joshuanmoses/piCloud/backend/internal/users"
)

// Phase 7: search, favourites, recent activity, tags, ZIP downloads, previews.
//
// The security property under test throughout is the same one Phase 5
// established for listings and Phase 6 for portals: every one of these views is
// a way to reach content, and each must apply the permission model rather than
// assuming an earlier check covered it. A favourites list that returned an
// unshared file, or a search that counted one, would be the leak.

// --- Search ---------------------------------------------------------------

func TestSearchFindsYourOwnFilesByPartialName(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	c.upload("root", "Quarterly Report.pdf", []byte("q4"))
	c.upload("root", "Holiday Photo.jpg", []byte("jpg"))

	results := c.search("report")
	if len(results.Results) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(results.Results), results.Results)
	}
	if results.Results[0].Name != "Quarterly Report.pdf" {
		t.Errorf("found %q, want the report", results.Results[0].Name)
	}
	if results.Total != 1 {
		t.Errorf("total = %d, want 1", results.Total)
	}
}

// The one that matters. A search must never surface — or count — a file the
// caller cannot see.
func TestSearchNeverReturnsSomebodyElsesFiles(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	stranger := h.signedIn("stranger")

	owner.upload("root", "Confidential Salary Report.xlsx", []byte("secret"))

	results := stranger.search("Confidential")
	if len(results.Results) != 0 {
		t.Errorf("a stranger's search returned %d results: %+v", len(results.Results), results.Results)
	}
	// The count is part of the leak: a total of 1 with zero rows would confirm
	// the file exists just as surely as returning its name.
	if results.Total != 0 {
		t.Errorf("total = %d, want 0 — a nonzero count confirms the file exists", results.Total)
	}
}

func TestSearchFindsFilesSharedWithYou(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	colleague := h.signedIn("colleague")

	folderID := owner.mkdir("root", "Team")
	owner.upload(folderID.String(), "Team Roster.csv", []byte("names"))
	h.share(folderID, "colleague", permissions.Read|permissions.Download)

	results := colleague.search("roster")
	if len(results.Results) != 1 {
		t.Fatalf("got %d results, want the shared file: %+v", len(results.Results), results.Results)
	}
	if !results.Results[0].Shared {
		t.Error("the result is not marked as shared")
	}
}

// Revoking access must take the file out of the coworker's search immediately.
func TestARevokedShareDisappearsFromSearch(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	colleague := h.signedIn("colleague")

	folderID := owner.mkdir("root", "Team")
	owner.upload(folderID.String(), "Draft Proposal.docx", []byte("draft"))
	grantID := h.share(folderID, "colleague", permissions.Read)

	if got := len(colleague.search("proposal").Results); got != 1 {
		t.Fatalf("before revocation the colleague found %d results, want 1", got)
	}

	if err := h.permissions.Revoke(t.Context(), grantID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if got := len(colleague.search("proposal").Results); got != 0 {
		t.Errorf("after revocation the colleague still found %d results", got)
	}
}

func TestSearchIgnoresDeletedFiles(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Obsolete Notes.txt", []byte("old"))
	if got := len(c.search("obsolete").Results); got != 1 {
		t.Fatalf("before deletion: %d results, want 1", got)
	}

	if rec := c.do(http.MethodDelete, "/api/v1/nodes/"+id.String(), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}

	if got := len(c.search("obsolete").Results); got != 0 {
		t.Errorf("a trashed file still appears in search (%d results)", got)
	}
}

// A wildcard in the query is data, not syntax. Without escaping, "%" would
// match everything — including, through the ILIKE, files the pattern was never
// meant to reach.
func TestWildcardCharactersInAQueryAreMatchedLiterally(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	c.upload("root", "Notes.txt", []byte("a"))
	c.upload("root", "100% Complete.txt", []byte("b"))

	results := c.search("100%")
	if len(results.Results) != 1 {
		t.Fatalf("got %d results, want only the literal match: %+v", len(results.Results), results.Results)
	}
	if results.Results[0].Name != "100% Complete.txt" {
		t.Errorf("matched %q", results.Results[0].Name)
	}

	// A bare "%" must not become "match everything".
	if got := len(c.search("%%").Results); got != 0 {
		t.Errorf("a wildcard-only query returned %d results, want 0", got)
	}
}

func TestAOneCharacterQueryReturnsNothingRatherThanAnError(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")
	c.upload("root", "Notes.txt", []byte("a"))

	rec := c.do(http.MethodGet, "/api/v1/search?q=N", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an empty search box is a normal state: %s", rec.Code, rec.Body)
	}
}

func TestSearchCanBeFilteredToFoldersOnly(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	c.mkdir("root", "Reports")
	c.upload("root", "Reports.pdf", []byte("x"))

	rec := c.do(http.MethodGet, "/api/v1/search?q=report&kind=folder", nil)
	var body searchBody
	decodeInto(t, rec, &body)

	if len(body.Results) != 1 {
		t.Fatalf("got %d results, want only the folder: %+v", len(body.Results), body.Results)
	}
	if body.Results[0].Kind != "folder" {
		t.Errorf("kind = %q, want folder", body.Results[0].Kind)
	}
}

// A search result should be as informative as the same file one click away in
// its folder: the star, the tag, the thumbnail. They come from the same
// batched decoration, so this is really a test that search uses it.
func TestASearchResultCarriesTheCallersOwnMetadata(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Tagged Report.png", pngFixture(t, 300, 200))
	h.runPreviewJobs()
	c.mustFavorite(id)

	tagID := c.mustCreateTag("Urgent", "#d94a4a")
	if rec := c.do(http.MethodPut,
		fmt.Sprintf("/api/v1/nodes/%s/tags/%s", id, tagID), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("apply tag: %d %s", rec.Code, rec.Body)
	}

	rec := c.do(http.MethodGet, "/api/v1/search?q=report", nil)
	var body struct {
		Results []struct {
			ID         uuid.UUID `json:"id"`
			Favorite   bool      `json:"favorite"`
			HasPreview bool      `json:"has_preview"`
			Tags       []struct {
				Name string `json:"name"`
			} `json:"tags"`
		} `json:"results"`
	}
	decodeInto(t, rec, &body)

	if len(body.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(body.Results))
	}
	got := body.Results[0]
	if !got.Favorite {
		t.Error("the result is not marked as a favourite")
	}
	if !got.HasPreview {
		t.Error("the result does not report its thumbnail")
	}
	if len(got.Tags) != 1 || got.Tags[0].Name != "Urgent" {
		t.Errorf("tags = %+v, want the caller's Urgent tag", got.Tags)
	}
}

// --- Favourites -----------------------------------------------------------

func TestFavouritingIsPrivateToEachUser(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	colleague := h.signedIn("colleague")

	folderID := owner.mkdir("root", "Shared Project")
	h.share(folderID, "colleague", permissions.Read)

	// Both favourite the same folder.
	owner.mustFavorite(folderID)
	colleague.mustFavorite(folderID)

	// The colleague unfavourites theirs.
	if rec := colleague.do(http.MethodDelete,
		"/api/v1/nodes/"+folderID.String()+"/favorite", nil); rec.Code != http.StatusOK {
		t.Fatalf("unfavourite: %d %s", rec.Code, rec.Body)
	}

	// The owner's is untouched.
	if got := owner.favorites(); len(got) != 1 {
		t.Errorf("the owner has %d favourites after the colleague removed theirs, want 1", len(got))
	}
	if got := colleague.favorites(); len(got) != 0 {
		t.Errorf("the colleague has %d favourites, want 0", len(got))
	}
}

func TestYouCannotFavouriteSomethingYouCannotSee(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	stranger := h.signedIn("stranger")

	folderID := owner.mkdir("root", "Private")

	rec := stranger.do(http.MethodPut, "/api/v1/nodes/"+folderID.String()+"/favorite", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 — a 403 would confirm the folder exists: %s", rec.Code, rec.Body)
	}
}

// A favourite on something you lost access to must stop appearing, rather than
// sitting in the list as a link that fails.
func TestAFavouriteDisappearsWhenAccessIsRevoked(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	colleague := h.signedIn("colleague")

	folderID := owner.mkdir("root", "Team")
	grantID := h.share(folderID, "colleague", permissions.Read)
	colleague.mustFavorite(folderID)

	if got := len(colleague.favorites()); got != 1 {
		t.Fatalf("before revocation: %d favourites, want 1", got)
	}

	if err := h.permissions.Revoke(t.Context(), grantID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if got := len(colleague.favorites()); got != 0 {
		t.Errorf("after revocation: %d favourites still listed", got)
	}
}

// Clearing your own list must keep working even after you lose access, or the
// entry becomes impossible to remove.
func TestYouCanAlwaysUnfavouriteEvenAfterLosingAccess(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	colleague := h.signedIn("colleague")

	folderID := owner.mkdir("root", "Team")
	grantID := h.share(folderID, "colleague", permissions.Read)
	colleague.mustFavorite(folderID)

	if err := h.permissions.Revoke(t.Context(), grantID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	rec := colleague.do(http.MethodDelete, "/api/v1/nodes/"+folderID.String()+"/favorite", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — you must be able to clear your own list: %s", rec.Code, rec.Body)
	}
}

func TestAFolderListingReportsWhichItemsAreFavourited(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	keepID := c.mkdir("root", "Keep")
	c.mkdir("root", "Ignore")
	c.mustFavorite(keepID)

	rec := c.do(http.MethodGet, "/api/v1/nodes", nil)
	var body struct {
		Nodes []struct {
			Name     string `json:"name"`
			Favorite bool   `json:"favorite"`
		} `json:"nodes"`
	}
	decodeInto(t, rec, &body)

	found := map[string]bool{}
	for _, n := range body.Nodes {
		found[n.Name] = n.Favorite
	}
	if !found["Keep"] {
		t.Error("the favourited folder is not marked in the listing")
	}
	if found["Ignore"] {
		t.Error("an unfavourited folder is marked in the listing")
	}
}

// --- Recent ---------------------------------------------------------------

func TestDownloadingAFileRecordsItAsRecent(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Notes.txt", []byte("hello"))

	rec := c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/download", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("download: %d", rec.Code)
	}

	items := c.recent()
	actions := map[string]bool{}
	for _, item := range items {
		if item.ID == id {
			actions[item.Action] = true
		}
	}
	if !actions["downloaded"] {
		t.Errorf("the download was not recorded; got actions %v", actions)
	}
	// Uploading it recorded an action too, and both should be present: they
	// are different facts about the same file.
	if !actions["uploaded"] {
		t.Errorf("the upload was not recorded; got actions %v", actions)
	}
}

// Twenty downloads of one file must not bury everything else in the list.
func TestRepeatedActivityUpdatesOneRowRatherThanAppending(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Popular.txt", []byte("x"))
	for range 5 {
		c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/download", nil)
	}

	downloads := 0
	for _, item := range c.recent() {
		if item.ID == id && item.Action == "downloaded" {
			downloads++
		}
	}
	if downloads != 1 {
		t.Errorf("five downloads produced %d rows, want 1", downloads)
	}
}

func TestRecentNeverShowsSomebodyElsesActivity(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	stranger := h.signedIn("stranger")

	owner.upload("root", "Private Notes.txt", []byte("x"))

	if got := len(stranger.recent()); got != 0 {
		t.Errorf("a stranger's Recent shows %d items, want 0", got)
	}
}

// --- Tags -----------------------------------------------------------------

func TestTagsAreScopedToTheirOwner(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	// Both create a tag with the same name. Neither should collide.
	aliceTag := alice.mustCreateTag("Urgent", "#d94a4a")
	bobTag := bob.mustCreateTag("Urgent", "#4a90d9")

	if aliceTag == bobTag {
		t.Fatal("two users' identically named tags share an identifier")
	}

	if got := len(alice.tags()); got != 1 {
		t.Errorf("alice sees %d tags, want only her own", got)
	}
	if got := len(bob.tags()); got != 1 {
		t.Errorf("bob sees %d tags, want only his own", got)
	}
}

func TestYouCannotApplySomebodyElsesTag(t *testing.T) {
	h := newHarness(t)
	alice := h.signedIn("alice")
	bob := h.signedIn("bob")

	aliceTag := alice.mustCreateTag("Confidential", "")
	bobFileID, _ := bob.upload("root", "Notes.txt", []byte("x"))

	rec := bob.do(http.MethodPut,
		fmt.Sprintf("/api/v1/nodes/%s/tags/%s", bobFileID, aliceTag), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for another user's tag: %s", rec.Code, rec.Body)
	}
}

// A tag applied to a shared file is a personal label, not an annotation the
// other party sees.
func TestATagOnASharedFileIsInvisibleToTheOtherParty(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	colleague := h.signedIn("colleague")

	folderID := owner.mkdir("root", "Team")
	fileID, _ := owner.upload(folderID.String(), "Proposal.docx", []byte("x"))
	h.share(folderID, "colleague", permissions.Read)

	tagID := colleague.mustCreateTag("Rejected", "")
	if rec := colleague.do(http.MethodPut,
		fmt.Sprintf("/api/v1/nodes/%s/tags/%s", fileID, tagID), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("apply tag: %d %s", rec.Code, rec.Body)
	}

	// The colleague sees their own label.
	if got := len(colleague.nodeTags(fileID)); got != 1 {
		t.Errorf("the colleague sees %d tags on the file, want their own 1", got)
	}
	// The owner sees nothing.
	if got := len(owner.nodeTags(fileID)); got != 0 {
		t.Errorf("the owner sees %d tags applied by somebody else, want 0", got)
	}
}

func TestATagColourMustBeAPlainHexValue(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	// This value reaches a CSS context in the browser, so it is validated
	// rather than sanitised.
	rec := c.do(http.MethodPost, "/api/v1/tags",
		map[string]any{"name": "Sneaky", "color": "#000; background:url(https://evil.example/x)"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 for a non-hex colour: %s", rec.Code, rec.Body)
	}
}

func TestSearchCanFilterByYourOwnTag(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	taggedID, _ := c.upload("root", "Report A.pdf", []byte("a"))
	c.upload("root", "Report B.pdf", []byte("b"))

	tagID := c.mustCreateTag("Urgent", "")
	if rec := c.do(http.MethodPut,
		fmt.Sprintf("/api/v1/nodes/%s/tags/%s", taggedID, tagID), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("apply tag: %d %s", rec.Code, rec.Body)
	}

	rec := c.do(http.MethodGet, "/api/v1/search?q=report&tag=Urgent", nil)
	var body searchBody
	decodeInto(t, rec, &body)

	if len(body.Results) != 1 {
		t.Fatalf("got %d results, want only the tagged file: %+v", len(body.Results), body.Results)
	}
	if body.Results[0].ID != taggedID {
		t.Errorf("matched the wrong file")
	}
}

// Two users may each have a tag called "Urgent". A filter on the name must not
// be satisfied by the other person's tag on a file they happen to share.
func TestATagFilterIsNotSatisfiedBySomebodyElsesIdenticallyNamedTag(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	colleague := h.signedIn("colleague")

	folderID := owner.mkdir("root", "Team")
	fileID, _ := owner.upload(folderID.String(), "Shared Report.pdf", []byte("x"))
	h.share(folderID, "colleague", permissions.Read)

	// The owner tags it "Urgent".
	ownerTag := owner.mustCreateTag("Urgent", "")
	if rec := owner.do(http.MethodPut,
		fmt.Sprintf("/api/v1/nodes/%s/tags/%s", fileID, ownerTag), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("apply tag: %d %s", rec.Code, rec.Body)
	}

	// The colleague has their own "Urgent" tag, applied to nothing.
	colleague.mustCreateTag("Urgent", "")

	rec := colleague.do(http.MethodGet, "/api/v1/search?q=report&tag=Urgent", nil)
	var body searchBody
	decodeInto(t, rec, &body)

	if len(body.Results) != 0 {
		t.Errorf("the colleague's tag filter matched %d files through the owner's tag: %+v",
			len(body.Results), body.Results)
	}
}

// --- ZIP downloads --------------------------------------------------------

func TestAFolderDownloadsAsAZipContainingItsTree(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	folderID := c.mkdir("root", "Project")
	c.upload(folderID.String(), "README.md", []byte("# Project"))
	subID := c.mkdir(folderID.String(), "src")
	c.upload(subID.String(), "main.go", []byte("package main"))
	c.mkdir(folderID.String(), "empty")

	names, contents := c.downloadArchive("/api/v1/nodes/" + folderID.String() + "/archive")

	want := []string{"Project/", "Project/README.md", "Project/empty/", "Project/src/", "Project/src/main.go"}
	sort.Strings(names)
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Errorf("archive contains:\n  %v\nwant:\n  %v", names, want)
	}
	if got := string(contents["Project/README.md"]); got != "# Project" {
		t.Errorf("README.md content = %q", got)
	}
}

// A root folder has no name — "My Files" is a label the interface applies, not
// something stored — so wrapping the archive in it produced a directory called
// "unnamed". Downloading everything should give you your files, not a mystery
// folder containing them.
func TestDownloadingYourRootPutsFilesAtTheTopOfTheArchive(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	c.upload("root", "Notes.txt", []byte("notes"))
	folderID := c.mkdir("root", "Project")
	c.upload(folderID.String(), "README.md", []byte("# hi"))

	root, err := h.nodes.Root(t.Context(), h.mustUser("josh").ID)
	if err != nil {
		t.Fatalf("root: %v", err)
	}

	names, contents := c.downloadArchive("/api/v1/nodes/" + root.ID.String() + "/archive")

	for _, name := range names {
		if strings.HasPrefix(name, "unnamed") {
			t.Errorf("archive contains %q — the root's empty name leaked into a directory", name)
		}
	}

	sort.Strings(names)
	want := []string{"Notes.txt", "Project/", "Project/README.md"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Errorf("archive contains:\n  %v\nwant:\n  %v", names, want)
	}
	if got := string(contents["Notes.txt"]); got != "notes" {
		t.Errorf("Notes.txt content = %q", got)
	}
}

// The archive walk reads content straight from storage. A missed permission
// check here would hand over a whole subtree.
func TestAStrangerCannotDownloadYourFolderAsAZip(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	stranger := h.signedIn("stranger")

	folderID := owner.mkdir("root", "Private")
	owner.upload(folderID.String(), "Secret.txt", []byte("secret"))

	rec := stranger.do(http.MethodGet, "/api/v1/nodes/"+folderID.String()+"/archive", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("secret")) {
		t.Error("the response body contains the file's content")
	}
}

// Read alone is not enough: seeing a folder in a listing does not entitle you
// to bulk-export every byte in it.
func TestAViewerWithoutDownloadCannotZipAFolder(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	viewer := h.signedIn("viewer")

	folderID := owner.mkdir("root", "Team")
	owner.upload(folderID.String(), "Notes.txt", []byte("x"))
	h.share(folderID, "viewer", permissions.Read)

	rec := viewer.do(http.MethodGet, "/api/v1/nodes/"+folderID.String()+"/archive", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a visible folder the caller may not download: %s",
			rec.Code, rec.Body)
	}
}

func TestASelectionArchiveSkipsWhatTheCallerCannotRead(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	colleague := h.signedIn("colleague")

	sharedFolder := owner.mkdir("root", "Shared")
	visibleID, _ := owner.upload(sharedFolder.String(), "Visible.txt", []byte("visible"))
	h.share(sharedFolder, "colleague", permissions.Read|permissions.Download)

	hiddenID, _ := owner.upload("root", "Hidden.txt", []byte("hidden"))

	target := fmt.Sprintf("/api/v1/archive?id=%s&id=%s", visibleID, hiddenID)
	names, contents := colleague.downloadArchive(target)

	if len(names) != 1 || names[0] != "Visible.txt" {
		t.Fatalf("archive contains %v, want only Visible.txt", names)
	}
	for _, body := range contents {
		if bytes.Contains(body, []byte("hidden")) {
			t.Error("the archive contains content the caller cannot read")
		}
	}
}

func TestASelectionOfNothingReadableProducesNoContentRatherThanALeak(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	stranger := h.signedIn("stranger")

	hiddenID, _ := owner.upload("root", "Hidden.txt", []byte("hidden"))
	// A UUID naming nothing at all, so the two cases are indistinguishable.
	imaginary := uuid.New()

	for _, id := range []uuid.UUID{hiddenID, imaginary} {
		rec := stranger.do(http.MethodGet, "/api/v1/archive?id="+id.String(), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d for %s", rec.Code, id)
		}
		if bytes.Contains(rec.Body.Bytes(), []byte("hidden")) {
			t.Errorf("the response for %s contains protected content", id)
		}
	}
}

// Within one folder PiFiles already forbids two siblings whose names differ
// only by case, so a folder archive cannot collide. A *selection* can: the
// items come from different folders, and at the top level of the archive they
// land side by side. Neither may silently overwrite the other on extraction.
func TestASelectionOfIdenticallyNamedFilesKeepsBothOfThem(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	firstFolder := c.mkdir("root", "January")
	secondFolder := c.mkdir("root", "February")
	firstID, _ := c.upload(firstFolder.String(), "Report.txt", []byte("january"))
	secondID, _ := c.upload(secondFolder.String(), "report.txt", []byte("february"))

	names, contents := c.downloadArchive(
		fmt.Sprintf("/api/v1/archive?id=%s&id=%s", firstID, secondID))

	if len(names) != 2 {
		t.Fatalf("archive holds %d entries (%v), want both files", len(names), names)
	}
	seen := map[string]bool{}
	for _, name := range names {
		lower := strings.ToLower(name)
		if seen[lower] {
			t.Errorf("two entries collide on a case-insensitive filesystem: %q in %v", name, names)
		}
		seen[lower] = true
	}

	// And both payloads survived, rather than one overwriting the other.
	bodies := map[string]bool{}
	for _, body := range contents {
		bodies[string(body)] = true
	}
	if !bodies["january"] || !bodies["february"] {
		t.Errorf("one of the two files was lost: %v", bodies)
	}
}

// A folder name is user-supplied and reaches a response header. Quotes,
// newlines, and the other header-splitting characters are rejected at creation,
// so what actually arrives here is non-ASCII — which must survive rather than
// be mangled, and must not escape the header while doing so.
func TestANonASCIIFolderNameSurvivesTheDownloadHeaderIntact(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	folderID := c.mkdir("root", "Contrat à signer")

	rec := c.do(http.MethodGet, "/api/v1/nodes/"+folderID.String()+"/archive", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	disposition := rec.Header().Get("Content-Disposition")
	if strings.ContainsAny(disposition, "\r\n") {
		t.Errorf("Content-Disposition contains a line break: %q", disposition)
	}
	if !strings.HasPrefix(disposition, "attachment") {
		t.Errorf("Content-Disposition = %q, want an attachment", disposition)
	}
	// RFC 5987, or the accent arrives as an underscore.
	if !strings.Contains(disposition, "filename*=UTF-8''") {
		t.Errorf("Content-Disposition = %q, want an RFC 5987 filename*", disposition)
	}
	if !strings.Contains(disposition, "%C3%A0") {
		t.Errorf("Content-Disposition = %q, want the accented character percent-encoded", disposition)
	}
}

func TestAFileRequestedAsAnArchiveRedirectsRatherThanFailing(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Notes.txt", []byte("x"))

	rec := c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/archive", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a file on the folder-archive route: %s", rec.Code, rec.Body)
	}
}

// --- Previews -------------------------------------------------------------

func TestAPreviewIsGeneratedForAnImageAndServedAsJPEG(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Photo.png", pngFixture(t, 1200, 900))

	// The upload queued the job; run it here rather than starting a worker,
	// so the test is deterministic.
	h.runPreviewJobs()

	rec := c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/preview", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != previews.ContentType {
		t.Errorf("Content-Type = %q, want %q", got, previews.ContentType)
	}
	// Uploaded content must never be sniffed into another type.
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	// A thumbnail is somebody's content; no shared cache may hold it.
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "private") {
		t.Errorf("Cache-Control = %q, want it marked private", got)
	}
	if rec.Body.Len() == 0 {
		t.Error("the preview body is empty")
	}
}

func TestAStrangerCannotFetchYourThumbnail(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	stranger := h.signedIn("stranger")

	id, _ := owner.upload("root", "Photo.png", pngFixture(t, 400, 300))
	h.runPreviewJobs()

	rec := stranger.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/preview", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

// A file whose extension lies must not be executed, decoded successfully, or
// retried forever. It is recorded as unavailable and left alone.
func TestAFileLyingAboutItsTypeIsRecordedAsUnavailableRatherThanRetried(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	// Real PNG magic bytes so the server sniffs image/png, followed by
	// garbage so no decoder can make an image of it.
	fake := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte("not an image"), 100)...)
	id, _ := c.upload("root", "Trojan.png", fake)

	h.runPreviewJobs()

	record, err := h.previews.Store().Get(t.Context(), id)
	if err != nil {
		t.Fatalf("no preview record was written: %v", err)
	}
	if record.Status != previews.StatusUnavailable {
		t.Errorf("status = %q, want unavailable", record.Status)
	}

	// And the interface is told so, without being told why.
	rec := c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/preview/status", nil)
	var body map[string]any
	decodeInto(t, rec, &body)
	if body["available"] != false {
		t.Errorf("available = %v, want false", body["available"])
	}
	if _, leaked := body["detail"]; leaked {
		t.Error("the response includes the decoder's failure detail")
	}
}

func TestNoPreviewIsAttemptedForAnUnsupportedType(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Document.pdf", []byte("%PDF-1.4\nnot really"))

	rec := c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/preview/status", nil)
	var body map[string]any
	decodeInto(t, rec, &body)

	if body["status"] != string(previews.StatusUnavailable) {
		t.Errorf("status = %v, want unavailable for a PDF", body["status"])
	}
}

// --- Helpers ---------------------------------------------------------------

type searchBody struct {
	Results []struct {
		ID     uuid.UUID `json:"id"`
		Name   string    `json:"name"`
		Kind   string    `json:"kind"`
		Shared bool      `json:"shared"`
		Score  float64   `json:"score"`
	} `json:"results"`
	Total int `json:"total"`
}

func (c *client) search(query string) searchBody {
	c.t.Helper()
	rec := c.do(http.MethodGet, "/api/v1/search?q="+urlEncode(query), nil)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("search %q: %d %s", query, rec.Code, rec.Body)
	}
	var body searchBody
	decodeInto(c.t, rec, &body)
	return body
}

func (c *client) mustFavorite(id uuid.UUID) {
	c.t.Helper()
	rec := c.do(http.MethodPut, "/api/v1/nodes/"+id.String()+"/favorite", nil)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("favourite %s: %d %s", id, rec.Code, rec.Body)
	}
}

func (c *client) favorites() []struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
} {
	c.t.Helper()
	rec := c.do(http.MethodGet, "/api/v1/favorites", nil)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("favourites: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Nodes []struct {
			ID   uuid.UUID `json:"id"`
			Name string    `json:"name"`
		} `json:"nodes"`
	}
	decodeInto(c.t, rec, &body)
	return body.Nodes
}

type recentItem struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Action string    `json:"action"`
}

func (c *client) recent() []recentItem {
	c.t.Helper()
	rec := c.do(http.MethodGet, "/api/v1/recent", nil)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("recent: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Items []recentItem `json:"items"`
	}
	decodeInto(c.t, rec, &body)
	return body.Items
}

func (c *client) mustCreateTag(name, color string) uuid.UUID {
	c.t.Helper()
	rec := c.do(http.MethodPost, "/api/v1/tags", map[string]any{"name": name, "color": color})
	if rec.Code != http.StatusCreated {
		c.t.Fatalf("create tag %q: %d %s", name, rec.Code, rec.Body)
	}
	var body struct {
		ID uuid.UUID `json:"id"`
	}
	decodeInto(c.t, rec, &body)
	return body.ID
}

func (c *client) tags() []struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
} {
	c.t.Helper()
	rec := c.do(http.MethodGet, "/api/v1/tags", nil)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("tags: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Tags []struct {
			ID   uuid.UUID `json:"id"`
			Name string    `json:"name"`
		} `json:"tags"`
	}
	decodeInto(c.t, rec, &body)
	return body.Tags
}

func (c *client) nodeTags(nodeID uuid.UUID) []struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
} {
	c.t.Helper()
	rec := c.do(http.MethodGet, "/api/v1/nodes/"+nodeID.String()+"/tags", nil)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("node tags: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Tags []struct {
			ID   uuid.UUID `json:"id"`
			Name string    `json:"name"`
		} `json:"tags"`
	}
	decodeInto(c.t, rec, &body)
	return body.Tags
}

// downloadArchive fetches a ZIP and returns its entry names and their contents.
func (c *client) downloadArchive(target string) ([]string, map[string][]byte) {
	c.t.Helper()

	rec := c.do(http.MethodGet, target, nil)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("archive %s: %d %s", target, rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/zip" {
		c.t.Fatalf("Content-Type = %q, want application/zip", got)
	}

	body := rec.Body.Bytes()
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		c.t.Fatalf("the response is not a readable ZIP: %v", err)
	}

	names := make([]string, 0, len(reader.File))
	contents := make(map[string][]byte, len(reader.File))
	for _, f := range reader.File {
		names = append(names, f.Name)
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			c.t.Fatalf("opening %q in the archive: %v", f.Name, err)
		}
		data := new(bytes.Buffer)
		if _, err := data.ReadFrom(rc); err != nil {
			c.t.Fatalf("reading %q from the archive: %v", f.Name, err)
		}
		rc.Close()
		contents[f.Name] = data.Bytes()
	}
	return names, contents
}

// runPreviewJobs drains the queue synchronously.
//
// Deliberately not a background worker: a test that started one would race its
// own assertions, and a sleep long enough to be reliable would make the suite
// slow. Drain uses the runner's real execution path, so what this exercises is
// the handler production runs.
func (h *harness) runPreviewJobs() {
	h.t.Helper()

	runner := jobs.NewRunner(h.jobs, jobs.Config{Workers: 1}, logging.Discard())
	h.previews.RegisterHandler(runner)

	if _, err := runner.Drain(h.t.Context(), 20); err != nil {
		h.t.Fatalf("draining the job queue: %v", err)
	}
}

// share grants a permission set on a node to a user, by name.
func (h *harness) share(nodeID uuid.UUID, username string, perms permissions.Set) uuid.UUID {
	h.t.Helper()

	subject, err := h.users.GetByUsername(h.t.Context(), username)
	if err != nil {
		h.t.Fatalf("looking up %q: %v", username, err)
	}
	grant, err := h.permissions.Grant(h.t.Context(), permissions.GrantParams{
		NodeID:      nodeID,
		SubjectType: permissions.SubjectUser,
		SubjectID:   subject.ID,
		Permissions: perms,
		Inherit:     true,
	})
	if err != nil {
		h.t.Fatalf("granting %s on %s to %q: %v", perms, nodeID, username, err)
	}
	return grant.ID
}

// pngFixture builds a real PNG of the given size.
func pngFixture(t testing.TB, w, h int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding the PNG fixture: %v", err)
	}
	return buf.Bytes()
}

func decodeInto(t testing.TB, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decoding the response: %v\nbody: %s", err, rec.Body)
	}
}

// --- Download response headers ---------------------------------------------

// The preview dialog frames a PDF from the ordinary download endpoint, so that
// endpoint must permit same-origin framing. `frame-ancestors 'none'` blocks
// PiFiles' own iframe as surely as anybody else's, and the failure is silent:
// an empty box, no error, nothing the user can act on.
func TestContentMayBeFramedByPiFilesItselfButByNobodyElse(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Report.pdf", []byte("%PDF-1.4\n%stub\n"))

	rec := c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/download", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("download: %d %s", rec.Code, rec.Body)
	}

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Errorf("CSP = %q, want frame-ancestors 'self' so the preview can render it", csp)
	}
	// The sandbox is what actually contains the document, and it must stay.
	if !strings.Contains(csp, "sandbox") {
		t.Errorf("CSP = %q, want the sandbox directive", csp)
	}
	if !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q, want default-src 'none'", csp)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("the download response does not forbid MIME sniffing")
	}
}

// A thumbnail is only ever an <img>. Nothing should frame it, PiFiles included.
func TestAThumbnailMayNotBeFramedAtAll(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "Photo.png", pngFixture(t, 300, 200))
	h.runPreviewJobs()

	rec := c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/preview", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q, want frame-ancestors 'none'", csp)
	}
}

// A script-bearing upload must never come back as something a browser will
// execute — and now that the download endpoint permits same-origin framing,
// that guarantee is load-bearing for the preview dialog too.
//
// The defence is at upload: the sniffed type is neutralised, so HTML is stored
// and served as text/plain and the browser shows its source. Nothing about the
// *response* has to be right for this to hold.
func TestAnUploadedHTMLFileIsNeutralisedIntoPlainText(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	id, _ := c.upload("root", "page.html", []byte("<html><script>alert(1)</script></html>"))

	rec := c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/download", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("download: %d %s", rec.Code, rec.Body)
	}

	contentType := rec.Header().Get("Content-Type")
	if strings.Contains(strings.ToLower(contentType), "html") {
		t.Errorf("Content-Type = %q — an uploaded HTML file is served as HTML", contentType)
	}
	if !strings.HasPrefix(contentType, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", contentType)
	}
	// Belt and braces: even served as text/plain, the browser must not sniff
	// its way back to HTML.
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("the response does not forbid MIME sniffing")
	}
	// And the sandbox still contains it, framed or not.
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Errorf("CSP = %q, want the sandbox directive", csp)
	}
}

// SVG is the other script-bearing type that looks harmless. It must not come
// back as an image, or the preview would hand it straight to a renderer.
func TestAnUploadedSVGIsNotServedAsAnImage(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	id, _ := c.upload("root", "logo.svg", svg)

	rec := c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/download", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("download: %d %s", rec.Code, rec.Body)
	}
	if contentType := rec.Header().Get("Content-Type"); strings.Contains(contentType, "svg") {
		t.Errorf("Content-Type = %q — an uploaded SVG is served as SVG", contentType)
	}
}

// mustUser looks up an account by name, for tests that need its ID.
func (h *harness) mustUser(username string) *users.User {
	h.t.Helper()
	u, err := h.users.GetByUsername(h.t.Context(), username)
	if err != nil {
		h.t.Fatalf("looking up %q: %v", username, err)
	}
	return u
}
