BEGIN;

DROP TABLE IF EXISTS control.storage_usage;
DROP TABLE IF EXISTS control.storage_alerts;

ALTER TABLE control.storage_volumes DROP CONSTRAINT IF EXISTS storage_volumes_capacity_state_check;
ALTER TABLE control.storage_volumes
  DROP COLUMN IF EXISTS capacity_total_bytes,
  DROP COLUMN IF EXISTS capacity_used_bytes,
  DROP COLUMN IF EXISTS capacity_free_bytes,
  DROP COLUMN IF EXISTS capacity_percent,
  DROP COLUMN IF EXISTS capacity_state,
  DROP COLUMN IF EXISTS capacity_checked_at;

ALTER TABLE control.storage_policy
  DROP CONSTRAINT IF EXISTS storage_policy_advisory_check,
  DROP CONSTRAINT IF EXISTS storage_policy_reserve_check,
  DROP CONSTRAINT IF EXISTS storage_policy_rearm_check,
  DROP COLUMN IF EXISTS advisory_percent,
  DROP COLUMN IF EXISTS reserve_percent,
  DROP COLUMN IF EXISTS alert_rearm_hours,
  DROP COLUMN IF EXISTS guard_enabled;

DELETE FROM schema_migrations WHERE version = '0093_storage_protection';

COMMIT;
