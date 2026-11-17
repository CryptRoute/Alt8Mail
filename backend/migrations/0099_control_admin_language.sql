-- System Control operators can choose their own interface language. An empty
-- value means "follow the system default" set under Languages.
BEGIN;

ALTER TABLE control.admin_users
  ADD COLUMN IF NOT EXISTS language text NOT NULL DEFAULT '';

INSERT INTO schema_migrations (version) VALUES ('0099_control_admin_language')
ON CONFLICT (version) DO NOTHING;

COMMIT;
