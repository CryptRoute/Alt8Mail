BEGIN;

DROP TRIGGER IF EXISTS users_active_seat_limit ON public.users;
DROP FUNCTION IF EXISTS public.enforce_active_seat_limit();

-- Migration 0109 removes the soft seat policy table. A rollback of 0106 must
-- therefore tolerate its absence when the upgrade smoke test unwinds a
-- database that already ran 0109.
DO $$
BEGIN
  IF to_regclass('control.seat_policy') IS NOT NULL THEN
    REVOKE SELECT, UPDATE ON control.seat_policy FROM a8m_app;
    DROP TABLE control.seat_policy;
  END IF;
END $$;

DELETE FROM schema_migrations WHERE version = '0106_seat_policy';

COMMIT;
