package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joshuanmoses/piCloud/backend/internal/config"
	"github.com/joshuanmoses/piCloud/backend/internal/database"
	"github.com/joshuanmoses/piCloud/backend/internal/health"
	"github.com/joshuanmoses/piCloud/backend/internal/logging"
	"github.com/joshuanmoses/piCloud/backend/internal/storage"
)

// --- Test doubles ---------------------------------------------------------

type stubDB struct {
	health database.Health
	calls  int
}

func (s *stubDB) Check(context.Context) database.Health {
	s.calls++
	return s.health
}

type stubStorage struct {
	health storage.Health
	calls  int
}

func (s *stubStorage) Check() storage.Health {
	s.calls++
	return s.health
}

func healthyDB() *stubDB {
	return &stubDB{health: database.Health{Status: health.Healthy, LatencyMs: 1, OpenConns: 2, MaxConns: 10}}
}

func healthyStorage() *stubStorage {
	return &stubStorage{health: storage.Health{
		Status: health.Healthy, Writable: true, FilesystemType: "ext2/ext3/ext4",
		TotalBytes: 500 << 30, FreeBytes: 400 << 30, UsedPercent: 20,
	}}
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.LoadFrom(config.MapLookup(map[string]string{
		"APP_ENV":      "development",
		"APP_NAME":     "PiFiles",
		"APP_URL":      "http://localhost:8080",
		"STORAGE_ROOT": "/tmp/pifiles-test",
	}))
	if err != nil {
		t.Fatalf("test config failed to load: %v", err)
	}
	return cfg
}

