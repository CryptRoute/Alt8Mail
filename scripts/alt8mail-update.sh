#!/usr/bin/env bash
# Tag-based Alt8Mail release updater.
#
# It fetches release tags from the configured private repository, builds the
# selected tag, takes a database/configuration backup, applies migrations,
# performs an atomic symlink switch and rolls back the application and database
# if readiness checks fail.
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
# runuser changes into the caller's directory before dropping privileges; keep
# that directory world-executable so PostgreSQL client tools do not warn.
cd /tmp

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
git -c advice.detachedHead=false clone --quiet --depth 1 --branch "$LATEST_TAG" "$A8M_REPO" "$WORK/src"
[[ -r "$WORK/src/scripts/build-release.sh" ]] || die "tag $LATEST_TAG has no build-release.sh"

log "building $LATEST_VERSION"
"$WORK/src/scripts/build-release.sh" "$LATEST_VERSION" "$WORK/dist" >/dev/null
NEW_RELEASE="$WORK/dist/alt8mail-$LATEST_VERSION"
[[ -x "$NEW_RELEASE/bin/a8m-server" ]] || die "release build did not produce a server binary"

log "backing up the database and configuration"
runuser -u postgres -- pg_dump -Fc -d a8m > "$BACKUP_DIR/database.dump"
tar -C /etc -czf "$BACKUP_DIR/config.tar.gz" a8m 2>/dev/null || true
tar -C /opt -czf "$BACKUP_DIR/control-config.tar.gz" alt8mail 2>/dev/null || true
tar -C /etc/systemd/system -czf "$BACKUP_DIR/units.tar.gz" \
  a8m-server.service a8m-admin.service alt8mail-control.service \
  a8m-mailprovision.service a8m-mailprovision.timer \
  a8m-dkimprovision.service a8m-dkimprovision.timer 2>/dev/null || true
printf '%s\n' "$CURRENT_VERSION" > "$BACKUP_DIR/previous-version"

TARGET_RELEASE="$INSTALL_ROOT/releases/$LATEST_VERSION"
# Replace a stale staging directory left by an earlier failed attempt, but never
# rewrite the release that is currently serving traffic.
if [[ -d "$TARGET_RELEASE" && "$(readlink -f "$TARGET_RELEASE")" != "$OLD_TARGET" ]]; then
  log "refreshing staged release $TARGET_RELEASE"
  rm -rf "$TARGET_RELEASE"
fi
if [[ ! -d "$TARGET_RELEASE" ]]; then
  install -d -m 0755 "$TARGET_RELEASE"
  cp -a "$NEW_RELEASE/." "$TARGET_RELEASE/"
  chmod 0755 "$TARGET_RELEASE/bin/"* 2>/dev/null || true
fi

switch_release() {
  local target="$1"
  [[ -x "$target/bin/a8m-server" ]] || die "release $target has no server binary"
  ln -sfn "$target" "$CURRENT"
  ln -sfn "$target/bin" "$INSTALL_ROOT/bin"
  ln -sfn "$target/admin" "$INSTALL_ROOT/admin"
  ln -sfn "$target" "$INSTALL_ROOT/app"
  rm -rf "$WEB_ROOT"
  ln -s "$target/public" "$WEB_ROOT"
  install -d -m 0755 "$(dirname "$CONTROL_LINK")"
  ln -sfn "$target/bin/alt8mail-control" "$CONTROL_LINK"
}

rollback() {
  local reason="$1"
  local restored=0 restore_copy
  trap - ERR
  log "rolling back to $OLD_TARGET ($reason)"
  for svc in $A8M_SERVICES; do
    systemctl stop "$svc" || true
  done
  if [[ -s "$BACKUP_DIR/units.tar.gz" ]]; then
    tar -C /etc/systemd/system -xzf "$BACKUP_DIR/units.tar.gz" 2>/dev/null || true
    systemctl daemon-reload || true
  fi
  switch_release "$OLD_TARGET"
  restore_copy="/tmp/a8m-restore-$$.dump"
  if install -m 0600 -o postgres -g postgres "$BACKUP_DIR/database.dump" "$restore_copy"; then
    if runuser -u postgres -- pg_restore --clean --if-exists -d a8m "$restore_copy"; then
      restored=1
    fi
  fi
  rm -f "$restore_copy"
  for svc in $A8M_SERVICES; do
    systemctl start "$svc" || true
  done
  if [[ "$restored" -ne 1 ]]; then
    die "rollback incomplete: application restored to $OLD_TARGET but the database restore failed; backup is in $BACKUP_DIR"
  fi
  die "update failed ($reason); application and database restored to $OLD_TARGET (backup: $BACKUP_DIR)"
}

UPDATE_ACTIVE=0
on_error() {
  local line="$1"
  trap - ERR
  if [[ "$UPDATE_ACTIVE" -eq 1 ]]; then
    rollback "unexpected error at line $line"
  fi
  die "update failed at line $line"
}
trap 'on_error $LINENO' ERR

# Install updated unit files before restarting. Custom site configuration is
# intentionally not overwritten by an application update.
for unit in "$TARGET_RELEASE"/deploy/systemd/*.service "$TARGET_RELEASE"/deploy/systemd/*.timer; do
  [[ -e "$unit" ]] || continue
  install -m 0644 "$unit" "/etc/systemd/system/$(basename "$unit")"
done
systemctl daemon-reload

UPDATE_ACTIVE=1
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
SQL
runuser -u postgres -- psql -d a8m -v ON_ERROR_STOP=1 -q -f - < "$TARGET_RELEASE/deploy/postgresql/grants.sql"

switch_release "$TARGET_RELEASE"
log "restarting services"
for svc in $A8M_SERVICES; do
  systemctl restart "$svc"
done

health_once() {
  local body
  body="$(curl -fsS --max-time 3 http://127.0.0.1:8080/api/health 2>/dev/null)" || return 1
  [[ "$body" == *"\"version\":\"$LATEST_VERSION\""* ]] || return 1
  body="$(curl -fsS --max-time 3 http://127.0.0.1:8081/api/health 2>/dev/null)" || return 1
  [[ "$body" == *"\"version\":\"$LATEST_VERSION\""* ]] || return 1
  body="$(curl -kfsS --max-time 3 https://127.0.0.1:8808/healthz 2>/dev/null)" || return 1
  [[ "$body" == *"\"version\":\"$LATEST_VERSION\""* ]] || return 1
}

ready=0
for attempt in $(seq 1 30); do
  if health_once; then
    ready=1
    break
  fi
  sleep 2
done
if [[ "$ready" -ne 1 ]]; then
  rollback "readiness checks did not pass within 60 seconds"
fi

# Refresh the installed updater atomically. The running shell keeps its open
# inode, so replacing the file here is safe.
updater_tmp="$(mktemp /usr/local/bin/.alt8mail-update.XXXXXX)"
install -m 0755 "$CURRENT/scripts/alt8mail-update.sh" "$updater_tmp"
mv -f "$updater_tmp" /usr/local/bin/alt8mail-update

UPDATE_ACTIVE=0
log "update complete: $CURRENT_VERSION -> $LATEST_VERSION"
echo "updated $CURRENT_VERSION -> $LATEST_VERSION"
echo "backup=$BACKUP_DIR"
