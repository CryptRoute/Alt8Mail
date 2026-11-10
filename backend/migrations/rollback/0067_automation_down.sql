BEGIN;
DROP TABLE IF EXISTS public.automation_runs;
DROP TABLE IF EXISTS public.automation_rules;
DELETE FROM schema_migrations WHERE version = '0067_automation';
COMMIT;
