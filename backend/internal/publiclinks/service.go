package publiclinks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/auth"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
)

// Config controls link behaviour.
type Config struct {
	// Enabled and UploadRequestsEnabled reflect instance policy.
	Enabled               bool
	UploadRequestsEnabled bool
	// RequirePassword forces every new link to carry one.
	RequirePassword bool
	// MaxLifetime bounds how far in the future an expiry may be set. Zero
	// means no bound.
	MaxLifetime time.Duration
	// DefaultLifetime pre-fills the expiry in the interface. Zero means none.
	DefaultLifetime time.Duration
	// AccessGrantLifetime is how long a verified password stays accepted
	// before the visitor must enter it again.
	AccessGrantLifetime time.Duration
}

// Service creates and resolves links.
type Service struct {
	store       *Store
	nodes       *nodes.Store
	permissions *permissions.Store
	hasher      *auth.Hasher
	cfg         Config
	logger      *slog.Logger

	// grantKey signs the short-lived proof that a visitor entered the correct
	// password, so the expensive hash runs once rather than on every request.
	grantKey []byte

	// Password attempts are rate limited per token. A public link password is
	// necessarily low-entropy, and the endpoint is unauthenticated, so this is
	// the only thing standing between it and offline-speed guessing.
	limiter *auth.RateLimiter
	// A second limiter caps how fast one address may probe *tokens*, so the
	// endpoint cannot be used to search the token space.
	probeLimiter *auth.RateLimiter

	now func() time.Time
}

// Rate limits for the unauthenticated portal endpoints.
const (
	passwordAttemptsPerWindow = 10
	passwordWindow            = 15 * time.Minute
	probeAttemptsPerWindow    = 60
	probeWindow               = time.Minute
)

// NewService wires the service.
func NewService(
	store *Store,
	nodeStore *nodes.Store,
	permissionStore *permissions.Store,
	hasher *auth.Hasher,
	appSecret string,
	cfg Config,
	logger *slog.Logger,
) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.AccessGrantLifetime <= 0 {
		cfg.AccessGrantLifetime = 2 * time.Hour
	}

	// The signing key is derived from the application secret with a purpose
	// string, so a grant from this subsystem can never be valid in another.
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write([]byte("pifiles/public-link-access/v1"))

	return &Service{
		store: store, nodes: nodeStore, permissions: permissionStore,
		hasher: hasher, cfg: cfg, logger: logger,
		grantKey:     mac.Sum(nil),
		limiter:      auth.NewRateLimiter(passwordAttemptsPerWindow, passwordWindow),
		probeLimiter: auth.NewRateLimiter(probeAttemptsPerWindow, probeWindow),
		now:          time.Now,
	}
}

// SetClock overrides the time source. For tests only.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Store exposes the underlying store for management operations.
func (s *Service) Store() *Store { return s.store }

// Config returns the policy in force.
func (s *Service) Config() Config { return s.cfg }

// CreateParams describes a link the owner wants to publish.
type CreateParams struct {
	NodeID  uuid.UUID
	ActorID uuid.UUID
	// Kind names a preset; Permissions overrides it when non-zero.
	Kind        string
	Permissions permissions.Set
	// Password is plaintext and is hashed here.
	Password          string
	ExpiresAt         *time.Time
	MaxUses           *int
	MaxUploadBytes    *int64
	AllowedExtensions []string
	Title             string
	Message           string
}

