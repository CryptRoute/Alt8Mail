-- Enterprise controls: legal holds, eDiscovery exports, one-click
-- offboarding jobs and signed SIEM audit webhook delivery.
BEGIN;

-- Classification must survive a restart while a message waits in the outbox.
ALTER TABLE public.outbox ADD COLUMN IF NOT EXISTS classification text NOT NULL DEFAULT 'Internal';
ALTER TABLE public.outbox ADD COLUMN IF NOT EXISTS dlp_warnings jsonb NOT NULL DEFAULT '[]'::jsonb;

CREATE TABLE IF NOT EXISTS public.mail_legal_holds (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  scope text NOT NULL DEFAULT 'user' CHECK (scope IN ('tenant', 'user')),
  user_id uuid REFERENCES public.users(id) ON DELETE SET NULL,
  name text NOT NULL DEFAULT '',
  reason text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'released')),
  created_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_by_email citext NOT NULL DEFAULT '',
  released_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  released_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mail_legal_holds_tenant_idx
  ON public.mail_legal_holds (tenant_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS mail_legal_holds_user_idx
  ON public.mail_legal_holds (tenant_id, user_id, status);

CREATE TABLE IF NOT EXISTS public.user_offboarding_jobs (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid REFERENCES public.users(id) ON DELETE SET NULL,
  actor_id uuid REFERENCES public.users(id) ON DELETE SET NULL,
  transfer_to uuid REFERENCES public.users(id) ON DELETE SET NULL,
  retain_mailbox boolean NOT NULL DEFAULT true,
  create_legal_hold boolean NOT NULL DEFAULT false,
  auto_reply boolean NOT NULL DEFAULT false,
  status text NOT NULL DEFAULT 'completed' CHECK (status IN ('running', 'completed', 'failed')),
  summary jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz
);
CREATE INDEX IF NOT EXISTS user_offboarding_jobs_tenant_idx
  ON public.user_offboarding_jobs (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS public.tenant_siem_config (
  tenant_id uuid PRIMARY KEY REFERENCES public.tenants(id) ON DELETE CASCADE,
  endpoint text NOT NULL DEFAULT '',
  secret_sealed text NOT NULL DEFAULT '',
  enabled boolean NOT NULL DEFAULT false,
  include_user_events boolean NOT NULL DEFAULT true,
  include_admin_events boolean NOT NULL DEFAULT true,
  last_user_audit_id bigint NOT NULL DEFAULT 0,
  last_admin_audit_id bigint NOT NULL DEFAULT 0,
  last_delivery_at timestamptz,
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.tenant_siem_deliveries (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  source text NOT NULL CHECK (source IN ('user', 'admin')),
  source_id bigint NOT NULL,
  event_action text NOT NULL DEFAULT '',
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'failed')),
  attempts integer NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  delivered_at timestamptz,
  UNIQUE (tenant_id, source, source_id)
);
CREATE INDEX IF NOT EXISTS tenant_siem_deliveries_pending_idx
  ON public.tenant_siem_deliveries (status, next_attempt_at);

-- Fast tenant-wide eDiscovery lookups across the normalized message store.
CREATE INDEX IF NOT EXISTS mail_messages_tenant_time_idx
  ON public.mail_messages (tenant_id, sent_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.mail_legal_holds TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.user_offboarding_jobs TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.tenant_siem_config TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.tenant_siem_deliveries TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0036_phase4')
ON CONFLICT (version) DO NOTHING;

COMMIT;
