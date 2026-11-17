-- Alt8Mail platform core schema (PostgreSQL 15+)
-- The system backend owns this schema. Dovecot/Postfix read only the mailbox and
-- alias mapping tables through the restricted a8m_mail role.
BEGIN;

CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS schema_migrations (
  version text PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS tenants (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug text NOT NULL UNIQUE,
  name text NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
  settings jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS mail_domains (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  domain citext NOT NULL UNIQUE,
  is_primary boolean NOT NULL DEFAULT false,
  dkim_selector text,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mail_domains_tenant_idx ON mail_domains (tenant_id);

CREATE TABLE IF NOT EXISTS users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  email citext NOT NULL UNIQUE,
  name text NOT NULL,
  initials text NOT NULL DEFAULT '',
  color text NOT NULL DEFAULT '#34557a',
  role text NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'admin', 'member', 'readonly')),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'invited')),
  password_hash text,
  password_changed_at timestamptz,
  mail_password_hash text,
  preferences jsonb NOT NULL DEFAULT '{}'::jsonb,
  security jsonb NOT NULL DEFAULT '{}'::jsonb,
  quota_bytes bigint NOT NULL DEFAULT 16106127360,
  used_bytes bigint NOT NULL DEFAULT 0,
  last_login_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS users_tenant_idx ON users (tenant_id);

