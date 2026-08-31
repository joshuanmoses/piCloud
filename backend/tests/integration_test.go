// Package tests holds integration tests that require a live PostgreSQL server.
//
// They skip automatically when PIFILES_TEST_DATABASE_HOST is unset, so the
// default `go test ./...` remains fast and self-contained.
package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/joshuanmoses/piCloud/backend/internal/api"
	"github.com/joshuanmoses/piCloud/backend/internal/config"
	"github.com/joshuanmoses/piCloud/backend/internal/database"
	"github.com/joshuanmoses/piCloud/backend/internal/health"
	"github.com/joshuanmoses/piCloud/backend/internal/logging"
	"github.com/joshuanmoses/piCloud/backend/internal/storage"
	"github.com/joshuanmoses/piCloud/backend/migrations"
)

// testDatabaseConfig reads connection settings from the environment, skipping
// the test when none are provided.
func testDatabaseConfig(t testing.TB) config.Database {
	t.Helper()

	host := os.Getenv("PIFILES_TEST_DATABASE_HOST")
	if host == "" {
		t.Skip("PIFILES_TEST_DATABASE_HOST is not set; run `make test-integration`")
	}

	port := 5432
	if raw := os.Getenv("PIFILES_TEST_DATABASE_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("PIFILES_TEST_DATABASE_PORT=%q is not a number", raw)
		}
		port = parsed
	}

	return config.Database{
		Host:             host,
		Port:             port,
		Name:             envOr("PIFILES_TEST_DATABASE_NAME", "postgres"),
		User:             envOr("PIFILES_TEST_DATABASE_USER", "pifiles"),
		Password:         config.Secret(os.Getenv("PIFILES_TEST_DATABASE_PASSWORD")),
		SSLMode:          "disable",
		MaxConns:         4,
		MinConns:         1,
		MaxConnLifetime:  time.Hour,
		MaxConnIdleTime:  15 * time.Minute,
		ConnectTimeout:   10 * time.Second,
		StatementTimeout: 30 * time.Second,
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// freshDatabase creates a uniquely named, empty database and returns a
// connection to it. Migrations therefore always run against a genuinely clean
// schema, and concurrent tests cannot interfere with one another.
func freshDatabase(t testing.TB) *database.DB {
	t.Helper()
	ctx := t.Context()

	admin := testDatabaseConfig(t)
	adminDB, err := database.Connect(ctx, admin, logging.Discard())
	if err != nil {
		t.Fatalf("cannot reach the test PostgreSQL server: %v", err)
	}

	// The name is derived from the test name plus a nanosecond stamp. It is
	// interpolated into DDL, which is unavoidable — PostgreSQL does not accept
	// a parameter for an identifier — so it is sanitized to [a-z0-9_] first.
	name := fmt.Sprintf("pifiles_test_%s_%d", sanitizeIdent(t.Name()), time.Now().UnixNano())
	if _, err := adminDB.Pool().Exec(ctx, `CREATE DATABASE `+pgQuoteIdent(name)); err != nil {
		adminDB.Close()
		t.Fatalf("cannot create test database: %v", err)
	}
	adminDB.Close()

	cfg := admin
	cfg.Name = name

	db, err := database.Connect(ctx, cfg, logging.Discard())
	if err != nil {
		t.Fatalf("cannot connect to the fresh test database: %v", err)
	}

	t.Cleanup(func() {
		db.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		dropper, err := database.Connect(cleanupCtx, admin, logging.Discard())
		if err != nil {
			t.Logf("could not connect to drop test database %s: %v", name, err)
			return
		}
		defer dropper.Close()
		if _, err := dropper.Pool().Exec(cleanupCtx,
			`DROP DATABASE IF EXISTS `+pgQuoteIdent(name)+` WITH (FORCE)`); err != nil {
			t.Logf("could not drop test database %s: %v", name, err)
		}
	})

	return db
}

func sanitizeIdent(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}

// pgQuoteIdent double-quotes an identifier, escaping embedded quotes.
func pgQuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// --- Migrations ------------------------------------------------------------

func TestMigrationsApplyToCleanDatabase(t *testing.T) {
	db := freshDatabase(t)
	ctx := t.Context()

	result, err := db.Migrate(ctx, migrations.FS)
	if err != nil {
		t.Fatalf("migrations failed against a clean database: %v", err)
	}
	if len(result.Applied) == 0 {
		t.Fatal("no migrations were applied to an empty database")
	}
	if result.AlreadyCurrent {
		t.Error("a clean database should not report as already current")
	}
	if result.SchemaVersion < 1 {
		t.Errorf("schema version = %d, want at least 1", result.SchemaVersion)
	}

	// The ledger must record exactly what was applied.
	var count int
	if err := db.Pool().QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("schema_migrations was not created: %v", err)
	}
	if count != len(result.Applied) {
		t.Errorf("ledger has %d rows, but %d migrations were applied", count, len(result.Applied))
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	db := freshDatabase(t)
	ctx := t.Context()

	first, err := db.Migrate(ctx, migrations.FS)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Every container restart re-runs this. It must be a no-op.
	second, err := db.Migrate(ctx, migrations.FS)
	if err != nil {
		t.Fatalf("second run failed; every restart would fail: %v", err)
	}
	if len(second.Applied) != 0 {
		t.Errorf("second run applied %d migrations, want 0", len(second.Applied))
	}
	if !second.AlreadyCurrent {
		t.Error("second run should report the schema as current")
	}
	if second.SchemaVersion != first.SchemaVersion {
		t.Errorf("schema version changed on a no-op run: %d then %d", first.SchemaVersion, second.SchemaVersion)
	}
}

func TestMigrationsRefuseEditedHistory(t *testing.T) {
	db := freshDatabase(t)
	ctx := t.Context()

	if _, err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("baseline migration: %v", err)
	}

	// Simulate somebody editing an already-applied migration file.
	//
	// Every migration is copied and only the first is altered, so the runner
	// reaches the checksum check rather than the separate "binary is older
	// than the schema" check. Building the set from the real embedded files
	// keeps this test correct as more migrations are added.
	tampered := fstest.MapFS{}
	entries, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range entries {
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(name, "0001_") {
			body = []byte("SELECT 'tampered';")
		}
		tampered[name] = &fstest.MapFile{Data: body}
	}

	_, err = db.Migrate(ctx, tampered)
	if err == nil {
		t.Fatal("an edited applied migration must be refused")
	}
	if !strings.Contains(err.Error(), "modified") {
		t.Errorf("error should explain the modification: %v", err)
	}
	if !strings.Contains(err.Error(), "add a new migration") {
		t.Errorf("error should say what to do instead: %v", err)
	}
}

