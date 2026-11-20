#!/usr/bin/env bash
set -Eeuo pipefail

phase_tenant() {
  local release="$A8M_INSTALL_ROOT/current"
  [[ -x "$release/bin/a8m-admin" ]] \
    || { ui_err "$release/bin/a8m-admin is missing"; return 1; }
  [[ -x "$release/bin/a8m-inituser" ]] \
    || { ui_err "$release/bin/a8m-inituser is missing"; return 1; }
  [[ -x "$release/bin/a8m-mailprovision" ]] \
    || { ui_err "$release/bin/a8m-mailprovision is missing"; return 1; }

  if ! runuser -u postgres -- psql -d a8m -v ON_ERROR_STOP=1 -q \
    -v tenant_name="$A8M_TENANT_NAME" -v tenant_slug="$A8M_TENANT_SLUG" \
    -v domain="$A8M_DOMAIN" -v email="$A8M_ADMIN_EMAIL" -v name="$A8M_ADMIN_NAME" <<'SQL'
INSERT INTO tenants (slug, name, plan, region, billing_email)
VALUES (:'tenant_slug', :'tenant_name', 'business', 'us-east-1', :'email')
ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name, updated_at = now()
RETURNING id AS tenant_id \gset
INSERT INTO mail_domains (tenant_id, domain, is_primary, dkim_selector, status, verification_token, verified_at, mx_verified, spf_verified, dmarc_verified)
VALUES (:'tenant_id', :'domain', true, 'a8m', 'manual', encode(gen_random_bytes(18), 'hex'), now(), true, true, true)
ON CONFLICT (domain) DO UPDATE SET
  tenant_id = EXCLUDED.tenant_id, is_primary = true, dkim_selector = 'a8m', status = 'manual',
  verified_at = COALESCE(mail_domains.verified_at, now()), mx_verified = true, spf_verified = true
RETURNING id AS domain_id \gset
INSERT INTO users (tenant_id, email, name, initials, color, role, status, password_hash, mail_password_hash,
                   quota_bytes, is_system_admin)
VALUES (:'tenant_id', :'email', :'name', 'OW', '#0f6cbd', 'owner', 'active', '', '', 17179869184, true)
ON CONFLICT (email) DO UPDATE SET
  tenant_id = EXCLUDED.tenant_id, name = EXCLUDED.name, role = 'owner',
  is_system_admin = true, status = 'active', deleted_at = NULL, updated_at = now()
RETURNING id AS owner_id \gset
INSERT INTO user_aliases (tenant_id, user_id, email, label, kind, can_send, active)
VALUES (:'tenant_id', :'owner_id', :'email', 'Primary', 'primary', true, true)
ON CONFLICT (email) DO UPDATE SET user_id = EXCLUDED.user_id, active = true;
INSERT INTO mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path, active)
VALUES (:'tenant_id', :'owner_id', :'email', :'domain', split_part(:'email', '@', 1),
        '/var/vmail/' || :'domain' || '/' || split_part(:'email', '@', 1) || '/Maildir', true)
ON CONFLICT (email) DO UPDATE SET user_id = EXCLUDED.user_id, active = true;
SQL
  then
    ui_err "Could not seed the first tenant and owner"
    return 1
  fi

  set -a
  # shellcheck disable=SC1090
  . "$A8M_CONFIG_ROOT/app.env"
  set +a

  local empty_web
  empty_web="$(runuser -u postgres -- psql -d a8m -Atqc \
    "SELECT COALESCE(password_hash,'') = '' FROM users WHERE email='$A8M_ADMIN_EMAIL'")"
  if [[ "$empty_web" == "t" ]]; then
    printf '%s\n' "$OWNER_WEB_PASSWORD" | "$release/bin/a8m-admin" -email "$A8M_ADMIN_EMAIL" >/dev/null \
      || { ui_err "Could not set the owner web password"; return 1; }
  fi

  local empty_mail
  empty_mail="$(runuser -u postgres -- psql -d a8m -Atqc \
    "SELECT COALESCE(mail_password_hash,'') = '' FROM users WHERE email='$A8M_ADMIN_EMAIL'")"
  if [[ "$empty_mail" == "t" ]]; then
    local mail_hash
    mail_hash="$(printf '%s\n%s\n' "$OWNER_MAIL_PASSWORD" "$OWNER_MAIL_PASSWORD" \
      | doveadm pw -s SHA512-CRYPT | tr -d '\r' \
      | awk '/^\{SHA512-CRYPT\}/ && !found {print; found=1}')"
    [[ -n "$mail_hash" ]] || { ui_err "Could not create the owner mailbox password hash"; return 1; }
    runuser -u postgres -- psql -d a8m -v ON_ERROR_STOP=1 -q \
      -v hash="$mail_hash" -v email="$A8M_ADMIN_EMAIL" <<'SQL'
