#!/usr/bin/env bash
# Alt8Mail uninstaller — removes the application, keeps mail and PostgreSQL data.
set -Eeuo pipefail
umask 027

[[ "$EUID" -eq 0 ]] || { echo "Run as root"; exit 1; }

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$HERE/installer/lib/ui.sh"
source "$HERE/installer/lib/log.sh"
source "$HERE/installer/lib/common.sh"

A8M_LOG_DIR="${A8M_LOG_DIR:-/var/log/a8m}"
log_init

ui_yesno "Uninstall Alt8Mail" "This stops and removes the Alt8Mail application services,
drops the a8m database and removes /etc/a8m, /opt/a8m and /opt/alt8mail.

Mail data in /var/vmail, storage in /srv/a8m-storage, the PostgreSQL data
directory and backups are NOT deleted. Uploads are moved to
/var/backups/alt8mail/uploads-<timestamp>.

Continue?" || exit 0

# Stop application units and provisioning timers.
for svc in a8m-server a8m-admin alt8mail-control a8m-office-agent a8m-addon-relay \
  a8m-mailprovision.timer a8m-dkimprovision.timer \
  a8m-mailprovision.service a8m-dkimprovision.service; do
  systemctl disable --now "$svc" 2>/dev/null || true
done

# Remove Alt8Mail-owned units without touching system mail-stack units.
rm -f /etc/systemd/system/a8m-server.service \
      /etc/systemd/system/a8m-admin.service \
      /etc/systemd/system/alt8mail-control.service \
      /etc/systemd/system/a8m-mailprovision.service \
      /etc/systemd/system/a8m-mailprovision.timer \
      /etc/systemd/system/a8m-dkimprovision.service \
      /etc/systemd/system/a8m-dkimprovision.timer \
      /etc/systemd/system/a8m-office-agent.service \
      /etc/systemd/system/a8m-addon-relay.service
for dropin in /etc/systemd/system/postgresql@*-main.service.d/50-alt8mail-enable.conf; do
  [[ -e "$dropin" ]] || continue
  rm -f "$dropin"
  rmdir "$(dirname "$dropin")" 2>/dev/null || true
done
systemctl daemon-reload || true

# Drop the application database and roles.
if runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_database WHERE datname='a8m'" 2>/dev/null | grep -qx 1; then
  runuser -u postgres -- psql -d postgres -v ON_ERROR_STOP=0 -q <<'SQL' || true
SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = 'a8m' AND pid <> pg_backend_pid();
DROP DATABASE IF EXISTS a8m;
SQL
fi
runuser -u postgres -- psql -d postgres -v ON_ERROR_STOP=0 -q <<'SQL' || true
DROP ROLE IF EXISTS a8m_app;
DROP ROLE IF EXISTS a8m_mail;
DROP ROLE IF EXISTS alt8mail_control;
SQL

# Preserve uploaded files before removing the application tree.
if [[ -d /opt/a8m/uploads ]] && [[ -n "$(ls -A /opt/a8m/uploads 2>/dev/null)" ]]; then
  install -d -m 0700 /var/backups/alt8mail
  local_stamp="$(date -u +%Y%m%d%H%M%S)"
  mv /opt/a8m/uploads "/var/backups/alt8mail/uploads-${local_stamp}"
fi

# Remove nginx sites and reload if nginx is still running.
rm -f /etc/nginx/sites-enabled/a8m /etc/nginx/sites-available/a8m
rm -f /etc/nginx/sites-enabled/a8m-office /etc/nginx/sites-available/a8m-office
if systemctl is-active --quiet nginx 2>/dev/null; then
  nginx -t >/dev/null 2>&1 && systemctl reload nginx >/dev/null 2>&1 || true
fi

# Remove Fail2Ban and sysctl fragments owned by Alt8Mail.
rm -f /etc/fail2ban/jail.d/a8m.local /etc/fail2ban/action.d/a8m-ufw-port.conf
if systemctl is-active --quiet fail2ban 2>/dev/null; then
  systemctl restart fail2ban >/dev/null 2>&1 || true
fi
rm -f /etc/sysctl.d/99-alt8mail-mail.conf
sysctl --system >/dev/null 2>&1 || true

# Redis include and TLS renewal hook.
sed -i '\|^include /etc/redis/a8m.conf$|d' /etc/redis/redis.conf 2>/dev/null || true
rm -f /etc/redis/a8m.conf
rm -f /etc/letsencrypt/renewal-hooks/deploy/a8m-reload.sh
rm -f /usr/local/bin/alt8mail-update
rm -f /root/alt8mail-initial-credentials.txt /root/alt8mail-dns-records.txt

# Remove application data and configuration. Mail, storage, backups and logs stay.
rm -rf /etc/a8m /opt/a8m /opt/alt8mail /var/www/a8m /var/lib/a8m /var/lib/a8m-office
# Keep /etc/ssl/a8m and /etc/ssl/alt8mail: the remaining mail stack
# references these certificates and they are harmless without Alt8Mail.

# Remove the service account; keep vmail and its mail data.
userdel "$A8M_SERVICE_USER" 2>/dev/null || true
groupdel "$A8M_SERVICE_USER" 2>/dev/null || true

ui_ok "Alt8Mail removed. Mail data, /srv/a8m-storage, certificates, backups and $A8M_LOG_DIR were preserved."
ui_info "The Postfix/Dovecot/Rspamd packages and their Alt8Mail configuration remain; remove them manually if this host is being retired."
