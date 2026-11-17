BEGIN;

DROP TABLE IF EXISTS control.webmail_locales;
DELETE FROM schema_migrations WHERE version = '0097_control_webmail_locales';

COMMIT;
