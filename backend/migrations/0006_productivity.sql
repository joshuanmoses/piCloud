-- 0006_productivity: search, favourites, recent activity, tags, and the
-- background job queue.

-- ---------------------------------------------------------------------------
-- Search
-- ---------------------------------------------------------------------------
-- Trigram indexes on the name, so ILIKE '%report%' uses an index instead of
-- scanning every row. This is why PiFiles needs no separate search service:
-- adequate for millions of rows, and it costs no extra RAM on a 4 GB board.
CREATE INDEX nodes_name_trigram_idx ON nodes USING gin (name gin_trgm_ops)
    WHERE deleted_at IS NULL;

-- Filtering a search by type.
CREATE INDEX nodes_mime_idx ON nodes (mime_type)
    WHERE deleted_at IS NULL AND mime_type <> '';

-- ---------------------------------------------------------------------------
-- favorites
-- ---------------------------------------------------------------------------
-- Per-user: two people may each favourite the same shared folder, and
-- unfavouriting must not affect the other.
CREATE TABLE favorites (
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    node_id    UUID        NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (user_id, node_id)
);

COMMENT ON TABLE favorites IS 'Per-user bookmarks. Favouriting a shared item is private to the user.';

-- "What have I favourited", newest first.
CREATE INDEX favorites_user_idx ON favorites (user_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- recent_items
-- ---------------------------------------------------------------------------
CREATE TYPE recent_action AS ENUM ('viewed', 'downloaded', 'uploaded', 'modified');

-- One row per (user, node, action), updated rather than appended.
--
-- An append-only activity log would grow without bound and would show the same
-- file twenty times after twenty downloads. What the Recent view actually
-- wants is "when did I last touch this", which is one row.
CREATE TABLE recent_items (
    user_id      UUID          NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    node_id      UUID          NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    action       RECENT_ACTION NOT NULL,
    occurred_at  TIMESTAMPTZ   NOT NULL DEFAULT now(),

    PRIMARY KEY (user_id, node_id, action)
);

COMMENT ON TABLE recent_items IS
    'Last time each user touched each node, per action. Not an append-only log.';

-- The Recent listing.
CREATE INDEX recent_items_user_idx ON recent_items (user_id, occurred_at DESC);
-- The retention sweep.
CREATE INDEX recent_items_age_idx ON recent_items (occurred_at);

-- ---------------------------------------------------------------------------
-- tags
-- ---------------------------------------------------------------------------
CREATE TABLE tags (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id   UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       CITEXT      NOT NULL,
    -- A hex colour for the interface, or empty for the default.
    color      TEXT        NOT NULL DEFAULT '' CHECK (color = '' OR color ~ '^#[0-9a-fA-F]{6}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT tags_name_format CHECK (length(name) BETWEEN 1 AND 40),
    -- Tags belong to the person who made them, so two users may each have a
    -- tag called "Urgent" without collision.
    UNIQUE (owner_id, name)
);

CREATE TABLE file_tags (
    node_id    UUID        NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    tag_id     UUID        NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (node_id, tag_id)
);

CREATE INDEX file_tags_tag_idx ON file_tags (tag_id);

-- ---------------------------------------------------------------------------
-- background_jobs
-- ---------------------------------------------------------------------------
-- A durable queue in PostgreSQL rather than Redis. `FOR UPDATE SKIP LOCKED`
-- gives safe concurrent claiming, the rows survive a restart, and it adds no
-- second stateful service to a 4 GB board.
CREATE TYPE job_status AS ENUM ('pending', 'running', 'succeeded', 'failed', 'cancelled');

CREATE TABLE background_jobs (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    kind         TEXT        NOT NULL,
    -- Arbitrary payload, interpreted by the handler registered for `kind`.
    payload      JSONB       NOT NULL DEFAULT '{}',

    status       JOB_STATUS  NOT NULL DEFAULT 'pending',
    priority     SMALLINT    NOT NULL DEFAULT 0,

    attempts     INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts INTEGER     NOT NULL DEFAULT 5 CHECK (max_attempts >= 1),

    -- When the job becomes eligible. Retries push this into the future.
    run_after    TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Deduplication: at most one pending job may hold a given key, so a file
    -- edited ten times in a minute produces one thumbnail job, not ten.
    dedupe_key   TEXT,

    last_error   TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at   TIMESTAMPTZ,
    finished_at  TIMESTAMPTZ
);

COMMENT ON TABLE background_jobs IS
    'Durable work queue. Claimed with FOR UPDATE SKIP LOCKED; no Redis required.';

CREATE TRIGGER background_jobs_set_updated_at
    BEFORE UPDATE ON background_jobs
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- The claim query: the highest-priority job that is due.
CREATE INDEX background_jobs_claim_idx
    ON background_jobs (priority DESC, run_after)
    WHERE status = 'pending';

-- At most one pending job per dedupe key.
CREATE UNIQUE INDEX background_jobs_dedupe_idx
    ON background_jobs (dedupe_key)
    WHERE dedupe_key IS NOT NULL AND status IN ('pending', 'running');

-- The administrator's job listing, and the cleanup sweep.
CREATE INDEX background_jobs_status_idx ON background_jobs (status, created_at DESC);

-- ---------------------------------------------------------------------------
-- previews
-- ---------------------------------------------------------------------------
-- Generated thumbnails are derived data: losing them costs regeneration, not
-- information. They are recorded so the interface knows whether one exists
-- yet, and so a failed generation is not retried forever.
CREATE TYPE preview_status AS ENUM ('pending', 'ready', 'unavailable');

CREATE TABLE previews (
    node_id      UUID           PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    -- The digest of the content this preview was made from, so a replaced
    -- file's stale thumbnail is detected rather than served.
    content_hash TEXT           NOT NULL,
    status       PREVIEW_STATUS NOT NULL DEFAULT 'pending',
    width        INTEGER,
    height       INTEGER,
    size_bytes   BIGINT,
    -- Why a preview could not be made, for the administrator.
    detail       TEXT           NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ    NOT NULL DEFAULT now()
);

CREATE TRIGGER previews_set_updated_at
    BEFORE UPDATE ON previews
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX previews_status_idx ON previews (status);
