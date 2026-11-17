-- Server-wide OAuth applications used by the webmail multi-mailbox feature.
-- Client secrets are sealed by the control plane before they reach this table.
BEGIN;

CREATE TABLE IF NOT EXISTS control.mail_oauth_providers (
  provider text PRIMARY KEY CHECK (provider IN ('gmail', 'microsoft')),
  enabled boolean NOT NULL DEFAULT false,
  client_id text NOT NULL DEFAULT '',
  tenant text NOT NULL DEFAULT '',
  secret_sealed text NOT NULL DEFAULT '',
  draft_saved boolean NOT NULL DEFAULT false,
  updated_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  applied_at timestamptz
);

INSERT INTO control.mail_oauth_providers (provider) VALUES ('gmail'), ('microsoft')
ON CONFLICT (provider) DO NOTHING;

GRANT SELECT, INSERT, UPDATE, DELETE ON control.mail_oauth_providers TO alt8mail_control;

INSERT INTO schema_migrations (version) VALUES ('0084_mail_oauth_config')
ON CONFLICT DO NOTHING;

COMMIT;
