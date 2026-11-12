BEGIN;

ALTER TABLE public.mailboxes DROP CONSTRAINT IF EXISTS mailboxes_storage_volume_fk;
ALTER TABLE public.mail_domains DROP CONSTRAINT IF EXISTS mail_domains_storage_volume_fk;
DROP INDEX IF EXISTS public.mailboxes_storage_volume_idx;
DROP INDEX IF EXISTS public.mail_domains_storage_volume_idx;
ALTER TABLE public.mailboxes DROP COLUMN IF EXISTS storage_state;
ALTER TABLE public.mailboxes DROP COLUMN IF EXISTS storage_volume_id;
ALTER TABLE public.mailboxes DROP COLUMN IF EXISTS mail_home;
ALTER TABLE public.mail_domains DROP COLUMN IF EXISTS storage_volume_id;

DELETE FROM schema_migrations WHERE version = '0089_mailbox_home';

COMMIT;
