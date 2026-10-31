-- Durable mailbox-migration activity log shown in the admin import view.
BEGIN;

CREATE TABLE IF NOT EXISTS public.mail_import_events (
  id text PRIMARY KEY,
  job_id text NOT NULL REFERENCES public.mail_import_jobs(id) ON DELETE CASCADE,
  item_id text REFERENCES public.mail_import_items(id) ON DELETE CASCADE,
  level text NOT NULL DEFAULT 'info' CHECK (level IN ('info', 'warn', 'error')),
  stage text NOT NULL DEFAULT '',
  message text NOT NULL DEFAULT '',
  detail text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mail_import_events_job_idx
  ON public.mail_import_events (job_id, created_at, id);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.mail_import_events TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0025_mail_import_events')
ON CONFLICT (version) DO NOTHING;

COMMIT;
