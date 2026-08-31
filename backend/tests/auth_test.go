package tests

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/api"
	"github.com/joshuanmoses/piCloud/backend/internal/archive"
	"github.com/joshuanmoses/piCloud/backend/internal/audit"
	"github.com/joshuanmoses/piCloud/backend/internal/auth"
	"github.com/joshuanmoses/piCloud/backend/internal/config"
	"github.com/joshuanmoses/piCloud/backend/internal/database"
	"github.com/joshuanmoses/piCloud/backend/internal/favorites"
	"github.com/joshuanmoses/piCloud/backend/internal/files"
	"github.com/joshuanmoses/piCloud/backend/internal/groups"
	"github.com/joshuanmoses/piCloud/backend/internal/jobs"
	"github.com/joshuanmoses/piCloud/backend/internal/logging"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
	"github.com/joshuanmoses/piCloud/backend/internal/previews"
	"github.com/joshuanmoses/piCloud/backend/internal/publiclinks"
	"github.com/joshuanmoses/piCloud/backend/internal/recent"
	"github.com/joshuanmoses/piCloud/backend/internal/search"
	"github.com/joshuanmoses/piCloud/backend/internal/sessions"
	"github.com/joshuanmoses/piCloud/backend/internal/sharing"
	"github.com/joshuanmoses/piCloud/backend/internal/storage"
	"github.com/joshuanmoses/piCloud/backend/internal/tags"
	"github.com/joshuanmoses/piCloud/backend/internal/users"
	"github.com/joshuanmoses/piCloud/backend/internal/versions"
	"github.com/joshuanmoses/piCloud/backend/migrations"
)

// harness wires a complete server against a real database, the way production
// does. Tests drive it over HTTP so middleware, cookies, and CSRF are all
// genuinely exercised rather than bypassed.
type harness struct {
	// testing.TB rather than *testing.T so the benchmarks can use the same
	// harness. Everything it calls — Helper, Fatalf, Cleanup, TempDir,
	// Context, Name — is on the interface.
	t           testing.TB
	db          *database.DB
	server      *api.Server
	users       *users.Store
	sessions    *sessions.Store
	nodes       *nodes.Store
	files       *files.Service
	publicLinks *publiclinks.Service
	groups      *groups.Store
	permissions *permissions.Store
	sharing     *sharing.Service
	auth        *auth.Service
	hasher      *auth.Hasher
	cfg         *config.Config
	storageRoot string

	// Phase 7.
	favorites *favorites.Store
	recent    *recent.Store
	tags      *tags.Store
	previews  *previews.Service
	jobs      *jobs.Store
	storage   *storage.Storage

	// Phase 8.
	versions *versions.Service

	// Phase 9.
	audit *audit.Store
}

// fastHashParams keep sign-in cheap in tests. Production uses DefaultParams.
var fastHashParams = auth.HashParams{
	Memory: 8 * 1024, Time: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
}

// newHarness builds a harness with settings suited to a test suite: rate
// limits generous enough that an ordinary test is never throttled, exactly as
// the sign-in limiter has been since Phase 2. A test that wants to exercise a
// limit builds its own harness with a tight one.
func newHarness(t testing.TB) *harness {
	t.Helper()
	return newHarnessWithEnv(t, nil)
}

