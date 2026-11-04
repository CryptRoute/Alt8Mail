-- Notes desk: many sticky notes per mailbox, one optional Inbox pin, archive
-- instead of destructive delete. The legacy single-note preference is migrated
-- lazily by the API so existing notes survive the upgrade.
BEGIN;

CREATE TABLE IF NOT EXISTS public.user_sticky_notes (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  owner_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_by uuid REFERENCES users(id) ON DELETE SET NULL,
  title text NOT NULL DEFAULT '',
  content text NOT NULL DEFAULT '',
  color text NOT NULL DEFAULT 'yellow' CHECK (color IN ('yellow', 'blue', 'pink', 'green')),
  collapsed boolean NOT NULL DEFAULT false,
  inbox_pinned boolean NOT NULL DEFAULT false,
  position jsonb NOT NULL DEFAULT '{}'::jsonb,
  links jsonb NOT NULL DEFAULT '[]'::jsonb,
  archived_at timestamptz,
  updated_by_id uuid REFERENCES users(id) ON DELETE SET NULL,
  updated_by_name text NOT NULL DEFAULT '',
  revision integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  search_vector tsvector GENERATED ALWAYS AS (
    to_tsvector('simple', coalesce(title, '') || ' ' || coalesce(content, ''))
  ) STORED
);
CREATE UNIQUE INDEX IF NOT EXISTS user_sticky_notes_one_pin_idx
  ON public.user_sticky_notes (owner_user_id)
  WHERE inbox_pinned = true AND archived_at IS NULL;
CREATE INDEX IF NOT EXISTS user_sticky_notes_owner_idx
  ON public.user_sticky_notes (owner_user_id, archived_at, updated_at DESC);
CREATE INDEX IF NOT EXISTS user_sticky_notes_search_idx
  ON public.user_sticky_notes USING GIN (search_vector);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.user_sticky_notes TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0040_notes_desk')
ON CONFLICT (version) DO NOTHING;

COMMIT;
