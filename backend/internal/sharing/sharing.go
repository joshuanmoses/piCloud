// Package sharing turns permission grants into the user-facing concept of a
// share: who has access to what, who gave it to them, and what they may do.
//
// A share is not a separate record from a permission grant — it is the same
// row, presented for people rather than for the evaluator. Modelling them
// separately would create two sources of truth for one question, and one day
// they would disagree about whether access had been revoked.
package sharing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/database"
	"github.com/joshuanmoses/piCloud/backend/internal/groups"
	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
	"github.com/joshuanmoses/piCloud/backend/internal/users"
)

// Errors returned by this package.
var (
	// ErrNotFound means no such share, or none the caller may see.
	ErrNotFound = errors.New("sharing: not found")
	// ErrCannotShareWithOwner means the recipient already owns the item.
	ErrCannotShareWithOwner = errors.New("sharing: the owner already has full access")
	// ErrCannotDenyOwner means somebody tried to deny the item's owner.
	//
	// The evaluator settles ownership before denials are considered, so such a
	// denial would sit in the access list looking as though it did something.
	// Refusing is clearer than accepting and quietly ignoring it.
	ErrCannotDenyOwner = errors.New(
		"sharing: the owner's access cannot be taken away — transfer ownership instead")
	// ErrCannotShareRoot means somebody tried to share their whole account.
	ErrCannotShareRoot = errors.New("sharing: your top-level folder cannot be shared")
	// ErrNotPermitted means the caller may not share this item.
	ErrNotPermitted = errors.New("sharing: you do not have permission to share this")
	// ErrSubjectNotFound means the named user or group does not exist.
	ErrSubjectNotFound = errors.New("sharing: no such user or group")
	// ErrDisabled means sharing is switched off for this instance.
	ErrDisabled = errors.New("sharing: sharing is disabled on this server")
)

// Share is a grant presented for a person to read.
type Share struct {
	ID uuid.UUID `json:"id"`

	// Node identifies what is shared.
	Node ShareTarget `json:"node"`

	// Exactly one of User or Group is set.
	SubjectType permissions.SubjectType `json:"subject_type"`
	User        *users.Public           `json:"user,omitempty"`
	Group       *GroupSummary           `json:"group,omitempty"`

	// Permissions in their API name form, plus the role they match if any.
	Permissions []string `json:"permissions"`
	Role        string   `json:"role,omitempty"`

	Deny    bool `json:"deny"`
	Inherit bool `json:"inherit"`

	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	Message   string     `json:"message,omitempty"`

	// SharedBy is who created the grant.
	SharedBy *users.Public `json:"shared_by,omitempty"`
}

// ShareTarget describes the shared node without exposing storage detail.
type ShareTarget struct {
	ID       uuid.UUID     `json:"id"`
	Name     string        `json:"name"`
	Kind     string        `json:"kind"`
	Size     int64         `json:"size_bytes"`
	MimeType string        `json:"mime_type,omitempty"`
	Owner    *users.Public `json:"owner,omitempty"`
}

// GroupSummary is a group as it appears in a share.
type GroupSummary struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// Service creates, lists, and revokes shares.
type Service struct {
	db          *database.DB
	permissions *permissions.Store
	nodes       *nodes.Store
	users       *users.Store
	groups      *groups.Store
	logger      *slog.Logger
	// enabled reflects the instance-wide sharing policy.
	enabled bool
}

// NewService wires the sharing service.
func NewService(
	db *database.DB,
	permissionStore *permissions.Store,
	nodeStore *nodes.Store,
	userStore *users.Store,
	groupStore *groups.Store,
	enabled bool,
	logger *slog.Logger,
) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		db: db, permissions: permissionStore, nodes: nodeStore,
		users: userStore, groups: groupStore, enabled: enabled, logger: logger,
	}
}

// Permissions exposes the evaluator, which the API uses for access checks.
func (s *Service) Permissions() *permissions.Store { return s.permissions }

// Enabled reports whether internal sharing is switched on.
func (s *Service) Enabled() bool { return s.enabled }

