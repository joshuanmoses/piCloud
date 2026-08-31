// Package config loads, validates, and exposes PiFiles runtime configuration.
//
// Configuration comes exclusively from environment variables so that the same
// binary behaves identically under Docker Compose, systemd, and `go run`. Every
// setting has a documented default in .env.example; only secrets are mandatory,
// and only in production.
package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Environment names the deployment mode. It gates security defaults such as
// whether cookies must be Secure and whether a real secret key is required.
type Environment string

const (
	// EnvDevelopment relaxes secret requirements and enables human-readable logs.
	EnvDevelopment Environment = "development"
	// EnvProduction enforces the full security posture.
	EnvProduction Environment = "production"
)

// IsProduction reports whether the strict production posture applies.
func (e Environment) IsProduction() bool { return e == EnvProduction }

// Config is the fully validated runtime configuration.
type Config struct {
	App      App
	HTTP     HTTP
	Database Database
	Storage  Storage
	Uploads  Uploads
	Session  Session
	Security Security
	Sharing  Sharing
	Trash    Trash
	Versions Versions
	Malware  Malware
	Jobs     Jobs
	Audit    Audit
	Limits   RateLimits
	Log      Log
}

// App holds product-level identity settings.
type App struct {
	Name string
	// URL is the externally reachable base URL. Public share links and upload
	// request links are built from it, so it must match what users type.
	URL         string
	Environment Environment
	// RegistrationEnabled allows self-service signup. Off by default: a private
	// family or small-business file server should not accept strangers.
	RegistrationEnabled bool
}

// HTTP configures the application's own listener. Caddy terminates TLS in
// front of it, so this listener speaks plain HTTP on a private network.
type HTTP struct {
	Host string
	Port int
	// ReadHeaderTimeout bounds slowloris-style attacks. It deliberately does
	// not bound the body, because a large upload legitimately takes hours.
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	// JSONTimeout bounds ordinary JSON handlers. It is deliberately NOT
	// applied to uploads, downloads, or event streams, where cutting a
	// legitimate long transfer off would be a bug rather than a protection.
	JSONTimeout time.Duration
	// MaxHeaderBytes caps request header size.
	MaxHeaderBytes int
}

// Addr renders the listen address for net.Listen.
func (h HTTP) Addr() string { return fmt.Sprintf("%s:%d", h.Host, h.Port) }

// Database configures the PostgreSQL connection pool. Defaults are chosen for
// a 4 GB Raspberry Pi, not for a datacentre.
type Database struct {
	Host     string
	Port     int
	Name     string
	User     string
	Password Secret
	SSLMode  string

	MaxConns        int
	MinConns        int
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
	// StatementTimeout bounds any single query so a pathological request
	// cannot pin a connection forever.
	StatementTimeout time.Duration
}

// DSN builds a libpq-style connection string. The password is revealed here
// because this value is handed directly to the driver; the result must never
// be logged.
func (d Database) DSN() string {
	esc := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `'`, `\'`)
		return "'" + s + "'"
	}
	parts := []string{
		"host=" + esc(d.Host),
		fmt.Sprintf("port=%d", d.Port),
		"dbname=" + esc(d.Name),
		"user=" + esc(d.User),
		"sslmode=" + esc(d.SSLMode),
		fmt.Sprintf("connect_timeout=%d", int(d.ConnectTimeout.Seconds())),
		"application_name='pifiles'",
	}
	if !d.Password.IsZero() {
		parts = append(parts, "password="+esc(d.Password.Reveal()))
	}
	return strings.Join(parts, " ")
}

// SafeDSN renders the same connection string with the password removed, for
// logs and error messages.
func (d Database) SafeDSN() string {
	c := d
	c.Password = ""
	return c.DSN()
}

// Storage describes where user content lives on disk.
type Storage struct {
	// Root is the data root. It is always an absolute, cleaned path.
	Root string
	// DirMode and FileMode are the permissions applied to created content.
	// Files are never created executable.
	DirMode  uint32
	FileMode uint32
	// MinFreeBytes is the headroom below which uploads are refused, so the
	// database and the OS never run out of space because of user content.
	MinFreeBytes int64
}

