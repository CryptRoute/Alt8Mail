#!/usr/bin/env bash
# Shared paths and small helpers for the Alt8Mail installer.
set -Eeuo pipefail

A8M_SERVICE_USER="${A8M_SERVICE_USER:-a8m}"
A8M_INSTALL_ROOT="${A8M_INSTALL_ROOT:-/opt/a8m}"
A8M_CONFIG_ROOT="${A8M_CONFIG_ROOT:-/etc/a8m}"
A8M_CONTROL_ROOT="${A8M_CONTROL_ROOT:-/opt/alt8mail}"
A8M_WEB_ROOT="${A8M_WEB_ROOT:-/var/www/a8m}"
A8M_SECRETS_DIR="$A8M_INSTALL_ROOT/secrets"
A8M_RELEASES_DIR="$A8M_INSTALL_ROOT/releases"
A8M_PUBLIC_IP=""
A8M_PG_MAJOR=""
A8M_RELEASE_SOURCE=""

rand_hex() {
  openssl rand -hex "$1"
}

rand_password() {
  openssl rand -base64 36 | tr -d '/+=\n' | cut -c1-28
}

read_or_create_secret() {  # read_or_create_secret <path> <hex-bytes>
  local path="$1" length="${2:-32}"
  if [[ ! -s "$path" ]]; then
    install -d -m 0750 "$(dirname "$path")"
    rand_hex "$length" >"$path"
    chmod 0600 "$path"
    chown "$A8M_SERVICE_USER:$A8M_SERVICE_USER" "$path"
  fi
  tr -d '\n' <"$path"
}

env_file_value() {  # env_file_value <file> <key> | env_file_value <key>
  local file key
  if [[ $# -eq 1 ]]; then
    key="$1"
    file="${A8M_CONFIG_ROOT:-/etc/a8m}/app.env"
  else
    file="$1"; key="$2"
  fi
  grep -E "^${key}=" "$file" 2>/dev/null | tail -n1 | cut -d= -f2- || true
}

control_env_value() {  # control_env_value <key>
  env_file_value "${A8M_CONFIG_ROOT:-/etc/a8m}/control.env" "$1"
}

restore_env_file_value() {  # restore_env_file_value <file> <key> <value> | <key> <value>
  local file key value
  if [[ $# -eq 2 ]]; then
    key="$1"; value="$2"
    file="${A8M_CONFIG_ROOT:-/etc/a8m}/app.env"
  else
    file="$1"; key="$2"; value="$3"
  fi
  [[ -n "$value" ]] || return 0
  sed -i "/^${key}=/d" "$file"
  printf '%s=%s\n' "$key" "$value" >>"$file"
}

detect_public_ip() {
  local ip=""
  ip="$(curl -4fsS --max-time 8 https://api.ipify.org 2>/dev/null || true)"
  if [[ -z "$ip" ]]; then
    ip="$(ip -4 route get 1.1.1.1 2>/dev/null \
      | awk '{for (i=1;i<=NF;i++) if (!found && $i=="src") {print $(i+1); found=1}}' || true)"
  fi
  printf '%s\n' "$ip"
}

detect_pg_major() {
  local major=""
  major="$(psql -V 2>/dev/null | sed -nE 's/.* ([0-9]+)\..*/\1/p' | head -n1)"
  if [[ -z "$major" && -d /etc/postgresql ]]; then
    major="$(find /etc/postgresql -mindepth 1 -maxdepth 1 -type d -printf '%f\n' 2>/dev/null \
      | sort -V | tail -n1)"
  fi
  printf '%s\n' "$major"
}

normalize_domain() {
  local value="${1,,}"
  printf '%s\n' "${value%.}"
}

normalize_email() {
  local value="${1,,}"
  printf '%s\n' "$value"
}

normalize_release_bin() {  # normalize_release_bin <release-root>
  local root="$1"
  [[ -d "$root/bin" && -f "$root/bin/a8m-server" ]] || return 1
  # Archives assembled on a Windows host can lose the executable bit. The
  # release is still valid; restore the bit before testing or installing it.
  chmod 0755 "$root/bin/"* 2>/dev/null || true
  [[ -x "$root/bin/a8m-server" ]]
}

resolve_release_source() {
  if [[ -n "${A8M_RELEASE_SOURCE:-}" ]] && normalize_release_bin "$A8M_RELEASE_SOURCE"; then
    return 0
  fi
  if normalize_release_bin "$A8M_SRC_DIR"; then
    A8M_RELEASE_SOURCE="$A8M_SRC_DIR"
    export A8M_RELEASE_SOURCE
    return 0
  fi
  local candidate="/tmp/a8m-build/alt8mail-$A8M_VERSION"
  if normalize_release_bin "$candidate"; then
    A8M_RELEASE_SOURCE="$candidate"
    export A8M_RELEASE_SOURCE
    return 0
  fi
  return 1
}

psql_postgres() {
  runuser -u postgres -- env "PGOPTIONS=${PGOPTIONS:-}" psql "$@"
}

apply_install_hostname() {  # apply_install_hostname <hostname> [domain]
  local new="$1" domain="${2:-}"
  [[ -n "$new" ]] || return 0
  if command -v hostnamectl >/dev/null 2>&1 \
      && hostnamectl set-hostname "$new" >/dev/null 2>&1; then
    :
  else
    printf '%s\n' "$new" >/etc/hostname 2>/dev/null || true
    hostname "$new" >/dev/null 2>&1 || true
  fi
  if [[ -f /etc/hosts ]]; then
    sed -i '/^127\.0\.1\.1[[:space:]]/d' /etc/hosts
    if [[ -n "$domain" ]]; then
      printf '127.0.1.1 %s %s\n' "$new" "$domain" >>/etc/hosts
    else
      printf '127.0.1.1 %s\n' "$new" >>/etc/hosts
    fi
  fi
  HOSTNAME="$new"
  export HOSTNAME
  A8M_BACKTITLE="Alt8Mail ${A8M_VERSION:-}  |  https://alt8mail.com  |  ${new}"
  export A8M_BACKTITLE
}
