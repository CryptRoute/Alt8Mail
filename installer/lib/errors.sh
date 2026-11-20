#!/usr/bin/env bash
set -Eeuo pipefail

on_phase_failed() {  # phase rc
  local phase="$1" rc="$2"
  local plog="$A8M_LOG_DIR/${phase}.log"
  local tail10; tail10="$(tail -n 10 "$plog" 2>/dev/null || true)"

  log_write "$phase" "phase failed rc=$rc"

  local body="Phase '${phase}' failed (exit ${rc}).

Last lines from ${plog}:

${tail10}

The installer has saved its progress. You can:
  • Fix the underlying issue, then re-run the installer to resume.
  • Inspect logs: ${A8M_LOG_DIR}/  and journalctl.
  • Re-run with --force to re-run this phase.

Do you want to open a shell to investigate? (Continue to abort.)"
  if [[ "${A8M_NONINTERACTIVE:-0}" != "1" ]] && ui_yesno "Installation failed: $phase" "$body"; then
    ui_msg "Dropping to shell" "Type 'exit' to resume the failure menu."
    bash -i || true
  fi
  exit "$rc"
}

on_unexpected_error() {  # ERR trap
  local rc=$?
  local line="$1" cmd="$2"
  ui_err "Unexpected error (rc=$rc) at line $line: $cmd"
  log_write "unexpected" "rc=$rc line=$line cmd=$cmd"
  on_phase_failed "${A8M_CURRENT_PHASE:-unknown}" "$rc"
}

a8m_install_traps() {
  trap 'on_unexpected_error "$LINENO" "$BASH_COMMAND"' ERR
  trap 'log_write "installer" "received SIGINT"; ui_err "Interrupted."; exit 130' INT
  trap 'log_write "installer" "received SIGTERM"; exit 143' TERM
}

# Simple retry wrapper
retry() {  # retry <attempts> -- cmd...
  local n="$1"; shift
  [[ "${1:-}" == "--" ]] && shift
  local i
  for (( i=1; i<=n; i++ )); do
    "$@" && return 0
    ui_warn "Attempt $i/$n failed: $*"
    sleep $(( i * 2 ))
  done
  return 1
}