-- 0003_nodes: the filesystem tree, content blobs, and upload sessions.
--
-- Files and folders live in ONE table with a `kind` discriminator rather than
-- in two parallel tables. Permissions, sharing, trash, search, and favourites
-- then have a single target type instead of two implementations that inevitably
-- diverge — the bug where a folder can be shared but its permission check was
-- only written for files is designed out rather than tested for.

-- ---------------------------------------------------------------------------
-- blobs — stored content, addressed by digest
-- ---------------------------------------------------------------------------
-- A blob is a distinct sequence of bytes on disk. Several nodes may reference
-- the same blob: two users uploading identical content store it once.
CREATE TABLE blobs (
    -- Lowercase hex SHA-256. This is also the on-disk address, fanned out as
    -- files/ab/cd/<digest>.
    hash       TEXT        PRIMARY KEY,
    size_bytes BIGINT      NOT NULL CHECK (size_bytes >= 0),

    -- How many live nodes reference this content. Maintained transactionally
    -- alongside every node change; a blob reaching zero becomes eligible for
    -- removal by the reconciliation job, never deleted inline. Deferring the
    -- unlink means a failed transaction can never orphan a referenced file.
    ref_count  INTEGER     NOT NULL DEFAULT 0 CHECK (ref_count >= 0),

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT blobs_hash_format CHECK (hash ~ '^[0-9a-f]{64}$')
);

COMMENT ON TABLE blobs IS
    'Content-addressed storage records. The bytes live under STORAGE_ROOT/files/.';

-- The sweep for unreferenced content.
CREATE INDEX blobs_unreferenced_idx ON blobs (created_at) WHERE ref_count = 0;

-- ---------------------------------------------------------------------------
-- nodes — files and folders
-- ---------------------------------------------------------------------------
CREATE TYPE node_kind AS ENUM ('file', 'folder');

CREATE TABLE nodes (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- NULL identifies a user's root folder. Every other node has a parent.
    parent_id   UUID        REFERENCES nodes(id) ON DELETE RESTRICT,
    kind        NODE_KIND   NOT NULL,

    -- The user-visible name. It is never used to build a filesystem path; see
    -- content_hash and docs/architecture.md.
    name        TEXT        NOT NULL,

    -- Files only. A folder's size is computed on demand, not stored, so it
    -- cannot drift from reality.
    size_bytes  BIGINT      NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    -- Determined by server-side sniffing. A client-supplied type is advisory
    -- and never stored.
    mime_type   TEXT        NOT NULL DEFAULT '',
    content_hash TEXT       REFERENCES blobs(hash) ON DELETE RESTRICT,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Soft delete. A non-NULL value means the node is in Trash.
    deleted_at  TIMESTAMPTZ,

    created_by  UUID        REFERENCES users(id) ON DELETE SET NULL,
    modified_by UUID        REFERENCES users(id) ON DELETE SET NULL,

    -- Names are compared case-insensitively for uniqueness but displayed as
    -- typed. Stored as a generated column so the unique index below cannot be
    -- bypassed by a code path that forgets to normalise.
    name_folded TEXT GENERATED ALWAYS AS (lower(name)) STORED,

    -- A file has content; a folder does not. Enforcing this in the schema means
    -- no query has to defend against a folder with a content hash.
    CONSTRAINT nodes_file_has_content CHECK (
        (kind = 'folder' AND content_hash IS NULL AND size_bytes = 0)
        OR kind = 'file'
    ),
    -- The root is always a folder.
    CONSTRAINT nodes_root_is_folder CHECK (parent_id IS NOT NULL OR kind = 'folder'),

    CONSTRAINT nodes_name_length CHECK (length(name) <= 255),
    -- Names that would break path handling, break clients, or be invisible.
    -- The on-disk layout does not depend on this — content addressing already
    -- makes names irrelevant to the filesystem — but a name containing a slash
    -- or a NUL would confuse every client that renders a breadcrumb.
    CONSTRAINT nodes_name_safe CHECK (
        parent_id IS NULL
        OR (
            length(name) > 0
            AND name !~ '[/\\:*?"<>|]'
            AND name !~ E'[\\x00-\\x1f\\x7f]'
            AND name NOT IN ('.', '..')
            AND name = btrim(name)
        )
    )
);

