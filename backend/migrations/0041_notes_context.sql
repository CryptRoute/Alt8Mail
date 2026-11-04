-- Notes desk context and discovery: typed links, archive search and shared
-- mailbox team lookups. Existing links only carried threadId, so backfill the
-- canonical type/id fields before the API starts returning typed attachments.
BEGIN;

UPDATE public.user_sticky_notes
SET links = COALESCE((
  SELECT jsonb_agg(
    CASE
      WHEN jsonb_typeof(item) = 'object' THEN item || jsonb_build_object(
        'type', COALESCE(NULLIF(item->>'type', ''), 'thread'),
        'id', COALESCE(NULLIF(item->>'id', ''), item->>'threadId')
      )
      WHEN jsonb_typeof(item) = 'string' THEN jsonb_build_object(
        'type', 'thread', 'id', item #>> '{}', 'threadId', item #>> '{}'
      )
      ELSE item
    END
  )
  FROM jsonb_array_elements(links) AS item
), '[]'::jsonb)
WHERE jsonb_typeof(links) = 'array' AND links <> '[]'::jsonb;

CREATE INDEX IF NOT EXISTS user_sticky_notes_owner_idx
  ON public.user_sticky_notes (owner_user_id, archived_at, updated_at DESC);

CREATE INDEX IF NOT EXISTS user_sticky_notes_links_idx
  ON public.user_sticky_notes USING GIN (links jsonb_path_ops);

INSERT INTO schema_migrations (version) VALUES ('0041_notes_context') ON CONFLICT DO NOTHING;

COMMIT;
