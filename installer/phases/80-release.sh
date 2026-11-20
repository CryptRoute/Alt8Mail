#!/usr/bin/env bash
set -Eeuo pipefail

phase_release() {
  resolve_release_source || { ui_err "No built release is available"; return 1; }
  local src="$A8M_RELEASE_SOURCE"
  [[ -x "$src/bin/a8m-server" ]] || { ui_err "Release has no bin/a8m-server: $src"; return 1; }

  ui_task "Installing the release into $A8M_RELEASES_DIR/$A8M_VERSION"
  local release_dir="$A8M_RELEASES_DIR/$A8M_VERSION"
  rm -rf "$release_dir"
  install -d -m 0755 "$release_dir"
  cp -a "$src/." "$release_dir/"
  chmod -R a+rX "$release_dir"
  chmod 0755 "$release_dir/bin/"* 2>/dev/null || true

  ln -sfn "$release_dir" "$A8M_INSTALL_ROOT/current"
  ln -sfn "$A8M_INSTALL_ROOT/current/bin" "$A8M_INSTALL_ROOT/bin"
  ln -sfn "$A8M_INSTALL_ROOT/current/admin" "$A8M_INSTALL_ROOT/admin"
  ln -sfn "$A8M_INSTALL_ROOT/current" "$A8M_INSTALL_ROOT/app"
  rm -rf "$A8M_WEB_ROOT"
  ln -s "$A8M_INSTALL_ROOT/current/public" "$A8M_WEB_ROOT"
  ln -sfn "$A8M_INSTALL_ROOT/current/bin/alt8mail-control" "$A8M_CONTROL_ROOT/bin/alt8mail-control"
  if [[ -x "$release_dir/scripts/alt8mail-update.sh" ]]; then
    install -m 0755 "$release_dir/scripts/alt8mail-update.sh" /usr/local/bin/alt8mail-update
  fi

  ui_task "Installing systemd units"
  local unit
  for unit in "$release_dir"/deploy/systemd/*.service "$release_dir"/deploy/systemd/*.timer; do
    [[ -f "$unit" ]] || continue
    install -m 0644 "$unit" "/etc/systemd/system/$(basename "$unit")"
  done
  run release -- systemctl daemon-reload || return 1

  # Every monitored unit must be enabled at boot.
  ui_task "Enabling every monitored unit at boot"
  run release -- systemctl enable --now postgresql redis-server dovecot rspamd \
    nginx postfix fail2ban cron chrony || return 1
  run release -- systemctl enable --now clamav-freshclam \
    || ui_warn "clamav-freshclam could not start yet"
  run release -- systemctl enable --now clamav-daemon \
    || ui_warn "clamav-daemon will start after its first signature update"
  local pg_unit="postgresql@${A8M_PG_MAJOR}-main.service"
  if systemctl cat "$pg_unit" >/dev/null 2>&1; then
    systemctl enable "$pg_unit" >/dev/null 2>&1 || true
    if [[ "$(systemctl is-enabled "$pg_unit" 2>/dev/null || true)" != "enabled" ]]; then
      local dropin="/etc/systemd/system/${pg_unit}.d"
      install -d -m 0755 "$dropin"
      printf '[Install]\nWantedBy=multi-user.target\n' >"$dropin/50-alt8mail-enable.conf"
      systemctl daemon-reload
      systemctl enable "$pg_unit" >/dev/null 2>&1 \
        || ui_warn "PostgreSQL instance unit $pg_unit could not be persistently enabled"
    fi
  fi

  ui_task "Restarting mail services"
  run release -- systemctl restart dovecot rspamd || return 1
  run release -- systemctl restart clamav-daemon \
    || ui_warn "clamav-daemon is not ready yet; the timer/health check will report it"
  run release -- systemctl enable --now postfix || return 1
  run release -- systemctl restart postfix || return 1
  if ! postfix check; then
    ui_err "postfix check rejected the installed configuration"
    return 1
  fi

  ui_task "Starting Alt8Mail web, admin and Control services"
  run release -- systemctl enable --now a8m-server a8m-admin alt8mail-control || return 1
  run release -- systemctl enable --now a8m-mailprovision.timer a8m-dkimprovision.timer || return 1
  run release -- systemctl restart a8m-server a8m-admin alt8mail-control || return 1
  run release -- systemctl enable --now a8m-office-agent \
    || ui_warn "Office agent did not start; install Docker, then start a8m-office-agent"
  ui_task_ok "Release installed and services started"
}
