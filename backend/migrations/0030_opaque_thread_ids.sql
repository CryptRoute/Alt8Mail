-- Thread identifiers are part of the webmail URL, so they must not reveal how
-- a conversation was created. Replace the legacy t_<random> and
-- t_imap_<hash> values with the same opaque 22-character base64url format
-- used by newThreadID() and opaqueThreadID() in thread_ids.go.
--
-- This mapping must stay in sync with opaqueThreadID():
--   base64url(sha256(old_id) first 16 bytes, no padding)
-- Already-opaque 22-character IDs are left alone, so the migration is safe to
-- replay by hand.
BEGIN;

CREATE TEMP TABLE _thread_id_map ON COMMIT DROP AS
WITH sources AS (
  SELECT thread_id AS old_id
  FROM mail_messages
  WHERE thread_id <> ''
  UNION
  SELECT thread_id
  FROM outbox
  WHERE thread_id <> ''
  UNION
  SELECT linked_thread_id
  FROM tasks
  WHERE linked_thread_id IS NOT NULL AND linked_thread_id <> ''
  UNION
  SELECT item.value #>> '{}'
  FROM app_states s
  CROSS JOIN LATERAL jsonb_array_elements(
    CASE
      WHEN jsonb_typeof(s.state->'user'->'ignoredThreads') = 'array'
        THEN s.state->'user'->'ignoredThreads'
      ELSE '[]'::jsonb
    END
  ) AS item(value)
  WHERE jsonb_typeof(item.value) = 'string'
  UNION
  SELECT entry.value->'full'->>'threadId'
  FROM undo_actions ua
  CROSS JOIN LATERAL jsonb_array_elements(
    CASE
      WHEN jsonb_typeof(ua.snapshot) = 'array' THEN ua.snapshot
      ELSE '[]'::jsonb
    END
  ) AS entry(value)
  WHERE entry.value->'full' ? 'threadId'
)
SELECT old_id,
       rtrim(
         translate(
           encode(substring(digest(old_id, 'sha256') from 1 for 16), 'base64'),
           '+/',
           '-_'
         ),
         '='
       ) AS new_id
FROM sources
WHERE old_id <> ''
  AND NOT (length(old_id) = 22 AND old_id ~ '^[A-Za-z0-9_-]{22}$');

CREATE INDEX ON _thread_id_map (old_id);

UPDATE mail_messages message
SET thread_id = map.new_id
FROM _thread_id_map map
WHERE message.thread_id = map.old_id;

UPDATE outbox item
SET thread_id = map.new_id
FROM _thread_id_map map
WHERE item.thread_id = map.old_id;

UPDATE tasks task
SET linked_thread_id = map.new_id
FROM _thread_id_map map
WHERE task.linked_thread_id = map.old_id;

UPDATE app_states state_row
SET state = jsonb_set(
  state_row.state,
  '{user,ignoredThreads}',
  COALESCE((
    SELECT jsonb_agg(COALESCE(map.new_id, item.value #>> '{}') ORDER BY item.position)
    FROM jsonb_array_elements(state_row.state->'user'->'ignoredThreads')
      WITH ORDINALITY AS item(value, position)
    LEFT JOIN _thread_id_map map ON map.old_id = item.value #>> '{}'
  ), '[]'::jsonb),
  false
)
WHERE jsonb_typeof(state_row.state->'user'->'ignoredThreads') = 'array'
  AND EXISTS (
    SELECT 1
    FROM jsonb_array_elements_text(state_row.state->'user'->'ignoredThreads') AS ignored(value)
    JOIN _thread_id_map map ON map.old_id = ignored.value
  );

-- Undo snapshots contain a full message for permanent deletes. They are
-- short-lived, but rewrite the copy so a restore cannot recreate a legacy ID.
UPDATE undo_actions action
SET snapshot = COALESCE((
  SELECT jsonb_agg(
    CASE
      WHEN entry.value ? 'full' AND entry.value->'full' ? 'threadId'
        THEN jsonb_set(
          entry.value,
          '{full,threadId}',
          to_jsonb(COALESCE(map.new_id, entry.value->'full'->>'threadId'))
        )
      ELSE entry.value
    END
    ORDER BY entry.position
  )
  FROM jsonb_array_elements(action.snapshot) WITH ORDINALITY AS entry(value, position)
  LEFT JOIN _thread_id_map map ON map.old_id = entry.value->'full'->>'threadId'
), action.snapshot)
WHERE jsonb_typeof(action.snapshot) = 'array'
  AND EXISTS (
    SELECT 1
    FROM jsonb_array_elements(action.snapshot) AS entry(value)
    JOIN _thread_id_map map ON map.old_id = entry.value->'full'->>'threadId'
    WHERE entry.value->'full' ? 'threadId'
  );

INSERT INTO schema_migrations (version) VALUES ('0030_opaque_thread_ids')
ON CONFLICT (version) DO NOTHING;

COMMIT;
