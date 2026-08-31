// Package api wires HTTP transport to the application's services.
//
// It is the only package that knows about HTTP. Domain packages return values
// and errors; this package decides status codes, headers, and serialization.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/joshuanmoses/piCloud/backend/internal/archive"
	"github.com/joshuanmoses/piCloud/backend/internal/audit"
	"github.com/joshuanmoses/piCloud/backend/internal/auth"
	"github.com/joshuanmoses/piCloud/backend/internal/config"
	"github.com/joshuanmoses/piCloud/backend/internal/database"
	"github.com/joshuanmoses/piCloud/backend/internal/favorites"
	"github.com/joshuanmoses/piCloud/backend/internal/files"
	"github.com/joshuanmoses/piCloud/backend/internal/groups"
	"github.com/joshuanmoses/piCloud/backend/internal/jobs"
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
)

// APIPrefix is the versioned API mount point. Every JSON endpoint lives beneath
// it, which is what lets the SPA fallback distinguish "unknown page" from
// "unknown endpoint".
const APIPrefix = "/api/v1"

// maxJSONBody bounds JSON request bodies. File content never travels this path;
// it is streamed through the upload endpoints, which have their own limits.
const maxJSONBody = 1 << 20 // 1 MiB

// DatabaseService is the database surface the transport layer depends on.
//
// The API package deliberately depends on a narrow interface rather than
// *database.DB. Domain services in later phases receive the concrete pool;
// what the router itself needs is only the health probe, and stating that
// explicitly keeps handler tests free of a live PostgreSQL.
type DatabaseService interface {
	Check(ctx context.Context) database.Health
}

// StorageService is the storage surface the transport layer depends on.
type StorageService interface {
	Check() storage.Health
}

// Deps are the collaborators a Server needs.
type Deps struct {
	Config  *config.Config
	DB      DatabaseService
	Storage StorageService
	Logger  *slog.Logger

	// Users, Sessions, and Auth are required for the authenticated routes.
	// They are nil in the transport-only tests that exercise health and
	// error handling, in which case those routes are not registered.
	Users    *users.Store
	Sessions *sessions.Store
	Auth     *auth.Service
	// Files is required for the content routes; nil leaves them unregistered.
	Files *files.Service
	// Sharing carries the permission evaluator every content handler uses,
	// so it is required alongside Files.
	Sharing *sharing.Service
	Groups  *groups.Store
	// PublicLinks enables the /s and /u portals; nil leaves them unregistered.
	PublicLinks *publiclinks.Service
	// StaticHandler serves the embedded single-page application. When nil, the
	// server runs API-only, which is how the backend tests exercise it.
	StaticHandler http.Handler

	// --- Phase 7: productivity -------------------------------------------
	// Each is independently optional. A deployment that supplies none of them
	// runs exactly as it did before, with the corresponding routes simply not
	// registered — which is also how the transport-only tests keep working.
	Search    search.Backend
	Favorites *favorites.Store
	Recent    *recent.Store
	Tags      *tags.Store
	Previews  *previews.Service
	Archives  *archive.Builder
	// Jobs is exposed so the administrator's job view can read queue depth.
	Jobs *jobs.Store
	// Versions enables file history; nil leaves those routes unregistered.
	Versions *versions.Service
	// Audit records security-relevant events. Nil switches recording off and
	// leaves the audit routes unregistered; every call site tolerates that,
	// so a deployment without it behaves exactly as it did before Phase 9.
	Audit *audit.Store
	// BlobStorage is the concrete storage handle, needed to stream a preview.
	// It is separate from Storage above, which is the narrow health interface
	// the transport-only tests supply.
	BlobStorage *storage.Storage
}

// Server holds the router and its dependencies.
type Server struct {
	cfg       *config.Config
	db        DatabaseService
	storage   StorageService
	logger    *slog.Logger
	router    chi.Router
	startedAt time.Time
	health    *healthCache
	static    http.Handler

	userStore    *users.Store
	sessionStore *sessions.Store
	authService  *auth.Service
	csrf         *auth.CSRFTokenizer
	files        *files.Service
	sharing      *sharing.Service
	groups       *groups.Store
	publicLinks  *publiclinks.Service

	search    search.Backend
	favorites *favorites.Store
	recent    *recent.Store
	tags      *tags.Store
	previews  *previews.Service
	archives  *archive.Builder
	jobs      *jobs.Store
	blobs     *storage.Storage
	versions  *versions.Service
	auditLog  *audit.Store
	limits    *rateLimiters
	// stop closes when the server is shut down, so the limiter's eviction
	// goroutine ends with it rather than outliving the process's usefulness.
	stop chan struct{}
}