func TestMigrationsCreateExpectedObjects(t *testing.T) {
	db := freshDatabase(t)
	ctx := t.Context()

	if _, err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, ext := range []string{"citext", "pg_trgm"} {
		var exists bool
		if err := db.Pool().QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = $1)`, ext).Scan(&exists); err != nil {
			t.Fatalf("checking extension %s: %v", ext, err)
		}
		if !exists {
			t.Errorf("extension %s was not created", ext)
		}
	}

	var settingCount int
	if err := db.Pool().QueryRow(ctx, `SELECT count(*) FROM system_settings`).Scan(&settingCount); err != nil {
		t.Fatalf("system_settings is missing: %v", err)
	}
	if settingCount == 0 {
		t.Error("system_settings should be seeded")
	}

	// The shared updated_at trigger must actually fire.
	var before, after time.Time
	if err := db.Pool().QueryRow(ctx,
		`SELECT updated_at FROM system_settings WHERE key = 'instance.name'`).Scan(&before); err != nil {
		t.Fatalf("reading seeded setting: %v", err)
	}
	if _, err := db.Pool().Exec(ctx,
		`UPDATE system_settings SET value = '"Renamed"' WHERE key = 'instance.name'`); err != nil {
		t.Fatalf("updating setting: %v", err)
	}
	if err := db.Pool().QueryRow(ctx,
		`SELECT updated_at FROM system_settings WHERE key = 'instance.name'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.After(before) {
		t.Error("the set_updated_at trigger did not fire")
	}
}

func TestSchemaVersionReporting(t *testing.T) {
	db := freshDatabase(t)
	ctx := t.Context()

	result, err := db.Migrate(ctx, migrations.FS)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}

	got, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if got != result.SchemaVersion {
		t.Errorf("SchemaVersion() = %d, want %d", got, result.SchemaVersion)
	}
}

// --- Transactions ----------------------------------------------------------

