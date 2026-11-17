-- Notification-center rows keep their original English title/body for legacy
-- fallback, and gain a template key plus parameters so each reader can be
-- shown the message in their own interface language.
BEGIN;

ALTER TABLE public.mail_notifications
  ADD COLUMN IF NOT EXISTS template_key text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS template_params jsonb NOT NULL DEFAULT '{}'::jsonb;

INSERT INTO schema_migrations (version) VALUES ('0096_notification_templates') ON CONFLICT DO NOTHING;

COMMIT;