CREATE TABLE IF NOT EXISTS user_aliases (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  email citext NOT NULL UNIQUE,
  label text NOT NULL,
  kind text NOT NULL DEFAULT 'alias' CHECK (kind IN ('primary', 'alias', 'shared')),
  can_send boolean NOT NULL DEFAULT true,
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS user_aliases_user_idx ON user_aliases (user_id);

CREATE TABLE IF NOT EXISTS mailboxes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  email citext NOT NULL UNIQUE,
  domain citext NOT NULL,
  local_part citext NOT NULL,
  maildir_path text NOT NULL,
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mailboxes_tenant_idx ON mailboxes (tenant_id);
CREATE INDEX IF NOT EXISTS mailboxes_domain_idx ON mailboxes (domain);

CREATE TABLE IF NOT EXISTS sessions (
  id text PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  csrf_token text NOT NULL,
  ip inet,
  user_agent text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions (user_id);
CREATE INDEX IF NOT EXISTS sessions_expiry_idx ON sessions (expires_at);

CREATE TABLE IF NOT EXISTS auth_challenges (
  id text PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  remember boolean NOT NULL DEFAULT true,
  attempts integer NOT NULL DEFAULT 0,
  ip inet,
  user_agent text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS auth_challenges_expiry_idx ON auth_challenges (expires_at);

CREATE TABLE IF NOT EXISTS recovery_codes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash text NOT NULL,
  used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS recovery_codes_user_idx ON recovery_codes (user_id);

CREATE TABLE IF NOT EXISTS webauthn_credentials (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  credential_id bytea NOT NULL UNIQUE,
  public_key bytea NOT NULL,
  sign_count bigint NOT NULL DEFAULT 0,
  transports text[] NOT NULL DEFAULT '{}',
  aaguid text,
  name text NOT NULL DEFAULT 'Passkey',
  created_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz
);
CREATE INDEX IF NOT EXISTS webauthn_user_idx ON webauthn_credentials (user_id);

CREATE TABLE IF NOT EXISTS trusted_devices (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash text NOT NULL UNIQUE,
  name text NOT NULL DEFAULT 'Trusted device',
  ip inet,
  user_agent text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz
);
CREATE INDEX IF NOT EXISTS trusted_devices_user_idx ON trusted_devices (user_id);

CREATE TABLE IF NOT EXISTS trusted_senders (
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  sender citext NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, sender)
);

-- A cache of the IMAP mail store used for fast threading/search and API reads.
-- Dovecot remains the canonical mailbox store. The worker fills this table.
CREATE TABLE IF NOT EXISTS mail_messages (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  mailbox_id uuid NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
  folder text NOT NULL,
  uid bigint,
  message_id text,
  thread_id text NOT NULL,
  in_reply_to text,
  subject text NOT NULL DEFAULT '',
  from_name text NOT NULL DEFAULT '',
  from_email citext NOT NULL,
  to_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  cc_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  bcc_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  snippet text NOT NULL DEFAULT '',
  body_text text NOT NULL DEFAULT '',
  body_html text NOT NULL DEFAULT '',
  sent_at timestamptz NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now(),
  size_bytes bigint NOT NULL DEFAULT 0,
  unread boolean NOT NULL DEFAULT true,
  starred boolean NOT NULL DEFAULT false,
  flagged boolean NOT NULL DEFAULT false,
  draft boolean NOT NULL DEFAULT false,
  external boolean NOT NULL DEFAULT false,
  classification text NOT NULL DEFAULT 'Internal',
  categories text[] NOT NULL DEFAULT '{}',
  attachments jsonb NOT NULL DEFAULT '[]'::jsonb,
  auth jsonb NOT NULL DEFAULT '{}'::jsonb,
  spam_report jsonb,
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (mailbox_id, folder, uid)
);
CREATE INDEX IF NOT EXISTS mail_messages_user_folder_idx ON mail_messages (user_id, folder, sent_at DESC);
CREATE INDEX IF NOT EXISTS mail_messages_thread_idx ON mail_messages (thread_id);
CREATE INDEX IF NOT EXISTS mail_messages_fts_idx ON mail_messages
  USING GIN (to_tsvector('english', coalesce(subject, '') || ' ' || coalesce(body_text, '') || ' ' || coalesce(from_email::text, '')));

CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  draft_id text,
  from_email citext NOT NULL,
  from_name text NOT NULL DEFAULT '',
  to_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  cc_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  bcc_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  subject text NOT NULL DEFAULT '',
  body_text text NOT NULL DEFAULT '',
  attachments jsonb NOT NULL DEFAULT '[]'::jsonb,
  thread_id text,
  in_reply_to text,
  priority text NOT NULL DEFAULT 'normal',
  receipt boolean NOT NULL DEFAULT false,
  delivery boolean NOT NULL DEFAULT false,
  save_copy boolean NOT NULL DEFAULT true,
  scheduled boolean NOT NULL DEFAULT false,
  send_at timestamptz NOT NULL,
  queued_at timestamptz NOT NULL DEFAULT now(),
  status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'sending', 'sent', 'cancelled', 'failed')),
  attempts integer NOT NULL DEFAULT 0,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS outbox_due_idx ON outbox (status, send_at);
CREATE INDEX IF NOT EXISTS outbox_user_idx ON outbox (user_id, status);

