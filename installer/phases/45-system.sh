#!/usr/bin/env bash
set -Eeuo pipefail

phase_system() {
  umask 027
  ui_task "Creating the a8m and vmail service users"

  if ! getent group "$A8M_SERVICE_USER" >/dev/null; then
    groupadd --system "$A8M_SERVICE_USER"
  fi
  if ! id "$A8M_SERVICE_USER" >/dev/null 2>&1; then
    useradd --system --home-dir "$A8M_INSTALL_ROOT" \
      --shell /usr/sbin/nologin --gid "$A8M_SERVICE_USER" "$A8M_SERVICE_USER"
  fi
  if ! getent group vmail >/dev/null; then
    groupadd --system --gid 5000 vmail
  fi
  if ! id vmail >/dev/null 2>&1; then
    useradd --system --uid 5000 --gid 5000 --home-dir /var/vmail \
      --shell /usr/sbin/nologin vmail
  fi

  install -d -m 0755 -o "$A8M_SERVICE_USER" -g "$A8M_SERVICE_USER" "$A8M_INSTALL_ROOT"
  chmod 0755 "$A8M_INSTALL_ROOT"
  install -d -m 0750 -o "$A8M_SERVICE_USER" -g "$A8M_SERVICE_USER" \
    "$A8M_SECRETS_DIR" "$A8M_INSTALL_ROOT/uploads"
  install -d -m 0755 "$A8M_RELEASES_DIR" "$A8M_CONFIG_ROOT" \
    "$A8M_CONTROL_ROOT" "$A8M_CONTROL_ROOT/bin"
  install -d -m 0700 /var/backups/alt8mail /etc/ssl/alt8mail /etc/cron.d
  install -d -m 0755 /etc/ssl/a8m /var/www/certbot /etc/letsencrypt /var/lib/letsencrypt
  install -d -m 0750 -o vmail -g vmail /var/vmail
  install -d -m 0750 -o root -g vmail /srv/a8m-storage
  install -d -m 0750 /var/lib/a8m
  install -d -m 0750 -o root -g "$A8M_SERVICE_USER" /var/lib/a8m-office
  install -d -m 0750 "$A8M_CONFIG_ROOT/office"

  ui_task "Generating and normalizing service secrets"
  APP_DB_PASSWORD="$(read_or_create_secret "$A8M_SECRETS_DIR/postgres_app_password" 32)"
  MAIL_DB_PASSWORD="$(read_or_create_secret "$A8M_SECRETS_DIR/postgres_mail_password" 32)"
  CONTROL_DB_PASSWORD="$(read_or_create_secret "$A8M_SECRETS_DIR/postgres_control_password" 32)"
  REDIS_PASSWORD="$(read_or_create_secret "$A8M_SECRETS_DIR/redis_password" 32)"
  SESSION_SECRET="$(read_or_create_secret "$A8M_SECRETS_DIR/session_secret" 32)"
  CRYPTO_SECRET="$(read_or_create_secret "$A8M_SECRETS_DIR/crypto_secret" 32)"
  CSRF_SECRET="$(read_or_create_secret "$A8M_SECRETS_DIR/csrf_secret" 32)"
  CONTROL_SESSION_SECRET="$(read_or_create_secret "$A8M_SECRETS_DIR/control_session_secret" 32)"
  CONTROL_CSRF_SECRET="$(read_or_create_secret "$A8M_SECRETS_DIR/control_csrf_secret" 32)"
  CONTROL_SECRET_KEY="$(read_or_create_secret "$A8M_SECRETS_DIR/control_secret_key" 32)"
  IMAP_MASTER_PASSWORD="$(read_or_create_secret "$A8M_SECRETS_DIR/imap_master_password" 32)"
  OWNER_WEB_PASSWORD="$(read_or_create_secret "$A8M_SECRETS_DIR/owner_web_password" 24)"
  OWNER_MAIL_PASSWORD="$(read_or_create_secret "$A8M_SECRETS_DIR/owner_mail_password" 32)"
  CONTROL_ADMIN_PASSWORD="$(read_or_create_secret "$A8M_SECRETS_DIR/control_admin_password" 24)"

  chown -R "$A8M_SERVICE_USER:$A8M_SERVICE_USER" "$A8M_SECRETS_DIR"
  chmod 0700 "$A8M_SECRETS_DIR"
  find "$A8M_SECRETS_DIR" -type f -exec chmod 0600 {} \;

  ui_task "Detecting the public IPv4 address and PostgreSQL version"
  A8M_PUBLIC_IP="$(detect_public_ip)"
  A8M_PUBLIC_IPV6="$(detect_public_ipv6)"
  if [[ -z "$A8M_PUBLIC_IP" ]]; then
    ui_warn "Could not determine the public IPv4 address; TLS will use a self-signed certificate"
  fi
  A8M_PG_MAJOR="$(detect_pg_major)"
  if [[ -z "$A8M_PG_MAJOR" ]]; then
    ui_err "Could not determine the PostgreSQL major version"
    return 1
  fi
  export APP_DB_PASSWORD MAIL_DB_PASSWORD CONTROL_DB_PASSWORD REDIS_PASSWORD
  export SESSION_SECRET CRYPTO_SECRET CSRF_SECRET CONTROL_SESSION_SECRET
  export CONTROL_CSRF_SECRET CONTROL_SECRET_KEY IMAP_MASTER_PASSWORD
  export OWNER_WEB_PASSWORD OWNER_MAIL_PASSWORD CONTROL_ADMIN_PASSWORD
  export A8M_PUBLIC_IP A8M_PUBLIC_IPV6 A8M_PG_MAJOR
  ui_task_ok "Users, directories and secrets ready (PostgreSQL ${A8M_PG_MAJOR})"
}
