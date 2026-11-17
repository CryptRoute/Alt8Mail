BEGIN;

DROP TABLE IF EXISTS control.storage_move_items;
DROP TABLE IF EXISTS control.storage_operations;

DELETE FROM schema_migrations WHERE version = '0090_storage_operations';

COMMIT;
