#!/usr/bin/env bash
set -Eeuo pipefail

phase_health() {
  if [[ -z "${A8M_PG_MAJOR:-}" ]]; then
    A8M_PG_MAJOR="$(detect_pg_major)"
    export A8M_PG_MAJOR
  fi
  local failed=0
  local core_units=($(distro_core_units))
  local support_units=($(distro_support_units))
  local optional_units=($(distro_optional_units))
  local app_units=(a8m-server a8m-admin alt8mail-control)
  local cluster_unit="postgresql@${A8M_PG_MAJOR}-main"
  local units=("${core_units[@]}" "$cluster_unit" "${support_units[@]}"
    "${optional_units[@]}" "${app_units[@]}")

  local unit enabled_state optional
  for unit in "${units[@]}"; do
    systemctl cat "$unit" >/dev/null 2>&1 || continue
    optional=0
    case " ${optional_units[*]} " in *" $unit "*) optional=1 ;; esac
    enabled_state="$(systemctl is-enabled "$unit" 2>/dev/null || true)"
    case "$enabled_state" in
      enabled) ui_ok "service $unit: enabled" ;;
      enabled-runtime)
        if [[ "$unit" == postgresql@* ]]; then
          ui_warn "service $unit: enabled for this boot only; persistent drop-in is not active"
        elif (( optional )); then
          ui_warn "service $unit: enabled for this boot only"
        else
          ui_err "service $unit: not persistently enabled at boot"
          failed=1
        fi
        ;;
      *)
        if (( optional )); then
          ui_warn "service $unit: not enabled at boot"
        else
          ui_err "service $unit: not enabled at boot"
          failed=1
        fi
        ;;
    esac
    if systemctl is-active --quiet "$unit"; then
      ui_ok "service $unit: active"
    elif (( optional )); then
      ui_warn "service $unit is not active yet; inspect journalctl -u $unit"
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