// Subdirectory names beneath the storage root.
const (
	DirFiles      = "files"
	DirVersions   = "versions"
	DirTrash      = "trash"
	DirPreviews   = "previews"
	DirTemp       = "temp"
	DirQuarantine = "quarantine"
	DirBackups    = "backups"
)

// Subdirs lists every directory PiFiles expects beneath the storage root.
func (s Storage) Subdirs() []string {
	return []string{DirFiles, DirVersions, DirTrash, DirPreviews, DirTemp, DirQuarantine, DirBackups}
}

// Path joins elements beneath the storage root. Callers are responsible for
// ensuring the elements are trusted; see the storage package for the safe
// content-addressed constructors.
func (s Storage) Path(elem ...string) string {
	return filepath.Join(append([]string{s.Root}, elem...)...)
}

// Uploads configures the chunked upload pipeline.
type Uploads struct {
	MaxSize   int64
	ChunkSize int64
	// SessionTTL is how long an incomplete upload may sit in temp/ before the
	// cleanup job removes it.
	SessionTTL time.Duration
	// DefaultQuota is applied to newly created users. Zero means unlimited.
	DefaultQuota int64
}

// Session configures authentication sessions.
type Session struct {
	Lifetime         time.Duration
	RememberLifetime time.Duration
	// IdleTimeout expires a session that has not been used, independently of
	// its absolute lifetime.
	IdleTimeout  time.Duration
	CookieName   string
	CookieDomain string
	CookieSecure bool
	// MaxPerUser bounds concurrent sessions per account.
	MaxPerUser int
}

// Security holds cross-cutting protective settings.
type Security struct {
	// SecretKey seeds CSRF tokens and other keyed derivations. It is never
	// used to encrypt file content.
	SecretKey Secret
	// TrustedProxies are the CIDRs whose X-Forwarded-For headers we believe.
	// Without this, a client could spoof its source IP and defeat rate limits.
	TrustedProxies []string
	// CORSOrigins is normally empty: the SPA is same-origin.
	CORSOrigins []string

	PasswordMinLength int
	MaxLoginAttempts  int
	LockoutDuration   time.Duration
	RateLimitRequests int
	RateLimitWindow   time.Duration
}

// Sharing holds instance-wide sharing policy.
type Sharing struct {
	Enabled              bool
	PublicLinksEnabled   bool
	UploadRequestEnabled bool
	// RequireLinkPassword forces every public link to carry a password.
	RequireLinkPassword bool
	// MaxLinkLifetime bounds how far in the future an expiry may be set.
	// Zero means no bound.
	MaxLinkLifetime time.Duration
	// DefaultLinkLifetime pre-fills the expiry in the UI. Zero means no expiry.
	DefaultLinkLifetime time.Duration
}

// Trash configures soft-delete retention.
type Trash struct {
	Enabled bool
	// RetentionDays is how long deleted content is kept. Zero disables
	// automatic purging.
	RetentionDays int
}

// Versions configures file version history retention.
type Versions struct {
	Enabled bool
	// MaxPerFile caps stored versions per file. Zero means unlimited.
	MaxPerFile int
	// MaxAge discards versions older than this. Zero means no age limit.
	MaxAge time.Duration
	// MaxTotalSize caps the bytes versions may consume per user. Zero means
	// unlimited.
	MaxTotalSize int64
}

// RateLimits bounds how often one caller may hit the API.
//
// Separate budgets rather than one number, because the operations differ by
// orders of magnitude in cost. Listing a folder is a single indexed query;
// starting an upload allocates a session and a temporary file; a search runs a
// trigram scan across a tree. One limit generous enough for the first would be
// useless against the third.
//
// Every limit is per caller per minute — the signed-in account where there is
// one, the client address otherwise. Zero switches that limit off.
type RateLimits struct {
	Enabled bool
	// Read covers listings, metadata, and downloads.
	Read int
	// Write covers anything that changes state: creating, renaming, moving,
	// deleting, sharing.
	Write int
	// Upload covers starting an upload session and sending chunks. Higher than
	// Write because one large file is legitimately hundreds of chunk requests.
	Upload int
	// Search is its own budget: a trigram scan over a shared tree is the most
	// expensive read the API offers.
	Search int
	// Anonymous covers the public-link portals, which have no account to key
	// on and are the only endpoints a stranger can reach at all.
	Anonymous int
}

