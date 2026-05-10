-- Seed data for local development

INSERT INTO organizations (id, name, slug, plan) VALUES
  ('00000000-0000-0000-0000-000000000001', 'Dev Org', 'dev-org', 'team');

INSERT INTO users (org_id, email, role) VALUES
  ('00000000-0000-0000-0000-000000000001', 'dev@therelic.dev', 'owner');

INSERT INTO api_keys (org_id, key_hash, key_prefix, name) VALUES
  ('00000000-0000-0000-0000-000000000001',
   'a3ae32a4d839195a3d546ebe78c79fd8bd6a673a8a22a8935cd938c0e0edc878',
   'rk_dev_tes',
   'Development Key');
-- The above hash is sha256("rk_dev_test_key_do_not_use_in_production").
-- Verify with: echo -n "rk_dev_test_key_do_not_use_in_production" | shasum -a 256
