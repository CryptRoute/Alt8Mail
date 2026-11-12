-- Roll back the wiped state. Existing wiped rows return to retired so the
-- 0091 constraint can be restored without losing their history.
BEGIN;

UPDATE control.storage_volumes SET state = 'retired' WHERE state = 'wiped';

ALTER TABLE control.storage_volumes DROP CONSTRAINT IF EXISTS storage_volumes_state_check;
ALTER TABLE control.storage_volumes
  ADD CONSTRAINT storage_volumes_state_check
  CHECK (state IN ('online', 'degraded', 'offline', 'retiring', 'error', 'provisioning', 'reboot_pending', 'retired'));

DELETE FROM schema_migrations WHERE version = '0092_storage_volume_wipe';

COMMIT;
