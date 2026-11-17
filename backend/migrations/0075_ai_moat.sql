-- Phase 4 AI/MCP moat: tenant AI budgets, triage, obligations ledger and
-- approval-gated MCP write actions.
BEGIN;

ALTER TABLE public.tenant_ai_settings
  ADD COLUMN IF NOT EXISTS monthly_budget_cents integer NOT NULL DEFAULT 0
    CHECK (monthly_budget_cents >= 0),
  ADD COLUMN IF NOT EXISTS triage_auto_label boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS triage_min_confidence numeric(4,3) NOT NULL DEFAULT 0.800
    CHECK (triage_min_confidence >= 0 AND triage_min_confidence <= 1),
  ADD COLUMN IF NOT EXISTS obligations_auto_task boolean NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS public.ai_usage (
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  month date NOT NULL,
  requests bigint NOT NULL DEFAULT 0,
  input_chars bigint NOT NULL DEFAULT 0,
  output_chars bigint NOT NULL DEFAULT 0,
  cost_micros bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, month)
);

CREATE TABLE IF NOT EXISTS public.ai_triage (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  message_id text NOT NULL REFERENCES public.mail_messages(id) ON DELETE CASCADE,
  category text NOT NULL CHECK (category IN ('action', 'fyi', 'newsletter', 'spam_like', 'needs_reply')),
  confidence numeric(4,3) NOT NULL DEFAULT 0,
  explanation text NOT NULL DEFAULT '',
  model text NOT NULL DEFAULT '',
  feedback text NOT NULL DEFAULT '' CHECK (feedback IN ('', 'correct', 'incorrect')),
  corrected_category text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, message_id)
);
CREATE INDEX IF NOT EXISTS ai_triage_user_idx ON public.ai_triage (user_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS public.ai_obligations (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  thread_id text NOT NULL,
  message_id text NOT NULL DEFAULT '',
  kind text NOT NULL DEFAULT 'promise' CHECK (kind IN ('promise', 'deadline', 'request')),
  text text NOT NULL,
  quote text NOT NULL DEFAULT '',
  due text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'completed', 'dismissed')),
  dedupe_key text NOT NULL,
  task_id text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, dedupe_key)
);
CREATE INDEX IF NOT EXISTS ai_obligations_user_idx ON public.ai_obligations (user_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS ai_obligations_thread_idx ON public.ai_obligations (user_id, thread_id);

CREATE TABLE IF NOT EXISTS public.mcp_action_requests (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  connection_id text NOT NULL REFERENCES public.mcp_connections(id) ON DELETE CASCADE,
  tool text NOT NULL,
  arguments jsonb NOT NULL DEFAULT '{}'::jsonb,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'denied', 'failed')),
  decision_note text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz
);
CREATE INDEX IF NOT EXISTS mcp_action_requests_user_idx
  ON public.mcp_action_requests (user_id, status, created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.ai_usage TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.ai_triage TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.ai_obligations TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.mcp_action_requests TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0075_ai_moat')
ON CONFLICT DO NOTHING;

COMMIT;
