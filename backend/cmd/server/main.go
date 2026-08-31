// Command server runs the PiFiles application: the REST API, the embedded
// single-page application, and (from Phase 7) the background job runner.
//
// It is designed to be supervised — by Docker, by systemd, or by a developer's
// terminal — so it fails fast and loudly on misconfiguration, and shuts down
// gracefully on SIGTERM so in-flight uploads and downloads are not truncated.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
	"github.com/joshuanmoses/piCloud/backend/internal/maintenance"
	"github.com/joshuanmoses/piCloud/backend/internal/malware"
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
	"github.com/joshuanmoses/piCloud/backend/internal/version"
	"github.com/joshuanmoses/piCloud/backend/internal/versions"
	"github.com/joshuanmoses/piCloud/backend/internal/web"
	"github.com/joshuanmoses/piCloud/backend/migrations"
)

func main() {
	var (
		showVersion = flag.Bool("version", false, "print version information and exit")
		checkConfig = flag.Bool("check-config", false, "validate configuration and exit")
		migrateOnly = flag.Bool("migrate", false, "apply database migrations and exit")
		skipMigrate = flag.Bool("skip-migrate", false, "start without applying migrations")
		makeAdmin   = flag.Bool("create-admin", false, "create an administrator account interactively and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Current().String())
		return
	}

	if err := run(runOptions{
		checkConfigOnly: *checkConfig,
		migrateOnly:     *migrateOnly,
		skipMigrate:     *skipMigrate,
		createAdmin:     *makeAdmin,
	}); err != nil {
		// Configuration errors are the ones a nontechnical operator will
		// actually hit, so they are printed plainly rather than as a log line
		// buried in JSON.
		fmt.Fprintf(os.Stderr, "\npifiles: startup failed\n\n%v\n\n", err)
		if config.IsInvalid(err) {
			fmt.Fprintf(os.Stderr,
				"Check your .env file against .env.example, then try again.\n"+
					"Documentation: docs/installation.md\n\n")
		}
		os.Exit(1)
	}
}

type runOptions struct {
	checkConfigOnly bool
	migrateOnly     bool
	skipMigrate     bool
	createAdmin     bool
}

func run(opts runOptions) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := logging.New(logging.Options{
		Level:     cfg.LogLevel(),
		Format:    cfg.Log.Format,
		AddSource: cfg.LogLevel() == "debug",
	})
	slog.SetDefault(logger)

	if opts.checkConfigOnly {
		fmt.Printf("Configuration is valid.\n\n")
		fmt.Printf("  environment   %s\n", cfg.App.Environment)
		fmt.Printf("  app url       %s\n", cfg.App.URL)
		fmt.Printf("  listen        %s\n", cfg.HTTP.Addr())
		fmt.Printf("  storage root  %s\n", cfg.Storage.Root)
		fmt.Printf("  database      %s:%d/%s\n", cfg.Database.Host, cfg.Database.Port, cfg.Database.Name)
		fmt.Printf("  max upload    %s\n", config.FormatSize(cfg.Uploads.MaxSize))
		fmt.Printf("  chunk size    %s\n", config.FormatSize(cfg.Uploads.ChunkSize))
		return nil
	}

	info := version.Current()
	logger.Info("starting PiFiles",
		"version", info.Version,
		"commit", info.Commit,
		"go", info.Go,
		"platform", info.Platform,
		"environment", string(cfg.App.Environment),
		"pid", os.Getpid(),
	)
	warnAboutDevelopmentPosture(logger, cfg)
	warnAboutProxyTrust(logger, cfg)

	// Signal handling is installed before any dependency is opened so that a
	// Ctrl-C during a slow database wait is honoured.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// --- Storage ---------------------------------------------------------
	store, err := storage.Open(cfg.Storage, logger)
	if err != nil {
		return fmt.Errorf("%w\n\nEnsure the storage volume is mounted and writable by uid %d.\nSee docs/storage.md",
			err, os.Getuid())
	}
	defer store.Close()

	// --- Database --------------------------------------------------------
	db, err := database.Connect(ctx, cfg.Database, logger)
	if err != nil {
		return err
	}
	defer db.Close()

	// --- Migrations ------------------------------------------------------
	if opts.skipMigrate {
		logger.Warn("skipping migrations at operator request")
	} else {
		result, err := db.Migrate(ctx, migrations.FS)
		if err != nil {
			return err
		}
		if opts.migrateOnly {
			if result.AlreadyCurrent {
				fmt.Printf("Database schema is already current (version %d).\n", result.SchemaVersion)
			} else {
				fmt.Printf("Applied %d migration(s); schema is now at version %d.\n",
					len(result.Applied), result.SchemaVersion)
			}
			return nil
		}
	}
	if opts.migrateOnly {
		return nil
	}

	// --- Domain services -------------------------------------------------
	userStore := users.NewStore(db)
	sessionStore := sessions.NewStore(db, sessions.Config{
		Lifetime:         cfg.Session.Lifetime,
		RememberLifetime: cfg.Session.RememberLifetime,
		IdleTimeout:      cfg.Session.IdleTimeout,
		MaxPerUser:       cfg.Session.MaxPerUser,
	})
	authService := auth.NewService(userStore, sessionStore, auth.NewDefaultHasher(), auth.Config{
		MaxLoginAttempts: cfg.Security.MaxLoginAttempts,
		LockoutDuration:  cfg.Security.LockoutDuration,
		PasswordPolicy:   auth.NewPasswordPolicy(cfg.Security.PasswordMinLength),
		PerIPLimit:       cfg.Security.MaxLoginAttempts * 2,
		PerIPWindow:      cfg.Security.LockoutDuration,
	}, logger)

	nodeStore := nodes.NewStore(db)
	groupStore := groups.NewStore(db)
	permissionStore := permissions.NewStore(db)
	sharingService := sharing.NewService(
		db, permissionStore, nodeStore, userStore, groupStore, cfg.Sharing.Enabled, logger)
	publicLinkService := publiclinks.NewService(
		publiclinks.NewStore(db), nodeStore, permissionStore, auth.NewDefaultHasher(),
		cfg.Security.SecretKey.Reveal(),
		publiclinks.Config{
			Enabled:               cfg.Sharing.Enabled && cfg.Sharing.PublicLinksEnabled,
			UploadRequestsEnabled: cfg.Sharing.UploadRequestEnabled,
			RequirePassword:       cfg.Sharing.RequireLinkPassword,
			MaxLifetime:           cfg.Sharing.MaxLinkLifetime,
			DefaultLifetime:       cfg.Sharing.DefaultLinkLifetime,
		}, logger)
	uploadStore := files.NewUploadStore(db, store, cfg.Uploads.SessionTTL, logger)
	fileService := files.NewService(nodeStore, userStore, uploadStore, store, files.Config{
		MaxUploadSize: cfg.Uploads.MaxSize,
		ChunkSize:     cfg.Uploads.ChunkSize,
		UploadTTL:     cfg.Uploads.SessionTTL,
	}, logger)

	// --- Phase 7: productivity and background work ------------------------
	favoriteStore := favorites.NewStore(db)
	recentStore := recent.NewStore(db)
	tagStore := tags.NewStore(db)
	searchBackend := search.NewPostgres(db)
	archiveBuilder := archive.NewBuilder(nodeStore, permissionStore, store, logger)

	jobStore := jobs.NewStoreWithAttempts(db, cfg.Jobs.MaxAttempts)
	previewService := previews.NewService(previews.NewStore(db), nodeStore, store, jobStore, logger)

	// --- Phase 9: the audit trail -----------------------------------------
	// Nil when switched off, and every call site tolerates that: an instance
	// without a trail behaves exactly as it did before Phase 9.
	var auditStore *audit.Store
	if cfg.Audit.Enabled {
		auditStore = audit.NewStore(db, logger)
		logger.Info("audit trail enabled", "retention", describeRetention(cfg.Audit.Retention))
	} else {
		logger.Warn("the audit trail is switched off; security events will only reach the log")
	}

	// --- Phase 8: version history -----------------------------------------
	// The node store calls back into this when content is replaced, so the
	// snapshot and the replacement commit together or not at all.
	versionService := versions.NewService(
		versions.NewStore(db), nodeStore, store,
		versions.Config{
			Enabled: cfg.Versions.Enabled,
			Retention: versions.Retention{
				MaxPerFile:    cfg.Versions.MaxPerFile,
				MaxAge:        cfg.Versions.MaxAge,
				MaxTotalBytes: cfg.Versions.MaxTotalSize,
			},
		}, logger)

	if cfg.Versions.Enabled {
		nodeStore.SetVersioner(versionService)
		logger.Info("version history enabled",
			"max_per_file", cfg.Versions.MaxPerFile,
			"max_age", cfg.Versions.MaxAge.String(),
			"max_total_size", config.FormatSize(cfg.Versions.MaxTotalSize))
	} else {
		logger.Info("version history is switched off; replaced content is not kept")
	}

	// --- Phase 10: malware scanning ---------------------------------------
	// Off by default. ClamAV needs about a gigabyte of RAM for its signature
	// database — a quarter of a Pi 4's memory — so an operator should spend it
	// knowingly rather than discover it has eaten their headroom.
	if cfg.Malware.Enabled {
		scanner := malware.NewScanner(malware.Config{
			Enabled:       true,
			Address:       cfg.Malware.Address,
			Timeout:       cfg.Malware.Timeout,
			MaxScanSize:   cfg.Malware.MaxScanSize,
			RejectOnError: cfg.Malware.RejectOnError,
		}, logger)
		fileService.SetScanner(scanner, cfg.Malware.Quarantine)

		// Probed once at startup so a misconfigured address is a loud warning
		// now rather than a failed upload later. Not fatal: clamd routinely
		// takes a minute to load its database, and refusing to start would
		// make PiFiles wait on it.
		pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := scanner.Ping(pingCtx); err != nil {
			logger.Warn("the malware scanner did not answer at startup",
				"address", cfg.Malware.Address, "error", err,
				"effect", uploadsWhenScannerDown(cfg.Malware.RejectOnError))
		} else {
			logger.Info("malware scanning enabled",
				"address", cfg.Malware.Address,
				"max_scan_size", config.FormatSize(cfg.Malware.MaxScanSize),
				"on_scanner_error", uploadsWhenScannerDown(cfg.Malware.RejectOnError),
				"quarantine", cfg.Malware.Quarantine)
		}
		cancel()
	}

	if opts.createAdmin {
		return createAdmin(ctx, cfg, db)
	}

	warnIfSetupIncomplete(ctx, logger, userStore, cfg)

	// --- Frontend --------------------------------------------------------
	static, err := web.NewHandler()
	if err != nil {
		return err
	}
	if !static.Built() {
		logger.Warn("frontend assets are a placeholder; the web interface will not work",
			"hint", "run 'make build-frontend' or build the Docker image")
	} else {
		logger.Info("frontend assets loaded", "files", static.AssetCount())
	}

	// --- HTTP ------------------------------------------------------------
	handler, err := api.NewServer(api.Deps{
		Config:        cfg,
		DB:            db,
		Storage:       store,
		Logger:        logger,
		StaticHandler: static,
		Users:         userStore,
		Sessions:      sessionStore,
		Auth:          authService,
		Files:         fileService,
		Sharing:       sharingService,
		Groups:        groupStore,
		PublicLinks:   publicLinkService,

		Search:      searchBackend,
		Favorites:   favoriteStore,
		Recent:      recentStore,
		Tags:        tagStore,
		Previews:    previewService,
		Archives:    archiveBuilder,
		Jobs:        jobStore,
		BlobStorage: store,
		Versions:    versionService,
		Audit:       auditStore,
	})
	if err != nil {
		return err
	}

	// --- Background work --------------------------------------------------
	// Started before the listener so a restart begins draining the queue
	// immediately, and stopped after it so a job that a request enqueued in
	// its last moments is not lost.
	var (
		runner    *jobs.Runner
		scheduler *maintenance.Scheduler
	)
	if cfg.Jobs.Enabled {
		runner = jobs.NewRunner(jobStore, jobs.Config{
			Workers:      cfg.Jobs.Workers,
			PollInterval: cfg.Jobs.PollInterval,
		}, logger)

		previewService.RegisterHandler(runner)

		housekeeping := maintenance.New(maintenance.Deps{
			Config:      cfg,
			Logger:      logger,
			Nodes:       nodeStore,
			Files:       fileService,
			Sessions:    sessionStore,
			Permissions: permissionStore,
			PublicLinks: publiclinks.NewStore(db),
			Recent:      recentStore,
			Previews:    previewService,
			Versions:    versionService,
			Audit:       auditStore,
			Storage:     store,
			Queue:       jobStore,
		})
		housekeeping.Register(runner)

		runner.Start(ctx)
		defer runner.Stop()

		scheduler = maintenance.NewScheduler(housekeeping)
		scheduler.Start(ctx)
		defer scheduler.Stop()

		logger.Info("background workers started",
			"workers", cfg.Jobs.Workers, "kinds", len(runner.Kinds()))
	} else {
		logger.Warn("background jobs are disabled; thumbnails will not be generated " +
			"and expired content will not be cleaned up")
	}

	srv := &http.Server{
		Addr:              cfg.HTTP.Addr(),
		Handler:           handler,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		// WriteTimeout stays zero on purpose: a nonzero value would abort a
		// large download or a long upload partway through. Slow-client
		// protection comes from ReadHeaderTimeout and IdleTimeout instead.
		WriteTimeout:   cfg.HTTP.WriteTimeout,
		IdleTimeout:    cfg.HTTP.IdleTimeout,
		MaxHeaderBytes: cfg.HTTP.MaxHeaderBytes,
		BaseContext:    func(_ net.Listener) context.Context { return ctx },
		ErrorLog:       slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.HTTP.Addr(), "url", cfg.App.URL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("http server failed: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutdown signal received", "grace_period", cfg.HTTP.ShutdownTimeout.String())
	}

	// Graceful shutdown: stop accepting new connections, let in-flight
	// requests finish. Truncating an upload at the last chunk would lose the
	// user's file.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.HTTP.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown timed out; forcing close",
			"error", err, "timeout", cfg.HTTP.ShutdownTimeout.String())
		if closeErr := srv.Close(); closeErr != nil {
			logger.Error("forced close failed", "error", closeErr)
		}
	}

	logger.Info("stopped cleanly")
	return nil
}

