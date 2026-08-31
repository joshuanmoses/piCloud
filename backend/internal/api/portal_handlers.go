package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/files"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
	"github.com/joshuanmoses/piCloud/backend/internal/publiclinks"
)

// Portal handlers: the unauthenticated surface a link recipient reaches.
//
// There is no user here. Authority comes entirely from the token, and every
// check in this file is against the token's permission set.
//
// Three rules govern everything below:
//
//  1. Every failure looks the same. Unknown token, revoked, expired, used up,
//     deleted content — all 404. Anything else would let somebody probing the
//     token space learn when they had found a real one.
//  2. Nothing about the instance leaks. No usernames, no folder paths, no
//     internal identifiers beyond what the visitor needs.
//  3. An upload portal shows nothing. Somebody handed a file-request link must
//     not be able to enumerate what other people submitted.

// accessGrantCookie is the name of the cookie carrying proof that a visitor
// entered the correct password.
const accessGrantCookie = "pifiles_link_access"

// portalResponse is what a visitor is told about a link.
type portalResponse struct {
	// NeedsPassword short-circuits everything else: when true, no detail about
	// the content is included at all.
	NeedsPassword bool `json:"needs_password"`

	Kind string `json:"kind,omitempty"`

	Title   string `json:"title,omitempty"`
	Message string `json:"message,omitempty"`

	// RequestedBy names the person who published the link, so a visitor knows
	// who is asking. It is a display name only.
	RequestedBy string `json:"requested_by,omitempty"`

	// Destination is the folder name for a file request, so the visitor can
	// see where their files are going. No path, just the one name.
	Destination string `json:"destination,omitempty"`

	// Item describes shared content. Absent for an upload-only portal.
	Item *portalItem `json:"item,omitempty"`
	// Items lists a shared folder's contents.
	Items []portalItem `json:"items,omitempty"`

	CanDownload bool `json:"can_download"`
	CanUpload   bool `json:"can_upload"`

	MaxUploadBytes    int64    `json:"max_upload_bytes,omitempty"`
	AllowedExtensions []string `json:"allowed_extensions,omitempty"`

	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	RemainingUses int        `json:"remaining_uses"`
}

// portalItem is a file or folder as a visitor sees it.
//
// Deliberately minimal: a name, a kind, a size, and an opaque identifier.
// No owner, no timestamps beyond modification, no permissions, no parent.
type portalItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Size     int64  `json:"size_bytes"`
	MimeType string `json:"mime_type,omitempty"`
	Modified string `json:"modified,omitempty"`
}

func toPortalItem(n *nodes.Node) portalItem {
	return portalItem{
		ID:       n.ID.String(),
		Name:     n.Name,
		Kind:     string(n.Kind),
		Size:     n.SizeBytes,
		MimeType: n.MimeType,
		Modified: n.UpdatedAt.UTC().Format(timeFormat),
	}
}

// handlePortal describes a link to its visitor.
func (s *Server) handlePortal(w http.ResponseWriter, r *http.Request) {
	token := chiURLParam(r, "token")
	grant := readAccessGrant(r)

	access, err := s.publicLinks.Open(r.Context(), token, grant, ClientIPFromContext(r.Context()))
	if err != nil {
		WriteError(w, r, translatePortalError(err))
		return
	}

	if access.NeedsPassword {
		// Nothing but the fact that a password is needed.
		WriteJSON(w, r, http.StatusOK, portalResponse{NeedsPassword: true})
		return
	}

	link, node := access.Link, access.Node
	response := portalResponse{
		Kind:              string(link.Kind()),
		Title:             link.Title,
		Message:           link.Message,
		CanDownload:       link.Permissions.Has(permissions.Download),
		CanUpload:         link.IsUploadPortal(),
		AllowedExtensions: link.AllowedExtensions,
		ExpiresAt:         link.ExpiresAt,
		RemainingUses:     link.RemainingUses(),
	}

	if link.IsUploadPortal() {
		response.MaxUploadBytes = s.portalUploadLimit(link)
		// The destination folder's own name, so the visitor can confirm where
		// their files land. Never the path — that would disclose the folder
		// structure above it.
		response.Destination = node.Name
		if creator := s.linkCreatorName(r, link); creator != "" {
			response.RequestedBy = creator
		}
	}

	// Content detail only when the link actually grants reading it. A
	// file-request portal shows nothing, so somebody handed the link cannot
	// enumerate what others submitted.
	if link.Permissions.Has(permissions.Read) {
		if node.IsFolder() {
			items, err := s.portalFolderContents(r, node)
			if err != nil {
				WriteError(w, r, err)
				return
			}
			response.Items = items
			response.Item = &portalItem{
				ID: node.ID.String(), Name: node.Name, Kind: string(node.Kind),
			}
		} else {
			item := toPortalItem(node)
			response.Item = &item
		}
	}

	WriteJSON(w, r, http.StatusOK, response)
}

