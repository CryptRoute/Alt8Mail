-- Move transient undo snapshots out of app_states into the normalized
-- undo_actions table, then remove the per-user key. Snapshots can contain a
-- full message for a permanent-delete undo, so they must not live in the
-- per-user document even though they are short-lived.
BEGIN;

GRANT SELECT, INSERT, UPDATE, DELETE ON undo_actions TO meridian_app;

INSERT INTO undo_actions (token, tenant_id, user_id, action, snapshot, created_at, expires_at)
SELECT
  u->>'token', s.tenant_id, s.user_id, COALESCE(u->>'action','message'),
  COALESCE(u->'snapshot','[]'::jsonb),
  to_timestamp(COALESCE(NULLIF(u->>'createdAt','')::bigint, 0) / 1000.0),
  to_timestamp(COALESCE(NULLIF(u->>'expiresAt','')::bigint, 0) / 1000.0)
FROM app_states s
CROSS JOIN LATERAL jsonb_array_elements(
  CASE WHEN jsonb_typeof(s.state->'undo') = 'array' THEN s.state->'undo' ELSE '[]'::jsonb END
) AS u
WHERE COALESCE(u->>'token','') <> ''
  AND COALESCE(NULLIF(u->>'expiresAt','')::bigint, 0) > 0
ON CONFLICT (token) DO NOTHING;

UPDATE app_states
SET state = state - 'undo', updated_at = now();

INSERT INTO schema_migrations (version) VALUES ('0013_undo_table')
ON CONFLICT (version) DO NOTHING;

COMMIT;
