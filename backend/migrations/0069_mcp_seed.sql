-- Phase 2 MCP seed: public OAuth clients, per-user connections, PKCE
-- authorization codes and bearer tokens. No admin/send/delete scopes.
BEGIN;

CREATE TABLE IF NOT EXISTS public.mcp_clients (
  id text PRIMARY KEY,
  client_name text NOT NULL,
  redirect_uris text[] NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.mcp_connections (
  id text PRIMARY KEY,
  client_id text NOT NULL REFERENCES public.mcp_clients(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  scopes text[] NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
  created_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz
);
CREATE INDEX IF NOT EXISTS mcp_connections_user_idx
  ON public.mcp_connections (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS public.mcp_authorization_codes (
  code_hash text PRIMARY KEY,
  connection_id text NOT NULL REFERENCES public.mcp_connections(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  scopes text[] NOT NULL DEFAULT '{}',
  redirect_uri text NOT NULL,
  code_challenge text NOT NULL,
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mcp_auth_codes_expiry_idx ON public.mcp_authorization_codes (expires_at);

CREATE TABLE IF NOT EXISTS public.mcp_tokens (
  token_hash text PRIMARY KEY,
  connection_id text NOT NULL REFERENCES public.mcp_connections(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  scopes text[] NOT NULL DEFAULT '{}',
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz
);
CREATE INDEX IF NOT EXISTS mcp_tokens_connection_idx ON public.mcp_tokens (connection_id);
CREATE INDEX IF NOT EXISTS mcp_tokens_expiry_idx ON public.mcp_tokens (expires_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.mcp_clients TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.mcp_connections TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.mcp_authorization_codes TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.mcp_tokens TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0069_mcp_seed')
ON CONFLICT DO NOTHING;

COMMIT;
