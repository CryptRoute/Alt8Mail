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

# Keep the failing command in the phase log as well as install.log. The
# installer failure dialog reads the phase log, so without this line the last
# visible output can be a daemon's chatter rather than the failed command.
log_phase_failure() {  # log_phase_failure phase rc cmd...
  local phase="$1" rc="$2"; shift 2
  printf '[%s] FAIL(%s): %s\n' "$(date -u +%FT%TZ)" "$rc" "$*" \
    >>"$A8M_LOG_DIR/${phase}.log"
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
    log_phase_failure "$phase" "$rc" "$*"
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

# Run a command quietly and keep a single compact spinner/elapsed line visible.
# The command's output belongs in the phase log; only a failure tail is shown.
run_spin() {  # run_spin <phase> <message> -- cmd...
  local phase="$1" msg="$2"; shift 2
  [[ "${1:-}" == "--" ]] && shift
  local plog="$A8M_LOG_DIR/${phase}.log"
  local rc=0 started=$SECONDS
  log_write "$phase" "exec(spin): $*"
  if [[ ! -t 1 ]]; then
    ui_task "$msg"
    if "$@" >>"$plog" 2>&1; then
      log_write "$phase" "ok: $*"
      ui_task_ok "$msg"
      return 0
    fi
    rc=$?
    log_write "$phase" "FAIL($rc): $*"
    log_phase_failure "$phase" "$rc" "$*"
    ui_err "$msg failed (see $plog)"
    tail -n 25 "$plog" >&2 || true
    return "$rc"
  fi

  "$@" >>"$plog" 2>&1 &
  local pid=$! frames=('⠋' '⠙' '⠹' '⠸' '⠼' '⠴' '⠦' '⠧' '⠇' '⠏') i=0
  while kill -0 "$pid" 2>/dev/null; do
    printf '\r\033[K  %s%s%s %s (%ss)' \
      "$C_BLUE" "${frames[i % ${#frames[@]}]}" "$C_RESET" "$msg" "$((SECONDS - started))"
    i=$((i + 1))
    sleep 0.12
  done
  if wait "$pid"; then rc=0; else rc=$?; fi
  printf '\r\033[K'
  if (( rc == 0 )); then
    log_write "$phase" "ok: $*"
    ui_task_ok "$msg ($((SECONDS - started))s)"
    return 0
  fi
  log_write "$phase" "FAIL($rc): $*"
  log_phase_failure "$phase" "$rc" "$*"
  ui_err "$msg failed (see $plog)"
  tail -n 25 "$plog" >&2 || true
  return "$rc"
}

