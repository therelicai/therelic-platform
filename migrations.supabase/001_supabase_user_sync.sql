-- Supabase-only: bind new auth.users signups to Relic orgs + users.
--
-- This migration runs ONLY when RELIC_AUTH_MODE=supabase. It depends
-- on the Supabase `auth.users` table existing (Supabase provisions it
-- automatically). For non-Supabase deployments (local-auth, OIDC),
-- user provisioning happens through the LocalAuth / OIDCAuth Go
-- adapters at signup time, and this trigger is never installed.
--
-- Originally lived in 006_auth_sync_audit.sql alongside the core
-- audit/invitations schema; split out so the core schema is portable.

CREATE OR REPLACE FUNCTION handle_new_user()
RETURNS TRIGGER AS $$
DECLARE
  new_org_id UUID;
  org_name TEXT;
  org_slug TEXT;
BEGIN
  org_name := split_part(NEW.email, '@', 1);
  org_slug := lower(regexp_replace(org_name, '[^a-z0-9]', '-', 'g'));
  org_slug := org_slug || '-' || substr(md5(random()::text), 1, 6);

  INSERT INTO public.organizations (name, slug)
  VALUES (org_name || '''s Organization', org_slug)
  RETURNING id INTO new_org_id;

  INSERT INTO public.users (id, org_id, email, role)
  VALUES (NEW.id, new_org_id, NEW.email, 'admin');

  -- Set org_id in JWT app_metadata so the platform's auth middleware
  -- (and the RLS get_org_id() function, if RLS is enabled) can read it.
  UPDATE auth.users
  SET raw_app_meta_data = COALESCE(raw_app_meta_data, '{}'::jsonb) || jsonb_build_object('org_id', new_org_id::text)
  WHERE id = NEW.id;

  RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

DROP TRIGGER IF EXISTS on_auth_user_created ON auth.users;
CREATE TRIGGER on_auth_user_created
  AFTER INSERT ON auth.users
  FOR EACH ROW EXECUTE FUNCTION handle_new_user();
