-- User-reported phishing, spam, malware and other abusive mail.
-- System Control reviews reports across every tenant. Pending and confirmed
-- reports are enforced by the Postfix policy daemon, so a reported sender is
-- blocked before delivery even while a human review is still pending.
BEGIN;

CREATE TABLE IF NOT EXISTS public.security_reports (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  user_email citext NOT NULL,
  message_id text NOT NULL,
  sender_email citext NOT NULL,
  sender_name text NOT NULL DEFAULT '',
  sender_domain text NOT NULL DEFAULT '',
  subject text NOT NULL DEFAULT '',
  snippet text NOT NULL DEFAULT '',
  reason text NOT NULL DEFAULT 'spam' CHECK (reason IN ('phishing', 'spam', 'unwanted', 'malware', 'other')),
  note text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'confirmed', 'dismissed')),
  block_scope text NOT NULL DEFAULT 'address' CHECK (block_scope IN ('address', 'domain', 'none')),
  notified_at timestamptz,
  reviewed_by text NOT NULL DEFAULT '',
  reviewed_at timestamptz,
  resolution text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, message_id)
);
CREATE INDEX IF NOT EXISTS security_reports_status_idx ON public.security_reports (status, created_at DESC);
CREATE INDEX IF NOT EXISTS security_reports_sender_idx ON public.security_reports (sender_email);
CREATE INDEX IF NOT EXISTS security_reports_domain_idx ON public.security_reports (sender_domain);

GRANT SELECT, INSERT, UPDATE ON public.security_reports TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.security_reports TO alt8mail_control;

INSERT INTO schema_migrations (version) VALUES ('0022_security_reports')
ON CONFLICT (version) DO NOTHING;

COMMIT;
