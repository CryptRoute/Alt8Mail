-- Rollback for 0058_mail_phase2.
-- This is destructive for the OAuth authorization-state table and the
-- connected-folder sync flag. Take a backup first.
BEGIN;

DROP TABLE IF EXISTS public.mail_oauth_states;
ALTER TABLE public.mail_folders DROP COLUMN IF EXISTS sync_enabled;

DELETE FROM schema_migrations WHERE version = '0058_mail_phase2';

COMMIT;
