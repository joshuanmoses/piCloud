package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// minimalProductionEnv is a configuration that must load cleanly. Tests mutate
// a copy of it to exercise individual validation rules.
func minimalProductionEnv() map[string]string {
	return map[string]string{
		"APP_ENV":           "production",
		"APP_URL":           "https://files.example.com",
		"DATABASE_PASSWORD": "a-real-database-password",
		"APP_SECRET_KEY":    "0123456789abcdef0123456789abcdef0123456789abcdef",
		"STORAGE_ROOT":      "/srv/pifiles",
	}
}

func loadWith(t *testing.T, env map[string]string) (*Config, error) {
	t.Helper()
	return LoadFrom(MapLookup(env))
}

func TestLoadProductionDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := loadWith(t, minimalProductionEnv())
	if err != nil {
		t.Fatalf("valid production config was rejected: %v", err)
	}

	if cfg.App.Name != "PiFiles" {
		t.Errorf("APP_NAME default = %q", cfg.App.Name)
	}
	if cfg.HTTP.Port != DefaultHTTPPort {
		t.Errorf("HTTP_PORT default = %d, want %d", cfg.HTTP.Port, DefaultHTTPPort)
	}
	if cfg.Uploads.MaxSize != 20*GB {
		t.Errorf("MAX_UPLOAD_SIZE default = %d, want %d", cfg.Uploads.MaxSize, 20*GB)
	}
	if cfg.Uploads.ChunkSize != 16*MB {
		t.Errorf("UPLOAD_CHUNK_SIZE default = %d, want %d", cfg.Uploads.ChunkSize, 16*MB)
	}
	if cfg.Trash.RetentionDays != 30 {
		t.Errorf("TRASH_RETENTION_DAYS default = %d, want 30", cfg.Trash.RetentionDays)
	}
	if !cfg.Versions.Enabled {
		t.Error("VERSIONING_ENABLED should default to true")
	}
	if cfg.Malware.Enabled {
		t.Error("MALWARE_SCANNING_ENABLED must default to false; ClamAV is too heavy for a Pi by default")
	}
	if cfg.App.RegistrationEnabled {
		t.Error("REGISTRATION_ENABLED must default to false")
	}
	if !cfg.Session.CookieSecure {
		t.Error("session cookies must default to Secure in production")
	}
	if cfg.Log.Format != "json" {
		t.Errorf("production log format = %q, want json", cfg.Log.Format)
	}
	if cfg.HTTP.WriteTimeout != 0 {
		t.Error("WriteTimeout must remain zero or large downloads will be truncated")
	}
}

func TestLoadDevelopmentRelaxesSecrets(t *testing.T) {
	t.Parallel()
	cfg, err := loadWith(t, map[string]string{
		"APP_ENV":      "development",
		"STORAGE_ROOT": "/tmp/pifiles",
	})
	if err != nil {
		t.Fatalf("development config without secrets should load: %v", err)
	}
	if cfg.Session.CookieSecure {
		t.Error("development cookies should not require Secure, or local HTTP login breaks")
	}
	if cfg.Log.Format != "console" {
		t.Errorf("development log format = %q, want console", cfg.Log.Format)
	}
}

func TestProductionRequiresSecrets(t *testing.T) {
	t.Parallel()
	env := minimalProductionEnv()
	delete(env, "APP_SECRET_KEY")
	delete(env, "DATABASE_PASSWORD")

	_, err := loadWith(t, env)
	if err == nil {
		t.Fatal("production without secrets must be rejected")
	}
	msg := err.Error()
	for _, want := range []string{"APP_SECRET_KEY", "DATABASE_PASSWORD"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should name %s; got:\n%s", want, msg)
		}
	}
}

func TestProductionRejectsPlaceholderSecret(t *testing.T) {
	t.Parallel()
	env := minimalProductionEnv()
	env["APP_SECRET_KEY"] = "changeme-changeme-changeme-changeme-changeme"

	_, err := loadWith(t, env)
	if err == nil {
		t.Fatal("a copied placeholder secret must be rejected, not accepted silently")
	}
	if !strings.Contains(err.Error(), "APP_SECRET_KEY") {
		t.Errorf("error should name APP_SECRET_KEY; got: %v", err)
	}
}

func TestProductionRequiresHTTPS(t *testing.T) {
	t.Parallel()
	env := minimalProductionEnv()
	env["APP_URL"] = "http://files.example.com"

	if _, err := loadWith(t, env); err == nil {
		t.Fatal("plain http APP_URL must be rejected in production")
	}

	// Loopback is exempt: it is how the installer verifies a fresh instance.
	env["APP_URL"] = "http://localhost:8080"
	if _, err := loadWith(t, env); err != nil {
		t.Fatalf("loopback http should be permitted: %v", err)
	}
}

