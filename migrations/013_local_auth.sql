-- 013: Local-auth support on the users table.
--
-- Adds two columns the auth provider interface needs across modes:
--
--   password_hash   bcrypt hash, only populated by the local auth
--                   adapter. NULL for Supabase / OIDC users.
--   auth_provider   which adapter created this user ('local',
--                   'supabase', 'oidc:<issuer>'). Lets the API
--                   refuse a local-login attempt against a
--                   Supabase-created user, and vice versa.
--
-- Idempotent: re-running on an already-migrated install is a no-op.

ALTER TABLE users ADD COLUMN IF NOT EXISTS password_hash TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS auth_provider TEXT NOT NULL DEFAULT 'supabase';

-- Existing Supabase deployments default to 'supabase' so the upgrade
-- from a pre-WS-2 install doesn't accidentally lock anyone out. New
-- local-auth installs override this when they create their admin via
-- the first-boot bootstrap in cmd/relic-api/main.go.

CREATE INDEX IF NOT EXISTS idx_users_email_provider ON users(email, auth_provider);
