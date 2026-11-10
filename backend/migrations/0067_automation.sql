-- Phase 2 visual automation: rule definitions, compiled Sieve preview,
-- idempotent execution records and dry-run support.
BEGIN;

CREATE TABLE IF NOT EXISTS public.automation_rules (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  team_mailbox_id uuid NOT NULL REFERENCES public.team_mailboxes(id) ON DELETE CASCADE,
  name text NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  priority integer NOT NULL DEFAULT 100 CHECK (priority BETWEEN 0 AND 10000),
  conditions jsonb NOT NULL DEFAULT '{}'::jsonb,
  actions jsonb NOT NULL DEFAULT '[]'::jsonb,
  sieve_text text NOT NULL DEFAULT '',
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  updated_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (team_mailbox_id, name)
);
CREATE INDEX IF NOT EXISTS automation_rules_team_idx
  ON public.automation_rules (team_mailbox_id, enabled, priority);

CREATE TABLE IF NOT EXISTS public.automation_runs (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  team_mailbox_id uuid NOT NULL REFERENCES public.team_mailboxes(id) ON DELETE CASCADE,
  rule_id text NOT NULL REFERENCES public.automation_rules(id) ON DELETE CASCADE,
  thread_id text NOT NULL DEFAULT '',
  message_id text NOT NULL,
  idempotency_key text NOT NULL UNIQUE,
  status text NOT NULL DEFAULT 'running' CHECK (status IN ('running','completed','failed','skipped')),
  dry_run boolean NOT NULL DEFAULT false,
  attempts integer NOT NULL DEFAULT 0,
  result jsonb NOT NULL DEFAULT '{}'::jsonb,
  error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);
CREATE INDEX IF NOT EXISTS automation_runs_rule_idx
  ON public.automation_runs (rule_id, created_at DESC);
CREATE INDEX IF NOT EXISTS automation_runs_team_idx
  ON public.automation_runs (team_mailbox_id, created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.automation_rules TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.automation_runs TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0067_automation')
ON CONFLICT (version) DO NOTHING;

COMMIT;
