-- Drain or cancel client-side queued outbox rows before running this
-- rollback. Dropping client_mime makes those ciphertext-only items
-- undeliverable and removes browser-key publications.
BEGIN;

DROP INDEX IF EXISTS public.user_crypto_identities_directory_idx;

ALTER TABLE public.outbox
  DROP COLUMN IF EXISTS client_mime;

DELETE FROM public.user_crypto_identities WHERE source = 'client';

ALTER TABLE public.user_crypto_identities
  DROP CONSTRAINT IF EXISTS user_crypto_identities_source_check;
ALTER TABLE public.user_crypto_identities
  ADD CONSTRAINT user_crypto_identities_source_check
  CHECK (source IN ('generated', 'imported'));

DELETE FROM schema_migrations WHERE version = '0103_encryption_phase4';

COMMIT;
