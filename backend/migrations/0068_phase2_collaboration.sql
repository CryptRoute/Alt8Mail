-- Phase 2 collaboration extensions: task planning fields, file versions,
-- comments and approval requests, VIP senders and thread muting.
BEGIN;

ALTER TABLE public.tasks
  ADD COLUMN IF NOT EXISTS parent_task_id text REFERENCES public.tasks(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS depends_on_task_id text REFERENCES public.tasks(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS kanban_status text NOT NULL DEFAULT 'todo'
    CHECK (kanban_status IN ('todo', 'in_progress', 'review', 'done')),
  ADD COLUMN IF NOT EXISTS recurrence_rule text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS reminder_at timestamptz;
CREATE INDEX IF NOT EXISTS tasks_parent_idx ON public.tasks (parent_task_id);
CREATE INDEX IF NOT EXISTS tasks_kanban_idx ON public.tasks (user_id, kanban_status, due_date);

CREATE TABLE IF NOT EXISTS public.file_versions (
  id text PRIMARY KEY,
  file_id text NOT NULL REFERENCES public.files(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  version integer NOT NULL,
  name text NOT NULL,
  mime text NOT NULL DEFAULT 'application/octet-stream',
  size_bytes bigint NOT NULL DEFAULT 0,
  storage_key text NOT NULL DEFAULT '',
  attachment_id text,
  checksum text NOT NULL DEFAULT '',
  created_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (file_id, version)
);
CREATE INDEX IF NOT EXISTS file_versions_file_idx ON public.file_versions (file_id, version DESC);

CREATE TABLE IF NOT EXISTS public.file_comments (
  id text PRIMARY KEY,
  file_id text NOT NULL REFERENCES public.files(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  author_user_id uuid REFERENCES public.users(id) ON DELETE SET NULL,
  body text NOT NULL,
  resolved_at timestamptz,
  resolved_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS file_comments_file_idx ON public.file_comments (file_id, created_at);

CREATE TABLE IF NOT EXISTS public.file_approvals (
  id text PRIMARY KEY,
  file_id text NOT NULL REFERENCES public.files(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  requested_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  approver_user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'changes_requested', 'rejected')),
  note text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz
);
CREATE INDEX IF NOT EXISTS file_approvals_file_idx ON public.file_approvals (file_id, created_at DESC);
CREATE INDEX IF NOT EXISTS file_approvals_approver_idx ON public.file_approvals (approver_user_id, status);

CREATE TABLE IF NOT EXISTS public.vip_senders (
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  email citext NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, email)
);

CREATE TABLE IF NOT EXISTS public.mail_thread_mutes (
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  thread_id text NOT NULL,
  muted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, thread_id)
);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.file_versions TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.file_comments TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.file_approvals TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.vip_senders TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.mail_thread_mutes TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0068_phase2_collaboration')
ON CONFLICT DO NOTHING;

COMMIT;
