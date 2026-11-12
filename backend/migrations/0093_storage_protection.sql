-- Capacity protection: cached guard state, threshold alerts and the nightly
-- mailbox/domain usage scan used for rebalance recommendations.
BEGIN;

ALTER TABLE control.storage_policy
  ADD COLUMN IF NOT EXISTS advisory_percent integer NOT NULL DEFAULT 80,
  ADD COLUMN IF NOT EXISTS reserve_percent integer NOT NULL DEFAULT 5,
  ADD COLUMN IF NOT EXISTS alert_rearm_hours integer NOT NULL DEFAULT 24,
  ADD COLUMN IF NOT EXISTS guard_enabled boolean NOT NULL DEFAULT true;

ALTER TABLE control.storage_policy DROP CONSTRAINT IF EXISTS storage_policy_advisory_check;
ALTER TABLE control.storage_policy
  ADD CONSTRAINT storage_policy_advisory_check CHECK (advisory_percent BETWEEN 1 AND 99);
ALTER TABLE control.storage_policy DROP CONSTRAINT IF EXISTS storage_policy_reserve_check;
ALTER TABLE control.storage_policy
  ADD CONSTRAINT storage_policy_reserve_check CHECK (reserve_percent BETWEEN 0 AND 50);
ALTER TABLE control.storage_policy DROP CONSTRAINT IF EXISTS storage_policy_rearm_check;
ALTER TABLE control.storage_policy
  ADD CONSTRAINT storage_policy_rearm_check CHECK (alert_rearm_hours BETWEEN 1 AND 720);

ALTER TABLE control.storage_volumes
  ADD COLUMN IF NOT EXISTS capacity_total_bytes bigint NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS capacity_used_bytes bigint NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS capacity_free_bytes bigint NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS capacity_percent double precision NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS capacity_state text NOT NULL DEFAULT 'unknown',
  ADD COLUMN IF NOT EXISTS capacity_checked_at timestamptz;

ALTER TABLE control.storage_volumes DROP CONSTRAINT IF EXISTS storage_volumes_capacity_state_check;
ALTER TABLE control.storage_volumes
  ADD CONSTRAINT storage_volumes_capacity_state_check
  CHECK (capacity_state IN ('unknown', 'ok', 'advisory', 'warning', 'serious', 'critical', 'emergency', 'offline'));

CREATE TABLE IF NOT EXISTS control.storage_alerts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  volume_id uuid NOT NULL REFERENCES control.storage_volumes(id) ON DELETE CASCADE,
  level text NOT NULL CHECK (level IN ('advisory', 'warning', 'serious', 'critical', 'emergency')),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'acknowledged', 'resolved')),
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  last_notified_at timestamptz,
  notify_count integer NOT NULL DEFAULT 0,
  acknowledged_by uuid,
  acknowledged_at timestamptz,
  detail jsonb NOT NULL DEFAULT '{}'::jsonb,
  UNIQUE (volume_id, level)
);
CREATE INDEX IF NOT EXISTS storage_alerts_status_idx
  ON control.storage_alerts (status, level, last_seen_at DESC);

CREATE TABLE IF NOT EXISTS control.storage_usage (
  volume_id uuid NOT NULL REFERENCES control.storage_volumes(id) ON DELETE CASCADE,
  mailbox_id uuid NOT NULL,
  email text NOT NULL DEFAULT '',
  domain text NOT NULL DEFAULT '',
  bytes bigint NOT NULL DEFAULT 0,
  messages bigint NOT NULL DEFAULT 0,
  scanned_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (volume_id, mailbox_id)
);
CREATE INDEX IF NOT EXISTS storage_usage_domain_idx
  ON control.storage_usage (volume_id, domain, bytes DESC);
CREATE INDEX IF NOT EXISTS storage_usage_scanned_idx
  ON control.storage_usage (scanned_at DESC);

INSERT INTO schema_migrations (version) VALUES ('0093_storage_protection')
ON CONFLICT (version) DO NOTHING;

COMMIT;
