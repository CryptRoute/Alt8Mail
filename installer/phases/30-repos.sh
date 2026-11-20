#!/usr/bin/env bash
set -Eeuo pipefail

phase_repos() {
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
  ui_task "Refreshing indexes from the Rspamd repository"
  run_stream repos -- apt-get -o DPkg::Lock::Timeout=300 update -y || return 1
  ui_task_ok "Rspamd repository configured"
}
