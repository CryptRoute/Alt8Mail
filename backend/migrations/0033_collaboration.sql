-- Collaboration primitives inside mail threads: handling status, assignment,
-- closure, internal team notes and in-app mention notifications.
BEGIN;

CREATE TABLE IF NOT EXISTS public.mail_thread_meta (
  owner_user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  thread_id text NOT NULL,
  status text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'in_progress', 'done')),
  assigned_user_id uuid REFERENCES public.users(id) ON DELETE SET NULL,
  assigned_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  assigned_at timestamptz,
  closed_at timestamptz,
  closed_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (owner_user_id, thread_id)
);
CREATE INDEX IF NOT EXISTS mail_thread_meta_assignee_idx
  ON public.mail_thread_meta (assigned_user_id, status);

CREATE TABLE IF NOT EXISTS public.mail_thread_notes (
  id text PRIMARY KEY,
  owner_user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  thread_id text NOT NULL,
  author_user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  author_name text NOT NULL DEFAULT '',
  author_email citext NOT NULL,
  body text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  deleted_by uuid REFERENCES public.users(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS mail_thread_notes_thread_idx
  ON public.mail_thread_notes (owner_user_id, thread_id, created_at);

CREATE TABLE IF NOT EXISTS public.mail_notifications (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  kind text NOT NULL DEFAULT 'mention',
  title text NOT NULL,
  body text NOT NULL DEFAULT '',
  detail text NOT NULL DEFAULT '',
  actor_user_id uuid REFERENCES public.users(id) ON DELETE SET NULL,
  actor_name text NOT NULL DEFAULT '',
  actor_email citext,
  thread_id text NOT NULL DEFAULT '',
  message_id text NOT NULL DEFAULT '',
  owner_user_id uuid REFERENCES public.users(id) ON DELETE SET NULL,
  read_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mail_notifications_user_idx
  ON public.mail_notifications (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS mail_notifications_unread_idx
  ON public.mail_notifications (user_id) WHERE read_at IS NULL;

CREATE TABLE IF NOT EXISTS public.security_block_events (
  id bigserial PRIMARY KEY,
  sender_email citext NOT NULL,
  recipient_domain text NOT NULL,
  blocked_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS security_block_events_domain_time_idx
  ON public.security_block_events (recipient_domain, blocked_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.mail_thread_meta TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.mail_thread_notes TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.mail_notifications TO a8m_app;
GRANT SELECT ON public.security_block_events TO a8m_app;
GRANT SELECT, INSERT ON public.security_block_events TO alt8mail_control;
GRANT USAGE, SELECT ON SEQUENCE public.security_block_events_id_seq TO alt8mail_control;

INSERT INTO schema_migrations (version) VALUES ('0033_collaboration')
ON CONFLICT (version) DO NOTHING;

COMMIT;