func TestValidationAccumulatesEveryProblem(t *testing.T) {
	t.Parallel()
	env := minimalProductionEnv()
	env["HTTP_PORT"] = "99999"
	env["DATABASE_MAX_CONNS"] = "5000"
	env["UPLOAD_CHUNK_SIZE"] = "1KB"
	env["PASSWORD_MIN_LENGTH"] = "4"

	_, err := loadWith(t, env)
	if err == nil {
		t.Fatal("expected validation failure")
	}
	if !IsInvalid(err) {
		t.Error("IsInvalid should recognize a configuration error")
	}

	var cerr *Error
	if !errors.As(err, &cerr) {
		t.Fatalf("error should be a *config.Error, got %T", err)
	}
	// Reporting one problem at a time forces a nontechnical user through
	// repeated restart cycles.
	if len(cerr.Problems) < 4 {
		t.Errorf("expected at least 4 problems, got %d:\n%v", len(cerr.Problems), err)
	}
	for _, want := range []string{"HTTP_PORT", "DATABASE_MAX_CONNS", "UPLOAD_CHUNK_SIZE", "PASSWORD_MIN_LENGTH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s", want)
		}
	}
}

func TestStorageRootMustBeAbsolute(t *testing.T) {
	t.Parallel()
	for _, root := range []string{"relative/path", "./data", "/", "../escape"} {
		env := minimalProductionEnv()
		env["STORAGE_ROOT"] = root
		if _, err := loadWith(t, env); err == nil {
			t.Errorf("STORAGE_ROOT=%q must be rejected", root)
		}
	}

	// An unset or blank value is not an error: it falls back to the documented
	// default, which is what a fresh .env with a commented-out line produces.
	env := minimalProductionEnv()
	env["STORAGE_ROOT"] = ""
	cfg, err := loadWith(t, env)
	if err != nil {
		t.Fatalf("blank STORAGE_ROOT should fall back to the default: %v", err)
	}
	if cfg.Storage.Root != DefaultStorageRoot {
		t.Errorf("STORAGE_ROOT fallback = %q, want %q", cfg.Storage.Root, DefaultStorageRoot)
	}
}

