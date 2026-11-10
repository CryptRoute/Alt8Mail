BEGIN;
DROP TABLE IF EXISTS public.team_template_versions;
DROP TABLE IF EXISTS public.team_templates;
DELETE FROM schema_migrations WHERE version = '0066_team_templates';
COMMIT;
