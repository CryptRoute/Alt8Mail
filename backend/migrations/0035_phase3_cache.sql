-- AI reaction suggestions reuse the AI cache with their own kind.
BEGIN;

ALTER TABLE public.mail_ai_cache DROP CONSTRAINT IF EXISTS mail_ai_cache_kind_check;
ALTER TABLE public.mail_ai_cache ADD CONSTRAINT mail_ai_cache_kind_check
  CHECK (kind IN ('summary', 'translation', 'reactions'));

INSERT INTO schema_migrations (version) VALUES ('0035_phase3_cache')
ON CONFLICT (version) DO NOTHING;

COMMIT;
