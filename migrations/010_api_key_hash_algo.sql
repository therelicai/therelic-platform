-- 010: Tag API keys with the hash algorithm used to store them
--
-- The platform previously stored every api_key as a plain SHA-256 of
-- the secret. SHA-256 of a 64-bit-entropy random string is fine against
-- pre-image attacks, but a database leak then exposes every key to an
-- offline rainbow-table check. Slice 3 introduces HMAC-SHA256 with a
-- server pepper (RELIC_API_KEY_PEPPER) so a database dump alone is
-- worthless without also stealing the pepper.
--
-- This column records WHICH algorithm produced the stored hash so
-- ValidateAPIKey can dispatch correctly during the rollout window.
-- Existing rows default to 'sha256' (zero migration risk).

ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS hash_algo TEXT NOT NULL DEFAULT 'sha256';

-- Index isn't strictly required (we look up by key_hash, not algo) but
-- helps the retention sweep that's coming in Slice 7.
CREATE INDEX IF NOT EXISTS idx_api_keys_hash_algo ON api_keys (hash_algo);