// authEnabled reports whether the authentication dependencies were supplied.
func (s *Server) authEnabled() bool {
	return s.userStore != nil && s.sessionStore != nil && s.authService != nil
}

// publicLinksEnabled reports whether the portal dependencies were supplied.
func (s *Server) publicLinksEnabled() bool { return s.publicLinks != nil && s.filesEnabled() }

// filesEnabled reports whether the content dependencies were supplied.
//
// The sharing service is part of this set rather than optional: every content
// handler evaluates permissions through it, so content routes without it would
// have no authorization at all.
func (s *Server) filesEnabled() bool {
	return s.files != nil && s.sharing != nil && s.authEnabled()
}

// NewServer builds the router and returns a ready http.Handler.
func NewServer(deps Deps) (*Server, error) {
	if deps.Config == nil {
		return nil, fmt.Errorf("api: config is required")
	}
	if deps.DB == nil {
		return nil, fmt.Errorf("api: database is required")
	}
	if deps.Storage == nil {
		return nil, fmt.Errorf("api: storage is required")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	s := &Server{
		cfg:       deps.Config,
		db:        deps.DB,
		storage:   deps.Storage,
		logger:    logger,
		startedAt: time.Now(),
		health:    &healthCache{ttl: 5 * time.Second},
		static:    deps.StaticHandler,

		userStore:    deps.Users,
		sessionStore: deps.Sessions,
		authService:  deps.Auth,
		files:        deps.Files,
		sharing:      deps.Sharing,
		groups:       deps.Groups,
		publicLinks:  deps.PublicLinks,
		csrf:         auth.NewCSRFTokenizer(deps.Config.Security.SecretKey.Reveal()),

		search:    deps.Search,
		favorites: deps.Favorites,
		recent:    deps.Recent,
		tags:      deps.Tags,
		previews:  deps.Previews,
		archives:  deps.Archives,
		jobs:      deps.Jobs,
		blobs:     deps.BlobStorage,
		versions:  deps.Versions,
		auditLog:  deps.Audit,
		stop:      make(chan struct{}),
	}

	s.limits = newRateLimiters(RateLimitConfig{
		Enabled:   deps.Config.Limits.Enabled,
		Read:      deps.Config.Limits.Read,
		Write:     deps.Config.Limits.Write,
		Upload:    deps.Config.Limits.Upload,
		Search:    deps.Config.Limits.Search,
		Anonymous: deps.Config.Limits.Anonymous,
	})
	s.limits.startCleanup(s.stop)

	if err := s.buildRoutes(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close releases what the server owns beyond its handlers.
//
// Only the rate limiter's eviction goroutine today. Safe to call more than
// once: a double shutdown must not panic on a closed channel.
func (s *Server) Close() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.router.ServeHTTP(w, r) }

// Router exposes the chi router, primarily for tests.
func (s *Server) Router() chi.Router { return s.router }

func (s *Server) buildRoutes() error {
	isTrustedProxy, err := TrustedProxyChecker(s.cfg.Security.TrustedProxies)
	if err != nil {
		return fmt.Errorf("api: TRUSTED_PROXIES is invalid: %w", err)
	}

	r := chi.NewRouter()

	// Order matters. RequestID must precede the logger so every line is
	// correlated; RealIP must precede anything that makes a decision based on
	// the client address; Recoverer must wrap the handlers, not the logger, so
	// a panic still produces a request log line.
	r.Use(RequestID(isTrustedProxy))
	r.Use(RealIP(isTrustedProxy))
	r.Use(ForwardedProto(isTrustedProxy))
	r.Use(RequestLogger(s.logger, s.cfg.Log.RequestLogging))
	r.Use(Recoverer)
	r.Use(SecurityHeaders())
	// Reject requests whose path contains a NUL or a raw control character
	// before any handler sees them.
	r.Use(middleware.CleanPath)

	// A JSON 404/405 for anything unmatched under /api; the SPA fallback
	// handles everything else and is registered last.
	r.NotFound(s.handleNotFound)
	r.MethodNotAllowed(s.handleMethodNotAllowed)

	// --- Operational endpoints -------------------------------------------
	// Deliberately outside /api/v1: they are infrastructure contracts, not
	// application API, and must stay stable across API versions.
	r.Group(func(r chi.Router) {
		r.Use(NoCache)
		r.Get("/health", s.handleHealth)
		r.Head("/health", s.handleHealth)
		r.Get("/health/live", s.handleLive)
		r.Get("/health/ready", s.handleReady)
	})

	// --- Versioned JSON API ----------------------------------------------
	r.Route(APIPrefix, func(r chi.Router) {
		r.Use(NoCache)

		// Two sibling groups, and the distinction matters.
		//
		// chi middleware is inherited by nested groups, so a Timeout applied
		// here would reach every route beneath it — including a 20 GB upload,
		// which would be aborted partway through. The transfer routes are
		// therefore a separate group that never sees it, rather than a nested
		// one that would.
		r.Group(func(r chi.Router) {
			r.Use(Timeout(s.cfg.HTTP.JSONTimeout))

			r.Get("/health", s.handleHealth)
			r.Get("/version", s.handleVersion)
			r.Get("/config", s.handlePublicConfig)

			if s.authEnabled() {
				s.mountAuthRoutes(r)
			}
			if s.filesEnabled() {
				s.mountContentRoutes(r)
				s.mountSharingRoutes(r)
			}
			if s.publicLinksEnabled() {
				s.mountPublicLinkRoutes(r)
			}
			s.mountProductivityRoutes(r)
			if s.versionsEnabled() {
				s.mountVersionRoutes(r)
			}
			if s.adminEnabled() {
				s.mountAdminRoutes(r)
			}
		})

		// Long transfers: identical protection, no deadline.
		r.Group(func(r chi.Router) {
			if s.filesEnabled() {
				s.mountTransferRoutes(r)
			}
			if s.publicLinksEnabled() {
				s.mountPortalTransferRoutes(r)
			}
			if s.archivesEnabled() {
				// A ZIP of a large folder is a long transfer for exactly the
				// same reason an upload is, and belongs on the same side of
				// the timeout boundary.
				s.mountArchiveRoutes(r)
			}
		})
	})

	// --- Single-page application -----------------------------------------
	// Registered last so it can never shadow an API route.
	if s.static != nil {
		r.Handle("/*", s.static)
	}

	s.router = r
	return nil
}

// handleNotFound returns JSON for unknown API paths and defers to the SPA for
// everything else, so a deep link such as /files/some-folder loads the app
// instead of a 404.
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	if isAPIPath(r.URL.Path) || s.static == nil {
		WriteError(w, r, ErrNotFound())
		return
	}
	s.static.ServeHTTP(w, r)
}

func (s *Server) handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	WriteError(w, r, &APIError{
		Status:  http.StatusMethodNotAllowed,
		Code:    CodeBadRequest,
		Message: fmt.Sprintf("The %s method is not supported for this endpoint.", r.Method),
	})
}