// warnAboutDevelopmentPosture makes an insecure configuration impossible to
// miss. These are warnings rather than errors because they are legitimate for
// local development; config.Load already refuses them in production.
func warnAboutDevelopmentPosture(logger *slog.Logger, cfg *config.Config) {
	if cfg.App.Environment.IsProduction() {
		return
	}
	logger.Warn("running in development mode; production security requirements are relaxed",
		"app_env", string(cfg.App.Environment))
	if cfg.Security.SecretKey.IsZero() {
		logger.Warn("APP_SECRET_KEY is unset; sessions will not survive a restart")
	}
	if !cfg.Session.CookieSecure {
		logger.Warn("session cookies are not marked Secure; do not use this configuration over a network")
	}
}

// warnAboutProxyTrust flags a configuration where forged headers would be
// believed.
//
// TRUSTED_PROXIES defaults to the private ranges, which is right behind Docker:
// Caddy reaches the app from the compose network, and its X-Forwarded-For is
// the only source of the real client address. But the same default is wrong if
// the operator publishes the application port directly, because then *any*
// caller on the LAN is inside a trusted range and can set X-Forwarded-For to
// whatever it likes — defeating per-IP rate limiting and account lockout, and
// forging the source address recorded in the audit trail.
//
// The application cannot tell which topology it is in from inside the process,
// so this states the condition and what it means rather than guessing.
func warnAboutProxyTrust(logger *slog.Logger, cfg *config.Config) {
	if len(cfg.Security.TrustedProxies) == 0 {
		return
	}

	// Binding to a loopback address means nothing outside the machine can
	// reach the listener directly, so the question does not arise.
	host := cfg.HTTP.Host
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return
	}

	logger.Warn("forwarded client-address headers are trusted from these networks",
		"trusted_proxies", strings.Join(cfg.Security.TrustedProxies, ", "),
		"listening_on", cfg.HTTP.Addr(),
		"why_this_matters", "a caller inside a trusted range can set X-Forwarded-For freely, "+
			"which defeats per-IP rate limiting and forges the address in the audit trail",
		"correct_if", "this port is reachable only by the reverse proxy",
		"wrong_if", "this port is published directly to a network users are on — "+
			"set TRUSTED_PROXIES to the proxy's address alone, or to nothing at all")
}

