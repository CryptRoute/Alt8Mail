BEGIN;

ALTER TABLE control.storage_volumes DROP CONSTRAINT IF EXISTS storage_volumes_state_check;
ALTER TABLE control.storage_volumes
  ADD CONSTRAINT storage_volumes_state_check
  CHECK (state IN ('online', 'degraded', 'offline', 'retiring', 'error'));
ALTER TABLE control.storage_volumes DROP COLUMN IF EXISTS verified_at;
ALTER TABLE control.storage_volumes DROP COLUMN IF EXISTS reboot_verified;
ALTER TABLE control.storage_volumes DROP COLUMN IF EXISTS mount_unit;
ALTER TABLE control.storage_volumes DROP COLUMN IF EXISTS device_wwn;
ALTER TABLE control.storage_volumes DROP COLUMN IF EXISTS device_serial;

DELETE FROM schema_migrations WHERE version = '0091_storage_host_details';

COMMIT;
