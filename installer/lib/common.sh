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

resolve_release_source() {
  if [[ -n "${A8M_RELEASE_SOURCE:-}" && -x "$A8M_RELEASE_SOURCE/bin/a8m-server" ]]; then
    return 0
  fi
  if [[ -x "$A8M_SRC_DIR/bin/a8m-server" ]]; then
    A8M_RELEASE_SOURCE="$A8M_SRC_DIR"
    export A8M_RELEASE_SOURCE
    return 0
  fi
  local candidate="/tmp/a8m-build/alt8mail-$A8M_VERSION"
  if [[ -x "$candidate/bin/a8m-server" ]]; then
    A8M_RELEASE_SOURCE="$candidate"
    export A8M_RELEASE_SOURCE
    return 0
  fi
  return 1
}
