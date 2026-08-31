package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/audit"
	"github.com/joshuanmoses/piCloud/backend/internal/auth"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
	"github.com/joshuanmoses/piCloud/backend/internal/publiclinks"
)

// Public link handlers.
//
// These split into two groups with very different trust levels:
//
//   - Management (/api/v1/public-links) is authenticated and behaves like the
//     rest of the API.
//   - The portal (/api/v1/portal/{token}) is *unauthenticated by definition*.
//     Everything a visitor sends is untrusted, every failure returns the same
//     answer, and nothing about the instance leaks through it.
//
// The portal handlers never call requireAccess: there is no user. Authority
// comes entirely from the token's permission set, checked here.

// --- Management -------------------------------------------------------------

type createLinkRequest struct {
	NodeID string `json:"node_id"`
	// Kind names a preset: view, download, upload, upload_view.
	Kind string `json:"kind,omitempty"`

	Password  string     `json:"password,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	MaxUses   *int       `json:"max_uses,omitempty"`

	MaxUploadBytes    *int64   `json:"max_upload_bytes,omitempty"`
	AllowedExtensions []string `json:"allowed_extensions,omitempty"`

	Title   string `json:"title,omitempty"`
	Message string `json:"message,omitempty"`
}

// linkResponse describes a link to its owner.
//
// The token appears exactly once, in the response to the request that created
// it, because only a hash is stored and it can never be recovered.
type linkResponse struct {
	ID   uuid.UUID `json:"id"`
	Kind string    `json:"kind"`
	// URL is the address to hand out. Present only on creation.
	URL string `json:"url,omitempty"`

	Node *linkTarget `json:"node,omitempty"`

	HasPassword bool       `json:"has_password"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	MaxUses     *int       `json:"max_uses,omitempty"`
	UseCount    int        `json:"use_count"`

	MaxUploadBytes    *int64   `json:"max_upload_bytes,omitempty"`
	AllowedExtensions []string `json:"allowed_extensions,omitempty"`

	Title   string `json:"title,omitempty"`
	Message string `json:"message,omitempty"`

	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	// Live reports whether the link currently works.
	Live bool `json:"live"`
}

type linkTarget struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Kind string    `json:"kind"`
	Size int64     `json:"size_bytes"`
}

func (s *Server) toLinkResponse(link *publiclinks.Link, node *nodes.Node, token string) linkResponse {
	out := linkResponse{
		ID:                link.ID,
		Kind:              string(link.Kind()),
		HasPassword:       link.HasPassword,
		ExpiresAt:         link.ExpiresAt,
		MaxUses:           link.MaxUses,
		UseCount:          link.UseCount,
		MaxUploadBytes:    link.MaxUploadBytes,
		AllowedExtensions: link.AllowedExtensions,
		Title:             link.Title,
		Message:           link.Message,
		CreatedAt:         link.CreatedAt,
		RevokedAt:         link.RevokedAt,
		LastUsedAt:        link.LastUsedAt,
		Live:              link.IsLive(time.Now()),
	}
	if token != "" {
		out.URL = s.publicLinkURL(link, token)
	}
	if node != nil {
		out.Node = &linkTarget{ID: node.ID, Name: node.Name, Kind: string(node.Kind), Size: node.SizeBytes}
	}
	return out
}

// publicLinkURL builds the address to hand out.
//
// A file request gets /u/ and a share link gets /s/, purely so the URL says
// what it is. Both resolve through the same table.
func (s *Server) publicLinkURL(link *publiclinks.Link, token string) string {
	if link.IsUploadPortal() {
		return s.cfg.UploadRequestURL(token)
	}
	return s.cfg.PublicLinkURL(token)
}

