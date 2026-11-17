-- Phase 5 compliance and data governance: BYOK envelope keys, file holds,
-- share approvals, eDiscovery exports, OCR/full-text index and DLP review.
BEGIN;

CREATE TABLE IF NOT EXISTS public.tenant_encryption_keys (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  version integer NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'retired', 'revoked')),
  wrapped_dek text NOT NULL,
  key_fingerprint text NOT NULL,
  created_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  rotated_at timestamptz,
  retired_at timestamptz,
  UNIQUE (tenant_id, version)
);
CREATE INDEX IF NOT EXISTS tenant_encryption_keys_tenant_idx
  ON public.tenant_encryption_keys (tenant_id, version DESC);

CREATE TABLE IF NOT EXISTS public.file_legal_holds (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  scope text NOT NULL CHECK (scope IN ('tenant', 'user', 'file')),
  user_id uuid REFERENCES public.users(id) ON DELETE CASCADE,
  file_id text REFERENCES public.files(id) ON DELETE CASCADE,
  name text NOT NULL,
  reason text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'released')),
  created_by_email citext NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  released_at timestamptz
);
CREATE INDEX IF NOT EXISTS file_legal_holds_tenant_idx
  ON public.file_legal_holds (tenant_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS file_legal_holds_file_idx
  ON public.file_legal_holds (tenant_id, file_id, status);

CREATE TABLE IF NOT EXISTS public.file_share_requests (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  file_id text NOT NULL REFERENCES public.files(id) ON DELETE CASCADE,
  requested_by uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  access text NOT NULL DEFAULT 'view' CHECK (access IN ('view', 'edit')),
  recipient_email citext NOT NULL DEFAULT '',
  expires_days integer NOT NULL DEFAULT 7,
  state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'approved', 'denied')),
  decided_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  decided_at timestamptz,
  decision_note text NOT NULL DEFAULT '',
  share_id text REFERENCES public.file_shares(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS file_share_requests_tenant_idx
  ON public.file_share_requests (tenant_id, state, created_at DESC);

CREATE TABLE IF NOT EXISTS public.discovery_exports (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  case_name text NOT NULL,
  format text NOT NULL CHECK (format IN ('json', 'mbox', 'csv')),
  query jsonb NOT NULL DEFAULT '{}'::jsonb,
  item_count integer NOT NULL DEFAULT 0,
  manifest_sha256 text NOT NULL DEFAULT '',
  signature text NOT NULL DEFAULT '',
  created_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS discovery_exports_tenant_idx
  ON public.discovery_exports (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS public.file_text_index (
  file_id text PRIMARY KEY REFERENCES public.files(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  content text NOT NULL DEFAULT '',
  source text NOT NULL DEFAULT 'text' CHECK (source IN ('text', 'ocr', 'unavailable')),
  ocr_used boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS file_text_index_user_idx
  ON public.file_text_index (user_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS file_text_index_search_idx
  ON public.file_text_index USING GIN (to_tsvector('simple', content));

CREATE TABLE IF NOT EXISTS public.dlp_false_positives (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  rule_id text NOT NULL,
  detector text NOT NULL DEFAULT 'terms',
  match_preview text NOT NULL DEFAULT '',
  context text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'confirmed', 'false_positive')),
  reviewed_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS dlp_false_positives_tenant_idx
  ON public.dlp_false_positives (tenant_id, status, created_at DESC);

ALTER TABLE public.office_document_versions
  ADD COLUMN IF NOT EXISTS encryption_key_version integer NOT NULL DEFAULT 0;

GRANT SELECT, INSERT, UPDATE, DELETE ON public.tenant_encryption_keys TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.file_legal_holds TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.file_share_requests TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.discovery_exports TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.file_text_index TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.dlp_false_positives TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0076_governance')
ON CONFLICT DO NOTHING;

COMMIT;
