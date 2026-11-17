-- Phase 3 native mail merge: templated batches, recipient variables,
-- suppression, throttled outbox scheduling and campaign reporting.
BEGIN;

CREATE TABLE IF NOT EXISTS public.mail_merges (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  name text NOT NULL DEFAULT '',
  from_email citext NOT NULL,
  subject text NOT NULL,
  body_text text NOT NULL DEFAULT '',
  body_html text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'draft'
    CHECK (status IN ('draft', 'queued', 'running', 'completed', 'cancelled')),
  throttle_seconds integer NOT NULL DEFAULT 5 CHECK (throttle_seconds BETWEEN 1 AND 60),
  scheduled_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mail_merges_user_idx
  ON public.mail_merges (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS public.mail_merge_recipients (
  id text PRIMARY KEY,
  merge_id text NOT NULL REFERENCES public.mail_merges(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  email citext NOT NULL,
  name text NOT NULL DEFAULT '',
  variables jsonb NOT NULL DEFAULT '{}'::jsonb,
  status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending','queued','sent','failed','skipped','suppressed')),
  error text NOT NULL DEFAULT '',
  outbox_id text,
  token text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (merge_id, email)
);
CREATE INDEX IF NOT EXISTS mail_merge_recipients_merge_idx
  ON public.mail_merge_recipients (merge_id, status);
CREATE INDEX IF NOT EXISTS mail_merge_recipients_outbox_idx
  ON public.mail_merge_recipients (outbox_id) WHERE outbox_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS public.mail_suppressions (
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  email citext NOT NULL,
  reason text NOT NULL DEFAULT 'unsubscribed',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, email)
);

ALTER TABLE public.outbox
  ADD COLUMN IF NOT EXISTS merge_id text,
  ADD COLUMN IF NOT EXISTS merge_recipient_id text;
CREATE INDEX IF NOT EXISTS outbox_merge_idx
  ON public.outbox (merge_id) WHERE merge_id IS NOT NULL;

GRANT SELECT, INSERT, UPDATE, DELETE ON public.mail_merges TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.mail_merge_recipients TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.mail_suppressions TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0071_mail_merge')
ON CONFLICT DO NOTHING;

COMMIT;
