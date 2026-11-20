-- Licensing is now instance-wide and soft. The community edition has no
-- active-user cap. A paid license overage is a true-up warning in the admin
-- console, never a refused activation or a database-level hard stop.
BEGIN;

DROP TRIGGER IF EXISTS users_active_seat_limit ON public.users;
DROP FUNCTION IF EXISTS public.enforce_active_seat_limit();
DROP TABLE IF EXISTS control.seat_policy;

INSERT INTO schema_migrations (version) VALUES ('0109_soft_user_licensing')
ON CONFLICT (version) DO NOTHING;

COMMIT;
