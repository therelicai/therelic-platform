-- 011: Distinguish "client claims chain" from "server verified chain"
--
-- Migration 008 added integrity_chain — a boolean saying "every action
-- event in this trace had an hmac field". That's a presence claim from
-- the client. Slice 6 adds platform-side recomputation: when
-- RELIC_TRACE_KEY is configured, the API derives the per-run HMAC key
-- and re-runs the chain over the uploaded bytes. chain_verified is
-- the boolean answer to "did the server's recomputation match?".
--
-- Splitting the two fields lets the dashboard show:
--   integrity_chain=true, chain_verified=true  → tamper-evident, proven
--   integrity_chain=true, chain_verified=false → tamper-evident, unverified
--                                                (no master secret on this server)
--   integrity_chain=false                      → unsealed trace, no claim
--
-- Existing rows default to false; they predate verification and we
-- have no master secret history to recompute them with.

ALTER TABLE runs
    ADD COLUMN IF NOT EXISTS chain_verified BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_runs_chain_verified ON runs (chain_verified)
    WHERE chain_verified = TRUE;
