-- Application update jobs requested from System Control. The job row lives in
-- PostgreSQL so it survives the control-plane restart performed by the updater.
BEGIN;

CREATE TABLE IF NOT EXISTS control.app_update_jobs (
  id text PRIMARY KEY,
  actor_id uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  actor_email citext NOT NULL DEFAULT '',
  actor_ip inet,
  user_agent text NOT NULL DEFAULT '',
  from_version text NOT NULL DEFAULT '',
  target_version text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'queued'
    CHECK (status IN ('queued', 'running', 'completed', 'failed')),
  unit_name text NOT NULL DEFAULT '',
  log_path text NOT NULL DEFAULT '',
  detail text NOT NULL DEFAULT '',
  requested_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  finished_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS control_app_update_jobs_requested_idx
  ON control.app_update_jobs (requested_at DESC);

-- Only one release update may be queued or running at a time. The expression
-- returns the same true value for every active row, while completed/failed
-- history is excluded.
CREATE UNIQUE INDEX IF NOT EXISTS control_app_update_jobs_one_active
  ON control.app_update_jobs ((status IN ('queued', 'running')))
  WHERE status IN ('queued', 'running');

INSERT INTO schema_migrations (version) VALUES ('0024_system_ops')
ON CONFLICT (version) DO NOTHING;

COMMIT;
