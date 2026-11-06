-- Add-on phase 2: tenant install policy, provider OAuth app credentials and
-- a durable inbox for verified provider events.
BEGIN;

ALTER TABLE public.tenant_addons
  ADD COLUMN IF NOT EXISTS install_policy text NOT NULL DEFAULT 'self_service'
    CHECK (install_policy IN ('self_service', 'approval_required')),
  ADD COLUMN IF NOT EXISTS client_id text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS client_secret_sealed text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS webhook_secret_sealed text NOT NULL DEFAULT '';

ALTER TABLE public.addon_connections
  ADD COLUMN IF NOT EXISTS external_account_id text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS addon_connections_external_idx
  ON public.addon_connections (addon_id, external_account_id)
  WHERE status = 'active' AND external_account_id <> '';

CREATE TABLE IF NOT EXISTS public.addon_events (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  addon_id text NOT NULL REFERENCES public.addon_system_catalog(id) ON DELETE CASCADE,
  connection_id text REFERENCES public.addon_connections(id) ON DELETE SET NULL,
  provider_event_id text NOT NULL DEFAULT '',
  event_type text NOT NULL DEFAULT '',
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  status text NOT NULL DEFAULT 'received'
    CHECK (status IN ('received', 'processed', 'failed')),
  error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  processed_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS addon_events_provider_idx
  ON public.addon_events (addon_id, provider_event_id)
  WHERE provider_event_id <> '';
CREATE INDEX IF NOT EXISTS addon_events_tenant_idx
  ON public.addon_events (tenant_id, addon_id, created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.addon_events TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0047_addon_oauth_policy') ON CONFLICT DO NOTHING;

COMMIT;
