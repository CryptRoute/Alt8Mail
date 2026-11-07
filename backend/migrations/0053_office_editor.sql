-- Office Phase 2: editable Cabinet documents, immutable versions, editor
-- sessions and idempotent OnlyOffice callback bookkeeping.
BEGIN;

CREATE TABLE IF NOT EXISTS public.office_documents (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  file_id text NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  format text NOT NULL,
  document_type text NOT NULL CHECK (document_type IN ('word', 'cell', 'slide', 'pdf')),
  current_version integer NOT NULL DEFAULT 1,
  current_key text NOT NULL DEFAULT '',
  last_saved_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (file_id)
);
CREATE INDEX IF NOT EXISTS office_documents_user_idx
  ON office_documents (user_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS public.office_document_versions (
  id text PRIMARY KEY,
  document_id text NOT NULL REFERENCES office_documents(id) ON DELETE CASCADE,
  version integer NOT NULL,
  storage_key text NOT NULL,
  size_bytes bigint NOT NULL DEFAULT 0,
  checksum text NOT NULL DEFAULT '',
  source text NOT NULL DEFAULT 'editor'
    CHECK (source IN ('template', 'initial', 'editor', 'restore')),
  created_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (document_id, version)
);
CREATE INDEX IF NOT EXISTS office_document_versions_idx
  ON office_document_versions (document_id, version DESC);

CREATE TABLE IF NOT EXISTS public.office_edit_sessions (
  id text PRIMARY KEY,
  document_id text NOT NULL REFERENCES office_documents(id) ON DELETE CASCADE,
  document_key text NOT NULL,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  status text NOT NULL DEFAULT 'open'
    CHECK (status IN ('open', 'saved', 'closed', 'error')),
  last_status integer NOT NULL DEFAULT 0,
  last_callback_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (document_id, document_key)
);
CREATE INDEX IF NOT EXISTS office_edit_sessions_document_idx
  ON office_edit_sessions (document_id, created_at DESC);

CREATE TABLE IF NOT EXISTS public.office_callback_events (
  id text PRIMARY KEY,
  document_id text NOT NULL REFERENCES office_documents(id) ON DELETE CASCADE,
  document_key text NOT NULL,
  status integer NOT NULL,
  event_key text NOT NULL,
  state text NOT NULL DEFAULT 'processing'
    CHECK (state IN ('processing', 'done', 'failed')),
  version_id text REFERENCES office_document_versions(id) ON DELETE SET NULL,
  error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (document_key, status, event_key)
);
CREATE INDEX IF NOT EXISTS office_callback_events_document_idx
  ON office_callback_events (document_id, created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.office_documents TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.office_document_versions TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.office_edit_sessions TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.office_callback_events TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0053_office_editor') ON CONFLICT DO NOTHING;

COMMIT;
