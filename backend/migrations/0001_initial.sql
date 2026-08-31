-- 0001_initial: extensions, shared helpers, and runtime settings.
--
-- This migration deliberately contains no user-facing tables. It establishes
-- the primitives every later migration relies on, so that the schema history
-- has a single, stable foundation.

-- citext gives case-insensitive comparison for usernames and email addresses
-- without scattering lower() around every query and losing index usage.
CREATE EXTENSION IF NOT EXISTS citext;

-- pg_trgm powers filename search. Choosing PostgreSQL over Elasticsearch is a
-- deliberate trade: adequate for millions of rows, and it costs no extra RAM
-- on a 4 GB board.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- gen_random_uuid() is built in from PostgreSQL 13 onward; pgcrypto is not
-- required. UUIDs are used for every externally visible identifier so that
-- sequential integers never leak record counts or permit enumeration.

-- Shared trigger function: keeps updated_at honest without relying on every
-- application code path to remember.
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;

-- Runtime-editable configuration. Environment variables define deployment
-- facts (where the database is, where storage lives); this table holds policy
-- an administrator can change from the UI without restarting the service.
CREATE TABLE system_settings (
    key         TEXT PRIMARY KEY,
    value       JSONB       NOT NULL,
    description TEXT        NOT NULL DEFAULT '',
    -- Settings marked non-public are never serialized to a non-admin client.
    is_public   BOOLEAN     NOT NULL DEFAULT FALSE,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by  UUID
);

COMMENT ON TABLE system_settings IS
    'Administrator-editable runtime policy. Deployment facts live in environment variables instead.';

CREATE TRIGGER system_settings_set_updated_at
    BEFORE UPDATE ON system_settings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

INSERT INTO system_settings (key, value, description, is_public) VALUES
    ('instance.name',            '"PiFiles"', 'Name shown in the interface and page titles.',        TRUE),
    ('instance.schema_version',  '1',         'Schema generation, bumped by structural migrations.', FALSE)
ON CONFLICT (key) DO NOTHING;
