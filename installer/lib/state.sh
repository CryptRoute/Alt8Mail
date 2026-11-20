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

# phase_run <phase-name> <function> [--force]
phase_run() {
  local name="$1" fn="$2" force="${3:-}"
  if state_done "$name" && [[ "$force" != "--force" ]]; then
    ui_ok "Phase '$name' already completed — resuming past it."
    log_write "$name" "skipped (already done)"
    return 0
  fi
  A8M_CURRENT_PHASE="$name"
  log_write "$name" "start"
  if "$fn"; then
    state_mark_done "$name"
    log_write "$name" "done"
    return 0
  else
    local rc=$?
    state_mark_failed "$name"
    log_write "$name" "failed rc=$rc"
    on_phase_failed "$name" "$rc"
    return "$rc"
  fi
}