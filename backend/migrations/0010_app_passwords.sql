-- Revocable, high-entropy credentials for CalDAV/CardDAV and other clients
-- that cannot complete interactive MFA. Tokens are shown once and stored as
-- SHA-256 hashes because the token itself carries 192 bits of entropy.
BEGIN;

CREATE TABLE IF NOT EXISTS app_passwords (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  label text NOT NULL DEFAULT '',
  password_hash text NOT NULL UNIQUE,
  last_used_at timestamptz,
  expires_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS app_passwords_user_idx ON app_passwords (user_id, created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON app_passwords TO meridian_app;

INSERT INTO schema_migrations (version) VALUES ('0010_app_passwords')
ON CONFLICT (version) DO NOTHING;

COMMIT;
