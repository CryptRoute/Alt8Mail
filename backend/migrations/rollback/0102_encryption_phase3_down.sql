BEGIN;

DROP INDEX IF EXISTS public.contact_crypto_keys_protocol_idx;

ALTER TABLE public.contact_crypto_keys
  DROP COLUMN IF EXISTS cert_chain,
  DROP COLUMN IF EXISTS subject,
  DROP COLUMN IF EXISTS issuer,
  DROP COLUMN IF EXISTS certificate_serial,
  DROP COLUMN IF EXISTS not_before;

ALTER TABLE public.user_crypto_identities
  DROP COLUMN IF EXISTS cert_chain,
  DROP COLUMN IF EXISTS subject,
  DROP COLUMN IF EXISTS issuer,
  DROP COLUMN IF EXISTS certificate_serial,
  DROP COLUMN IF EXISTS not_before,
  DROP COLUMN IF EXISTS not_after;

DELETE FROM schema_migrations WHERE version = '0102_encryption_phase3';

COMMIT;
