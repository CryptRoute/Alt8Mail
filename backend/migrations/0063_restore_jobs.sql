-- Phase 1G: per-mailbox/per-folder restore jobs.
BEGIN;

CREATE TABLE IF NOT EXISTS control.restore_jobs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  backup_id uuid NOT NULL REFERENCES control.backups(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  email citext NOT NULL,
  folder text NOT NULL DEFAULT '',
  mode text NOT NULL CHECK (mode IN ('preview','apply')),
  status text NOT NULL DEFAULT 'running' CHECK (status IN ('running','completed','failed')),
  preview jsonb NOT NULL DEFAULT '{}'::jsonb,
  result jsonb NOT NULL DEFAULT '{}'::jsonb,
  detail text NOT NULL DEFAULT '',
  created_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);
CREATE INDEX IF NOT EXISTS control_restore_jobs_created_idx
  ON control.restore_jobs (created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON control.restore_jobs TO alt8mail_control;

INSERT INTO schema_migrations (version) VALUES ('0063_restore_jobs')
ON CONFLICT (version) DO NOTHING;

COMMIT;
