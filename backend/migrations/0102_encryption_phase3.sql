-- Phase 3 server-managed S/MIME certificate metadata.
BEGIN;

ALTER TABLE public.user_crypto_identities
  ADD COLUMN IF NOT EXISTS cert_chain text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS subject text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS issuer text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS certificate_serial text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS not_before timestamptz,
  ADD COLUMN IF NOT EXISTS not_after timestamptz;

ALTER TABLE public.contact_crypto_keys
  ADD COLUMN IF NOT EXISTS cert_chain text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS subject text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS issuer text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS certificate_serial text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS not_before timestamptz;

CREATE INDEX IF NOT EXISTS contact_crypto_keys_protocol_idx
  ON public.contact_crypto_keys (user_id, protocol, email, updated_at DESC);

INSERT INTO schema_migrations (version) VALUES ('0102_encryption_phase3')
ON CONFLICT (version) DO NOTHING;

COMMIT;
