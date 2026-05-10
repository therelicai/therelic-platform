-- 009: Finish the RLS rollout from migration 007
--
-- RLS is defense-in-depth, not the primary tenant boundary. The Go
-- service connects with a privileged role and filters everything by
-- org_id at the application layer (see internal/api/audit.go's
-- requireOrg helper). RLS adds a second line of defense IF the
-- caller sets the request.jwt.claims setting — useful for analytics
-- consoles or pgAdmin sessions but currently unused by the API.
--
-- This migration closes three gaps in 007:
--
--   1. agent_baselines had no RLS at all.
--   2. proposals was missing INSERT and DELETE policies, so the
--      governance worker could silently fail to write rows once
--      RLS is in force.
--   3. FORCE ROW LEVEL SECURITY was only applied to organizations
--      and users; every other tenant table escaped enforcement
--      when the API server connects with its privileged role.
--
-- Each statement is idempotent so re-running this migration in
-- environments that already partially applied 007 is safe.

-- ---------------------------------------------------------------------------
-- agent_baselines
-- ---------------------------------------------------------------------------
ALTER TABLE agent_baselines ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_baselines FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS agent_baselines_select ON agent_baselines;
CREATE POLICY agent_baselines_select ON agent_baselines FOR SELECT
  USING (EXISTS (
    SELECT 1 FROM agents a
    WHERE a.id = agent_baselines.agent_id
      AND a.org_id::text = public.get_org_id()
  ));

DROP POLICY IF EXISTS agent_baselines_insert ON agent_baselines;
CREATE POLICY agent_baselines_insert ON agent_baselines FOR INSERT
  WITH CHECK (EXISTS (
    SELECT 1 FROM agents a
    WHERE a.id = agent_baselines.agent_id
      AND a.org_id::text = public.get_org_id()
  ));

-- ---------------------------------------------------------------------------
-- proposals: add the missing INSERT and DELETE policies
-- ---------------------------------------------------------------------------
DROP POLICY IF EXISTS proposals_insert ON proposals;
CREATE POLICY proposals_insert ON proposals FOR INSERT
  WITH CHECK (org_id::text = public.get_org_id());

DROP POLICY IF EXISTS proposals_delete ON proposals;
CREATE POLICY proposals_delete ON proposals FOR DELETE
  USING (org_id::text = public.get_org_id());

-- ---------------------------------------------------------------------------
-- FORCE RLS on every tenant table (was only on organizations + users)
-- ---------------------------------------------------------------------------
ALTER TABLE api_keys FORCE ROW LEVEL SECURITY;
ALTER TABLE runs FORCE ROW LEVEL SECURITY;
ALTER TABLE agents FORCE ROW LEVEL SECURITY;
ALTER TABLE proposals FORCE ROW LEVEL SECURITY;
ALTER TABLE audit_events FORCE ROW LEVEL SECURITY;
ALTER TABLE invitations FORCE ROW LEVEL SECURITY;
ALTER TABLE capability_listings FORCE ROW LEVEL SECURITY;
ALTER TABLE bilateral_agreements FORCE ROW LEVEL SECURITY;
ALTER TABLE transactions FORCE ROW LEVEL SECURITY;

-- ---------------------------------------------------------------------------
-- audit_events: also allow DELETE for retention worker (Slice 7).
-- Without this, the retention sweep can't trim expired rows under RLS.
-- ---------------------------------------------------------------------------
DROP POLICY IF EXISTS audit_delete ON audit_events;
CREATE POLICY audit_delete ON audit_events FOR DELETE
  USING (org_id::text = public.get_org_id());

-- ---------------------------------------------------------------------------
-- runs: existing 007 covered SELECT/INSERT/DELETE. Add UPDATE for
-- correcting expiry timestamps and the integrity_chain/truncated
-- columns added in migration 008.
-- ---------------------------------------------------------------------------
DROP POLICY IF EXISTS runs_update ON runs;
CREATE POLICY runs_update ON runs FOR UPDATE
  USING (org_id::text = public.get_org_id());
