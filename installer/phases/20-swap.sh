#!/usr/bin/env bash
set -Eeuo pipefail

phase_swap() {
  ui_step "Configuring 2 GiB swap"
  local total
  total="$(swapon --show=SIZE --noheadings --bytes 2>/dev/null | awk '{s+=$1} END {print s+0}')"
  if (( total >= 2147483648 )); then
    ui_ok "Swap already configured"; return 0
  fi
  if [[ -f /swapfile ]]; then
    ui_info "Reusing the existing /swapfile"
    if ! swapon --show=NAME --noheadings 2>/dev/null | grep -q '/swapfile'; then
      run swap -- swapon /swapfile || return 1
    fi
  else
    run swap -- fallocate -l 2G /swapfile || return 1
    run swap -- chmod 600 /swapfile || return 1
    run swap -- mkswap /swapfile || return 1
    run swap -- swapon /swapfile || return 1
  fi
  grep -q '^/swapfile ' /etc/fstab || echo '/swapfile none swap sw 0 0' >>/etc/fstab
  ui_ok "Swap enabled"
}