func newTestServer(t *testing.T, db DatabaseService, store StorageService) *Server {
	t.Helper()
	srv, err := NewServer(Deps{
		Config:  testConfig(t),
		DB:      db,
		Storage: store,
		Logger:  logging.Discard(),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

func do(t *testing.T, srv *Server, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response was not valid JSON (%s): %v", rec.Body.String(), err)
	}
	return out
}

// --- Health ----------------------------------------------------------------

func TestHealthReportsHealthy(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, healthyDB(), healthyStorage())

	rec := do(t, srv, http.MethodGet, "/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}

	body := decode[HealthResponse](t, rec)
	if body.Status != health.Healthy {
		t.Errorf("status = %q, want healthy", body.Status)
	}
	if body.Database != health.Healthy {
		t.Errorf("database = %q, want healthy", body.Database)
	}
	if body.Storage != health.Healthy {
		t.Errorf("storage = %q, want healthy", body.Storage)
	}
	if body.Version == "" {
		t.Error("version should be reported")
	}
	if len(body.Checks) != 2 {
		t.Errorf("expected per-subsystem checks, got %d", len(body.Checks))
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestHealthReturns503WhenDatabaseIsDown(t *testing.T) {
	t.Parallel()
	db := &stubDB{health: database.Health{Status: health.Unhealthy, Detail: "database query failed"}}
	srv := newTestServer(t, db, healthyStorage())

	rec := do(t, srv, http.MethodGet, "/health")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if body := decode[HealthResponse](t, rec); body.Status != health.Unhealthy {
		t.Errorf("overall status = %q, want unhealthy", body.Status)
	}
}

// A degraded subsystem must not take the instance out of service: a disk at 90%
// is an administrator's problem, not a reason to stop serving requests.
func TestHealthReturns200WhenDegraded(t *testing.T) {
	t.Parallel()
	store := &stubStorage{health: storage.Health{
		Status: health.Degraded, Writable: true, Detail: "free space is approaching the configured minimum",
	}}
	srv := newTestServer(t, healthyDB(), store)

	rec := do(t, srv, http.MethodGet, "/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a degraded instance", rec.Code)
	}
	if body := decode[HealthResponse](t, rec); body.Status != health.Degraded {
		t.Errorf("status = %q, want degraded", body.Status)
	}
}

// TestHealthDoesNotLeakInfrastructure guards an unauthenticated endpoint: it
// must not describe hosts, ports, paths, credentials, or server versions.
func TestHealthDoesNotLeakInfrastructure(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, healthyDB(), healthyStorage())

	rec := do(t, srv, http.MethodGet, "/health")
	body := strings.ToLower(rec.Body.String())

	forbidden := []string{
		"postgres", "5432", "password", "/srv/", "/tmp/", "dsn",
		"localhost", "secret", "token", "user=", "host=",
	}
	for _, needle := range forbidden {
		if strings.Contains(body, needle) {
			t.Errorf("health response leaked %q: %s", needle, rec.Body.String())
		}
	}
}

func TestHealthIsCachedToProtectThePi(t *testing.T) {
	t.Parallel()
	db := healthyDB()
	store := healthyStorage()
	srv := newTestServer(t, db, store)

	// Docker, Caddy, and any uptime monitor all poll this endpoint. Probing
	// the database and the filesystem on every request would be a self-
	// inflicted load problem.
	for range 10 {
		if rec := do(t, srv, http.MethodGet, "/health"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	if db.calls != 1 {
		t.Errorf("database was probed %d times for 10 requests; want 1", db.calls)
	}
	if store.calls != 1 {
		t.Errorf("storage was probed %d times for 10 requests; want 1", store.calls)
	}
}

// Liveness must not depend on the database: restarting the container does not
// fix a database outage, so a failing database must not cause a restart loop.
func TestLivenessIgnoresDependencies(t *testing.T) {
	t.Parallel()
	db := &stubDB{health: database.Health{Status: health.Unhealthy}}
	srv := newTestServer(t, db, healthyStorage())

	rec := do(t, srv, http.MethodGet, "/health/live")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if db.calls != 0 {
		t.Error("liveness must not probe the database")
	}
}

func TestReadinessFailsWhenDependenciesAreDown(t *testing.T) {
	t.Parallel()
	db := &stubDB{health: database.Health{Status: health.Unhealthy}}
	srv := newTestServer(t, db, healthyStorage())

	rec := do(t, srv, http.MethodGet, "/health/ready")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestReadinessIsNotCached(t *testing.T) {
	t.Parallel()
	db := healthyDB()
	srv := newTestServer(t, db, healthyStorage())

	for range 3 {
		do(t, srv, http.MethodGet, "/health/ready")
	}
	if db.calls != 3 {
		t.Errorf("readiness was probed %d times for 3 requests; startup ordering needs a live answer", db.calls)
	}
}

// --- Public config ---------------------------------------------------------

func TestPublicConfigExposesOnlySafeSettings(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, healthyDB(), healthyStorage())

	rec := do(t, srv, http.MethodGet, APIPrefix+"/config")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	cfg := decode[PublicConfig](t, rec)
	if cfg.AppName != "PiFiles" {
		t.Errorf("app_name = %q", cfg.AppName)
	}
	if cfg.MaxUploadBytes <= 0 || cfg.ChunkSizeBytes <= 0 {
		t.Error("upload limits should be published so the client can chunk correctly")
	}

	// The published key set is asserted exactly. Anything added to PublicConfig
	// must be a deliberate decision reviewed here, rather than something that
	// slid in because a struct field was added upstream.
	allowed := map[string]bool{
		"app_name": true, "version": true, "registration_enabled": true,
		"sharing_enabled": true, "public_links_enabled": true,
		"upload_requests_enabled": true, "max_upload_bytes": true,
		"chunk_size_bytes": true, "password_min_length": true,
		"trash_retention_days": true, "versioning_enabled": true,
	}

	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for key := range raw {
		if !allowed[key] {
			t.Errorf("public config exposes an unreviewed key %q", key)
		}
	}

	// And no deployment detail may appear anywhere in the payload.
	body := strings.ToLower(rec.Body.String())
	for _, forbidden := range []string{"postgres", "5432", "/srv/", "dsn", "secret_key", "sslmode"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("public config leaked %q: %s", forbidden, rec.Body.String())
		}
	}
}

// --- Errors and envelopes --------------------------------------------------

func TestUnknownAPIPathReturnsJSONNotFound(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, healthyDB(), healthyStorage())

	rec := do(t, srv, http.MethodGet, APIPrefix+"/does-not-exist")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	body := decode[ErrorResponse](t, rec)
	if body.Error.Code != CodeNotFound {
		t.Errorf("code = %q, want %q", body.Error.Code, CodeNotFound)
	}
	if body.RequestID == "" {
		t.Error("error envelope should carry the request ID for support correlation")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("an unknown API path must return JSON, not HTML; Content-Type = %q", ct)
	}
}

// Without a static handler, non-API paths 404 as JSON too; with one, the SPA
// takes over. This test covers the API-only configuration used by tests.
func TestUnknownNonAPIPathWithoutSPAReturnsJSON(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, healthyDB(), healthyStorage())

	rec := do(t, srv, http.MethodGet, "/some/app/route")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestSPAHandlesUnknownNonAPIPaths(t *testing.T) {
	t.Parallel()
	spa := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><title>app</title>"))
	})
	srv, err := NewServer(Deps{
		Config: testConfig(t), DB: healthyDB(), Storage: healthyStorage(),
		Logger: logging.Discard(), StaticHandler: spa,
	})
	if err != nil {
		t.Fatal(err)
	}

	// A deep link into the app must load the SPA...
	rec := do(t, srv, http.MethodGet, "/files/photos/2026")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Errorf("deep link should serve the SPA; got %d %s", rec.Code, rec.Body)
	}

	// ...but an unknown API path must still be a JSON 404, never HTML.
	rec = do(t, srv, http.MethodGet, APIPrefix+"/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Error("the SPA must never shadow an unknown API route")
	}
}

