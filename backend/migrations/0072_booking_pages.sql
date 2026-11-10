-- Phase 3 booking pages: public scheduling links, working hours/buffers,
-- team round-robin and race-safe booking into calendars.
BEGIN;

CREATE TABLE IF NOT EXISTS public.booking_pages (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  slug citext NOT NULL UNIQUE,
  title text NOT NULL,
  description text NOT NULL DEFAULT '',
  duration_minutes integer NOT NULL DEFAULT 30 CHECK (duration_minutes BETWEEN 5 AND 240),
  buffer_before_minutes integer NOT NULL DEFAULT 0 CHECK (buffer_before_minutes BETWEEN 0 AND 120),
  buffer_after_minutes integer NOT NULL DEFAULT 0 CHECK (buffer_after_minutes BETWEEN 0 AND 120),
  working_start_hour integer NOT NULL DEFAULT 9 CHECK (working_start_hour BETWEEN 0 AND 23),
  working_end_hour integer NOT NULL DEFAULT 17 CHECK (working_end_hour BETWEEN 1 AND 24),
  timezone text NOT NULL DEFAULT 'UTC',
  weekdays integer[] NOT NULL DEFAULT '{1,2,3,4,5}',
  round_robin boolean NOT NULL DEFAULT false,
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS booking_pages_user_idx ON public.booking_pages (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS public.booking_page_members (
  page_id text NOT NULL REFERENCES public.booking_pages(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  PRIMARY KEY (page_id, user_id)
);

CREATE TABLE IF NOT EXISTS public.bookings (
  id text PRIMARY KEY,
  page_id text NOT NULL REFERENCES public.booking_pages(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  booked_user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  start_at timestamptz NOT NULL,
  end_at timestamptz NOT NULL,
  booker_name text NOT NULL,
  booker_email citext NOT NULL,
  note text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'confirmed' CHECK (status IN ('confirmed', 'cancelled')),
  calendar_event_id text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS bookings_page_slot_idx ON public.bookings (page_id, start_at) WHERE status='confirmed';
CREATE INDEX IF NOT EXISTS bookings_user_slot_idx ON public.bookings (booked_user_id, start_at) WHERE status='confirmed';

GRANT SELECT, INSERT, UPDATE, DELETE ON public.booking_pages TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.booking_page_members TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.bookings TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0072_booking_pages')
ON CONFLICT DO NOTHING;

COMMIT;
