-- Phase 1F: white-label custom webmail hostnames.
--
-- The tenant settings branding.webmailHosts array remains the source read by
-- the webmail. This table is the global uniqueness index and makes cross-tenant
-- hostname takeover impossible. Control Plane Web Net remains authoritative
-- for DNS, TLS and nginx; the webmail checks an enabled webmail route with a
-- certificate before accepting a hostname.
BEGIN;

CREATE TABLE IF NOT EXISTS tenant_webmail_hosts (
  hostname citext PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS tenant_webmail_hosts_tenant_idx
  ON tenant_webmail_hosts (tenant_id);

-- Backfill any hostnames already stored in tenant settings.
INSERT INTO tenant_webmail_hosts (hostname, tenant_id)
SELECT lower(hostname), t.id
FROM tenants t
CROSS JOIN LATERAL jsonb_array_elements_text(
  COALESCE(t.settings->'branding'->'webmailHosts', '[]'::jsonb)
) AS hostname
WHERE btrim(hostname) <> ''
ON CONFLICT (hostname) DO NOTHING;

GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_webmail_hosts TO a8m_app;
GRANT USAGE ON SCHEMA control TO a8m_app;
GRANT SELECT (hostname, service, enabled, certificate_name)
  ON control.webnet_routes TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0062_tenant_webmail_hosts')
ON CONFLICT (version) DO NOTHING;

COMMIT;