func TestMethodNotAllowedReturnsJSON(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, healthyDB(), healthyStorage())

	rec := do(t, srv, http.MethodDelete, APIPrefix+"/config")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if body := decode[ErrorResponse](t, rec); body.Error.Message == "" {
		t.Error("405 should carry an explanatory message")
	}
}

func TestErrorsNeverExposeInternalCauses(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, healthyDB(), healthyStorage())

	const secret = "pq: relation \"users\" does not exist at character 15"
	srv.router.Get("/boom", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, ErrInternal(errInternalCause(secret)))
	})

	rec := do(t, srv, http.MethodGet, "/boom")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "relation") || strings.Contains(rec.Body.String(), "pq:") {
		t.Errorf("internal database error leaked to the client: %s", rec.Body)
	}
	if body := decode[ErrorResponse](t, rec); body.Error.Code != CodeInternal {
		t.Errorf("code = %q", body.Error.Code)
	}
}

func TestPanicIsRecoveredAsJSON500(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, healthyDB(), healthyStorage())
	srv.router.Get("/panic", func(http.ResponseWriter, *http.Request) {
		panic("handler bug")
	})

	rec := do(t, srv, http.MethodGet, "/panic")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "handler bug") {
		t.Error("panic text must not reach the client")
	}
	if strings.Contains(rec.Body.String(), "goroutine") {
		t.Error("a stack trace must never be sent to the client")
	}
}

// --- Middleware ------------------------------------------------------------

func TestSecurityHeadersArePresent(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, healthyDB(), healthyStorage())

	rec := do(t, srv, http.MethodGet, "/health")
	want := map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "same-origin",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
	}
	for header, expected := range want {
		if got := rec.Header().Get(header); got != expected {
			t.Errorf("%s = %q, want %q", header, got, expected)
		}
	}

	csp := rec.Header().Get("Content-Security-Policy")
	for _, directive := range []string{"default-src 'self'", "object-src 'none'", "frame-ancestors 'none'", "base-uri 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP is missing %q; got %q", directive, csp)
		}
	}
}

// HSTS must follow the actual connection, not the environment. A plain-HTTP
// LAN install is supported; sending HSTS there pins the browser to a scheme
// the server cannot serve and locks the user out until they clear it manually.
func TestHSTSOnlyOverHTTPS(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, healthyDB(), healthyStorage())

	// Plain HTTP, no proxy: no HSTS.
	rec := do(t, srv, http.MethodGet, "/health")
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS was sent over plain HTTP: %q", got)
	}

	// Behind a trusted proxy that terminated TLS: HSTS applies.
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "127.0.0.1:9999"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Header().Get("Strict-Transport-Security"), "max-age=") {
		t.Error("HSTS should be set when the client connection was HTTPS")
	}
}

// A client that can forge X-Forwarded-Proto could otherwise induce the server
// to send HSTS and lock a plain-HTTP instance out of its own users' browsers.
func TestForwardedProtoIgnoredFromUntrustedPeer(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, healthyDB(), healthyStorage())

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "203.0.113.7:44321" // not a trusted proxy
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("a forged X-Forwarded-Proto caused HSTS to be sent: %q", got)
	}
}

func TestForwardedProtoParsing(t *testing.T) {
	t.Parallel()
	check, err := TrustedProxyChecker([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatal(err)
	}

	var scheme string
	handler := ForwardedProto(check)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		scheme = RequestSchemeFromContext(r.Context())
	}))

	cases := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		want       string
	}{
		{"no headers", "127.0.0.1:1", nil, "http"},
		{"x-forwarded-proto https", "127.0.0.1:1", map[string]string{"X-Forwarded-Proto": "https"}, "https"},
		{"x-forwarded-proto uppercase", "127.0.0.1:1", map[string]string{"X-Forwarded-Proto": "HTTPS"}, "https"},
		{"rfc 7239 forwarded", "127.0.0.1:1", map[string]string{"Forwarded": `for=192.0.2.1;proto=https`}, "https"},
		{"garbage value ignored", "127.0.0.1:1", map[string]string{"X-Forwarded-Proto": "javascript:"}, "http"},
		{"untrusted peer ignored", "203.0.113.9:1", map[string]string{"X-Forwarded-Proto": "https"}, "http"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			handler.ServeHTTP(httptest.NewRecorder(), req)
			if scheme != tc.want {
				t.Errorf("scheme = %q, want %q", scheme, tc.want)
			}
		})
	}
}

