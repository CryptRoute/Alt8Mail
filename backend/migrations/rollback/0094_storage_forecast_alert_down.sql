BEGIN;

DELETE FROM control.storage_alerts WHERE level = 'projected_full';
ALTER TABLE control.storage_alerts DROP CONSTRAINT IF EXISTS storage_alerts_level_check;
ALTER TABLE control.storage_alerts
  ADD CONSTRAINT storage_alerts_level_check
  CHECK (level IN ('advisory', 'warning', 'serious', 'critical', 'emergency'));

ALTER TABLE control.storage_policy
  DROP CONSTRAINT IF EXISTS storage_policy_forecast_days_check,
  DROP COLUMN IF EXISTS forecast_alert_days;

DELETE FROM schema_migrations WHERE version = '0094_storage_forecast_alert';

COMMIT;