// portalFolderContents lists a shared folder for a visitor.
func (s *Server) portalFolderContents(r *http.Request, folder *nodes.Node) ([]portalItem, error) {
	page, err := s.files.Nodes().List(r.Context(), nodes.ListParams{
		ParentID: folder.ID,
		Limit:    nodes.MaxPageSize,
		Sort:     nodes.SortByName,
	})
	if err != nil {
		return nil, ErrInternal(err)
	}

	items := make([]portalItem, 0, len(page.Nodes))
	for _, n := range page.Nodes {
		items = append(items, toPortalItem(n))
	}
	return items, nil
}

// portalUploadLimit is the smaller of the link's own limit and the instance's.
func (s *Server) portalUploadLimit(link *publiclinks.Link) int64 {
	limit := s.files.Config().MaxUploadSize
	if link.MaxUploadBytes != nil && *link.MaxUploadBytes < limit {
		return *link.MaxUploadBytes
	}
	return limit
}

// linkCreatorName returns the display name of whoever published the link.
//
// A display name and nothing else: a visitor should know who is asking for
// files without learning a username they could then try to sign in as.
func (s *Server) linkCreatorName(r *http.Request, link *publiclinks.Link) string {
	if link.CreatedBy == nil {
		return ""
	}
	user, err := s.userStore.GetByID(r.Context(), *link.CreatedBy)
	if err != nil {
		return ""
	}
	return user.DisplayName
}

type portalPasswordRequest struct {
	Password string `json:"password"`
}

// handlePortalPassword verifies a visitor's password and sets the access
// cookie.
func (s *Server) handlePortalPassword(w http.ResponseWriter, r *http.Request) {
	token := chiURLParam(r, "token")

	var req portalPasswordRequest
	if err := DecodeJSON(w, r, &req, maxJSONBody); err != nil {
		WriteError(w, r, err)
		return
	}

	grant, expiresAt, err := s.publicLinks.VerifyPassword(
		r.Context(), token, req.Password, ClientIPFromContext(r.Context()))
	if err != nil {
		WriteError(w, r, translatePortalError(err))
		return
	}

	// Scoped to this link's portal path, so opening a second link does not
	// clobber the first, and so the grant is never sent anywhere else.
	http.SetCookie(w, &http.Cookie{
		Name:     accessGrantCookie,
		Value:    grant,
		Path:     "/api/v1/portal/" + token,
		HttpOnly: true,
		Secure:   s.cfg.Session.CookieSecure,
		// Lax rather than Strict: a visitor arriving from a link in an email
		// must not have to enter the password twice.
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
	})

	WriteNoContent(w)
}

