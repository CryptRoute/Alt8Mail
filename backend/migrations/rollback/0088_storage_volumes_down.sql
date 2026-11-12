BEGIN;

DROP TABLE IF EXISTS control.storage_samples;
DROP TABLE IF EXISTS control.storage_policy;
DROP TABLE IF EXISTS control.storage_volumes;

DELETE FROM schema_migrations WHERE version = '0088_storage_volumes';

COMMIT;