COMMENT ON TABLE nodes IS
    'Files and folders. Content lives on disk, addressed by content_hash.';
COMMENT ON COLUMN nodes.parent_id IS 'NULL identifies the owner''s root folder.';
COMMENT ON COLUMN nodes.deleted_at IS 'Non-NULL means the node is in Trash.';

CREATE TRIGGER nodes_set_updated_at
    BEFORE UPDATE ON nodes
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Listing a folder: the single hottest query in the product.
CREATE INDEX nodes_parent_idx ON nodes (parent_id, kind, name_folded)
    WHERE deleted_at IS NULL;

-- Two live siblings may not share a name, compared case-insensitively.
-- Deleted nodes are excluded so a name can be reused after something is
-- trashed, and so restoring can detect the collision.
CREATE UNIQUE INDEX nodes_unique_sibling_name_idx
    ON nodes (parent_id, name_folded)
    WHERE deleted_at IS NULL AND parent_id IS NOT NULL;

-- Exactly one root folder per user.
CREATE UNIQUE INDEX nodes_one_root_per_owner_idx
    ON nodes (owner_id)
    WHERE parent_id IS NULL AND deleted_at IS NULL;

-- Quota recalculation and per-owner listings.
CREATE INDEX nodes_owner_idx ON nodes (owner_id) WHERE deleted_at IS NULL;

-- Trash listing and the retention sweep.
CREATE INDEX nodes_deleted_idx ON nodes (owner_id, deleted_at) WHERE deleted_at IS NOT NULL;

-- Reverse lookup from a blob to its referencing nodes, used by reconciliation.
CREATE INDEX nodes_content_hash_idx ON nodes (content_hash) WHERE content_hash IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Every user gets a root folder
-- ---------------------------------------------------------------------------
-- Done with a trigger rather than in application code so the invariant holds
-- for every path that creates a user: the setup endpoint, the create-admin
-- command, and the administration API added in Phase 9.
CREATE OR REPLACE FUNCTION create_user_root_folder()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO nodes (owner_id, parent_id, kind, name, created_by)
    VALUES (NEW.id, NULL, 'folder', '', NEW.id);
    RETURN NEW;
END;
$$;

CREATE TRIGGER users_create_root_folder
    AFTER INSERT ON users
    FOR EACH ROW EXECUTE FUNCTION create_user_root_folder();

-- Backfill for any account created before this migration.
INSERT INTO nodes (owner_id, parent_id, kind, name, created_by)
SELECT u.id, NULL, 'folder', '', u.id
FROM users u
WHERE NOT EXISTS (
    SELECT 1 FROM nodes n WHERE n.owner_id = u.id AND n.parent_id IS NULL
);

-- ---------------------------------------------------------------------------
-- uploads — resumable upload sessions
-- ---------------------------------------------------------------------------
CREATE TABLE uploads (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id       UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    parent_id      UUID        NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,

    name           TEXT        NOT NULL,
    -- What the client said it would send. Used for the quota pre-check and to
    -- detect a truncated upload; the authoritative size is what actually
    -- arrives.
    declared_size  BIGINT      NOT NULL CHECK (declared_size >= 0),
    -- Bytes durably written to the temporary file so far. This is what a
    -- resuming client asks for.
    received_bytes BIGINT      NOT NULL DEFAULT 0 CHECK (received_bytes >= 0),

    -- Set when the upload replaces an existing file, so finalisation knows to
    -- create a version rather than a new node.
    replaces_id    UUID        REFERENCES nodes(id) ON DELETE SET NULL,

    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Abandoned uploads are swept after this.
    expires_at     TIMESTAMPTZ NOT NULL,

    CONSTRAINT uploads_not_over_declared CHECK (received_bytes <= declared_size)
);

COMMENT ON TABLE uploads IS
    'In-flight chunked uploads. The partial content lives in STORAGE_ROOT/temp/.';

CREATE TRIGGER uploads_set_updated_at
    BEFORE UPDATE ON uploads
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX uploads_owner_idx ON uploads (owner_id, created_at DESC);
CREATE INDEX uploads_expiry_idx ON uploads (expires_at);
