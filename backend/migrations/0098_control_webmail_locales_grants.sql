-- Re-assert the language-policy grants for hosts that may have applied the
-- first 3.7.1 migration build before its Control Plane grant was added.
BEGIN;

GRANT USAGE ON SCHEMA control TO a8m_app;
GRANT SELECT ON control.webmail_locales TO a8m_app;
GRANT USAGE ON SCHEMA control TO alt8mail_control;
GRANT SELECT, INSERT, UPDATE, DELETE ON control.webmail_locales TO alt8mail_control;

INSERT INTO schema_migrations (version) VALUES ('0098_control_webmail_locales_grants')
ON CONFLICT (version) DO NOTHING;

COMMIT;
