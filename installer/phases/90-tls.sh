#!/usr/bin/env bash
set -Eeuo pipefail

A8M_TLS_READY=0
A8M_LE_LIVE=""

a8m_copy_letsencrypt_cert() {
  install -d -m 0755 /etc/ssl/a8m /etc/ssl/alt8mail
  install -m 0644 "$A8M_LE_LIVE/fullchain.pem" /etc/ssl/a8m/mail.crt || return 1
  install -m 0640 "$A8M_LE_LIVE/privkey.pem" /etc/ssl/a8m/mail.key || return 1
  chown root:postfix /etc/ssl/a8m/mail.key
  A8M_TLS_READY=1
}

a8m_le_cert_usable() {
  [[ -s "$A8M_LE_LIVE/fullchain.pem" && -s "$A8M_LE_LIVE/privkey.pem" ]] || return 1
  openssl x509 -checkend 86400 -noout -in "$A8M_LE_LIVE/fullchain.pem" >/dev/null 2>&1 || return 1
  openssl x509 -checkhost "$A8M_MAIL_HOSTNAME" -noout -in "$A8M_LE_LIVE/fullchain.pem" >/dev/null 2>&1 || return 1
  openssl x509 -checkhost "$A8M_ADMIN_HOSTNAME" -noout -in "$A8M_LE_LIVE/fullchain.pem" >/dev/null 2>&1 || return 1
}

a8m_name_points_here() {
  local name="$1" records ip
  records="$(dig +short +time=3 +tries=2 A "$name" 2>/dev/null | awk '/^[0-9.]+$/')"
  records+=$'\n'
  records+="$(dig +short +time=3 +tries=2 AAAA "$name" 2>/dev/null | awk '/:/')"
  for ip in "$A8M_PUBLIC_IP" "$A8M_PUBLIC_IPV6"; do
    [[ -n "$ip" ]] || continue
    grep -Fxq "$ip" <<<"$records" && return 0
  done
  return 1
}

