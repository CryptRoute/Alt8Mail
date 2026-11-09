BEGIN;

DROP TABLE IF EXISTS scim_tokens;

DROP INDEX IF EXISTS groups_tenant_scim_external_idx;
ALTER TABLE groups DROP COLUMN IF EXISTS scim_role;
ALTER TABLE groups DROP COLUMN IF EXISTS scim_external_id;

DROP INDEX IF EXISTS users_tenant_scim_external_idx;
ALTER TABLE users DROP COLUMN IF EXISTS role_source;
ALTER TABLE users DROP COLUMN IF EXISTS scim_external_id;
ALTER TABLE users DROP COLUMN IF EXISTS family_name;
ALTER TABLE users DROP COLUMN IF EXISTS given_name;

DELETE FROM schema_migrations WHERE version = '0061_scim';

COMMIT;
