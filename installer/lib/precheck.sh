#!/usr/bin/env bash
set -Eeuo pipefail

A8M_MIN_RAM_MB="${A8M_MIN_RAM_MB:-2048}"
A8M_MIN_DISK_GB="${A8M_MIN_DISK_GB:-20}"

precheck_all() {
  ui_step "Preflight checks"
  precheck_root
  precheck_os
  precheck_arch
  precheck_time
  precheck_network
  precheck_ram
  precheck_disk
  precheck_clean
  ui_ok "All preflight checks passed."
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

# Ensure git/curl/whiptail exist so the rest of the installer can run.
precheck_bootstrap_tools() {
  local missing=()
  for b in git curl whiptail; do
    command -v "$b" >/dev/null 2>&1 || missing+=("$b")
  done
  if (( ${#missing[@]} > 0 )); then
    ui_info "Installing bootstrap tools: ${missing[*]}"
    export DEBIAN_FRONTEND=noninteractive
    run bootstrap -- apt-get update -y
    run bootstrap -- apt-get install -y --no-install-recommends "${missing[@]}"
  fi
}