func TestAPIResponsesAreNotCacheable(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, healthyDB(), healthyStorage())

	for _, path := range []string{"/health", APIPrefix + "/config"} {
		rec := do(t, srv, http.MethodGet, path)
		cc := rec.Header().Get("Cache-Control")
		if !strings.Contains(cc, "no-store") {
			t.Errorf("%s Cache-Control = %q; API responses must not be cached", path, cc)
		}
	}
}

func TestRequestIDIsGeneratedAndEchoed(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, healthyDB(), healthyStorage())

	rec := do(t, srv, http.MethodGet, "/health")
	id := rec.Header().Get(RequestIDHeader)
	if id == "" {
		t.Fatal("every response must carry a request ID")
	}

	second := do(t, srv, http.MethodGet, "/health")
	if second.Header().Get(RequestIDHeader) == id {
		t.Error("request IDs must be unique per request")
	}
}

// A client-supplied request ID must not be trusted from an untrusted peer:
// otherwise a caller can inject arbitrary text into log lines or collapse
// correlation by reusing one ID.
func TestRequestIDFromUntrustedClientIsIgnored(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, healthyDB(), healthyStorage())

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "203.0.113.7:44321" // public address, not a trusted proxy
	req.Header.Set(RequestIDHeader, "attacker-supplied")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if got := rec.Header().Get(RequestIDHeader); got == "attacker-supplied" {
		t.Error("request ID from an untrusted peer must be replaced")
	}
}

func TestRequestIDFromTrustedProxyIsHonoured(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, healthyDB(), healthyStorage())

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "127.0.0.1:9999" // Caddy, on the trusted list
	req.Header.Set(RequestIDHeader, "caddy-trace-42")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if got := rec.Header().Get(RequestIDHeader); got != "caddy-trace-42" {
		t.Errorf("request ID from a trusted proxy = %q, want caddy-trace-42", got)
	}
}

func TestMalformedRequestIDFromTrustedProxyIsReplaced(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, healthyDB(), healthyStorage())

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "127.0.0.1:9999"
	// Newlines would forge additional log lines.
	req.Header.Set(RequestIDHeader, "abc\ndef INFO fake log line")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if got := rec.Header().Get(RequestIDHeader); strings.Contains(got, "\n") {
		t.Errorf("a request ID containing a newline must be rejected; got %q", got)
	}
}

func TestTrustedProxyChecker(t *testing.T) {
	t.Parallel()
	check, err := TrustedProxyChecker([]string{"127.0.0.1/32", "10.0.0.0/8", "::1", "192.168.1.5"})
	if err != nil {
		t.Fatalf("TrustedProxyChecker: %v", err)
	}

	trusted := []string{"127.0.0.1", "10.4.5.6", "::1", "192.168.1.5"}
	for _, ip := range trusted {
		if !check(ip) {
			t.Errorf("%s should be trusted", ip)
		}
	}
	untrusted := []string{"203.0.113.1", "192.168.1.6", "8.8.8.8", "", "not-an-ip", "11.0.0.1"}
	for _, ip := range untrusted {
		if check(ip) {
			t.Errorf("%s should not be trusted", ip)
		}
	}
}

func TestTrustedProxyCheckerRejectsGarbage(t *testing.T) {
	t.Parallel()
	// Silently ignoring a malformed entry would leave the operator believing
	// their proxy is trusted when it is not.
	for _, bad := range []string{"not-a-cidr", "10.0.0.0/64", "999.1.1.1"} {
		if _, err := TrustedProxyChecker([]string{bad}); err == nil {
			t.Errorf("TrustedProxyChecker(%q) should fail", bad)
		}
	}
}

