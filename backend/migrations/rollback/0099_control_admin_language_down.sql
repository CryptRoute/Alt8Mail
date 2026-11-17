BEGIN;

ALTER TABLE control.admin_users DROP COLUMN IF EXISTS language;
DELETE FROM schema_migrations WHERE version = '0099_control_admin_language';

COMMIT;
