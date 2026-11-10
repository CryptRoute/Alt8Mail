-- Phase 6 ecosystem: CRM cache, migration fidelity, phishing simulation,
-- support telemetry and platform analytics support.
BEGIN;

CREATE TABLE IF NOT EXISTS public.crm_records (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  provider text NOT NULL,
  object_type text NOT NULL CHECK (object_type IN ('contact', 'company', 'deal', 'activity')),
  external_id text NOT NULL,
  email citext NOT NULL DEFAULT '',
  name text NOT NULL DEFAULT '',
  company text NOT NULL DEFAULT '',
  title text NOT NULL DEFAULT '',
  stage text NOT NULL DEFAULT '',
  amount numeric(18,2) NOT NULL DEFAULT 0,
  raw jsonb NOT NULL DEFAULT '{}'::jsonb,
  last_synced_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, provider, object_type, external_id)
);
CREATE INDEX IF NOT EXISTS crm_records_tenant_email_idx
  ON public.crm_records (tenant_id, email);
CREATE INDEX IF NOT EXISTS crm_records_tenant_sync_idx
  ON public.crm_records (tenant_id, provider, last_synced_at DESC);

CREATE TABLE IF NOT EXISTS public.crm_sync_state (
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  provider text NOT NULL,
  status text NOT NULL DEFAULT 'idle',
  records integer NOT NULL DEFAULT 0,
  last_sync_at timestamptz,
  last_error text NOT NULL DEFAULT '',
  PRIMARY KEY (tenant_id, provider)
);

CREATE TABLE IF NOT EXISTS public.migration_reports (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  job_id text NOT NULL,
  source_counts jsonb NOT NULL DEFAULT '{}'::jsonb,
  target_counts jsonb NOT NULL DEFAULT '{}'::jsonb,
  fidelity jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS migration_reports_tenant_idx
  ON public.migration_reports (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS public.phishing_campaigns (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  name text NOT NULL,
  subject text NOT NULL,
  body_text text NOT NULL,
  landing_note text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'running', 'completed')),
  created_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  launched_at timestamptz
);

CREATE TABLE IF NOT EXISTS public.phishing_targets (
  id text PRIMARY KEY,
  campaign_id text NOT NULL REFERENCES public.phishing_campaigns(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  email citext NOT NULL,
  token text NOT NULL UNIQUE,
  status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'sent', 'opened', 'clicked', 'reported')),
  created_at timestamptz NOT NULL DEFAULT now(),
  sent_at timestamptz,
  opened_at timestamptz,
  clicked_at timestamptz,
  reported_at timestamptz,
  UNIQUE (campaign_id, email)
);
CREATE INDEX IF NOT EXISTS phishing_targets_tenant_idx
  ON public.phishing_targets (tenant_id, campaign_id, status);

CREATE TABLE IF NOT EXISTS public.tenant_telemetry (
  tenant_id uuid PRIMARY KEY REFERENCES public.tenants(id) ON DELETE CASCADE,
  enabled boolean NOT NULL DEFAULT false,
  endpoint text NOT NULL DEFAULT '',
  last_sent_at timestamptz,
  last_error text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now()
);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.crm_records TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.crm_sync_state TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.migration_reports TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.phishing_campaigns TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.phishing_targets TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.tenant_telemetry TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0080_ecosystem')
ON CONFLICT DO NOTHING;

COMMIT;