func isAPIPath(p string) bool {
	return p == "/api" || len(p) >= 5 && p[:5] == "/api/"
}

// PublicConfig is the subset of configuration the browser legitimately needs.
//
// Everything here is safe for an unauthenticated caller: it describes how the
// interface should behave, never where anything lives or how it is secured.
type PublicConfig struct {
	AppName             string `json:"app_name"`
	Version             string `json:"version"`
	RegistrationEnabled bool   `json:"registration_enabled"`
	SharingEnabled      bool   `json:"sharing_enabled"`
	PublicLinksEnabled  bool   `json:"public_links_enabled"`
	UploadRequests      bool   `json:"upload_requests_enabled"`
	MaxUploadBytes      int64  `json:"max_upload_bytes"`
	ChunkSizeBytes      int64  `json:"chunk_size_bytes"`
	PasswordMinLength   int    `json:"password_min_length"`
	TrashRetentionDays  int    `json:"trash_retention_days"`
	VersioningEnabled   bool   `json:"versioning_enabled"`
}

func (s *Server) handlePublicConfig(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, r, http.StatusOK, PublicConfig{
		AppName:             s.cfg.App.Name,
		Version:             versionString(),
		RegistrationEnabled: s.cfg.App.RegistrationEnabled,
		SharingEnabled:      s.cfg.Sharing.Enabled,
		PublicLinksEnabled:  s.cfg.Sharing.PublicLinksEnabled,
		UploadRequests:      s.cfg.Sharing.UploadRequestEnabled,
		MaxUploadBytes:      s.cfg.Uploads.MaxSize,
		ChunkSizeBytes:      s.cfg.Uploads.ChunkSize,
		PasswordMinLength:   s.cfg.Security.PasswordMinLength,
		TrashRetentionDays:  s.cfg.Trash.RetentionDays,
		VersioningEnabled:   s.cfg.Versions.Enabled,
	})
}

