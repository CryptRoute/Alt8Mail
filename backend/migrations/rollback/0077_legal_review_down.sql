BEGIN;
DROP TABLE IF EXISTS public.discovery_review_items;
DELETE FROM schema_migrations WHERE version = '0077_legal_review';
COMMIT;