CREATE TABLE IF NOT EXISTS outbound_events (
  id bigserial PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  outbox_id text,
  message_id text,
  sent_at timestamptz NOT NULL DEFAULT now(),
  from_email citext NOT NULL,
  recipients integer NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS outbound_events_user_time_idx ON outbound_events (user_id, sent_at DESC);

CREATE TABLE IF NOT EXISTS undo_actions (
  token text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  action text NOT NULL,
  snapshot jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS undo_expiry_idx ON undo_actions (expires_at);

CREATE TABLE IF NOT EXISTS attachments (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  mime text NOT NULL,
  size_bytes bigint NOT NULL,
  sha256 text NOT NULL,
  stored_path text NOT NULL,
  previewable boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS attachments_user_idx ON attachments (user_id);

CREATE TABLE IF NOT EXISTS tasks (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title text NOT NULL,
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'done')),
  priority text NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
  due date,
  due_time time,
  notes text NOT NULL DEFAULT '',
  tags text[] NOT NULL DEFAULT '{}',
  assignee_email citext,
  created_by citext,
  source text NOT NULL DEFAULT 'manual',
  linked_thread_id text,
  linked_subject text,
  completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS tasks_user_idx ON tasks (user_id, status, due);

CREATE TABLE IF NOT EXISTS storage_providers (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  kind text NOT NULL,
  connected boolean NOT NULL DEFAULT false,
  account text NOT NULL DEFAULT '',
  used_bytes bigint NOT NULL DEFAULT 0,
  limit_bytes bigint NOT NULL DEFAULT 0,
  color text NOT NULL DEFAULT '#0f6cbd',
  status text NOT NULL DEFAULT 'disconnected',
  description text NOT NULL DEFAULT '',
  encrypted_tokens text,
  last_sync_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS storage_providers_user_idx ON storage_providers (user_id);

CREATE TABLE IF NOT EXISTS files (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider_id text NOT NULL,
  name text NOT NULL,
  type text NOT NULL,
  mime text NOT NULL,
  size_bytes bigint NOT NULL DEFAULT 0,
  path text NOT NULL DEFAULT '',
  owner_email citext,
  description text NOT NULL DEFAULT '',
  version integer NOT NULL DEFAULT 1,
  starred boolean NOT NULL DEFAULT false,
  locked boolean NOT NULL DEFAULT false,
  content_url text,
  attachment_id text REFERENCES attachments(id) ON DELETE SET NULL,
  shared_with text[] NOT NULL DEFAULT '{}',
  modified_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS files_user_idx ON files (user_id, modified_at DESC);

CREATE TABLE IF NOT EXISTS file_shares (
  token text PRIMARY KEY,
  file_id text NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  access text NOT NULL DEFAULT 'view' CHECK (access IN ('view', 'edit')),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz
);

CREATE TABLE IF NOT EXISTS sieve_profiles (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  revision integer NOT NULL DEFAULT 1,
  rules jsonb NOT NULL DEFAULT '[]'::jsonb,
  vacation jsonb NOT NULL DEFAULT '{}'::jsonb,
  script text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS audit_log (
  id bigserial PRIMARY KEY,
  tenant_id uuid REFERENCES tenants(id) ON DELETE SET NULL,
  user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  actor text NOT NULL DEFAULT '',
  action text NOT NULL,
  target text NOT NULL DEFAULT '',
  detail text NOT NULL DEFAULT '',
  ip inet,
  user_agent text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS audit_log_tenant_time_idx ON audit_log (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS audit_log_user_time_idx ON audit_log (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS calendar_events (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  date date NOT NULL,
  start_time time NOT NULL,
  end_time time,
  title text NOT NULL,
  color text NOT NULL DEFAULT '#34557a',
  location text NOT NULL DEFAULT '',
  attendees integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS calendar_events_user_date_idx ON calendar_events (user_id, date);

CREATE TABLE IF NOT EXISTS contacts (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name text NOT NULL,
  role text NOT NULL DEFAULT '',
  company text NOT NULL DEFAULT '',
  team text NOT NULL DEFAULT '',
  email citext NOT NULL UNIQUE,
  phone text NOT NULL DEFAULT '',
  location text NOT NULL DEFAULT '',
  reports_to text NOT NULL DEFAULT '',
  initials text NOT NULL DEFAULT '',
  color text NOT NULL DEFAULT '#34557a',
  visible boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS contacts_tenant_idx ON contacts (tenant_id);

CREATE TABLE IF NOT EXISTS system_settings (
  key text PRIMARY KEY,
  value jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Restrict the mail daemons to the mapping tables they need.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'a8m_mail') THEN
    RAISE EXCEPTION 'a8m_mail role must exist before migrations';
  END IF;
END $$;

GRANT CONNECT ON DATABASE a8m TO a8m_mail;
GRANT USAGE ON SCHEMA public TO a8m_mail;
GRANT SELECT ON mailboxes, user_aliases, mail_domains, users TO a8m_mail;

INSERT INTO schema_migrations (version) VALUES ('0001_core')
ON CONFLICT (version) DO NOTHING;

COMMIT;