// newHarnessWithEnv builds a harness with extra configuration.
func newHarnessWithEnv(t testing.TB, extra map[string]string) *harness {
	t.Helper()
	db := freshDatabase(t)

	if _, err := db.Migrate(t.Context(), migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	store, err := storage.Open(config.Storage{
		Root: t.TempDir(), DirMode: 0o750, FileMode: 0o640, MinFreeBytes: 1024,
	}, logging.Discard())
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	env := map[string]string{
		"APP_ENV":             "development",
		"APP_URL":             "http://localhost:8080",
		"STORAGE_ROOT":        store.Root(),
		"APP_SECRET_KEY":      "test-secret-key-0123456789abcdef0123456789abcdef",
		"MAX_LOGIN_ATTEMPTS":  "5",
		"LOCKOUT_DURATION":    "15m",
		"PASSWORD_MIN_LENGTH": "12",

		// Generous by default so an ordinary test is never throttled. The
		// rate-limit tests set their own.
		"RATE_LIMIT_READ":      "100000",
		"RATE_LIMIT_WRITE":     "100000",
		"RATE_LIMIT_UPLOAD":    "100000",
		"RATE_LIMIT_SEARCH":    "100000",
		"RATE_LIMIT_ANONYMOUS": "100000",
	}
	for key, value := range extra {
		env[key] = value
	}

	cfg, err := config.LoadFrom(config.MapLookup(env))
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	userStore := users.NewStore(db)
	sessionStore := sessions.NewStore(db, sessions.Config{
		Lifetime:         cfg.Session.Lifetime,
		RememberLifetime: cfg.Session.RememberLifetime,
		MaxPerUser:       cfg.Session.MaxPerUser,
	})
	hasher, err := auth.NewHasher(fastHashParams)
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.NewService(userStore, sessionStore, hasher, auth.Config{
		MaxLoginAttempts: cfg.Security.MaxLoginAttempts,
		LockoutDuration:  cfg.Security.LockoutDuration,
		PasswordPolicy:   auth.NewPasswordPolicy(cfg.Security.PasswordMinLength),
		// Generous so ordinary tests are not throttled; the rate-limit test
		// builds its own service.
		PerIPLimit:  10_000,
		PerIPWindow: time.Minute,
	}, logging.Discard())

	nodeStore := nodes.NewStore(db)
	uploadStore := files.NewUploadStore(db, store, time.Hour, logging.Discard())
	fileService := files.NewService(nodeStore, userStore, uploadStore, store, files.Config{
		MaxUploadSize: cfg.Uploads.MaxSize,
		ChunkSize:     cfg.Uploads.ChunkSize,
		UploadTTL:     time.Hour,
	}, logging.Discard())

	groupStore := groups.NewStore(db)
	permissionStore := permissions.NewStore(db)
	sharingService := sharing.NewService(
		db, permissionStore, nodeStore, userStore, groupStore, true, logging.Discard())

	publicLinkService := publiclinks.NewService(
		publiclinks.NewStore(db), nodeStore, permissionStore, hasher,
		cfg.Security.SecretKey.Reveal(),
		publiclinks.Config{Enabled: true, UploadRequestsEnabled: true}, logging.Discard())

	favoriteStore := favorites.NewStore(db)
	recentStore := recent.NewStore(db)
	tagStore := tags.NewStore(db)
	jobStore := jobs.NewStore(db)
	previewService := previews.NewService(previews.NewStore(db), nodeStore, store, jobStore, logging.Discard())

	// Version history, wired the way production wires it: the node store calls
	// back into the service when content is replaced, so the snapshot and the
	// replacement share one transaction.
	versionService := versions.NewService(
		versions.NewStore(db), nodeStore, store,
		versions.Config{
			Enabled: true,
			Retention: versions.Retention{
				MaxPerFile: cfg.Versions.MaxPerFile,
				MaxAge:     cfg.Versions.MaxAge,
			},
		}, logging.Discard())
	nodeStore.SetVersioner(versionService)

	auditStore := audit.NewStore(db, logging.Discard())

	server, err := api.NewServer(api.Deps{
		Config: cfg, DB: db, Storage: store, Logger: logging.Discard(),
		Users: userStore, Sessions: sessionStore, Auth: authService,
		Files: fileService, Sharing: sharingService, Groups: groupStore,
		PublicLinks: publicLinkService,

		Search:      search.NewPostgres(db),
		Favorites:   favoriteStore,
		Recent:      recentStore,
		Tags:        tagStore,
		Previews:    previewService,
		Archives:    archive.NewBuilder(nodeStore, permissionStore, store, logging.Discard()),
		Jobs:        jobStore,
		BlobStorage: store,
		Versions:    versionService,
		Audit:       auditStore,
	})
	if err != nil {
		t.Fatalf("api.NewServer: %v", err)
	}
	t.Cleanup(server.Close)

	return &harness{
		t: t, db: db, server: server, users: userStore,
		sessions: sessionStore, nodes: nodeStore, files: fileService,
		groups: groupStore, permissions: permissionStore, sharing: sharingService,
		publicLinks: publicLinkService,
		auth:        authService, hasher: hasher, cfg: cfg, storageRoot: store.Root(),

		favorites: favoriteStore, recent: recentStore, tags: tagStore,
		previews: previewService, jobs: jobStore, storage: store,
		versions: versionService, audit: auditStore,
	}
}

// usersUpdateQuota builds an UpdateParams that sets only the quota. The double
// pointer distinguishes "leave unchanged" from "set to unlimited".
func usersUpdateQuota(quota **int64) users.UpdateParams {
	return users.UpdateParams{QuotaBytes: quota}
}

// createUser inserts an account directly, bypassing the API.
func (h *harness) createUser(username, password string, admin bool) *users.User {
	h.t.Helper()
	hash, err := h.hasher.Hash(password)
	if err != nil {
		h.t.Fatal(err)
	}
	u, err := h.users.Create(h.t.Context(), users.CreateParams{
		Username:     username,
		DisplayName:  username,
		PasswordHash: hash,
		IsAdmin:      admin,
		IsEnabled:    true,
	})
	if err != nil {
		h.t.Fatalf("create user %q: %v", username, err)
	}
	return u
}

// client is a browser-like HTTP client that keeps cookies and echoes the CSRF
// token, exactly as the real frontend does.
type client struct {
	t       testing.TB
	server  *api.Server
	cookies map[string]string
	csrf    string
}

func (h *harness) newClient() *client {
	return &client{t: h.t, server: h.server, cookies: map[string]string{}}
}

func (c *client) do(method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()

	var reader *strings.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = strings.NewReader(string(encoded))
	} else {
		reader = strings.NewReader("")
	}

	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Loopback is on the trusted-proxy list, so this exercises the real
	// client-IP path.
	req.RemoteAddr = "127.0.0.1:40000"
	for name, value := range c.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	if c.csrf != "" {
		req.Header.Set(auth.CSRFHeaderName, c.csrf)
	}

	rec := httptest.NewRecorder()
	c.server.ServeHTTP(rec, req)

	// Track Set-Cookie the way a browser would, including deletions.
	for _, cookie := range rec.Result().Cookies() {
		if cookie.MaxAge < 0 {
			delete(c.cookies, cookie.Name)
			if cookie.Name == auth.CSRFCookieName {
				c.csrf = ""
			}
			continue
		}
		c.cookies[cookie.Name] = cookie.Value
		if cookie.Name == auth.CSRFCookieName {
			c.csrf = cookie.Value
		}
	}
	return rec
}

