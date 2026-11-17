BEGIN;

DROP TRIGGER IF EXISTS users_active_seat_limit ON public.users;
DROP FUNCTION IF EXISTS public.enforce_active_seat_limit();
REVOKE SELECT, UPDATE ON control.seat_policy FROM a8m_app;
DROP TABLE IF EXISTS control.seat_policy;

DELETE FROM schema_migrations WHERE version = '0106_seat_policy';

COMMIT;
