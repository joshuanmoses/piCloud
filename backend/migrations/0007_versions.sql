-- 0007_versions: file version history.
--
-- A version records what a file *was* before it was replaced. The bytes are not
-- copied anywhere: the previous content already exists as a blob, and the
-- version simply holds a reference to it. Copying would double the storage cost
-- of every edit on a machine chosen for having a modest disk, and would break
-- the case where two nodes share content — you cannot move a blob that somebody
-- else is still using.
--
-- This is why STORAGE_ROOT/versions/ stays empty. Content addressing made a
-- separate directory unnecessary rather than the design forgetting about it.

CREATE TABLE versions (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    -- The file this is a version OF. Cascade: a purged file's history goes
    -- with it, since a version of nothing has no meaning.
    node_id      UUID        NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,

    -- Denormalised from the node so the per-user retention sweep does not have
    -- to join through the tree on every pass. A version's owner cannot change,
    -- so the copy cannot go stale.
    owner_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- Monotonic per file, starting at 1. Displayed to people, so it must not
    -- renumber when an old version is pruned.
    version      INTEGER     NOT NULL CHECK (version >= 1),

    -- The content this version points at. RESTRICT rather than CASCADE: a
    -- blob must never be deleted while a version still references it, and if
    -- that is ever attempted the database should refuse loudly rather than
    -- leave history pointing at nothing.
    content_hash TEXT        NOT NULL REFERENCES blobs(hash) ON DELETE RESTRICT,
    size_bytes   BIGINT      NOT NULL CHECK (size_bytes >= 0),
    mime_type    TEXT        NOT NULL DEFAULT '',

    -- The name at the time. A file renamed between versions should show what
    -- it was called then, not what it is called now.
    name         TEXT        NOT NULL,

    -- Who replaced it, and when. SET NULL so deleting an account does not take
    -- the history of shared files with it.
    created_by   UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Why this version exists, for the interface: an ordinary replacement, or
    -- the snapshot taken when an older version was restored over it.
    reason       TEXT        NOT NULL DEFAULT 'replaced',

    UNIQUE (node_id, version)
);

COMMENT ON TABLE versions IS
    'Previous contents of a file. References a blob rather than copying bytes.';

-- The version list for one file, newest first.
CREATE INDEX versions_node_idx ON versions (node_id, version DESC);

-- The retention sweep: oldest first, and by owner for the per-user size cap.
CREATE INDEX versions_age_idx ON versions (created_at);

-- The per-user size cap.
CREATE INDEX versions_owner_idx ON versions (owner_id, created_at);
