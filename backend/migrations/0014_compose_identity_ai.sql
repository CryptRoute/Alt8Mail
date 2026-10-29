-- Compose/identity/AI support:
--   * app-password revocation history and expiry enforcement
--   * self-service sending identities with external-address verification
--   * tenant-scoped AI assistant configuration and cached summaries/translations
--   * HTML bodies on queued outbox items
BEGIN;

ALTER TABLE app_passwords ADD COLUMN IF NOT EXISTS revoked_at timestamptz;
CREATE INDEX IF NOT EXISTS app_passwords_active_idx
  ON app_passwords (user_id) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS mail_identities (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  email citext NOT NULL,
  display_name text NOT NULL DEFAULT '',
  reply_to citext,
  signature text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'verified')),
  verification_token_hash text,
  verification_expires_at timestamptz,
  verification_sent_at timestamptz,
  verified_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, email)
);
CREATE INDEX IF NOT EXISTS mail_identities_user_idx ON mail_identities (user_id, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS mail_identities_token_idx
  ON mail_identities (verification_token_hash) WHERE verification_token_hash IS NOT NULL;

CREATE TABLE IF NOT EXISTS tenant_ai_settings (
  tenant_id uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  enabled boolean NOT NULL DEFAULT false,
  provider text NOT NULL DEFAULT 'openai' CHECK (provider IN ('openai', 'anthropic')),
  endpoint text NOT NULL DEFAULT '',
  model text NOT NULL DEFAULT '',
  api_key_sealed text NOT NULL DEFAULT '',
  max_tokens integer NOT NULL DEFAULT 1024,
  temperature numeric(3,2) NOT NULL DEFAULT 0.70,
  system_prompt text NOT NULL DEFAULT '',
  timeout_seconds integer NOT NULL DEFAULT 30,
  updated_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS mail_ai_cache (
  message_id text NOT NULL REFERENCES mail_messages(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('summary', 'translation')),
  language text NOT NULL,
  content text NOT NULL,
  provider text NOT NULL DEFAULT '',
  model text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (message_id, user_id, kind, language)
);
CREATE INDEX IF NOT EXISTS mail_ai_cache_user_idx ON mail_ai_cache (user_id, updated_at DESC);

ALTER TABLE outbox ADD COLUMN IF NOT EXISTS body_html text NOT NULL DEFAULT '';

GRANT SELECT, INSERT, UPDATE, DELETE ON app_passwords TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON mail_identities TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_ai_settings TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON mail_ai_cache TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0014_compose_identity_ai')
ON CONFLICT (version) DO NOTHING;

COMMIT;
