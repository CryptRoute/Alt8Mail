-- The product writes the seat policy with INSERT ... ON CONFLICT, so the
-- application role needs INSERT in addition to SELECT and UPDATE. Migration
-- 0106 granted only SELECT and UPDATE, which made every sync fail with
-- "permission denied for table seat_policy" after an enterprise activation.
BEGIN;

GRANT SELECT, INSERT, UPDATE ON control.seat_policy TO a8m_app;
GRANT SELECT ON control.seat_policy TO alt8mail_control;

INSERT INTO schema_migrations (version) VALUES ('0107_seat_policy_grants')
ON CONFLICT (version) DO NOTHING;

COMMIT;
