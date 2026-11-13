-- Alt8Mail database grants.
--
-- Migrations create the objects; this file is the single authoritative grant
-- set applied by the installer and by the release updater after migrations or a
-- restore. Keep it idempotent: every statement must be safe to re-run.
BEGIN;

GRANT CONNECT ON DATABASE a8m TO a8m_app, a8m_mail, alt8mail_control;

GRANT USAGE, CREATE ON SCHEMA public TO a8m_app;
GRANT USAGE ON SCHEMA public TO a8m_mail;
GRANT USAGE ON SCHEMA control TO a8m_mail, alt8mail_control;

-- Dovecot authentication and Postfix virtual maps use the restricted mail role.
GRANT SELECT ON
  public.mailboxes,
  public.user_aliases,
  public.mail_domains,
  public.users,
  public.tenants,
  public.groups,
  public.group_members,
  public.postfix_alias_targets
TO a8m_mail;
GRANT EXECUTE ON FUNCTION public.postfix_alias_lookup(text) TO a8m_mail;
GRANT SELECT ON control.mail_aliases TO a8m_mail;

-- The System Control plane manages mail objects but not webmail internals.
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA control TO alt8mail_control;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA control TO alt8mail_control;
GRANT SELECT, INSERT, UPDATE, DELETE ON
  public.mail_domains,
  public.users,
  public.user_aliases,
  public.mailboxes,
  public.sieve_profiles,
  public.mail_identities
TO alt8mail_control;
GRANT SELECT ON
  public.tenants,
  public.group_members,
  public.mail_messages,
  public.outbound_events,
  public.tenant_ai_settings,
  public.trusted_senders
TO alt8mail_control;
GRANT SELECT, UPDATE, DELETE ON public.groups TO alt8mail_control;
GRANT SELECT, INSERT ON public.password_history TO alt8mail_control;
GRANT SELECT, INSERT, UPDATE ON public.app_states TO alt8mail_control;
GRANT SELECT, INSERT, UPDATE, DELETE ON
  public.security_reports,
  public.security_report_url_hosts
TO alt8mail_control;
GRANT SELECT, INSERT ON public.security_block_events TO alt8mail_control;
GRANT USAGE, SELECT ON SEQUENCE public.security_block_events_id_seq TO alt8mail_control;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO alt8mail_control;
-- Office lifecycle state is owned by the Control Plane. The file, document
-- and callback tables stay webmail-owned and are read-only here.
GRANT SELECT, INSERT, UPDATE, DELETE ON public.office_settings TO alt8mail_control;
GRANT SELECT ON
  public.files,
  public.office_documents,
  public.office_callback_events
TO alt8mail_control;
-- Add-on catalogue and web push are surfaced in System Control too.
GRANT SELECT ON
  public.addon_system_catalog,
  public.tenant_addons,
  public.addon_installs
TO alt8mail_control;
GRANT UPDATE ON public.addon_system_catalog TO alt8mail_control;
GRANT SELECT, INSERT, UPDATE, DELETE ON
  public.web_push_settings,
  public.web_push_subscriptions
TO alt8mail_control;

-- The webmail and customer-admin services own the public application schema.
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO a8m_app;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO a8m_app;

-- The webmail reads the Control Plane's per-mailbox daily cap so its outbound
-- meter and pre-send check match the Postfix policy daemon.
GRANT USAGE ON SCHEMA control TO a8m_app;
GRANT SELECT ON control.domain_settings TO a8m_app;
-- The webmail and customer-admin services resolve the effective mail volume
-- when they create a mailbox. They never write the volume registry.
GRANT SELECT ON control.storage_volumes, control.storage_policy TO a8m_app;

COMMIT;