UPDATE users SET mail_password_hash = :'hash' WHERE email = :'email';
SQL
  fi

  local app_states
  app_states="$(runuser -u postgres -- psql -d a8m -Atqc \
    "SELECT COUNT(*) FROM app_states s JOIN users u ON u.id=s.user_id WHERE u.email='$A8M_ADMIN_EMAIL'")"
  if [[ "$app_states" == "0" ]]; then
    "$release/bin/a8m-inituser" -email "$A8M_ADMIN_EMAIL" >/dev/null \
      || { ui_err "Could not initialize the owner app state"; return 1; }
  fi
  "$release/bin/a8m-mailprovision" -all >/dev/null \
    || { ui_err "Maildir provisioning failed"; return 1; }

  ui_task "Creating the System Control superadmin"
  set -a
  # shellcheck disable=SC1090
  . "$A8M_CONFIG_ROOT/control.env"
  set +a
  local control_exists
  control_exists="$(runuser -u postgres -- psql -d a8m -Atqc \
    "SELECT COUNT(*) FROM control.admin_users WHERE email='$A8M_CONTROL_EMAIL'")"
  if [[ "$control_exists" == "0" ]]; then
    printf '%s\n' "$CONTROL_ADMIN_PASSWORD" \
      | "$A8M_CONTROL_ROOT/bin/alt8mail-control" admin \
        --email "$A8M_CONTROL_EMAIL" --name 'System Administrator' \
        --role superadmin --password-stdin >/dev/null \
      || { ui_err "Could not create the Control superadmin"; return 1; }
  fi

  if ! runuser -u postgres -- psql -d a8m -v ON_ERROR_STOP=1 -q \
    -v host="$A8M_MAIL_HOSTNAME" -v webmail="https://$A8M_MAIL_HOSTNAME" \
    -v public_ip="$A8M_PUBLIC_IP" -v mail_from="postmaster@$A8M_DOMAIN" \
    -v ssl_email="$A8M_TLS_EMAIL" <<'SQL'
INSERT INTO control.settings (key, value) VALUES
  ('server.hostname', to_jsonb(:'host'::text)),
  ('server.mail_hostname', to_jsonb(:'host'::text)),
  ('server.webmail_url', to_jsonb(:'webmail'::text)),
  ('server.public_ipv4', to_jsonb(:'public_ip'::text)),
  ('server.mail_from', to_jsonb(:'mail_from'::text)),
  ('ssl.admin_email', to_jsonb(:'ssl_email'::text))
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
WHERE control.settings.value IS NULL
   OR control.settings.value = 'null'::jsonb
   OR control.settings.value = '""'::jsonb;
SQL
  then
    ui_err "Could not seed the Control server settings"
    return 1
  fi

  run tenant -- systemctl enable --now a8m-office-agent \
    || ui_warn "Office agent did not start; install Docker, then start a8m-office-agent"
  if [[ -x "$release/bin/a8m-dkimprovision" ]]; then
    run tenant -- "$release/bin/a8m-dkimprovision" \
      || ui_warn "DKIM provisioning will retry from its timer"
  fi
  systemctl try-restart rspamd >/dev/null 2>&1 || true

  local dkim_record
  dkim_record="$(runuser -u postgres -- psql -d a8m -Atqc \
    "SELECT dkim_record FROM mail_domains WHERE domain='$A8M_DOMAIN'")"
  umask 077
  if [[ ! -s /root/alt8mail-initial-credentials.txt ]]; then
    cat >/root/alt8mail-initial-credentials.txt <<EOF
Alt8Mail initial credentials
Generated: $(date -u +%Y-%m-%dT%H:%M:%SZ)
Webmail: https://$A8M_MAIL_HOSTNAME/
Customer admin: https://$A8M_MAIL_HOSTNAME/admin/
Owner email: $A8M_ADMIN_EMAIL
Owner web password: $OWNER_WEB_PASSWORD
Owner mailbox password: $OWNER_MAIL_PASSWORD
System Control: https://$A8M_MAIL_HOSTNAME:8808/
Control email: $A8M_CONTROL_EMAIL
Control password: $CONTROL_ADMIN_PASSWORD
Secrets directory: $A8M_SECRETS_DIR
Database: a8m (app role a8m_app, mail role a8m_mail)
EOF
    chmod 0600 /root/alt8mail-initial-credentials.txt
  fi
  cat >/root/alt8mail-dns-records.txt <<EOF
MX: $A8M_DOMAIN. 10 $A8M_MAIL_HOSTNAME.
SPF: v=spf1 mx -all
DMARC: v=DMARC1; p=none; rua=mailto:dmarc@$A8M_DOMAIN
DKIM selector a8m: a8m._domainkey.$A8M_DOMAIN. TXT "$dkim_record"
EOF
  chmod 0600 /root/alt8mail-dns-records.txt
  ui_task_ok "Tenant, domain, owner and Control account ready"
}
