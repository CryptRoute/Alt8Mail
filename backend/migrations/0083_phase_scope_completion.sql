-- Complete remaining Phase 2-6 scope: task recurrence/reminders, DMARC
-- polling, distributed MCP connection limits and calendar resource approvals.
BEGIN;

ALTER TABLE public.tasks
  ADD COLUMN IF NOT EXISTS reminder_sent_at timestamptz,
  ADD COLUMN IF NOT EXISTS client_request_id text NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS tasks_client_request_idx
  ON public.tasks (user_id, client_request_id)
  WHERE client_request_id <> '';

CREATE UNIQUE INDEX IF NOT EXISTS tasks_recurrence_parent_idx
  ON public.tasks (parent_task_id)
  WHERE parent_task_id IS NOT NULL AND recurrence_rule <> '';

CREATE INDEX IF NOT EXISTS tasks_reminder_idx
  ON public.tasks (reminder_at)
  WHERE reminder_at IS NOT NULL AND reminder_sent_at IS NULL AND status = 'open';

CREATE TABLE IF NOT EXISTS public.dmarc_pollers (
  tenant_id uuid PRIMARY KEY REFERENCES public.tenants(id) ON DELETE CASCADE,
  enabled boolean NOT NULL DEFAULT false,
  mailbox_email citext NOT NULL DEFAULT '',
  folder text NOT NULL DEFAULT 'INBOX',
  last_uid bigint NOT NULL DEFAULT 0,
  last_poll_at timestamptz,
  last_error text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.mcp_rate_events (
  id bigserial PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  connection_id text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mcp_rate_events_connection_idx
  ON public.mcp_rate_events (connection_id, created_at DESC);
CREATE INDEX IF NOT EXISTS mcp_rate_events_created_idx
  ON public.mcp_rate_events (created_at);

ALTER TABLE public.calendar_resources
  ADD COLUMN IF NOT EXISTS requires_approval boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS approver_user_id uuid REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE public.calendar_event_resources
  DROP CONSTRAINT IF EXISTS calendar_event_resources_status_check;
ALTER TABLE public.calendar_event_resources
  ADD CONSTRAINT calendar_event_resources_status_check
  CHECK (status IN ('pending', 'needs-action', 'accepted', 'declined', 'tentative'));

GRANT SELECT, INSERT, UPDATE, DELETE ON public.dmarc_pollers TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.mcp_rate_events TO a8m_app;
GRANT USAGE, SELECT ON SEQUENCE public.mcp_rate_events_id_seq TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0083_phase_scope_completion')
ON CONFLICT DO NOTHING;

COMMIT;
