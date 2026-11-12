BEGIN;

ALTER TABLE public.outbox DROP COLUMN IF EXISTS reply_to;

DELETE FROM schema_migrations WHERE version = '0087_outbox_reply_to';

COMMIT;
