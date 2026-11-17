BEGIN;

DROP TABLE IF EXISTS control.mail_oauth_providers;

DELETE FROM schema_migrations WHERE version = '0084_mail_oauth_config';

COMMIT;
