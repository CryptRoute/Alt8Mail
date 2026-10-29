-- Tenant-managed storage connector credentials and per-user default storage.
--
-- OAuth client secrets and S3 secret keys are sealed with the workspace key
-- before they reach this table. Client IDs, endpoints, buckets and access keys
-- are safe to display to tenant administrators; secrets never are.
BEGIN;

CREATE TABLE IF NOT EXISTS storage_provider_configs (
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  provider_id text NOT NULL,
  client_id text NOT NULL DEFAULT '',
  client_secret_enc text NOT NULL DEFAULT '',
  config jsonb NOT NULL DEFAULT '{}'::jsonb,
  enabled boolean NOT NULL DEFAULT true,
  updated_by uuid REFERENCES users(id) ON DELETE SET NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, provider_id)
);

CREATE TABLE IF NOT EXISTS storage_defaults (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  provider_id text NOT NULL DEFAULT 'a8m',
  updated_at timestamptz NOT NULL DEFAULT now()
);

GRANT SELECT, INSERT, UPDATE, DELETE ON storage_provider_configs, storage_defaults TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0020_storage_credentials')
ON CONFLICT (version) DO NOTHING;

COMMIT;
