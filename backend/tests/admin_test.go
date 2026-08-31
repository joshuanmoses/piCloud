package tests

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/audit"
	"github.com/joshuanmoses/piCloud/backend/internal/jobs"
)

// Phase 9: administration and the audit trail.
//
// The boundary under test throughout is the one that is easiest to get wrong
// and worst to get wrong: an administrator manages accounts and the instance,
// and is *not* thereby entitled to read what people store on it. Half of these
// tests exist to prove that an administrator gets a 404 from somebody else's
// file, and the other half to prove that what they do get is recorded.

// --- The administrator boundary ---------------------------------------------

// The single most important property in this phase. Running a server is not the
// same as being entitled to read what people keep on it, and an audit trail
// could not distinguish legitimate support from snooping if it were.
func TestAnAdministratorCannotReadOtherPeoplesFiles(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")
	person := h.signedIn("josh")

	folderID := person.mkdir("root", "Private")
	fileID, _ := person.upload(folderID.String(), "Diary.txt", []byte("dear diary"))

	// Every route into content, tried as the administrator.
	for _, target := range []string{
		"/api/v1/nodes/" + fileID.String(),
		"/api/v1/nodes/" + fileID.String() + "/download",
		"/api/v1/nodes/" + folderID.String() + "/archive",
		"/api/v1/nodes/" + fileID.String() + "/versions",
		"/api/v1/nodes/" + fileID.String() + "/preview",
	} {
		rec := admin.do(http.MethodGet, target, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s as an administrator returned %d, want 404: %s",
				target, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "dear diary") {
			t.Errorf("GET %s leaked the file's content to an administrator", target)
		}
	}

	// And listing their folder does not work either.
	rec := admin.do(http.MethodGet, "/api/v1/nodes?parent_id="+folderID.String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("listing another user's folder returned %d, want 404: %s", rec.Code, rec.Body)
	}
}

// The administration API itself must be closed to everybody else.
func TestTheAdministrationAPIIsClosedToOrdinaryUsers(t *testing.T) {
	h := newHarness(t)
	h.signedInAdmin("boss")
	person := h.signedIn("josh")

	for _, route := range []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/admin/overview"},
		{http.MethodGet, "/api/v1/admin/users"},
		{http.MethodPost, "/api/v1/admin/users"},
		{http.MethodGet, "/api/v1/admin/sessions"},
		{http.MethodGet, "/api/v1/admin/audit"},
		{http.MethodGet, "/api/v1/admin/jobs"},
	} {
		rec := person.do(route.method, route.path, map[string]any{})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as an ordinary user returned %d, want 403: %s",
				route.method, route.path, rec.Code, rec.Body)
		}
	}
}

// --- Account management ------------------------------------------------------

func TestAnAdministratorCanCreateAnAccountThatMustChooseItsOwnPassword(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")

	rec := admin.do(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username":     "newcomer",
		"display_name": "New Comer",
		"password":     "a temporary passphrase for now",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}

	var body struct {
		User struct {
			ID                 uuid.UUID `json:"id"`
			Username           string    `json:"username"`
			IsAdmin            bool      `json:"is_admin"`
			MustChangePassword bool      `json:"must_change_password"`
		} `json:"user"`
	}
	decodeInto(t, rec, &body)

	if body.User.Username != "newcomer" {
		t.Errorf("username = %q", body.User.Username)
	}
	if body.User.IsAdmin {
		t.Error("a new account was made an administrator by default")
	}
	// The default that matters: an administrator who sets somebody's password
	// should not be able to keep signing in as them.
	if !body.User.MustChangePassword {
		t.Error("the new account is not required to choose its own password")
	}
}

func TestAWeakPasswordIsRefusedForAnAdministratorCreatedAccountToo(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")

	rec := admin.do(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"username": "newcomer",
		"password": "password123",
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 for a weak password: %s", rec.Code, rec.Body)
	}
}

// Disabling somebody must actually sign them out. Leaving their cookie working
// until it expires is not what "disabled" means to whoever clicked it.
func TestDisablingAnAccountEndsItsSessionsImmediately(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")
	person := h.signedIn("josh")
	target := h.mustUser("josh")

	// They are signed in and it works.
	if rec := person.do(http.MethodGet, "/api/v1/nodes", nil); rec.Code != http.StatusOK {
		t.Fatalf("before disabling: %d", rec.Code)
	}

	rec := admin.do(http.MethodPatch, "/api/v1/admin/users/"+target.ID.String(),
		map[string]any{"is_enabled": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body)
	}

	if rec := person.do(http.MethodGet, "/api/v1/nodes", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("a disabled account's session still works: %d", rec.Code)
	}
}

