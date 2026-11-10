-- Phase 6 public API, API keys, idempotency and signed webhooks.
BEGIN;

CREATE TABLE IF NOT EXISTS public.api_keys (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  created_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  name text NOT NULL,
  prefix text NOT NULL,
  token_hash text NOT NULL UNIQUE,
  scopes text[] NOT NULL DEFAULT '{}',
  rate_limit_per_minute integer NOT NULL DEFAULT 120,
  expires_at timestamptz,
  revoked_at timestamptz,
  last_used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS api_keys_tenant_idx ON public.api_keys (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS public.api_idempotency (
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  idempotency_key text NOT NULL,
  method text NOT NULL,
  path text NOT NULL,
  status integer NOT NULL,
  response jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, idempotency_key, method, path)
);

CREATE TABLE IF NOT EXISTS public.webhook_endpoints (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  url text NOT NULL,
  secret_sealed text NOT NULL,
  events text[] NOT NULL DEFAULT '{}',
  enabled boolean NOT NULL DEFAULT true,
  created_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS webhook_endpoints_tenant_idx ON public.webhook_endpoints (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS public.webhook_deliveries (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  endpoint_id text NOT NULL REFERENCES public.webhook_endpoints(id) ON DELETE CASCADE,
  event_id text NOT NULL,
  event text NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'failed')),
  attempts integer NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  delivered_at timestamptz,
  UNIQUE (endpoint_id, event_id)
);
CREATE INDEX IF NOT EXISTS webhook_deliveries_due_idx
  ON public.webhook_deliveries (status, next_attempt_at);
CREATE INDEX IF NOT EXISTS webhook_deliveries_tenant_idx
  ON public.webhook_deliveries (tenant_id, created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.api_keys TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.api_idempotency TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.webhook_endpoints TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.webhook_deliveries TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0079_api_platform')
ON CONFLICT DO NOTHING;

COMMIT;
