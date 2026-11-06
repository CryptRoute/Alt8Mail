-- First-party add-ons: system catalog, tenant policy, encrypted connections,
-- per-user installs and user setup requests. OAuth provider credentials will
-- use the same connection records when the managed relay arrives.
BEGIN;

CREATE TABLE IF NOT EXISTS public.addon_system_catalog (
  id text PRIMARY KEY,
  name text NOT NULL,
  vendor text NOT NULL DEFAULT '',
  category text NOT NULL DEFAULT 'Productivity',
  description text NOT NULL DEFAULT '',
  icon text NOT NULL DEFAULT 'i-grid',
  color text NOT NULL DEFAULT '#34557a',
  connection_scope text NOT NULL CHECK (connection_scope IN ('user', 'tenant')),
  auth_type text NOT NULL CHECK (auth_type IN ('api_key', 'webhook', 'oauth2')),
  self_service boolean NOT NULL DEFAULT false,
  requires_tenant_setup boolean NOT NULL DEFAULT false,
  scopes jsonb NOT NULL DEFAULT '[]'::jsonb,
  permissions jsonb NOT NULL DEFAULT '[]'::jsonb,
  actions jsonb NOT NULL DEFAULT '[]'::jsonb,
  fields jsonb NOT NULL DEFAULT '[]'::jsonb,
  sort_order integer NOT NULL DEFAULT 0,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.tenant_addons (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  addon_id text NOT NULL REFERENCES public.addon_system_catalog(id) ON DELETE CASCADE,
  enabled boolean NOT NULL DEFAULT true,
  config jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_by uuid REFERENCES users(id) ON DELETE SET NULL,
  updated_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, addon_id)
);

CREATE TABLE IF NOT EXISTS public.addon_connections (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  addon_id text NOT NULL REFERENCES public.addon_system_catalog(id) ON DELETE CASCADE,
  connection_scope text NOT NULL CHECK (connection_scope IN ('user', 'tenant')),
  owner_user_id uuid REFERENCES users(id) ON DELETE CASCADE,
  account_label text NOT NULL DEFAULT '',
  credentials_sealed text NOT NULL,
  config jsonb NOT NULL DEFAULT '{}'::jsonb,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked', 'error')),
  created_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  revoked_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS addon_connections_tenant_active_idx
  ON public.addon_connections (tenant_id, addon_id)
  WHERE connection_scope = 'tenant' AND status = 'active';
CREATE UNIQUE INDEX IF NOT EXISTS addon_connections_user_active_idx
  ON public.addon_connections (tenant_id, addon_id, owner_user_id)
  WHERE connection_scope = 'user' AND status = 'active';
CREATE INDEX IF NOT EXISTS addon_connections_owner_idx
  ON public.addon_connections (tenant_id, owner_user_id, addon_id);

CREATE TABLE IF NOT EXISTS public.addon_installs (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  addon_id text NOT NULL REFERENCES public.addon_system_catalog(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  connection_id text REFERENCES public.addon_connections(id) ON DELETE SET NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, addon_id, user_id)
);

CREATE TABLE IF NOT EXISTS public.addon_requests (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  addon_id text NOT NULL REFERENCES public.addon_system_catalog(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  request_type text NOT NULL DEFAULT 'setup' CHECK (request_type IN ('setup', 'install')),
  note text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'denied')),
  resolved_by uuid REFERENCES users(id) ON DELETE SET NULL,
  resolved_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS addon_requests_pending_idx
  ON public.addon_requests (tenant_id, addon_id, user_id)
  WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS addon_requests_tenant_idx
  ON public.addon_requests (tenant_id, status, created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.addon_system_catalog TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.tenant_addons TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.addon_connections TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.addon_installs TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.addon_requests TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0046_addons') ON CONFLICT DO NOTHING;

COMMIT;
