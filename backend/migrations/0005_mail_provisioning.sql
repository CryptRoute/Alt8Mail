-- Explicit mailbox provisioning state and password history for policy enforcement.
BEGIN;

ALTER TABLE mailboxes
  ADD COLUMN IF NOT EXISTS provision_status text NOT NULL DEFAULT 'pending',
  ADD COLUMN IF NOT EXISTS provisioned_at timestamptz,
  ADD COLUMN IF NOT EXISTS provision_attempts integer NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS provision_error text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS mailboxes_provision_idx ON mailboxes (provision_status, active);

CREATE TABLE IF NOT EXISTS password_history (
  id bigserial PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  password_hash text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS password_history_user_idx ON password_history (user_id, created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON mailboxes, password_history TO meridian_app;
GRANT USAGE, SELECT ON SEQUENCE password_history_id_seq TO meridian_app;
GRANT SELECT ON mailboxes, groups, group_members TO meridian_mail;

INSERT INTO schema_migrations (version) VALUES ('0005_mail_provisioning')
ON CONFLICT (version) DO NOTHING;

COMMIT;
