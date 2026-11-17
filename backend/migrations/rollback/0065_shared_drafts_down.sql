BEGIN;
DROP TABLE IF EXISTS public.shared_draft_comments;
DROP TABLE IF EXISTS public.shared_draft_versions;
DROP TABLE IF EXISTS public.shared_drafts;
DELETE FROM schema_migrations WHERE version = '0065_shared_drafts';
COMMIT;
