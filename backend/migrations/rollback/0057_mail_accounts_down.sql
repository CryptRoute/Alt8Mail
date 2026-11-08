-- Rollback for 0057_mail_accounts.
--
-- DESTRUCTIVE: this removes every connected-account row, the native account
-- and folder rows, and the account_id columns on mail. It also restores the
-- pre-multi-account IMAP UID uniqueness rule, which can fail if connected
-- account rows with colliding native UIDs are still present. Run 0058 down
-- first and take a database backup before applying this file.
BEGIN;

DROP TRIGGER IF EXISTS a8m_users_create_native_mail_account ON public.users;
DROP TRIGGER IF EXISTS a8m_users_sync_native_mail_account ON public.users;
DROP TRIGGER IF EXISTS a8m_mail_accounts_seed_folders ON public.mail_accounts;
DROP TRIGGER IF EXISTS a8m_mail_messages_fill_account ON public.mail_messages;
DROP TRIGGER IF EXISTS a8m_outbox_fill_account ON public.outbox;
DROP TRIGGER IF EXISTS a8m_mail_identities_fill_account ON public.mail_identities;

DROP FUNCTION IF EXISTS public.a8m_create_native_mail_account();
DROP FUNCTION IF EXISTS public.a8m_seed_native_mail_folders();
DROP FUNCTION IF EXISTS public.a8m_sync_native_mail_account();
DROP FUNCTION IF EXISTS public.a8m_fill_mail_account_id();

DROP INDEX IF EXISTS public.mail_messages_imap_uidx;
ALTER TABLE public.mail_messages DROP COLUMN IF EXISTS account_id CASCADE;
ALTER TABLE public.outbox DROP COLUMN IF EXISTS account_id CASCADE;
ALTER TABLE public.mail_identities DROP COLUMN IF EXISTS account_id CASCADE;

CREATE UNIQUE INDEX mail_messages_imap_uidx
  ON public.mail_messages (user_id, imap_folder, uid_validity, uid)
  WHERE uid > 0;

DROP TABLE IF EXISTS public.mail_folders;
DROP TABLE IF EXISTS public.mail_accounts;

DELETE FROM schema_migrations WHERE version = '0057_mail_accounts';

COMMIT;
