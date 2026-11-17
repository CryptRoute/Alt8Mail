-- Phase 4 hardening: alias-specific public-key rows and zero-knowledge
-- encrypted key backups. The backup blob is encrypted by the browser before
-- upload; the database stores no unwrapped private key.
BEGIN;

ALTER TABLE public.user_crypto_identities
  DROP CONSTRAINT IF EXISTS user_crypto_identities_user_id_protocol_fingerprint_key;
ALTER TABLE public.user_crypto_identities
  ADD CONSTRAINT user_crypto_identities_user_protocol_fingerprint_email_key
  UNIQUE (user_id, protocol, fingerprint, email);

CREATE TABLE IF NOT EXISTS public.user_crypto_key_backups (
  user_id uuid PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  email citext NOT NULL,
  fingerprint text NOT NULL,
  public_key text NOT NULL,
  encrypted_private_key text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.user_crypto_key_backups TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0104_encryption_client_hardening')
ON CONFLICT (version) DO NOTHING;

COMMIT;
