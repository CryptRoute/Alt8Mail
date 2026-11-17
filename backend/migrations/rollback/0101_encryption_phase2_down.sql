BEGIN;

DROP INDEX IF EXISTS public.contact_crypto_keys_trust_idx;

ALTER TABLE public.user_crypto_identities
  DROP COLUMN IF EXISTS last_used_at;

ALTER TABLE public.contact_crypto_keys
  DROP COLUMN IF EXISTS verified_at,
  DROP COLUMN IF EXISTS key_changed_at,
  DROP COLUMN IF EXISTS last_seen_at,
  DROP COLUMN IF EXISTS not_after,
  DROP COLUMN IF EXISTS revoked_at;

DELETE FROM schema_migrations WHERE version = '0101_encryption_phase2';

COMMIT;
