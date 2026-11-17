-- Compatibility feature state during the reference-to-Go migration.
-- One JSON document per user is intentionally scoped behind the Go API while
-- normalized tables remain the target for mail/workspace data. The document is
-- tenant-scoped by user_id and is never exposed cross-tenant.
BEGIN;

CREATE TABLE IF NOT EXISTS app_states (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  state jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS app_states_tenant_idx ON app_states (tenant_id);
GRANT SELECT, INSERT, UPDATE, DELETE ON app_states TO a8m_app;
INSERT INTO schema_migrations (version) VALUES ('0002_app_state')
ON CONFLICT (version) DO NOTHING;

COMMIT;