// Audit configures the queryable security trail.
type Audit struct {
	// Enabled switches recording on. Off means events are not written at all,
	// which is a legitimate choice for a single-user instance and a poor one
	// for anything shared.
	Enabled bool
	// Retention is how long events are kept. Zero keeps them forever, which is
	// deliberately not read as "delete everything": the pruning sweep refuses
	// to run rather than interpreting an unset value as a policy.
	Retention time.Duration
}

// Malware configures the optional ClamAV integration.
type Malware struct {
	Enabled bool
	// Address is a clamd endpoint, e.g. "clamav:3310" or "unix:/run/clamd.sock".
	Address string
	Timeout time.Duration
	// MaxScanSize skips scanning for files above this size, because clamd on a
	// Pi will otherwise stall the upload pipeline. Skipped files are recorded
	// in the audit log rather than silently passed.
	MaxScanSize int64
	// RejectOnError decides what happens when clamd is unreachable: refuse the
	// upload (safe) or accept it unscanned (available).
	RejectOnError bool
	// Quarantine keeps infected content under quarantine/ instead of
	// discarding it.
	//
	// Off by default, deliberately. Keeping malware on a family file server so
	// somebody can investigate it later is a real risk for value most
	// operators will never collect; the audit trail already records what was
	// blocked, who sent it, and which signature matched. An operator who does
	// want the sample can ask for it.
	Quarantine bool
}

// Jobs configures the PostgreSQL-backed background worker pool.
type Jobs struct {
	Enabled      bool
	Workers      int
	PollInterval time.Duration
	// MaxAttempts before a job is parked as failed.
	MaxAttempts int
}

// Log configures structured logging.
type Log struct {
	// Level is one of debug, info, warning, error.
	Level string
	// Format is "console" or "json".
	Format string
	// RequestLogging emits one structured line per HTTP request.
	RequestLogging bool
}

// Default values, kept in one place so .env.example and the code cannot drift.
const (
	DefaultHTTPPort    = 8080
	DefaultStorageRoot = "/srv/pifiles"
)

// Load reads configuration from the process environment.
func Load() (*Config, error) { return LoadFrom(OSLookup) }

