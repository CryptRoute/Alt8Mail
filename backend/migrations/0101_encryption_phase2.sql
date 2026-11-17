-- Phase 2 OpenPGP trust, discovery and shared-mailbox boundaries.
BEGIN;

ALTER TABLE public.contact_crypto_keys
  ADD COLUMN IF NOT EXISTS verified_at timestamptz,
  ADD COLUMN IF NOT EXISTS key_changed_at timestamptz,
  ADD COLUMN IF NOT EXISTS last_seen_at timestamptz,
  ADD COLUMN IF NOT EXISTS not_after timestamptz,
  ADD COLUMN IF NOT EXISTS revoked_at timestamptz;

ALTER TABLE public.user_crypto_identities
  ADD COLUMN IF NOT EXISTS last_used_at timestamptz;

CREATE INDEX IF NOT EXISTS contact_crypto_keys_trust_idx
  ON public.contact_crypto_keys (user_id, email, protocol, trust, updated_at DESC);

INSERT INTO schema_migrations (version) VALUES ('0101_encryption_phase2')
ON CONFLICT (version) DO NOTHING;

COMMIT;
