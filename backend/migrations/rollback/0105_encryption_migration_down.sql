BEGIN;

DROP TRIGGER IF EXISTS user_crypto_private_keys_client_side_guard ON public.user_crypto_private_keys;
DROP FUNCTION IF EXISTS public.reject_client_side_private_key();
DROP TABLE IF EXISTS public.tenant_encryption_migrations;
DROP TABLE IF EXISTS public.user_crypto_key_exports;

DELETE FROM schema_migrations WHERE version = '0105_encryption_migration';

COMMIT;