func versionString() string { return version.Version }

// mountAuthRoutes registers everything that depends on authentication.
//
// The grouping is the security boundary, so it is worth reading carefully:
//
//   - Setup and sign-in are reachable without a session, because a caller who
//     has no session must be able to obtain one.
//   - Authenticate runs on every route so a handler can tell who is calling,
//     but it never rejects; RequireAuth does that.
//   - CSRF runs only where there is a session to forge against.
//   - RequirePasswordChange sits inside the authenticated group but outside
//     the password-change endpoint itself, or a forced change would be
//     impossible to complete.
func (s *Server) mountAuthRoutes(r chi.Router) {
	// --- First-run setup -------------------------------------------------
	r.Route("/setup", func(r chi.Router) {
		r.Get("/", s.handleSetupStatus)
		r.Post("/", s.handleSetup)
	})

	// --- Authentication --------------------------------------------------
	r.Route("/auth", func(r chi.Router) {
		r.Use(s.Authenticate)

		// Sign-in has no CSRF token to present. It is protected instead by
		// SameSite=Lax on the cookie it sets, by rate limiting, and by the
		// fact that a forged sign-in gains an attacker nothing.
		r.Post("/login", s.handleLogin)

		r.Group(func(r chi.Router) {
			r.Use(s.CSRF)
			r.Post("/logout", s.handleLogout)
		})

		r.Get("/session", s.handleSession)

		// The password-change endpoint requires authentication but must remain
		// reachable while a forced change is outstanding.
		r.Group(func(r chi.Router) {
			r.Use(RequireAuth, s.CSRF)
			r.Post("/password", s.handleChangePassword)
			r.Get("/sessions", s.handleListMySessions)
			r.Delete("/sessions/{id}", s.handleRevokeMySession)
		})
	})
}

// authenticated returns a router group requiring a signed-in user who has no
// outstanding forced password change. Feature routes mount inside it.
func (s *Server) authenticated(r chi.Router, fn func(chi.Router)) {
	r.Group(func(r chi.Router) {
		r.Use(s.Authenticate, RequireAuth, RequirePasswordChange, s.CSRF)
		fn(r)
	})
}

// chiURLParam reads a path parameter.
func chiURLParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

// mountContentRoutes registers the file and folder API.
//
// Every route here sits behind Authenticate → RequireAuth →
// RequirePasswordChange → CSRF. The one deliberate exception is the transfer
// group, which is identical except that it is registered OUTSIDE the 30-second
// timeout applied to ordinary JSON handlers: a 20 GB upload over a home
// connection legitimately takes hours, and a timeout there would abort it
// partway through and corrupt nothing but waste everything.
func (s *Server) mountContentRoutes(r chi.Router) {
	// Reads and writes are limited separately, because they cost differently
	// and because a client legitimately reads far more often than it writes.
	// Splitting them means a busy listing view cannot exhaust the budget that
	// a delete needs.
	s.authenticated(r, func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(s.Limit(LimitRead))
			r.Get("/nodes", s.handleListNodes)
			r.Get("/nodes/{id}", s.handleGetNode)
			r.Get("/trash", s.handleListTrash)
		})

		r.Group(func(r chi.Router) {
			r.Use(s.Limit(LimitWrite))
			r.Post("/nodes/folders", s.handleCreateFolder)
			r.Patch("/nodes/{id}", s.handleUpdateNode)
			r.Delete("/nodes/{id}", s.handleDeleteNode)
			r.Post("/nodes/{id}/copy", s.handleCopyNode)
			r.Post("/nodes/{id}/restore", s.handleRestoreNode)
		})

		// Upload session control. These are small JSON calls; only the chunk
		// transfer itself needs to escape the timeout. They share the upload
		// budget rather than the write one: beginning and completing are one
		// pair per file, and counting them as writes would let a sync client
		// exhaust the budget a delete needs.
		r.Group(func(r chi.Router) {
			r.Use(s.Limit(LimitUpload))
			r.Route("/uploads", func(r chi.Router) {
				r.Get("/", s.handleListUploads)
				r.Post("/", s.handleBeginUpload)
				r.Get("/{id}", s.handleGetUpload)
				r.Post("/{id}/complete", s.handleCompleteUpload)
				r.Delete("/{id}", s.handleAbortUpload)
			})
		})
	})
}

