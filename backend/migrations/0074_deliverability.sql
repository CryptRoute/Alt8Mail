-- Phase 3 deliverability: DMARC aggregate ingestion and opt-in campaign
-- tracking. Tracking is disabled by tenant policy and never silent.
BEGIN;

CREATE TABLE IF NOT EXISTS public.dmarc_reports (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  domain citext NOT NULL,
  report_id text NOT NULL,
  org_name text NOT NULL DEFAULT '',
  range_begin timestamptz NOT NULL,
  range_end timestamptz NOT NULL,
  policy text NOT NULL DEFAULT '',
  records_count integer NOT NULL DEFAULT 0,
  messages_count bigint NOT NULL DEFAULT 0,
  pass_count bigint NOT NULL DEFAULT 0,
  fail_count bigint NOT NULL DEFAULT 0,
  summary jsonb NOT NULL DEFAULT '{}'::jsonb,
  raw_xml text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, domain, report_id, org_name)
);
CREATE INDEX IF NOT EXISTS dmarc_reports_tenant_idx
  ON public.dmarc_reports (tenant_id, domain, range_begin DESC);

CREATE TABLE IF NOT EXISTS public.dmarc_records (
  id bigserial PRIMARY KEY,
  report_id text NOT NULL REFERENCES public.dmarc_reports(id) ON DELETE CASCADE,
  source_ip inet,
  count bigint NOT NULL DEFAULT 0,
  disposition text NOT NULL DEFAULT '',
  dkim_result text NOT NULL DEFAULT '',
  spf_result text NOT NULL DEFAULT '',
  header_from text NOT NULL DEFAULT '',
  aligned_dkim text NOT NULL DEFAULT '',
  aligned_spf text NOT NULL DEFAULT '',
  reasons jsonb NOT NULL DEFAULT '[]'::jsonb
);
CREATE INDEX IF NOT EXISTS dmarc_records_report_idx ON public.dmarc_records (report_id);
CREATE INDEX IF NOT EXISTS dmarc_records_source_idx ON public.dmarc_records (source_ip);

CREATE TABLE IF NOT EXISTS public.campaign_tracking (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  merge_id text REFERENCES public.mail_merges(id) ON DELETE CASCADE,
  outbox_id text REFERENCES public.outbox(id) ON DELETE CASCADE,
  recipient_email citext NOT NULL,
  token text NOT NULL UNIQUE,
  open_count integer NOT NULL DEFAULT 0,
  click_count integer NOT NULL DEFAULT 0,
  last_event_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS campaign_tracking_user_idx
  ON public.campaign_tracking (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS public.campaign_events (
  id bigserial PRIMARY KEY,
  tracking_id text NOT NULL REFERENCES public.campaign_tracking(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('open', 'click')),
  url text NOT NULL DEFAULT '',
  ip_hash text NOT NULL DEFAULT '',
  user_agent text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS campaign_events_tracking_idx ON public.campaign_events (tracking_id, created_at DESC);

CREATE TABLE IF NOT EXISTS public.campaign_links (
  token text PRIMARY KEY,
  tracking_id text NOT NULL REFERENCES public.campaign_tracking(id) ON DELETE CASCADE,
  url text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.dmarc_reports TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.dmarc_records TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.campaign_tracking TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.campaign_events TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.campaign_links TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0074_deliverability')
ON CONFLICT DO NOTHING;

COMMIT;
