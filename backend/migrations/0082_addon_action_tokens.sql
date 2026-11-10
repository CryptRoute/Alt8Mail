-- Interactive add-on actions: single-use tokens embedded in Slack Block Kit
-- buttons and Microsoft Teams Adaptive Card links.
BEGIN;

CREATE TABLE IF NOT EXISTS public.addon_action_tokens (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  addon_id text NOT NULL,
  connection_id text NOT NULL DEFAULT '',
  message_id text NOT NULL,
  action text NOT NULL CHECK (action IN ('mark_read', 'archive', 'create_task')),
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS addon_action_tokens_tenant_idx
  ON public.addon_action_tokens (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS addon_action_tokens_expiry_idx
  ON public.addon_action_tokens (expires_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.addon_action_tokens TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0082_addon_action_tokens')
ON CONFLICT DO NOTHING;

COMMIT;
