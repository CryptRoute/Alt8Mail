BEGIN;

ALTER TABLE mail_identity_signatures
  DROP COLUMN IF EXISTS reply_to,
  DROP COLUMN IF EXISTS display_name,
  DROP COLUMN IF EXISTS signature_id;

ALTER TABLE mail_identities DROP COLUMN IF EXISTS signature_id;

DELETE FROM schema_migrations WHERE version = '0086_identity_signature_choices';

COMMIT;
