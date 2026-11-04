-- Calendar invitation polish: delegated senders, recurrence exceptions and
-- attendee-proposed times (iTIP COUNTER).
BEGIN;

-- A delegate may send on behalf of the organizer. The calendar ORGANIZER stays
-- stable, while SENDER identifies the mailbox that submitted this version.
ALTER TABLE calendar_events ADD COLUMN IF NOT EXISTS sender_email citext NOT NULL DEFAULT '';
ALTER TABLE calendar_events ADD COLUMN IF NOT EXISTS sender_name text NOT NULL DEFAULT '';

-- One override row per modified occurrence. RECURRENCE-ID is the original
-- occurrence start (UTC); the override fields describe the replacement.
CREATE TABLE IF NOT EXISTS calendar_event_exceptions (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  event_id text NOT NULL REFERENCES calendar_events(id) ON DELETE CASCADE,
  recurrence_id timestamptz NOT NULL,
  title text NOT NULL DEFAULT '',
  description text NOT NULL DEFAULT '',
  location text NOT NULL DEFAULT '',
  starts_at timestamptz NOT NULL,
  ends_at timestamptz NOT NULL,
  all_day boolean NOT NULL DEFAULT false,
  timezone text NOT NULL DEFAULT 'UTC',
  status text NOT NULL DEFAULT 'confirmed' CHECK (status IN ('confirmed', 'tentative', 'cancelled')),
  sequence integer NOT NULL DEFAULT 0,
  reminder_minutes integer NOT NULL DEFAULT 15,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (event_id, recurrence_id)
);
CREATE INDEX IF NOT EXISTS calendar_event_exceptions_event_idx
  ON calendar_event_exceptions (event_id, recurrence_id);

-- A guest can propose another time from a signed RSVP page, webmail, or an
-- external calendar client sending METHOD:COUNTER.
CREATE TABLE IF NOT EXISTS calendar_meeting_proposals (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  event_id text NOT NULL REFERENCES calendar_events(id) ON DELETE CASCADE,
  attendee_email citext NOT NULL,
  attendee_name text NOT NULL DEFAULT '',
  proposed_starts_at timestamptz NOT NULL,
  proposed_ends_at timestamptz NOT NULL,
  timezone text NOT NULL DEFAULT 'UTC',
  note text NOT NULL DEFAULT '',
  sequence integer NOT NULL DEFAULT 0,
  recurrence_id timestamptz,
  status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'accepted', 'declined', 'superseded')),
  source text NOT NULL DEFAULT 'link' CHECK (source IN ('imip', 'link', 'app')),
  message_id text NOT NULL DEFAULT '',
  decided_by uuid REFERENCES users(id) ON DELETE SET NULL,
  decided_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS calendar_meeting_proposals_pending_idx
  ON calendar_meeting_proposals (event_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS calendar_meeting_proposals_attendee_idx
  ON calendar_meeting_proposals (event_id, attendee_email, status);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.calendar_event_exceptions TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.calendar_meeting_proposals TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0038_calendar_phase2')
ON CONFLICT (version) DO NOTHING;

COMMIT;
