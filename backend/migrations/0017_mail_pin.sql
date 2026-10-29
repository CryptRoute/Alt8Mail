-- Per-message pin state for the webmail. Pinning is a user preference stored
-- alongside stars/flags; it is deliberately not exported to IMAP flags.
BEGIN;

ALTER TABLE mail_messages ADD COLUMN IF NOT EXISTS pinned boolean NOT NULL DEFAULT false;

CREATE INDEX IF NOT EXISTS mail_messages_user_pinned_idx
  ON mail_messages (user_id, pinned) WHERE pinned;

INSERT INTO schema_migrations (version) VALUES ('0017_mail_pin')
ON CONFLICT (version) DO NOTHING;

COMMIT;
