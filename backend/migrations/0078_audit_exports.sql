-- Phase 5 signed audit export bundles.
BEGIN;

CREATE TABLE IF NOT EXISTS public.audit_exports (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  range_from timestamptz,
  range_to timestamptz,
  event_count integer NOT NULL DEFAULT 0,
  content_sha256 text NOT NULL DEFAULT '',
  signature text NOT NULL DEFAULT '',
  created_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS audit_exports_tenant_idx
  ON public.audit_exports (tenant_id, created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.audit_exports TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0078_audit_exports')
ON CONFLICT DO NOTHING;

COMMIT;
