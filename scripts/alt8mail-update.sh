#!/usr/bin/env bash
# Tag-based Alt8Mail release updater.
#
# It fetches signed/annotated Git tags from the configured private repository,
# builds a release from the selected tag, applies migrations, performs an
# atomic symlink switch and rolls back the application symlink if health checks
# fail. Database backups are written under /var/backups/alt8mail before any
# migration runs.
set -Eeuo pipefail

CONFIG=/etc/a8m/update.conf
INSTALL_ROOT=/opt/a8m
CURRENT="$INSTALL_ROOT/current"
WEB_ROOT=/var/www/a8m
CONTROL_LINK=/opt/alt8mail/bin/alt8mail-control
LOG=/var/log/alt8mail-update.log

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
die() { printf '[%s] ERROR: %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; exit 1; }

[[ "$(id -u)" -eq 0 ]] || die "run the updater as root"
[[ -r "$CONFIG" ]] || die "missing $CONFIG"
# shellcheck disable=SC1090
. "$CONFIG"
A8M_REPO="${A8M_REPO:-git@github.com:CryptRoute/Alt8Mail.git}"
A8M_GIT_KEY="${A8M_GIT_KEY:-/etc/a8m/github_readonly}"
A8M_CHANNEL="${A8M_CHANNEL:-stable}"
A8M_SERVICES="${A8M_SERVICES:-a8m-server a8m-admin alt8mail-control}"

CHECK_ONLY=0
TARGET_TAG=""
FORCE=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --check) CHECK_ONLY=1; shift ;;
    --to) TARGET_TAG="${2:-}"; shift 2 ;;
    --force) FORCE=1; shift ;;
    --help|-h)
      echo "usage: alt8mail-update [--check] [--to vX.Y.Z] [--force]"
      exit 0
      ;;
    *) die "unknown argument: $1" ;;
  esac
done

mkdir -p "$(dirname "$LOG")"
exec > >(tee -a "$LOG") 2>&1
trap 'die "update failed at line $LINENO"' ERR

CURRENT_VERSION="$(tr -d '[:space:]' < "$CURRENT/VERSION")"
[[ -n "$CURRENT_VERSION" ]] || die "cannot read the installed version"

if [[ ! -r "$A8M_GIT_KEY" ]]; then
  die "missing read-only Git key $A8M_GIT_KEY"
fi
export GIT_SSH_COMMAND="ssh -i $A8M_GIT_KEY -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o BatchMode=yes"

log "checking $A8M_REPO ($A8M_CHANNEL) against installed $CURRENT_VERSION"
TAG_REFS="$(git ls-remote --tags --refs "$A8M_REPO" 'refs/tags/v*' | awk '{print $2}')"
[[ -n "$TAG_REFS" ]] || die "no release tags found in $A8M_REPO"
LATEST_VERSION="$(printf '%s\n' "$TAG_REFS" | sed 's#refs/tags/v##' | sort -V | tail -1)"
LATEST_TAG="v$LATEST_VERSION"
if [[ -n "$TARGET_TAG" ]]; then
  [[ "$TARGET_TAG" == v* ]] || TARGET_TAG="v$TARGET_TAG"
  if ! printf '%s\n' "$TAG_REFS" | grep -qx "refs/tags/$TARGET_TAG"; then
    die "tag $TARGET_TAG does not exist"
  fi
  LATEST_TAG="$TARGET_TAG"
  LATEST_VERSION="${TARGET_TAG#v}"
fi

log "installed=$CURRENT_VERSION latest=$LATEST_VERSION"
if [[ "$CHECK_ONLY" -eq 1 ]]; then
  if [[ "$LATEST_VERSION" == "$CURRENT_VERSION" ]]; then
    echo "up-to-date $CURRENT_VERSION"
  else
    echo "update-available $CURRENT_VERSION -> $LATEST_VERSION"
  fi
  exit 0
fi
if [[ "$LATEST_VERSION" == "$CURRENT_VERSION" && "$FORCE" -eq 0 ]]; then
  log "already up to date"
  exit 0
fi

WORK="$(mktemp -d /tmp/alt8mail-update.XXXXXX)"
OLD_TARGET="$(readlink -f "$CURRENT")"
BACKUP_TS="$(date -u +%Y%m%dT%H%M%SZ)"
BACKUP_DIR="/var/backups/alt8mail/update-$BACKUP_TS"
install -d -m 0700 "$BACKUP_DIR"

cleanup() {
  rm -rf "$WORK"
}
trap cleanup EXIT

log "fetching $LATEST_TAG"
git clone --quiet --depth 1 --branch "$LATEST_TAG" "$A8M_REPO" "$WORK/src"
[[ -r "$WORK/src/scripts/build-release.sh" ]] || die "tag $LATEST_TAG has no build-release.sh"

