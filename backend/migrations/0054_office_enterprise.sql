-- Office Phase 4: internal/group file permissions, per-version DLP results
-- and the enterprise review/export surface.
BEGIN;

CREATE TABLE IF NOT EXISTS public.file_permissions (
  id text PRIMARY KEY,
  file_id text NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  grantee_type text NOT NULL CHECK (grantee_type IN ('user', 'group')),
  grantee_id uuid NOT NULL,
  access text NOT NULL DEFAULT 'view' CHECK (access IN ('view', 'edit')),
  created_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (file_id, grantee_type, grantee_id)
);
CREATE INDEX IF NOT EXISTS file_permissions_file_idx
  ON file_permissions (file_id, created_at DESC);
CREATE INDEX IF NOT EXISTS file_permissions_grantee_idx
  ON file_permissions (tenant_id, grantee_type, grantee_id);

ALTER TABLE public.office_document_versions
  ADD COLUMN IF NOT EXISTS dlp_classification text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS dlp_warnings jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN IF NOT EXISTS dlp_matches jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN IF NOT EXISTS review_status text NOT NULL DEFAULT ''
    CHECK (review_status IN ('', 'approved', 'changes_requested')),
  ADD COLUMN IF NOT EXISTS review_note text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS reviewed_by uuid REFERENCES users(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS reviewed_by_email citext,
  ADD COLUMN IF NOT EXISTS reviewed_at timestamptz;

GRANT SELECT, INSERT, UPDATE, DELETE ON public.file_permissions TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0054_office_enterprise') ON CONFLICT DO NOTHING;

COMMIT;
