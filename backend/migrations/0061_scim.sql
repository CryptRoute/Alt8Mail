-- SCIM 2.0 provisioning: identity attributes, external-id keys and the
-- per-tenant bearer tokens used by IdPs. Tokens are stored only as SHA-256
-- hashes; the raw value is shown once at creation/rotation time.
BEGIN;

ALTER TABLE users ADD COLUMN IF NOT EXISTS given_name text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS family_name text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS scim_external_id text;
ALTER TABLE users ADD COLUMN IF NOT EXISTS role_source text NOT NULL DEFAULT 'manual'
  CHECK (role_source IN ('manual','scim'));
CREATE UNIQUE INDEX IF NOT EXISTS users_tenant_scim_external_idx
  ON users (tenant_id, scim_external_id) WHERE scim_external_id IS NOT NULL;

ALTER TABLE groups ADD COLUMN IF NOT EXISTS scim_external_id text;
ALTER TABLE groups ADD COLUMN IF NOT EXISTS scim_role text NOT NULL DEFAULT ''
  CHECK (scim_role IN ('','admin','member','readonly'));
CREATE UNIQUE INDEX IF NOT EXISTS groups_tenant_scim_external_idx
  ON groups (tenant_id, scim_external_id) WHERE scim_external_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS scim_tokens (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name text NOT NULL,
  token_hash text NOT NULL UNIQUE,
  token_prefix text NOT NULL,
  created_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  expires_at timestamptz,
  revoked_at timestamptz
);
CREATE INDEX IF NOT EXISTS scim_tokens_tenant_idx ON scim_tokens (tenant_id);
GRANT SELECT, INSERT, UPDATE, DELETE ON scim_tokens TO a8m_app;
INSERT INTO schema_migrations (version) VALUES ('0061_scim')
ON CONFLICT (version) DO NOTHING;

COMMIT;