// handleCreatePublicLink publishes a link.
func (s *Server) handleCreatePublicLink(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	var req createLinkRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}

	nodeID, err := uuid.Parse(req.NodeID)
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	if req.MaxUses != nil && *req.MaxUses < 1 {
		WriteError(w, r, ErrValidation("The usage limit must be at least 1.").
			WithField("max_uses", "Enter 1 or more, or leave it blank for no limit."))
		return
	}
	if req.Password != "" {
		if err := s.authService.Policy().Validate(req.Password); err != nil {
			var pwErr *auth.PasswordError
			if errors.As(err, &pwErr) {
				WriteError(w, r, ErrValidation("Choose a stronger password for this link.").
					WithField("password", pwErr.Reason))
				return
			}
		}
	}

	link, token, err := s.publicLinks.Create(r.Context(), publiclinks.CreateParams{
		NodeID:            nodeID,
		ActorID:           user.ID,
		Kind:              req.Kind,
		Password:          req.Password,
		ExpiresAt:         req.ExpiresAt,
		MaxUses:           req.MaxUses,
		MaxUploadBytes:    req.MaxUploadBytes,
		AllowedExtensions: req.AllowedExtensions,
		Title:             req.Title,
		Message:           req.Message,
	})
	if err != nil {
		WriteError(w, r, translatePublicLinkError(err))
		return
	}

	node, _ := s.files.Nodes().Get(r.Context(), nodeID)

	// Publishing content beyond the account boundary is exactly the kind of
	// event this trail exists for. The token is deliberately absent: the
	// record says a link was made, never what it is.
	nodeName := ""
	if node != nil {
		nodeName = node.Name
	}
	s.audit(r, audit.ActionLinkCreate, audit.Success,
		auditTarget(nodeID, "node", nodeName),
		auditDetail(map[string]any{
			"link_id":      link.ID.String(),
			"kind":         string(link.Kind()),
			"has_password": link.HasPassword,
			"expires_at":   link.ExpiresAt,
			"max_uses":     link.MaxUses,
		}))

	// The token travels in this response and nowhere else, ever.
	WriteJSON(w, r, http.StatusCreated, map[string]any{
		"link": s.toLinkResponse(link, node, token.Reveal()),
	})
}

// handleListMyPublicLinks returns the links the caller has published.
func (s *Server) handleListMyPublicLinks(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	links, err := s.publicLinks.Store().ListForCreator(r.Context(), user.ID)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	out := make([]linkResponse, 0, len(links))
	for _, link := range links {
		// The node may have been deleted since; the link is still worth
		// listing so the owner can tidy it up.
		node, _ := s.files.Nodes().Get(r.Context(), link.NodeID)
		out = append(out, s.toLinkResponse(link, node, ""))
	}

	WriteJSON(w, r, http.StatusOK, map[string]any{"links": out})
}

// handleListNodePublicLinks returns the links published on one node.
func (s *Server) handleListNodePublicLinks(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	// Seeing who a node has been published to is a management operation.
	if _, apiErr := s.requireAccess(r, id, permissions.Manage, false); apiErr != nil {
		WriteError(w, r, apiErr)
		return
	}

	links, err := s.publicLinks.Store().ListForNode(r.Context(), id)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	out := make([]linkResponse, 0, len(links))
	for _, link := range links {
		out = append(out, s.toLinkResponse(link, nil, ""))
	}
	WriteJSON(w, r, http.StatusOK, map[string]any{"links": out})
}

// handleRevokePublicLink withdraws a link.
func (s *Server) handleRevokePublicLink(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())

	id, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		WriteError(w, r, ErrNotFound())
		return
	}

	if err := s.publicLinks.Revoke(r.Context(), id, user.ID); err != nil {
		WriteError(w, r, translatePublicLinkError(err))
		return
	}

	s.audit(r, audit.ActionLinkRevoke, audit.Success,
		auditDetail(map[string]any{"link_id": id.String()}))

	WriteNoContent(w)
}

// translatePublicLinkError maps domain errors to HTTP responses.
func translatePublicLinkError(err error) error {
	switch {
	case errors.Is(err, publiclinks.ErrNotFound):
		return ErrNotFound()
	case errors.Is(err, publiclinks.ErrDisabled):
		return ErrForbidden(strings.TrimPrefix(err.Error(), "publiclinks: "))
	case errors.Is(err, publiclinks.ErrNotPermitted):
		return ErrForbidden(strings.TrimPrefix(err.Error(), "publiclinks: "))
	case errors.Is(err, publiclinks.ErrPasswordRequired):
		return ErrValidation("This link needs a password.").
			WithField("password", strings.TrimPrefix(err.Error(), "publiclinks: "))
	case errors.Is(err, publiclinks.ErrWrongPassword):
		return ErrUnauthorized("Incorrect password.")
	case errors.Is(err, publiclinks.ErrUsedUp):
		return ErrForbidden("This link has reached its limit.")
	case errors.Is(err, publiclinks.ErrFileTypeNotAllowed):
		return ErrValidation("That file type is not accepted here.").
			WithField("file", strings.TrimPrefix(err.Error(), "publiclinks: "))
	case errors.Is(err, auth.ErrRateLimited):
		return ErrRateLimited("Too many attempts. Please wait a few minutes and try again.")
	case errors.Is(err, nodes.ErrNotFound):
		return ErrNotFound()
	default:
		// A malformed expiry or an over-long lifetime arrives as a plain
		// error from the service; those messages are written for users.
		if err != nil && strings.HasPrefix(err.Error(), "publiclinks: ") {
			return ErrValidation(strings.TrimPrefix(err.Error(), "publiclinks: "))
		}
		return ErrInternal(err)
	}
}