// handlePortalDownload streams content to a visitor.
func (s *Server) handlePortalDownload(w http.ResponseWriter, r *http.Request) {
	token := chiURLParam(r, "token")

	access, err := s.publicLinks.Open(
		r.Context(), token, readAccessGrant(r), ClientIPFromContext(r.Context()))
	if err != nil {
		WriteError(w, r, translatePortalError(err))
		return
	}
	if access.NeedsPassword {
		WriteError(w, r, ErrUnauthorized("This link requires a password."))
		return
	}
	if !access.Link.Permissions.Has(permissions.Download) {
		WriteError(w, r, ErrForbidden("This link does not allow downloading."))
		return
	}

	// A shared folder can be entered one level, so a visitor can fetch an
	// individual file from it. The requested node must be the shared node
	// itself or one of its direct children — never an arbitrary identifier,
	// which would turn the portal into a way to read the whole instance.
	target := access.Node
	if requested := r.URL.Query().Get("item"); requested != "" {
		child, apiErr := s.portalChild(r, access, requested)
		if apiErr != nil {
			WriteError(w, r, apiErr)
			return
		}
		target = child
	}

	if !target.IsFile() {
		WriteError(w, r, ErrBadRequest("Folder downloads are not available through a link yet."))
		return
	}

	// Count the use before streaming. Counting afterwards would let a visitor
	// abandon each request just before completion and exceed the cap.
	if err := s.publicLinks.RecordUse(r.Context(), access.Link.ID, ClientIPFromContext(r.Context())); err != nil {
		WriteError(w, r, translatePortalError(err))
		return
	}

	content, size, err := s.files.OpenContent(target)
	if err != nil {
		s.logger.Error("cannot open content for a public link",
			"link_id", access.Link.ID, "node_id", target.ID, "error", err)
		WriteError(w, r, ErrNotFound())
		return
	}
	defer content.Close()

	s.writeDownloadHeaders(w, r, target)
	s.logger.Info("public link download",
		"link_id", access.Link.ID, "node_id", target.ID, "size", size,
		"ip", ClientIPFromContext(r.Context()))

	http.ServeContent(w, r, target.Name, target.UpdatedAt, content)
}

// portalChild resolves a requested item to a direct child of the shared node.
//
// Confining this to one level is the whole point: without it, a visitor with
// any valid token could pass any node identifier and read the entire instance.
func (s *Server) portalChild(
	r *http.Request, access *publiclinks.Access, requested string,
) (*nodes.Node, error) {
	if !access.Node.IsFolder() {
		return nil, ErrNotFound()
	}

	id, err := uuid.Parse(requested)
	if err != nil {
		return nil, ErrNotFound()
	}

	child, err := s.files.Nodes().Get(r.Context(), id)
	if err != nil {
		return nil, ErrNotFound()
	}
	// The parent check is the containment guarantee.
	if child.ParentID == nil || *child.ParentID != access.Node.ID {
		s.logger.Warn("public link tried to reach content outside its folder",
			"link_id", access.Link.ID, "requested", id,
			"ip", ClientIPFromContext(r.Context()))
		return nil, ErrNotFound()
	}
	return child, nil
}

// handlePortalUpload accepts a file into a file-request folder.
func (s *Server) handlePortalUpload(w http.ResponseWriter, r *http.Request) {
	token := chiURLParam(r, "token")

	access, err := s.publicLinks.Open(
		r.Context(), token, readAccessGrant(r), ClientIPFromContext(r.Context()))
	if err != nil {
		WriteError(w, r, translatePortalError(err))
		return
	}
	if access.NeedsPassword {
		WriteError(w, r, ErrUnauthorized("This link requires a password."))
		return
	}
	if !access.Link.IsUploadPortal() {
		WriteError(w, r, ErrForbidden("This link does not accept uploads."))
		return
	}
	if !access.Node.IsFolder() {
		WriteError(w, r, ErrBadRequest("This link does not point at a folder."))
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		WriteError(w, r, ErrBadRequest("Provide the file name in the 'name' query parameter."))
		return
	}

	// The extension filter is a courtesy to the visitor — telling them
	// immediately that a .exe is not wanted — not a security control. The
	// server still sniffs the content and never trusts the extension.
	if !access.Link.ExtensionAllowed(name) {
		WriteError(w, r, ErrValidation(
			fmt.Sprintf("Only these file types are accepted here: %s.",
				joinStrings(access.Link.AllowedExtensions, ", "))).
			WithField("name", "That file type is not accepted."))
		return
	}

	limit := s.portalUploadLimit(access.Link)
	if r.ContentLength > limit {
		WriteError(w, r, ErrTooLarge(fmt.Sprintf(
			"That file is larger than the %s this link accepts.", humanSize(limit))))
		return
	}

	// Count the use before accepting the bytes, for the same reason downloads
	// do: an abandoned request must not be a free attempt.
	if err := s.publicLinks.RecordUse(r.Context(), access.Link.ID, ClientIPFromContext(r.Context())); err != nil {
		WriteError(w, r, translatePortalError(err))
		return
	}

	// A colliding name gets a derived one rather than an error. A visitor
	// cannot see what is already in the folder, so "that name is taken" would
	// be both unhelpful and a disclosure.
	finalName, err := s.uniquePortalName(r, access.Node.ID, name)
	if err != nil {
		WriteError(w, r, ErrInternal(err))
		return
	}

	result, err := s.files.Upload(r.Context(), files.UploadParams{
		// The content belongs to the folder's owner and counts against their
		// quota. The uploader has no account to charge.
		OwnerID:      access.Node.OwnerID,
		ParentID:     access.Node.ID,
		Name:         finalName,
		DeclaredSize: r.ContentLength,
		Content:      http.MaxBytesReader(w, r.Body, limit),
		ActorID:      access.Node.OwnerID,
	})
	if err != nil {
		WriteError(w, r, translatePortalUploadError(err))
		return
	}

	s.logger.Info("public link upload",
		"link_id", access.Link.ID, "node_id", result.Node.ID,
		"name", finalName, "size", result.Node.SizeBytes,
		"ip", ClientIPFromContext(r.Context()))

	// The response confirms what was received and nothing more: no node
	// identifier the visitor could use elsewhere, no folder detail.
	WriteJSON(w, r, http.StatusCreated, map[string]any{
		"name":       finalName,
		"size_bytes": result.Node.SizeBytes,
		"renamed":    finalName != name,
	})
}

