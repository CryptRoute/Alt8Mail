BEGIN;
DROP TABLE IF EXISTS public.webhook_deliveries;
DROP TABLE IF EXISTS public.webhook_endpoints;
DROP TABLE IF EXISTS public.api_idempotency;
DROP TABLE IF EXISTS public.api_keys;
DELETE FROM schema_migrations WHERE version = '0079_api_platform';
COMMIT;
