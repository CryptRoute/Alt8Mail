#!/usr/bin/env bash
# Raise ClamAV's stream/file scan limits so Cabinet uploads up to 300 MB can be
# scanned. Idempotent; restores the previous config if clamd fails to restart.
set -Eeuo pipefail

CONF="${A8M_CLAMD_CONF:-/etc/clamav/clamd.conf}"
LIMIT="320M"

if [[ "$(id -u)" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi
if [[ ! -f "$CONF" ]]; then
  echo "ClamAV config not found at $CONF; skipping"
  exit 0
fi

backup="$(mktemp)"
cp -a "$CONF" "$backup"
restore() {
  cp -a "$backup" "$CONF"
  systemctl restart clamav-daemon >/dev/null 2>&1 || true
}
trap 'rm -f "$backup"' EXIT

set_value() {
  local key="$1" value="$2"
  if grep -qE "^[[:space:]]*${key}[[:space:]]" "$CONF"; then
    sed -i -E "s|^[[:space:]]*${key}[[:space:]].*$|${key} ${value}|" "$CONF"
  else
    printf '\n%s %s\n' "$key" "$value" >> "$CONF"
  fi
}

set_value StreamMaxLength "$LIMIT"
set_value MaxFileSize "$LIMIT"
set_value MaxScanSize "$LIMIT"
set_value MaxScanTime 600000

if ! systemctl restart clamav-daemon; then
  restore
  echo "clamd rejected the new limits; restored the previous configuration" >&2
  exit 1
fi
for _ in $(seq 1 30); do
  if systemctl is-active --quiet clamav-daemon; then
    echo "ClamAV scan limits set to $LIMIT"
    exit 0
  fi
  sleep 1
done
restore
echo "clamd did not become ready with the new limits; restored the previous configuration" >&2
exit 1
