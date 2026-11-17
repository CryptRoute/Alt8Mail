BEGIN;
DROP TABLE IF EXISTS public.team_assignment_log;
DROP TABLE IF EXISTS public.team_thread_presence;
DROP TABLE IF EXISTS public.team_thread_watchers;
DROP TABLE IF EXISTS public.team_thread_state;
DROP TABLE IF EXISTS public.team_mailboxes;
DELETE FROM schema_migrations WHERE version = '0064_shared_inboxes';
COMMIT;
