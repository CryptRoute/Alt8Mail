BEGIN;
DROP TABLE IF EXISTS public.tenant_telemetry;
DROP TABLE IF EXISTS public.phishing_targets;
DROP TABLE IF EXISTS public.phishing_campaigns;
DROP TABLE IF EXISTS public.migration_reports;
DROP TABLE IF EXISTS public.crm_sync_state;
DROP TABLE IF EXISTS public.crm_records;
DELETE FROM schema_migrations WHERE version = '0080_ecosystem';
COMMIT;
