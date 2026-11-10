BEGIN;

DROP TABLE IF EXISTS public.mcp_rate_events;
DROP TABLE IF EXISTS public.dmarc_pollers;
DROP INDEX IF EXISTS public.tasks_reminder_idx;
DROP INDEX IF EXISTS public.tasks_recurrence_parent_idx;
DROP INDEX IF EXISTS public.tasks_client_request_idx;
ALTER TABLE public.tasks
  DROP COLUMN IF EXISTS client_request_id,
  DROP COLUMN IF EXISTS reminder_sent_at;
ALTER TABLE public.calendar_event_resources
  DROP CONSTRAINT IF EXISTS calendar_event_resources_status_check;
UPDATE public.calendar_event_resources SET status='accepted' WHERE status='pending';
ALTER TABLE public.calendar_event_resources
  ADD CONSTRAINT calendar_event_resources_status_check
  CHECK (status IN ('needs-action', 'accepted', 'declined', 'tentative'));
ALTER TABLE public.calendar_resources
  DROP COLUMN IF EXISTS approver_user_id,
  DROP COLUMN IF EXISTS requires_approval;
DELETE FROM schema_migrations WHERE version = '0083_phase_scope_completion';

COMMIT;
