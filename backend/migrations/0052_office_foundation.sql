-- Phase 1 office foundation: Office/OnlyOffice lifecycle state and the
-- webmail-facing feature flag. The JWT secret is kept in a root:a8m file on
-- the office host, never in the database.
BEGIN;

CREATE TABLE IF NOT EXISTS public.office_settings (
  id text PRIMARY KEY,
  enabled boolean NOT NULL DEFAULT false,
  deployment_mode text NOT NULL DEFAULT 'local'
    CHECK (deployment_mode IN ('local', 'remote')),
  public_url text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'not_installed',
  container_status text NOT NULL DEFAULT '',
  version text NOT NULL DEFAULT '',
  image text NOT NULL DEFAULT '',
  gateway_status text NOT NULL DEFAULT 'not_configured',
  gateway_host text NOT NULL DEFAULT '',
  last_error text NOT NULL DEFAULT '',
  last_health_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO public.office_settings (id)
VALUES ('default')
ON CONFLICT (id) DO NOTHING;

GRANT SELECT ON public.office_settings TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.office_settings TO alt8mail_control;

INSERT INTO schema_migrations (version) VALUES ('0052_office_foundation') ON CONFLICT DO NOTHING;

COMMIT;