// warnIfSetupIncomplete makes an unconfigured instance impossible to miss.
//
// Between first start and first sign-in, the setup endpoint is open to whoever
// reaches it. That window is unavoidable — the instance must be reachable
// before an administrator can exist — so the operator is told about it in the
// clearest terms available, on every start, until it is closed.
func warnIfSetupIncomplete(ctx context.Context, logger *slog.Logger, store *users.Store, cfg *config.Config) {
	count, err := store.Count(ctx)
	if err != nil {
		logger.Warn("could not determine whether setup is complete", "error", err)
		return
	}
	if count > 0 {
		return
	}

	logger.Warn("SETUP IS NOT COMPLETE — this instance has no accounts yet",
		"action", "open "+cfg.App.URL+" and create the first administrator",
		"alternative", "docker compose exec app pifiles -create-admin",
		"note", "until an administrator exists, anyone who can reach this server can claim it")
}

// describeRetention renders a retention window for the startup log, where
// "forever" is clearer than "0s".
func describeRetention(d time.Duration) string {
	if d <= 0 {
		return "forever"
	}
	return d.String()
}

// uploadsWhenScannerDown renders the configured behaviour in words, because
// "reject_on_error=true" tells an operator less at 2am than the sentence does.
func uploadsWhenScannerDown(reject bool) string {
	if reject {
		return "uploads are refused"
	}
	return "uploads are accepted unscanned"
}
