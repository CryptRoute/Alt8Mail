#!/usr/bin/env bash
# Run govulncheck and fail unless every finding is explicitly allowlisted.
# Allowlisted entries must name an upstream vulnerability with no fixed version.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ALLOW_FILE="$ROOT/.govulncheck-allow"
BIN="${GOVULNCHECK_BIN:-govulncheck}"

if [[ ! -f "$ALLOW_FILE" ]]; then
  echo "missing $ALLOW_FILE" >&2
  exit 1
fi

output="$(cd "$ROOT/backend" && "$BIN" ./... 2>&1)"
status=$?
printf '%s\n' "$output"

if [[ $status -eq 0 ]]; then
  exit 0
fi

ids="$(printf '%s\n' "$output" | awk '/^Vulnerability #[0-9]+:/{print $3}' | sort -u)"
if [[ -z "$ids" ]]; then
  echo "govulncheck failed without a parseable vulnerability id" >&2
  exit 1
fi

failed=0
for id in $ids; do
  if grep -Eq "^[[:space:]]*${id}([[:space:]]|$)" "$ALLOW_FILE"; then
    echo "Allowed known advisory: $id" >&2
    continue
  fi
  echo "Unapproved vulnerability: $id" >&2
  failed=1
done

exit "$failed"