func (c *client) login(username, password string) *httptest.ResponseRecorder {
	return c.do(http.MethodPost, "/api/v1/auth/login",
		map[string]any{"username": username, "password": password})
}

func (c *client) sessionCookie() string { return c.cookies["pifiles_session"] }

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response was not valid JSON (%s): %v", rec.Body.String(), err)
	}
	return out
}

// --- Login ------------------------------------------------------------------

func TestLoginSucceedsAndSetsCookies(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	c := h.newClient()
	rec := c.login("josh", "correct horse battery staple")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}

	sessionCookie := findCookie(rec, "pifiles_session")
	if sessionCookie == nil {
		t.Fatal("no session cookie was set")
	}
	if !sessionCookie.HttpOnly {
		t.Error("the session cookie must be HttpOnly so scripts cannot read the credential")
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", sessionCookie.SameSite)
	}
	if sessionCookie.Path != "/" {
		t.Errorf("cookie path = %q, want /", sessionCookie.Path)
	}
	// Without "remember", the cookie must expire when the browser closes.
	if !sessionCookie.Expires.IsZero() || sessionCookie.MaxAge > 0 {
		t.Error("a non-remembered session should be a browser-session cookie")
	}

	csrfCookie := findCookie(rec, auth.CSRFCookieName)
	if csrfCookie == nil {
		t.Fatal("no CSRF cookie was set")
	}
	if csrfCookie.HttpOnly {
		t.Error("the CSRF cookie must be readable by the frontend, so it cannot be HttpOnly")
	}

	// The raw session token must never appear in the response body.
	if strings.Contains(rec.Body.String(), sessionCookie.Value) {
		t.Error("the session token leaked into the response body")
	}
}

func TestLoginResponseNeverIncludesPasswordHash(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	rec := h.newClient().login("josh", "correct horse battery staple")
	body := rec.Body.String()
	for _, needle := range []string{"argon2id", "password_hash", "PasswordHash", "$v=19$"} {
		if strings.Contains(body, needle) {
			t.Errorf("login response leaked %q: %s", needle, body)
		}
	}
}

