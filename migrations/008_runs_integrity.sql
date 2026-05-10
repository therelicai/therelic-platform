-- 008: Augment runs with trace-integrity flags
--
-- Slice 1 of the production-readiness work makes the platform recompute
-- run metadata from the trace bytes (rather than trusting client headers).
-- We surface two new boolean columns:
--
--   integrity_chain — every action event carried a server-verifiable
--                     hmac field, so the trace is tamper-evident end-to-end.
--   truncated       — the parser stopped before reaching the run-end event,
--                     either because the stream was cut off or hit a
--                     malformed line. The run is still indexed but flagged.

ALTER TABLE runs
    ADD COLUMN IF NOT EXISTS integrity_chain BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS truncated       BOOLEAN NOT NULL DEFAULT FALSE;
