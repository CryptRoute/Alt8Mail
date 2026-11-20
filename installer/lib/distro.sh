#!/usr/bin/env bash
set -Eeuo pipefail

A8M_OS_ID=""; A8M_OS_VER=""; A8M_OS_CODENAME=""; A8M_OS_FAMILY=""

distro_detect() {
  [[ -r /etc/os-release ]] || { ui_err "/etc/os-release missing"; exit 1; }
  . /etc/os-release
  A8M_OS_ID="${ID:-}"; A8M_OS_VER="${VERSION_ID:-}"; A8M_OS_CODENAME="${VERSION_CODENAME:-}"
  case "$A8M_OS_ID" in
    debian) A8M_OS_FAMILY=debian ;;
    ubuntu) A8M_OS_FAMILY=ubuntu ;;
    *) ui_err "Unsupported distribution '$A8M_OS_ID'. Supported: Debian 12, Ubuntu 22.04/24.04."; exit 1 ;;
  esac
  log_write "distro" "detected $A8M_OS_ID $A8M_OS_VER ($A8M_OS_FAMILY)"
  ui_ok "OS: $A8M_OS_ID $A8M_OS_VER ($A8M_OS_FAMILY)"
}

distro_base_pkgs() {
  case "$A8M_OS_FAMILY" in
    debian|ubuntu)
      echo "ca-certificates curl gnupg lsb-release apt-transport-https \
            software-properties-common whiptail git jq rsync acl swaks \
            fail2ban ufw chrony unattended-upgrades build-essential \
            unzip zip whois dnsutils netcat-openbsd htop ncdu vim-tiny \
            python3-certbot python3-acme python3-systemd openssl \
            make gdisk xfsprogs smartmontools cloud-guest-utils lvm2"
      ;;
  esac
}

distro_mailstack_pkgs() {
  case "$A8M_OS_FAMILY" in
    debian|ubuntu)
      echo "postfix postfix-pgsql dovecot-core dovecot-imapd dovecot-lmtpd \
            dovecot-sieve dovecot-managesieved dovecot-pop3d dovecot-pgsql \
            rspamd clamav clamav-daemon clamav-freshclam clamdscan \
            postgresql postgresql-client postgresql-contrib \
            redis-server redis-tools nginx certbot rsyslog"
      ;;
  esac
}