func TestClientIPPrefersForwardedHeaderOnlyFromTrustedProxy(t *testing.T) {
	t.Parallel()
	var captured string
	check, err := TrustedProxyChecker([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatal(err)
	}
	handler := RealIP(check)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		captured = ClientIPFromContext(r.Context())
	}))

	// From Caddy: trust the forwarded address.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.42, 10.0.0.1")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if captured != "198.51.100.42" {
		t.Errorf("client IP = %q, want 198.51.100.42", captured)
	}

	// Direct from the internet: the header is a lie and must be ignored,
	// otherwise rate limiting and lockout can be trivially evaded.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if captured != "203.0.113.7" {
		t.Errorf("client IP = %q, want the real peer 203.0.113.7", captured)
	}
}

// --- JSON decoding ---------------------------------------------------------

func TestDecodeJSONRejectsBadInput(t *testing.T) {
	t.Parallel()
	type payload struct {
		Name string `json:"name"`
	}

	cases := []struct {
		name        string
		body        string
		contentType string
	}{
		{"malformed", `{"name":`, "application/json"},
		{"empty", ``, "application/json"},
		{"wrong type", `{"name": 42}`, "application/json"},
		{"unknown field", `{"name":"a","admin":true}`, "application/json"},
		{"two objects", `{"name":"a"}{"name":"b"}`, "application/json"},
		{"wrong content type", `{"name":"a"}`, "text/plain"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			var dst payload
			if err := DecodeJSON(httptest.NewRecorder(), req, &dst, maxJSONBody); err == nil {
				t.Errorf("DecodeJSON accepted %s", tc.name)
			}
		})
	}
}

// An unknown field is rejected rather than ignored: silently dropping it hides
// client bugs and can mask a parameter-smuggling attempt such as {"is_admin":true}.
func TestDecodeJSONRejectsPrivilegeSmuggling(t *testing.T) {
	t.Parallel()
	type createUser struct {
		Username string `json:"username"`
	}

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"username":"bob","is_admin":true}`))
	req.Header.Set("Content-Type", "application/json")

	var dst createUser
	err := DecodeJSON(httptest.NewRecorder(), req, &dst, maxJSONBody)
	if err == nil {
		t.Fatal("an unrecognized field must be rejected, not silently ignored")
	}
}

func TestDecodeJSONEnforcesSizeLimit(t *testing.T) {
	t.Parallel()
	big := `{"name":"` + strings.Repeat("a", 4096) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big))
	req.Header.Set("Content-Type", "application/json")

	var dst struct {
		Name string `json:"name"`
	}
	if err := DecodeJSON(httptest.NewRecorder(), req, &dst, 512); err == nil {
		t.Fatal("an oversized body must be rejected")
	}
}

func TestDecodeJSONAcceptsValidInput(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"Report.pdf"}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	var dst struct {
		Name string `json:"name"`
	}
	if err := DecodeJSON(httptest.NewRecorder(), req, &dst, maxJSONBody); err != nil {
		t.Fatalf("valid JSON was rejected: %v", err)
	}
	if dst.Name != "Report.pdf" {
		t.Errorf("name = %q", dst.Name)
	}
}

// --- Error helpers ---------------------------------------------------------

func TestAPIErrorWrappingKeepsCausePrivate(t *testing.T) {
	t.Parallel()
	cause := errInternalCause("connection refused to 10.0.0.5:5432")
	err := ErrInternal(cause)

	if !strings.Contains(err.Error(), "connection refused") {
		t.Error("the cause should be available for logging")
	}
	if strings.Contains(err.Message, "connection refused") {
		t.Error("the client-facing message must not contain the cause")
	}
	if err.Unwrap() != cause {
		t.Error("Unwrap should expose the cause to errors.Is/As")
	}
}

func TestNotFoundMessageIsUniform(t *testing.T) {
	t.Parallel()
	// A message that varies with whether a resource exists is an enumeration
	// oracle, so 404 text must be identical everywhere.
	if ErrNotFound().Message != ErrNotFound().Message {
		t.Error("404 messages must be identical")
	}
	if strings.Contains(strings.ToLower(ErrNotFound().Message), "permission") {
		t.Error("404 must not hint at why access failed")
	}
}

func TestWithFieldDoesNotMutateOriginal(t *testing.T) {
	t.Parallel()
	base := ErrValidation("Check the form.")
	withField := base.WithField("username", "already taken")

	if base.Fields != nil {
		t.Error("WithField must not mutate the receiver")
	}
	if withField.Fields["username"] != "already taken" {
		t.Errorf("fields = %v", withField.Fields)
	}
}

// errInternalCause builds a plain error for tests.
func errInternalCause(msg string) error { return &simpleError{msg} }

type simpleError struct{ msg string }

func (e *simpleError) Error() string { return e.msg }
