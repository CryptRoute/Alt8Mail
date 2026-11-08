BEGIN;

ALTER TABLE control.domain_settings DROP COLUMN IF EXISTS user_daily_cap;

COMMIT;
