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
            fail2ban ufw chrony cron unattended-upgrades build-essential \
            unzip zip whois dnsutils netcat-openbsd htop ncdu vim-tiny \
            python3-certbot python3-acme python3-systemd openssl openssh-client \
            tzdata hostname iproute2 make gdisk xfsprogs smartmontools \
            cloud-guest-utils lvm2"
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

# Units that must be running for mail delivery and the web application. Any
# failure here aborts the release phase.
distro_core_units() {
  echo "postgresql redis-server dovecot rspamd nginx postfix"
}

# Units that support the stack. They should be enabled and running after a
# successful install, but they do not carry mail or HTTP traffic themselves.
distro_support_units() {
  echo "rsyslog cron chrony"
}

# Units that improve the deployment but must not abort the install when the
# host cannot run them yet (for example, a container without netfilter access).
distro_optional_units() {
  echo "fail2ban clamav-freshclam clamav-daemon"
}

distro_required_units() {
  echo "$(distro_core_units) $(distro_support_units)"
}

# Executable-to-package map used by the dependency check. The installer calls
# these commands after the package phases, so a minimal image must not rely on
# them arriving through some other package's recommends.
distro_runtime_command_packages() {
  cat <<'EOF'
systemctl systemd
hostnamectl systemd
timedatectl systemd
hostname hostname
curl curl
dig dnsutils
jq jq
openssl openssl
gpg gnupg
git git
whiptail whiptail
ssh-keygen openssh-client
psql postgresql-client
createdb postgresql-client
pg_dump postgresql-client
runuser util-linux
postmap postfix
postconf postfix
newaliases postfix
doveadm dovecot-core
certbot certbot
nginx nginx
ufw ufw
fail2ban-client fail2ban
sysctl procps
EOF
}

# Unit-to-package map used by the same dependency check. It catches a minimal
# image that installs a daemon binary without its systemd unit (or the reverse).
distro_runtime_unit_packages() {
  cat <<'EOF'
postgresql postgresql
redis-server redis-server
dovecot dovecot-core
rspamd rspamd
nginx nginx
postfix postfix
rsyslog rsyslog
cron cron
chrony chrony
fail2ban fail2ban
clamav-freshclam clamav-freshclam
clamav-daemon clamav-daemon
EOF
}
