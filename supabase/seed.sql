-- Seed data for local development

INSERT INTO organizations (id, name, slug, plan) VALUES
  ('00000000-0000-0000-0000-000000000001', 'Dev Org', 'dev-org', 'team');

INSERT INTO users (org_id, email, role) VALUES
  ('00000000-0000-0000-0000-000000000001', 'dev@therelic.dev', 'owner');

INSERT INTO api_keys (org_id, key_hash, key_prefix, name) VALUES
  ('00000000-0000-0000-0000-000000000001',
   'a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3',
   'rk_dev_tes',
   'Development Key');
-- The above hash corresponds to the plaintext: rk_dev_test_key_do_not_use_in_production
