#!/usr/bin/env bash
# Create the managed mail storage root before the control plane starts.
# Older releases did not create this directory, and systemd refuses a missing
# ReadWritePaths target unless the unit marks it optional. Keep this idempotent:
# fresh installs already create the directory with the same ownership and mode.
set -Eeuo pipefail

root="$(grep -E '^CONTROL_STORAGE_ROOT=' /etc/a8m/control.env 2>/dev/null | tail -n1 | cut -d= -f2- || true)"
root="${root:-/srv/a8m-storage}"
if [[ "$root" != /* || "$root" == *..* ]]; then
  root=/srv/a8m-storage
fi

if install -d -m 0750 -o root -g vmail "$root" 2>/dev/null; then
  exit 0
fi
install -d -m 0750 "$root"
