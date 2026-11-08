BEGIN;

REVOKE SELECT ON control.domain_settings FROM a8m_app;
REVOKE USAGE ON SCHEMA control FROM a8m_app;
DELETE FROM schema_migrations WHERE version = '0060_control_settings_webmail_grant';

COMMIT;
