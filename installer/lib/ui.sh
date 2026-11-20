#!/usr/bin/env bash
# Alt8Mail enterprise installer UI layer (whiptail).
set -Eeuo pipefail

# ---- Brand theme (Virtualmin-style) -----------------------------------------
A8M_TITLE="Alt8Mail Enterprise Installer"
if [[ -z "${A8M_VERSION:-}" ]]; then
  A8M_VERSION="$(tr -d '[:space:]' < "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/VERSION" 2>/dev/null || true)"
fi
A8M_VERSION="${A8M_VERSION:-dev}"
A8M_BACKTITLE="Alt8Mail ${A8M_VERSION}  |  https://alt8mail.com  |  ${HOSTNAME:-localhost}"

# whiptail geometry
WT_HEIGHT=20
WT_WIDTH=74
WT_MENU_HEIGHT=12

# Colors for CLI echo (only when TTY + not quiet)
if [[ -t 1 ]] && [[ "${A8M_QUIET:-0}" != "1" ]]; then
  C_RESET=$'\e[0m'; C_BOLD=$'\e[1m'
  C_BLUE=$'\e[38;5;39m'; C_GREEN=$'\e[38;5;42m'
  C_YELLOW=$'\e[38;5;220m'; C_RED=$'\e[38;5;196m'
  C_GRAY=$'\e[38;5;245m'
else
  C_RESET=; C_BOLD=; C_BLUE=; C_GREEN=; C_YELLOW=; C_RED=; C_GRAY=
fi

# ---- Primitives --------------------------------------------------------------
ui_has_whiptail() { command -v whiptail >/dev/null 2>&1; }

ui_info()  { printf '%s[i]%s %s\n' "$C_BLUE"   "$C_RESET" "$*"; }
ui_ok()    { printf '%s[✓]%s %s\n' "$C_GREEN"  "$C_RESET" "$*"; }
ui_warn()  { printf '%s[!]%s %s\n' "$C_YELLOW" "$C_RESET" "$*"; }
ui_err()   { printf '%s[✗]%s %s\n' "$C_RED"    "$C_RESET" "$*" >&2; }

# Whiptail wrappers (fall back to plain text if whiptail absent)
ui_msg() {
  local title="$1" text="$2"
  if [[ "${A8M_NONINTERACTIVE:-0}" == "1" ]]; then
    printf '\n== %s ==\n%s\n' "$title" "$text"
    return 0
  fi
  if ui_has_whiptail; then
    whiptail --title "$title" --backtitle "$A8M_BACKTITLE" \
      --msgbox "$text" "$WT_HEIGHT" "$WT_WIDTH" 3>&1 1>&2 2>&3
  else
    echo "== $title =="; echo "$text"; read -r -p "Press Enter..." _ || true
  fi
}

ui_yesno() {
  local title="$1" text="$2"
  if [[ "${A8M_NONINTERACTIVE:-0}" == "1" ]]; then
    [[ "${A8M_YES:-0}" == "1" ]]
    return
  fi
  if ui_has_whiptail; then
    whiptail --title "$title" --backtitle "$A8M_BACKTITLE" \
      --yesno "$text" "$WT_HEIGHT" "$WT_WIDTH"
  else
    local ans; read -r -p "$title: $text [y/N] " ans || true
    [[ "$ans" =~ ^[Yy]$ ]]
  fi
}

ui_input() {
  local title="$1" prompt="$2" default="${3:-}"
  if [[ "${A8M_NONINTERACTIVE:-0}" == "1" ]]; then
    printf '%s\n' "$default"
    return 0
  fi
  if ui_has_whiptail; then
    whiptail --title "$title" --backtitle "$A8M_BACKTITLE" \
      --inputbox "$prompt" "$WT_HEIGHT" "$WT_WIDTH" "$default" 3>&1 1>&2 2>&3
  else
    local ans; read -r -p "$prompt [$default] " ans || true; echo "${ans:-$default}"
  fi
}

ui_menu() {  # ui_menu title prompt tag1 item1 tag2 item2 ...
  local title="$1" prompt="$2"; shift 2
  if [[ "${A8M_NONINTERACTIVE:-0}" == "1" ]]; then
    printf '%s\n' "${1:-}"
    return 0
  fi
  if ui_has_whiptail; then
    whiptail --title "$title" --backtitle "$A8M_BACKTITLE" \
      --menu "$prompt" "$WT_HEIGHT" "$WT_WIDTH" "$WT_MENU_HEIGHT" \
      "$@" 3>&1 1>&2 2>&3
  else
    echo "$prompt"; select t in "$@"; do echo "$t"; break; done
  fi
}

# ---- Step banner (Virtualmin-style) -----------------------------------------
A8M_STEP=0; A8M_STEP_TOTAL=15
ui_step() {
  A8M_STEP=$((A8M_STEP+1))
  local name="$1"
  local bar=""; local i
  for i in $(seq 1 "$A8M_STEP_TOTAL"); do
    (( i <= A8M_STEP )) && bar+="█" || bar+="░"
  done
  if [[ -t 1 ]] && [[ "${A8M_QUIET:-0}" != "1" ]]; then
    clear 2>/dev/null || true
    printf '\n%s%s Alt8Mail Enterprise Installer %s%s\n' "$C_BOLD" "$C_BLUE" "$A8M_VERSION" "$C_RESET"
    printf '%s┌────────────────────────────────────────────────────────────────────┐%s\n' "$C_GRAY" "$C_RESET"
    printf '%s│%s %-2s/%-2s  %-40s %s%s│%s\n' \
      "$C_GRAY" "$C_RESET" "$A8M_STEP" "$A8M_STEP_TOTAL" "$name" \
      "$C_GREEN" "$bar" "$C_RESET"
    printf '%s└────────────────────────────────────────────────────────────────────┘%s\n\n' "$C_GRAY" "$C_RESET"
  else
    printf '[%s/%s] %s\n' "$A8M_STEP" "$A8M_STEP_TOTAL" "$name"
  fi
}

# ---- Spinner for long ops ----------------------------------------------------
ui_spin() {  # ui_spin "message" cmd [args...]
  local msg="$1"; shift
  local logf="${A8M_LOG_DIR:-/var/log/a8m}/_spin.log"
  local frames=('⣾' '⣽' '⣻' '⢿' '⡿' '⣟' '⣯' '⣷')
  local i=0 pid rc
  "$@" >>"$logf" 2>&1 &
  pid=$!
  while kill -0 "$pid" 2>/dev/null; do
    if [[ -t 1 ]]; then
      printf '\r%s%s%s %s' "$C_BLUE" "${frames[i % ${#frames[@]}]}" "$C_RESET" "$msg"
    fi
    i=$((i+1)); sleep 0.12
  done
  if wait "$pid"; then rc=0; else rc=$?; fi
  if (( rc == 0 )); then
    [[ -t 1 ]] && printf '\r%s[✓]%s %s\n' "$C_GREEN" "$C_RESET" "$msg"
    return 0
  fi
  [[ -t 1 ]] && printf '\r%s[✗]%s %s (see %s)\n' "$C_RED" "$C_RESET" "$msg" "$logf"
  return "$rc"
}