func TestStorageRootIsCleaned(t *testing.T) {
	t.Parallel()
	env := minimalProductionEnv()
	env["STORAGE_ROOT"] = "/srv/pifiles/../pifiles/./"

	cfg, err := loadWith(t, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Storage.Root != "/srv/pifiles" {
		t.Errorf("STORAGE_ROOT = %q, want /srv/pifiles", cfg.Storage.Root)
	}
}

func TestStorageRootIsNeverHardcoded(t *testing.T) {
	t.Parallel()
	env := minimalProductionEnv()
	env["STORAGE_ROOT"] = "/mnt/usb-ssd/pifiles-data"

	cfg, err := loadWith(t, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Storage.Root != "/mnt/usb-ssd/pifiles-data" {
		t.Errorf("configured storage root was not honoured: %q", cfg.Storage.Root)
	}
	if got := cfg.Storage.Path(DirFiles); got != "/mnt/usb-ssd/pifiles-data/files" {
		t.Errorf("Path(files) = %q", got)
	}
}

func TestChunkSizeMustNotExceedMaxUpload(t *testing.T) {
	t.Parallel()
	env := minimalProductionEnv()
	env["MAX_UPLOAD_SIZE"] = "8MB"
	env["UPLOAD_CHUNK_SIZE"] = "16MB"

	_, err := loadWith(t, env)
	if err == nil {
		t.Fatal("a chunk larger than the maximum upload is nonsensical and must be rejected")
	}
	if !strings.Contains(err.Error(), "UPLOAD_CHUNK_SIZE") {
		t.Errorf("error should name UPLOAD_CHUNK_SIZE; got %v", err)
	}
}

func TestChunkSizeIsBoundedForPi(t *testing.T) {
	t.Parallel()
	env := minimalProductionEnv()
	env["UPLOAD_CHUNK_SIZE"] = "2GB"

	if _, err := loadWith(t, env); err == nil {
		t.Fatal("an oversized chunk would exhaust memory on a 4 GB board and must be rejected")
	}
}

func TestEmptyEnvironmentVariableFallsBackToDefault(t *testing.T) {
	t.Parallel()
	env := minimalProductionEnv()
	// A stray "HTTP_PORT=" line in a .env file must not be a fatal error.
	env["HTTP_PORT"] = ""
	env["LOG_LEVEL"] = "   "

	cfg, err := loadWith(t, env)
	if err != nil {
		t.Fatalf("empty values should fall back to defaults: %v", err)
	}
	if cfg.HTTP.Port != DefaultHTTPPort {
		t.Errorf("HTTP_PORT = %d, want default %d", cfg.HTTP.Port, DefaultHTTPPort)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("LOG_LEVEL = %q, want info", cfg.Log.Level)
	}
}

func TestUnknownEnumValuesAreRejected(t *testing.T) {
	t.Parallel()
	for key, bad := range map[string]string{
		"LOG_LEVEL":        "verbose",
		"LOG_FORMAT":       "xml",
		"APP_ENV":          "staging",
		"DATABASE_SSLMODE": "maybe",
	} {
		env := minimalProductionEnv()
		env[key] = bad
		if _, err := loadWith(t, env); err == nil {
			t.Errorf("%s=%q must be rejected", key, bad)
		}
	}
}

func TestDSNRedactsPassword(t *testing.T) {
	t.Parallel()
	cfg, err := loadWith(t, minimalProductionEnv())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	const password = "a-real-database-password"
	if !strings.Contains(cfg.Database.DSN(), password) {
		t.Fatal("DSN must carry the real password; it is handed to the driver")
	}
	if strings.Contains(cfg.Database.SafeDSN(), password) {
		t.Error("SafeDSN leaked the password")
	}
}

func TestDSNEscapesSpecialCharacters(t *testing.T) {
	t.Parallel()
	env := minimalProductionEnv()
	// Generated passwords can legitimately contain quotes and backslashes;
	// an unescaped one would corrupt the connection string.
	env["DATABASE_PASSWORD"] = `p'a\ss word`

	cfg, err := loadWith(t, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dsn := cfg.Database.DSN()
	if !strings.Contains(dsn, `password='p\'a\\ss word'`) {
		t.Errorf("password was not escaped correctly in DSN: %s", cfg.Database.SafeDSN())
	}
}

func TestConnectionPoolIsConservative(t *testing.T) {
	t.Parallel()
	cfg, err := loadWith(t, minimalProductionEnv())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// PostgreSQL on the Pi is configured for max_connections=50; the app pool
	// must leave room for maintenance connections and any second instance.
	if cfg.Database.MaxConns > 10 {
		t.Errorf("default pool size %d is too large for a Raspberry Pi", cfg.Database.MaxConns)
	}
	if cfg.Jobs.Workers > 2 {
		t.Errorf("default job workers %d will starve interactive requests", cfg.Jobs.Workers)
	}
}

func TestMalwareSettingsValidatedOnlyWhenEnabled(t *testing.T) {
	t.Parallel()
	env := minimalProductionEnv()
	env["CLAMAV_ADDRESS"] = ""
	if _, err := loadWith(t, env); err != nil {
		t.Fatalf("an empty ClamAV address is fine while scanning is disabled: %v", err)
	}

	env["MALWARE_SCANNING_ENABLED"] = "true"
	env["CLAMAV_TIMEOUT"] = "0s"
	if _, err := loadWith(t, env); err == nil {
		t.Fatal("enabling scanning with a zero timeout must be rejected")
	}
}

func TestPublicLinkURLConstruction(t *testing.T) {
	t.Parallel()
	env := minimalProductionEnv()
	// A trailing slash must not produce a double slash in generated links.
	env["APP_URL"] = "https://files.example.com/"

	cfg, err := loadWith(t, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.PublicLinkURL("r4ND0mSecureToken"); got != "https://files.example.com/s/r4ND0mSecureToken" {
		t.Errorf("PublicLinkURL = %q", got)
	}
	if got := cfg.UploadRequestURL("secureRandomToken"); got != "https://files.example.com/u/secureRandomToken" {
		t.Errorf("UploadRequestURL = %q", got)
	}
}

func TestSessionLifetimeOrdering(t *testing.T) {
	t.Parallel()
	env := minimalProductionEnv()
	env["SESSION_LIFETIME"] = "24h"
	env["SESSION_REMEMBER_LIFETIME"] = "1h"

	if _, err := loadWith(t, env); err == nil {
		t.Fatal("remember-me shorter than the base session is contradictory and must be rejected")
	}
}

func TestSubdirsCoverTheDocumentedLayout(t *testing.T) {
	t.Parallel()
	cfg, err := loadWith(t, minimalProductionEnv())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"files", "versions", "trash", "previews", "temp", "quarantine", "backups"}
	got := cfg.Storage.Subdirs()
	if len(got) != len(want) {
		t.Fatalf("Subdirs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Subdirs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDurationSettingsAcceptDaySuffix(t *testing.T) {
	t.Parallel()
	env := minimalProductionEnv()
	env["SESSION_REMEMBER_LIFETIME"] = "30d"
	env["VERSIONS_MAX_AGE"] = "90d"

	cfg, err := loadWith(t, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Session.RememberLifetime != 30*24*time.Hour {
		t.Errorf("SESSION_REMEMBER_LIFETIME = %v", cfg.Session.RememberLifetime)
	}
	if cfg.Versions.MaxAge != 90*24*time.Hour {
		t.Errorf("VERSIONS_MAX_AGE = %v", cfg.Versions.MaxAge)
	}
}
