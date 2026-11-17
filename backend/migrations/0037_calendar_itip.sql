-- iTIP/iMIP meeting invitation state. Invitations keep a stable UID and use
-- SEQUENCE to distinguish updates and cancellations. Attendee response state is
-- tracked separately from the invitation sequence so stale replies can be
-- rejected after the organizer changes the event.
BEGIN;

ALTER TABLE calendar_events ADD COLUMN IF NOT EXISTS method_last_sent text NOT NULL DEFAULT '';
ALTER TABLE calendar_events ADD COLUMN IF NOT EXISTS invite_sent_at timestamptz;

ALTER TABLE calendar_attendees ADD COLUMN IF NOT EXISTS role text NOT NULL DEFAULT 'REQ-PARTICIPANT';
ALTER TABLE calendar_attendees ADD COLUMN IF NOT EXISTS rsvp boolean NOT NULL DEFAULT true;
ALTER TABLE calendar_attendees ADD COLUMN IF NOT EXISTS last_invite_sequence integer NOT NULL DEFAULT -1;
ALTER TABLE calendar_attendees ADD COLUMN IF NOT EXISTS last_invite_at timestamptz;
ALTER TABLE calendar_attendees ADD COLUMN IF NOT EXISTS last_response_sequence integer NOT NULL DEFAULT -1;
ALTER TABLE calendar_attendees ADD COLUMN IF NOT EXISTS last_response_at timestamptz;

CREATE INDEX IF NOT EXISTS calendar_attendees_status_idx
  ON calendar_attendees (event_id, status);

-- Every inbound iMIP REPLY is recorded before it is applied. The dedup key
-- prevents an IMAP rescan from applying the same reply twice while still
-- allowing a later reply at the same SEQUENCE to change the attendee's answer.
CREATE TABLE IF NOT EXISTS calendar_invite_replies (
  id bigserial PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  event_id text NOT NULL REFERENCES calendar_events(id) ON DELETE CASCADE,
  event_uid text NOT NULL DEFAULT '',
  attendee_email citext NOT NULL,
  sequence integer NOT NULL DEFAULT 0,
  partstat text NOT NULL DEFAULT 'NEEDS-ACTION',
  method text NOT NULL DEFAULT 'REPLY',
  message_id text NOT NULL DEFAULT '',
  dedup_key text NOT NULL,
  source text NOT NULL DEFAULT 'imip' CHECK (source IN ('imip', 'link', 'app')),
  applied boolean NOT NULL DEFAULT false,
  stale boolean NOT NULL DEFAULT false,
  detail text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (event_id, dedup_key)
);
CREATE INDEX IF NOT EXISTS calendar_invite_replies_event_idx
  ON calendar_invite_replies (event_id, created_at DESC);

-- Invitation delivery is held for the same short undo window as ordinary
-- mail. Cancelling the job prevents the messages from leaving the server.
CREATE TABLE IF NOT EXISTS calendar_invite_jobs (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  event_id text NOT NULL,
  organizer_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  method text NOT NULL DEFAULT 'REQUEST' CHECK (method IN ('REQUEST', 'CANCEL')),
  sequence integer NOT NULL DEFAULT 0,
  due_at timestamptz NOT NULL DEFAULT now(),
  status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'sending', 'sent', 'cancelled', 'skipped')),
  attempts integer NOT NULL DEFAULT 0,
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  sent_at timestamptz
);
CREATE INDEX IF NOT EXISTS calendar_invite_jobs_due_idx
  ON calendar_invite_jobs (status, due_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.calendar_invite_replies TO a8m_app;
GRANT USAGE, SELECT ON SEQUENCE public.calendar_invite_replies_id_seq TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.calendar_invite_jobs TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0037_calendar_itip')
ON CONFLICT (version) DO NOTHING;

COMMIT;
