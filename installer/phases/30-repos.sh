#!/usr/bin/env bash
set -Eeuo pipefail

phase_repos() {
  ui_step "Configuring Rspamd repository"
  if [[ "${A8M_SKIP_PACKAGES:-0}" == "1" ]]; then
    ui_warn "Skipping repository configuration by request"
    return 0
  fi

  local key=/usr/share/keyrings/rspamd.gpg
  local list=/etc/apt/sources.list.d/rspamd.list
  local codename="${A8M_OS_CODENAME:-bookworm}"

  install -d -m 0755 /usr/share/keyrings
  if [[ ! -s "$key" ]]; then
    curl -fsSL https://rspamd.com/apt-stable/gpg.key | gpg --batch --yes --dearmor -o "$key" || return 1
    chmod 0644 "$key"
  fi
  printf 'deb [signed-by=%s] https://rspamd.com/apt-stable/ %s main\n' "$key" "$codename" >"$list"
  run repos -- apt-get update -y || return 1
  ui_ok "Rspamd repository configured"
}
