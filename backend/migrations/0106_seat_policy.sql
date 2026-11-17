-- Defense-in-depth active-user seat limit.
--
-- The application enforces seats in the webmail, admin, SCIM and Control
-- activation paths. This trigger catches a missed application path or a direct
-- INSERT/UPDATE that activates a user. It is not a substitute for the signed
-- license: the policy row is written by the product after it verifies a
-- license, and a root operator can still alter the database.
BEGIN;

CREATE TABLE IF NOT EXISTS control.seat_policy (
  id boolean PRIMARY KEY DEFAULT true CHECK (id),
  seats integer NOT NULL DEFAULT 25 CHECK (seats > 0),
  tenant_id text,
  expires_at timestamptz,
  grace_days integer NOT NULL DEFAULT 0 CHECK (grace_days >= 0),
  lock_key integer NOT NULL DEFAULT 0,
  source text NOT NULL DEFAULT 'community',
  updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO control.seat_policy (id, seats, tenant_id, expires_at, grace_days, lock_key, source)
VALUES (true, 25, NULL, NULL, 0, 0, 'community')
ON CONFLICT (id) DO NOTHING;

GRANT SELECT, UPDATE ON control.seat_policy TO a8m_app;
GRANT SELECT ON control.seat_policy TO alt8mail_control;

CREATE OR REPLACE FUNCTION public.enforce_active_seat_limit() RETURNS trigger AS $$
DECLARE
  policy control.seat_policy%ROWTYPE;
  active_count integer;
  effective_seats integer;
  effective_tenant text;
  active_new boolean;
  active_old boolean;
  activation boolean;
BEGIN
  active_new := NEW.status = 'active' AND NEW.deleted_at IS NULL AND NEW.is_system_admin = false;
  IF TG_OP = 'UPDATE' THEN
    active_old := OLD.status = 'active' AND OLD.deleted_at IS NULL AND OLD.is_system_admin = false;
    activation := active_new AND (NOT active_old OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id);
  ELSE
    activation := active_new;
  END IF;
  IF NOT activation THEN
    RETURN NEW;
  END IF;

  SELECT * INTO policy FROM control.seat_policy WHERE id = true;
  IF NOT FOUND THEN
    RETURN NEW;
  END IF;
  effective_seats := policy.seats;
  effective_tenant := policy.tenant_id;
  IF policy.expires_at IS NOT NULL
     AND now() > policy.expires_at + make_interval(days => policy.grace_days) THEN
    effective_seats := 25;
    effective_tenant := NULL;
  END IF;
  IF effective_tenant IS NOT NULL AND effective_tenant <> NEW.tenant_id::text THEN
    RETURN NEW;
  END IF;

  IF effective_tenant IS NOT NULL THEN
    PERFORM 1 FROM public.tenants WHERE id::text = effective_tenant FOR UPDATE;
  ELSE
    PERFORM pg_advisory_xact_lock(1094208817, policy.lock_key);
  END IF;

  SELECT count(*) INTO active_count
  FROM public.users
  WHERE status = 'active'
    AND deleted_at IS NULL
    AND is_system_admin = false
    AND id <> NEW.id
    AND (effective_tenant IS NULL OR tenant_id::text = effective_tenant);

  IF active_count >= effective_seats THEN
    RAISE EXCEPTION 'license seat limit reached (limit %, used %)',
      effective_seats, active_count
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS users_active_seat_limit ON public.users;
CREATE TRIGGER users_active_seat_limit
  BEFORE INSERT OR UPDATE OF status, deleted_at, is_system_admin, tenant_id ON public.users
  FOR EACH ROW EXECUTE FUNCTION public.enforce_active_seat_limit();

INSERT INTO schema_migrations (version) VALUES ('0106_seat_policy')
ON CONFLICT (version) DO NOTHING;

COMMIT;
