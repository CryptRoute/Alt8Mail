-- Phase 5 encryption: application-level blob encryption and client-side
-- encrypted Cabinet folder flags.
BEGIN;

ALTER TABLE public.files
  ADD COLUMN IF NOT EXISTS encryption_key_version integer NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS client_encrypted boolean NOT NULL DEFAULT false;

ALTER TABLE public.file_folders
  ADD COLUMN IF NOT EXISTS client_encrypted boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS encryption_key_hint text NOT NULL DEFAULT '';

INSERT INTO schema_migrations (version) VALUES ('0081_encrypted_storage')
ON CONFLICT DO NOTHING;

COMMIT;