// Resetting a password is what an administrator does when an account may be
// compromised. Leaving the intruder's session alive would defeat the exercise.
func TestResettingAPasswordEndsEverySession(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")
	person := h.signedIn("josh")
	target := h.mustUser("josh")

	rec := admin.do(http.MethodPost, "/api/v1/admin/users/"+target.ID.String()+"/password",
		map[string]any{"password": "a replacement passphrase entirely"})
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body)
	}

	if rec := person.do(http.MethodGet, "/api/v1/nodes", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("a session survived a password reset: %d", rec.Code)
	}
}

// The refusals that stop an administrator locking everybody out.
func TestAnAdministratorCannotStrandTheInstance(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")
	self := h.mustUser("boss")

	cases := []struct {
		name string
		body map[string]any
	}{
		{"demoting themselves", map[string]any{"is_admin": false}},
		{"disabling themselves", map[string]any{"is_enabled": false}},
	}
	for _, tc := range cases {
		rec := admin.do(http.MethodPatch, "/api/v1/admin/users/"+self.ID.String(), tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s returned %d, want 400: %s", tc.name, rec.Code, rec.Body)
		}
	}

	rec := admin.do(http.MethodDelete, "/api/v1/admin/users/"+self.ID.String(), nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("deleting their own account returned %d, want 400: %s", rec.Code, rec.Body)
	}

	// And the last administrator cannot be demoted by anybody, including a
	// second administrator who then demotes themselves first.
	other := h.createUser("second", "another perfectly fine passphrase", true)
	if rec := admin.do(http.MethodPatch, "/api/v1/admin/users/"+other.ID.String(),
		map[string]any{"is_admin": false}); rec.Code != http.StatusOK {
		t.Fatalf("demoting a second administrator: %d %s", rec.Code, rec.Body)
	}
}

func TestAQuotaCanBeSetAndClearedDistinctly(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")
	target := h.mustUser("boss")

	// A number sets it.
	rec := admin.do(http.MethodPatch, "/api/v1/admin/users/"+target.ID.String(),
		map[string]any{"quota_bytes": 5_000_000})
	if rec.Code != http.StatusOK {
		t.Fatalf("set quota: %d %s", rec.Code, rec.Body)
	}
	if got := h.mustUser("boss").QuotaBytes; got == nil || *got != 5_000_000 {
		t.Fatalf("quota = %v, want 5000000", got)
	}

	// An omitted field leaves it alone — the distinction a plain pointer
	// cannot express.
	if rec := admin.do(http.MethodPatch, "/api/v1/admin/users/"+target.ID.String(),
		map[string]any{"display_name": "Root User"}); rec.Code != http.StatusOK {
		t.Fatalf("update without a quota: %d %s", rec.Code, rec.Body)
	}
	if got := h.mustUser("boss").QuotaBytes; got == nil || *got != 5_000_000 {
		t.Errorf("quota = %v after an unrelated update, want it unchanged", got)
	}

	// An explicit null clears it.
	if rec := admin.do(http.MethodPatch, "/api/v1/admin/users/"+target.ID.String(),
		map[string]any{"quota_bytes": nil}); rec.Code != http.StatusOK {
		t.Fatalf("clear quota: %d %s", rec.Code, rec.Body)
	}
	if got := h.mustUser("boss").QuotaBytes; got != nil {
		t.Errorf("quota = %v after an explicit null, want unlimited", got)
	}
}

// --- Sessions ----------------------------------------------------------------

