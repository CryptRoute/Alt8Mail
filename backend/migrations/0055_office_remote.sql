-- Office Phase 5: remote deployment target and sealed SSH/agent credentials.
BEGIN;

ALTER TABLE public.office_settings
  ADD COLUMN IF NOT EXISTS remote_host text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS remote_port integer NOT NULL DEFAULT 22,
  ADD COLUMN IF NOT EXISTS remote_user text NOT NULL DEFAULT 'root',
  ADD COLUMN IF NOT EXISTS remote_auth text NOT NULL DEFAULT 'key',
  ADD COLUMN IF NOT EXISTS remote_secret text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS remote_passphrase text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS remote_host_key text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS remote_public_ip text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS remote_agent_token text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS remote_agent_path text NOT NULL DEFAULT '/opt/a8m-office/a8m-office-agent';

INSERT INTO schema_migrations (version) VALUES ('0055_office_remote') ON CONFLICT DO NOTHING;

COMMIT;
