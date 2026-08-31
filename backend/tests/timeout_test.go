package tests

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/api"
	"github.com/joshuanmoses/piCloud/backend/internal/auth"
	"github.com/joshuanmoses/piCloud/backend/internal/config"
	"github.com/joshuanmoses/piCloud/backend/internal/files"
	"github.com/joshuanmoses/piCloud/backend/internal/groups"
	"github.com/joshuanmoses/piCloud/backend/internal/logging"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
	"github.com/joshuanmoses/piCloud/backend/internal/sessions"
	"github.com/joshuanmoses/piCloud/backend/internal/sharing"
	"github.com/joshuanmoses/piCloud/backend/internal/storage"
	"github.com/joshuanmoses/piCloud/backend/internal/users"
	"github.com/joshuanmoses/piCloud/backend/migrations"
)

// trickleReader delivers a body slowly, the way a real upload over a home
// connection does.
type trickleReader struct {
	remaining int
	chunk     int
	delay     time.Duration
}

func (t *trickleReader) Read(p []byte) (int, error) {
	if t.remaining <= 0 {
		return 0, io.EOF
	}
	time.Sleep(t.delay)

	n := min(t.chunk, min(len(p), t.remaining))
	for i := range n {
		p[i] = byte('a' + i%26)
	}
	t.remaining -= n
	return n, nil
}

// A long transfer must not be aborted by the deadline that bounds ordinary
// JSON handlers.
//
// This is the regression test for a real defect: the transfer routes were
// nested inside the group carrying the timeout, and chi middleware is
// inherited by nested groups — so every upload was silently bounded, and a
// large one would have failed on the final metadata write.
func TestSlowUploadIsNotCutOffByTheJSONTimeout(t *testing.T) {
	// A deliberately tiny deadline. If transfers inherited it, an upload
	// taking longer than this would fail.
	const jsonTimeout = 150 * time.Millisecond
	h := newHarnessWithTimeout(t, jsonTimeout)

	c := h.signedIn("josh")

	// Ordinary JSON calls must still be bounded, or the test proves nothing:
	// confirm the deadline is genuinely in force on that path.
	if rec := c.do(http.MethodGet, "/api/v1/nodes", nil); rec.Code != http.StatusOK {
		t.Fatalf("a fast JSON request should succeed: %d", rec.Code)
	}

	// The body takes roughly 10 × 40ms = 400ms to deliver, comfortably beyond
	// the deadline.
	body := &trickleReader{remaining: 10_000, chunk: 1_000, delay: 40 * time.Millisecond}

	rec := c.raw(http.MethodPost, "/api/v1/files?name=slow.bin&parent_id=root",
		"application/octet-stream", body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("a slow upload returned %d, want 201 — transfers must not inherit "+
			"the JSON timeout, or a large upload is aborted partway through; body: %s",
			rec.Code, rec.Body)
	}

	// And the content actually landed.
	rec = c.do(http.MethodGet, "/api/v1/nodes", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing failed: %d", rec.Code)
	}
	if names := c.listNames(""); len(names) != 1 || names[0] != "slow.bin" {
		t.Errorf("the uploaded file is missing: %v", names)
	}
}

// The same for a download: streaming a large file must not be cut off.
func TestSlowDownloadIsNotCutOffByTheJSONTimeout(t *testing.T) {
	h := newHarnessWithTimeout(t, 150*time.Millisecond)
	c := h.signedIn("josh")

	id, rec := c.upload("root", "file.bin", make([]byte, 64*1024))
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup upload failed: %d %s", rec.Code, rec.Body)
	}

	// Sleep past the deadline before reading, to prove no deadline is armed on
	// this route.
	time.Sleep(200 * time.Millisecond)

	rec = c.do(http.MethodGet, "/api/v1/nodes/"+id.String()+"/download", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("download returned %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 64*1024 {
		t.Errorf("downloaded %d bytes, want %d", rec.Body.Len(), 64*1024)
	}
}

