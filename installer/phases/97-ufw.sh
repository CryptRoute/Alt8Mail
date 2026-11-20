#!/usr/bin/env bash
set -Eeuo pipefail

phase_ufw() {
  ui_step "Configuring firewall and Fail2Ban"
  local ssh_port="${A8M_SSH_PORT:-}"
  if [[ -z "$ssh_port" ]]; then
    ssh_port="$(sshd -T 2>/dev/null | awk '/^port / && !port {port=$2} END {print port}')"
  fi
  if [[ -n "${SSH_CONNECTION:-}" ]]; then
    local connected_port
    connected_port="$(awk '{print $4}' <<<"$SSH_CONNECTION")"
    [[ "$connected_port" =~ ^[0-9]+$ ]] && ssh_port="$connected_port"
  fi
  ssh_port="${ssh_port:-22}"
  [[ "$ssh_port" =~ ^[0-9]+$ ]] || ssh_port=22

  run ufw -- ufw --force reset || true
  run ufw -- ufw default deny incoming || return 1
  run ufw -- ufw default allow outgoing || return 1
  run ufw -- ufw allow "${ssh_port}/tcp" || return 1
  local port
  for port in 25 80 110 143 443 465 587 993 995 4190 8808; do
    run ufw -- ufw allow "$port/tcp" || return 1
  done
  run ufw -- ufw --force enable || return 1

  install -d -m 0755 /etc/fail2ban/action.d
  local jail_backup action_backup
  jail_backup="$(mktemp /tmp/a8m-fail2ban-jail.XXXXXX)"
  action_backup="$(mktemp /tmp/a8m-fail2ban-action.XXXXXX)"
  if [[ -f /etc/fail2ban/jail.d/a8m.local ]]; then cp -a /etc/fail2ban/jail.d/a8m.local "$jail_backup"; fi
  if [[ -f /etc/fail2ban/action.d/a8m-ufw-port.conf ]]; then cp -a /etc/fail2ban/action.d/a8m-ufw-port.conf "$action_backup"; fi
  install -m 0644 "$A8M_SRC_DIR/deploy/fail2ban/action.d/a8m-ufw-port.conf" \
    /etc/fail2ban/action.d/a8m-ufw-port.conf
  install -m 0644 "$A8M_SRC_DIR/deploy/fail2ban/a8m.local" /etc/fail2ban/jail.d/a8m.local
  sed -i "s/^port = ssh$/port = $ssh_port/" /etc/fail2ban/jail.d/a8m.local
  if fail2ban-client -t >/dev/null 2>&1; then
    systemctl enable --now fail2ban >/dev/null 2>&1 || true
    systemctl restart fail2ban >/dev/null 2>&1 \
      || ui_warn "Fail2Ban did not restart; inspect journalctl -u fail2ban"
    rm -f "$jail_backup" "$action_backup"
  else
    if [[ -s "$jail_backup" ]]; then
      cp -a "$jail_backup" /etc/fail2ban/jail.d/a8m.local
    else
      rm -f /etc/fail2ban/jail.d/a8m.local
    fi
    if [[ -s "$action_backup" ]]; then
      cp -a "$action_backup" /etc/fail2ban/action.d/a8m-ufw-port.conf
    else
      rm -f /etc/fail2ban/action.d/a8m-ufw-port.conf
    fi
    rm -f "$jail_backup" "$action_backup"
    systemctl restart fail2ban >/dev/null 2>&1 || true
    ui_warn "Fail2Ban configuration did not validate; the previous configuration was restored"
  fi
  run ufw -- systemctl enable --now fail2ban || ui_warn "Fail2Ban is not enabled"
  install -m 0644 "$A8M_SRC_DIR/deploy/sysctl/99-alt8mail-mail.conf" /etc/sysctl.d/99-alt8mail-mail.conf
  sysctl --system >/dev/null 2>&1 || ui_warn "Some sysctl settings could not be applied"
  ui_ok "Firewall and Fail2Ban configured (SSH ${ssh_port}/tcp)"
}
