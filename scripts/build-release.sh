#!/usr/bin/env bash
# Build a self-contained Alt8Mail release tarball.
#
# Usage: scripts/build-release.sh [version] [output-directory]
#
# The release contains Linux binaries, static assets, migrations and the
# installer/deployment templates. It does not contain secrets or runtime data.
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-$(tr -d '[:space:]' < "$ROOT/VERSION")}"
OUT_DIR="${2:-$ROOT/dist}"
GOOS_TARGET="${GOOS_TARGET:-linux}"
GOARCH_TARGET="${GOARCH_TARGET:-amd64}"
NAME="alt8mail-$VERSION"
STAGE="$OUT_DIR/$NAME"

if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  echo "invalid version: $VERSION" >&2
  exit 2
fi
if [[ ! -d "$ROOT/backend" || ! -d "$ROOT/public" ]]; then
  echo "run from the Alt8Mail repository" >&2
  exit 2
fi

rm -rf "$STAGE" "$OUT_DIR/$NAME.tar.gz" "$OUT_DIR/$NAME.tar.gz.sha256"
mkdir -p "$STAGE/bin" "$STAGE/public" "$STAGE/admin" "$STAGE/migrations" "$STAGE/deploy" "$STAGE/scripts"

echo "building Alt8Mail $VERSION for $GOOS_TARGET/$GOARCH_TARGET"
(
  cd "$ROOT/backend"
  export CGO_ENABLED=0 GOOS="$GOOS_TARGET" GOARCH="$GOARCH_TARGET"
  go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$STAGE/bin/a8m-server" ./cmd/server
  go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$STAGE/bin/a8m-admin-server" ./cmd/adminserver
  go build -trimpath -ldflags "-s -w" -o "$STAGE/bin/a8m-admin" ./cmd/admin
  go build -trimpath -ldflags "-s -w" -o "$STAGE/bin/a8m-inituser" ./cmd/inituser
  go build -trimpath -ldflags "-s -w" -o "$STAGE/bin/a8m-mailprovision" ./cmd/mailprovision
  go build -trimpath -ldflags "-s -w" -o "$STAGE/bin/a8m-dkimprovision" ./cmd/dkimprovision
  go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$STAGE/bin/alt8mail-control" ./cmd/control
)

chmod 0755 "$STAGE/bin/"*
cp -a "$ROOT/public/." "$STAGE/public/"
cp -a "$ROOT/admin/." "$STAGE/admin/"
cp -a "$ROOT/backend/migrations/." "$STAGE/migrations/"
cp -a "$ROOT/deploy/." "$STAGE/deploy/"
cp -a "$ROOT/scripts/." "$STAGE/scripts/"
install -m 0644 "$ROOT/install.sh" "$STAGE/install.sh"
printf '%s
' "$VERSION" > "$STAGE/VERSION"
chmod 0644 "$STAGE/VERSION"
chmod 0755 "$STAGE/install.sh" "$STAGE/scripts/"*.sh

# Keep the control-plane config template with the deployment assets.
install -d "$STAGE/deploy/control"
cat > "$STAGE/deploy/control/control.env.example" <<'EOF'
# Rendered by install.sh at /etc/a8m/control.env.
CONTROL_ENV=production
CONTROL_LISTEN=0.0.0.0:8808
CONTROL_PUBLIC_URL=https://mail.example.com:8808
CONTROL_WEBMAIL_URL=https://mail.example.com
CONTROL_MAIL_HOSTNAME=mail.example.com
CONTROL_DNS_RESOLVER=1.1.1.1:53
CONTROL_MAIL_LOG=/var/log/mail.log
CONTROL_VMAIL_ROOT=/var/vmail
CONTROL_DKIM_DIR=/var/lib/rspamd/dkim
CONTROL_POSTFIX_DIR=/etc/postfix
CONTROL_DOVECOT_DIR=/etc/dovecot
CONTROL_SSL_DIR=/etc/ssl/alt8mail
CONTROL_TLS_CERT=/etc/ssl/alt8mail/control.crt
CONTROL_TLS_KEY=/etc/ssl/alt8mail/control.key
CONTROL_BACKUP_DIR=/var/backups/alt8mail
CONTROL_CRON_FILE=/etc/cron.d/alt8mail
CONTROL_CERTBOT_DIR=/etc/letsencrypt/live
CONTROL_CERTBOT_WEBROOT=/var/www/certbot
CONTROL_SMTP_ADDR=127.0.0.1:25
CONTROL_POLICY_LISTEN=127.0.0.1:10040
CONTROL_REDIS_PASSWORD_FILE=/opt/a8m/secrets/redis_password
CONTROL_RETENTION_DAYS=365
CONTROL_TRUSTED_PROXY=false
EOF

# Normalize line endings for text files. The repository is edited on Windows
# as well as Linux, but the installer, systemd units and mail daemons require LF.
find "$STAGE" -type f \( \
  -name '*.sh' -o -name '*.conf' -o -name '*.service' -o -name '*.timer' \
  -o -name '*.local' -o -name '*.go' -o -name '*.js' -o -name '*.mjs' \
  -o -name '*.css' -o -name '*.html' -o -name '*.json' -o -name '*.md' \
  -o -name '*.sql' -o -name '*.yml' -o -name '*.yaml' -o -name 'VERSION' \
\) -exec sed -i 's/\r$//' {} +

# Reproducible file metadata where the host tar supports it.
(cd "$OUT_DIR" && tar \
  --owner=0 --group=0 --numeric-owner \
  -czf "$NAME.tar.gz" "$NAME")
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$OUT_DIR" && sha256sum "$NAME.tar.gz" > "$NAME.tar.gz.sha256")
else
  (cd "$OUT_DIR" && shasum -a 256 "$NAME.tar.gz" > "$NAME.tar.gz.sha256")
fi
printf 'release=%s\ntarball=%s\n' "$VERSION" "$OUT_DIR/$NAME.tar.gz"
cat "$OUT_DIR/$NAME.tar.gz.sha256"
