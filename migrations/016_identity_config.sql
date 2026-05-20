-- 016: Identity configuration tables.
--
-- Adds three surfaces an org admin manages from the SPA's
-- /settings/identity page:
--
--   sso_configs       — per-org OIDC/SAML config. Client secret is
--                       encrypted at rest with RELIC_SECRETS_KEY.
--   scim_tokens       — bearer tokens minted for SCIM provisioning.
--                       Stored as HMAC-SHA256 hashes; the plaintext
--                       is shown once at creation and never again.
--   identity_invites  — pending email-based invites with a server-
--                       side accept token.
--   sessions          — active session records keyed by JWT jti.
--                       Lets an admin revoke individual sessions or
--                       all sessions for a user.
--
-- Idempotent: re-running on an already-migrated install is a no-op.

CREATE TABLE IF NOT EXISTS sso_configs (
  org_id              UUID PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
  provider            TEXT NOT NULL,
  discovery_url       TEXT,
  client_id           TEXT,
  client_secret_enc   BYTEA,
  redirect_url        TEXT,
  scopes              TEXT[],
  default_role        TEXT NOT NULL DEFAULT 'member',
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS scim_tokens (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id       UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  token_hash   TEXT UNIQUE NOT NULL,
  token_prefix TEXT NOT NULL,
  name         TEXT NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_at   TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_scim_tokens_org ON scim_tokens(org_id) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS identity_invites (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  email         TEXT NOT NULL,
  role          TEXT NOT NULL DEFAULT 'member',
  accept_token  TEXT UNIQUE NOT NULL,
  expires_at    TIMESTAMPTZ NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  accepted_at   TIMESTAMPTZ,
  cancelled_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_identity_invites_org_email
  ON identity_invites(org_id, email)
  WHERE accepted_at IS NULL AND cancelled_at IS NULL;

CREATE TABLE IF NOT EXISTS sessions (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id       UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  jti          TEXT UNIQUE NOT NULL,
  user_agent   TEXT,
  ip           INET,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at   TIMESTAMPTZ NOT NULL,
  revoked_at   TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_sessions_user_active
  ON sessions(user_id, last_seen_at DESC)
  WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_sessions_org_active
  ON sessions(org_id, last_seen_at DESC)
  WHERE revoked_at IS NULL;
