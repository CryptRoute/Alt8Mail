BEGIN;

DROP TABLE IF EXISTS public.addon_action_tokens;
DELETE FROM schema_migrations WHERE version = '0082_addon_action_tokens';

COMMIT;
