-- Deployment identity-provider settings edited from the organization admin
-- console. The row is optional: when it is absent the server keeps using the
-- OIDC/SAML environment configuration, so existing installations upgrade
-- without changing their sign-in path.
BEGIN;

CREATE TABLE IF NOT EXISTS public.identity_provider_settings (
  id text PRIMARY KEY,
  oidc_enabled boolean NOT NULL DEFAULT false,
  oidc_issuer text NOT NULL DEFAULT '',
  oidc_client_id text NOT NULL DEFAULT '',
  oidc_client_secret text NOT NULL DEFAULT '',
  oidc_redirect_url text NOT NULL DEFAULT '',
  oidc_display_name text NOT NULL DEFAULT '',
  oidc_email_domains text[] NOT NULL DEFAULT '{}',
  saml_enabled boolean NOT NULL DEFAULT false,
  saml_display_name text NOT NULL DEFAULT '',
  saml_idp_metadata_url text NOT NULL DEFAULT '',
  saml_sp_entity_id text NOT NULL DEFAULT '',
  saml_sp_cert_pem text NOT NULL DEFAULT '',
  saml_sp_key_pem text NOT NULL DEFAULT '',
  saml_email_attribute text NOT NULL DEFAULT '',
  saml_email_domains text[] NOT NULL DEFAULT '{}',
  updated_by uuid REFERENCES public.users(id) ON DELETE SET NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.identity_provider_settings TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0108_identity_provider_settings')
ON CONFLICT (version) DO NOTHING;

COMMIT;
