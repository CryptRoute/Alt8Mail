-- Host identity and verification state for managed and adopted volumes.
BEGIN;

ALTER TABLE control.storage_volumes ADD COLUMN IF NOT EXISTS device_serial text NOT NULL DEFAULT '';
ALTER TABLE control.storage_volumes ADD COLUMN IF NOT EXISTS device_wwn text NOT NULL DEFAULT '';
ALTER TABLE control.storage_volumes ADD COLUMN IF NOT EXISTS mount_unit text NOT NULL DEFAULT '';
ALTER TABLE control.storage_volumes ADD COLUMN IF NOT EXISTS reboot_verified boolean NOT NULL DEFAULT false;
ALTER TABLE control.storage_volumes ADD COLUMN IF NOT EXISTS verified_at timestamptz;

ALTER TABLE control.storage_volumes DROP CONSTRAINT IF EXISTS storage_volumes_state_check;
ALTER TABLE control.storage_volumes
  ADD CONSTRAINT storage_volumes_state_check
  CHECK (state IN ('online', 'degraded', 'offline', 'retiring', 'error', 'provisioning', 'reboot_pending', 'retired'));

INSERT INTO schema_migrations (version) VALUES ('0091_storage_host_details')
ON CONFLICT (version) DO NOTHING;

COMMIT;
