BEGIN;

DROP TABLE IF EXISTS tenant_webmail_hosts;
REVOKE SELECT (hostname, service, enabled, certificate_name)
  ON control.webnet_routes FROM a8m_app;

DELETE FROM schema_migrations WHERE version = '0062_tenant_webmail_hosts';

COMMIT;