// uniquePortalName derives a non-colliding name inside the destination.
func (s *Server) uniquePortalName(r *http.Request, parentID uuid.UUID, name string) (string, error) {
	normalized := nodes.NormalizeName(name)
	if err := nodes.ValidateName(normalized); err != nil {
		return "", err
	}

	taken, err := s.files.Nodes().NameExists(r.Context(), parentID, normalized)
	if err != nil {
		return "", err
	}
	if !taken {
		return normalized, nil
	}

	// Probe candidates rather than reading the folder's names, which this
	// caller is not entitled to see.
	for i := 2; i < 1000; i++ {
		candidate := nodes.BaseName(normalized) +
			" (" + itoa(i) + ")" + nodes.Extension(normalized)
		taken, err := s.files.Nodes().NameExists(r.Context(), parentID, candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return normalized + " (" + newShortID() + ")", nil
}

// translatePortalError maps portal failures to responses.
//
// Every "this link will not work" reason collapses to 404, so a caller probing
// tokens learns nothing.
func translatePortalError(err error) error {
	switch {
	case errors.Is(err, publiclinks.ErrNotFound),
		errors.Is(err, publiclinks.ErrInvalidToken),
		errors.Is(err, publiclinks.ErrUsedUp):
		return ErrNotFound()
	case errors.Is(err, publiclinks.ErrWrongPassword):
		return ErrUnauthorized("Incorrect password.")
	case errors.Is(err, publiclinks.ErrDisabled):
		return ErrNotFound()
	default:
		return translatePublicLinkError(err)
	}
}

// translatePortalUploadError maps an upload failure for an anonymous visitor.
//
// The quota belongs to somebody the visitor has never heard of, so the message
// says the destination is full rather than naming whose allowance it is.
func translatePortalUploadError(err error) error {
	switch {
	case errors.Is(err, files.ErrQuotaExceeded):
		return &APIError{
			Status:  http.StatusInsufficientStorage,
			Code:    CodeQuotaExceeded,
			Message: "There is not enough space left here to accept this file.",
		}
	case errors.Is(err, files.ErrTooLarge):
		return ErrTooLarge("That file is larger than this link accepts.")
	case errors.Is(err, files.ErrNoSpace):
		return ErrUnavailable("The server is out of space. Please try again later.")
	default:
		return translateNodeError(err)
	}
}

// readAccessGrant extracts the password-verification cookie.
func readAccessGrant(r *http.Request) string {
	cookie, err := r.Cookie(accessGrantCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// itoa is strconv.Itoa, wrapped so the import stays local to its one use.
func itoa(n int) string { return strconv.Itoa(n) }

// joinStrings is strings.Join.
func joinStrings(parts []string, sep string) string { return strings.Join(parts, sep) }

// newShortID returns a short random suffix for a last-resort filename.
func newShortID() string { return uuid.NewString()[:8] }