// mountTransferRoutes registers the authenticated routes that legitimately run
// for a long time.
//
// They carry the same authentication, forced-password-change, and CSRF
// protection as everything else. The only difference is that they are mounted
// in a group with no request deadline: a 20 GB upload over a home connection
// takes hours, and cutting it off would be a bug rather than a protection.
func (s *Server) mountTransferRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(s.Authenticate, RequireAuth, RequirePasswordChange, s.CSRF)
		r.Use(s.Limit(LimitUpload))
		r.Put("/uploads/{id}/chunk", s.handleUploadChunk)
		r.Post("/files", s.handleUpload)
	})

	// Downloads are a GET, so CSRF does not apply, but authentication does.
	r.Group(func(r chi.Router) {
		r.Use(s.Authenticate, RequireAuth, RequirePasswordChange)
		r.Use(s.Limit(LimitRead))
		r.Get("/nodes/{id}/download", s.handleDownload)
		r.Head("/nodes/{id}/download", s.handleDownload)
	})
}

// mountSharingRoutes registers the sharing and group API.
//
// The split of authority here is deliberate:
//
//   - Sharing an item requires the Share permission on that item, checked
//     per-node by the sharing service. Any user may therefore share what they
//     have been given the right to share.
//   - Creating and deleting groups requires an administrator, because a group
//     is an instance-wide building block of access control.
//   - Changing a group's membership requires an administrator *or* a manager
//     of that specific group, so a team lead can run their own team.
func (s *Server) mountSharingRoutes(r chi.Router) {
	s.authenticated(r, func(r chi.Router) {
		// The permission and role vocabulary, so the interface never hardcodes
		// a list that could drift from the server's.
		r.Get("/sharing/vocabulary", s.handleSharingVocabulary)

		r.Route("/shares", func(r chi.Router) {
			r.Post("/", s.handleCreateShare)
			r.Delete("/{id}", s.handleRevokeShare)
		})

		// Who has access to one node.
		r.Get("/nodes/{id}/shares", s.handleListNodeShares)

		// Everything shared with the caller.
		r.Get("/shared-with-me", s.handleSharedWithMe)

		r.Route("/groups", func(r chi.Router) {
			// Any signed-in user may search groups, because sharing with one
			// requires finding it. Membership is not exposed here.
			r.Get("/", s.handleListGroups)
			r.Get("/mine", s.handleListMyGroups)
			r.Get("/{id}", s.handleGetGroup)

			// Membership changes are gated per group inside the handler.
			r.Post("/{id}/members", s.handleAddGroupMember)
			r.Delete("/{id}/members/{userID}", s.handleRemoveGroupMember)

			// Creating and deleting groups is instance-wide, so it is an
			// administrator action.
			r.Group(func(r chi.Router) {
				r.Use(s.RequireAdmin)
				r.Post("/", s.handleCreateGroup)
				r.Patch("/{id}", s.handleUpdateGroup)
				r.Delete("/{id}", s.handleDeleteGroup)
			})
		})
	})
}

