#!/usr/bin/env bash
set -Eeuo pipefail

A8M_MIN_RAM_MB="${A8M_MIN_RAM_MB:-2048}"
A8M_MIN_DISK_GB="${A8M_MIN_DISK_GB:-20}"

precheck_all() {
  ui_step "Preflight checks"
  local started=$SECONDS
  precheck_root
  precheck_os
  precheck_arch
  precheck_time
  precheck_network
  precheck_dns
  precheck_ram
  precheck_disk
  precheck_clean
  ui_phase_done "Preflight checks" "$((SECONDS - started))"
}

precheck_root() {
  [[ "$EUID" -eq 0 ]] || { ui_err "Run as root."; exit 1; }
  ui_ok "Running as root"
}

precheck_os() {
  distro_detect
  case "${A8M_OS_ID}:${A8M_OS_VER}" in
    debian:12)  ;;
    ubuntu:22.04|ubuntu:24.04) ;;
    debian:*|ubuntu:*) ui_warn "Tested on Debian 12 / Ubuntu 22.04 & 24.04; continuing anyway." ;;
    *) ui_err "Unsupported OS $A8M_OS_ID $A8M_OS_VER"; exit 1 ;;
  esac
}

precheck_arch() {
  local arch; arch="$(uname -m)"
  case "$arch" in
    x86_64|aarch64) ui_ok "Architecture: $arch" ;;
    *) ui_err "Unsupported architecture: $arch"; exit 1 ;;
  esac
}

precheck_time() {
  local now_utc; now_utc="$(date -u +%s)"
  local http_date
  http_date="$(curl -sI --max-time 8 http://deb.debian.org 2>/dev/null \
    | awk -F': ' 'tolower($1)=="date"{print $2}' | tr -d '\r')"
  if [[ -n "$http_date" ]]; then
    local net_ts; net_ts="$(date -u -d "$http_date" +%s 2>/dev/null || echo 0)"
    local skew=$(( now_utc - net_ts )); (( skew < 0 )) && skew=$(( -skew ))
    if (( skew > 300 )); then
      ui_err "System clock is off by ${skew}s (>5 min). Set correct time before installing."
      exit 1
    fi
    ui_ok "Clock within ${skew}s of network time"
  else
    ui_warn "Could not verify network time; continuing."
  fi
}

precheck_network() {
  getent hosts deb.debian.org >/dev/null 2>&1 || { ui_err "DNS resolution failed."; exit 1; }
  curl -fsS --max-time 10 -o /dev/null https://deb.debian.org/ \
    || { ui_err "Cannot reach https://deb.debian.org — check network/proxy."; exit 1; }
  ui_ok "DNS and outbound HTTPS OK"
}

precheck_dns() {
  local v4="${A8M_PUBLIC_IP:-}" v6="${A8M_PUBLIC_IPV6:-}"
  if [[ -z "$v4" ]]; then v4="$(detect_public_ip)"; fi
  if [[ -z "$v6" ]]; then v6="$(detect_public_ipv6)"; fi
  A8M_PUBLIC_IP="$v4"; A8M_PUBLIC_IPV6="$v6"
  export A8M_PUBLIC_IP A8M_PUBLIC_IPV6
  if [[ -z "$v4" && -z "$v6" ]]; then
    ui_err "Could not determine any public IP address for this server."
    exit 1
  fi
  command -v dig >/dev/null 2>&1 || {
    ui_err "dig is required for the DNS precheck. Install dnsutils and retry."
    exit 1
  }

  local names=("$A8M_MAIL_HOSTNAME" "$A8M_ADMIN_HOSTNAME")
  local seen=" " name records ip matched
  for name in "${names[@]}"; do
    [[ -n "$name" ]] || continue
    case "$seen" in *" $name "*) continue ;; esac
    seen+=" $name"
    records="$(dig +short +time=3 +tries=2 A "$name" 2>/dev/null | awk '/^[0-9.]+$/')"
    records+=$'\n'
    records+="$(dig +short +time=3 +tries=2 AAAA "$name" 2>/dev/null | awk '/:/')"
    matched=0
    for ip in $v4 $v6; do
      [[ -n "$ip" ]] || continue
      if grep -Fxq "$ip" <<<"$records"; then matched=1; break; fi
    done
    if (( ! matched )); then
      ui_err "DNS precheck failed for $name."
      ui_err "  Expected one of: ${v4:-none} ${v6:-}"
      ui_err "  A/AAAA records:  $(printf '%s' "$records" | tr '\n' ' ' | sed 's/  */ /g')"
      ui_err "Add an A (IPv4) or AAAA (IPv6) record pointing $name at this server, then retry."
      exit 1
    fi
    ui_ok "$name points to this server"
  done
}

precheck_ram() {
  local mem_mb
  mem_mb="$(awk '/MemTotal/ {printf "%d", $2/1024}' /proc/meminfo)"
  if (( mem_mb < A8M_MIN_RAM_MB )); then
    ui_err "RAM ${mem_mb} MiB < required ${A8M_MIN_RAM_MB} MiB. Aborting."
    exit 1
  fi
  ui_ok "RAM: ${mem_mb} MiB (>= ${A8M_MIN_RAM_MB})"
}

