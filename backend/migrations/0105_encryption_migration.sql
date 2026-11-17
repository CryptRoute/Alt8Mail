-- Guarded migration from server-managed to client-side encryption.
--
-- An export row proves that the user has received the private half of an
-- identity before the server deletes its wrapped copy. The migration record
-- keeps the preflight report and the exact private-key count for audit.
BEGIN;

CREATE TABLE IF NOT EXISTS public.user_crypto_key_exports (
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  protocol text NOT NULL CHECK (protocol IN ('openpgp', 'smime')),
  fingerprint text NOT NULL,
  exported_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, protocol, fingerprint)
);

CREATE INDEX IF NOT EXISTS user_crypto_key_exports_tenant_idx
  ON public.user_crypto_key_exports (tenant_id, protocol, fingerprint);

CREATE TABLE IF NOT EXISTS public.tenant_encryption_migrations (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  from_mode text NOT NULL,
  to_mode text NOT NULL,
  status text NOT NULL CHECK (status IN ('completed', 'failed')),
  report jsonb NOT NULL DEFAULT '{}'::jsonb,
  private_keys_deleted integer NOT NULL DEFAULT 0,
  actor_user_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz
);

CREATE INDEX IF NOT EXISTS tenant_encryption_migrations_tenant_idx
  ON public.tenant_encryption_migrations (tenant_id, created_at DESC);

-- A tenant that has switched to client-side mode must never acquire a new
-- server-held private key, even through a stale background job or a race with
-- the migration transaction.
CREATE OR REPLACE FUNCTION public.reject_client_side_private_key() RETURNS trigger AS $$
DECLARE
  tenant_mode text;
BEGIN
  SELECT COALESCE(t.settings->'encryption'->>'mode', '')
    INTO tenant_mode
    FROM public.user_crypto_identities i
    JOIN public.tenants t ON t.id = i.tenant_id
   WHERE i.id = NEW.identity_id;
  IF tenant_mode = 'client_side' THEN
    RAISE EXCEPTION 'server-held private keys are not allowed in client-side encryption mode';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS user_crypto_private_keys_client_side_guard ON public.user_crypto_private_keys;
CREATE TRIGGER user_crypto_private_keys_client_side_guard
  BEFORE INSERT OR UPDATE ON public.user_crypto_private_keys
  FOR EACH ROW EXECUTE FUNCTION public.reject_client_side_private_key();

GRANT SELECT, INSERT, UPDATE, DELETE ON public.user_crypto_key_exports TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.tenant_encryption_migrations TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0105_encryption_migration')
ON CONFLICT (version) DO NOTHING;

COMMIT;
