-- 006: Auth Sync, Audit Log, Invitations, Schema Improvements
-- Fixes the critical auth/org binding gap: new Supabase Auth signups
-- automatically get an organization, a users row, and app_metadata.org_id set.

-- Add updated_at columns
ALTER TABLE organizations ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE users ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE agents ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- Add scopes to API keys for permission control
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS scopes TEXT[] NOT NULL DEFAULT '{"full-access"}';

-- Add policy storage to agents
ALTER TABLE agents ADD COLUMN IF NOT EXISTS policy_yaml TEXT;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS policy_updated_at TIMESTAMPTZ;

-- Auto-update updated_at triggers
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_organizations_updated_at
  BEFORE UPDATE ON organizations
  FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TRIGGER trg_users_updated_at
  BEFORE UPDATE ON users
  FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TRIGGER trg_agents_updated_at
  BEFORE UPDATE ON agents
  FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- Audit events table
CREATE TABLE audit_events (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  user_id     TEXT,
  action      TEXT NOT NULL,
  resource    TEXT NOT NULL,
  resource_id TEXT,
  metadata    JSONB NOT NULL DEFAULT '{}',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_events_org ON audit_events(org_id, created_at DESC);
CREATE INDEX idx_audit_events_action ON audit_events(org_id, action);

-- Invitations table
CREATE TABLE invitations (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  email       TEXT NOT NULL,
  role        TEXT NOT NULL DEFAULT 'member',
  invited_by  TEXT,
  token       TEXT UNIQUE NOT NULL,
  accepted_at TIMESTAMPTZ,
  expires_at  TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '7 days'),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_invitations_token ON invitations(token) WHERE accepted_at IS NULL;
CREATE INDEX idx_invitations_org ON invitations(org_id);

-- Function: create org and user on Supabase Auth signup
-- This fires as a trigger on auth.users INSERT.
-- It creates an organization (using the email prefix as name),
-- a users row, and sets app_metadata.org_id on the JWT.
CREATE OR REPLACE FUNCTION handle_new_user()
RETURNS TRIGGER AS $$
DECLARE
  new_org_id UUID;
  org_name TEXT;
  org_slug TEXT;
BEGIN
  -- Derive org name from email (e.g., "richard" from "richard@company.com")
  org_name := split_part(NEW.email, '@', 1);
  org_slug := lower(regexp_replace(org_name, '[^a-z0-9]', '-', 'g'));
  -- Ensure slug uniqueness by appending random suffix
  org_slug := org_slug || '-' || substr(md5(random()::text), 1, 6);

  -- Create the organization
  INSERT INTO public.organizations (name, slug)
  VALUES (org_name || '''s Organization', org_slug)
  RETURNING id INTO new_org_id;

  -- Create the user record
  INSERT INTO public.users (id, org_id, email, role)
  VALUES (NEW.id, new_org_id, NEW.email, 'admin');

  -- Set org_id in the JWT app_metadata so the auth middleware can read it
  UPDATE auth.users
  SET raw_app_meta_data = COALESCE(raw_app_meta_data, '{}'::jsonb) || jsonb_build_object('org_id', new_org_id::text)
  WHERE id = NEW.id;

  RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

-- Create the trigger on auth.users
DROP TRIGGER IF EXISTS on_auth_user_created ON auth.users;
CREATE TRIGGER on_auth_user_created
  AFTER INSERT ON auth.users
  FOR EACH ROW EXECUTE FUNCTION handle_new_user();
