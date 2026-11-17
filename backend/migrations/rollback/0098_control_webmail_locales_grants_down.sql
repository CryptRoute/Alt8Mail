BEGIN;

DELETE FROM schema_migrations WHERE version = '0098_control_webmail_locales_grants';

COMMIT;
