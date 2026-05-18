-- RLS / 001: Row-level security policies. Opt-in via RELIC_RLS_ENABLED.
--
-- Defense-in-depth, not the primary tenant boundary. The Go API
-- already filters every query by org_id at the application layer
-- (see internal/api/audit.go's requireOrg helper). RLS adds a second
-- line of defense IF the caller sets `request.jwt.claims` via
-- `SET LOCAL` per request (or if you connect with pgAdmin / analytics
-- tooling under an unprivileged role).
--
-- Originally migrations/007_rls_policies.sql before being moved into
-- the opt-in folder. Every statement here is idempotent so re-runs
-- (including upgrades from the pre-split layout) are safe.

-- Enable RLS on all tables (idempotent: no error if already enabled).
ALTER TABLE organizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE agents ENABLE ROW LEVEL SECURITY;
ALTER TABLE proposals ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE invitations ENABLE ROW LEVEL SECURITY;
ALTER TABLE capability_listings ENABLE ROW LEVEL SECURITY;
ALTER TABLE bilateral_agreements ENABLE ROW LEVEL SECURITY;
ALTER TABLE transactions ENABLE ROW LEVEL SECURITY;

-- Helper: extract org_id from the current JWT claims. The Go API sets
-- this via `SET LOCAL request.jwt.claims = '...'` per request after it
-- verifies the bearer token through its AuthProvider adapter. Works
-- identically whether the JWT was issued by Supabase, our LocalAuth
-- adapter, or an OIDC IdP, as long as the claims contain
-- app_metadata.org_id.
CREATE OR REPLACE FUNCTION public.get_org_id()
RETURNS TEXT AS $$
  SELECT coalesce(
    current_setting('request.jwt.claims', true)::jsonb -> 'app_metadata' ->> 'org_id',
    ''
  );
$$ LANGUAGE sql STABLE;

CREATE OR REPLACE FUNCTION public.get_uid_text()
RETURNS TEXT AS $$
  SELECT coalesce(
    current_setting('request.jwt.claims', true)::jsonb ->> 'sub',
    ''
  );
$$ LANGUAGE sql STABLE;

-- Organizations
DROP POLICY IF EXISTS org_select ON organizations;
CREATE POLICY org_select ON organizations FOR SELECT
  USING (id::text = public.get_org_id());
DROP POLICY IF EXISTS org_update ON organizations;
CREATE POLICY org_update ON organizations FOR UPDATE
  USING (id::text = public.get_org_id());

-- Users
DROP POLICY IF EXISTS users_select ON users;
CREATE POLICY users_select ON users FOR SELECT
  USING (org_id::text = public.get_org_id());
DROP POLICY IF EXISTS users_insert ON users;
CREATE POLICY users_insert ON users FOR INSERT
  WITH CHECK (org_id::text = public.get_org_id());

-- API Keys
DROP POLICY IF EXISTS api_keys_select ON api_keys;
CREATE POLICY api_keys_select ON api_keys FOR SELECT
  USING (org_id::text = public.get_org_id());
DROP POLICY IF EXISTS api_keys_insert ON api_keys;
CREATE POLICY api_keys_insert ON api_keys FOR INSERT
  WITH CHECK (org_id::text = public.get_org_id());
DROP POLICY IF EXISTS api_keys_update ON api_keys;
CREATE POLICY api_keys_update ON api_keys FOR UPDATE
  USING (org_id::text = public.get_org_id());

-- Runs
DROP POLICY IF EXISTS runs_select ON runs;
CREATE POLICY runs_select ON runs FOR SELECT
  USING (org_id::text = public.get_org_id());
DROP POLICY IF EXISTS runs_insert ON runs;
CREATE POLICY runs_insert ON runs FOR INSERT
  WITH CHECK (org_id::text = public.get_org_id());
DROP POLICY IF EXISTS runs_delete ON runs;
CREATE POLICY runs_delete ON runs FOR DELETE
  USING (org_id::text = public.get_org_id());

-- Agents
DROP POLICY IF EXISTS agents_select ON agents;
CREATE POLICY agents_select ON agents FOR SELECT
  USING (org_id::text = public.get_org_id());
DROP POLICY IF EXISTS agents_insert ON agents;
CREATE POLICY agents_insert ON agents FOR INSERT
  WITH CHECK (org_id::text = public.get_org_id());
DROP POLICY IF EXISTS agents_update ON agents;
CREATE POLICY agents_update ON agents FOR UPDATE
  USING (org_id::text = public.get_org_id());

-- Proposals (INSERT + DELETE added in 002_rls_completeness)
DROP POLICY IF EXISTS proposals_select ON proposals;
CREATE POLICY proposals_select ON proposals FOR SELECT
  USING (org_id::text = public.get_org_id());
DROP POLICY IF EXISTS proposals_update ON proposals;
CREATE POLICY proposals_update ON proposals FOR UPDATE
  USING (org_id::text = public.get_org_id());

-- Audit events
DROP POLICY IF EXISTS audit_select ON audit_events;
CREATE POLICY audit_select ON audit_events FOR SELECT
  USING (org_id::text = public.get_org_id());
DROP POLICY IF EXISTS audit_insert ON audit_events;
CREATE POLICY audit_insert ON audit_events FOR INSERT
  WITH CHECK (org_id::text = public.get_org_id());

-- Invitations
DROP POLICY IF EXISTS invitations_select ON invitations;
CREATE POLICY invitations_select ON invitations FOR SELECT
  USING (org_id::text = public.get_org_id());
DROP POLICY IF EXISTS invitations_insert ON invitations;
CREATE POLICY invitations_insert ON invitations FOR INSERT
  WITH CHECK (org_id::text = public.get_org_id());

-- Capability listings: public read, org-scoped write.
DROP POLICY IF EXISTS listings_select ON capability_listings;
CREATE POLICY listings_select ON capability_listings FOR SELECT
  USING (true);
DROP POLICY IF EXISTS listings_insert ON capability_listings;
CREATE POLICY listings_insert ON capability_listings FOR INSERT
  WITH CHECK (org_id::text = public.get_org_id());
DROP POLICY IF EXISTS listings_update ON capability_listings;
CREATE POLICY listings_update ON capability_listings FOR UPDATE
  USING (org_id::text = public.get_org_id());
DROP POLICY IF EXISTS listings_delete ON capability_listings;
CREATE POLICY listings_delete ON capability_listings FOR DELETE
  USING (org_id::text = public.get_org_id());

-- Bilateral agreements: both parties can see.
DROP POLICY IF EXISTS agreements_select ON bilateral_agreements;
CREATE POLICY agreements_select ON bilateral_agreements FOR SELECT
  USING (caller_org_id::text = public.get_org_id() OR provider_org_id::text = public.get_org_id());

-- Transactions: both parties can see.
DROP POLICY IF EXISTS transactions_select ON transactions;
CREATE POLICY transactions_select ON transactions FOR SELECT
  USING (caller_org_id::text = public.get_org_id() OR provider_org_id::text = public.get_org_id());

-- Force RLS on the tenancy roots so privileged service roles still
-- get filtered (002_rls_completeness extends this to every tenant
-- table).
ALTER TABLE organizations FORCE ROW LEVEL SECURITY;
ALTER TABLE users FORCE ROW LEVEL SECURITY;
