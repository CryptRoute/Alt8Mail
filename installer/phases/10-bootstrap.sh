#!/usr/bin/env bash
set -Eeuo pipefail

phase_bootstrap() {
  export DEBIAN_FRONTEND=noninteractive
  export NEEDRESTART_MODE=a NEEDRESTART_SUSPEND=1

  if [[ "${A8M_SKIP_PACKAGES:-0}" != "1" ]]; then
    run_spin bootstrap "Refreshing package indexes" -- \
      apt-get -o DPkg::Lock::Timeout=300 update -y || return 1
    # shellcheck disable=SC2046
    run_spin bootstrap "Installing base packages" -- \
      apt-get -o DPkg::Lock::Timeout=300 \
      -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold \
      install -y --no-install-recommends $(distro_base_pkgs) || return 1
  else
    ui_warn "Skipping apt package installation by request"
  fi

  run bootstrap -- timedatectl set-timezone UTC || ui_warn "Could not set timezone to UTC"
  apply_install_hostname "$A8M_MAIL_HOSTNAME" "$A8M_DOMAIN"
  printf '%s\n' "$A8M_DOMAIN" >/etc/mailname
  install -d -m 0755 /var/www/certbot /etc/letsencrypt /var/lib/letsencrypt
  ui_task_ok "Host identity configured"
}