// Create publishes a link.
//
// The caller must hold Share on the node, and — as with internal sharing —
// cannot publish more than they hold themselves. A public link is a share to
// everybody, so it would be strange if it could grant more than a share to one
// person.
func (s *Service) Create(ctx context.Context, p CreateParams) (*Link, Token, error) {
	if !s.cfg.Enabled {
		return nil, "", ErrDisabled
	}

	node, err := s.nodes.Get(ctx, p.NodeID)
	if err != nil {
		return nil, "", ErrNotFound
	}
	if node.IsRoot() {
		return nil, "", fmt.Errorf("%w: your top-level folder cannot be published", ErrNotPermitted)
	}

	decision, err := s.permissions.Evaluate(ctx, p.NodeID, p.ActorID)
	if err != nil {
		return nil, "", err
	}
	// Somebody who cannot see the node must not learn that it exists.
	if !decision.Allows(permissions.Read) {
		return nil, "", ErrNotFound
	}
	if !decision.Allows(permissions.Share) {
		return nil, "", fmt.Errorf("%w: you do not have permission to share this", ErrNotPermitted)
	}

	set := p.Permissions
	if set == permissions.None {
		set, err = ParseKind(p.Kind)
		if err != nil {
			return nil, "", err
		}
	}

	if set.HasAny(permissions.Upload|permissions.Create) && !s.cfg.UploadRequestsEnabled {
		return nil, "", fmt.Errorf("%w: file requests are disabled on this server", ErrDisabled)
	}
	// An upload portal only makes sense on a folder.
	if set.HasAny(permissions.Upload|permissions.Create) && !node.IsFolder() {
		return nil, "", fmt.Errorf("%w: a file request must point at a folder", ErrNotPermitted)
	}

	if !decision.Effective.Has(set) {
		missing := set &^ decision.Effective
		return nil, "", fmt.Errorf(
			"%w: you cannot publish %s because you do not have it yourself",
			ErrNotPermitted, missing)
	}

	if s.cfg.RequirePassword && p.Password == "" {
		return nil, "", fmt.Errorf("%w: this server requires every public link to have a password", ErrPasswordRequired)
	}

	expiresAt, err := s.resolveExpiry(p.ExpiresAt)
	if err != nil {
		return nil, "", err
	}

	var passwordHash string
	if p.Password != "" {
		passwordHash, err = s.hasher.Hash(p.Password)
		if err != nil {
			return nil, "", fmt.Errorf("publiclinks: cannot hash password: %w", err)
		}
	}

	link, token, err := s.store.Create(ctx, InsertParams{
		NodeID:            p.NodeID,
		Permissions:       set,
		PasswordHash:      passwordHash,
		ExpiresAt:         expiresAt,
		MaxUses:           p.MaxUses,
		MaxUploadBytes:    p.MaxUploadBytes,
		AllowedExtensions: p.AllowedExtensions,
		Title:             strings.TrimSpace(p.Title),
		Message:           strings.TrimSpace(p.Message),
		CreatedBy:         p.ActorID,
	})
	if err != nil {
		return nil, "", err
	}

	s.logger.Info("public link created",
		"link_id", link.ID, "node_id", p.NodeID, "node_name", node.Name,
		"kind", link.Kind(), "has_password", link.HasPassword,
		"expires_at", expiresAt, "actor_id", p.ActorID)

	return link, token, nil
}

// resolveExpiry applies the instance's lifetime policy.
func (s *Service) resolveExpiry(requested *time.Time) (*time.Time, error) {
	now := s.now()

	if requested == nil {
		if s.cfg.DefaultLifetime > 0 {
			t := now.Add(s.cfg.DefaultLifetime)
			return &t, nil
		}
		if s.cfg.MaxLifetime > 0 {
			// A server that caps link lifetime cannot allow one with no expiry.
			t := now.Add(s.cfg.MaxLifetime)
			return &t, nil
		}
		return nil, nil
	}

	if !requested.After(now) {
		return nil, fmt.Errorf("publiclinks: the expiry must be in the future")
	}
	if s.cfg.MaxLifetime > 0 && requested.After(now.Add(s.cfg.MaxLifetime)) {
		return nil, fmt.Errorf(
			"publiclinks: this server does not allow links to last longer than %s",
			s.cfg.MaxLifetime)
	}
	return requested, nil
}

// Access is what a visitor holding a token may do.
type Access struct {
	Link *Link
	Node *nodes.Node
	// NeedsPassword is true when the caller has not yet proved they know it.
	NeedsPassword bool
}

// Open resolves a token for a visitor.
//
// accessGrant is the signed proof from a previous successful password entry,
// or empty. When the link is protected and no valid grant is supplied, the
// result reports NeedsPassword and carries no content detail — the portal can
// render a password prompt without learning anything about what is behind it.
func (s *Service) Open(ctx context.Context, rawToken, accessGrant, clientIP string) (*Access, error) {
	if !s.cfg.Enabled {
		return nil, ErrDisabled
	}

	// Cap how fast one address may probe the token space.
	if clientIP != "" {
		if ok, _ := s.probeLimiter.Allow("link-probe:" + clientIP); !ok {
			return nil, ErrNotFound
		}
	}

	resolved, err := s.store.Resolve(ctx, rawToken)
	if err != nil {
		return nil, err
	}

	access := &Access{Link: resolved.Link}

	if resolved.PasswordHash != "" && !s.grantIsValid(accessGrant, resolved.Link.ID) {
		access.NeedsPassword = true
		return access, nil
	}

	node, err := s.nodes.Get(ctx, resolved.Link.NodeID)
	if err != nil {
		// The content was deleted after the link was made.
		return nil, ErrNotFound
	}
	access.Node = node
	return access, nil
}

