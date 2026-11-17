BEGIN;

ALTER TABLE public.outbox
  DROP COLUMN IF EXISTS crypto_protocol,
  DROP COLUMN IF EXISTS crypto_encrypt,
  DROP COLUMN IF EXISTS crypto_sign;

ALTER TABLE public.mail_messages
  DROP COLUMN IF EXISTS crypto_protocol,
  DROP COLUMN IF EXISTS crypto_encrypted,
  DROP COLUMN IF EXISTS crypto_signed,
  DROP COLUMN IF EXISTS crypto_state,
  DROP COLUMN IF EXISTS crypto_signer,
  DROP COLUMN IF EXISTS crypto_error;

DROP TABLE IF EXISTS public.contact_crypto_keys;
DROP TABLE IF EXISTS public.user_crypto_private_keys;
DROP TABLE IF EXISTS public.user_crypto_identities;

DELETE FROM schema_migrations WHERE version = '0100_encryption_phase1';

COMMIT;
