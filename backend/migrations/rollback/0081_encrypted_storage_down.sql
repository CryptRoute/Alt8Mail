BEGIN;
ALTER TABLE public.file_folders DROP COLUMN IF EXISTS encryption_key_hint;
ALTER TABLE public.file_folders DROP COLUMN IF EXISTS client_encrypted;
ALTER TABLE public.files DROP COLUMN IF EXISTS client_encrypted;
ALTER TABLE public.files DROP COLUMN IF EXISTS encryption_key_version;
DELETE FROM schema_migrations WHERE version = '0081_encrypted_storage';
COMMIT;
