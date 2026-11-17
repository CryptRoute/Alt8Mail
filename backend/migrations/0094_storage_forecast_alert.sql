-- Projected-full notification window and its alert level.
BEGIN;

ALTER TABLE control.storage_policy
  ADD COLUMN IF NOT EXISTS forecast_alert_days integer NOT NULL DEFAULT 30;

ALTER TABLE control.storage_policy DROP CONSTRAINT IF EXISTS storage_policy_forecast_days_check;
ALTER TABLE control.storage_policy
  ADD CONSTRAINT storage_policy_forecast_days_check CHECK (forecast_alert_days BETWEEN 1 AND 365);

ALTER TABLE control.storage_alerts DROP CONSTRAINT IF EXISTS storage_alerts_level_check;
ALTER TABLE control.storage_alerts
  ADD CONSTRAINT storage_alerts_level_check
  CHECK (level IN ('advisory', 'warning', 'serious', 'critical', 'emergency', 'projected_full'));

INSERT INTO schema_migrations (version) VALUES ('0094_storage_forecast_alert')
ON CONFLICT (version) DO NOTHING;

COMMIT;
