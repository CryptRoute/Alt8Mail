-- Cabinet upload sessions (desktop + mobile QR) and share-link access requests.
BEGIN;

CREATE TABLE IF NOT EXISTS public.file_upload_sessions (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash text NOT NULL UNIQUE,
  provider_id text NOT NULL DEFAULT 'a8m',
  folder_id text REFERENCES public.file_folders(id) ON DELETE SET NULL,
  expires_at timestamptz NOT NULL,
  extended_at timestamptz,
  revoked_at timestamptz,
  uploads jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS file_upload_sessions_owner_idx
  ON public.file_upload_sessions (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS file_upload_sessions_expiry_idx
  ON public.file_upload_sessions (expires_at) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS public.file_share_access_requests (
  id text PRIMARY KEY,
  share_id text NOT NULL REFERENCES public.file_shares(id) ON DELETE CASCADE,
  file_id text NOT NULL REFERENCES public.files(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  owner_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  requester_email citext NOT NULL,
  message text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'declined')),
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz
);
CREATE INDEX IF NOT EXISTS file_share_access_requests_share_idx
  ON public.file_share_access_requests (share_id, created_at DESC);
CREATE INDEX IF NOT EXISTS file_share_access_requests_owner_idx
  ON public.file_share_access_requests (owner_user_id, status, created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.file_upload_sessions TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.file_share_access_requests TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0042_file_transfer_security') ON CONFLICT DO NOTHING;

COMMIT;