// An unknown username and a wrong password must be indistinguishable, or the
// endpoint becomes an account-enumeration oracle.
func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	wrongPassword := h.newClient().login("josh", "definitely not the password")
	unknownUser := h.newClient().login("nosuchperson", "definitely not the password")

	if wrongPassword.Code != unknownUser.Code {
		t.Errorf("status codes differ: %d vs %d", wrongPassword.Code, unknownUser.Code)
	}
	if wrongPassword.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", wrongPassword.Code)
	}

	a := decodeBody[api.ErrorResponse](t, wrongPassword)
	b := decodeBody[api.ErrorResponse](t, unknownUser)
	if a.Error.Code != b.Error.Code || a.Error.Message != b.Error.Message {
		t.Errorf("responses differ and can be used to enumerate accounts:\n  wrong password: %+v\n  unknown user:   %+v",
			a.Error, b.Error)
	}
	// The message must not name the field that was wrong.
	if strings.Contains(strings.ToLower(a.Error.Message), "user not found") {
		t.Errorf("message reveals which half failed: %q", a.Error.Message)
	}
}

func TestLoginRejectsDisabledAccount(t *testing.T) {
	h := newHarness(t)
	user := h.createUser("josh", "correct horse battery staple", false)

	disabled := false
	if _, err := h.users.Update(t.Context(), user.ID,
		users.UpdateParams{IsEnabled: &disabled}); err != nil {
		t.Fatal(err)
	}

	rec := h.newClient().login("josh", "correct horse battery staple")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if body := decodeBody[api.ErrorResponse](t, rec); body.Error.Code != "account_disabled" {
		t.Errorf("code = %q, want account_disabled", body.Error.Code)
	}
}

// A disabled account must not be detectable without the correct password,
// otherwise the endpoint reveals which accounts exist and are suspended.
func TestDisabledAccountNotRevealedByWrongPassword(t *testing.T) {
	h := newHarness(t)
	user := h.createUser("josh", "correct horse battery staple", false)

	disabled := false
	if _, err := h.users.Update(t.Context(), user.ID,
		users.UpdateParams{IsEnabled: &disabled}); err != nil {
		t.Fatal(err)
	}

	rec := h.newClient().login("josh", "the wrong password entirely")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — a wrong password must not reveal that the account is disabled", rec.Code)
	}
}

func TestLoginIsCaseInsensitiveForUsername(t *testing.T) {
	h := newHarness(t)
	h.createUser("Josh", "correct horse battery staple", false)

	for _, spelling := range []string{"josh", "JOSH", "JoSh", "  josh  "} {
		rec := h.newClient().login(spelling, "correct horse battery staple")
		if rec.Code != http.StatusOK {
			t.Errorf("login as %q failed with %d", spelling, rec.Code)
		}
	}
}

func TestRememberMeSetsPersistentCookie(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	c := h.newClient()
	rec := c.do(http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "josh", "password": "correct horse battery staple", "remember": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	cookie := findCookie(rec, "pifiles_session")
	if cookie == nil || cookie.MaxAge <= 0 {
		t.Fatal("remember-me should produce a persistent cookie with a Max-Age")
	}
	// It must outlive the ordinary session lifetime.
	if time.Duration(cookie.MaxAge)*time.Second <= h.cfg.Session.Lifetime {
		t.Errorf("remembered cookie lifetime %ds is not longer than the base session lifetime", cookie.MaxAge)
	}
}

// --- Lockout ----------------------------------------------------------------

func TestAccountLocksAfterRepeatedFailures(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	// MAX_LOGIN_ATTEMPTS is 5 in the harness.
	for i := 1; i <= 5; i++ {
		rec := h.newClient().login("josh", "wrong password here")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i, rec.Code)
		}
	}

	// The account is now locked; even the correct password is refused.
	rec := h.newClient().login("josh", "correct horse battery staple")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 once locked; body: %s", rec.Code, rec.Body)
	}
	if body := decodeBody[api.ErrorResponse](t, rec); body.Error.Code != "account_locked" {
		t.Errorf("code = %q, want account_locked", body.Error.Code)
	}
}

// Locking an account must end its existing sessions: repeated failures may mean
// the account is under attack, and a session opened earlier could be the
// attacker's.
func TestLockoutRevokesExistingSessions(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	victim := h.newClient()
	if rec := victim.login("josh", "correct horse battery staple"); rec.Code != http.StatusOK {
		t.Fatalf("setup login failed: %d", rec.Code)
	}
	if rec := victim.do(http.MethodGet, "/api/v1/auth/session", nil); rec.Code != http.StatusOK {
		t.Fatalf("session should be live: %d", rec.Code)
	}

	for range 5 {
		h.newClient().login("josh", "wrong password here")
	}

	if rec := victim.do(http.MethodGet, "/api/v1/auth/session", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("the pre-existing session survived lockout: status = %d", rec.Code)
	}
}

