#!/usr/bin/env bash
set -Eeuo pipefail

A8M_STATE_DIR="${A8M_STATE_DIR:-/var/lib/a8m}"
A8M_STATE_FILE="${A8M_STATE_FILE:-$A8M_STATE_DIR/install.state}"

state_init() {
  install -d -m 0750 "$A8M_STATE_DIR"
  touch "$A8M_STATE_FILE"
  chmod 0640 "$A8M_STATE_FILE"
}

state_done() {  # phase name
  grep -qx "done:$1" "$A8M_STATE_FILE" 2>/dev/null
}

state_mark_done() {
  state_done "$1" || echo "done:$1" >>"$A8M_STATE_FILE"
}

state_mark_failed() {
  echo "failed:$1:$(date -u +%FT%TZ)" >>"$A8M_STATE_FILE"
}

state_clear() { : >"$A8M_STATE_FILE"; }

phase_label() {
  case "$1" in
    bootstrap) echo "Installing base packages" ;;
    swap)      echo "Configuring swap" ;;
    repos)     echo "Configuring package repositories" ;;
    mailstack) echo "Installing the mail stack" ;;
    build)     echo "Building the Alt8Mail release" ;;
    system)    echo "Creating users, directories and secrets" ;;
    postgres)  echo "Configuring PostgreSQL and migrations" ;;
    config)    echo "Configuring mail services and application environment" ;;
    tls)       echo "Configuring nginx and TLS" ;;
    release)   echo "Installing the release and starting services" ;;
    tenant)    echo "Creating the first tenant and owner" ;;
    ufw)       echo "Configuring firewall and Fail2Ban" ;;
    health)    echo "Running health checks" ;;
    *)         echo "$1" ;;
  esac
}

# phase_run <phase-name> <function> [--force]
phase_run() {
  local name="$1" fn="$2" force="${3:-}"
  local label; label="$(phase_label "$name")"
  ui_step "$label"
  if state_done "$name" && [[ "$force" != "--force" ]]; then
    ui_phase_done "$label" "0" "already completed"
    log_write "$name" "skipped (already done)"
    return 0
  fi
  A8M_CURRENT_PHASE="$name"
  log_write "$name" "start"
  local started=$SECONDS
  if "$fn"; then
    local elapsed=$((SECONDS - started))
    state_mark_done "$name"
    log_write "$name" "done"
    ui_phase_done "$label" "$elapsed"
    return 0
  else
    local rc=$?
    state_mark_failed "$name"
    log_write "$name" "failed rc=$rc"
    on_phase_failed "$name" "$rc"
    return "$rc"
  fi
}