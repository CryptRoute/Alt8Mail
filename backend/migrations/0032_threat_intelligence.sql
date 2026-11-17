-- Confirmed threat indicators shared with Rspamd.
--
-- Hostnames are stored separately from the report so a reviewer can approve
-- or reject individual shared destinations, and so the complete Rspamd map can
-- be rebuilt from the remaining confirmed reports after a dismissal or undo.
BEGIN;

CREATE TABLE IF NOT EXISTS public.security_report_url_hosts (
  report_id text NOT NULL REFERENCES public.security_reports(id) ON DELETE CASCADE,
  host text NOT NULL,
  shared boolean NOT NULL DEFAULT false,
  approved boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (report_id, host)
);
CREATE INDEX IF NOT EXISTS security_report_url_hosts_approved_idx
  ON public.security_report_url_hosts (host) WHERE approved;

GRANT SELECT, INSERT, UPDATE, DELETE ON public.security_report_url_hosts TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.security_report_url_hosts TO alt8mail_control;

-- Off by default. Turning it on in System Control publishes confirmed
-- sender/domain/host indicators to the Redis multimaps consumed by Rspamd.
INSERT INTO control.settings (key, value)
VALUES ('security.rspamd_share', 'false'::jsonb)
ON CONFLICT (key) DO NOTHING;

INSERT INTO schema_migrations (version) VALUES ('0032_threat_intelligence')
ON CONFLICT (version) DO NOTHING;

COMMIT;