// VerifyPassword checks a visitor's password and issues an access grant.
//
// The grant is an HMAC over the link identifier and an expiry, so the
// expensive Argon2id verification runs once rather than on every subsequent
// request — which matters on a Raspberry Pi, where a page of thumbnails would
// otherwise mean a page of password hashes.
func (s *Service) VerifyPassword(
	ctx context.Context, rawToken, password, clientIP string,
) (grant string, expiresAt time.Time, err error) {
	if !s.cfg.Enabled {
		return "", time.Time{}, ErrDisabled
	}

	resolved, err := s.store.Resolve(ctx, rawToken)
	if err != nil {
		// Still burn time, so a wrong token and a wrong password take
		// comparable effort.
		auth.BurnTime(password)
		return "", time.Time{}, err
	}

	// Rate limit per link and per address. A link password is necessarily
	// low-entropy and this endpoint is unauthenticated, so this is the only
	// thing between it and offline-speed guessing.
	key := "link-password:" + resolved.Link.ID.String()
	if ok, _ := s.limiter.Allow(key); !ok {
		s.logger.Warn("public link password rate limit exceeded",
			"link_id", resolved.Link.ID, "ip", clientIP)
		return "", time.Time{}, auth.ErrRateLimited
	}
	if clientIP != "" {
		if ok, _ := s.limiter.Allow("link-password-ip:" + clientIP); !ok {
			return "", time.Time{}, auth.ErrRateLimited
		}
	}

	if resolved.PasswordHash == "" {
		// Not protected; nothing to verify.
		return s.issueGrant(resolved.Link.ID)
	}

	if err := auth.Verify(password, resolved.PasswordHash); err != nil {
		s.logger.Info("public link password rejected",
			"link_id", resolved.Link.ID, "ip", clientIP)
		return "", time.Time{}, ErrWrongPassword
	}

	s.limiter.Reset(key)
	return s.issueGrant(resolved.Link.ID)
}

// issueGrant mints the signed proof of a successful password entry.
func (s *Service) issueGrant(linkID uuid.UUID) (string, time.Time, error) {
	expiry := s.now().Add(s.cfg.AccessGrantLifetime)
	payload := fmt.Sprintf("%s:%d", linkID, expiry.Unix())

	mac := hmac.New(sha256.New, s.grantKey)
	mac.Write([]byte(payload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return fmt.Sprintf("%d.%s", expiry.Unix(), signature), expiry, nil
}

// grantIsValid checks a grant against a link.
func (s *Service) grantIsValid(grant string, linkID uuid.UUID) bool {
	if grant == "" {
		return false
	}

	expiryText, signature, found := strings.Cut(grant, ".")
	if !found {
		return false
	}
	expiryUnix, err := strconv.ParseInt(expiryText, 10, 64)
	if err != nil {
		return false
	}
	if s.now().Unix() >= expiryUnix {
		return false
	}

	mac := hmac.New(sha256.New, s.grantKey)
	mac.Write([]byte(fmt.Sprintf("%s:%d", linkID, expiryUnix)))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	// Constant time: a timing side channel would let an attacker forge a grant
	// byte by byte.
	return hmac.Equal([]byte(expected), []byte(signature))
}

// RecordUse counts a download or an upload against the link's cap.
func (s *Service) RecordUse(ctx context.Context, linkID uuid.UUID, clientIP string) error {
	return s.store.RecordUse(ctx, linkID, clientIP)
}

// Revoke withdraws a link. The caller must have created it, or hold Manage on
// the node.
func (s *Service) Revoke(ctx context.Context, linkID, actorID uuid.UUID) error {
	link, err := s.store.Get(ctx, linkID)
	if err != nil {
		return ErrNotFound
	}

	if link.CreatedBy == nil || *link.CreatedBy != actorID {
		decision, err := s.permissions.Evaluate(ctx, link.NodeID, actorID)
		if err != nil {
			return err
		}
		if !decision.Allows(permissions.Manage) {
			// Uniform: confirming the link exists would tell the caller they
			// guessed a real identifier.
			return ErrNotFound
		}
	}

	if err := s.store.Revoke(ctx, linkID); err != nil {
		return err
	}
	s.logger.Info("public link revoked", "link_id", linkID, "actor_id", actorID)
	return nil
}

// Cleanup releases stale rate-limiter memory. Called periodically.
func (s *Service) Cleanup() {
	s.limiter.Cleanup()
	s.probeLimiter.Cleanup()
}
