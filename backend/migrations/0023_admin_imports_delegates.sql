-- Admin-managed mailbox delegation and resumable mailbox migration jobs.
BEGIN;

CREATE TABLE IF NOT EXISTS public.mailbox_delegates (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  owner_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  delegate_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  can_read boolean NOT NULL DEFAULT true,
  can_send boolean NOT NULL DEFAULT false,
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (owner_user_id, delegate_user_id),
  CHECK (owner_user_id <> delegate_user_id)
);
CREATE INDEX IF NOT EXISTS mailbox_delegates_delegate_idx ON public.mailbox_delegates (tenant_id, delegate_user_id);
CREATE INDEX IF NOT EXISTS mailbox_delegates_owner_idx ON public.mailbox_delegates (tenant_id, owner_user_id);

CREATE TABLE IF NOT EXISTS public.mail_import_jobs (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  created_by text NOT NULL DEFAULT '',
  created_by_user uuid REFERENCES users(id) ON DELETE SET NULL,
  protocol text NOT NULL DEFAULT 'imap' CHECK (protocol IN ('imap', 'pop3')),
  host text NOT NULL,
  port integer NOT NULL DEFAULT 993,
  security text NOT NULL DEFAULT 'ssl' CHECK (security IN ('ssl', 'starttls', 'none')),
  status text NOT NULL DEFAULT 'validating' CHECK (status IN ('validating', 'awaiting_mapping', 'queued', 'running', 'completed', 'failed', 'cancelled')),
  options jsonb NOT NULL DEFAULT '{}'::jsonb,
  total_items integer NOT NULL DEFAULT 0,
  validated_items integer NOT NULL DEFAULT 0,
  completed_items integer NOT NULL DEFAULT 0,
  failed_items integer NOT NULL DEFAULT 0,
  imported_messages bigint NOT NULL DEFAULT 0,
  current_item text NOT NULL DEFAULT '',
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz
);
CREATE INDEX IF NOT EXISTS mail_import_jobs_status_idx ON public.mail_import_jobs (tenant_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS public.mail_import_items (
  id text PRIMARY KEY,
  job_id text NOT NULL REFERENCES public.mail_import_jobs(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  source_email citext NOT NULL,
  source_password text NOT NULL DEFAULT '',
  notify_email citext NOT NULL DEFAULT '',
  target_mode text NOT NULL DEFAULT 'new' CHECK (target_mode IN ('existing', 'new')),
  target_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  target_email citext NOT NULL DEFAULT '',
  target_name text NOT NULL DEFAULT '',
  target_role text NOT NULL DEFAULT 'member',
  target_quota_bytes bigint NOT NULL DEFAULT 17179869184,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'validating', 'ready', 'auth_failed', 'queued', 'running', 'completed', 'failed', 'skipped')),
  auth_error text NOT NULL DEFAULT '',
  imported_messages bigint NOT NULL DEFAULT 0,
  total_messages bigint NOT NULL DEFAULT 0,
  cursor jsonb NOT NULL DEFAULT '{}'::jsonb,
  initial_password text NOT NULL DEFAULT '',
  mail_password text NOT NULL DEFAULT '',
  password_shown boolean NOT NULL DEFAULT false,
  notified_at timestamptz,
  error text NOT NULL DEFAULT '',
  started_at timestamptz,
  completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (job_id, source_email)
);
CREATE INDEX IF NOT EXISTS mail_import_items_job_idx ON public.mail_import_items (job_id, created_at);
CREATE INDEX IF NOT EXISTS mail_import_items_status_idx ON public.mail_import_items (job_id, status);

CREATE TABLE IF NOT EXISTS public.user_import_jobs (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  created_by text NOT NULL DEFAULT '',
  created_by_user uuid REFERENCES users(id) ON DELETE SET NULL,
  status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'completed', 'failed')),
  total_items integer NOT NULL DEFAULT 0,
  created_items integer NOT NULL DEFAULT 0,
  failed_items integer NOT NULL DEFAULT 0,
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz
);
CREATE INDEX IF NOT EXISTS user_import_jobs_tenant_idx ON public.user_import_jobs (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS public.user_import_items (
  id text PRIMARY KEY,
  job_id text NOT NULL REFERENCES public.user_import_jobs(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  email citext NOT NULL,
  name text NOT NULL DEFAULT '',
  role text NOT NULL DEFAULT 'member',
  quota_bytes bigint NOT NULL DEFAULT 17179869184,
  notify_email citext NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'created', 'failed')),
  user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  temporary_password text NOT NULL DEFAULT '',
  mail_password text NOT NULL DEFAULT '',
  emailed boolean NOT NULL DEFAULT false,
  error text NOT NULL DEFAULT '',
  email_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS user_import_items_job_idx ON public.user_import_items (job_id, created_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.mailbox_delegates TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.mail_import_jobs TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.mail_import_items TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.user_import_jobs TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.user_import_items TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0023_admin_imports_delegates')
ON CONFLICT (version) DO NOTHING;

COMMIT;
