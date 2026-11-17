-- Everyday-use features: newsletter unsubscribe metadata, reactions, reaction
-- rules, private/sent reaction delivery and recipient-time hints.
BEGIN;

ALTER TABLE public.mail_messages ADD COLUMN IF NOT EXISTS unsubscribe jsonb;
ALTER TABLE public.mail_messages ADD COLUMN IF NOT EXISTS sender_utc_offset_minutes integer;
CREATE INDEX IF NOT EXISTS mail_messages_unsubscribe_idx
  ON public.mail_messages (user_id, sent_at DESC)
  WHERE unsubscribe IS NOT NULL;

CREATE TABLE IF NOT EXISTS public.mail_reaction_rules (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  name text NOT NULL DEFAULT '',
  sender_contains text NOT NULL DEFAULT '',
  subject_contains text NOT NULL DEFAULT '',
  emoji text NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mail_reaction_rules_user_idx
  ON public.mail_reaction_rules (user_id, enabled);

CREATE TABLE IF NOT EXISTS public.mail_reactions (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  owner_user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  message_id text NOT NULL,
  thread_id text NOT NULL DEFAULT '',
  emoji text NOT NULL,
  semantic text NOT NULL DEFAULT '',
  visibility text NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'sent')),
  status text NOT NULL DEFAULT 'private' CHECK (status IN ('private', 'pending', 'sent', 'failed')),
  send_after timestamptz,
  sent_at timestamptz,
  dismissed_at timestamptz,
  error text NOT NULL DEFAULT '',
  rule_id text REFERENCES public.mail_reaction_rules(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, message_id)
);
CREATE INDEX IF NOT EXISTS mail_reactions_thread_idx
  ON public.mail_reactions (owner_user_id, thread_id, created_at);
CREATE INDEX IF NOT EXISTS mail_reactions_pending_idx
  ON public.mail_reactions (status, send_after)
  WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS public.mail_unsubscribe_actions (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  message_id text NOT NULL DEFAULT '',
  sender_email citext NOT NULL,
  method text NOT NULL DEFAULT '',
  target text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'requested' CHECK (status IN ('requested', 'sent', 'manual', 'failed')),
  error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, sender_email)
);
CREATE INDEX IF NOT EXISTS mail_unsubscribe_actions_user_idx
  ON public.mail_unsubscribe_actions (user_id, updated_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.mail_reaction_rules TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.mail_reactions TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.mail_unsubscribe_actions TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0034_phase3')
ON CONFLICT (version) DO NOTHING;

COMMIT;
