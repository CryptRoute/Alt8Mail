-- Storage volume registry, host policy and capacity samples for the Control
-- Plane Disk Management surface. Volumes are identified by filesystem UUID and
-- mount path; a device path is informational only.
BEGIN;

CREATE SCHEMA IF NOT EXISTS control;

CREATE TABLE IF NOT EXISTS control.storage_volumes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL UNIQUE,
  mount_path text NOT NULL UNIQUE,
  fs_uuid text,
  device_path text NOT NULL DEFAULT '',
  fs_type text NOT NULL DEFAULT '',
  role text NOT NULL DEFAULT 'mail' CHECK (role IN ('mail', 'archive', 'general')),
  managed boolean NOT NULL DEFAULT false,
  state text NOT NULL DEFAULT 'online' CHECK (state IN ('online', 'degraded', 'offline', 'retiring', 'error')),
  size_bytes bigint NOT NULL DEFAULT 0,
  mount_options text NOT NULL DEFAULT '',
  fstab_managed boolean NOT NULL DEFAULT false,
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS storage_volumes_fs_uuid_idx
  ON control.storage_volumes (fs_uuid) WHERE fs_uuid IS NOT NULL AND fs_uuid <> '';
CREATE INDEX IF NOT EXISTS storage_volumes_state_idx ON control.storage_volumes (state);

CREATE TABLE IF NOT EXISTS control.storage_policy (
  id boolean PRIMARY KEY DEFAULT true CHECK (id = true),
  default_volume_id uuid REFERENCES control.storage_volumes(id) ON DELETE SET NULL,
  warning_percent integer NOT NULL DEFAULT 85 CHECK (warning_percent BETWEEN 1 AND 100),
  serious_percent integer NOT NULL DEFAULT 90 CHECK (serious_percent BETWEEN 1 AND 100),
  critical_percent integer NOT NULL DEFAULT 95 CHECK (critical_percent BETWEEN 1 AND 100),
  emergency_percent integer NOT NULL DEFAULT 98 CHECK (emergency_percent BETWEEN 1 AND 100),
  min_free_bytes bigint NOT NULL DEFAULT 10737418240 CHECK (min_free_bytes >= 0),
  migration_grace_hours integer NOT NULL DEFAULT 24 CHECK (migration_grace_hours BETWEEN 1 AND 336),
  max_concurrent_moves integer NOT NULL DEFAULT 2 CHECK (max_concurrent_moves BETWEEN 1 AND 16),
  provisioning_enabled boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO control.storage_policy (id) VALUES (true) ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS control.storage_samples (
  volume_id uuid NOT NULL REFERENCES control.storage_volumes(id) ON DELETE CASCADE,
  sampled_at timestamptz NOT NULL DEFAULT now(),
  total_bytes bigint NOT NULL DEFAULT 0,
  used_bytes bigint NOT NULL DEFAULT 0,
  free_bytes bigint NOT NULL DEFAULT 0,
  inode_used bigint NOT NULL DEFAULT 0,
  inode_total bigint NOT NULL DEFAULT 0,
  read_bytes bigint NOT NULL DEFAULT 0,
  write_bytes bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (volume_id, sampled_at)
);
CREATE INDEX IF NOT EXISTS storage_samples_time_idx ON control.storage_samples (sampled_at);

INSERT INTO schema_migrations (version) VALUES ('0088_storage_volumes')
ON CONFLICT (version) DO NOTHING;

COMMIT;