func TestAPersonCanSeeAndEndTheirOwnSessions(t *testing.T) {
	h := newHarness(t)
	const password = "a perfectly reasonable passphrase"
	h.createUser("josh", password, false)

	first := h.newClient()
	if rec := first.login("josh", password); rec.Code != http.StatusOK {
		t.Fatalf("first sign-in: %d", rec.Code)
	}
	second := h.newClient()
	if rec := second.login("josh", password); rec.Code != http.StatusOK {
		t.Fatalf("second sign-in: %d", rec.Code)
	}

	// The second sign-in sees both, and knows which one it is.
	list := second.sessions()
	current := 0
	for _, s := range list {
		if s.Current {
			current++
		}
	}
	if current != 1 {
		t.Errorf("%d sessions are marked current, want exactly 1", current)
	}
	// A recognisable device label is what lets somebody spot a session they
	// did not start.
	for _, s := range list {
		if s.Device == "" && s.UserAgent != "" {
			t.Errorf("session %s has a user agent but no readable device label", s.ID)
		}
	}

	// Signing out everywhere else keeps this session and ends the other.
	rec := second.do(http.MethodPost, "/api/v1/auth/sessions/revoke-others", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke others: %d %s", rec.Code, rec.Body)
	}
	if rec := second.do(http.MethodGet, "/api/v1/nodes", nil); rec.Code != http.StatusOK {
		t.Errorf("the session that did the revoking was signed out too: %d", rec.Code)
	}
	if rec := first.do(http.MethodGet, "/api/v1/nodes", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("the other session survived: %d", rec.Code)
	}
}

// A valid session identifier must not let anybody sign anybody else out.
func TestYouCannotEndSomebodyElsesSession(t *testing.T) {
	h := newHarness(t)
	victim := h.signedIn("victim")
	attacker := h.signedIn("attacker")

	target := victim.sessions()
	if len(target) == 0 {
		t.Fatal("the victim has no sessions")
	}

	rec := attacker.do(http.MethodDelete, "/api/v1/auth/sessions/"+target[0].ID, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 — a 403 would confirm the identifier is real: %s",
			rec.Code, rec.Body)
	}
	if rec := victim.do(http.MethodGet, "/api/v1/nodes", nil); rec.Code != http.StatusOK {
		t.Error("the victim was signed out by somebody else")
	}
}

// --- The audit trail ---------------------------------------------------------

func TestSigningInAndOutIsRecorded(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")
	const password = "a perfectly reasonable passphrase"
	h.createUser("josh", password, false)

	person := h.newClient()
	if rec := person.login("josh", password); rec.Code != http.StatusOK {
		t.Fatalf("sign in: %d", rec.Code)
	}
	if rec := person.do(http.MethodPost, "/api/v1/auth/logout", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("sign out: %d %s", rec.Code, rec.Body)
	}

	events := admin.auditEvents("")
	if !hasEvent(events, audit.ActionLogin, "josh") {
		t.Errorf("the sign-in was not recorded: %+v", events)
	}
	if !hasEvent(events, audit.ActionLogout, "josh") {
		t.Errorf("the sign-out was not recorded: %+v", events)
	}
}

// The failures are the events that matter during an incident. A trail holding
// only what worked would be missing exactly the interesting part.
func TestAFailedSignInIsRecordedWithTheNameButNotThePassword(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")
	h.createUser("josh", "a perfectly reasonable passphrase", false)

	person := h.newClient()
	if rec := person.login("josh", "the wrong passphrase entirely"); rec.Code == http.StatusOK {
		t.Fatal("a wrong password was accepted")
	}
	// Including one for an account that does not exist.
	if rec := person.login("nobody", "some passphrase or other"); rec.Code == http.StatusOK {
		t.Fatal("an unknown account signed in")
	}

	events := admin.auditEvents(audit.ActionLoginFailed)
	if len(events) < 2 {
		t.Fatalf("got %d failed sign-in events, want 2: %+v", len(events), events)
	}

	labels := map[string]bool{}
	for _, e := range events {
		labels[e.ActorLabel] = true
		if e.Outcome != string(audit.Failure) {
			t.Errorf("outcome = %q, want failure", e.Outcome)
		}
	}
	if !labels["josh"] || !labels["nobody"] {
		t.Errorf("the attempted usernames were not recorded: %v", labels)
	}

	// The rule that admits no exception.
	raw := admin.rawAudit()
	for _, secret := range []string{"the wrong passphrase entirely", "some passphrase or other", "argon2id"} {
		if strings.Contains(raw, secret) {
			t.Errorf("the audit trail contains %q", secret)
		}
	}
}