// LoadFrom reads configuration using an arbitrary lookup function.
func LoadFrom(lookup Lookup) (*Config, error) {
	l := &loader{lookup: lookup}

	env := Environment(l.oneOf("APP_ENV", string(EnvProduction),
		string(EnvDevelopment), string(EnvProduction)))
	production := env.IsProduction()

	cfg := &Config{
		App: App{
			Name:                l.str("APP_NAME", "PiFiles"),
			URL:                 strings.TrimRight(l.str("APP_URL", "http://localhost:8080"), "/"),
			Environment:         env,
			RegistrationEnabled: l.boolean("REGISTRATION_ENABLED", false),
		},
		HTTP: HTTP{
			Host:              l.str("HTTP_HOST", "0.0.0.0"),
			Port:              l.integer("HTTP_PORT", DefaultHTTPPort),
			ReadHeaderTimeout: l.duration("HTTP_READ_HEADER_TIMEOUT", 20*time.Second),
			// WriteTimeout must stay zero: a nonzero value would abort long
			// downloads of large files partway through.
			WriteTimeout:    0,
			IdleTimeout:     l.duration("HTTP_IDLE_TIMEOUT", 120*time.Second),
			ShutdownTimeout: l.duration("HTTP_SHUTDOWN_TIMEOUT", 30*time.Second),
			JSONTimeout:     l.duration("HTTP_JSON_TIMEOUT", 30*time.Second),
			MaxHeaderBytes:  int(l.size("HTTP_MAX_HEADER_SIZE", 64*KB)),
		},
		Database: Database{
			Host:     l.str("DATABASE_HOST", "postgres"),
			Port:     l.integer("DATABASE_PORT", 5432),
			Name:     l.str("DATABASE_NAME", "pifiles"),
			User:     l.str("DATABASE_USER", "pifiles"),
			Password: l.secret("DATABASE_PASSWORD"),
			SSLMode: l.oneOf("DATABASE_SSLMODE", "disable",
				"disable", "allow", "prefer", "require", "verify-ca", "verify-full"),
			MaxConns:         l.integer("DATABASE_MAX_CONNS", defaultMaxConns()),
			MinConns:         l.integer("DATABASE_MIN_CONNS", 2),
			MaxConnLifetime:  l.duration("DATABASE_MAX_CONN_LIFETIME", time.Hour),
			MaxConnIdleTime:  l.duration("DATABASE_MAX_CONN_IDLE_TIME", 15*time.Minute),
			ConnectTimeout:   l.duration("DATABASE_CONNECT_TIMEOUT", 10*time.Second),
			StatementTimeout: l.duration("DATABASE_STATEMENT_TIMEOUT", 30*time.Second),
		},
		Storage: Storage{
			Root:         l.str("STORAGE_ROOT", DefaultStorageRoot),
			DirMode:      0o750,
			FileMode:     0o640,
			MinFreeBytes: l.size("STORAGE_MIN_FREE", 1*GB),
		},
		Uploads: Uploads{
			MaxSize:      l.size("MAX_UPLOAD_SIZE", 20*GB),
			ChunkSize:    l.size("UPLOAD_CHUNK_SIZE", 16*MB),
			SessionTTL:   l.duration("UPLOAD_SESSION_TTL", 24*time.Hour),
			DefaultQuota: l.size("DEFAULT_USER_QUOTA", 25*GB),
		},
		Session: Session{
			Lifetime:         l.duration("SESSION_LIFETIME", 24*time.Hour),
			RememberLifetime: l.duration("SESSION_REMEMBER_LIFETIME", 30*24*time.Hour),
			IdleTimeout:      l.duration("SESSION_IDLE_TIMEOUT", 0),
			CookieName:       l.str("SESSION_COOKIE_NAME", "pifiles_session"),
			CookieDomain:     l.str("SESSION_COOKIE_DOMAIN", ""),
			CookieSecure:     l.boolean("SESSION_COOKIE_SECURE", production),
			MaxPerUser:       l.integer("SESSION_MAX_PER_USER", 25),
		},
		Security: Security{
			SecretKey: l.secret("APP_SECRET_KEY"),
			// Docker's default bridge networks plus loopback. Caddy runs
			// inside one of these, so its forwarded headers are trusted.
			TrustedProxies:    l.csv("TRUSTED_PROXIES", []string{"127.0.0.1/32", "::1/128", "172.16.0.0/12", "192.168.0.0/16", "10.0.0.0/8"}),
			CORSOrigins:       l.csv("CORS_ORIGINS", nil),
			PasswordMinLength: l.integer("PASSWORD_MIN_LENGTH", 12),
			MaxLoginAttempts:  l.integer("MAX_LOGIN_ATTEMPTS", 10),
			LockoutDuration:   l.duration("LOCKOUT_DURATION", 15*time.Minute),
			RateLimitRequests: l.integer("RATE_LIMIT_REQUESTS", 300),
			RateLimitWindow:   l.duration("RATE_LIMIT_WINDOW", time.Minute),
		},
		Sharing: Sharing{
			Enabled:              l.boolean("SHARING_ENABLED", true),
			PublicLinksEnabled:   l.boolean("PUBLIC_LINKS_ENABLED", true),
			UploadRequestEnabled: l.boolean("UPLOAD_REQUESTS_ENABLED", true),
			RequireLinkPassword:  l.boolean("PUBLIC_LINK_REQUIRE_PASSWORD", false),
			MaxLinkLifetime:      l.duration("PUBLIC_LINK_MAX_LIFETIME", 0),
			DefaultLinkLifetime:  l.duration("PUBLIC_LINK_DEFAULT_LIFETIME", 0),
		},
		Trash: Trash{
			Enabled:       l.boolean("TRASH_ENABLED", true),
			RetentionDays: l.integer("TRASH_RETENTION_DAYS", 30),
		},
		Versions: Versions{
			Enabled:      l.boolean("VERSIONING_ENABLED", true),
			MaxPerFile:   l.integer("VERSIONS_MAX_PER_FILE", 20),
			MaxAge:       l.duration("VERSIONS_MAX_AGE", 90*24*time.Hour),
			MaxTotalSize: l.size("VERSIONS_MAX_TOTAL_SIZE", 0),
		},
		Malware: Malware{
			Enabled:       l.boolean("MALWARE_SCANNING_ENABLED", false),
			Address:       l.str("CLAMAV_ADDRESS", "clamav:3310"),
			Timeout:       l.duration("CLAMAV_TIMEOUT", 2*time.Minute),
			MaxScanSize:   l.size("CLAMAV_MAX_SCAN_SIZE", 256*MB),
			RejectOnError: l.boolean("CLAMAV_REJECT_ON_ERROR", true),
			Quarantine:    l.boolean("CLAMAV_QUARANTINE", false),
		},
		Limits: RateLimits{
			Enabled:   l.boolean("RATE_LIMIT_ENABLED", true),
			Read:      l.integer("RATE_LIMIT_READ", 600),
			Write:     l.integer("RATE_LIMIT_WRITE", 120),
			Upload:    l.integer("RATE_LIMIT_UPLOAD", 1200),
			Search:    l.integer("RATE_LIMIT_SEARCH", 60),
			Anonymous: l.integer("RATE_LIMIT_ANONYMOUS", 60),
		},
		Audit: Audit{
			Enabled:   l.boolean("AUDIT_ENABLED", true),
			Retention: l.duration("AUDIT_RETENTION", 365*24*time.Hour),
		},
		Jobs: Jobs{
			Enabled:      l.boolean("JOBS_ENABLED", true),
			Workers:      l.integer("JOBS_WORKERS", defaultJobWorkers()),
			PollInterval: l.duration("JOBS_POLL_INTERVAL", 5*time.Second),
			MaxAttempts:  l.integer("JOBS_MAX_ATTEMPTS", 5),
		},
		Log: Log{
			Level:          l.oneOf("LOG_LEVEL", "info", "debug", "info", "warning", "warn", "error"),
			Format:         l.oneOf("LOG_FORMAT", defaultLogFormat(production), "console", "json"),
			RequestLogging: l.boolean("LOG_REQUESTS", true),
		},
	}

	cfg.validate(l, production)

	if err := l.err(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate(l *loader, production bool) {
	// --- App -------------------------------------------------------------
	l.require(c.App.Name != "", "APP_NAME", "must not be empty")
	if u, err := url.Parse(c.App.URL); err != nil || u.Scheme == "" || u.Host == "" {
		l.fail("APP_URL", fmt.Errorf("%q is not an absolute URL (expected e.g. https://files.example.com)", c.App.URL))
	} else if production && u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
		l.fail("APP_URL", fmt.Errorf("must use https in production (got %q)", c.App.URL))
	}

	// --- HTTP ------------------------------------------------------------
	l.require(c.HTTP.Port > 0 && c.HTTP.Port <= 65535, "HTTP_PORT", "must be between 1 and 65535")
	l.require(c.HTTP.ReadHeaderTimeout > 0, "HTTP_READ_HEADER_TIMEOUT", "must be greater than zero")
	l.require(c.HTTP.MaxHeaderBytes >= 4*int(KB), "HTTP_MAX_HEADER_SIZE", "must be at least 4KB")
	l.require(c.HTTP.JSONTimeout > 0, "HTTP_JSON_TIMEOUT", "must be greater than zero")

	// --- Database --------------------------------------------------------
	l.require(c.Database.Host != "", "DATABASE_HOST", "must not be empty")
	l.require(c.Database.Name != "", "DATABASE_NAME", "must not be empty")
	l.require(c.Database.User != "", "DATABASE_USER", "must not be empty")
	l.require(c.Database.Port > 0 && c.Database.Port <= 65535, "DATABASE_PORT", "must be between 1 and 65535")
	l.require(c.Database.MaxConns >= 1, "DATABASE_MAX_CONNS", "must be at least 1")
	l.require(c.Database.MaxConns <= 100, "DATABASE_MAX_CONNS",
		"must be at most 100; a Raspberry Pi cannot usefully service more")
	l.require(c.Database.MinConns >= 0 && c.Database.MinConns <= c.Database.MaxConns,
		"DATABASE_MIN_CONNS", "must be between 0 and DATABASE_MAX_CONNS")
	l.require(c.Database.ConnectTimeout >= time.Second, "DATABASE_CONNECT_TIMEOUT", "must be at least 1s")
	if production {
		l.require(!c.Database.Password.IsZero(), "DATABASE_PASSWORD",
			"is required in production; run scripts/generate-secrets.sh or use the installer")
	}

	// --- Storage ---------------------------------------------------------
	if c.Storage.Root == "" {
		l.fail("STORAGE_ROOT", fmt.Errorf("must not be empty"))
	} else if !filepath.IsAbs(c.Storage.Root) {
		l.fail("STORAGE_ROOT", fmt.Errorf("%q must be an absolute path", c.Storage.Root))
	} else {
		c.Storage.Root = filepath.Clean(c.Storage.Root)
		if c.Storage.Root == "/" {
			l.fail("STORAGE_ROOT", fmt.Errorf("must not be the filesystem root"))
		}
	}
	l.require(c.Storage.MinFreeBytes >= 0, "STORAGE_MIN_FREE", "must not be negative")

	// --- Uploads ---------------------------------------------------------
	l.require(c.Uploads.MaxSize > 0, "MAX_UPLOAD_SIZE", "must be greater than zero")
	l.require(c.Uploads.ChunkSize >= 256*KB, "UPLOAD_CHUNK_SIZE", "must be at least 256KB")
	l.require(c.Uploads.ChunkSize <= 256*MB, "UPLOAD_CHUNK_SIZE",
		"must be at most 256MB; larger chunks risk exhausting memory on a Raspberry Pi")
	if c.Uploads.MaxSize > 0 && c.Uploads.ChunkSize > c.Uploads.MaxSize {
		l.fail("UPLOAD_CHUNK_SIZE", fmt.Errorf("must not exceed MAX_UPLOAD_SIZE (%s)", FormatSize(c.Uploads.MaxSize)))
	}
	l.require(c.Uploads.DefaultQuota >= 0, "DEFAULT_USER_QUOTA", "must not be negative (use 0 for unlimited)")

	// --- Session ---------------------------------------------------------
	l.require(c.Session.Lifetime >= time.Minute, "SESSION_LIFETIME", "must be at least 1m")
	l.require(c.Session.RememberLifetime >= c.Session.Lifetime, "SESSION_REMEMBER_LIFETIME",
		"must be at least SESSION_LIFETIME")
	l.require(c.Session.CookieName != "", "SESSION_COOKIE_NAME", "must not be empty")
	l.require(c.Session.MaxPerUser >= 1, "SESSION_MAX_PER_USER", "must be at least 1")

	// --- Security --------------------------------------------------------
	const minSecretLen = 32
	if production {
		switch {
		case c.Security.SecretKey.IsZero():
			l.fail("APP_SECRET_KEY", fmt.Errorf(
				"is required in production; generate one with: openssl rand -base64 48"))
		case len(c.Security.SecretKey.Reveal()) < minSecretLen:
			l.fail("APP_SECRET_KEY", fmt.Errorf("must be at least %d characters", minSecretLen))
		case isPlaceholderSecret(c.Security.SecretKey.Reveal()):
			l.fail("APP_SECRET_KEY", fmt.Errorf(
				"still contains an example placeholder; generate a real one with: openssl rand -base64 48"))
		}
	}
	l.require(c.Security.PasswordMinLength >= 8, "PASSWORD_MIN_LENGTH",
		"must be at least 8; shorter passwords cannot be made safe")
	l.require(c.Security.MaxLoginAttempts >= 1, "MAX_LOGIN_ATTEMPTS", "must be at least 1")
	l.require(c.Security.RateLimitRequests >= 1, "RATE_LIMIT_REQUESTS", "must be at least 1")
	l.require(c.Security.RateLimitWindow > 0, "RATE_LIMIT_WINDOW", "must be greater than zero")

	// --- Trash / Versions ------------------------------------------------
	l.require(c.Trash.RetentionDays >= 0, "TRASH_RETENTION_DAYS", "must not be negative (use 0 to keep forever)")
	l.require(c.Versions.MaxPerFile >= 0, "VERSIONS_MAX_PER_FILE", "must not be negative (use 0 for unlimited)")
	l.require(c.Versions.MaxTotalSize >= 0, "VERSIONS_MAX_TOTAL_SIZE", "must not be negative")

	// --- Malware ---------------------------------------------------------
	if c.Malware.Enabled {
		l.require(c.Malware.Address != "", "CLAMAV_ADDRESS", "is required when MALWARE_SCANNING_ENABLED=true")
		l.require(c.Malware.Timeout > 0, "CLAMAV_TIMEOUT", "must be greater than zero")
	}

	// --- Rate limits -----------------------------------------------------
	for _, limit := range []struct {
		name  string
		value int
	}{
		{"RATE_LIMIT_READ", c.Limits.Read},
		{"RATE_LIMIT_WRITE", c.Limits.Write},
		{"RATE_LIMIT_UPLOAD", c.Limits.Upload},
		{"RATE_LIMIT_SEARCH", c.Limits.Search},
		{"RATE_LIMIT_ANONYMOUS", c.Limits.Anonymous},
	} {
		l.require(limit.value >= 0, limit.name, "must not be negative (use 0 to switch this limit off)")
	}

	// --- Audit -----------------------------------------------------------
	l.require(c.Audit.Retention >= 0, "AUDIT_RETENTION",
		"must not be negative (use 0 to keep events forever)")

	// --- Jobs ------------------------------------------------------------
	l.require(c.Jobs.Workers >= 1, "JOBS_WORKERS", "must be at least 1")
	l.require(c.Jobs.Workers <= 16, "JOBS_WORKERS",
		"must be at most 16; more workers will starve interactive requests on a Raspberry Pi")
	l.require(c.Jobs.PollInterval >= time.Second, "JOBS_POLL_INTERVAL",
		"must be at least 1s to avoid needless database load")
	l.require(c.Jobs.MaxAttempts >= 1, "JOBS_MAX_ATTEMPTS", "must be at least 1")
}

// Normalize returns the canonical slog level string.
func (c Config) LogLevel() string {
	if c.Log.Level == "warn" {
		return "warning"
	}
	return c.Log.Level
}

// PublicLinkURL builds an absolute share URL for a token.
func (c Config) PublicLinkURL(token string) string { return c.App.URL + "/s/" + token }

// UploadRequestURL builds an absolute upload-portal URL for a token.
func (c Config) UploadRequestURL(token string) string { return c.App.URL + "/u/" + token }

func defaultMaxConns() int {
	// Two per core keeps the pool responsive without overwhelming a Pi's
	// PostgreSQL, which is configured for max_connections=50.
	n := runtime.NumCPU() * 2
	if n < 4 {
		n = 4
	}
	if n > 10 {
		n = 10
	}
	return n
}

func defaultJobWorkers() int {
	// Background work must never consume the whole board.
	if runtime.NumCPU() <= 2 {
		return 1
	}
	return 2
}

func defaultLogFormat(production bool) string {
	if production {
		return "json"
	}
	return "console"
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// isPlaceholderSecret catches the case where somebody copied .env.example
// verbatim and never ran the generator.
func isPlaceholderSecret(s string) bool {
	lowered := strings.ToLower(s)
	for _, marker := range []string{"changeme", "change_me", "replace-me", "replaceme", "example", "placeholder", "generate", "xxxxxx"} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}