a8m_write_http_nginx() {
  cat >/etc/nginx/sites-available/a8m <<EOF
server {
    listen 80;
    listen [::]:80;
    server_name $A8M_MAIL_HOSTNAME $A8M_ADMIN_HOSTNAME $A8M_DOMAIN ${A8M_PUBLIC_IP:-127.0.0.1} localhost;

    location /.well-known/acme-challenge/ { root /var/www/certbot; }
    location / { return 301 https://\$host\$request_uri; }
}
EOF
  ln -sfn /etc/nginx/sites-available/a8m /etc/nginx/sites-enabled/a8m
  rm -f /etc/nginx/sites-enabled/default
  if ! grep -qE 'application/javascript[[:space:]]+.*mjs' /etc/nginx/mime.types; then
    sed -i '0,/application\/javascript[[:space:]]\+js;/s//application\/javascript js mjs;/' /etc/nginx/mime.types
  fi
  nginx -t
}

a8m_render_https_nginx() {
  sed -e "s|@MAIL_HOSTNAME@|$A8M_MAIL_HOSTNAME|g" \
      -e "s|@DOMAIN@|$A8M_DOMAIN|g" \
      -e "s|@ADMIN_HOSTNAME@|$A8M_ADMIN_HOSTNAME|g" \
      -e "s|@SERVER_IP@|${A8M_PUBLIC_IP:-127.0.0.1}|g" \
      -e "s|@WEB_ROOT@|$A8M_WEB_ROOT|g" \
      -e "s|@TLS_CERT@|/etc/ssl/a8m/mail.crt|g" \
      -e "s|@TLS_KEY@|/etc/ssl/a8m/mail.key|g" \
      "$A8M_SRC_DIR/deploy/nginx/a8m-site.conf" >/etc/nginx/sites-available/a8m
  ln -sfn /etc/nginx/sites-available/a8m /etc/nginx/sites-enabled/a8m
  nginx -t
}

phase_tls() {
  A8M_LE_LIVE="/etc/letsencrypt/live/$A8M_MAIL_HOSTNAME"
  install -d -m 0755 /var/www/certbot /etc/letsencrypt /etc/letsencrypt/renewal-hooks/deploy

  local tls_attempt=0
  if [[ "$A8M_TLS_MODE" == "letsencrypt" && "$A8M_SKIP_CERTBOT" -eq 0 ]] && a8m_le_cert_usable; then
    ui_info "Reusing the existing Let's Encrypt certificate"
    a8m_copy_letsencrypt_cert || return 1
  fi
  if [[ "$A8M_TLS_MODE" == "letsencrypt" && "$A8M_SKIP_CERTBOT" -eq 0 && "$A8M_TLS_READY" -eq 0 ]]; then
    # The mandatory preflight DNS check has already confirmed A/AAAA records
    # for the domain, mail hostname and admin hostname.
    tls_attempt=1
  fi

  ui_task "Writing the HTTP ACME challenge site"
  a8m_write_http_nginx || return 1
  run tls -- systemctl enable --now nginx || return 1
  run tls -- systemctl reload nginx || return 1

  if [[ "$tls_attempt" -eq 1 ]]; then
    local cert_names=(-d "$A8M_MAIL_HOSTNAME" -d "$A8M_ADMIN_HOSTNAME")
    if a8m_name_points_here "$A8M_DOMAIN"; then
      # The primary domain often hosts a separate website. Include it only
      # when its DNS actually points here.
      cert_names+=(-d "$A8M_DOMAIN")
    fi
    if run_spin tls "Requesting a Let's Encrypt certificate" -- \
        certbot certonly --webroot -w /var/www/certbot \
        "${cert_names[@]}" \
        --cert-name "$A8M_MAIL_HOSTNAME" --expand \
        --email "$A8M_TLS_EMAIL" --agree-tos --no-eff-email --non-interactive; then
      a8m_copy_letsencrypt_cert || return 1
    else
      ui_warn "Let's Encrypt failed"
      if a8m_le_cert_usable; then
        a8m_copy_letsencrypt_cert || return 1
      fi
    fi
  fi

  if [[ "$A8M_TLS_READY" -eq 0 ]]; then
    ui_task "Generating a self-signed certificate"
    local san="DNS:$A8M_MAIL_HOSTNAME,DNS:$A8M_ADMIN_HOSTNAME,DNS:$A8M_DOMAIN,DNS:localhost,IP:127.0.0.1"
    [[ -n "$A8M_PUBLIC_IP" ]] && san="$san,IP:$A8M_PUBLIC_IP"
    [[ -n "$A8M_PUBLIC_IPV6" ]] && san="$san,IP:$A8M_PUBLIC_IPV6"
    install -d -m 0755 /etc/ssl/a8m /etc/ssl/alt8mail
    openssl req -x509 -nodes -newkey rsa:3072 -sha256 -days 825 \
      -subj "/CN=$A8M_MAIL_HOSTNAME/O=Alt8Mail" \
      -addext "subjectAltName=$san" \
      -keyout /etc/ssl/a8m/mail.key -out /etc/ssl/a8m/mail.crt >/dev/null 2>&1 || return 1
    chmod 0644 /etc/ssl/a8m/mail.crt
    chmod 0640 /etc/ssl/a8m/mail.key
    chown root:postfix /etc/ssl/a8m/mail.key
  fi

  install -m 0644 /etc/ssl/a8m/mail.crt /etc/ssl/alt8mail/control.crt
  install -m 0600 /etc/ssl/a8m/mail.key /etc/ssl/alt8mail/control.key
  install -d -m 0755 /etc/dovecot/private
  install -m 0640 -o root -g dovecot /etc/ssl/a8m/mail.crt /etc/dovecot/private/a8m-mail.crt
  install -m 0640 -o root -g dovecot /etc/ssl/a8m/mail.key /etc/dovecot/private/a8m-mail.key

  a8m_render_https_nginx || return 1
  run tls -- systemctl reload nginx || return 1

  if [[ "$A8M_TLS_MODE" == "letsencrypt" ]]; then
    cat >/etc/letsencrypt/renewal-hooks/deploy/a8m-reload.sh <<'HOOK'
#!/bin/sh
set -eu
install -m 0644 /etc/letsencrypt/live/__MAIL_HOSTNAME__/fullchain.pem /etc/ssl/a8m/mail.crt
install -m 0640 /etc/letsencrypt/live/__MAIL_HOSTNAME__/privkey.pem /etc/ssl/a8m/mail.key
chown root:postfix /etc/ssl/a8m/mail.key
install -m 0644 /etc/ssl/a8m/mail.crt /etc/dovecot/private/a8m-mail.crt
install -m 0640 /etc/ssl/a8m/mail.key /etc/dovecot/private/a8m-mail.key
chown root:dovecot /etc/dovecot/private/a8m-mail.crt /etc/dovecot/private/a8m-mail.key
install -m 0644 /etc/ssl/a8m/mail.crt /etc/ssl/alt8mail/control.crt
install -m 0600 /etc/ssl/a8m/mail.key /etc/ssl/alt8mail/control.key
systemctl reload nginx >/dev/null 2>&1 || true
systemctl reload postfix >/dev/null 2>&1 || true
systemctl reload dovecot >/dev/null 2>&1 || true
systemctl restart alt8mail-control >/dev/null 2>&1 || true
HOOK
    sed -i "s|__MAIL_HOSTNAME__|$A8M_MAIL_HOSTNAME|g" /etc/letsencrypt/renewal-hooks/deploy/a8m-reload.sh
    chmod 0755 /etc/letsencrypt/renewal-hooks/deploy/a8m-reload.sh
    systemctl enable --now certbot.timer >/dev/null 2>&1 || true
  fi
  ui_ok "TLS certificate installed"
}