// CreateParams describes a new share.
type CreateParams struct {
	NodeID uuid.UUID
	// ActorID is who is doing the sharing.
	ActorID uuid.UUID

	// Exactly one of these identifies the recipient.
	Username  string
	UserID    *uuid.UUID
	GroupID   *uuid.UUID
	GroupName string

	Permissions permissions.Set
	Inherit     bool
	ExpiresAt   *time.Time
	Message     string

	// Deny takes access away rather than granting it.
	//
	// A denial always wins over every grant, including one inherited from a
	// group, which is what makes it the only way to express "everyone in
	// Accounting may edit this, except Bob". It cannot reach an owner: the
	// evaluator settles ownership before denials are considered, so a denial
	// against the owner would silently do nothing — and is refused here rather
	// than accepted and ignored.
	Deny bool
}

// Create shares a node with a user or a group.
//
// The caller must hold Share on the node, and cannot grant more than they hold
// themselves. That second rule is what stops privilege escalation by sharing:
// a user given read access cannot turn round and grant somebody else the right
// to delete.
func (s *Service) Create(ctx context.Context, p CreateParams) (*Share, error) {
	if !s.enabled {
		return nil, ErrDisabled
	}

	node, err := s.nodes.Get(ctx, p.NodeID)
	if err != nil {
		return nil, ErrNotFound
	}
	if node.IsRoot() {
		return nil, ErrCannotShareRoot
	}

	decision, err := s.permissions.Evaluate(ctx, p.NodeID, p.ActorID)
	if err != nil {
		return nil, err
	}

	// Somebody who cannot see the node at all must not learn that it exists.
	// Returning "you may not share this" would confirm a guessed identifier
	// names something real, which is exactly the enumeration oracle the 404
	// convention exists to close.
	if !decision.Allows(permissions.Read) {
		return nil, ErrNotFound
	}
	// Denying takes access away from somebody who has it by another route,
	// which is administering the item's access rather than sharing it out.
	// Manage is the permission for that, and requiring only Share would let
	// anyone a folder was shared with revoke everybody else's access to it.
	if p.Deny {
		if !decision.Allows(permissions.Manage) {
			return nil, fmt.Errorf(
				"%w: taking access away needs the manage permission, not just share",
				ErrNotPermitted)
		}
	} else {
		if !decision.Allows(permissions.Share) {
			return nil, ErrNotPermitted
		}

		// Nobody may grant a permission they do not hold. Without this, a
		// share would be a privilege-escalation primitive.
		//
		// The rule does not apply to a denial: taking access away cannot
		// increase anybody's, so denying more than you hold is harmless.
		if !decision.Effective.Has(p.Permissions) {
			missing := p.Permissions &^ decision.Effective
			return nil, fmt.Errorf(
				"%w: you cannot grant %s because you do not have it yourself",
				ErrNotPermitted, missing)
		}
	}

	subjectType, subjectID, err := s.resolveSubject(ctx, p)
	if err != nil {
		return nil, err
	}

	// Sharing with the owner is a no-op that would show a confusing entry.
	// Denying the owner is worse than a no-op — the evaluator settles
	// ownership first, so the entry would sit in the access list looking like
	// it did something.
	if subjectType == permissions.SubjectUser && subjectID == node.OwnerID {
		if p.Deny {
			return nil, ErrCannotDenyOwner
		}
		return nil, ErrCannotShareWithOwner
	}
	// Nor with yourself.
	if subjectType == permissions.SubjectUser && subjectID == p.ActorID {
		return nil, ErrCannotShareWithOwner
	}

	grant, err := s.permissions.Grant(ctx, permissions.GrantParams{
		NodeID:      p.NodeID,
		SubjectType: subjectType,
		SubjectID:   subjectID,
		Permissions: p.Permissions,
		Deny:        p.Deny,
		Inherit:     p.Inherit,
		ExpiresAt:   p.ExpiresAt,
		CreatedBy:   p.ActorID,
		Message:     p.Message,
	})
	if err != nil {
		return nil, err
	}

	action := "node shared"
	if p.Deny {
		action = "access denied on a node"
	}
	s.logger.Info(action,
		"node_id", p.NodeID, "node_name", node.Name,
		"subject_type", subjectType, "subject_id", subjectID,
		"permissions", p.Permissions.String(), "deny", p.Deny, "actor_id", p.ActorID)

	shares, err := s.hydrate(ctx, []*permissions.Grant{grant})
	if err != nil {
		return nil, err
	}
	if len(shares) == 0 {
		return nil, ErrNotFound
	}
	return shares[0], nil
}

