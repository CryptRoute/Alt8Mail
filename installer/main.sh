#!/usr/bin/env bash
# Alt8Mail Installer — orchestrator.
set -Eeuo pipefail
umask 027

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Defaults
A8M_DOMAIN="${A8M_DOMAIN:-}"
A8M_MAIL_HOSTNAME="${A8M_MAIL_HOSTNAME:-${A8M_HOSTNAME:-}}"
A8M_ADMIN_EMAIL="${A8M_ADMIN_EMAIL:-}"
A8M_ADMIN_NAME="${A8M_ADMIN_NAME:-Alt8Mail Owner}"
A8M_CONTROL_EMAIL="${A8M_CONTROL_EMAIL:-sysadmin@alt8mail.local}"
A8M_TENANT_NAME="${A8M_TENANT_NAME:-Alt8Mail}"
A8M_TENANT_SLUG="${A8M_TENANT_SLUG:-a8m}"
A8M_TLS_MODE="${A8M_TLS_MODE:-letsencrypt}"
A8M_TLS_EMAIL="${A8M_TLS_EMAIL:-}"
A8M_NONINTERACTIVE=0
A8M_FORCE=0
A8M_DRY_RUN=0
A8M_QUIET=0
A8M_YES=0
A8M_SKIP_PACKAGES=0
A8M_SKIP_CERTBOT=0
A8M_SRC_DIR="${A8M_SRC_DIR:-}"
A8M_SSH_PORT="${A8M_SSH_PORT:-}"

source "$HERE/lib/ui.sh"
source "$HERE/lib/log.sh"
source "$HERE/lib/state.sh"
source "$HERE/lib/errors.sh"
source "$HERE/lib/common.sh"
source "$HERE/lib/distro.sh"
source "$HERE/lib/precheck.sh"

