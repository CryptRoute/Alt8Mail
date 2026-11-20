#!/usr/bin/env bash
set -Eeuo pipefail

phase_mailstack() {
  if [[ "${A8M_SKIP_PACKAGES:-0}" == "1" ]]; then
    ui_warn "Skipping mail-stack package installation by request"
    return 0
  fi
  export DEBIAN_FRONTEND=noninteractive
  export NEEDRESTART_MODE=a NEEDRESTART_SUSPEND=1

  # Non-interactive postfix debconf
  echo "postfix postfix/main_mailer_type select Internet Site" | debconf-set-selections
  echo "postfix postfix/mailname string ${A8M_MAIL_HOSTNAME}" | debconf-set-selections

  ui_task "Installing Postfix, Dovecot, Rspamd, ClamAV, PostgreSQL, Redis and nginx"
  # shellcheck disable=SC2046
  run_stream mailstack -- apt-get -o DPkg::Lock::Timeout=300 \
    -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold \
    install -y --no-install-recommends $(distro_mailstack_pkgs) || return 1
  ui_task_ok "Mail stack packages installed"
}
