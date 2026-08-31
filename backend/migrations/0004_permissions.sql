-- 0004_permissions: access-control grants.
--
-- One table holds every grant. There is deliberately no separate `shares`
-- table: a share IS a permission grant on a node to a subject, and modelling
-- them separately would mean two sources of truth for the same question — with
-- the certainty that one day they disagree and somebody keeps access they were
-- told had been revoked.
--
-- The /api/v1/shares endpoints are the user-facing view of these rows.

-- ---------------------------------------------------------------------------
-- permissions
-- ---------------------------------------------------------------------------
CREATE TYPE permission_subject AS ENUM ('user', 'group');

CREATE TABLE permissions (
    id           UUID               PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id      UUID               NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,

    subject_type PERMISSION_SUBJECT NOT NULL,
    -- References either users(id) or groups(id) depending on subject_type.
    -- PostgreSQL cannot express a conditional foreign key, so cleanup is
    -- handled by the two triggers below rather than by a constraint.
    subject_id   UUID               NOT NULL,

    -- Bitmask of the permission constants in internal/permissions. An integer
    -- rather than an array because effective access is the union of every
    -- applicable grant, and a bitwise OR does that in one operation whether
    -- the user holds one grant or twenty through their groups.
    permissions  INTEGER            NOT NULL CHECK (permissions >= 0),

    -- A deny always wins over any grant, at any depth. This is what makes it
    -- possible to share a whole tree and carve one subfolder back out.
    deny         BOOLEAN            NOT NULL DEFAULT FALSE,

    -- Whether the grant reaches descendants. Almost always true; false pins a
    -- grant to exactly one node.
    inherit      BOOLEAN            NOT NULL DEFAULT TRUE,

    -- Optional expiry, so a share can be given for a limited time.
    expires_at   TIMESTAMPTZ,

    created_at   TIMESTAMPTZ        NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ        NOT NULL DEFAULT now(),
    created_by   UUID               REFERENCES users(id) ON DELETE SET NULL,

    -- A note from the person sharing, shown to the recipient.
    message      TEXT               NOT NULL DEFAULT '' CHECK (length(message) <= 500)
);

COMMENT ON TABLE permissions IS
    'Access grants. A share is a grant; there is no separate shares table.';
COMMENT ON COLUMN permissions.permissions IS
    'Bitmask; see internal/permissions for the constants.';
COMMENT ON COLUMN permissions.deny IS
    'A deny overrides every grant, including one on a nearer ancestor.';

CREATE TRIGGER permissions_set_updated_at
    BEFORE UPDATE ON permissions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- One grant per (node, subject). Re-sharing updates the existing row rather
-- than accumulating grants that would have to be unioned and separately
-- revoked.
CREATE UNIQUE INDEX permissions_unique_grant_idx
    ON permissions (node_id, subject_type, subject_id);

-- Evaluation walks ancestors and asks "what applies to this node?".
CREATE INDEX permissions_node_idx ON permissions (node_id);

-- "Shared with me" asks the reverse question.
CREATE INDEX permissions_subject_idx ON permissions (subject_type, subject_id);

-- The expiry sweep.
CREATE INDEX permissions_expiry_idx ON permissions (expires_at)
    WHERE expires_at IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Referential integrity for the polymorphic subject
-- ---------------------------------------------------------------------------
-- subject_id points at users or groups depending on subject_type, which a
-- foreign key cannot express. Without these, deleting a user would leave
-- grants addressed to a nonexistent subject — harmless in itself, but they
-- would silently reactivate if a new account were ever issued the same UUID.
CREATE OR REPLACE FUNCTION delete_permissions_for_user()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    DELETE FROM permissions WHERE subject_type = 'user' AND subject_id = OLD.id;
    RETURN OLD;
END;
$$;

CREATE TRIGGER users_cascade_permissions
    BEFORE DELETE ON users
    FOR EACH ROW EXECUTE FUNCTION delete_permissions_for_user();

CREATE OR REPLACE FUNCTION delete_permissions_for_group()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    DELETE FROM permissions WHERE subject_type = 'group' AND subject_id = OLD.id;
    RETURN OLD;
END;
$$;

CREATE TRIGGER groups_cascade_permissions
    BEFORE DELETE ON groups
    FOR EACH ROW EXECUTE FUNCTION delete_permissions_for_group();

-- ---------------------------------------------------------------------------
-- Settings introduced by this migration
-- ---------------------------------------------------------------------------
INSERT INTO system_settings (key, value, description, is_public) VALUES
    ('sharing.internal_enabled', 'true',
     'Whether users may share content with other users and groups.', TRUE)
ON CONFLICT (key) DO NOTHING;
