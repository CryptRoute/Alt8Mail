-- Phase 3 follow-ups: user reminders and awaiting-reply nudges. These are
-- advisory only; the worker never sends mail.
BEGIN;

CREATE TABLE IF NOT EXISTS public.follow_ups (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  owner_user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  thread_id text NOT NULL,
  message_id text NOT NULL DEFAULT '',
  kind text NOT NULL DEFAULT 'awaiting_reply'
    CHECK (kind IN ('awaiting_reply', 'remind_me')),
  due_at timestamptz NOT NULL,
  note text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'notified', 'completed', 'cancelled')),
  last_checked_at timestamptz,
  notified_at timestamptz,
  completed_at timestamptz,
  reply_message_id text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS follow_ups_due_idx
  ON public.follow_ups (status, due_at);
CREATE INDEX IF NOT EXISTS follow_ups_user_idx
  ON public.follow_ups (user_id, status, due_at);
CREATE UNIQUE INDEX IF NOT EXISTS follow_ups_one_pending_idx
  ON public.follow_ups (user_id, thread_id, kind)
  WHERE status IN ('pending', 'notified');

GRANT SELECT, INSERT, UPDATE, DELETE ON public.follow_ups TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0070_follow_ups')
ON CONFLICT DO NOTHING;

COMMIT;
