#!/usr/bin/env bash
set -Eeuo pipefail

phase_health() {
  ui_step "Running health checks"
  local failed=0
  local units=(postgresql "postgresql@${A8M_PG_MAJOR}-main" redis-server dovecot
    postfix rspamd clamav-daemon clamav-freshclam nginx fail2ban cron chrony
    a8m-server a8m-admin alt8mail-control)

  local unit enabled_state
  for unit in "${units[@]}"; do
    systemctl cat "$unit" >/dev/null 2>&1 || continue
    enabled_state="$(systemctl is-enabled "$unit" 2>/dev/null || true)"
    case "$enabled_state" in
      enabled|enabled-runtime) ui_ok "service $unit: enabled" ;;
      *) ui_err "service $unit: not enabled at boot"; failed=1 ;;
    esac
    if systemctl is-active --quiet "$unit"; then
      ui_ok "service $unit: active"
    elif [[ "$unit" == clamav-* ]]; then
      ui_warn "service $unit is still starting or updating signatures"
    else
      ui_err "service $unit: not active"
      failed=1
    fi
  done

  local i
  local ready=0
  for i in $(seq 1 30); do
    if curl -fsS --max-time 3 http://127.0.0.1:8080/api/health >/dev/null 2>&1 \
      && curl -fsS --max-time 3 http://127.0.0.1:8081/api/health >/dev/null 2>&1 \
      && curl -kfsS --max-time 3 https://127.0.0.1:8808/healthz >/dev/null 2>&1; then
      ready=1
      break
    fi
    sleep 2
  done
  if (( ready )); then
    ui_ok "Application readiness checks passed"
  else
    ui_err "Application readiness checks did not pass within 60 seconds"
    failed=1
  fi

  if nginx -t >/dev/null 2>&1; then
    ui_ok "nginx configuration valid"
  else
    ui_err "nginx configuration check failed"
    failed=1
  fi
  if postfix check >/dev/null 2>&1; then
    ui_ok "postfix configuration valid"
  else
    ui_err "postfix check failed"
    failed=1
  fi

  if curl -kfsS --max-time 5 "https://$A8M_MAIL_HOSTNAME/api/health" >/dev/null 2>&1; then
    ui_ok "Public webmail endpoint responds"
  else
    ui_warn "Public webmail endpoint is not responding yet (DNS or certificate may still be propagating)"
  fi

  if (( failed )); then
    ui_warn "Some checks failed. Inspect: journalctl -u a8m-server -u a8m-admin -u alt8mail-control"
    return 1
  fi
  ui_ok "All health checks passed"
}
