BEGIN;

DROP TABLE IF EXISTS control.restore_jobs;
DELETE FROM schema_migrations WHERE version = '0063_restore_jobs';

COMMIT;
