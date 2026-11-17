-- Phase 4 client-side E2EE: browser-held private keys and ciphertext-only
-- outbound storage. The server stores only the encrypted MIME body.
BEGIN;

ALTER TABLE public.user_crypto_identities
  DROP CONSTRAINT IF EXISTS user_crypto_identities_source_check;
ALTER TABLE public.user_crypto_identities
  ADD CONSTRAINT user_crypto_identities_source_check
  CHECK (source IN ('generated', 'imported', 'client'));

ALTER TABLE public.outbox
  ADD COLUMN IF NOT EXISTS client_mime text NOT NULL DEFAULT '';

-- The browser directory looks up up to 50 addresses by tenant, protocol,
-- status and source. Without this index the lookup scans every identity in the
-- tenant as the mailbox count grows.
CREATE INDEX IF NOT EXISTS user_crypto_identities_directory_idx
  ON public.user_crypto_identities
     (tenant_id, protocol, status, source, lower(email::text));

INSERT INTO schema_migrations (version) VALUES ('0103_encryption_phase4')
ON CONFLICT (version) DO NOTHING;

COMMIT;