precheck_disk() {
  local target=/var
  [[ -d "$target" ]] || target=/
  local avail_gb
  avail_gb="$(df -BG --output=avail "$target" | tail -n1 | tr -dc '0-9')"
  if (( avail_gb < A8M_MIN_DISK_GB )); then
    ui_err "Free disk on $target: ${avail_gb} GiB < required ${A8M_MIN_DISK_GB} GiB. Aborting."
    exit 1
  fi
  ui_ok "Disk free on $target: ${avail_gb} GiB (>= ${A8M_MIN_DISK_GB})"
}

precheck_clean() {
  local offenders=()
  for svc in postfix dovecot nginx postgresql redis-server rspamd clamav-daemon; do
    if systemctl is-active --quiet "$svc" 2>/dev/null; then
      offenders+=("$svc")
    fi
  done
  [[ -f /etc/a8m/install.lock ]] && ui_warn "Found prior Alt8Mail install marker."
  if (( ${#offenders[@]} > 0 )); then
    ui_warn "Existing services are active: ${offenders[*]}"
    if [[ "${A8M_NONINTERACTIVE:-0}" == "1" ]]; then
      if [[ "${A8M_YES:-0}" != "1" && "${A8M_ALLOW_DIRTY:-0}" != "1" ]]; then
        ui_err "Refusing a non-fresh system in non-interactive mode. Pass --yes to override."
        exit 1
      fi
    elif ! ui_yesno "Non-fresh system" \
      "This is not a fresh system. Existing services may conflict.

Continue anyway (not recommended)?"; then
      exit 1
    fi
    A8M_ALLOW_DIRTY=1
  else
    ui_ok "System appears fresh (no conflicting mail services active)"
  fi
}

# Ensure every command and systemd unit used by later phases is present. This
# runs after the base and mail-stack package phases and installs anything a
# minimal image did not provide (for example the cron daemon).
precheck_runtime_dependencies() {
  local -A seen=()
  local packages=() cmd pkg unit

  while read -r cmd pkg; do
    [[ -n "$cmd" ]] || continue
    if ! command -v "$cmd" >/dev/null 2>&1; then
      if [[ -z "${seen[$pkg]:-}" ]]; then seen[$pkg]=1; packages+=("$pkg"); fi
    fi
  done < <(distro_runtime_command_packages)

  while read -r unit pkg; do
    [[ -n "$unit" ]] || continue
    if ! systemctl cat "$unit" >/dev/null 2>&1; then
      if [[ -z "${seen[$pkg]:-}" ]]; then seen[$pkg]=1; packages+=("$pkg"); fi
    fi
  done < <(distro_runtime_unit_packages)

  if (( ${#packages[@]} > 0 )); then
    if [[ "${A8M_SKIP_PACKAGES:-0}" == "1" ]]; then
      ui_err "Missing runtime packages: ${packages[*]} (package installation was skipped)"
      return 1
    fi
    ui_task "Installing missing runtime dependencies: ${packages[*]}"
    export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a NEEDRESTART_SUSPEND=1
    run_spin bootstrap "Installing missing runtime dependencies" -- \
      apt-get -o DPkg::Lock::Timeout=300 \
      -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold \
      install -y --no-install-recommends "${packages[@]}" || return 1
  fi

  local unresolved=()
  while read -r cmd pkg; do
    [[ -n "$cmd" ]] || continue
    command -v "$cmd" >/dev/null 2>&1 || unresolved+=("$cmd ($pkg)")
  done < <(distro_runtime_command_packages)
  while read -r unit pkg; do
    [[ -n "$unit" ]] || continue
    systemctl cat "$unit" >/dev/null 2>&1 || unresolved+=("unit:$unit ($pkg)")
  done < <(distro_runtime_unit_packages)
  if (( ${#unresolved[@]} > 0 )); then
    ui_err "Runtime dependencies are still missing: ${unresolved[*]}"
    return 1
  fi
  ui_ok "Installer runtime dependencies present (${#packages[@]} package(s) added)"
}

# Ensure git/curl/whiptail/dig exist so the rest of the installer can run.
precheck_bootstrap_tools() {
  local missing=() packages=()
  for b in git curl whiptail; do
    command -v "$b" >/dev/null 2>&1 || missing+=("$b")
  done
  command -v dig >/dev/null 2>&1 || packages+=(dnsutils)
  if (( ${#missing[@]} > 0 || ${#packages[@]} > 0 )); then
    export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a NEEDRESTART_SUSPEND=1
    run_spin bootstrap "Refreshing package indexes" -- \
      apt-get -o DPkg::Lock::Timeout=300 update -y
    run_spin bootstrap "Installing bootstrap tools" -- \
      apt-get -o DPkg::Lock::Timeout=300 \
      -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold \
      install -y --no-install-recommends "${missing[@]}" "${packages[@]}"
  fi
}
