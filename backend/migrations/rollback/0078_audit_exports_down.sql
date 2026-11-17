BEGIN;
DROP TABLE IF EXISTS public.audit_exports;
DELETE FROM schema_migrations WHERE version = '0078_audit_exports';
COMMIT;