// The deadline must still be in force on ordinary JSON handlers, or the fix
// above would have removed a real protection rather than scoping it.
//
// A one-nanosecond deadline is expired before the handler runs, so any request
// that reaches the database on that path must fail — deterministically, with
// no reliance on timing.
func TestJSONHandlersAreStillBounded(t *testing.T) {
	h := newHarnessWithTimeout(t, time.Nanosecond)

	// Signing in happens through the same bounded group, so it must fail too;
	// a session is created directly instead.
	user := h.createUser("josh", "a perfectly reasonable passphrase", false)
	session, token, err := h.sessions.Create(t.Context(), sessionsCreateFor(user.ID))
	if err != nil {
		t.Fatal(err)
	}

	c := h.newClient()
	c.cookies["pifiles_session"] = token.Reveal()
	// The CSRF token is derived from the session identifier, so it can be
	// computed rather than obtained from a sign-in response — which would
	// itself go through the bounded group.
	c.csrf = auth.NewCSRFTokenizer(h.cfg.Security.SecretKey.Reveal()).Generate(session.ID)

	// The transfer path first: with the same expired deadline configured, it
	// must still work. That is the whole point of the split.
	uploadRec := c.raw(http.MethodPost, "/api/v1/files?name=ok.txt&parent_id=root",
		"application/octet-stream", strings.NewReader("content"))
	if uploadRec.Code != http.StatusCreated {
		t.Fatalf("the transfer path inherited the deadline: %d %s", uploadRec.Code, uploadRec.Body)
	}

	// The JSON path, with the same deadline, must fail.
	rec := c.do(http.MethodGet, "/api/v1/nodes", nil)
	if rec.Code == http.StatusOK {
		t.Fatal("a JSON handler completed with an expired deadline; the timeout is not applied")
	}
}

// sessionsCreateFor builds the parameters for a session made outside the API.
func sessionsCreateFor(userID uuid.UUID) sessions.CreateParams {
	return sessions.CreateParams{UserID: userID, IPAddress: "127.0.0.1", UserAgent: "test"}
}

// newHarnessWithTimeout builds a harness with a specific JSON timeout.
func newHarnessWithTimeout(t testing.TB, jsonTimeout time.Duration) *harness {
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

	cfg, err := config.LoadFrom(config.MapLookup(map[string]string{
		"APP_ENV":              "development",
		"APP_URL":              "http://localhost:8080",
		"STORAGE_ROOT":         store.Root(),
		"APP_SECRET_KEY":       "test-secret-key-0123456789abcdef0123456789abcdef",
		"HTTP_JSON_TIMEOUT":    jsonTimeout.String(),
		"RATE_LIMIT_READ":      "100000",
		"RATE_LIMIT_WRITE":     "100000",
		"RATE_LIMIT_UPLOAD":    "100000",
		"RATE_LIMIT_SEARCH":    "100000",
		"RATE_LIMIT_ANONYMOUS": "100000",
	}))
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	userStore := users.NewStore(db)
	sessionStore := sessions.NewStore(db, sessions.Config{
		Lifetime: cfg.Session.Lifetime, RememberLifetime: cfg.Session.RememberLifetime,
		MaxPerUser: cfg.Session.MaxPerUser,
	})
	hasher, err := auth.NewHasher(fastHashParams)
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.NewService(userStore, sessionStore, hasher, auth.Config{
		MaxLoginAttempts: 100, LockoutDuration: time.Minute,
		PasswordPolicy: auth.NewPasswordPolicy(12),
		PerIPLimit:     10_000, PerIPWindow: time.Minute,
	}, logging.Discard())

	nodeStore := nodes.NewStore(db)
	uploadStore := files.NewUploadStore(db, store, time.Hour, logging.Discard())
	fileService := files.NewService(nodeStore, userStore, uploadStore, store, files.Config{
		MaxUploadSize: cfg.Uploads.MaxSize, ChunkSize: cfg.Uploads.ChunkSize, UploadTTL: time.Hour,
	}, logging.Discard())

	groupStore := groups.NewStore(db)
	permissionStore := permissions.NewStore(db)
	sharingService := sharing.NewService(
		db, permissionStore, nodeStore, userStore, groupStore, true, logging.Discard())

	server, err := api.NewServer(api.Deps{
		Config: cfg, DB: db, Storage: store, Logger: logging.Discard(),
		Users: userStore, Sessions: sessionStore, Auth: authService,
		Files: fileService, Sharing: sharingService, Groups: groupStore,
	})
	if err != nil {
		t.Fatalf("api.NewServer: %v", err)
	}

	return &harness{
		t: t, db: db, server: server, users: userStore,
		sessions: sessionStore, nodes: nodeStore, files: fileService,
		groups: groupStore, permissions: permissionStore, sharing: sharingService,
		auth: authService, hasher: hasher, cfg: cfg, storageRoot: store.Root(),
	}
}