func TestInTxRollsBackOnError(t *testing.T) {
	db := freshDatabase(t)
	ctx := t.Context()

	if _, err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	sentinel := fmt.Errorf("deliberate failure")
	err := db.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO system_settings (key, value) VALUES ('test.rollback', '"x"')`); err != nil {
			return err
		}
		return sentinel
	})
	if err == nil || !strings.Contains(err.Error(), "deliberate failure") {
		t.Fatalf("InTx should return the callback's error, got %v", err)
	}

	// This is the guarantee the whole file-operation design rests on: a failed
	// transaction must leave nothing behind.
	var count int
	if err := db.Pool().QueryRow(ctx,
		`SELECT count(*) FROM system_settings WHERE key = 'test.rollback'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("the failed transaction was not rolled back")
	}
}

func TestInTxCommitsOnSuccess(t *testing.T) {
	db := freshDatabase(t)
	ctx := t.Context()

	if _, err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	err := db.InTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO system_settings (key, value) VALUES ('test.commit', '"y"')`)
		return err
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}

	var value string
	if err := db.Pool().QueryRow(ctx,
		`SELECT value #>> '{}' FROM system_settings WHERE key = 'test.commit'`).Scan(&value); err != nil {
		t.Fatalf("the committed row is missing: %v", err)
	}
	if value != "y" {
		t.Errorf("value = %q, want y", value)
	}
}

func TestInTxRollsBackOnPanic(t *testing.T) {
	db := freshDatabase(t)
	ctx := t.Context()

	if _, err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic should propagate after rollback")
			}
		}()
		_ = db.InTx(ctx, func(tx pgx.Tx) error {
			_, _ = tx.Exec(ctx, `INSERT INTO system_settings (key, value) VALUES ('test.panic', '"z"')`)
			panic("handler bug inside a transaction")
		})
	}()

	var count int
	if err := db.Pool().QueryRow(ctx,
		`SELECT count(*) FROM system_settings WHERE key = 'test.panic'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("a panic must not leave a half-finished transaction committed")
	}
}

func TestUniqueViolationIsDetectable(t *testing.T) {
	db := freshDatabase(t)
	ctx := t.Context()

	if _, err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	_, err := db.Pool().Exec(ctx,
		`INSERT INTO system_settings (key, value) VALUES ('instance.name', '"duplicate"')`)
	if err == nil {
		t.Fatal("expected a unique violation")
	}
	if !database.IsUniqueViolation(err) {
		t.Errorf("IsUniqueViolation should recognize this error: %v", err)
	}
	if database.ConstraintName(err) == "" {
		t.Error("ConstraintName should name the violated constraint for server-side branching")
	}
}

// --- Full stack ------------------------------------------------------------

// TestHealthEndpointAgainstRealDependencies wires the real database, the real
// storage root, and the real router together, which is the closest thing to
// what runs in production.
func TestHealthEndpointAgainstRealDependencies(t *testing.T) {
	db := freshDatabase(t)
	ctx := t.Context()

	if _, err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	store, err := storage.Open(config.Storage{
		Root:         t.TempDir(),
		DirMode:      0o750,
		FileMode:     0o640,
		MinFreeBytes: 1024,
	}, logging.Discard())
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer store.Close()

	cfg, err := config.LoadFrom(config.MapLookup(map[string]string{
		"APP_ENV":      "development",
		"APP_URL":      "http://localhost:8080",
		"STORAGE_ROOT": store.Root(),
	}))
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	srv, err := api.NewServer(api.Deps{
		Config: cfg, DB: db, Storage: store, Logger: logging.Discard(),
	})
	if err != nil {
		t.Fatalf("api.NewServer: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}

	var body api.HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
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

	// Even wired to real dependencies, /health must not describe them.
	lowered := strings.ToLower(rec.Body.String())
	for _, forbidden := range []string{store.Root(), "postgres", "5432", "password"} {
		if forbidden != "" && strings.Contains(lowered, strings.ToLower(forbidden)) {
			t.Errorf("health response leaked %q: %s", forbidden, rec.Body.String())
		}
	}
}

func TestDatabaseCheckReportsUnhealthyAfterClose(t *testing.T) {
	db := freshDatabase(t)
	ctx := t.Context()

	if h := db.Check(ctx); h.Status != health.Healthy {
		t.Fatalf("a live database should be healthy, got %q (%s)", h.Status, h.Detail)
	}

	db.Close()

	h := db.Check(context.Background())
	if h.Status != health.Unhealthy {
		t.Errorf("status = %q, want unhealthy after the pool is closed", h.Status)
	}
	// The failure detail must stay generic; /health is unauthenticated.
	if strings.Contains(strings.ToLower(h.Detail), "password") ||
		strings.Contains(h.Detail, "5432") {
		t.Errorf("health detail leaked connection information: %q", h.Detail)
	}
}
