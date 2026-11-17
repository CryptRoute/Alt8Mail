-- The Control Plane owns which shipped webmail locales are available.
-- Webmail reads this single row to filter its manifest-backed language picker,
-- so a disabled language cannot be selected or persisted as a preference.
BEGIN;

CREATE TABLE IF NOT EXISTS control.webmail_locales (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  default_locale text NOT NULL DEFAULT 'en-US',
  disabled jsonb NOT NULL DEFAULT '[]'::jsonb,
  updated_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO control.webmail_locales (singleton)
VALUES (true)
ON CONFLICT (singleton) DO NOTHING;

GRANT USAGE ON SCHEMA control TO a8m_app;
GRANT SELECT ON control.webmail_locales TO a8m_app;

-- The Control Plane owns the policy and needs to read and write the row.
GRANT USAGE ON SCHEMA control TO alt8mail_control;
GRANT SELECT, INSERT, UPDATE, DELETE ON control.webmail_locales TO alt8mail_control;

INSERT INTO schema_migrations (version) VALUES ('0097_control_webmail_locales')
ON CONFLICT (version) DO NOTHING;

COMMIT;
