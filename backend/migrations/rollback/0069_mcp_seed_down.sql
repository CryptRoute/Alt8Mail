BEGIN;
DROP TABLE IF EXISTS public.mcp_tokens;
DROP TABLE IF EXISTS public.mcp_authorization_codes;
DROP TABLE IF EXISTS public.mcp_connections;
DROP TABLE IF EXISTS public.mcp_clients;
DELETE FROM schema_migrations WHERE version = '0069_mcp_seed';
COMMIT;
