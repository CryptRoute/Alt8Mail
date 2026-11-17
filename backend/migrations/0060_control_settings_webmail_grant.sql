-- The webmail runs as a8m_app and reads the Control Plane's per-mailbox daily
-- cap to keep its displayed limit and pre-send check in sync with the Postfix
-- policy daemon. Without these grants the lookup fails and the webmail falls
-- back to its default 200-message meter.
BEGIN;

GRANT USAGE ON SCHEMA control TO a8m_app;
GRANT SELECT ON control.domain_settings TO a8m_app;

-- 0059 predates its schema_migrations row; record it now so future updates do
-- not revisit the column change.
INSERT INTO schema_migrations (version) VALUES ('0059_control_outbound_user_cap')
ON CONFLICT (version) DO NOTHING;
INSERT INTO schema_migrations (version) VALUES ('0060_control_settings_webmail_grant')
ON CONFLICT (version) DO NOTHING;

COMMIT;
