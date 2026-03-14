-- 007: Row-Level Security Policies
-- Ensures data isolation between organizations at the database level.

-- Enable RLS on all tables
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

-- Helper: extract org_id from the current JWT
CREATE OR REPLACE FUNCTION auth.org_id()
RETURNS TEXT AS $$
  SELECT coalesce(
    current_setting('request.jwt.claims', true)::jsonb -> 'app_metadata' ->> 'org_id',
    ''
  );
$$ LANGUAGE sql STABLE;

-- Helper: extract user id from the current JWT
CREATE OR REPLACE FUNCTION auth.uid_text()
RETURNS TEXT AS $$
  SELECT coalesce(
    current_setting('request.jwt.claims', true)::jsonb ->> 'sub',
    ''
  );
$$ LANGUAGE sql STABLE;

-- Organizations: users can only see their own org
CREATE POLICY org_select ON organizations FOR SELECT
  USING (id::text = auth.org_id());
CREATE POLICY org_update ON organizations FOR UPDATE
  USING (id::text = auth.org_id());

-- Users: can see users in their own org
CREATE POLICY users_select ON users FOR SELECT
  USING (org_id::text = auth.org_id());
CREATE POLICY users_insert ON users FOR INSERT
  WITH CHECK (org_id::text = auth.org_id());

-- API Keys: scoped to org
CREATE POLICY api_keys_select ON api_keys FOR SELECT
  USING (org_id::text = auth.org_id());
CREATE POLICY api_keys_insert ON api_keys FOR INSERT
  WITH CHECK (org_id::text = auth.org_id());
CREATE POLICY api_keys_update ON api_keys FOR UPDATE
  USING (org_id::text = auth.org_id());

-- Runs: scoped to org
CREATE POLICY runs_select ON runs FOR SELECT
  USING (org_id::text = auth.org_id());
CREATE POLICY runs_insert ON runs FOR INSERT
  WITH CHECK (org_id::text = auth.org_id());
CREATE POLICY runs_delete ON runs FOR DELETE
  USING (org_id::text = auth.org_id());

-- Agents: scoped to org
CREATE POLICY agents_select ON agents FOR SELECT
  USING (org_id::text = auth.org_id());
CREATE POLICY agents_insert ON agents FOR INSERT
  WITH CHECK (org_id::text = auth.org_id());
CREATE POLICY agents_update ON agents FOR UPDATE
  USING (org_id::text = auth.org_id());

-- Proposals: scoped to org
CREATE POLICY proposals_select ON proposals FOR SELECT
  USING (org_id::text = auth.org_id());
CREATE POLICY proposals_update ON proposals FOR UPDATE
  USING (org_id::text = auth.org_id());

-- Audit events: scoped to org
CREATE POLICY audit_select ON audit_events FOR SELECT
  USING (org_id::text = auth.org_id());
CREATE POLICY audit_insert ON audit_events FOR INSERT
  WITH CHECK (org_id::text = auth.org_id());

-- Invitations: scoped to org
CREATE POLICY invitations_select ON invitations FOR SELECT
  USING (org_id::text = auth.org_id());
CREATE POLICY invitations_insert ON invitations FOR INSERT
  WITH CHECK (org_id::text = auth.org_id());

-- Capability listings: public read, org-scoped write
CREATE POLICY listings_select ON capability_listings FOR SELECT
  USING (true);
CREATE POLICY listings_insert ON capability_listings FOR INSERT
  WITH CHECK (org_id::text = auth.org_id());
CREATE POLICY listings_update ON capability_listings FOR UPDATE
  USING (org_id::text = auth.org_id());
CREATE POLICY listings_delete ON capability_listings FOR DELETE
  USING (org_id::text = auth.org_id());

-- Bilateral agreements: both parties can see
CREATE POLICY agreements_select ON bilateral_agreements FOR SELECT
  USING (caller_org_id::text = auth.org_id() OR provider_org_id::text = auth.org_id());

-- Transactions: both parties can see
CREATE POLICY transactions_select ON transactions FOR SELECT
  USING (caller_org_id::text = auth.org_id() OR provider_org_id::text = auth.org_id());

-- Allow the service role (API server) to bypass RLS
-- The Go API uses a service role connection, so these are needed:
ALTER TABLE organizations FORCE ROW LEVEL SECURITY;
ALTER TABLE users FORCE ROW LEVEL SECURITY;
