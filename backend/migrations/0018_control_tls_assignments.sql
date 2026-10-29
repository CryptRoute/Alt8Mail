-- Record which certificate was mapped to each service by the control plane.
-- The live configuration remains authoritative; this table supplies an
-- operator-visible history and makes reconciliation explicit.
BEGIN;

CREATE TABLE IF NOT EXISTS control.tls_assignments (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  certificate_name text NOT NULL,
  target text NOT NULL CHECK (target IN ('web', 'postfix', 'dovecot')),
  cert_path text NOT NULL,
  key_path text NOT NULL,
  previous_cert_path text NOT NULL DEFAULT '',
  previous_key_path text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'applied' CHECK (status IN ('applied', 'rolled_back', 'failed')),
  detail text NOT NULL DEFAULT '',
  applied_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  applied_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (certificate_name, target)
);
CREATE INDEX IF NOT EXISTS control_tls_assignments_target_idx
  ON control.tls_assignments (target, applied_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON control.tls_assignments TO alt8mail_control;

INSERT INTO schema_migrations (version) VALUES ('0018_control_tls_assignments')
ON CONFLICT (version) DO NOTHING;

COMMIT;
