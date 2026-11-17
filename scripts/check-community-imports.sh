#!/usr/bin/env bash
# Fail if the community build resolves or imports the private enterprise
# module. The private module is linked only by the tagged enterprise_hook.go
# files, which are excluded from the community build.
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PRIVATE="github.com/CryptRoute/Alt8Mail/backend/enterprise"

cd "$ROOT/backend"
deps="$(go list -deps ./cmd/server ./cmd/adminserver ./cmd/control 2>/dev/null || true)"
if grep -qx "$PRIVATE" <<<"$deps"; then
  echo "community build depends on the private module: $PRIVATE" >&2
  exit 1
fi

unexpected="$(grep -R -n --include='*.go' "$PRIVATE" . \
  | grep -v -- 'enterprise_hook.go' || true)"
if [[ -n "$unexpected" ]]; then
  echo "community source imports the private module outside the tagged hook:" >&2
  echo "$unexpected" >&2
  exit 1
fi

echo "community build excludes the private enterprise module"
