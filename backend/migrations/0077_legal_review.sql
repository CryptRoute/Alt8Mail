-- Phase 5 eDiscovery review queue.
BEGIN;

CREATE TABLE IF NOT EXISTS public.discovery_review_items (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  export_id text REFERENCES public.discovery_exports(id) ON DELETE SET NULL,
  item_type text NOT NULL CHECK (item_type IN ('mail', 'file', 'document')),
  item_id text NOT NULL,
  subject text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
  reviewer_id uuid REFERENCES public.users(id) ON DELETE SET NULL,
  reviewer_email citext NOT NULL DEFAULT '',
  note text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, item_type, item_id)
);
CREATE INDEX IF NOT EXISTS discovery_review_items_tenant_idx
  ON public.discovery_review_items (tenant_id, status, created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.discovery_review_items TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0077_legal_review')
ON CONFLICT DO NOTHING;

COMMIT;
