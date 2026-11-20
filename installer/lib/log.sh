#!/usr/bin/env bash
set -Eeuo pipefail

A8M_LOG_DIR="${A8M_LOG_DIR:-/var/log/a8m}"
A8M_LOG_FILE="${A8M_LOG_FILE:-$A8M_LOG_DIR/install.log}"
A8M_QUIET="${A8M_QUIET:-0}"
A8M_CURRENT_PHASE="${A8M_CURRENT_PHASE:-init}"

log_init() {
  install -d -m 0750 "$A8M_LOG_DIR"
  : >"$A8M_LOG_DIR/_spin.log"
  touch "$A8M_LOG_FILE"
  chmod 0640 "$A8M_LOG_FILE"
  log_write "installer" "log initialized at $A8M_LOG_FILE"
}

log_write() {  # log_write phase message
  local phase="$1"; shift
  printf '[%s] [%s] %s\n' "$(date -u +%FT%TZ)" "$phase" "$*" | tee -a "$A8M_LOG_FILE" >/dev/null
}

# Run a command quietly; output captured to phase log; prints nothing on success.
run() {  # run phase -- cmd...
  local phase="$1"; shift
  [[ "${1:-}" == "--" ]] && shift
  local plog="$A8M_LOG_DIR/${phase}.log"
  log_write "$phase" "exec: $*"
  if "$@" >>"$plog" 2>&1; then
    log_write "$phase" "ok: $*"
    return 0
  else
    local rc=$?
    log_write "$phase" "FAIL($rc): $*"
    return "$rc"
  fi
}

# Same but surfaced (e.g., for interactive steps).
run_show() {
  local phase="$1"; shift
  [[ "${1:-}" == "--" ]] && shift
  local plog="$A8M_LOG_DIR/${phase}.log"
  log_write "$phase" "exec(show): $*"
  "$@" 2>&1 | tee -a "$plog"
  return "${PIPESTATUS[0]}"
}

# Stream command output to the terminal and the phase log. Used for package
# installation and builds so a long-running phase never looks frozen.
run_stream() {
  local phase="$1"; shift
  [[ "${1:-}" == "--" ]] && shift
  local plog="$A8M_LOG_DIR/${phase}.log"
  local rc=0
  log_write "$phase" "exec(stream): $*"
  set +e
  "$@" 2>&1 | tee -a "$plog"
  rc=${PIPESTATUS[0]}
  set -e
  if (( rc == 0 )); then
    log_write "$phase" "ok: $*"
  else
    log_write "$phase" "FAIL($rc): $*"
  fi
  return "$rc"
}