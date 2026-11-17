BEGIN;
DROP TABLE IF EXISTS public.mcp_action_requests;
DROP TABLE IF EXISTS public.ai_obligations;
DROP TABLE IF EXISTS public.ai_triage;
DROP TABLE IF EXISTS public.ai_usage;
ALTER TABLE public.tenant_ai_settings
  DROP COLUMN IF EXISTS monthly_budget_cents,
  DROP COLUMN IF EXISTS triage_auto_label,
  DROP COLUMN IF EXISTS triage_min_confidence,
  DROP COLUMN IF EXISTS obligations_auto_task;
DELETE FROM schema_migrations WHERE version = '0075_ai_moat';
COMMIT;
