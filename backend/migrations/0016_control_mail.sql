-- Alt8Mail System Control: domains, aliases, delivery, events, imports,
-- backups, cron, certificates and public API support.
BEGIN;

CREATE TABLE IF NOT EXISTS control.domain_settings (
  domain_id uuid PRIMARY KEY REFERENCES public.mail_domains(id) ON DELETE CASCADE,
  caps_enabled boolean NOT NULL DEFAULT false,
  cap_hour integer NOT NULL DEFAULT 0 CHECK (cap_hour BETWEEN 0 AND 1000000),
  cap_day integer NOT NULL DEFAULT 0 CHECK (cap_day BETWEEN 0 AND 5000000),
  cap_month integer NOT NULL DEFAULT 0 CHECK (cap_month BETWEEN 0 AND 50000000),
  burst integer NOT NULL DEFAULT 0 CHECK (burst BETWEEN 0 AND 100000),
  transport_name text NOT NULL DEFAULT '',
  notes text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Inbound alias/forwarding targets. user_aliases remains the webmail
-- sending-identity table; this table extends Postfix's virtual alias map with
-- external forwarders, catch-all rows and comma-separated distributions.
CREATE TABLE IF NOT EXISTS control.mail_aliases (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  domain citext NOT NULL,
  address citext NOT NULL UNIQUE,
  kind text NOT NULL DEFAULT 'forward'
    CHECK (kind IN ('forward', 'catch_all', 'distribution')),
  targets text[] NOT NULL DEFAULT '{}',
  description text NOT NULL DEFAULT '',
  active boolean NOT NULL DEFAULT true,
  created_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS control_mail_aliases_domain_idx ON control.mail_aliases (domain, active);

CREATE TABLE IF NOT EXISTS control.sieve_rules (
  user_id uuid PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
  rules jsonb NOT NULL DEFAULT '[]'::jsonb,
  revision integer NOT NULL DEFAULT 1,
  script_name text NOT NULL DEFAULT 'alt8mail-control',
  managed boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS control.mail_events (
  id bigserial PRIMARY KEY,
  event_at timestamptz NOT NULL DEFAULT now(),
  ingested_at timestamptz NOT NULL DEFAULT now(),
  source text NOT NULL DEFAULT 'mail.log',
  queue_id text NOT NULL DEFAULT '',
  message_id text NOT NULL DEFAULT '',
  sender citext NOT NULL DEFAULT '',
  recipient citext NOT NULL DEFAULT '',
  domain citext NOT NULL DEFAULT '',
  direction text NOT NULL DEFAULT 'outbound'
    CHECK (direction IN ('inbound', 'outbound', 'internal', 'unknown')),
  status text NOT NULL DEFAULT 'sent'
    CHECK (status IN ('sent', 'received', 'deferred', 'bounced', 'rejected', 'expired')),
  size_bytes bigint NOT NULL DEFAULT 0,
  delay_seconds integer NOT NULL DEFAULT 0,
  relay text NOT NULL DEFAULT '',
  subject text NOT NULL DEFAULT '',
  detail text NOT NULL DEFAULT '',
  raw text NOT NULL DEFAULT '',
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS control_mail_events_time_idx ON control.mail_events (event_at DESC);
CREATE INDEX IF NOT EXISTS control_mail_events_sender_idx ON control.mail_events (sender, event_at DESC);
CREATE INDEX IF NOT EXISTS control_mail_events_recipient_idx ON control.mail_events (recipient, event_at DESC);
CREATE INDEX IF NOT EXISTS control_mail_events_domain_idx ON control.mail_events (domain, event_at DESC);
CREATE INDEX IF NOT EXISTS control_mail_events_status_idx ON control.mail_events (status, event_at DESC);
CREATE INDEX IF NOT EXISTS control_mail_events_queue_idx ON control.mail_events (queue_id);

CREATE TABLE IF NOT EXISTS control.outbound_attempts (
  id bigserial PRIMARY KEY,
  attempted_at timestamptz NOT NULL DEFAULT now(),
  queue_id text NOT NULL DEFAULT '',
  sender citext NOT NULL DEFAULT '',
  sender_domain citext NOT NULL DEFAULT '',
  recipient citext NOT NULL DEFAULT '',
  size_bytes bigint NOT NULL DEFAULT 0,
  sasl_user text NOT NULL DEFAULT '',
  client_address text NOT NULL DEFAULT '',
  action text NOT NULL DEFAULT 'permit',
  reason text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS control_outbound_attempts_domain_time_idx
  ON control.outbound_attempts (sender_domain, attempted_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS control_outbound_attempts_unique_idx
  ON control.outbound_attempts (queue_id, recipient) WHERE queue_id <> '' AND action = 'permit';

CREATE TABLE IF NOT EXISTS control.smtp_tests (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  actor_id uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  recipient citext NOT NULL,
  subject text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'running',
  transcript text NOT NULL DEFAULT '',
  error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);
CREATE INDEX IF NOT EXISTS control_smtp_tests_time_idx ON control.smtp_tests (created_at DESC);

CREATE TABLE IF NOT EXISTS control.relay_settings (
  id boolean PRIMARY KEY DEFAULT true CHECK (id),
  enabled boolean NOT NULL DEFAULT false,
  host text NOT NULL DEFAULT '',
  port integer NOT NULL DEFAULT 587 CHECK (port BETWEEN 1 AND 65535),
  username text NOT NULL DEFAULT '',
  password_sealed text NOT NULL DEFAULT '',
  security text NOT NULL DEFAULT 'starttls'
    CHECK (security IN ('starttls', 'tls', 'none')),
  last_test_at timestamptz,
  last_test_status text NOT NULL DEFAULT '',
  last_test_detail text NOT NULL DEFAULT '',
  updated_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO control.relay_settings (id) VALUES (true) ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS control.outbound_ips (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  address inet NOT NULL UNIQUE,
  label text NOT NULL DEFAULT '',
  mode text NOT NULL DEFAULT 'primary' CHECK (mode IN ('primary', 'backup', 'disabled')),
  priority integer NOT NULL DEFAULT 100,
  interface_name text NOT NULL DEFAULT '',
  helo_name text NOT NULL DEFAULT '',
  active boolean NOT NULL DEFAULT true,
  notes text NOT NULL DEFAULT '',
  discovered_at timestamptz NOT NULL DEFAULT now(),
  last_checked_at timestamptz,
  last_listed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS control_outbound_ips_priority_idx ON control.outbound_ips (active, priority);

CREATE TABLE IF NOT EXISTS control.sender_routes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  sender_domain citext NOT NULL,
  source_ip_id uuid REFERENCES control.outbound_ips(id) ON DELETE SET NULL,
  helo_name text NOT NULL DEFAULT '',
  transport_name text NOT NULL DEFAULT '',
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS control_sender_routes_domain_idx ON control.sender_routes (lower(sender_domain::text));

CREATE TABLE IF NOT EXISTS control.suppressions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  direction text NOT NULL CHECK (direction IN ('outbound', 'inbound')),
  pattern citext NOT NULL,
  kind text NOT NULL DEFAULT 'address' CHECK (kind IN ('address', 'domain', 'catch_all')),
  reason text NOT NULL DEFAULT '',
  expires_at timestamptz,
  created_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS control_suppressions_unique_idx
  ON control.suppressions (direction, lower(pattern::text));

CREATE TABLE IF NOT EXISTS control.blocklist_results (
  id bigserial PRIMARY KEY,
  ip inet NOT NULL,
  blocklist text NOT NULL,
  listed boolean NOT NULL DEFAULT false,
  response text NOT NULL DEFAULT '',
  checked_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS control_blocklist_results_time_idx ON control.blocklist_results (checked_at DESC);
CREATE INDEX IF NOT EXISTS control_blocklist_results_ip_idx ON control.blocklist_results (ip, checked_at DESC);

CREATE TABLE IF NOT EXISTS control.service_state (
  unit text PRIMARY KEY,
  active boolean NOT NULL DEFAULT false,
  previous_active boolean,
  state text NOT NULL DEFAULT '',
  sub_state text NOT NULL DEFAULT '',
  enabled_state text NOT NULL DEFAULT '',
  last_checked_at timestamptz NOT NULL DEFAULT now(),
  last_change_at timestamptz NOT NULL DEFAULT now(),
  detail text NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS control.config_sync_runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  trigger text NOT NULL DEFAULT 'manual',
  status text NOT NULL DEFAULT 'running',
  started_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  files_changed integer NOT NULL DEFAULT 0,
  warnings jsonb NOT NULL DEFAULT '[]'::jsonb,
  drift jsonb NOT NULL DEFAULT '[]'::jsonb,
  details jsonb NOT NULL DEFAULT '{}'::jsonb,
  actor_id uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  error text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS control_config_sync_time_idx ON control.config_sync_runs (started_at DESC);

CREATE TABLE IF NOT EXISTS control.import_jobs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind text NOT NULL CHECK (kind IN ('imap', 'mbox', 'maildir')),
  target_mailbox citext NOT NULL,
  source jsonb NOT NULL DEFAULT '{}'::jsonb,
  secret_sealed text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'queued'
    CHECK (status IN ('queued', 'running', 'stopped', 'completed', 'failed')),
  progress integer NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
  messages_imported integer NOT NULL DEFAULT 0,
  log text NOT NULL DEFAULT '',
  error text NOT NULL DEFAULT '',
  created_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  finished_at timestamptz
);
CREATE INDEX IF NOT EXISTS control_import_jobs_time_idx ON control.import_jobs (created_at DESC);
CREATE INDEX IF NOT EXISTS control_import_jobs_queue_idx ON control.import_jobs (status, created_at);

CREATE TABLE IF NOT EXISTS control.backups (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  path text NOT NULL,
  size_bytes bigint NOT NULL DEFAULT 0,
  contents text[] NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'running',
  detail text NOT NULL DEFAULT '',
  created_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);
CREATE INDEX IF NOT EXISTS control_backups_time_idx ON control.backups (created_at DESC);

CREATE TABLE IF NOT EXISTS control.cron_jobs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  schedule text NOT NULL,
  command text NOT NULL,
  run_as text NOT NULL DEFAULT 'root',
  stdin text NOT NULL DEFAULT '',
  start_date date,
  end_date date,
  enabled boolean NOT NULL DEFAULT true,
  created_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  last_run_at timestamptz,
  last_status text NOT NULL DEFAULT '',
  last_duration_ms bigint NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS control.cron_runs (
  id bigserial PRIMARY KEY,
  job_id uuid REFERENCES control.cron_jobs(id) ON DELETE CASCADE,
  status text NOT NULL DEFAULT 'running',
  output text NOT NULL DEFAULT '',
  started_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  duration_ms bigint NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS control_cron_runs_job_idx ON control.cron_runs (job_id, started_at DESC);

CREATE TABLE IF NOT EXISTS control.message_templates (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  slug text NOT NULL UNIQUE,
  subject text NOT NULL DEFAULT '',
  body_text text NOT NULL DEFAULT '',
  body_html text NOT NULL DEFAULT '',
  created_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS control.api_send_history (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  api_key_id uuid REFERENCES control.api_keys(id) ON DELETE SET NULL,
  message_id text NOT NULL DEFAULT '',
  sender citext NOT NULL DEFAULT '',
  recipient citext NOT NULL DEFAULT '',
  subject text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'queued',
  detail text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS control_api_send_history_time_idx ON control.api_send_history (created_at DESC);

CREATE TABLE IF NOT EXISTS control.network_addresses (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  interface_name text NOT NULL,
  address_cidr text NOT NULL,
  label text NOT NULL DEFAULT '',
  active boolean NOT NULL DEFAULT true,
  created_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS control.database_migrations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  target_database text NOT NULL,
  status text NOT NULL DEFAULT 'queued',
  progress integer NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
  log text NOT NULL DEFAULT '',
  created_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  finished_at timestamptz
);

CREATE TABLE IF NOT EXISTS control.ssl_certificates (
  name text PRIMARY KEY,
  cert_path text NOT NULL,
  key_path text NOT NULL DEFAULT '',
  subject text NOT NULL DEFAULT '',
  issuer text NOT NULL DEFAULT '',
  sans text[] NOT NULL DEFAULT '{}',
  not_before timestamptz,
  not_after timestamptz,
  self_signed boolean NOT NULL DEFAULT false,
  last_error text NOT NULL DEFAULT '',
  checked_at timestamptz NOT NULL DEFAULT now()
);

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA control TO alt8mail_control;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA control TO alt8mail_control;

-- One Postfix alias lookup view keeps webmail identities, groups and the
-- control panel's external forwarders/catch-alls/distribution lists in sync.
CREATE OR REPLACE VIEW public.postfix_alias_targets AS
SELECT lower(a.email::text) AS address, u.email::text AS target
FROM public.user_aliases a
JOIN public.users u ON u.id = a.user_id
JOIN public.tenants t ON t.id = u.tenant_id
WHERE a.active AND u.status = 'active' AND u.deleted_at IS NULL AND t.status = 'active'
UNION ALL
SELECT lower(g.email::text) AS address, string_agg(u.email::text, ',') AS target
FROM public.groups g
JOIN public.group_members gm ON gm.group_id = g.id
JOIN public.users u ON u.id = gm.user_id
JOIN public.tenants t ON t.id = g.tenant_id
WHERE g.status = 'active' AND u.status = 'active' AND u.deleted_at IS NULL AND t.status = 'active'
GROUP BY g.id, g.email
UNION ALL
SELECT lower(m.address::text) AS address, string_agg(target, ',') AS target
FROM control.mail_aliases m
CROSS JOIN LATERAL unnest(m.targets) AS target
WHERE m.active
GROUP BY m.id, m.address;

CREATE OR REPLACE FUNCTION public.postfix_alias_lookup(recipient text)
RETURNS text
LANGUAGE sql
STABLE
AS $$
  SELECT target
  FROM (
    SELECT target, 0 AS priority
    FROM public.postfix_alias_targets
    WHERE address = lower(recipient)
    UNION ALL
    SELECT target, 1 AS priority
    FROM public.postfix_alias_targets
    WHERE address = '@' || lower(split_part(recipient, '@', 2))
  ) expanded
  WHERE COALESCE(target, '') <> ''
  ORDER BY priority
  LIMIT 1
$$;

REVOKE ALL ON FUNCTION public.postfix_alias_lookup(text) FROM PUBLIC;
GRANT SELECT ON public.postfix_alias_targets TO meridian_mail;
GRANT EXECUTE ON FUNCTION public.postfix_alias_lookup(text) TO meridian_mail;

-- The panel manages the shared mail tables. Existing webmail/admin services
-- remain the source of truth for their own tables.
GRANT SELECT, INSERT, UPDATE, DELETE ON
  public.mail_domains, public.users, public.user_aliases, public.mailboxes,
  public.sieve_profiles, public.mail_identities
TO alt8mail_control;
GRANT SELECT ON public.tenants, public.group_members,
  public.mail_messages, public.outbound_events, public.tenant_ai_settings,
  public.trusted_senders
TO alt8mail_control;
-- Domain rename rewrites group addresses; domain deletion removes groups that
-- belong to the deleted domain. Neither operation creates or edits membership.
GRANT SELECT, UPDATE, DELETE ON public.groups TO alt8mail_control;
GRANT SELECT, INSERT ON public.password_history TO alt8mail_control;
GRANT SELECT, INSERT, UPDATE ON public.app_states TO alt8mail_control;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO alt8mail_control;

-- Postfix/Dovecot read aliases, domains and mailboxes through meridian_mail.
GRANT USAGE ON SCHEMA control TO meridian_mail;
GRANT SELECT ON control.mail_aliases TO meridian_mail;

INSERT INTO schema_migrations (version) VALUES ('0016_control_mail')
ON CONFLICT (version) DO NOTHING;

COMMIT;
