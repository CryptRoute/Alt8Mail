#!/usr/bin/env bash
set -Eeuo pipefail

phase_bootstrap() {
  ui_step "Installing base packages"
  export DEBIAN_FRONTEND=noninteractive

  if [[ "${A8M_SKIP_PACKAGES:-0}" != "1" ]]; then
    run bootstrap -- apt-get update -y
    # shellcheck disable=SC2046
    run bootstrap -- apt-get install -y --no-install-recommends $(distro_base_pkgs)
  else
    ui_warn "Skipping apt package installation by request"
  fi

  run bootstrap -- timedatectl set-timezone UTC || ui_warn "Could not set timezone to UTC"
  run bootstrap -- hostnamectl set-hostname "$A8M_MAIL_HOSTNAME" \
    || ui_warn "Could not set the system hostname"
  if ! grep -qE "^127\\.0\\.1\\.1[[:space:]]+$A8M_MAIL_HOSTNAME([[:space:]]|$)" /etc/hosts; then
    sed -i '/^127\.0\.1\.1[[:space:]]/d' /etc/hosts
    printf '127.0.1.1 %s %s\n' "$A8M_MAIL_HOSTNAME" "$A8M_DOMAIN" >>/etc/hosts
  fi
  printf '%s\n' "$A8M_DOMAIN" >/etc/mailname
  install -d -m 0755 /var/www/certbot /etc/letsencrypt /var/lib/letsencrypt
  ui_ok "Base packages and host identity configured"
}
