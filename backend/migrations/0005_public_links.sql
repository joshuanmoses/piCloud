-- 0005_public_links: tokenised access for people without an account.
--
-- One table covers both directions:
--
--   /s/<token>   share existing content outward
--   /u/<token>   collect content inward (a file request)
--
-- They are the same object — a token granting a permission set on a node to
-- an anonymous visitor — and the permission bitmask distinguishes them. Two
-- tables would mean writing the security-critical path twice: resolve the
-- token, check revoked, check expired, check the password, check the usage
-- limit. Having that logic exist exactly once is the point.

CREATE TABLE public_links (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id      UUID        NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,

    -- Only a SHA-256 hash of the token is stored. The token itself exists in
    -- memory once, long enough to build the URL shown to its creator, and is
    -- never recoverable afterwards. A leaked database backup therefore yields
    -- no usable links.
    token_hash   BYTEA       NOT NULL UNIQUE,

    -- What the visitor may do. Reuses the same bitmask as internal grants, so
    -- there is one permission vocabulary rather than two.
    permissions  INTEGER     NOT NULL CHECK (permissions > 0),

    -- Optional second factor, Argon2id. The link itself already carries 256
    -- bits of entropy; a password protects it after the URL has leaked.
    password_hash TEXT,

    expires_at   TIMESTAMPTZ,

    -- Usage cap. Counts downloads for a share link and uploads for a file
    -- request — the same "how many times may this be used" question.
    max_uses     INTEGER     CHECK (max_uses IS NULL OR max_uses > 0),
    use_count    INTEGER     NOT NULL DEFAULT 0 CHECK (use_count >= 0),

    -- Upload constraints. NULL means "fall back to the instance limit".
    max_upload_bytes   BIGINT CHECK (max_upload_bytes IS NULL OR max_upload_bytes > 0),
    -- Lowercase extensions including the dot, e.g. {'.pdf','.jpg'}. Empty
    -- means any type. This is a convenience for the person collecting files,
    -- never a security control: the server still sniffs content and never
    -- trusts an extension.
    allowed_extensions TEXT[] NOT NULL DEFAULT '{}',

    -- Shown on the portal page so a visitor knows what they are looking at.
    title        TEXT        NOT NULL DEFAULT '' CHECK (length(title) <= 120),
    message      TEXT        NOT NULL DEFAULT '' CHECK (length(message) <= 500),

    created_by   UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    revoked_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ
);

COMMENT ON TABLE public_links IS
    'Tokenised anonymous access. Covers both share links (/s) and file requests (/u).';
COMMENT ON COLUMN public_links.token_hash IS
    'SHA-256 of the token. The token is never stored.';
COMMENT ON COLUMN public_links.allowed_extensions IS
    'A convenience filter for the uploader, not a security control.';

CREATE TRIGGER public_links_set_updated_at
    BEFORE UPDATE ON public_links
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Token lookup happens on every portal request; the UNIQUE constraint above
-- provides that index.

-- "My public links" for the owner.
CREATE INDEX public_links_creator_idx ON public_links (created_by, created_at DESC);

-- Listing the links on one node, and cascading when it is deleted.
CREATE INDEX public_links_node_idx ON public_links (node_id);

-- The expiry sweep.
CREATE INDEX public_links_expiry_idx ON public_links (expires_at)
    WHERE expires_at IS NOT NULL AND revoked_at IS NULL;

-- ---------------------------------------------------------------------------
-- Settings introduced by this migration
-- ---------------------------------------------------------------------------
INSERT INTO system_settings (key, value, description, is_public) VALUES
    ('sharing.public_links_enabled', 'true',
     'Whether users may create links that work without an account.', TRUE),
    ('sharing.upload_requests_enabled', 'true',
     'Whether users may create file-request portals.', TRUE)
ON CONFLICT (key) DO NOTHING;