func TestSharingAndPublishingAreRecordedWithoutTheToken(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")
	owner := h.signedIn("owner")
	h.signedIn("colleague")

	folderID := owner.mkdir("root", "Team")
	colleague := h.mustUser("colleague")

	if rec := owner.do(http.MethodPost, "/api/v1/shares", map[string]any{
		"node_id":     folderID.String(),
		"user_id":     colleague.ID.String(),
		"permissions": []string{"viewer"},
	}); rec.Code != http.StatusCreated {
		t.Fatalf("share: %d %s", rec.Code, rec.Body)
	}

	rec := owner.do(http.MethodPost, "/api/v1/public-links", map[string]any{
		"node_id": folderID.String(),
		"kind":    "download",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	var created struct {
		Link struct {
			URL string `json:"url"`
		} `json:"link"`
	}
	decodeInto(t, rec, &created)
	token := created.Link.URL[strings.LastIndex(created.Link.URL, "/")+1:]
	if len(token) < 20 {
		t.Fatalf("could not extract a token from %q", created.Link.URL)
	}

	events := admin.auditEvents("")
	if !hasEvent(events, audit.ActionShareGrant, "owner") {
		t.Error("the share was not recorded")
	}
	if !hasEvent(events, audit.ActionLinkCreate, "owner") {
		t.Error("the public link was not recorded")
	}

	// The trail says a link was created. It must never say what the link is.
	if raw := admin.rawAudit(); strings.Contains(raw, token) {
		t.Error("the audit trail contains the public link's token")
	}
}

func TestDeletingContentIsRecordedWithItsNameSoTheRecordStillReads(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")
	person := h.signedIn("josh")

	id, _ := person.upload("root", "Important.txt", []byte("gone"))
	if rec := person.do(http.MethodDelete, "/api/v1/nodes/"+id.String(), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("trash: %d", rec.Code)
	}
	if rec := person.do(http.MethodDelete, "/api/v1/nodes/"+id.String()+"?permanent=true", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("purge: %d", rec.Code)
	}

	events := admin.auditEvents(audit.ActionNodePurge)
	if len(events) != 1 {
		t.Fatalf("got %d purge events, want 1", len(events))
	}
	// The name is what makes the record answerable later; the identifier alone
	// names something that no longer exists.
	if events[0].TargetLabel != "Important.txt" {
		t.Errorf("target label = %q, want the file's name", events[0].TargetLabel)
	}
}

// Deleting an account must not make its history anonymous. That is the whole
// reason the actor's name is stored alongside their identifier.
func TestTheTrailSurvivesTheDeletionOfTheAccountThatMadeIt(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")
	const password = "a perfectly reasonable passphrase"
	h.createUser("departing", password, false)

	person := h.newClient()
	if rec := person.login("departing", password); rec.Code != http.StatusOK {
		t.Fatalf("sign in: %d", rec.Code)
	}

	target := h.mustUser("departing")
	if rec := admin.do(http.MethodDelete, "/api/v1/admin/users/"+target.ID.String(),
		nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete account: %d %s", rec.Code, rec.Body)
	}

	events := admin.auditEvents("")
	found := false
	for _, e := range events {
		if e.Action == audit.ActionLogin && e.ActorLabel == "departing" {
			found = true
			if e.Actor != nil {
				t.Error("the deleted account still has a linked profile")
			}
		}
	}
	if !found {
		t.Error("the deleted account's sign-in is no longer in the trail")
	}
	// And the deletion itself is recorded, naming who did it.
	if !hasEvent(events, audit.ActionUserDelete, "boss") {
		t.Error("the account deletion was not recorded")
	}
}

func TestTheTrailCanBeFilteredByActionAndOutcome(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")
	h.createUser("josh", "a perfectly reasonable passphrase", false)

	person := h.newClient()
	person.login("josh", "the wrong passphrase entirely")
	person.login("josh", "a perfectly reasonable passphrase")

	failures := admin.auditEvents(audit.ActionLoginFailed)
	for _, e := range failures {
		if e.Action != audit.ActionLoginFailed {
			t.Errorf("the action filter returned %q", e.Action)
		}
	}

	rec := admin.do(http.MethodGet, "/api/v1/admin/audit?outcome=failure", nil)
	var body auditPage
	decodeInto(t, rec, &body)
	for _, e := range body.Events {
		if e.Outcome != string(audit.Failure) {
			t.Errorf("the outcome filter returned a %q event", e.Outcome)
		}
	}
	if len(body.Events) == 0 {
		t.Error("the outcome filter returned nothing despite a failed sign-in")
	}
}

// A retention of zero means "keep everything". Reading it as "delete
// everything" would destroy the record precisely when somebody needs it.
func TestAZeroAuditRetentionRefusesToPruneRatherThanDeletingEverything(t *testing.T) {
	h := newHarness(t)
	h.signedInAdmin("boss")

	if _, err := h.audit.DeleteOlderThan(t.Context(), 0); err == nil {
		t.Error("pruning accepted a zero retention period")
	}
	if _, err := h.audit.DeleteOlderThan(t.Context(), -time.Hour); err == nil {
		t.Error("pruning accepted a negative retention period")
	}

	// And a real window leaves recent events alone.
	removed, err := h.audit.DeleteOlderThan(t.Context(), 24*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 0 {
		t.Errorf("pruning with a day's retention removed %d of today's events", removed)
	}
}

// --- Overview and jobs -------------------------------------------------------

// The overview describes the instance. It must not become a window into what
// people are storing.
func TestTheOverviewDescribesTheInstanceAndNobodysContent(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")
	person := h.signedIn("josh")
	person.upload("root", "Confidential Salary Review.xlsx", []byte("secret"))

	rec := admin.do(http.MethodGet, "/api/v1/admin/overview", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("overview: %d %s", rec.Code, rec.Body)
	}

	body := rec.Body.String()
	if strings.Contains(body, "Confidential") || strings.Contains(body, "secret") {
		t.Error("the overview names somebody's file")
	}

	var parsed struct {
		Users struct {
			Total          int `json:"total"`
			Administrators int `json:"administrators"`
		} `json:"users"`
		Sessions struct {
			Live int `json:"live"`
		} `json:"sessions"`
	}
	decodeInto(t, rec, &parsed)
	if parsed.Users.Total != 2 {
		t.Errorf("users.total = %d, want 2", parsed.Users.Total)
	}
	if parsed.Users.Administrators != 1 {
		t.Errorf("users.administrators = %d, want 1", parsed.Users.Administrators)
	}
	if parsed.Sessions.Live < 2 {
		t.Errorf("sessions.live = %d, want at least 2", parsed.Sessions.Live)
	}
}

func TestAFailedJobIsVisibleAndCanBeRetried(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")

	runner := h.runnerWith("test.doomed", func(_ context.Context, _ *jobs.Job) error {
		return jobs.PermanentError(fmt.Errorf("the handler gave up"))
	})
	if _, err := h.jobs.Enqueue(t.Context(), jobs.EnqueueParams{
		Kind: "test.doomed", MaxAttempts: 1,
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := runner.Drain(t.Context(), 3); err != nil {
		t.Fatalf("drain: %v", err)
	}

	rec := admin.do(http.MethodGet, "/api/v1/admin/jobs", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("jobs: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Failed []struct {
			ID        uuid.UUID `json:"id"`
			Kind      string    `json:"kind"`
			LastError string    `json:"last_error"`
		} `json:"failed"`
	}
	decodeInto(t, rec, &body)

	if len(body.Failed) != 1 {
		t.Fatalf("got %d failed jobs, want 1", len(body.Failed))
	}
	// The error is what tells an administrator what to fix.
	if !strings.Contains(body.Failed[0].LastError, "gave up") {
		t.Errorf("last_error = %q, want the handler's message", body.Failed[0].LastError)
	}

	// Retrying puts it back with a full attempt budget.
	rec = admin.do(http.MethodPost,
		"/api/v1/admin/jobs/"+body.Failed[0].ID.String()+"/retry", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}

	after, err := h.jobs.Get(t.Context(), body.Failed[0].ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Status != jobs.StatusPending {
		t.Errorf("status after retry = %q, want pending", after.Status)
	}
	if after.Attempts != 0 {
		t.Errorf("attempts after retry = %d, want 0", after.Attempts)
	}
}

// A running job must not be retried: two workers executing it at once is the
// single thing the claim protocol exists to prevent.
func TestARunningJobCannotBeRetried(t *testing.T) {
	h := newHarness(t)
	admin := h.signedInAdmin("boss")

	job, err := h.jobs.Enqueue(t.Context(), jobs.EnqueueParams{Kind: "test.busy"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := h.jobs.Claim(t.Context()); err != nil {
		t.Fatalf("claim: %v", err)
	}

	rec := admin.do(http.MethodPost, "/api/v1/admin/jobs/"+job.ID.String()+"/retry", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a running job: %s", rec.Code, rec.Body)
	}
}

// --- Helpers -----------------------------------------------------------------

type auditRow struct {
	ID          uuid.UUID `json:"id"`
	Action      string    `json:"action"`
	Label       string    `json:"label"`
	Outcome     string    `json:"outcome"`
	ActorLabel  string    `json:"actor_label"`
	TargetLabel string    `json:"target_label"`
	Actor       *struct {
		Username string `json:"username"`
	} `json:"actor"`
}

type auditPage struct {
	Events []auditRow `json:"events"`
	Total  int        `json:"total"`
}

// auditEvents fetches the trail, optionally filtered to one action.
func (c *client) auditEvents(action string) []auditRow {
	c.t.Helper()
	target := "/api/v1/admin/audit?limit=200"
	if action != "" {
		target += "&action=" + urlEncode(action)
	}
	rec := c.do(http.MethodGet, target, nil)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("audit: %d %s", rec.Code, rec.Body)
	}
	var body auditPage
	decodeInto(c.t, rec, &body)
	return body.Events
}

// rawAudit returns the whole trail as text, for asserting that something is
// absent from it.
func (c *client) rawAudit() string {
	c.t.Helper()
	rec := c.do(http.MethodGet, "/api/v1/admin/audit?limit=500", nil)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("audit: %d %s", rec.Code, rec.Body)
	}
	return rec.Body.String()
}

func hasEvent(events []auditRow, action, actor string) bool {
	for _, e := range events {
		if e.Action == action && e.ActorLabel == actor {
			return true
		}
	}
	return false
}

type sessionRow struct {
	ID        string `json:"id"`
	Current   bool   `json:"current"`
	Device    string `json:"device"`
	UserAgent string `json:"user_agent"`
}

func (c *client) sessions() []sessionRow {
	c.t.Helper()
	rec := c.do(http.MethodGet, "/api/v1/auth/sessions", nil)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("sessions: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Sessions []sessionRow `json:"sessions"`
	}
	decodeInto(c.t, rec, &body)
	return body.Sessions
}

// signedInAdmin returns a client authenticated as a fresh administrator.
func (h *harness) signedInAdmin(username string) *client {
	h.t.Helper()
	const password = "a perfectly reasonable passphrase"
	h.createUser(username, password, true)

	c := h.newClient()
	if rec := c.login(username, password); rec.Code != http.StatusOK {
		h.t.Fatalf("sign in as administrator %q failed: %d %s", username, rec.Code, rec.Body)
	}
	return c
}

// A wrong password used to end the session the caller already had, so a
// mistyped retry from a stale tab signed them out of the working one. Session
// fixation is still prevented — a successful sign-in always issues a new
// identifier and kills the old session — but only a successful one.
func TestAFailedSignInDoesNotEndTheSessionYouAlreadyHave(t *testing.T) {
	h := newHarness(t)
	const password = "a perfectly reasonable passphrase"
	h.createUser("josh", password, false)

	c := h.newClient()
	if rec := c.login("josh", password); rec.Code != http.StatusOK {
		t.Fatalf("sign in: %d", rec.Code)
	}

	if rec := c.login("josh", "the wrong passphrase entirely"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the wrong password returned %d, want 401", rec.Code)
	}

	if rec := c.do(http.MethodGet, "/api/v1/nodes", nil); rec.Code != http.StatusOK {
		t.Errorf("a failed sign-in ended the working session: %d", rec.Code)
	}
}

// And a successful one still replaces it, which is what stops session fixation.
func TestASuccessfulSignInReplacesTheSessionYouAlreadyHad(t *testing.T) {
	h := newHarness(t)
	const password = "a perfectly reasonable passphrase"
	h.createUser("josh", password, false)

	first := h.newClient()
	if rec := first.login("josh", password); rec.Code != http.StatusOK {
		t.Fatalf("first sign in: %d", rec.Code)
	}
	before := first.sessions()
	if len(before) != 1 {
		t.Fatalf("got %d sessions after one sign-in, want 1", len(before))
	}

	// Sign in again from the same client, carrying the existing cookie.
	if rec := first.login("josh", password); rec.Code != http.StatusOK {
		t.Fatalf("second sign in: %d", rec.Code)
	}

	after := first.sessions()
	if len(after) != 1 {
		t.Errorf("got %d live sessions after signing in twice, want 1", len(after))
	}
	if len(after) == 1 && after[0].ID == before[0].ID {
		t.Error("the session identifier did not change on sign-in")
	}
}
