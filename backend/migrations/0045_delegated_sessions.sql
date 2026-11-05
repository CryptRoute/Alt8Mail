-- Delegated mailbox windows use a scoped session of their own. A delegated
-- session belongs to the mailbox owner but is anchored to the delegate's
-- parent sign-in, so signing out of the primary mailbox revokes every window
-- opened from it (including windows in other browsers).
BEGIN;

ALTER TABLE public.sessions
  ADD COLUMN IF NOT EXISTS parent_session_id text REFERENCES public.sessions(id) ON DELETE CASCADE,
  ADD COLUMN IF NOT EXISTS delegated_by_user_id uuid REFERENCES users(id) ON DELETE CASCADE,
  ADD COLUMN IF NOT EXISTS delegated_owner_id uuid REFERENCES users(id) ON DELETE CASCADE,
  ADD COLUMN IF NOT EXISTS delegated_can_send boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'standard';

CREATE INDEX IF NOT EXISTS sessions_parent_idx
  ON public.sessions (parent_session_id)
  WHERE parent_session_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS sessions_delegated_by_idx
  ON public.sessions (delegated_by_user_id)
  WHERE delegated_by_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS sessions_delegated_owner_idx
  ON public.sessions (delegated_owner_id)
  WHERE delegated_owner_id IS NOT NULL;

-- One-time launch codes keep the long-lived delegated token out of the new
-- window's address bar. The code is bound to the parent sign-in and expires
-- quickly even if it is never redeemed.
CREATE TABLE IF NOT EXISTS public.delegated_launch_tokens (
  id text PRIMARY KEY,
  parent_session_id text NOT NULL REFERENCES public.sessions(id) ON DELETE CASCADE,
  delegate_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  owner_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS delegated_launch_tokens_parent_idx
  ON public.delegated_launch_tokens (parent_session_id, expires_at DESC);
CREATE INDEX IF NOT EXISTS delegated_launch_tokens_expiry_idx
  ON public.delegated_launch_tokens (expires_at) WHERE used_at IS NULL;

GRANT SELECT, INSERT, UPDATE, DELETE ON public.delegated_launch_tokens TO a8m_app;

-- Administrators can also grant a mailbox to a group. Every active member of
-- the group inherits the same read/send permissions through the delegate
-- resolution query used by the webmail switcher.
CREATE TABLE IF NOT EXISTS public.mailbox_delegate_groups (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  owner_user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  group_id uuid NOT NULL REFERENCES public.groups(id) ON DELETE CASCADE,
  can_read boolean NOT NULL DEFAULT true,
  can_send boolean NOT NULL DEFAULT false,
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (owner_user_id, group_id)
);
CREATE INDEX IF NOT EXISTS mailbox_delegate_groups_owner_idx
  ON public.mailbox_delegate_groups (tenant_id, owner_user_id);
CREATE INDEX IF NOT EXISTS mailbox_delegate_groups_group_idx
  ON public.mailbox_delegate_groups (tenant_id, group_id);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.mailbox_delegate_groups TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0045_delegated_sessions') ON CONFLICT DO NOTHING;

COMMIT;
