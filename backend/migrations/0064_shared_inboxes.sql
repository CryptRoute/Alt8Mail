-- Phase 2 shared inboxes. A team inbox wraps an existing mailbox owner (the
-- backing store for mail) with an active group of agents, assignment policy,
-- SLA state and lightweight presence/collision tracking.
BEGIN;

CREATE TABLE IF NOT EXISTS public.team_mailboxes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  name text NOT NULL,
  address citext NOT NULL,
  owner_user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  group_id uuid NOT NULL REFERENCES public.groups(id) ON DELETE CASCADE,
  assignment_strategy text NOT NULL DEFAULT 'manual'
    CHECK (assignment_strategy IN ('manual', 'round_robin')),
  assignment_cursor integer NOT NULL DEFAULT 0 CHECK (assignment_cursor >= 0),
  sla_first_response_minutes integer NOT NULL DEFAULT 0
    CHECK (sla_first_response_minutes BETWEEN 0 AND 10080),
  sla_resolution_minutes integer NOT NULL DEFAULT 0
    CHECK (sla_resolution_minutes BETWEEN 0 AND 10080),
  can_send boolean NOT NULL DEFAULT true,
  active boolean NOT NULL DEFAULT true,
  settings jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, address),
  UNIQUE (tenant_id, owner_user_id),
  UNIQUE (tenant_id, group_id)
);
CREATE INDEX IF NOT EXISTS team_mailboxes_tenant_idx
  ON public.team_mailboxes (tenant_id, active, name);
CREATE INDEX IF NOT EXISTS team_mailboxes_owner_idx
  ON public.team_mailboxes (owner_user_id) WHERE active;

CREATE TABLE IF NOT EXISTS public.team_thread_state (
  team_mailbox_id uuid NOT NULL REFERENCES public.team_mailboxes(id) ON DELETE CASCADE,
  thread_id text NOT NULL,
  first_response_at timestamptz,
  first_response_due_at timestamptz,
  resolution_due_at timestamptz,
  breached_at timestamptz,
  escalated_at timestamptz,
  last_agent_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (team_mailbox_id, thread_id)
);
CREATE INDEX IF NOT EXISTS team_thread_state_due_idx
  ON public.team_thread_state (team_mailbox_id, first_response_due_at, resolution_due_at);

CREATE TABLE IF NOT EXISTS public.team_thread_watchers (
  team_mailbox_id uuid NOT NULL REFERENCES public.team_mailboxes(id) ON DELETE CASCADE,
  thread_id text NOT NULL,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (team_mailbox_id, thread_id, user_id)
);
CREATE INDEX IF NOT EXISTS team_thread_watchers_user_idx
  ON public.team_thread_watchers (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS public.team_thread_presence (
  team_mailbox_id uuid NOT NULL REFERENCES public.team_mailboxes(id) ON DELETE CASCADE,
  thread_id text NOT NULL,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  state text NOT NULL DEFAULT 'viewing' CHECK (state IN ('viewing', 'replying')),
  expires_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (team_mailbox_id, thread_id, user_id)
);
CREATE INDEX IF NOT EXISTS team_thread_presence_expiry_idx
  ON public.team_thread_presence (team_mailbox_id, thread_id, expires_at);

CREATE TABLE IF NOT EXISTS public.team_assignment_log (
  id bigserial PRIMARY KEY,
  team_mailbox_id uuid NOT NULL REFERENCES public.team_mailboxes(id) ON DELETE CASCADE,
  thread_id text NOT NULL,
  assigned_to uuid REFERENCES public.users(id) ON DELETE SET NULL,
  assigned_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  strategy text NOT NULL DEFAULT 'manual',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS team_assignment_log_thread_idx
  ON public.team_assignment_log (team_mailbox_id, thread_id, created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.team_mailboxes TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.team_thread_state TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.team_thread_watchers TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.team_thread_presence TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.team_assignment_log TO a8m_app;
GRANT USAGE, SELECT ON SEQUENCE public.team_assignment_log_id_seq TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0064_shared_inboxes')
ON CONFLICT (version) DO NOTHING;

COMMIT;
