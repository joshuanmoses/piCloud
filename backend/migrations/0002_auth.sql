-- 0002_auth: users, groups, and sessions.
--
-- These accounts are entirely internal to PiFiles. They are not Linux users,
-- they have no shell, no home directory, and no presence in /etc/passwd. A
-- compromise of an application account grants access to files through the API
-- and nothing else.

-- ---------------------------------------------------------------------------
-- users
-- ---------------------------------------------------------------------------
CREATE TABLE users (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    -- citext gives case-insensitive uniqueness without lower() in every query,
    -- so "Josh" and "josh" cannot both exist and either spelling can sign in.
    username           CITEXT      NOT NULL UNIQUE,
    display_name       TEXT        NOT NULL,
    email              CITEXT      UNIQUE,

    -- Argon2id in PHC string format, which carries its own parameters so the
    -- cost can be raised later without invalidating existing passwords.
    password_hash      TEXT        NOT NULL,

    is_enabled         BOOLEAN     NOT NULL DEFAULT TRUE,
    is_admin           BOOLEAN     NOT NULL DEFAULT FALSE,

    -- NULL means unlimited. 0 means the user may store nothing, which is a
    -- meaningful setting for an upload-request-only account.
    quota_bytes        BIGINT      CHECK (quota_bytes IS NULL OR quota_bytes >= 0),
    -- Denormalised running total, maintained transactionally alongside every
    -- content change and rebuildable by the quota-recalculation job.
    used_bytes         BIGINT      NOT NULL DEFAULT 0 CHECK (used_bytes >= 0),

    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at      TIMESTAMPTZ,

    failed_login_count INTEGER     NOT NULL DEFAULT 0 CHECK (failed_login_count >= 0),
    locked_until       TIMESTAMPTZ,

    -- Forces a password change at next sign-in, set by an administrator reset.
    must_change_password BOOLEAN   NOT NULL DEFAULT FALSE,

    CONSTRAINT users_username_format CHECK (
        length(username) BETWEEN 2 AND 64
        -- Letters, digits, and a few separators. Deliberately narrow: a
        -- username appears in audit logs, share dialogs, and URLs, and
        -- permitting arbitrary Unicode invites homoglyph impersonation.
        AND username ~ '^[A-Za-z0-9][A-Za-z0-9._-]*$'
    ),
    CONSTRAINT users_display_name_length CHECK (length(display_name) BETWEEN 1 AND 128),
    CONSTRAINT users_email_format CHECK (email IS NULL OR email ~ '^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$')
);

COMMENT ON TABLE users IS
    'Application-internal accounts. Independent of operating-system users.';
COMMENT ON COLUMN users.quota_bytes IS 'NULL means unlimited.';
COMMENT ON COLUMN users.used_bytes IS 'Denormalised; authoritative value is the sum of owned content.';

CREATE TRIGGER users_set_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Administrator lists and the "can the last admin be removed" check.
CREATE INDEX users_is_admin_idx ON users (is_admin) WHERE is_admin;
-- Admin user listing is ordered by name.
CREATE INDEX users_username_idx ON users (username);

-- ---------------------------------------------------------------------------
-- groups
-- ---------------------------------------------------------------------------
CREATE TABLE groups (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        CITEXT      NOT NULL UNIQUE,
    description TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT groups_name_format CHECK (
        length(name) BETWEEN 2 AND 64
        AND name ~ '^[A-Za-z0-9][A-Za-z0-9 ._-]*$'
    )
);

CREATE TRIGGER groups_set_updated_at
    BEFORE UPDATE ON groups
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE group_members (
    user_id   UUID        NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
    group_id  UUID        NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    -- 'member' or 'manager'. A manager may add and remove members without
    -- being a full administrator.
    role      TEXT        NOT NULL DEFAULT 'member' CHECK (role IN ('member', 'manager')),
    added_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    added_by  UUID        REFERENCES users(id) ON DELETE SET NULL,

    PRIMARY KEY (user_id, group_id)
);

-- "Who is in this group" — the reverse of the primary key's ordering.
CREATE INDEX group_members_group_id_idx ON group_members (group_id);

-- ---------------------------------------------------------------------------
-- sessions
-- ---------------------------------------------------------------------------
-- Server-side sessions rather than self-contained tokens. A JWT cannot be
-- revoked before it expires; an administrator disabling a compromised account
-- must be able to end its sessions immediately, and users must be able to see
-- and revoke their own.
CREATE TABLE sessions (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- Only a SHA-256 hash of the session token is stored. Read access to this
    -- table — a leaked backup, an SQL-injection bug elsewhere — must not yield
    -- usable credentials.
    token_hash        BYTEA       NOT NULL UNIQUE,

    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Absolute end of life; never extended.
    expires_at        TIMESTAMPTZ NOT NULL,
    -- Sliding idle deadline, extended as the session is used. NULL disables
    -- idle expiry.
    idle_expires_at   TIMESTAMPTZ,

    revoked_at        TIMESTAMPTZ,
    -- Recorded so the sessions list can say why a session ended.
    revoked_reason    TEXT,

    remember          BOOLEAN     NOT NULL DEFAULT FALSE,

    -- Shown in the active-sessions list so a user can recognise their own
    -- devices. Truncated on write; never trusted as an identifier.
    ip_address        INET,
    user_agent        TEXT        NOT NULL DEFAULT ''
);

COMMENT ON COLUMN sessions.token_hash IS
    'SHA-256 of the session token. The token itself is never stored.';

-- Lookup by token happens on every authenticated request; the UNIQUE
-- constraint above already provides that index.
CREATE INDEX sessions_user_id_idx ON sessions (user_id);
-- The cleanup job scans for expired rows.
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at) WHERE revoked_at IS NULL;

-- ---------------------------------------------------------------------------
-- Settings introduced by this migration
-- ---------------------------------------------------------------------------
INSERT INTO system_settings (key, value, description, is_public) VALUES
    ('auth.setup_completed', 'false',
     'Set once the first administrator exists. Setup cannot be re-run.', TRUE)
ON CONFLICT (key) DO NOTHING;
