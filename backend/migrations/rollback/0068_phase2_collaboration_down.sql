BEGIN;
DROP TABLE IF EXISTS public.mail_thread_mutes;
DROP TABLE IF EXISTS public.vip_senders;
DROP TABLE IF EXISTS public.file_approvals;
DROP TABLE IF EXISTS public.file_comments;
DROP TABLE IF EXISTS public.file_versions;
ALTER TABLE public.tasks
  DROP COLUMN IF EXISTS reminder_at,
  DROP COLUMN IF EXISTS recurrence_rule,
  DROP COLUMN IF EXISTS kanban_status,
  DROP COLUMN IF EXISTS depends_on_task_id,
  DROP COLUMN IF EXISTS parent_task_id;
DELETE FROM schema_migrations WHERE version = '0068_phase2_collaboration';
COMMIT;