// mountPublicLinkRoutes registers link management and the anonymous portal.
//
// The two halves sit at very different trust levels and are deliberately kept
// apart:
//
//   - /public-links is authenticated, CSRF-protected, and behaves like the
//     rest of the API.
//   - /portal is unauthenticated by definition. It has no CSRF protection
//     because there is no session to forge against, and no auth middleware at
//     all — authority comes entirely from the token.
func (s *Server) mountPublicLinkRoutes(r chi.Router) {
	// --- Management -------------------------------------------------------
	s.authenticated(r, func(r chi.Router) {
		r.Route("/public-links", func(r chi.Router) {
			r.Get("/", s.handleListMyPublicLinks)
			r.Post("/", s.handleCreatePublicLink)
			r.Delete("/{id}", s.handleRevokePublicLink)
		})
		r.Get("/nodes/{id}/public-links", s.handleListNodePublicLinks)
	})

	// --- The anonymous portal ---------------------------------------------
	//
	// Limited per client address, which is the only identity a visitor has.
	// The publiclinks service already rate-limits password attempts and token
	// probing; this is the cruder outer bound that stops one address consuming
	// the server through a link it legitimately holds.
	r.Route("/portal/{token}", func(r chi.Router) {
		r.Use(s.Limit(LimitAnonymous))
		r.Get("/", s.handlePortal)
		r.Post("/password", s.handlePortalPassword)
	})
}

// mountPortalTransferRoutes registers the anonymous transfer endpoints.
//
// No authentication middleware at all: authority comes from the token, checked
// inside each handler. No CSRF either, because there is no session to forge
// against — a visitor has no account.
func (s *Server) mountPortalTransferRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(s.Limit(LimitAnonymous))
		r.Get("/portal/{token}/download", s.handlePortalDownload)
		r.Head("/portal/{token}/download", s.handlePortalDownload)
		r.Post("/portal/{token}/upload", s.handlePortalUpload)
	})
}

// mountProductivityRoutes registers Phase 7's search, favourites, recent
// activity, tags, and preview endpoints.
//
// Each block is guarded independently, so a deployment that supplies only some
// of the collaborators gets only the corresponding routes rather than a handler
// that panics on a nil store.
func (s *Server) mountProductivityRoutes(r chi.Router) {
	if s.searchEnabled() {
		s.authenticated(r, func(r chi.Router) {
			// Its own budget: a trigram scan across a shared tree is the most
			// expensive read the API offers, and a search-as-you-type client
			// can issue one per keystroke.
			r.Use(s.Limit(LimitSearch))
			r.Get("/search", s.handleSearch)
		})
	}

	if s.favorites != nil && s.filesEnabled() {
		s.authenticated(r, func(r chi.Router) {
			r.Get("/favorites", s.handleListFavorites)
			r.Put("/nodes/{id}/favorite", s.handleAddFavorite)
			r.Delete("/nodes/{id}/favorite", s.handleRemoveFavorite)
		})
	}

	if s.recent != nil && s.filesEnabled() {
		s.authenticated(r, func(r chi.Router) {
			r.Get("/recent", s.handleListRecent)
		})
	}

	if s.tags != nil && s.filesEnabled() {
		s.authenticated(r, func(r chi.Router) {
			r.Route("/tags", func(r chi.Router) {
				r.Get("/", s.handleListTags)
				r.Post("/", s.handleCreateTag)
				r.Patch("/{id}", s.handleUpdateTag)
				r.Delete("/{id}", s.handleDeleteTag)
			})
			r.Get("/nodes/{id}/tags", s.handleListNodeTags)
			r.Put("/nodes/{id}/tags/{tagID}", s.handleApplyTag)
			r.Delete("/nodes/{id}/tags/{tagID}", s.handleRemoveTag)
		})
	}

	if s.previewsEnabled() {
		s.authenticated(r, func(r chi.Router) {
			r.Get("/nodes/{id}/preview/status", s.handlePreviewStatus)
		})
	}
}