// resolveSubject turns whichever recipient identifier was supplied into a
// concrete subject.
func (s *Service) resolveSubject(
	ctx context.Context, p CreateParams,
) (permissions.SubjectType, uuid.UUID, error) {
	switch {
	case p.UserID != nil:
		if _, err := s.users.GetByID(ctx, *p.UserID); err != nil {
			return "", uuid.Nil, ErrSubjectNotFound
		}
		return permissions.SubjectUser, *p.UserID, nil

	case p.Username != "":
		user, err := s.users.GetByUsername(ctx, p.Username)
		if err != nil {
			return "", uuid.Nil, ErrSubjectNotFound
		}
		return permissions.SubjectUser, user.ID, nil

	case p.GroupID != nil:
		if _, err := s.groups.Get(ctx, *p.GroupID); err != nil {
			return "", uuid.Nil, ErrSubjectNotFound
		}
		return permissions.SubjectGroup, *p.GroupID, nil

	case p.GroupName != "":
		found, err := s.groups.List(ctx, p.GroupName, 2)
		if err != nil {
			return "", uuid.Nil, err
		}
		for _, g := range found {
			if equalFold(g.Name, p.GroupName) {
				return permissions.SubjectGroup, g.ID, nil
			}
		}
		return "", uuid.Nil, ErrSubjectNotFound

	default:
		return "", uuid.Nil, fmt.Errorf("sharing: name a user or a group to share with")
	}
}

// Revoke removes a share.
//
// The caller must hold Manage on the node, or be the person who created the
// share. The second case matters: somebody with Share but not Manage can undo
// what they did without being able to touch anybody else's access.
func (s *Service) Revoke(ctx context.Context, shareID, actorID uuid.UUID) error {
	grant, err := s.permissions.Get(ctx, shareID)
	if err != nil {
		return ErrNotFound
	}

	decision, err := s.permissions.Evaluate(ctx, grant.NodeID, actorID)
	if err != nil {
		return err
	}

	createdByActor := grant.CreatedBy != nil && *grant.CreatedBy == actorID
	if !decision.Allows(permissions.Manage) && !createdByActor {
		// A uniform not-found: confirming the share exists would tell the
		// caller they guessed a real identifier.
		return ErrNotFound
	}

	if err := s.permissions.Revoke(ctx, shareID); err != nil {
		return err
	}

	s.logger.Info("share revoked",
		"share_id", shareID, "node_id", grant.NodeID, "actor_id", actorID)
	return nil
}

// ListForNode returns who has access to a node.
//
// The caller must be able to read the node. Seeing who else it is shared with
// requires Manage or ownership; otherwise the list is limited to the caller's
// own entry, so a recipient cannot enumerate everyone else with access.
func (s *Service) ListForNode(
	ctx context.Context, nodeID, actorID uuid.UUID,
) ([]*Share, error) {
	decision, err := s.permissions.Evaluate(ctx, nodeID, actorID)
	if err != nil {
		return nil, err
	}
	if !decision.Allows(permissions.Read) {
		return nil, ErrNotFound
	}

	grants, err := s.permissions.ListForNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	if !decision.Allows(permissions.Manage) {
		// Restrict to the caller's own grants, direct or through a group.
		memberOf, err := s.groups.ListForUser(ctx, actorID)
		if err != nil {
			return nil, err
		}
		groupIDs := make(map[uuid.UUID]struct{}, len(memberOf))
		for _, g := range memberOf {
			groupIDs[g.ID] = struct{}{}
		}

		filtered := grants[:0]
		for _, g := range grants {
			switch g.SubjectType {
			case permissions.SubjectUser:
				if g.SubjectID == actorID {
					filtered = append(filtered, g)
				}
			case permissions.SubjectGroup:
				if _, member := groupIDs[g.SubjectID]; member {
					filtered = append(filtered, g)
				}
			}
		}
		grants = filtered
	}

	return s.hydrate(ctx, grants)
}

