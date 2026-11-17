-- Phase 1 server-managed OpenPGP encryption.
--
-- Private keys are never stored in plaintext. The private-key material is
-- wrapped by the application crypto key before it reaches this table.
BEGIN;

CREATE TABLE IF NOT EXISTS public.user_crypto_identities (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  email citext NOT NULL,
  protocol text NOT NULL DEFAULT 'openpgp' CHECK (protocol IN ('openpgp', 'smime')),
  key_id text NOT NULL,
  fingerprint text NOT NULL,
  public_key text NOT NULL,
  source text NOT NULL DEFAULT 'generated' CHECK (source IN ('generated', 'imported')),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'retired', 'revoked')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, protocol, fingerprint)
);
CREATE INDEX IF NOT EXISTS user_crypto_identities_user_idx
  ON public.user_crypto_identities (user_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS public.user_crypto_private_keys (
  identity_id text PRIMARY KEY REFERENCES public.user_crypto_identities(id) ON DELETE CASCADE,
  wrapped_key text NOT NULL,
  wrap_version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  retired_at timestamptz
);

CREATE TABLE IF NOT EXISTS public.contact_crypto_keys (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  email citext NOT NULL,
  protocol text NOT NULL DEFAULT 'openpgp' CHECK (protocol IN ('openpgp', 'smime')),
  key_id text NOT NULL,
  fingerprint text NOT NULL,
  public_key text NOT NULL,
  trust text NOT NULL DEFAULT 'unknown' CHECK (trust IN ('unknown', 'tofu', 'verified', 'enterprise', 'revoked')),
  source text NOT NULL DEFAULT 'manual',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, email, protocol, fingerprint)
);
CREATE INDEX IF NOT EXISTS contact_crypto_keys_user_idx
  ON public.contact_crypto_keys (user_id, email, protocol, updated_at DESC);

ALTER TABLE public.mail_messages
  ADD COLUMN IF NOT EXISTS crypto_protocol text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS crypto_encrypted boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS crypto_signed boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS crypto_state text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS crypto_signer text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS crypto_error text NOT NULL DEFAULT '';

ALTER TABLE public.outbox
  ADD COLUMN IF NOT EXISTS crypto_protocol text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS crypto_encrypt boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS crypto_sign boolean NOT NULL DEFAULT false;

GRANT SELECT, INSERT, UPDATE, DELETE ON public.user_crypto_identities TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.user_crypto_private_keys TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.contact_crypto_keys TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0100_encryption_phase1')
ON CONFLICT (version) DO NOTHING;

COMMIT;
