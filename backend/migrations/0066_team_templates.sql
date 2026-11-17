-- Phase 2 team templates: shared canned responses with variables, categories,
-- optional sending alias, immutable version snapshots and team permissions.
BEGIN;

CREATE TABLE IF NOT EXISTS public.team_templates (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  team_mailbox_id uuid NOT NULL REFERENCES public.team_mailboxes(id) ON DELETE CASCADE,
  name text NOT NULL,
  category text NOT NULL DEFAULT '',
  alias_email citext NOT NULL DEFAULT '',
  subject text NOT NULL DEFAULT '',
  body_text text NOT NULL DEFAULT '',
  body_html text NOT NULL DEFAULT '',
  variables text[] NOT NULL DEFAULT '{}',
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  active boolean NOT NULL DEFAULT true,
  created_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  updated_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (team_mailbox_id, name)
);
CREATE INDEX IF NOT EXISTS team_templates_team_idx
  ON public.team_templates (team_mailbox_id, active, category, name);

CREATE TABLE IF NOT EXISTS public.team_template_versions (
  id bigserial PRIMARY KEY,
  template_id text NOT NULL REFERENCES public.team_templates(id) ON DELETE CASCADE,
  version integer NOT NULL,
  snapshot jsonb NOT NULL,
  author_user_id uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (template_id, version)
);
CREATE INDEX IF NOT EXISTS team_template_versions_template_idx
  ON public.team_template_versions (template_id, version DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.team_templates TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.team_template_versions TO a8m_app;
GRANT USAGE, SELECT ON SEQUENCE public.team_template_versions_id_seq TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0066_team_templates')
ON CONFLICT (version) DO NOTHING;

COMMIT;
