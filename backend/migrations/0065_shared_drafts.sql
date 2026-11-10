-- Phase 2 shared drafts: optimistic versioning, a single-writer lease,
-- immutable version history and inline comments for team inbox agents.
BEGIN;

CREATE TABLE IF NOT EXISTS public.shared_drafts (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  team_mailbox_id uuid NOT NULL REFERENCES public.team_mailboxes(id) ON DELETE CASCADE,
  thread_id text NOT NULL DEFAULT '',
  subject text NOT NULL DEFAULT '',
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  updated_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  lease_owner uuid REFERENCES public.users(id) ON DELETE SET NULL,
  lease_token text NOT NULL DEFAULT '',
  lease_expires_at timestamptz,
  deleted_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS shared_drafts_team_idx
  ON public.shared_drafts (team_mailbox_id, updated_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS shared_drafts_thread_idx
  ON public.shared_drafts (team_mailbox_id, thread_id) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS public.shared_draft_versions (
  id bigserial PRIMARY KEY,
  shared_draft_id text NOT NULL REFERENCES public.shared_drafts(id) ON DELETE CASCADE,
  version bigint NOT NULL,
  payload jsonb NOT NULL,
  author_user_id uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (shared_draft_id, version)
);
CREATE INDEX IF NOT EXISTS shared_draft_versions_draft_idx
  ON public.shared_draft_versions (shared_draft_id, version DESC);

CREATE TABLE IF NOT EXISTS public.shared_draft_comments (
  id text PRIMARY KEY,
  shared_draft_id text NOT NULL REFERENCES public.shared_drafts(id) ON DELETE CASCADE,
  author_user_id uuid REFERENCES public.users(id) ON DELETE SET NULL,
  body text NOT NULL,
  anchor jsonb NOT NULL DEFAULT '{}'::jsonb,
  resolved_at timestamptz,
  resolved_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS shared_draft_comments_draft_idx
  ON public.shared_draft_comments (shared_draft_id, created_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.shared_drafts TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.shared_draft_versions TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.shared_draft_comments TO a8m_app;
GRANT USAGE, SELECT ON SEQUENCE public.shared_draft_versions_id_seq TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0065_shared_drafts')
ON CONFLICT (version) DO NOTHING;

COMMIT;
