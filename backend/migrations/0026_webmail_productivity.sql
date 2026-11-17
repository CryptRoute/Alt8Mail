-- Webmail productivity state:
--   * reply/forward activity recorded on the source message so the list can
--     show Outlook-style direction indicators and the reader can show when the
--     action happened;
--   * undo-send outbox items carry the compose intent and source message;
--   * snooze and ignore state used by the folder and message action APIs;
--   * draft compose intent, so a restored reply/forward keeps its context.
BEGIN;

ALTER TABLE mail_messages ADD COLUMN IF NOT EXISTS replied_at timestamptz;
ALTER TABLE mail_messages ADD COLUMN IF NOT EXISTS forwarded_at timestamptz;
ALTER TABLE mail_messages ADD COLUMN IF NOT EXISTS reply_count integer NOT NULL DEFAULT 0;
ALTER TABLE mail_messages ADD COLUMN IF NOT EXISTS forward_count integer NOT NULL DEFAULT 0;
ALTER TABLE mail_messages ADD COLUMN IF NOT EXISTS snoozed_until timestamptz;
ALTER TABLE mail_messages ADD COLUMN IF NOT EXISTS ignored boolean NOT NULL DEFAULT false;
ALTER TABLE mail_messages ADD COLUMN IF NOT EXISTS ignored_at timestamptz;
ALTER TABLE mail_messages ADD COLUMN IF NOT EXISTS compose_kind text NOT NULL DEFAULT '';
ALTER TABLE mail_messages ADD COLUMN IF NOT EXISTS source_message_id text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS mail_messages_snoozed_idx
  ON mail_messages (user_id, snoozed_until)
  WHERE snoozed_until IS NOT NULL;
CREATE INDEX IF NOT EXISTS mail_messages_ignored_idx
  ON mail_messages (user_id, ignored)
  WHERE ignored;

ALTER TABLE outbox ADD COLUMN IF NOT EXISTS compose_kind text NOT NULL DEFAULT '';
ALTER TABLE outbox ADD COLUMN IF NOT EXISTS source_message_id text NOT NULL DEFAULT '';

-- Idempotent message actions let the optimistic webmail client retry a queued
-- delete after a lost response without applying the action twice.
CREATE TABLE IF NOT EXISTS mail_action_requests (
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  request_id text NOT NULL,
  response jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, request_id)
);
CREATE INDEX IF NOT EXISTS mail_action_requests_age_idx ON mail_action_requests (created_at);
GRANT SELECT, INSERT, UPDATE, DELETE ON mail_action_requests TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0026_webmail_productivity')
ON CONFLICT (version) DO NOTHING;

COMMIT;