// mountArchiveRoutes registers the ZIP downloads.
//
// Mounted in the no-deadline group: a folder holding 40 GB takes as long to
// stream as it takes, and a timeout would truncate it into an archive that
// will not open.
//
// GET, so CSRF does not apply — but authentication does, and the permission
// check is per-item inside the builder.
func (s *Server) mountArchiveRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(s.Authenticate, RequireAuth, RequirePasswordChange)
		// A folder archive walks a whole subtree and reads every byte in it —
		// the most expensive single request PiFiles serves. It gets the write
		// budget rather than the read one despite being a GET, because its
		// cost has nothing to do with a listing's.
		r.Use(s.Limit(LimitWrite))
		r.Get("/nodes/{id}/archive", s.handleDownloadFolder)
		r.Get("/archive", s.handleDownloadSelection)
	})

	if s.previewsEnabled() {
		// A preview is small, but it is served from the same content path and
		// there is no reason to put a JSON deadline on a binary stream.
		r.Group(func(r chi.Router) {
			r.Use(s.Authenticate, RequireAuth, RequirePasswordChange)
			r.Get("/nodes/{id}/preview", s.handlePreview)
			r.Head("/nodes/{id}/preview", s.handlePreview)
		})
	}

	if s.versionsEnabled() {
		// A version is a file, and a file can be enormous. It belongs on the
		// same side of the timeout boundary as every other content stream.
		r.Group(func(r chi.Router) {
			r.Use(s.Authenticate, RequireAuth, RequirePasswordChange)
			r.Get("/nodes/{id}/versions/{versionID}/download", s.handleDownloadVersion)
			r.Head("/nodes/{id}/versions/{versionID}/download", s.handleDownloadVersion)
		})
	}
}

// mountVersionRoutes registers the JSON side of file history.
//
// A version carries no permissions of its own: every handler resolves the file
// first and checks against that, so a version identifier can never reach
// content the caller could not otherwise open, and losing access to a file
// loses access to its history in the same instant.
//
// The permission each operation requires is deliberately not uniform:
//
//   - Listing and downloading need Read (and Download): seeing what a file used
//     to contain is seeing the file.
//   - Restoring needs Modify, because it rewrites the file.
//   - Deleting a version needs Delete, not Modify. It destroys data that
//     nothing else recovers, and being allowed to edit a document is not the
//     same as being allowed to erase the record of what it used to say.
func (s *Server) mountVersionRoutes(r chi.Router) {
	s.authenticated(r, func(r chi.Router) {
		r.Get("/nodes/{id}/versions", s.handleListVersions)
		r.Post("/nodes/{id}/versions/{versionID}/restore", s.handleRestoreVersion)
		r.Delete("/nodes/{id}/versions/{versionID}", s.handleDeleteVersion)
	})
}

// mountAdminRoutes registers the administration API.
//
// Everything here sits behind RequireAdmin, and the boundary the grouping
// enforces is worth naming: an administrator manages *accounts and the
// instance*, not other people's files. No route in this group reads, lists, or
// downloads anybody's content, and the permission model does not grant an
// administrator access to it — running a server is not the same as being
// entitled to read what people store on it.
//
// The one route outside the administrator group is the caller's own
// "sign me out everywhere else", which belongs to everybody: it is what a
// person reaches for when they think somebody else has their password.
func (s *Server) mountAdminRoutes(r chi.Router) {
	s.authenticated(r, func(r chi.Router) {
		r.Post("/auth/sessions/revoke-others", s.handleRevokeMyOtherSessions)
	})

	s.authenticated(r, func(r chi.Router) {
		r.Use(s.RequireAdmin)

		r.Get("/admin/overview", s.handleAdminOverview)

		r.Route("/admin/users", func(r chi.Router) {
			r.Get("/", s.handleAdminListUsers)
			r.Post("/", s.handleAdminCreateUser)
			r.Get("/{id}", s.handleAdminGetUser)
			r.Patch("/{id}", s.handleAdminUpdateUser)
			r.Delete("/{id}", s.handleAdminDeleteUser)
			r.Post("/{id}/password", s.handleAdminResetPassword)
			r.Post("/{id}/unlock", s.handleAdminUnlockUser)
			r.Post("/{id}/sessions/revoke", s.handleAdminRevokeUserSessions)
		})

		r.Get("/admin/sessions", s.handleAdminListSessions)

		if s.jobs != nil {
			r.Get("/admin/jobs", s.handleAdminJobs)
			r.Post("/admin/jobs/{id}/retry", s.handleAdminRetryJob)
		}

		if s.auditEnabled() {
			r.Get("/admin/audit", s.handleAdminListAudit)
			r.Get("/admin/audit/vocabulary", s.handleAdminAuditVocabulary)
		}
	})
}
