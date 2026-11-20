#!/usr/bin/env bash
set -Eeuo pipefail

phase_config() {
  local deploy="$A8M_SRC_DIR/deploy"

  # ---- Redis -------------------------------------------------------------
  ui_task "Configuring Redis"
  install -d -m 0755 /etc/redis
  sed "s|__REDIS_PASSWORD__|$REDIS_PASSWORD|g" "$deploy/redis/a8m.conf" >/etc/redis/a8m.conf
  chmod 0640 /etc/redis/a8m.conf
  if ! grep -q '^include /etc/redis/a8m.conf' /etc/redis/redis.conf; then
    printf '\ninclude /etc/redis/a8m.conf\n' >>/etc/redis/redis.conf
  fi
  run config -- systemctl enable --now redis-server || return 1
  run config -- systemctl restart redis-server || return 1

  # ---- Dovecot -----------------------------------------------------------
  ui_task "Configuring Dovecot"
  command -v doveadm >/dev/null 2>&1 || { ui_err "doveadm is not installed"; return 1; }
  local master_hash
  master_hash="$(printf '%s\n%s\n' "$IMAP_MASTER_PASSWORD" "$IMAP_MASTER_PASSWORD" \
    | doveadm pw -s SHA512-CRYPT | tr -d '\r' \
    | awk '/^\{SHA512-CRYPT\}/ && !found {print; found=1}')"
  [[ -n "$master_hash" ]] || { ui_err "Could not create the Dovecot master-user hash"; return 1; }
  umask 077
  printf 'a8m-worker:%s\n' "$master_hash" >/etc/dovecot/master-users
  chown root:dovecot /etc/dovecot/master-users
  chmod 0640 /etc/dovecot/master-users

  install -m 0644 "$deploy/dovecot/10-auth.conf" /etc/dovecot/conf.d/10-auth.conf
  install -m 0644 "$deploy/dovecot/10-ssl.conf" /etc/dovecot/conf.d/10-ssl.conf
  install -m 0644 "$deploy/dovecot/10-mail.conf" /etc/dovecot/conf.d/10-mail.conf
  sed "s|__DOMAIN__|$A8M_DOMAIN|g" "$deploy/dovecot/99-a8m.conf" >/etc/dovecot/conf.d/99-a8m.conf
  install -m 0644 "$deploy/dovecot/05-a8m-master.conf" /etc/dovecot/conf.d/05-a8m-master.conf
  sed "s|__DB_PASSWORD__|$MAIL_DB_PASSWORD|g" "$deploy/dovecot/dovecot-sql.conf.ext" \
    >/etc/dovecot/dovecot-sql.conf.ext
  chmod 0640 /etc/dovecot/dovecot-sql.conf.ext
  chown root:dovecot /etc/dovecot/dovecot-sql.conf.ext 2>/dev/null || true
  umask 027

  # ---- Postfix -----------------------------------------------------------
  ui_task "Configuring Postfix"
  install -m 0644 /etc/postfix/main.cf "/etc/postfix/main.cf.pre-a8m-$(date +%Y%m%d%H%M%S)"
  sed -e "s|__MAIL_HOSTNAME__|$A8M_MAIL_HOSTNAME|g" \
      -e "s|__DOMAIN__|$A8M_DOMAIN|g" "$deploy/postfix/main.cf" >/etc/postfix/main.cf
  local map
  for map in pgsql-virtual-domains.cf pgsql-virtual-mailbox.cf pgsql-virtual-alias.cf; do
    sed "s|__DB_PASSWORD__|$MAIL_DB_PASSWORD|g" "$deploy/postfix/$map" >"/etc/postfix/$map"
    chown root:postfix "/etc/postfix/$map"
    chmod 0640 "/etc/postfix/$map"
  done
  touch /etc/postfix/suppressions /etc/postfix/suppressions_in
  chown root:postfix /etc/postfix/suppressions /etc/postfix/suppressions_in
  chmod 0640 /etc/postfix/suppressions /etc/postfix/suppressions_in
  postmap /etc/postfix/suppressions || return 1
  postmap /etc/postfix/suppressions_in || return 1
  newaliases || return 1
  postconf -M submission/inet="submission inet n - n - - smtpd"
  postconf -P submission/inet/syslog_name=postfix/submission
  postconf -P submission/inet/smtpd_tls_security_level=encrypt
  postconf -P submission/inet/smtpd_sasl_auth_enable=yes
  postconf -e smtpd_tls_security_level=may
  [[ -n "$(postconf -h smtp_helo_name)" ]] || { ui_err "smtp_helo_name is empty"; return 1; }

  # ---- Rspamd and ClamAV -------------------------------------------------
  ui_task "Configuring Rspamd and ClamAV"
  install -d -m 0755 /etc/rspamd/local.d /var/lib/rspamd/dkim
  install -m 0644 "$deploy/rspamd/antivirus.conf" /etc/rspamd/local.d/antivirus.conf
  install -m 0644 "$deploy/rspamd/dkim_signing.conf" /etc/rspamd/local.d/dkim_signing.conf
  install -m 0644 "$deploy/rspamd/milter_headers.conf" /etc/rspamd/local.d/milter_headers.conf
  sed "s|__REDIS_PASSWORD__|$REDIS_PASSWORD|g" "$deploy/rspamd/redis.conf" >/etc/rspamd/local.d/redis.conf
  chown root:_rspamd /etc/rspamd/local.d/redis.conf 2>/dev/null || true
  chmod 0640 /etc/rspamd/local.d/redis.conf
  chmod 0644 /etc/rspamd/local.d/antivirus.conf /etc/rspamd/local.d/dkim_signing.conf

  local multimap=/etc/rspamd/local.d/multimap.conf
  if [[ ! -e "$multimap" ]]; then
    install -m 0644 "$deploy/rspamd/multimap.conf" "$multimap"
  elif ! grep -q 'A8M_REPORTED_SENDER' "$multimap"; then
    printf '\n# Alt8Mail threat-intelligence rules\n' >>"$multimap"
    cat "$deploy/rspamd/multimap.conf" >>"$multimap"
  fi
  chmod 0644 "$multimap"
  local settings=/etc/rspamd/local.d/settings.conf
  if [[ ! -e "$settings" ]]; then
    install -m 0644 "$deploy/rspamd/settings.conf" "$settings"
  elif ! grep -q 'a8m_authenticated_submission' "$settings"; then
    printf '\n# Alt8Mail local-domain guard\n' >>"$settings"
    cat "$deploy/rspamd/settings.conf" >>"$settings"
  fi
  chmod 0644 "$settings"
  touch /var/lib/rspamd/dkim/selectors.map /var/lib/rspamd/local_domains.map
  chown -R _rspamd:_rspamd /var/lib/rspamd/dkim 2>/dev/null || true
  chown _rspamd:_rspamd /var/lib/rspamd/local_domains.map 2>/dev/null || true
  run config -- systemctl enable --now clamav-freshclam || ui_warn "clamav-freshclam could not start yet"
  run config -- systemctl enable --now clamav-daemon || ui_warn "clamav-daemon will start after its first signature update"

  # ---- License and application environment -------------------------------
  ui_task "Writing application, Control and update configuration"
  resolve_release_source || true
  # A source checkout does not carry deploy/release-license.env; the build
  # phase generates it inside the release tree. Pick up the vendor defaults
  # from whichever tree actually supplied the binaries.
  if [[ -f "$A8M_RELEASE_SOURCE/deploy/release-license.env" ]]; then
    # shellcheck disable=SC1091
    . "$A8M_RELEASE_SOURCE/deploy/release-license.env"
    A8M_LICENSE_PUBLIC_KEY="${A8M_LICENSE_PUBLIC_KEY:-${A8M_RELEASE_LICENSE_PUBLIC_KEY:-}}"
    A8M_LICENSE_PUBLIC_KEY_ID="${A8M_LICENSE_PUBLIC_KEY_ID:-${A8M_RELEASE_LICENSE_PUBLIC_KEY_ID:-}}"
    A8M_LICENSE_PUBLIC_KEYS="${A8M_LICENSE_PUBLIC_KEYS:-${A8M_RELEASE_LICENSE_PUBLIC_KEYS:-}}"
    A8M_LICENSE_PORTAL_URL="${A8M_LICENSE_PORTAL_URL:-${A8M_RELEASE_LICENSE_PORTAL_URL:-}}"
    A8M_ENTERPRISE_RELEASE_PUBLIC_KEY="${A8M_ENTERPRISE_RELEASE_PUBLIC_KEY:-${A8M_RELEASE_ENTERPRISE_RELEASE_PUBLIC_KEY:-}}"
    export A8M_LICENSE_PUBLIC_KEY A8M_LICENSE_PUBLIC_KEY_ID A8M_LICENSE_PUBLIC_KEYS
    export A8M_LICENSE_PORTAL_URL A8M_ENTERPRISE_RELEASE_PUBLIC_KEY
  fi
  local p_app_usage_url p_app_usage_token p_app_usage_hours
  local p_addon_url p_addon_secret p_addon_gateway
  p_app_usage_url="$(env_file_value ALT8MAIL_LICENSE_USAGE_URL)"
  p_app_usage_token="$(env_file_value ALT8MAIL_LICENSE_USAGE_TOKEN)"
  p_app_usage_hours="$(env_file_value ALT8MAIL_LICENSE_USAGE_INTERVAL_HOURS)"
  p_addon_url="$(env_file_value ALT8MAIL_ADDON_RELAY_URL)"
  p_addon_secret="$(env_file_value ALT8MAIL_ADDON_RELAY_SECRET)"
  p_addon_gateway="$(env_file_value ALT8MAIL_ADDON_GATEWAY_URL)"

  # The webmail license values are initialized before the optional app.env
  # read. A first-time install has no app.env; without these defaults the safe
  # nested expansions below would touch an unset variable under `set -u`.
  PRESERVE_ALT8MAIL_LICENSE_FILE=""
  PRESERVE_ALT8MAIL_LICENSE_PUBLIC_KEY=""
  PRESERVE_ALT8MAIL_LICENSE_PUBLIC_KEY_ID=""
  PRESERVE_ALT8MAIL_LICENSE_PUBLIC_KEYS=""
  PRESERVE_ALT8MAIL_LICENSE_TENANT=""
  PRESERVE_ALT8MAIL_LICENSE_PORTAL_URL=""
  PRESERVE_ALT8MAIL_LICENSE_ALLOW_CONFIG_KEY=""
  PRESERVE_ALT8MAIL_LICENSE_USAGE_URL=""
  PRESERVE_ALT8MAIL_LICENSE_USAGE_TOKEN=""
  PRESERVE_ALT8MAIL_LICENSE_USAGE_INTERVAL_HOURS=""
  if [[ -f "$A8M_CONFIG_ROOT/app.env" ]]; then
    PRESERVE_ALT8MAIL_LICENSE_FILE="$(env_file_value ALT8MAIL_LICENSE_FILE)"
    PRESERVE_ALT8MAIL_LICENSE_PUBLIC_KEY="$(env_file_value ALT8MAIL_LICENSE_PUBLIC_KEY)"
    PRESERVE_ALT8MAIL_LICENSE_PUBLIC_KEY_ID="$(env_file_value ALT8MAIL_LICENSE_PUBLIC_KEY_ID)"
    PRESERVE_ALT8MAIL_LICENSE_PUBLIC_KEYS="$(env_file_value ALT8MAIL_LICENSE_PUBLIC_KEYS)"
    PRESERVE_ALT8MAIL_LICENSE_TENANT="$(env_file_value ALT8MAIL_LICENSE_TENANT)"
    PRESERVE_ALT8MAIL_LICENSE_PORTAL_URL="$(env_file_value ALT8MAIL_LICENSE_PORTAL_URL)"
    PRESERVE_ALT8MAIL_LICENSE_ALLOW_CONFIG_KEY="$(env_file_value ALT8MAIL_LICENSE_ALLOW_CONFIG_KEY)"
    PRESERVE_ALT8MAIL_LICENSE_USAGE_URL="$(env_file_value ALT8MAIL_LICENSE_USAGE_URL)"
    PRESERVE_ALT8MAIL_LICENSE_USAGE_TOKEN="$(env_file_value ALT8MAIL_LICENSE_USAGE_TOKEN)"
    PRESERVE_ALT8MAIL_LICENSE_USAGE_INTERVAL_HOURS="$(env_file_value ALT8MAIL_LICENSE_USAGE_INTERVAL_HOURS)"
  fi
  PRESERVE_CONTROL_LICENSE_FILE=""
  PRESERVE_CONTROL_LICENSE_PUBLIC_KEY=""
  PRESERVE_CONTROL_LICENSE_PUBLIC_KEY_ID=""
  PRESERVE_CONTROL_LICENSE_PUBLIC_KEYS=""
  PRESERVE_CONTROL_LICENSE_GROUP=""
  PRESERVE_CONTROL_LICENSE_WARNING_PERCENT=""
  PRESERVE_CONTROL_LICENSE_PORTAL_URL=""
  PRESERVE_CONTROL_LICENSE_ALLOW_CONFIG_KEY=""
  PRESERVE_CONTROL_ENTERPRISE_RELEASE_PUBLIC_KEY=""
  if [[ -f "$A8M_CONFIG_ROOT/control.env" ]]; then
    PRESERVE_CONTROL_LICENSE_FILE="$(control_env_value CONTROL_LICENSE_FILE)"
    PRESERVE_CONTROL_LICENSE_PUBLIC_KEY="$(control_env_value CONTROL_LICENSE_PUBLIC_KEY)"
    PRESERVE_CONTROL_LICENSE_PUBLIC_KEY_ID="$(control_env_value CONTROL_LICENSE_PUBLIC_KEY_ID)"
    PRESERVE_CONTROL_LICENSE_PUBLIC_KEYS="$(control_env_value CONTROL_LICENSE_PUBLIC_KEYS)"
    PRESERVE_CONTROL_LICENSE_GROUP="$(control_env_value CONTROL_LICENSE_GROUP)"
    PRESERVE_CONTROL_LICENSE_WARNING_PERCENT="$(control_env_value CONTROL_LICENSE_WARNING_PERCENT)"
    PRESERVE_CONTROL_LICENSE_PORTAL_URL="$(control_env_value CONTROL_LICENSE_PORTAL_URL)"
    PRESERVE_CONTROL_LICENSE_ALLOW_CONFIG_KEY="$(control_env_value CONTROL_LICENSE_ALLOW_CONFIG_KEY)"
    PRESERVE_CONTROL_ENTERPRISE_RELEASE_PUBLIC_KEY="$(control_env_value CONTROL_ENTERPRISE_RELEASE_PUBLIC_KEY)"
  fi

  LICENSE_FILE="$A8M_LICENSE_FILE"
  LICENSE_PUBLIC_KEY="$A8M_LICENSE_PUBLIC_KEY"
  LICENSE_PUBLIC_KEY_ID="$A8M_LICENSE_PUBLIC_KEY_ID"
  LICENSE_PUBLIC_KEYS="$A8M_LICENSE_PUBLIC_KEYS"
  LICENSE_GROUP="$A8M_LICENSE_GROUP"
  LICENSE_TENANT="$A8M_LICENSE_TENANT"
  LICENSE_PORTAL_URL="$A8M_LICENSE_PORTAL_URL"
  ENTERPRISE_RELEASE_PUBLIC_KEY="$A8M_ENTERPRISE_RELEASE_PUBLIC_KEY"

  # BEGIN LICENSE ENV RESOLUTION (executed verbatim by license_install_test.go)
  CONTROL_LICENSE_FILE="${PRESERVE_CONTROL_LICENSE_FILE:-${PRESERVE_ALT8MAIL_LICENSE_FILE:-$LICENSE_FILE}}"
  CONTROL_LICENSE_GROUP="${PRESERVE_CONTROL_LICENSE_GROUP:-$LICENSE_GROUP}"
  CONTROL_LICENSE_PUBLIC_KEY="${LICENSE_PUBLIC_KEY:-${PRESERVE_CONTROL_LICENSE_PUBLIC_KEY:-${PRESERVE_ALT8MAIL_LICENSE_PUBLIC_KEY:-}}}"
  CONTROL_LICENSE_PUBLIC_KEY_ID="${LICENSE_PUBLIC_KEY_ID:-${PRESERVE_CONTROL_LICENSE_PUBLIC_KEY_ID:-${PRESERVE_ALT8MAIL_LICENSE_PUBLIC_KEY_ID:-}}}"
  CONTROL_LICENSE_PUBLIC_KEYS="${LICENSE_PUBLIC_KEYS:-${PRESERVE_CONTROL_LICENSE_PUBLIC_KEYS:-${PRESERVE_ALT8MAIL_LICENSE_PUBLIC_KEYS:-}}}"
  CONTROL_LICENSE_WARNING_PERCENT="${PRESERVE_CONTROL_LICENSE_WARNING_PERCENT:-80}"
  CONTROL_LICENSE_PORTAL_URL="${PRESERVE_CONTROL_LICENSE_PORTAL_URL:-${PRESERVE_ALT8MAIL_LICENSE_PORTAL_URL:-${LICENSE_PORTAL_URL:-}}}"
  CONTROL_LICENSE_ALLOW_CONFIG_KEY="${PRESERVE_CONTROL_LICENSE_ALLOW_CONFIG_KEY:-${PRESERVE_ALT8MAIL_LICENSE_ALLOW_CONFIG_KEY:-false}}"
  CONTROL_ENTERPRISE_RELEASE_PUBLIC_KEY="${ENTERPRISE_RELEASE_PUBLIC_KEY:-${PRESERVE_CONTROL_ENTERPRISE_RELEASE_PUBLIC_KEY:-}}"
  APP_LICENSE_FILE="${PRESERVE_ALT8MAIL_LICENSE_FILE:-$CONTROL_LICENSE_FILE}"
  APP_LICENSE_PUBLIC_KEY="${LICENSE_PUBLIC_KEY:-${PRESERVE_ALT8MAIL_LICENSE_PUBLIC_KEY:-$CONTROL_LICENSE_PUBLIC_KEY}}"
  APP_LICENSE_PUBLIC_KEY_ID="${LICENSE_PUBLIC_KEY_ID:-${PRESERVE_ALT8MAIL_LICENSE_PUBLIC_KEY_ID:-$CONTROL_LICENSE_PUBLIC_KEY_ID}}"
  APP_LICENSE_PUBLIC_KEYS="${PRESERVE_ALT8MAIL_LICENSE_PUBLIC_KEYS:-$CONTROL_LICENSE_PUBLIC_KEYS}"
  APP_LICENSE_TENANT="${LICENSE_TENANT:-${PRESERVE_ALT8MAIL_LICENSE_TENANT:-}}"
  APP_LICENSE_PORTAL_URL="${PRESERVE_ALT8MAIL_LICENSE_PORTAL_URL:-$CONTROL_LICENSE_PORTAL_URL}"
  APP_LICENSE_ALLOW_CONFIG_KEY="${PRESERVE_ALT8MAIL_LICENSE_ALLOW_CONFIG_KEY:-false}"
  # END LICENSE ENV RESOLUTION

  cat >"$A8M_CONFIG_ROOT/app.env" <<EOF
ALT8MAIL_ENV=production
ALT8MAIL_LISTEN=127.0.0.1:8080
ALT8MAIL_PUBLIC_URL=https://$A8M_MAIL_HOSTNAME
ALT8MAIL_MAIL_DOMAIN=$A8M_DOMAIN
ALT8MAIL_MAIL_HOSTNAME=$A8M_MAIL_HOSTNAME
ALT8MAIL_MAILDIR_BASE=/var/vmail
ALT8MAIL_DB_HOST=127.0.0.1
ALT8MAIL_DB_PORT=5432
ALT8MAIL_DB_NAME=a8m
ALT8MAIL_DB_USER=a8m_app
ALT8MAIL_DB_PASSWORD=$APP_DB_PASSWORD
ALT8MAIL_DB_SSLMODE=disable
ALT8MAIL_REDIS_ADDR=127.0.0.1:6379
ALT8MAIL_REDIS_PASSWORD=$REDIS_PASSWORD
ALT8MAIL_SESSION_SECRET=$SESSION_SECRET
ALT8MAIL_CRYPTO_SECRET=$CRYPTO_SECRET
ALT8MAIL_CSRF_SECRET=$CSRF_SECRET
ALT8MAIL_COOKIE_SECURE=true
ALT8MAIL_IMAP_ADDR=127.0.0.1:993
ALT8MAIL_IMAP_MASTER_USER=a8m-worker
ALT8MAIL_IMAP_MASTER_PASSWORD_FILE=$A8M_SECRETS_DIR/imap_master_password
ALT8MAIL_IMAP_TLS_NAME=$A8M_MAIL_HOSTNAME
ALT8MAIL_SMTP_ADDR=127.0.0.1:587
ALT8MAIL_SMTP_TLS_NAME=$A8M_MAIL_HOSTNAME
ALT8MAIL_MAIL_CA_FILE=/etc/ssl/a8m/mail.crt
ALT8MAIL_MANAGESIEVE_ADDR=127.0.0.1:4190
ALT8MAIL_MANAGESIEVE_TLS_NAME=$A8M_MAIL_HOSTNAME
ALT8MAIL_WEBAUTHN_RPID=$A8M_MAIL_HOSTNAME
ALT8MAIL_WEBAUTHN_ORIGINS=https://$A8M_MAIL_HOSTNAME,https://$A8M_DOMAIN
ALT8MAIL_ADMIN_LISTEN=127.0.0.1:8081
ALT8MAIL_ADMIN_DIR=$A8M_INSTALL_ROOT/current/admin
ALT8MAIL_ADMIN_URL=https://$A8M_MAIL_HOSTNAME/admin/
ALT8MAIL_ADMIN_SESSION_HOURS=8
ALT8MAIL_DKIM_DIR=/var/lib/rspamd/dkim
ALT8MAIL_DKIM_SELECTOR_MAP=/var/lib/rspamd/dkim/selectors.map
ALT8MAIL_LOCAL_DOMAINS_MAP=/var/lib/rspamd/local_domains.map
ALT8MAIL_CLAMAV_ADDR=/var/run/clamav/clamd.ctl
ALT8MAIL_IMPORT_SCAN=true
ALT8MAIL_IMPORT_SCAN_MAX_MB=24
ALT8MAIL_UPLOAD_DIR=$A8M_INSTALL_ROOT/uploads
ALT8MAIL_MAIL_WORKER_CONCURRENCY=8
ALT8MAIL_IMAP_SYNC_SECONDS=300
ALT8MAIL_IMAP_POLL_SECONDS=15
ALT8MAIL_IMAP_IDLE=true
ALT8MAIL_ORPHAN_SWEEP_SECONDS=3600
ALT8MAIL_ORPHAN_GRACE_SECONDS=300
ALT8MAIL_DEMO_MODE=false
ALT8MAIL_OFFICE_JWT_SECRET_FILE=/etc/a8m/office-jwt-secret
EOF

  restore_env_file_value ALT8MAIL_ADDON_RELAY_URL "$p_addon_url"
  restore_env_file_value ALT8MAIL_ADDON_RELAY_SECRET "$p_addon_secret"
  restore_env_file_value ALT8MAIL_ADDON_GATEWAY_URL "$p_addon_gateway"
  restore_env_file_value ALT8MAIL_LICENSE_FILE "$APP_LICENSE_FILE"
  restore_env_file_value ALT8MAIL_LICENSE_PUBLIC_KEY "$APP_LICENSE_PUBLIC_KEY"
  restore_env_file_value ALT8MAIL_LICENSE_PUBLIC_KEY_ID "$APP_LICENSE_PUBLIC_KEY_ID"
  restore_env_file_value ALT8MAIL_LICENSE_PUBLIC_KEYS "$APP_LICENSE_PUBLIC_KEYS"
  restore_env_file_value ALT8MAIL_LICENSE_TENANT "$APP_LICENSE_TENANT"
  restore_env_file_value ALT8MAIL_LICENSE_PORTAL_URL "$APP_LICENSE_PORTAL_URL"
  restore_env_file_value ALT8MAIL_LICENSE_ALLOW_CONFIG_KEY "$APP_LICENSE_ALLOW_CONFIG_KEY"
  restore_env_file_value ALT8MAIL_LICENSE_USAGE_URL "$p_app_usage_url"
  restore_env_file_value ALT8MAIL_LICENSE_USAGE_TOKEN "$p_app_usage_token"
  restore_env_file_value ALT8MAIL_LICENSE_USAGE_INTERVAL_HOURS "$p_app_usage_hours"
  chown root:"$A8M_SERVICE_USER" "$A8M_CONFIG_ROOT/app.env"
  chmod 0640 "$A8M_CONFIG_ROOT/app.env"

  cat >"$A8M_CONFIG_ROOT/control.env" <<EOF
CONTROL_ENV=production
CONTROL_LISTEN=0.0.0.0:8808
CONTROL_DATABASE_URL=postgres://alt8mail_control:$CONTROL_DB_PASSWORD@127.0.0.1:5432/a8m?sslmode=disable
CONTROL_SESSION_SECRET=$CONTROL_SESSION_SECRET
CONTROL_CSRF_SECRET=$CONTROL_CSRF_SECRET
CONTROL_SECRET_KEY=$CONTROL_SECRET_KEY
CONTROL_PUBLIC_URL=https://$A8M_MAIL_HOSTNAME:8808
CONTROL_WEBMAIL_URL=https://$A8M_MAIL_HOSTNAME
CONTROL_MAIL_HOSTNAME=$A8M_MAIL_HOSTNAME
CONTROL_DNS_RESOLVER=1.1.1.1:53
CONTROL_MAIL_LOG=/var/log/mail.log
CONTROL_VMAIL_ROOT=/var/vmail
CONTROL_STORAGE_ROOT=/srv/a8m-storage
CONTROL_STORAGE_RUNNER=/opt/a8m/current/bin/a8m-storage-runner
CONTROL_DKIM_DIR=/var/lib/rspamd/dkim
CONTROL_POSTFIX_DIR=/etc/postfix
CONTROL_DOVECOT_DIR=/etc/dovecot
CONTROL_SSL_DIR=/etc/ssl/alt8mail
CONTROL_TLS_CERT=/etc/ssl/alt8mail/control.crt
CONTROL_TLS_KEY=/etc/ssl/alt8mail/control.key
CONTROL_BACKUP_DIR=/var/backups/alt8mail
CONTROL_CRON_FILE=/etc/cron.d/alt8mail
CONTROL_CERTBOT_DIR=/etc/letsencrypt/live
CONTROL_CERTBOT_WEBROOT=/var/www/certbot
CONTROL_SMTP_ADDR=127.0.0.1:25
CONTROL_POLICY_LISTEN=127.0.0.1:10040
CONTROL_REDIS_ADDR=127.0.0.1:6379
CONTROL_REDIS_PASSWORD_FILE=$A8M_SECRETS_DIR/redis_password
CONTROL_OFFICE_AGENT_URL=http://127.0.0.1:8098
CONTROL_OFFICE_AGENT_TOKEN_FILE=/etc/a8m/office-agent-token
CONTROL_OFFICE_IMAGE=onlyoffice/documentserver:9.4.0.1
CONTROL_OFFICE_JWT_SECRET_FILE=/etc/a8m/office-jwt-secret
CONTROL_RETENTION_DAYS=365
CONTROL_TRUSTED_PROXY=false
CONTROL_LICENSE_FILE=$CONTROL_LICENSE_FILE
CONTROL_LICENSE_GROUP=$CONTROL_LICENSE_GROUP
CONTROL_LICENSE_PUBLIC_KEY=$CONTROL_LICENSE_PUBLIC_KEY
CONTROL_LICENSE_PUBLIC_KEY_ID=$CONTROL_LICENSE_PUBLIC_KEY_ID
CONTROL_LICENSE_PUBLIC_KEYS=$CONTROL_LICENSE_PUBLIC_KEYS
CONTROL_LICENSE_WARNING_PERCENT=$CONTROL_LICENSE_WARNING_PERCENT
CONTROL_LICENSE_PORTAL_URL=$CONTROL_LICENSE_PORTAL_URL
CONTROL_LICENSE_ALLOW_CONFIG_KEY=$CONTROL_LICENSE_ALLOW_CONFIG_KEY
CONTROL_ENTERPRISE_RELEASE_PUBLIC_KEY=$CONTROL_ENTERPRISE_RELEASE_PUBLIC_KEY
EOF
  chown root:root "$A8M_CONFIG_ROOT/control.env"
  chmod 0600 "$A8M_CONFIG_ROOT/control.env"

  cat >"$A8M_CONFIG_ROOT/update.conf" <<EOF
A8M_REPO=git@github.com:CryptRoute/Alt8Mail.git
A8M_REPO_HTTPS=https://github.com/CryptRoute/Alt8Mail.git
A8M_GIT_KEY=/etc/a8m/github_readonly
A8M_CHANNEL=stable
A8M_SERVICES="a8m-server a8m-admin alt8mail-control a8m-office-agent"
A8M_RELEASE_BASE_URL=https://github.com/CryptRoute/Alt8Mail/releases/download
A8M_PREFER_RELEASE_ASSET=1
A8M_MIN_FREE_MB=1024
EOF
  chmod 0640 "$A8M_CONFIG_ROOT/update.conf"
  chown root:"$A8M_SERVICE_USER" "$A8M_CONFIG_ROOT/update.conf"

  if [[ ! -s /etc/a8m/github_readonly ]]; then
    ssh-keygen -q -t ed25519 -N '' -C "a8m-readonly-$(hostname)" -f /etc/a8m/github_readonly
    chmod 0600 /etc/a8m/github_readonly
  fi

  if [[ -f "$CONTROL_LICENSE_FILE" ]]; then
    if chown root:"$CONTROL_LICENSE_GROUP" "$CONTROL_LICENSE_FILE" 2>/dev/null; then
      chmod 0640 "$CONTROL_LICENSE_FILE"
    else
      ui_warn "Could not set group $CONTROL_LICENSE_GROUP on $CONTROL_LICENSE_FILE"
    fi
  fi

  install -d -m 0750 "$A8M_CONFIG_ROOT/office"
  if [[ ! -f "$A8M_CONFIG_ROOT/office-agent.env" ]]; then
    cat >"$A8M_CONFIG_ROOT/office-agent.env" <<'EOF'
OFFICE_AGENT_LISTEN=127.0.0.1:8098
OFFICE_AGENT_TOKEN_FILE=/etc/a8m/office-agent-token
OFFICE_AGENT_COMPOSE_FILE=/etc/a8m/office/docker-compose.yml
OFFICE_AGENT_SECRET_FILE=/etc/a8m/office-jwt-secret
OFFICE_AGENT_STATE_FILE=/var/lib/a8m-office/state.json
OFFICE_AGENT_IMAGE=onlyoffice/documentserver:9.4.0.1
OFFICE_AGENT_CONTAINER=a8m-office
OFFICE_AGENT_HTTP_PORT=8082
OFFICE_AGENT_NGINX_SITE=/etc/nginx/sites-available/a8m-office
OFFICE_AGENT_NGINX_ENABLED=/etc/nginx/sites-enabled/a8m-office
OFFICE_AGENT_CERTBOT=certbot
OFFICE_AGENT_CERTBOT_DIR=/etc/letsencrypt/live
OFFICE_AGENT_CERTBOT_WEBROOT=/var/www/certbot
OFFICE_AGENT_FONTS_DIR=/var/lib/a8m-office/fonts
EOF
  fi
  if [[ ! -s "$A8M_CONFIG_ROOT/office-agent-token" ]]; then
    rand_hex 48 >"$A8M_CONFIG_ROOT/office-agent-token"
  fi
  chown root:"$A8M_SERVICE_USER" "$A8M_CONFIG_ROOT/office-agent.env" "$A8M_CONFIG_ROOT/office-agent-token"
  chmod 0640 "$A8M_CONFIG_ROOT/office-agent.env" "$A8M_CONFIG_ROOT/office-agent-token"

  export CONTROL_LICENSE_FILE CONTROL_LICENSE_GROUP CONTROL_LICENSE_PUBLIC_KEY
  export CONTROL_LICENSE_PUBLIC_KEY_ID CONTROL_LICENSE_PUBLIC_KEYS
  export CONTROL_LICENSE_WARNING_PERCENT CONTROL_LICENSE_PORTAL_URL
  export CONTROL_LICENSE_ALLOW_CONFIG_KEY CONTROL_ENTERPRISE_RELEASE_PUBLIC_KEY
  export APP_LICENSE_FILE APP_LICENSE_PUBLIC_KEY APP_LICENSE_PUBLIC_KEY_ID
  export APP_LICENSE_PUBLIC_KEYS APP_LICENSE_TENANT APP_LICENSE_PORTAL_URL
  export APP_LICENSE_ALLOW_CONFIG_KEY
  ui_task_ok "Mail services and application environment configured"
}
