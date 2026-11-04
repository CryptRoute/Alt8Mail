-- Calendar enterprise differentiators: delivery analytics, room/resource
-- booking and external meeting polls.
BEGIN;

ALTER TABLE calendar_attendees ADD COLUMN IF NOT EXISTS last_delivery_status text NOT NULL DEFAULT 'unknown';
ALTER TABLE calendar_attendees ADD COLUMN IF NOT EXISTS last_delivery_at timestamptz;
ALTER TABLE calendar_attendees ADD COLUMN IF NOT EXISTS last_delivery_error text NOT NULL DEFAULT '';

-- Shared rooms and equipment. Resource addresses are calendar identities, not
-- mailboxes; they are emitted with CUTYPE=RESOURCE and never receive SMTP mail.
CREATE TABLE IF NOT EXISTS calendar_resources (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name text NOT NULL,
  email citext NOT NULL,
  kind text NOT NULL DEFAULT 'room' CHECK (kind IN ('room', 'equipment', 'other')),
  location text NOT NULL DEFAULT '',
  capacity integer NOT NULL DEFAULT 0,
  active boolean NOT NULL DEFAULT true,
  created_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, email)
);

CREATE TABLE IF NOT EXISTS calendar_event_resources (
  event_id text NOT NULL REFERENCES calendar_events(id) ON DELETE CASCADE,
  resource_id text NOT NULL REFERENCES calendar_resources(id) ON DELETE RESTRICT,
  name text NOT NULL DEFAULT '',
  email citext NOT NULL,
  status text NOT NULL DEFAULT 'accepted' CHECK (status IN ('needs-action', 'accepted', 'declined', 'tentative')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (event_id, resource_id)
);
CREATE INDEX IF NOT EXISTS calendar_event_resources_resource_idx
  ON calendar_event_resources (resource_id, event_id);

INSERT INTO calendar_resources (id, tenant_id, name, email, kind, location, capacity)
SELECT 'room-a-' || replace(t.id::text, '-', ''), t.id, 'Conference Room A',
       'room-a@' || COALESCE((
         SELECT md.domain::text FROM mail_domains md WHERE md.tenant_id=t.id
         ORDER BY md.is_primary DESC, md.domain LIMIT 1
       ), 'alt8mail.local'), 'room', 'Head office', 8
FROM tenants t
ON CONFLICT (tenant_id, email) DO NOTHING;

INSERT INTO calendar_resources (id, tenant_id, name, email, kind, location, capacity)
SELECT 'board-' || replace(t.id::text, '-', ''), t.id, 'Board Room',
       'board-room@' || COALESCE((
         SELECT md.domain::text FROM mail_domains md WHERE md.tenant_id=t.id
         ORDER BY md.is_primary DESC, md.domain LIMIT 1
       ), 'alt8mail.local'), 'room', 'Executive floor', 16
FROM tenants t
ON CONFLICT (tenant_id, email) DO NOTHING;

INSERT INTO calendar_resources (id, tenant_id, name, email, kind, location, capacity)
SELECT 'projector-' || replace(t.id::text, '-', ''), t.id, 'Portable projector',
       'projector@' || COALESCE((
         SELECT md.domain::text FROM mail_domains md WHERE md.tenant_id=t.id
         ORDER BY md.is_primary DESC, md.domain LIMIT 1
       ), 'alt8mail.local'), 'equipment', 'IT cupboard', 0
FROM tenants t
ON CONFLICT (tenant_id, email) DO NOTHING;

-- Meeting polls collect external availability without requiring an account.
CREATE TABLE IF NOT EXISTS calendar_polls (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title text NOT NULL,
  description text NOT NULL DEFAULT '',
  timezone text NOT NULL DEFAULT 'UTC',
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed', 'scheduled', 'cancelled')),
  selected_slot_id text,
  event_id text REFERENCES calendar_events(id) ON DELETE SET NULL,
  closes_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS calendar_polls_user_idx ON calendar_polls (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS calendar_poll_slots (
  id text PRIMARY KEY,
  poll_id text NOT NULL REFERENCES calendar_polls(id) ON DELETE CASCADE,
  starts_at timestamptz NOT NULL,
  ends_at timestamptz NOT NULL,
  position integer NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS calendar_poll_slots_poll_idx ON calendar_poll_slots (poll_id, position);

CREATE TABLE IF NOT EXISTS calendar_poll_invitees (
  poll_id text NOT NULL REFERENCES calendar_polls(id) ON DELETE CASCADE,
  email citext NOT NULL,
  name text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (poll_id, email)
);

CREATE TABLE IF NOT EXISTS calendar_poll_responses (
  poll_id text NOT NULL REFERENCES calendar_polls(id) ON DELETE CASCADE,
  invitee_email citext NOT NULL,
  slot_id text NOT NULL REFERENCES calendar_poll_slots(id) ON DELETE CASCADE,
  response text NOT NULL DEFAULT 'maybe' CHECK (response IN ('yes', 'maybe', 'no')),
  note text NOT NULL DEFAULT '',
  responded_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (poll_id, invitee_email, slot_id)
);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.calendar_resources TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.calendar_event_resources TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.calendar_polls TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.calendar_poll_slots TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.calendar_poll_invitees TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.calendar_poll_responses TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0039_calendar_phase3')
ON CONFLICT (version) DO NOTHING;

COMMIT;
