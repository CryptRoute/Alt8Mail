-- User-defined message labels. The built-in decision/product/finance labels
-- remain client defaults; this table stores the labels a user creates from the
-- message menu or Settings → Mail.
BEGIN;

CREATE TABLE IF NOT EXISTS mail_labels (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  color text NOT NULL DEFAULT '#0f6cbd',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS mail_labels_user_name_idx
  ON mail_labels (user_id, lower(name));
CREATE INDEX IF NOT EXISTS mail_labels_user_idx
  ON mail_labels (user_id, created_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON mail_labels TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0028_mail_labels')
ON CONFLICT (version) DO NOTHING;

COMMIT;
