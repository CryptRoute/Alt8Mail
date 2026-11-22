-- Office Phase 7: Collabora Online engine selection.
-- The engine is per installation. Existing rows remain on ONLYOFFICE until
-- the administrator flips it; the WOPI host re-checks every permission on
-- each request, so switching engines does not change the version model.
BEGIN;

ALTER TABLE public.office_settings
  ADD COLUMN IF NOT EXISTS engine text NOT NULL DEFAULT 'onlyoffice'
  CHECK (engine IN ('onlyoffice', 'collabora'));

INSERT INTO schema_migrations (version) VALUES ('0057_office_collabora') ON CONFLICT DO NOTHING;

COMMIT;