for p in "$HERE"/phases/*.sh; do source "$p"; done

usage() {
  cat <<EOF
Alt8Mail Installer ${A8M_VERSION}

Usage: $0 [options]

  --domain <d>          Primary mail domain
  --hostname <h>        Mail hostname (default: mail.<domain>)
  --admin-email <e>     Owner/admin email
  --admin-name <n>      Owner display name
  --control-email <e>   First System Control superadmin
  --tenant-name <n>     Organization name
  --tenant-slug <s>     Organization slug
  --tls <mode>          letsencrypt or selfsigned
  --tls-email <e>       Let's Encrypt account email
  --source-dir <dir>    Source checkout or extracted release
  --non-interactive     No prompts
  --force               Re-run completed phases
  --yes                 Assume yes
  --quiet               Suppress decorations
  --dry-run             Print plan and exit
  --skip-packages       Skip apt package installation
  --skip-certbot        Use a self-signed certificate
  --reset-state         Clear the resume journal and exit
  -h, --help
EOF
}

need_arg() {
  [[ $# -ge 2 && -n "$2" ]] || { ui_err "Option $1 requires a value"; exit 2; }
}

while (( $# )); do
  case "$1" in
    --domain) need_arg "$@"; A8M_DOMAIN="$2"; shift 2 ;;
    --hostname) need_arg "$@"; A8M_MAIL_HOSTNAME="$2"; shift 2 ;;
    --admin-email) need_arg "$@"; A8M_ADMIN_EMAIL="$2"; shift 2 ;;
    --admin-name) need_arg "$@"; A8M_ADMIN_NAME="$2"; shift 2 ;;
    --control-email) need_arg "$@"; A8M_CONTROL_EMAIL="$2"; shift 2 ;;
    --tenant-name) need_arg "$@"; A8M_TENANT_NAME="$2"; shift 2 ;;
    --tenant-slug) need_arg "$@"; A8M_TENANT_SLUG="$2"; shift 2 ;;
    --tls) need_arg "$@"; A8M_TLS_MODE="$2"; shift 2 ;;
    --tls-email) need_arg "$@"; A8M_TLS_EMAIL="$2"; shift 2 ;;
    --source-dir) need_arg "$@"; A8M_SRC_DIR="$2"; shift 2 ;;
    --ssh-port) need_arg "$@"; A8M_SSH_PORT="$2"; shift 2 ;;
    --non-interactive) A8M_NONINTERACTIVE=1; shift ;;
    --force) A8M_FORCE=1; shift ;;
    --yes) A8M_YES=1; shift ;;
    --quiet) A8M_QUIET=1; shift ;;
    --dry-run) A8M_DRY_RUN=1; shift ;;
    --skip-packages) A8M_SKIP_PACKAGES=1; shift ;;
    --skip-certbot) A8M_SKIP_CERTBOT=1; A8M_TLS_MODE=selfsigned; shift ;;
    --reset-state)
      [[ "$EUID" -eq 0 ]] || { ui_err "Run as root to reset installer state."; exit 1; }
      state_init; state_clear; ui_ok "State cleared."; exit 0 ;;
    -h|--help) usage; exit 0 ;;
    *) ui_err "Unknown option: $1"; usage; exit 2 ;;
  esac
done

export A8M_NONINTERACTIVE A8M_QUIET A8M_YES A8M_FORCE A8M_DRY_RUN
export A8M_SKIP_PACKAGES A8M_SKIP_CERTBOT

# Require root before creating logs or state under /var.
precheck_root

# Resolve the source/release root before reading VERSION.
if [[ -n "$A8M_SRC_DIR" ]]; then
  A8M_SRC_DIR="$(cd "$A8M_SRC_DIR" && pwd)"
else
  A8M_SRC_DIR="$(cd "$HERE/.." && pwd)"
fi
[[ -r "$A8M_SRC_DIR/VERSION" ]] || { ui_err "Missing VERSION in $A8M_SRC_DIR"; exit 1; }
A8M_VERSION="$(tr -d '[:space:]' < "$A8M_SRC_DIR/VERSION")"
[[ "$A8M_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] \
  || { ui_err "Invalid VERSION: $A8M_VERSION"; exit 1; }
export A8M_VERSION A8M_SRC_DIR

# Vendor defaults for licensing and update delivery.
if [[ -f "$A8M_SRC_DIR/deploy/release-license.env" ]]; then
  # shellcheck disable=SC1091
  . "$A8M_SRC_DIR/deploy/release-license.env"
fi
A8M_LICENSE_FILE="${A8M_LICENSE_FILE:-/etc/a8m/license.token}"
A8M_LICENSE_PUBLIC_KEY="${A8M_LICENSE_PUBLIC_KEY:-${A8M_RELEASE_LICENSE_PUBLIC_KEY:-}}"
A8M_LICENSE_PUBLIC_KEY_ID="${A8M_LICENSE_PUBLIC_KEY_ID:-${A8M_RELEASE_LICENSE_PUBLIC_KEY_ID:-}}"
A8M_LICENSE_PUBLIC_KEYS="${A8M_LICENSE_PUBLIC_KEYS:-${A8M_RELEASE_LICENSE_PUBLIC_KEYS:-}}"
A8M_LICENSE_GROUP="${A8M_LICENSE_GROUP:-a8m}"
A8M_LICENSE_TENANT="${A8M_LICENSE_TENANT:-}"
A8M_LICENSE_PORTAL_URL="${A8M_LICENSE_PORTAL_URL:-${A8M_RELEASE_LICENSE_PORTAL_URL:-}}"
A8M_ENTERPRISE_RELEASE_PUBLIC_KEY="${A8M_ENTERPRISE_RELEASE_PUBLIC_KEY:-${A8M_RELEASE_ENTERPRISE_RELEASE_PUBLIC_KEY:-}}"
export A8M_LICENSE_FILE A8M_LICENSE_PUBLIC_KEY A8M_LICENSE_PUBLIC_KEY_ID
export A8M_LICENSE_PUBLIC_KEYS A8M_LICENSE_GROUP A8M_LICENSE_TENANT A8M_LICENSE_PORTAL_URL
export A8M_ENTERPRISE_RELEASE_PUBLIC_KEY

if [[ "$A8M_DRY_RUN" != "1" ]]; then
  ui_welcome
fi
log_init
state_init
a8m_install_traps
ui_has_whiptail || precheck_bootstrap_tools

# Interactive collection
if [[ "$A8M_NONINTERACTIVE" != "1" ]]; then
  [[ -z "$A8M_DOMAIN" ]]      && A8M_DOMAIN="$(ui_input 'Configuration' 'Primary mail domain:' 'example.com')"
  [[ -z "$A8M_MAIL_HOSTNAME" ]] && A8M_MAIL_HOSTNAME="$(ui_input 'Configuration' 'Mail hostname:' "mail.${A8M_DOMAIN}")"
  [[ -z "$A8M_ADMIN_EMAIL" ]] && A8M_ADMIN_EMAIL="$(ui_input 'Configuration' 'Admin/owner email:' "owner@${A8M_DOMAIN}")"
  [[ -z "$A8M_TLS_EMAIL" ]]   && A8M_TLS_EMAIL="$A8M_ADMIN_EMAIL"
elif [[ -z "$A8M_DOMAIN" || -z "$A8M_MAIL_HOSTNAME" || -z "$A8M_ADMIN_EMAIL" ]]; then
  ui_err "--non-interactive requires --domain, --hostname, --admin-email"; exit 2
fi

A8M_DOMAIN="$(normalize_domain "$A8M_DOMAIN")"
A8M_MAIL_HOSTNAME="$(normalize_domain "$A8M_MAIL_HOSTNAME")"
[[ -n "$A8M_MAIL_HOSTNAME" ]] || A8M_MAIL_HOSTNAME="mail.$A8M_DOMAIN"
A8M_ADMIN_EMAIL="$(normalize_email "$A8M_ADMIN_EMAIL")"
[[ -n "$A8M_ADMIN_EMAIL" ]] || A8M_ADMIN_EMAIL="owner@$A8M_DOMAIN"
A8M_CONTROL_EMAIL="$(normalize_email "$A8M_CONTROL_EMAIL")"
A8M_TLS_EMAIL="${A8M_TLS_EMAIL:-$A8M_ADMIN_EMAIL}"
A8M_TLS_MODE="${A8M_TLS_MODE,,}"

[[ "$A8M_DOMAIN" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?\.[a-z]{2,}$ ]] \
  || { ui_err "Invalid mail domain: $A8M_DOMAIN"; exit 2; }
[[ "$A8M_MAIL_HOSTNAME" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]] \
  || { ui_err "Invalid mail hostname: $A8M_MAIL_HOSTNAME"; exit 2; }
[[ "$A8M_ADMIN_EMAIL" =~ ^[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}$ ]] \
  || { ui_err "Invalid owner email: $A8M_ADMIN_EMAIL"; exit 2; }
[[ "$A8M_ADMIN_EMAIL" == *"@$A8M_DOMAIN" ]] \
  || { ui_err "Owner email must use $A8M_DOMAIN"; exit 2; }
[[ "$A8M_CONTROL_EMAIL" =~ ^[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}$ ]] \
  || { ui_err "Invalid control-plane email: $A8M_CONTROL_EMAIL"; exit 2; }
[[ "$A8M_TLS_MODE" == "letsencrypt" || "$A8M_TLS_MODE" == "selfsigned" ]] \
  || { ui_err "--tls must be letsencrypt or selfsigned"; exit 2; }
[[ "$A8M_TENANT_SLUG" =~ ^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$ ]] \
  || { ui_err "Invalid tenant slug: $A8M_TENANT_SLUG"; exit 2; }
export A8M_DOMAIN A8M_MAIL_HOSTNAME A8M_ADMIN_EMAIL A8M_ADMIN_NAME
export A8M_CONTROL_EMAIL A8M_TENANT_NAME A8M_TENANT_SLUG
export A8M_TLS_MODE A8M_TLS_EMAIL A8M_SSH_PORT

ui_info "Domain: ${A8M_DOMAIN} · hostname: ${A8M_MAIL_HOSTNAME} · owner: ${A8M_ADMIN_EMAIL} · Control: ${A8M_CONTROL_EMAIL}"

if [[ "$A8M_DRY_RUN" == "1" ]]; then
  ui_msg "Dry run" "Plan:
  precheck → bootstrap → swap → repos → mailstack → build → system
  → postgres → config → tls → release → tenant → ufw → health

  Source:   ${A8M_SRC_DIR}
  Version:  ${A8M_VERSION}
  Domain:   ${A8M_DOMAIN}
  Hostname: ${A8M_MAIL_HOSTNAME}
  Owner:    ${A8M_ADMIN_EMAIL}
  Control:  ${A8M_CONTROL_EMAIL}
  TLS:      ${A8M_TLS_MODE}"
  exit 0
fi

F=""; [[ "$A8M_FORCE" == "1" ]] && F="--force"

precheck_all

phase_run bootstrap  phase_bootstrap  "$F"
phase_run swap       phase_swap       "$F"
phase_run repos      phase_repos      "$F"
phase_run mailstack  phase_mailstack  "$F"
phase_run build      phase_build      "$F"
phase_run system     phase_system     "$F"
phase_run postgres   phase_postgres   "$F"
phase_run config     phase_config     "$F"
phase_run tls        phase_tls        "$F"
phase_run release    phase_release    "$F"
phase_run tenant     phase_tenant     "$F"
phase_run ufw        phase_ufw        "$F"
phase_run health     phase_health     "$F"

ui_step "Complete"
ui_summary
ui_msg "Installation complete" "Alt8Mail ${A8M_VERSION} is installed.

  Webmail: https://${A8M_MAIL_HOSTNAME}/
  Admin:   https://${A8M_MAIL_HOSTNAME}/admin/
  Control: https://${A8M_MAIL_HOSTNAME}:8808/
  Owner:   ${A8M_ADMIN_EMAIL}
  Control: ${A8M_CONTROL_EMAIL}
  Credentials: /root/alt8mail-initial-credentials.txt
  Logs:  ${A8M_LOG_DIR}
  State: ${A8M_STATE_FILE}"