func TestSuccessfulLoginClearsFailureCounter(t *testing.T) {
	h := newHarness(t)
	user := h.createUser("josh", "correct horse battery staple", false)

	for range 3 {
		h.newClient().login("josh", "wrong password here")
	}

	if rec := h.newClient().login("josh", "correct horse battery staple"); rec.Code != http.StatusOK {
		t.Fatalf("login should succeed below the threshold: %d", rec.Code)
	}

	reloaded, err := h.users.GetByID(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.FailedLoginCount != 0 {
		t.Errorf("failed count = %d after a successful login, want 0", reloaded.FailedLoginCount)
	}
	if reloaded.LastLoginAt == nil {
		t.Error("last_login_at should be stamped")
	}
}

// --- Sessions ---------------------------------------------------------------

func TestSessionEndpointRequiresAuthentication(t *testing.T) {
	h := newHarness(t)

	rec := h.newClient().do(http.MethodGet, "/api/v1/auth/session", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestSessionEndpointReturnsCurrentUser(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	c := h.newClient()
	c.login("josh", "correct horse battery staple")

	rec := c.do(http.MethodGet, "/api/v1/auth/session", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	body := decodeBody[struct {
		User struct {
			Username string `json:"username"`
			IsAdmin  bool   `json:"is_admin"`
		} `json:"user"`
		CSRFToken string `json:"csrf_token"`
	}](t, rec)

	if body.User.Username != "josh" {
		t.Errorf("username = %q", body.User.Username)
	}
	if body.User.IsAdmin {
		t.Error("this account is not an administrator")
	}
	if body.CSRFToken == "" {
		t.Error("the session response must carry a CSRF token")
	}
}

func TestLogoutRevokesTheSession(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	c := h.newClient()
	c.login("josh", "correct horse battery staple")
	token := c.sessionCookie()

	rec := c.do(http.MethodPost, "/api/v1/auth/logout", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body)
	}

	// The token must be dead server-side, not merely cleared from the browser.
	if _, err := h.sessions.Validate(t.Context(), token); !errors.Is(err, sessions.ErrNotFound) {
		t.Errorf("the session token still validates after logout: %v", err)
	}

	// And the cookies must be cleared.
	if c.sessionCookie() != "" {
		t.Error("the session cookie was not cleared")
	}
}

// A stolen session token must stop working the moment its session is revoked.
// This is the property a stateless JWT could not provide.
func TestRevokedSessionIsRejectedImmediately(t *testing.T) {
	h := newHarness(t)
	user := h.createUser("josh", "correct horse battery staple", false)

	c := h.newClient()
	c.login("josh", "correct horse battery staple")

	if _, err := h.sessions.RevokeAllForUser(t.Context(), user.ID, nil, "test"); err != nil {
		t.Fatal(err)
	}

	rec := c.do(http.MethodGet, "/api/v1/auth/session", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a revoked session was still accepted: status = %d", rec.Code)
	}
}

// Disabling an account must take effect on the next request, not when the
// session eventually expires.
func TestDisablingAccountInvalidatesLiveSession(t *testing.T) {
	h := newHarness(t)
	user := h.createUser("josh", "correct horse battery staple", false)

	c := h.newClient()
	c.login("josh", "correct horse battery staple")

	disabled := false
	if _, err := h.users.Update(t.Context(), user.ID,
		users.UpdateParams{IsEnabled: &disabled}); err != nil {
		t.Fatal(err)
	}

	rec := c.do(http.MethodGet, "/api/v1/auth/session", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a disabled account kept its live session: status = %d", rec.Code)
	}
}

// Signing in again must issue a new session identifier, so a fixed token
// planted before sign-in cannot be elevated afterwards.
func TestSessionFixationIsPrevented(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	c := h.newClient()
	c.login("josh", "correct horse battery staple")
	first := c.sessionCookie()

	c.login("josh", "correct horse battery staple")
	second := c.sessionCookie()

	if first == second {
		t.Fatal("the session token did not change on a fresh sign-in")
	}
	// The previous session must also be dead.
	if _, err := h.sessions.Validate(t.Context(), first); !errors.Is(err, sessions.ErrNotFound) {
		t.Error("the prior session survived a new sign-in")
	}
}

func TestSessionTokensAreUnpredictable(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	seen := map[string]bool{}
	for range 20 {
		c := h.newClient()
		c.login("josh", "correct horse battery staple")
		token := c.sessionCookie()

		if len(token) < 40 {
			t.Fatalf("session token is only %d characters; too little entropy", len(token))
		}
		if seen[token] {
			t.Fatal("a session token was reused")
		}
		seen[token] = true
	}
}

// Only a hash of the token is stored, so a leaked database backup does not
// yield usable credentials.
func TestSessionTokenIsNotStoredInPlaintext(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	c := h.newClient()
	c.login("josh", "correct horse battery staple")
	token := c.sessionCookie()

	var matches int
	err := h.db.Pool().QueryRow(t.Context(),
		`SELECT count(*) FROM sessions WHERE encode(token_hash, 'escape') LIKE '%' || $1 || '%'`,
		token).Scan(&matches)
	if err != nil {
		t.Fatal(err)
	}
	if matches != 0 {
		t.Fatal("the raw session token appears in the sessions table")
	}
}

// --- CSRF -------------------------------------------------------------------

// A state-changing request without the token header must be refused, even with
// a perfectly valid session cookie. This is the cross-site forgery case.
func TestStateChangingRequestRequiresCSRFToken(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	c := h.newClient()
	c.login("josh", "correct horse battery staple")

	// Simulate a cross-site POST: the browser sends the session cookie, but the
	// attacker cannot read the CSRF cookie to populate the header.
	c.csrf = ""
	rec := c.do(http.MethodPost, "/api/v1/auth/password", map[string]any{
		"current_password": "correct horse battery staple",
		"new_password":     "an entirely new passphrase",
	})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 without a CSRF token; body: %s", rec.Code, rec.Body)
	}
	if body := decodeBody[api.ErrorResponse](t, rec); body.Error.Code != "csrf_failed" {
		t.Errorf("code = %q, want csrf_failed", body.Error.Code)
	}
}

func TestWrongCSRFTokenIsRejected(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	c := h.newClient()
	c.login("josh", "correct horse battery staple")
	c.csrf = "a-forged-token-value"

	rec := c.do(http.MethodPost, "/api/v1/auth/logout", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a forged token", rec.Code)
	}
}

// One user's CSRF token must not work for another user's session.
func TestCSRFTokenFromAnotherSessionIsRejected(t *testing.T) {
	h := newHarness(t)
	h.createUser("alice", "alice correct horse staple", false)
	h.createUser("mallory", "mallory correct horse staple", false)

	alice := h.newClient()
	alice.login("alice", "alice correct horse staple")

	mallory := h.newClient()
	mallory.login("mallory", "mallory correct horse staple")

	// Alice's session cookie with Mallory's CSRF token.
	alice.csrf = mallory.csrf
	rec := alice.do(http.MethodPost, "/api/v1/auth/logout", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a CSRF token from another session was accepted: status = %d", rec.Code)
	}
}

// Safe methods must not require a token, or every page load would break.
func TestReadRequestsDoNotRequireCSRF(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	c := h.newClient()
	c.login("josh", "correct horse battery staple")
	c.csrf = ""

	if rec := c.do(http.MethodGet, "/api/v1/auth/session", nil); rec.Code != http.StatusOK {
		t.Errorf("GET should not require a CSRF token: status = %d", rec.Code)
	}
}

// --- Password change --------------------------------------------------------

func TestChangePasswordRequiresCurrentPassword(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	c := h.newClient()
	c.login("josh", "correct horse battery staple")

	rec := c.do(http.MethodPost, "/api/v1/auth/password", map[string]any{
		"current_password": "not the current password",
		"new_password":     "an entirely new passphrase",
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", rec.Code, rec.Body)
	}

	// The old password must still work.
	if rec := h.newClient().login("josh", "correct horse battery staple"); rec.Code != http.StatusOK {
		t.Error("the password was changed despite the wrong current password")
	}
}

func TestChangePasswordSucceedsAndRevokesOtherSessions(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	// Two devices signed in.
	phone := h.newClient()
	phone.login("josh", "correct horse battery staple")
	laptop := h.newClient()
	laptop.login("josh", "correct horse battery staple")

	rec := laptop.do(http.MethodPost, "/api/v1/auth/password", map[string]any{
		"current_password": "correct horse battery staple",
		"new_password":     "an entirely different passphrase",
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body)
	}

	// The device that made the change stays signed in.
	if rec := laptop.do(http.MethodGet, "/api/v1/auth/session", nil); rec.Code != http.StatusOK {
		t.Errorf("the session that changed the password should survive: %d", rec.Code)
	}
	// Every other device is signed out — the whole point of changing a password
	// you think someone else knows.
	if rec := phone.do(http.MethodGet, "/api/v1/auth/session", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("another device kept its session after a password change: %d", rec.Code)
	}

	// The new password works and the old one does not.
	if rec := h.newClient().login("josh", "an entirely different passphrase"); rec.Code != http.StatusOK {
		t.Error("the new password does not work")
	}
	if rec := h.newClient().login("josh", "correct horse battery staple"); rec.Code == http.StatusOK {
		t.Error("the old password still works")
	}
}

func TestChangePasswordEnforcesPolicy(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	c := h.newClient()
	c.login("josh", "correct horse battery staple")

	for _, weak := range []string{"short", "password123", "joshjoshjoshjosh"} {
		rec := c.do(http.MethodPost, "/api/v1/auth/password", map[string]any{
			"current_password": "correct horse battery staple",
			"new_password":     weak,
		})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("weak password %q was accepted: status = %d", weak, rec.Code)
			continue
		}
		body := decodeBody[api.ErrorResponse](t, rec)
		if body.Error.Fields["new_password"] == "" {
			t.Errorf("the rejection should explain the problem on the new_password field: %+v", body.Error)
		}
	}
}

func TestChangePasswordRejectsReuse(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	c := h.newClient()
	c.login("josh", "correct horse battery staple")

	rec := c.do(http.MethodPost, "/api/v1/auth/password", map[string]any{
		"current_password": "correct horse battery staple",
		"new_password":     "correct horse battery staple",
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("reusing the current password should be refused: status = %d", rec.Code)
	}
}

// --- Session listing --------------------------------------------------------

func TestUserCanListAndRevokeTheirSessions(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	phone := h.newClient()
	phone.login("josh", "correct horse battery staple")
	laptop := h.newClient()
	laptop.login("josh", "correct horse battery staple")

	rec := laptop.do(http.MethodGet, "/api/v1/auth/sessions", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	body := decodeBody[struct {
		Sessions []struct {
			ID      uuid.UUID `json:"id"`
			Current bool      `json:"current"`
		} `json:"sessions"`
	}](t, rec)

	if len(body.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(body.Sessions))
	}

	var currentCount int
	var otherID uuid.UUID
	for _, s := range body.Sessions {
		if s.Current {
			currentCount++
		} else {
			otherID = s.ID
		}
	}
	if currentCount != 1 {
		t.Errorf("exactly one session should be marked current, got %d", currentCount)
	}

	// Revoking the phone from the laptop must sign the phone out.
	rec = laptop.do(http.MethodDelete, "/api/v1/auth/sessions/"+otherID.String(), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d; body: %s", rec.Code, rec.Body)
	}
	if rec := phone.do(http.MethodGet, "/api/v1/auth/session", nil); rec.Code != http.StatusUnauthorized {
		t.Error("the revoked device kept its session")
	}
}

// A user must not be able to revoke another user's session by guessing its
// identifier. This is the core IDOR test for this endpoint.
func TestUserCannotRevokeAnotherUsersSession(t *testing.T) {
	h := newHarness(t)
	h.createUser("alice", "alice correct horse staple", false)
	h.createUser("mallory", "mallory correct horse staple", false)

	alice := h.newClient()
	alice.login("alice", "alice correct horse staple")

	aliceUser, err := h.users.GetByUsername(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	aliceSessions, err := h.sessions.ListForUser(t.Context(), aliceUser.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliceSessions) != 1 {
		t.Fatalf("expected 1 session for alice, got %d", len(aliceSessions))
	}

	mallory := h.newClient()
	mallory.login("mallory", "mallory correct horse staple")

	rec := mallory.do(http.MethodDelete,
		"/api/v1/auth/sessions/"+aliceSessions[0].ID.String(), nil)

	// A uniform 404 rather than 403: confirming the session exists would tell
	// Mallory she guessed a real identifier.
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; Mallory must not be able to touch Alice's session", rec.Code)
	}

	// And Alice must still be signed in.
	if rec := alice.do(http.MethodGet, "/api/v1/auth/session", nil); rec.Code != http.StatusOK {
		t.Error("Alice's session was revoked by another user")
	}
}

// --- Setup ------------------------------------------------------------------

func TestSetupCreatesFirstAdministrator(t *testing.T) {
	h := newHarness(t)

	c := h.newClient()
	rec := c.do(http.MethodGet, "/api/v1/setup", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body := decodeBody[struct {
		SetupRequired bool `json:"setup_required"`
	}](t, rec); !body.SetupRequired {
		t.Fatal("setup should be required on an empty instance")
	}

	rec = c.do(http.MethodPost, "/api/v1/setup", map[string]any{
		"username":     "josh",
		"display_name": "Josh Moses",
		"password":     "the first administrator passphrase",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body)
	}

	// The new administrator is signed in immediately.
	if c.sessionCookie() == "" {
		t.Error("setup should sign the new administrator in")
	}
	rec = c.do(http.MethodGet, "/api/v1/auth/session", nil)
	body := decodeBody[struct {
		User struct {
			Username string `json:"username"`
			IsAdmin  bool   `json:"is_admin"`
		} `json:"user"`
	}](t, rec)
	if !body.User.IsAdmin {
		t.Error("the first account must be an administrator")
	}
}

// Once an account exists, setup must be permanently closed. Otherwise anyone
// reaching the server could grant themselves administrator access.
func TestSetupIsClosedOnceAnAccountExists(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", true)

	c := h.newClient()

	rec := c.do(http.MethodGet, "/api/v1/setup", nil)
	if body := decodeBody[struct {
		SetupRequired bool `json:"setup_required"`
	}](t, rec); body.SetupRequired {
		t.Error("setup should not be required once an account exists")
	}

	rec = c.do(http.MethodPost, "/api/v1/setup", map[string]any{
		"username": "mallory",
		"password": "mallory would like to be an admin",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — setup must be closed permanently", rec.Code)
	}

	// And no account was created.
	if _, err := h.users.GetByUsername(t.Context(), "mallory"); !errors.Is(err, users.ErrNotFound) {
		t.Fatal("setup created an account after it should have been closed")
	}
}

func TestSetupEnforcesPasswordPolicy(t *testing.T) {
	h := newHarness(t)

	rec := h.newClient().do(http.MethodPost, "/api/v1/setup", map[string]any{
		"username": "josh",
		"password": "admin",
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 for a weak password", rec.Code)
	}

	count, err := h.users.Count(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("a rejected setup must not create an account")
	}
}

// --- Rate limiting ----------------------------------------------------------

func TestLoginIsRateLimitedPerAddress(t *testing.T) {
	h := newHarness(t)
	h.createUser("josh", "correct horse battery staple", false)

	// A dedicated service with a tight limit; the shared harness deliberately
	// uses a generous one so other tests are not throttled.
	limited := auth.NewService(h.users, h.sessions, h.hasher, auth.Config{
		MaxLoginAttempts: 100, // high, so lockout does not fire first
		LockoutDuration:  time.Minute,
		PasswordPolicy:   auth.NewPasswordPolicy(12),
		PerIPLimit:       3,
		PerIPWindow:      time.Minute,
	}, logging.Discard())

	var lastErr error
	for i := range 5 {
		_, lastErr = limited.Login(context.Background(), auth.LoginParams{
			Username:  "josh",
			Password:  "wrong password attempt",
			IPAddress: "198.51.100.10",
		})
		if i < 3 && errors.Is(lastErr, auth.ErrRateLimited) {
			t.Fatalf("attempt %d was rate limited too early", i+1)
		}
	}

	if !errors.Is(lastErr, auth.ErrRateLimited) {
		t.Fatalf("expected rate limiting after the budget was exhausted, got %v", lastErr)
	}

	// A different address must have its own budget.
	_, err := limited.Login(context.Background(), auth.LoginParams{
		Username:  "josh",
		Password:  "correct horse battery staple",
		IPAddress: "198.51.100.99",
	})
	if errors.Is(err, auth.ErrRateLimited) {
		t.Error("one address exhausting its budget must not throttle another")
	}
}

// --- Helpers ----------------------------------------------------------------

func findCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}
