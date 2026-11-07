-- Office Phase 6: optional runtime monitoring for the Control Plane dashboard.
BEGIN;

ALTER TABLE public.office_settings
  ADD COLUMN IF NOT EXISTS monitoring_enabled boolean NOT NULL DEFAULT true;

INSERT INTO schema_migrations (version) VALUES ('0056_office_monitoring') ON CONFLICT DO NOTHING;

COMMIT;
