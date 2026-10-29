-- Alt8Mail System Control: separate infrastructure panel identity, RBAC,
-- authenticated sessions, API keys, notifications, webhooks and audit.
BEGIN;

CREATE SCHEMA IF NOT EXISTS control;

CREATE TABLE IF NOT EXISTS control.admin_users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email citext NOT NULL UNIQUE,
  name text NOT NULL DEFAULT '',
  password_hash text NOT NULL DEFAULT '',
  role text NOT NULL DEFAULT 'readonly'
    CHECK (role IN ('superadmin', 'admin', 'operator', 'readonly')),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  totp_secret text NOT NULL DEFAULT '',
  totp_pending_secret text NOT NULL DEFAULT '',
  totp_enabled boolean NOT NULL DEFAULT false,
  session_ttl_seconds integer NOT NULL DEFAULT 28800
    CHECK (session_ttl_seconds BETWEEN 900 AND 604800),
  failed_attempts integer NOT NULL DEFAULT 0,
  failed_window_start timestamptz,
  locked_until timestamptz,
  last_login_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS control_admin_users_status_idx ON control.admin_users (status);

CREATE TABLE IF NOT EXISTS control.groups (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  permissions text[] NOT NULL DEFAULT '{}',
  created_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS control_groups_name_idx ON control.groups (lower(name));

CREATE TABLE IF NOT EXISTS control.group_members (
  group_id uuid NOT NULL REFERENCES control.groups(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES control.admin_users(id) ON DELETE CASCADE,
  added_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, user_id)
);
CREATE INDEX IF NOT EXISTS control_group_members_user_idx ON control.group_members (user_id);

CREATE TABLE IF NOT EXISTS control.sessions (
  id text PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES control.admin_users(id) ON DELETE CASCADE,
  csrf_token text NOT NULL,
  ip inet,
  user_agent text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS control_sessions_user_idx ON control.sessions (user_id, expires_at DESC);
CREATE INDEX IF NOT EXISTS control_sessions_expiry_idx ON control.sessions (expires_at);

CREATE TABLE IF NOT EXISTS control.auth_challenges (
  id text PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES control.admin_users(id) ON DELETE CASCADE,
  attempts integer NOT NULL DEFAULT 0,
  ip inet,
  user_agent text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS control_auth_challenges_expiry_idx ON control.auth_challenges (expires_at);

CREATE TABLE IF NOT EXISTS control.audit_log (
  id bigserial PRIMARY KEY,
  actor_id uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  actor_email citext NOT NULL DEFAULT '',
  action text NOT NULL,
  target text NOT NULL DEFAULT '',
  detail text NOT NULL DEFAULT '',
  ip inet,
  user_agent text NOT NULL DEFAULT '',
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS control_audit_time_idx ON control.audit_log (created_at DESC);
CREATE INDEX IF NOT EXISTS control_audit_actor_idx ON control.audit_log (actor_id, created_at DESC);

CREATE TABLE IF NOT EXISTS control.settings (
  key text PRIMARY KEY,
  value jsonb NOT NULL DEFAULT '{}'::jsonb,
  updated_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO control.settings (key, value) VALUES
  ('server.hostname', to_jsonb(''::text)),
  ('server.webmail_url', to_jsonb(''::text)),
  ('server.dns_resolver', to_jsonb('1.1.1.1:53'::text)),
  ('server.mail_hostname', to_jsonb(''::text)),
  ('server.public_ipv4', to_jsonb(''::text)),
  ('server.public_ipv6', to_jsonb(''::text)),
  ('ssl.admin_email', to_jsonb(''::text)),
  ('ssl.warn_days', '14'::jsonb),
  ('ssl.renewal_installed', 'false'::jsonb),
  ('logs.retention_days', '365'::jsonb),
  ('backup.directory', to_jsonb('/var/backups/alt8mail'::text)),
  ('config.last_sync', 'null'::jsonb)
ON CONFLICT (key) DO NOTHING;

CREATE TABLE IF NOT EXISTS control.api_keys (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  prefix text NOT NULL UNIQUE,
  key_hash text NOT NULL UNIQUE,
  scopes text[] NOT NULL DEFAULT ARRAY['read']::text[],
  created_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  expires_at timestamptz,
  revoked_at timestamptz
);
CREATE INDEX IF NOT EXISTS control_api_keys_active_idx ON control.api_keys (revoked_at, expires_at);

CREATE TABLE IF NOT EXISTS control.notification_handles (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES control.admin_users(id) ON DELETE CASCADE,
  name text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('slack', 'discord', 'telegram', 'webhook', 'email')),
  config_sealed text NOT NULL DEFAULT '',
  events text[] NOT NULL DEFAULT '{}',
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS control_notification_handles_user_idx ON control.notification_handles (user_id);

CREATE TABLE IF NOT EXISTS control.notification_deliveries (
  id bigserial PRIMARY KEY,
  handle_id uuid REFERENCES control.notification_handles(id) ON DELETE SET NULL,
  event text NOT NULL,
  status text NOT NULL DEFAULT 'pending',
  detail text NOT NULL DEFAULT '',
  attempted_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS control_notification_deliveries_time_idx ON control.notification_deliveries (attempted_at DESC);

CREATE TABLE IF NOT EXISTS control.webhooks (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  url text NOT NULL,
  secret_sealed text NOT NULL DEFAULT '',
  events text[] NOT NULL DEFAULT '{}',
  enabled boolean NOT NULL DEFAULT true,
  created_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS control.webhook_deliveries (
  id bigserial PRIMARY KEY,
  webhook_id uuid REFERENCES control.webhooks(id) ON DELETE SET NULL,
  event text NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  status text NOT NULL DEFAULT 'pending',
  response_code integer,
  response_body text NOT NULL DEFAULT '',
  attempted_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS control_webhook_deliveries_time_idx ON control.webhook_deliveries (attempted_at DESC);

GRANT USAGE ON SCHEMA control TO alt8mail_control;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA control TO alt8mail_control;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA control TO alt8mail_control;

INSERT INTO schema_migrations (version) VALUES ('0015_control_core')
ON CONFLICT (version) DO NOTHING;

COMMIT;
