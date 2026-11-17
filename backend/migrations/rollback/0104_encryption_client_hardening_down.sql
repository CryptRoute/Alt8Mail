-- Removing alias rows keeps one identity per fingerprint so the older unique
-- constraint can be restored. Backups are browser-encrypted, but dropping the
-- table removes the account backup path.
BEGIN;

DROP TABLE IF EXISTS public.user_crypto_key_backups;

DELETE FROM public.user_crypto_identities a
USING public.user_crypto_identities b
WHERE a.user_id=b.user_id AND a.protocol=b.protocol AND a.fingerprint=b.fingerprint
  AND a.email > b.email;

ALTER TABLE public.user_crypto_identities
  DROP CONSTRAINT IF EXISTS user_crypto_identities_user_protocol_fingerprint_email_key;
ALTER TABLE public.user_crypto_identities
  ADD CONSTRAINT user_crypto_identities_user_id_protocol_fingerprint_key
  UNIQUE (user_id, protocol, fingerprint);

DELETE FROM schema_migrations WHERE version = '0104_encryption_client_hardening';

COMMIT;
