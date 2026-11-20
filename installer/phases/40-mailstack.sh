#!/usr/bin/env bash
set -Eeuo pipefail

phase_mailstack() {
  ui_step "Installing mail stack (Postfix, Dovecot, Rspamd, ClamAV, PostgreSQL, Redis)"
  if [[ "${A8M_SKIP_PACKAGES:-0}" == "1" ]]; then
    ui_warn "Skipping mail-stack package installation by request"
    return 0
  fi
  export DEBIAN_FRONTEND=noninteractive

  # Non-interactive postfix debconf
  echo "postfix postfix/main_mailer_type select Internet Site" | debconf-set-selections
  echo "postfix postfix/mailname string ${A8M_MAIL_HOSTNAME}" | debconf-set-selections

  # shellcheck disable=SC2046
  run mailstack -- apt-get install -y --no-install-recommends $(distro_mailstack_pkgs) || return 1
  ui_ok "Mail stack packages installed"
}
