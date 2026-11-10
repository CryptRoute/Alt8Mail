BEGIN;
DROP TABLE IF EXISTS public.template_gallery;
DELETE FROM schema_migrations WHERE version = '0073_template_gallery';
COMMIT;
