-- Web Net edge routing, webmail browser push, and enriched mail-log sources.
BEGIN;

-- ---------------------------------------------------------------------------
-- Web Net: operator-managed webmail/admin hostnames and edge policy.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS control.webnet_settings (
  id boolean PRIMARY KEY DEFAULT true CHECK (id),
  mail_host_mode text NOT NULL DEFAULT 'default_page'
    CHECK (mail_host_mode IN ('default_page', 'drop')),
  webmail_maintenance boolean NOT NULL DEFAULT false,
  admin_maintenance boolean NOT NULL DEFAULT false,
  maintenance_title text NOT NULL DEFAULT 'Alt8Mail is temporarily unavailable',
  maintenance_message text NOT NULL DEFAULT 'We are performing planned maintenance. Please try again shortly.',
  blocked_title text NOT NULL DEFAULT 'This address is not a webmail host',
  blocked_message text NOT NULL DEFAULT 'Use the webmail or administration address provided by your organization.',
  webmail_allow_cidrs text[] NOT NULL DEFAULT '{}',
  webmail_deny_cidrs text[] NOT NULL DEFAULT '{}',
  admin_allow_cidrs text[] NOT NULL DEFAULT '{}',
  admin_deny_cidrs text[] NOT NULL DEFAULT '{}',
  applied_revision integer NOT NULL DEFAULT 0,
  applied_at timestamptz,
  last_error text NOT NULL DEFAULT '',
  updated_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO control.webnet_settings (id) VALUES (true) ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS control.webnet_routes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  service text NOT NULL CHECK (service IN ('webmail', 'admin')),
  hostname citext NOT NULL UNIQUE,
  certificate_name text NOT NULL DEFAULT '',
  ip_mode text NOT NULL DEFAULT 'shared' CHECK (ip_mode IN ('shared', 'dedicated')),
  ip_address inet,
  primary_route boolean NOT NULL DEFAULT false,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS control_webnet_primary_service_idx
  ON control.webnet_routes (service) WHERE primary_route AND enabled;
CREATE INDEX IF NOT EXISTS control_webnet_service_idx ON control.webnet_routes (service, hostname);

GRANT SELECT, INSERT, UPDATE, DELETE ON control.webnet_settings, control.webnet_routes TO alt8mail_control;

-- ---------------------------------------------------------------------------
-- Browser push settings are shared with the webmail service through the
-- public schema. The VAPID private key is kept in /etc/a8m and is never stored
-- in this table or returned by the API.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.web_push_settings (
  id boolean PRIMARY KEY DEFAULT true CHECK (id),
  enabled boolean NOT NULL DEFAULT false,
  public_key text NOT NULL DEFAULT '',
  subject text NOT NULL DEFAULT '',
  include_sender boolean NOT NULL DEFAULT true,
  include_subject boolean NOT NULL DEFAULT true,
  quiet_hours_enabled boolean NOT NULL DEFAULT false,
  quiet_start integer NOT NULL DEFAULT 22 CHECK (quiet_start BETWEEN 0 AND 23),
  quiet_end integer NOT NULL DEFAULT 7 CHECK (quiet_end BETWEEN 0 AND 23),
  cursor_at timestamptz,
  cursor_id text NOT NULL DEFAULT '',
  updated_by text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO public.web_push_settings (id) VALUES (true) ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS public.web_push_subscriptions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  endpoint text NOT NULL UNIQUE,
  p256dh text NOT NULL,
  auth text NOT NULL,
  user_agent text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  last_success_at timestamptz,
  last_error text NOT NULL DEFAULT '',
  failure_count integer NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS web_push_subscriptions_user_idx
  ON public.web_push_subscriptions (user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS mail_messages_push_cursor_idx
  ON public.mail_messages (received_at, id)
  WHERE folder = 'inbox' AND NOT draft AND NOT ignored AND unread;

GRANT SELECT, INSERT, UPDATE, DELETE ON public.web_push_settings, public.web_push_subscriptions TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.web_push_settings, public.web_push_subscriptions TO alt8mail_control;

-- ---------------------------------------------------------------------------
-- Enriched source for a mail event. The Postfix log line does not contain the
-- subject or message body; the ingestor finds the delivered message in the
-- local Maildir (or Postfix queue) and caches a bounded raw copy here.
-- ---------------------------------------------------------------------------
ALTER TABLE control.mail_events ADD COLUMN IF NOT EXISTS raw_message text NOT NULL DEFAULT '';
ALTER TABLE control.mail_events ADD COLUMN IF NOT EXISTS raw_message_truncated boolean NOT NULL DEFAULT false;
ALTER TABLE control.mail_events ADD COLUMN IF NOT EXISTS message_source text NOT NULL DEFAULT '';
ALTER TABLE control.mail_events ADD COLUMN IF NOT EXISTS message_enriched_at timestamptz;
CREATE INDEX IF NOT EXISTS control_mail_events_unenriched_idx
  ON control.mail_events (event_at DESC) WHERE raw_message = '';

INSERT INTO schema_migrations (version) VALUES ('0031_webnet_push_maillogs')
ON CONFLICT (version) DO NOTHING;

COMMIT;