// SharedWithMe returns everything shared with a user, directly or through a
// group, excluding anything they own.
func (s *Service) SharedWithMe(ctx context.Context, userID uuid.UUID) ([]*Share, error) {
	now := time.Now()

	rows, err := s.db.Pool().Query(ctx, `
		SELECT
			p.id, p.node_id, p.subject_type, p.subject_id, p.permissions,
			p.deny, p.inherit, p.expires_at, p.created_at, p.updated_at,
			p.created_by, p.message
		FROM permissions p
		JOIN nodes n ON n.id = p.node_id
		WHERE p.deny = FALSE
		  AND n.deleted_at IS NULL
		  -- Something you own is not "shared with you", even if a stray grant
		  -- exists.
		  AND n.owner_id <> $1
		  AND (p.expires_at IS NULL OR p.expires_at > $2)
		  AND (
		    (p.subject_type = 'user' AND p.subject_id = $1)
		    OR (p.subject_type = 'group' AND p.subject_id IN (
		          SELECT group_id FROM group_members WHERE user_id = $1))
		  )
		  -- A grant that a denial has overridden must not be listed. Without
		  -- this the recipient sees the folder here and gets a 404 opening it,
		  -- which reads as a broken product rather than as an access decision.
		  --
		  -- Only a denial that removes Read matters: being denied "delete"
		  -- still leaves something genuinely shared with you.
		  AND NOT EXISTS (
		    WITH RECURSIVE ancestry AS (
		      SELECT n.id, n.parent_id, 0 AS depth
		      UNION ALL
		      SELECT a2.id, a2.parent_id, ancestry.depth + 1
		      FROM nodes a2 JOIN ancestry ON a2.id = ancestry.parent_id
		      WHERE ancestry.depth < 64
		    )
		    SELECT 1 FROM permissions d
		    JOIN ancestry a ON a.id = d.node_id
		    WHERE d.deny = TRUE
		      AND (a.depth = 0 OR d.inherit)
		      AND (d.expires_at IS NULL OR d.expires_at > $2)
		      -- The Read bit. A denial that does not remove it leaves the
		      -- share meaningful.
		      AND (d.permissions & 1) <> 0
		      AND (
		        (d.subject_type = 'user' AND d.subject_id = $1)
		        OR (d.subject_type = 'group' AND d.subject_id IN (
		              SELECT group_id FROM group_members WHERE user_id = $1))
		      )
		  )
		ORDER BY p.created_at DESC`, userID, now)
	if err != nil {
		return nil, fmt.Errorf("sharing: shared with me: %w", err)
	}
	defer rows.Close()

	var grants []*permissions.Grant
	for rows.Next() {
		var g permissions.Grant
		var bits int32
		if err := rows.Scan(
			&g.ID, &g.NodeID, &g.SubjectType, &g.SubjectID, &bits,
			&g.Deny, &g.Inherit, &g.ExpiresAt, &g.CreatedAt, &g.UpdatedAt,
			&g.CreatedBy, &g.Message,
		); err != nil {
			return nil, fmt.Errorf("sharing: scan share: %w", err)
		}
		g.Permissions = permissions.Set(bits)
		grants = append(grants, &g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sharing: shared with me: %w", err)
	}

	return s.hydrate(ctx, grants)
}

// hydrate turns grants into shares, filling in the node, subject, and sharer.
//
// The lookups are batched: rendering a list of thirty shares must not become
// ninety queries.
func (s *Service) hydrate(ctx context.Context, grants []*permissions.Grant) ([]*Share, error) {
	if len(grants) == 0 {
		return []*Share{}, nil
	}

	nodeIDs := make([]uuid.UUID, 0, len(grants))
	userIDs := make([]uuid.UUID, 0, len(grants))
	groupIDs := make([]uuid.UUID, 0, len(grants))

	for _, g := range grants {
		nodeIDs = append(nodeIDs, g.NodeID)
		if g.SubjectType == permissions.SubjectUser {
			userIDs = append(userIDs, g.SubjectID)
		} else {
			groupIDs = append(groupIDs, g.SubjectID)
		}
		if g.CreatedBy != nil {
			userIDs = append(userIDs, *g.CreatedBy)
		}
	}

	nodeMap, err := s.loadNodes(ctx, nodeIDs)
	if err != nil {
		return nil, err
	}
	userMap, err := s.loadUsers(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	groupMap, err := s.loadGroups(ctx, groupIDs)
	if err != nil {
		return nil, err
	}

	out := make([]*Share, 0, len(grants))
	for _, g := range grants {
		target, ok := nodeMap[g.NodeID]
		if !ok {
			// The node vanished between the query and now.
			continue
		}

		share := &Share{
			ID:          g.ID,
			Node:        target,
			SubjectType: g.SubjectType,
			Permissions: g.Permissions.Names(),
			Role:        g.Permissions.RoleName(),
			Deny:        g.Deny,
			Inherit:     g.Inherit,
			ExpiresAt:   g.ExpiresAt,
			CreatedAt:   g.CreatedAt,
			Message:     g.Message,
		}

		if g.SubjectType == permissions.SubjectUser {
			if u, found := userMap[g.SubjectID]; found {
				share.User = &u
			}
		} else if grp, found := groupMap[g.SubjectID]; found {
			share.Group = &grp
		}

		if g.CreatedBy != nil {
			if u, found := userMap[*g.CreatedBy]; found {
				share.SharedBy = &u
			}
		}

		out = append(out, share)
	}

	return out, nil
}

func (s *Service) loadNodes(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]ShareTarget, error) {
	result := make(map[uuid.UUID]ShareTarget, len(ids))
	if len(ids) == 0 {
		return result, nil
	}

	rows, err := s.db.Pool().Query(ctx, `
		SELECT n.id, n.name, n.kind::text, n.size_bytes, n.mime_type,
		       u.id, u.username, u.display_name
		FROM nodes n
		JOIN users u ON u.id = n.owner_id
		WHERE n.id = ANY($1) AND n.deleted_at IS NULL`, ids)
	if err != nil {
		return nil, fmt.Errorf("sharing: load nodes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var t ShareTarget
		var owner users.Public
		if err := rows.Scan(&t.ID, &t.Name, &t.Kind, &t.Size, &t.MimeType,
			&owner.ID, &owner.Username, &owner.DisplayName); err != nil {
			return nil, fmt.Errorf("sharing: scan node: %w", err)
		}
		t.Owner = &owner
		result[t.ID] = t
	}
	return result, rows.Err()
}

func (s *Service) loadUsers(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]users.Public, error) {
	result := make(map[uuid.UUID]users.Public, len(ids))
	if len(ids) == 0 {
		return result, nil
	}

	rows, err := s.db.Pool().Query(ctx,
		`SELECT id, username, display_name FROM users WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("sharing: load users: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var u users.Public
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName); err != nil {
			return nil, fmt.Errorf("sharing: scan user: %w", err)
		}
		result[u.ID] = u
	}
	return result, rows.Err()
}

func (s *Service) loadGroups(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]GroupSummary, error) {
	result := make(map[uuid.UUID]GroupSummary, len(ids))
	if len(ids) == 0 {
		return result, nil
	}

	rows, err := s.db.Pool().Query(ctx, `SELECT id, name FROM groups WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("sharing: load groups: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var g GroupSummary
		if err := rows.Scan(&g.ID, &g.Name); err != nil {
			return nil, fmt.Errorf("sharing: scan group: %w", err)
		}
		result[g.ID] = g
	}
	return result, rows.Err()
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
