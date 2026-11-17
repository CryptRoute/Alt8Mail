BEGIN;

ALTER TABLE public.mail_notifications
  DROP COLUMN IF EXISTS template_params,
  DROP COLUMN IF EXISTS template_key;

DELETE FROM schema_migrations WHERE version = '0096_notification_templates';

COMMIT;