log "building $LATEST_VERSION"
"$WORK/src/scripts/build-release.sh" "$LATEST_VERSION" "$WORK/dist" >/dev/null
NEW_RELEASE="$WORK/dist/alt8mail-$LATEST_VERSION"
[[ -x "$NEW_RELEASE/bin/a8m-server" ]] || die "release build did not produce a server binary"

log "backing up the database and configuration"
runuser -u postgres -- pg_dump -Fc -d a8m > "$BACKUP_DIR/database.dump"
tar -C /etc -czf "$BACKUP_DIR/config.tar.gz" a8m 2>/dev/null || true
tar -C /opt -czf "$BACKUP_DIR/control-config.tar.gz" alt8mail 2>/dev/null || true
printf '%s\n' "$CURRENT_VERSION" > "$BACKUP_DIR/previous-version"

TARGET_RELEASE="$INSTALL_ROOT/releases/$LATEST_VERSION"
if [[ ! -d "$TARGET_RELEASE" ]]; then
  install -d -m 0755 "$TARGET_RELEASE"
  cp -a "$NEW_RELEASE/." "$TARGET_RELEASE/"
  chmod 0755 "$TARGET_RELEASE/bin/"* 2>/dev/null || true
fi

# Install any updated unit files before restarting. Custom site configuration is
# intentionally not overwritten by an application update.
for unit in "$TARGET_RELEASE"/deploy/systemd/*.service "$TARGET_RELEASE"/deploy/systemd/*.timer; do
  [[ -e "$unit" ]] || continue
  install -m 0644 "$unit" "/etc/systemd/system/$(basename "$unit")"
done
systemctl daemon-reload

log "applying migrations for $LATEST_VERSION"
MIGRATION_TMP="$WORK/migrations"
install -d -m 0755 "$MIGRATION_TMP"
cp -a "$TARGET_RELEASE/migrations/." "$MIGRATION_TMP/"
chmod 0644 "$MIGRATION_TMP"/*.sql 2>/dev/null || true
for migration in "$MIGRATION_TMP"/*.sql; do
  version="$(basename "$migration" .sql)"
  applied="$(runuser -u postgres -- psql -d a8m -Atqc "SELECT COUNT(*) FROM schema_migrations WHERE version = '$version'" 2>/dev/null || echo 0)"
  if [[ "$applied" == "1" ]]; then
    continue
  fi
  log "applying migration $version"
  runuser -u postgres -- psql -d a8m -v ON_ERROR_STOP=1 -q -f "$migration"
done
runuser -u postgres -- psql -d a8m -v ON_ERROR_STOP=1 -q <<'SQL'
GRANT USAGE, CREATE ON SCHEMA public TO a8m_app;
DO $$
DECLARE r record;
BEGIN
  FOR r IN SELECT tablename FROM pg_tables WHERE schemaname = 'public' LOOP
    EXECUTE format('ALTER TABLE public.%I OWNER TO a8m_app', r.tablename);
  END LOOP;
  FOR r IN SELECT sequence_name FROM information_schema.sequences WHERE sequence_schema = 'public' LOOP
    EXECUTE format('ALTER SEQUENCE public.%I OWNER TO a8m_app', r.sequence_name);
  END LOOP;
END $$;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO a8m_app;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO a8m_app;
SQL

switch_release() {
  local target="$1"
  ln -sfn "$target" "$CURRENT"
  ln -sfn "$CURRENT/bin" "$INSTALL_ROOT/bin"
  ln -sfn "$CURRENT/admin" "$INSTALL_ROOT/admin"
  ln -sfn "$CURRENT" "$INSTALL_ROOT/app"
  rm -rf "$WEB_ROOT"
  ln -s "$CURRENT/public" "$WEB_ROOT"
  ln -sfn "$CURRENT/bin/alt8mail-control" "$CONTROL_LINK"
}

switch_release "$TARGET_RELEASE"
log "restarting services"
for svc in $A8M_SERVICES; do
  systemctl restart "$svc"
done

health() {
  local failed=0
  curl -fsS --max-time 10 http://127.0.0.1:8080/api/health >/dev/null || failed=1
  curl -fsS --max-time 10 http://127.0.0.1:8081/api/health >/dev/null || failed=1
  curl -kfsS --max-time 10 https://127.0.0.1:8808/healthz >/dev/null || failed=1
  return "$failed"
}

if ! health; then
  log "health checks failed; rolling back to $OLD_TARGET"
  switch_release "$OLD_TARGET"
  for svc in $A8M_SERVICES; do
    systemctl restart "$svc" || true
  done
  runuser -u postgres -- pg_restore --clean --if-exists --no-owner --no-privileges -d a8m "$BACKUP_DIR/database.dump" || true
  die "update failed health checks; application symlink and database restored from $BACKUP_DIR"
fi

log "update complete: $CURRENT_VERSION -> $LATEST_VERSION"
echo "updated $CURRENT_VERSION -> $LATEST_VERSION"
echo "backup=$BACKUP_DIR"
