BEGIN;
ALTER TABLE public.outbox DROP COLUMN IF EXISTS merge_recipient_id, DROP COLUMN IF EXISTS merge_id;
DROP TABLE IF EXISTS public.mail_suppressions;
DROP TABLE IF EXISTS public.mail_merge_recipients;
DROP TABLE IF EXISTS public.mail_merges;
DELETE FROM schema_migrations WHERE version = '0071_mail_merge';
COMMIT;
