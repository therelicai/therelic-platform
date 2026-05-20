-- 017: Evidence pack configuration.
--
-- Per-org settings for the evidence-pack export. v0 uses a single
-- platform-wide HMAC key (RELIC_EVIDENCE_KEY); this table lets a
-- future v1 store a per-org GPG public key so auditors can verify
-- with their own key + the customer's public key.
--
-- Idempotent.

CREATE TABLE IF NOT EXISTS evidence_config (
  org_id          UUID PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
  gpg_public_key  TEXT,                         -- ASCII-armored
  gpg_key_id      TEXT,                         -- short hex id for display
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
