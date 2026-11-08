-- Multi-account Phase 2: OAuth2 authorization state, custom folder sync and
-- remote-deletion reconciliation support.
BEGIN;

-- Connected folders that are visible but must not be mirrored as their own
-- message set (Gmail's virtual All Mail) carry sync_enabled = false. Custom
-- selectable folders are synced and appear in the connected-account sidebar.
ALTER TABLE public.mail_folders
  ADD COLUMN IF NOT EXISTS sync_enabled boolean NOT NULL DEFAULT true;

-- One-time OAuth authorization requests. The PKCE verifier is sealed with the
-- workspace key because it is as sensitive as a short-lived credential. The
-- optional account id lets an existing account be reconnected in place.
CREATE TABLE IF NOT EXISTS public.mail_oauth_states (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  provider text NOT NULL CHECK (provider IN ('gmail', 'microsoft')),
  verifier_sealed text NOT NULL DEFAULT '',
  account_id text REFERENCES public.mail_accounts(id) ON DELETE CASCADE,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mail_oauth_states_expiry_idx
  ON public.mail_oauth_states (expires_at);
CREATE INDEX IF NOT EXISTS mail_oauth_states_user_idx
  ON public.mail_oauth_states (user_id, created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.mail_oauth_states TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.mail_folders TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0058_mail_phase2')
ON CONFLICT (version) DO NOTHING;

COMMIT;
