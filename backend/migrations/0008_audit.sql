-- 0008_audit: the queryable record of security-relevant events.
--
-- Separate from the structured log deliberately. A log is for diagnosing what
-- the software did; an audit trail is for answering "who did this, and when",
-- which is a question somebody asks months later under pressure. Those need
-- different retention, different access control, and — most importantly —
-- the audit trail must survive log rotation.

-- The vocabulary is a text column rather than an enum, so a new event kind is
-- a code change rather than a migration. The trade is that a typo is not caught
-- by the database; the constants in internal/audit are the single source, and
-- nothing writes an event without going through them.
CREATE TABLE audit_events (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    -- What happened, e.g. "auth.login", "node.delete", "share.grant".
    action      TEXT        NOT NULL CHECK (length(action) BETWEEN 1 AND 64),

    -- Whether it succeeded. A failed attempt is often the interesting one.
    outcome     TEXT        NOT NULL DEFAULT 'success'
                            CHECK (outcome IN ('success', 'failure', 'denied')),

    -- Who did it. SET NULL rather than CASCADE: deleting an account must not
    -- erase the record of what it did, which is the whole point of an audit
    -- trail. actor_label preserves a readable name for exactly that case.
    actor_id    UUID        REFERENCES users(id) ON DELETE SET NULL,
    actor_label TEXT        NOT NULL DEFAULT '',

    -- What it was done to. Also SET NULL: the record of a deletion is most
    -- useful precisely when the thing is gone.
    target_id   UUID,
    target_type TEXT        NOT NULL DEFAULT '',
    target_label TEXT       NOT NULL DEFAULT '',

    -- Where from. Stored as text rather than INET so a proxy chain or an
    -- unparseable value is recorded as-is rather than rejected — losing the
    -- event would be worse than storing an odd string.
    ip          TEXT        NOT NULL DEFAULT '',
    user_agent  TEXT        NOT NULL DEFAULT '',
    request_id  TEXT        NOT NULL DEFAULT '',

    -- Anything else worth keeping, per action. Never credentials: the writing
    -- layer has no path that puts a password, token, or hash in here.
    detail      JSONB       NOT NULL DEFAULT '{}',

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE audit_events IS
    'Security-relevant events. Survives account deletion; never contains secrets.';

-- The administrator's view: newest first, optionally filtered.
CREATE INDEX audit_events_time_idx ON audit_events (created_at DESC);
CREATE INDEX audit_events_actor_idx ON audit_events (actor_id, created_at DESC);
CREATE INDEX audit_events_action_idx ON audit_events (action, created_at DESC);
CREATE INDEX audit_events_target_idx ON audit_events (target_id, created_at DESC)
    WHERE target_id IS NOT NULL;
-- "Show me everything that was refused", the query that finds an attack.
CREATE INDEX audit_events_outcome_idx ON audit_events (outcome, created_at DESC)
    WHERE outcome <> 'success';
