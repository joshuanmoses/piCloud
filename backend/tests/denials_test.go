package tests

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/audit"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
)

// Phase 13: denials through the API.
//
// A denial is the only way to express "everyone in Accounting may edit this,
// except Bob". Until now the permission model supported them and nothing could
// create one — they existed only in tests that wrote to the database directly,
// which meant the feature was real for the evaluator and imaginary for anybody
// using the product.
//
// The risk a denial introduces is the reason it needs its own permission:
// granting access is bounded by what you hold, but taking it away is not, so
// the wrong boundary would let anyone a folder was shared with lock everybody
// else out of it.

func TestADenialBeatsAGroupGrant(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	excluded := h.signedIn("bob")
	included := h.signedIn("ana")

	folderID := owner.mkdir("root", "Accounting")
	owner.upload(folderID.String(), "Ledger.csv", []byte("figures"))

	groupID := h.createGroup("Accounting")
	h.addToGroup(groupID, "bob")
	h.addToGroup(groupID, "ana")
	h.shareWithGroup(folderID, groupID, permissions.Read|permissions.Download|permissions.Modify)

	// Both can see it through the group.
	if rec := excluded.do(http.MethodGet, "/api/v1/nodes?parent_id="+folderID.String(), nil); rec.Code != http.StatusOK {
		t.Fatalf("bob cannot see the shared folder: %d", rec.Code)
	}
	if rec := included.do(http.MethodGet, "/api/v1/nodes?parent_id="+folderID.String(), nil); rec.Code != http.StatusOK {
		t.Fatalf("ana cannot see the shared folder: %d", rec.Code)
	}

	// The owner takes Bob's access away without touching the group.
	bob := h.mustUser("bob")
	rec := owner.do(http.MethodPost, "/api/v1/shares", map[string]any{
		"node_id":     folderID.String(),
		"user_id":     bob.ID.String(),
		"permissions": []string{"read", "download", "modify"},
		"deny":        true,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating the denial: %d %s", rec.Code, rec.Body)
	}

	// Bob is out — and gets a 404, not a 403, because he can no longer see it.
	if rec := excluded.do(http.MethodGet, "/api/v1/nodes?parent_id="+folderID.String(), nil); rec.Code != http.StatusNotFound {
		t.Errorf("bob still reaches the folder: %d %s", rec.Code, rec.Body)
	}
	// Ana is unaffected, which is the whole point of denying rather than
	// removing the group share.
	if rec := included.do(http.MethodGet, "/api/v1/nodes?parent_id="+folderID.String(), nil); rec.Code != http.StatusOK {
		t.Errorf("ana lost access too: %d %s", rec.Code, rec.Body)
	}
}

// Granting is bounded by what you hold; taking away is not. That asymmetry is
// why a denial needs Manage rather than Share — otherwise anybody a folder was
// shared with could lock everybody else out of it.
func TestDenyingNeedsManageRatherThanShare(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	sharer := h.signedIn("sharer")
	victim := h.signedIn("victim")

	folderID := owner.mkdir("root", "Team")
	// Everything a sharer needs, deliberately without Manage.
	h.share(folderID, "sharer",
		permissions.Read|permissions.Download|permissions.Modify|permissions.Share)
	h.share(folderID, "victim", permissions.Read|permissions.Download)

	victimUser := h.mustUser("victim")
	rec := sharer.do(http.MethodPost, "/api/v1/shares", map[string]any{
		"node_id":     folderID.String(),
		"user_id":     victimUser.ID.String(),
		"permissions": []string{"read"},
		"deny":        true,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — Share alone must not permit a denial: %s",
			rec.Code, rec.Body)
	}

	// And the victim still has access.
	if rec := victim.do(http.MethodGet, "/api/v1/nodes?parent_id="+folderID.String(), nil); rec.Code != http.StatusOK {
		t.Errorf("the victim lost access despite the refusal: %d", rec.Code)
	}

	// The same person *can* still share, which is what they hold.
	third := h.signedIn("third")
	_ = third
	thirdUser := h.mustUser("third")
	if rec := sharer.do(http.MethodPost, "/api/v1/shares", map[string]any{
		"node_id":     folderID.String(),
		"user_id":     thirdUser.ID.String(),
		"permissions": []string{"read"},
	}); rec.Code != http.StatusCreated {
		t.Errorf("the sharer could not share, which they should be able to: %d %s", rec.Code, rec.Body)
	}
}

// Somebody with Manage may deny more than they themselves hold, because taking
// access away cannot increase anybody's.
func TestAManagerMayDenyMoreThanTheyHold(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	manager := h.signedIn("manager")
	other := h.signedIn("other")

	folderID := owner.mkdir("root", "Team")
	// Manage and Read, but not Modify or Delete.
	h.share(folderID, "manager", permissions.Read|permissions.Manage)
	h.share(folderID, "other", permissions.Read|permissions.Download|permissions.Delete)

	otherUser := h.mustUser("other")
	rec := manager.do(http.MethodPost, "/api/v1/shares", map[string]any{
		"node_id":     folderID.String(),
		"user_id":     otherUser.ID.String(),
		"permissions": []string{"delete"},
		"deny":        true,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 — a denial is not bounded by what the "+
			"denier holds, because it cannot increase anybody's access: %s", rec.Code, rec.Body)
	}

	// The other user can still read, but no longer delete.
	fileID, _ := owner.upload(folderID.String(), "Notes.txt", []byte("x"))
	if rec := other.do(http.MethodGet, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusOK {
		t.Errorf("the denial removed more than it named: %d", rec.Code)
	}
	if rec := other.do(http.MethodDelete, "/api/v1/nodes/"+fileID.String(), nil); rec.Code != http.StatusForbidden {
		t.Errorf("delete returned %d, want 403 after being denied", rec.Code)
	}
}

// The evaluator settles ownership before denials, so a denial against the owner
// would sit in the access list looking as though it did something. Refusing is
// clearer than accepting and quietly ignoring it.
func TestTheOwnerCannotBeDenied(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	manager := h.signedIn("manager")

	folderID := owner.mkdir("root", "Mine")
	h.share(folderID, "manager", permissions.All)

	ownerUser := h.mustUser("owner")
	rec := manager.do(http.MethodPost, "/api/v1/shares", map[string]any{
		"node_id":     folderID.String(),
		"user_id":     ownerUser.ID.String(),
		"permissions": []string{"read"},
		"deny":        true,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if !contains(rec.Body.String(), "transfer ownership") {
		t.Errorf("the message does not say what to do instead: %s", rec.Body)
	}

	// And the owner still has full access.
	if rec := owner.do(http.MethodGet, "/api/v1/nodes?parent_id="+folderID.String(), nil); rec.Code != http.StatusOK {
		t.Errorf("the owner lost access: %d", rec.Code)
	}
}

func TestRemovingADenialRestoresAccess(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	excluded := h.signedIn("bob")

	folderID := owner.mkdir("root", "Team")
	h.share(folderID, "bob", permissions.Read|permissions.Download)

	bob := h.mustUser("bob")
	rec := owner.do(http.MethodPost, "/api/v1/shares", map[string]any{
		"node_id":     folderID.String(),
		"user_id":     bob.ID.String(),
		"permissions": []string{"read"},
		"deny":        true,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("deny: %d %s", rec.Code, rec.Body)
	}
	var created struct {
		Share struct {
			ID uuid.UUID `json:"id"`
		} `json:"share"`
	}
	decodeInto(t, rec, &created)

	if rec := excluded.do(http.MethodGet, "/api/v1/nodes?parent_id="+folderID.String(), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bob was not excluded: %d", rec.Code)
	}

	if rec := owner.do(http.MethodDelete,
		"/api/v1/shares/"+created.Share.ID.String(), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("removing the denial: %d %s", rec.Code, rec.Body)
	}

	if rec := excluded.do(http.MethodGet, "/api/v1/nodes?parent_id="+folderID.String(), nil); rec.Code != http.StatusOK {
		t.Errorf("bob's access was not restored: %d %s", rec.Code, rec.Body)
	}
}

// A denial is not something shared *with* you, so it must not appear in the
// list of things people have given you.
func TestADenialDoesNotAppearInSharedWithMe(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	excluded := h.signedIn("bob")

	folderID := owner.mkdir("root", "Team")
	otherID := owner.mkdir("root", "Visible")
	h.share(otherID, "bob", permissions.Read)

	bob := h.mustUser("bob")
	if rec := owner.do(http.MethodPost, "/api/v1/shares", map[string]any{
		"node_id":     folderID.String(),
		"user_id":     bob.ID.String(),
		"permissions": []string{"read"},
		"deny":        true,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("deny: %d %s", rec.Code, rec.Body)
	}

	rec := excluded.do(http.MethodGet, "/api/v1/shared-with-me", nil)
	var body struct {
		Shares []struct {
			Node struct {
				Name string `json:"name"`
			} `json:"node"`
			Deny bool `json:"deny"`
		} `json:"shares"`
	}
	decodeInto(t, rec, &body)

	for _, share := range body.Shares {
		if share.Deny {
			t.Errorf("a denial appears in Shared with me: %+v", share)
		}
		if share.Node.Name == "Team" {
			t.Error("the denied folder appears in Shared with me")
		}
	}
	if len(body.Shares) != 1 {
		t.Errorf("got %d entries, want only the genuinely shared one", len(body.Shares))
	}
}

// --- The completed audit trail -------------------------------------------------

// "What happened to the file called X" is a question somebody asks weeks
// later, and it was unanswerable: renames, moves, and copies were not recorded
// at all.
func TestRenamingMovingAndCopyingAreRecorded(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")
	c := h.signedIn("josh")

	folderID := c.mkdir("root", "Destination")
	fileID, _ := c.upload("root", "Original.txt", []byte("content"))

	if rec := c.do(http.MethodPatch, "/api/v1/nodes/"+fileID.String(),
		map[string]any{"name": "Renamed.txt"}); rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do(http.MethodPatch, "/api/v1/nodes/"+fileID.String(),
		map[string]any{"parent_id": folderID.String()}); rec.Code != http.StatusOK {
		t.Fatalf("move: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do(http.MethodPost, "/api/v1/nodes/"+fileID.String()+"/copy",
		map[string]any{"parent_id": "root"}); rec.Code != http.StatusCreated {
		t.Fatalf("copy: %d %s", rec.Code, rec.Body)
	}

	events := admin.auditEvents("")
	for _, action := range []string{
		audit.ActionNodeRename, audit.ActionNodeMove, audit.ActionNodeCopy,
	} {
		if !hasEvent(events, action, "josh") {
			t.Errorf("%s was not recorded", action)
		}
	}

	// The rename records what it *was*, which is the only thing that makes the
	// record answerable from the old name.
	renames := admin.auditEvents(audit.ActionNodeRename)
	if len(renames) != 1 {
		t.Fatalf("got %d rename events, want 1", len(renames))
	}
	raw := admin.rawAudit()
	if !contains(raw, "Original.txt") {
		t.Error("the trail does not record the name the file used to have")
	}
}

func TestTakingAccessAwayIsRecordedSeparatelyFromGivingIt(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")
	owner := h.signedIn("owner")
	h.signedIn("bob")

	folderID := owner.mkdir("root", "Team")
	bob := h.mustUser("bob")

	if rec := owner.do(http.MethodPost, "/api/v1/shares", map[string]any{
		"node_id": folderID.String(), "user_id": bob.ID.String(),
		"permissions": []string{"read"},
	}); rec.Code != http.StatusCreated {
		t.Fatalf("share: %d %s", rec.Code, rec.Body)
	}
	if rec := owner.do(http.MethodPost, "/api/v1/shares", map[string]any{
		"node_id": folderID.String(), "user_id": bob.ID.String(),
		"permissions": []string{"read"}, "deny": true,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("deny: %d %s", rec.Code, rec.Body)
	}

	events := admin.auditEvents("")
	if !hasEvent(events, audit.ActionShareGrant, "owner") {
		t.Error("the share was not recorded")
	}
	// "Who took Bob's access away" and "who gave Bob access" are different
	// questions; one action for both would answer neither well.
	if !hasEvent(events, audit.ActionShareDeny, "owner") {
		t.Error("the denial was not recorded under its own action")
	}
}

// --- Helpers ---------------------------------------------------------------

// shareWithGroup grants a permission set to a group.
func (h *harness) shareWithGroup(nodeID, groupID uuid.UUID, perms permissions.Set) uuid.UUID {
	h.t.Helper()
	grant, err := h.permissions.Grant(h.t.Context(), permissions.GrantParams{
		NodeID:      nodeID,
		SubjectType: permissions.SubjectGroup,
		SubjectID:   groupID,
		Permissions: perms,
		Inherit:     true,
	})
	if err != nil {
		h.t.Fatalf("granting %s on %s to group %s: %v", perms, nodeID, groupID, err)
	}
	return grant.ID
}

// A grant that a denial has overridden must not be listed as shared with you.
// Otherwise the folder appears in the list and 404s when opened, which reads as
// a broken product rather than as an access decision.
func TestAnOverriddenGrantDisappearsFromSharedWithMe(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	excluded := h.signedIn("bob")

	deniedID := owner.mkdir("root", "Denied")
	keptID := owner.mkdir("root", "Kept")
	h.share(deniedID, "bob", permissions.Read|permissions.Download)
	h.share(keptID, "bob", permissions.Read|permissions.Download)

	bob := h.mustUser("bob")
	if rec := owner.do(http.MethodPost, "/api/v1/shares", map[string]any{
		"node_id": deniedID.String(), "user_id": bob.ID.String(),
		"permissions": []string{"read"}, "deny": true,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("deny: %d %s", rec.Code, rec.Body)
	}

	rec := excluded.do(http.MethodGet, "/api/v1/shared-with-me", nil)
	var body struct {
		Shares []struct {
			Node struct {
				Name string `json:"name"`
			} `json:"node"`
		} `json:"shares"`
	}
	decodeInto(t, rec, &body)

	names := make([]string, 0, len(body.Shares))
	for _, share := range body.Shares {
		names = append(names, share.Node.Name)
	}
	if len(names) != 1 || names[0] != "Kept" {
		t.Errorf("Shared with me lists %v, want only the folder bob can still open", names)
	}
}

// A denial that does not remove Read leaves the share genuinely shared: the
// person can still open the folder, they simply cannot do one thing in it.
func TestAPartialDenialLeavesTheShareListed(t *testing.T) {
	h := newHarness(t)
	owner := h.signedIn("owner")
	limited := h.signedIn("bob")

	folderID := owner.mkdir("root", "Team")
	h.share(folderID, "bob", permissions.Read|permissions.Download|permissions.Delete)

	bob := h.mustUser("bob")
	if rec := owner.do(http.MethodPost, "/api/v1/shares", map[string]any{
		"node_id": folderID.String(), "user_id": bob.ID.String(),
		"permissions": []string{"delete"}, "deny": true,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("deny: %d %s", rec.Code, rec.Body)
	}

	rec := limited.do(http.MethodGet, "/api/v1/shared-with-me", nil)
	var body struct {
		Shares []struct {
			Node struct {
				Name string `json:"name"`
			} `json:"node"`
		} `json:"shares"`
	}
	decodeInto(t, rec, &body)

	if len(body.Shares) != 1 {
		t.Fatalf("got %d entries, want the folder to stay listed: %+v", len(body.Shares), body.Shares)
	}
	// And it genuinely opens.
	if rec := limited.do(http.MethodGet, "/api/v1/nodes?parent_id="+folderID.String(), nil); rec.Code != http.StatusOK {
		t.Errorf("the folder is listed but does not open: %d", rec.Code)
	}
}
