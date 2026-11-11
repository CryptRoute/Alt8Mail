BEGIN;

DELETE FROM schema_migrations WHERE version = '0085_mail_sync_transient_status';

COMMIT;
