BEGIN;
DROP TABLE IF EXISTS public.follow_ups;
DELETE FROM schema_migrations WHERE version = '0070_follow_ups';
COMMIT;
