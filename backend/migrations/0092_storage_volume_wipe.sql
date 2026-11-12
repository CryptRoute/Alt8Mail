-- A retired volume can be separately wiped and kept in the registry as a
-- tombstone. Wiping removes filesystem signatures; it is never implied by
-- retirement and always requires its own reviewed plan.
BEGIN;

ALTER TABLE control.storage_volumes DROP CONSTRAINT IF EXISTS storage_volumes_state_check;
ALTER TABLE control.storage_volumes
  ADD CONSTRAINT storage_volumes_state_check
  CHECK (state IN ('online', 'degraded', 'offline', 'retiring', 'error', 'provisioning', 'reboot_pending', 'retired', 'wiped'));

INSERT INTO schema_migrations (version) VALUES ('0092_storage_volume_wipe')
ON CONFLICT (version) DO NOTHING;

COMMIT;
