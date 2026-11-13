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
env_file_value() { grep -E "^${1}=" "$2" 2>/dev/null | tail -n1 | cut -d= -f2- || true; }
set_env_if_missing() {
  local file="$1" key="$2" value="$3"
  [[ -n "$value" && -f "$file" ]] || return 0
  # A non-empty existing value is authoritative. An empty KEY= line is treated
  # as unset, matching install.sh, and is replaced by the fallback.
  if grep -qE "^${key}=.+" "$file" 2>/dev/null; then
    return 0
  fi
  sed -i "/^${key}=$/d" "$file" 2>/dev/null || true
  printf '%s=%s\n' "$key" "$value" >> "$file"
  log "added missing $key to $file"
}

[[ "$(id -u)" -eq 0 ]] || die "run the updater as root"
[[ -r "$CONFIG" ]] || die "missing $CONFIG"
# shellcheck disable=SC1090
. "$CONFIG"
A8M_REPO="${A8M_REPO:-git@github.com:CryptRoute/Alt8Mail.git}"
A8M_REPO_HTTPS="${A8M_REPO_HTTPS:-}"
A8M_GIT_KEY="${A8M_GIT_KEY:-/etc/a8m/github_readonly}"
A8M_CHANNEL="${A8M_CHANNEL:-stable}"
A8M_SERVICES="${A8M_SERVICES:-a8m-server a8m-admin alt8mail-control a8m-office-agent}"
A8M_RELEASE_BASE_URL="${A8M_RELEASE_BASE_URL:-https://github.com/CryptRoute/Alt8Mail/releases/download}"
A8M_PREFER_RELEASE_ASSET="${A8M_PREFER_RELEASE_ASSET:-1}"
A8M_MIN_FREE_MB="${A8M_MIN_FREE_MB:-1024}"

# The control plane launches this script from a transient systemd unit with no
# login environment. Set stable HOME/Go paths before SSH or the release build.
if [[ -z "${HOME:-}" ]]; then
  HOME="$(getent passwd "$(id -u)" 2>/dev/null | cut -d: -f6)"
  HOME="${HOME:-/root}"
  export HOME
fi
export PATH="/usr/local/go/bin:/usr/local/bin:${PATH:-/usr/sbin:/usr/bin:/sbin:/bin}"
if [[ -z "${GOPATH:-}" ]]; then export GOPATH="$HOME/go"; fi
if [[ -z "${GOMODCACHE:-}" ]]; then export GOMODCACHE="$GOPATH/pkg/mod"; fi
if [[ -z "${GOCACHE:-}" ]]; then export GOCACHE="$HOME/.cache/go-build"; fi

# Derive an HTTPS fallback for GitHub SSH remotes. This keeps existing hosts
# working when their update.conf predates A8M_REPO_HTTPS.
if [[ -z "$A8M_REPO_HTTPS" && "$A8M_REPO" =~ ^git@github\.com:(.+)$ ]]; then
  A8M_REPO_HTTPS="https://github.com/${BASH_REMATCH[1]}"
fi

repo_is_http() {
  [[ "$1" == http://* || "$1" == https://* ]]
}

repo_git() {
  local repo="$1"
  shift
  if repo_is_http "$repo"; then
    env -u GIT_SSH_COMMAND git "$@"
    return
  fi
  [[ -r "$A8M_GIT_KEY" ]] || return 1
  GIT_SSH_COMMAND="ssh -i \"$A8M_GIT_KEY\" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o BatchMode=yes" git "$@"
}

REPO_CANDIDATES=()
add_repo_candidate() {
  local candidate="$1" existing
  [[ -n "$candidate" ]] || return 0
  for existing in "${REPO_CANDIDATES[@]:-}"; do
    [[ "$existing" == "$candidate" ]] && return 0
  done
  REPO_CANDIDATES+=("$candidate")
}
add_repo_candidate "$A8M_REPO"
add_repo_candidate "$A8M_REPO_HTTPS"

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

log "checking $A8M_REPO ($A8M_CHANNEL) against installed $CURRENT_VERSION (SSH/HTTPS fallback)"
ACTIVE_REPO=""
TAG_REFS=""
for repo in "${REPO_CANDIDATES[@]}"; do
  if ! repo_is_http "$repo" && [[ ! -r "$A8M_GIT_KEY" ]]; then
    log "skipping SSH repository (missing key $A8M_GIT_KEY): $repo"
    continue
  fi
  if raw_tags="$(repo_git "$repo" ls-remote --tags --refs "$repo" 'refs/tags/v*' 2>&1)"; then
    tags="$(printf '%s\n' "$raw_tags" | awk '$2 ~ /^refs\/tags\// {print $2}' | sed '/^$/d')"
    if [[ -n "$tags" ]]; then
      ACTIVE_REPO="$repo"
      TAG_REFS="$tags"
      log "using repository: $repo"
      break
    fi
    log "no release tags found via $repo"
  else
    log "repository check failed via $repo: $(printf '%s\n' "$raw_tags" | tail -n 1)"
  fi
done
[[ -n "$ACTIVE_REPO" ]] || die "no release tags reachable through SSH or HTTPS"
[[ -n "$TAG_REFS" ]] || die "no release tags found in $ACTIVE_REPO"
LATEST_VERSION="$(printf '%s\n' "$TAG_REFS" | sed 's#refs/tags/v##' | sort -V | tail -1)"
LATEST_TAG="v$LATEST_VERSION"
if [[ -n "$TARGET_TAG" ]]; then
  [[ "$TARGET_TAG" == v* ]] || TARGET_TAG="v$TARGET_TAG"
  if ! printf '%s\n' "$TAG_REFS" | grep -x "refs/tags/$TARGET_TAG" >/dev/null; then
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

free_kb="$(df -Pk "$INSTALL_ROOT" 2>/dev/null | awk 'NR==2 {print $4}')"
mem_available_kb="$(awk '/^MemAvailable:/ {print $2}' /proc/meminfo 2>/dev/null || true)"
log "preflight: free_disk=${free_kb:-unknown}KB available_memory=${mem_available_kb:-unknown}KB"
if [[ -n "$free_kb" ]] && (( free_kb < A8M_MIN_FREE_MB * 1024 )); then
  die "not enough free disk for a safe update: ${free_kb} KB available, ${A8M_MIN_FREE_MB} MB required; stop the mail worker and remove orphan uploads first"
fi

# Prefer the prebuilt, checksummed GitHub release asset. This avoids running a
# full Go build on a small production host, where a long compile can exhaust
# RAM or disk. Any download/verification failure falls back to the source build.
fetch_release_asset() {
  local base="${A8M_RELEASE_BASE_URL%/}"
  local name="alt8mail-$LATEST_VERSION.tar.gz"
  local dir="$WORK/release"
  local url="$base/$LATEST_TAG/$name"
  local downloader=""
  if command -v curl >/dev/null 2>&1; then
    downloader="curl"
  elif command -v wget >/dev/null 2>&1; then
    downloader="wget"
  fi
  if [[ -z "$downloader" ]]; then
    log "neither curl nor wget is available for release asset download"
    return 1
  fi
  rm -rf "$dir"
  install -d -m 0755 "$dir"
  log "downloading published release asset $LATEST_TAG"
  if [[ "$downloader" == "curl" ]]; then
    if ! curl -fsSL --retry 3 --connect-timeout 15 --max-time 600 -o "$dir/$name" "$url"; then
      log "release asset download failed; falling back to source build"
      return 1
    fi
    if ! curl -fsSL --retry 3 --connect-timeout 15 --max-time 60 -o "$dir/$name.sha256" "$url.sha256"; then
      log "release checksum download failed; falling back to source build"
      return 1
    fi
  else
    if ! wget -q --timeout=60 --tries=3 -O "$dir/$name" "$url"; then
      log "release asset download failed; falling back to source build"
      return 1
    fi
    if ! wget -q --timeout=30 --tries=3 -O "$dir/$name.sha256" "$url.sha256"; then
      log "release checksum download failed; falling back to source build"
      return 1
    fi
  fi
  if ! (cd "$dir" && sha256sum -c "$name.sha256" >/dev/null 2>&1); then
    log "release asset checksum verification failed; falling back to source build"
    return 1
  fi
  if ! tar -xzf "$dir/$name" -C "$dir"; then
    log "release asset extraction failed; falling back to source build"
    return 1
  fi
  NEW_RELEASE="$dir/alt8mail-$LATEST_VERSION"
  if [[ ! -x "$NEW_RELEASE/bin/a8m-server" ]] ||
     [[ "$(tr -d '[:space:]' < "$NEW_RELEASE/VERSION" 2>/dev/null)" != "$LATEST_VERSION" ]]; then
    log "release asset is incomplete or has the wrong version; falling back to source build"
    rm -rf "$NEW_RELEASE"
    return 1
  fi
  log "using published release asset $LATEST_TAG"
  return 0
}

NEW_RELEASE=""
if [[ "$A8M_PREFER_RELEASE_ASSET" == "1" ]] && fetch_release_asset; then
  :
else
log "fetching $LATEST_TAG"
CLONE_ORDER=("$ACTIVE_REPO")
for repo in "${REPO_CANDIDATES[@]}"; do
  [[ "$repo" == "$ACTIVE_REPO" ]] && continue
  CLONE_ORDER+=("$repo")
done
CLONED=0
for repo in "${CLONE_ORDER[@]}"; do
  if ! repo_is_http "$repo" && [[ ! -r "$A8M_GIT_KEY" ]]; then
    log "skipping clone via SSH (missing key): $repo"
    continue
  fi
  rm -rf "$WORK/src"
  log "cloning $LATEST_TAG via $repo"
  if repo_git "$repo" -c advice.detachedHead=false clone --quiet --depth 1 --branch "$LATEST_TAG" "$repo" "$WORK/src"; then
    ACTIVE_REPO="$repo"
    CLONED=1
    break
  fi
  log "clone failed via $repo; trying the next transport"
done
[[ "$CLONED" -eq 1 ]] || die "could not clone $LATEST_TAG over SSH or HTTPS"
[[ -r "$WORK/src/scripts/build-release.sh" ]] || die "tag $LATEST_TAG has no build-release.sh"

log "building $LATEST_VERSION"
"$WORK/src/scripts/build-release.sh" "$LATEST_VERSION" "$WORK/dist" >/dev/null
NEW_RELEASE="$WORK/dist/alt8mail-$LATEST_VERSION"
fi
[[ -n "$NEW_RELEASE" && -x "$NEW_RELEASE/bin/a8m-server" ]] || die "release did not produce a server binary"

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

apply_restored_database_owners() {
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
}

restore_database_dump() {
  local dump="$1" release="$2" stamp old_database restore_log
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  old_database="a8m_pre_rollback_${stamp}"
  restore_log="/tmp/a8m-rollback-restore-${stamp}.log"
  runuser -u postgres -- psql -d postgres -v ON_ERROR_STOP=1 -q -c \
    "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = 'a8m' AND pid <> pg_backend_pid();" >/dev/null
  if ! runuser -u postgres -- psql -d postgres -v ON_ERROR_STOP=1 -q -c \
      "ALTER DATABASE a8m RENAME TO \"$old_database\""; then
    log "could not detach the current a8m database for restore"
    return 1
  fi
  if ! runuser -u postgres -- createdb -O a8m_app a8m; then
    runuser -u postgres -- psql -d postgres -v ON_ERROR_STOP=1 -q -c \
      "ALTER DATABASE \"$old_database\" RENAME TO a8m" || true
    return 1
  fi
  # Restoring into an empty database avoids pg_restore --clean dependency
  # failures when the failed update created tables that reference old ones.
  if runuser -u postgres -- pg_restore --no-owner --no-privileges \
      --single-transaction --exit-on-error -d a8m "$dump" >"$restore_log" 2>&1; then
    if apply_restored_database_owners && [[ -f "$release/deploy/postgresql/grants.sql" ]] \
        && runuser -u postgres -- psql -d a8m -v ON_ERROR_STOP=1 -q \
          -f "$release/deploy/postgresql/grants.sql"; then
      log "database restored into a fresh a8m; previous database retained as $old_database"
      return 0
    fi
    log "database restored, but ownership/grants failed; see $restore_log"
  else
    log "database restore failed; see $restore_log"
  fi
  # Never leave the application pointing at a half-restored database.
  runuser -u postgres -- psql -d postgres -v ON_ERROR_STOP=1 -q -c \
    "DROP DATABASE IF EXISTS a8m WITH (FORCE)" || true
  runuser -u postgres -- psql -d postgres -v ON_ERROR_STOP=1 -q -c \
    "ALTER DATABASE \"$old_database\" RENAME TO a8m" || true
  return 1
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
    if restore_database_dump "$restore_copy" "$OLD_TARGET"; then
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
  local line="$1" command="${2:-}"
  trap - ERR
  if [[ "$UPDATE_ACTIVE" -eq 1 ]]; then
    rollback "unexpected error at line $line: $command"
  fi
  die "update failed at line $line: $command"
}
trap 'on_error "$LINENO" "$BASH_COMMAND"' ERR

# Existing installs predate the managed storage root. Create it before the
# updated units start; systemd otherwise refuses to set up a ReadWritePaths
# entry that does not exist. The units also mark the path optional so a host
# can still boot and repair storage if the directory is removed later.
storage_root="$(env_file_value CONTROL_STORAGE_ROOT /etc/a8m/control.env)"
storage_root="${storage_root:-/srv/a8m-storage}"
if [[ "$storage_root" != /* || "$storage_root" == *..* ]]; then
  log "WARNING: invalid CONTROL_STORAGE_ROOT '$storage_root'; using /srv/a8m-storage"
  storage_root=/srv/a8m-storage
fi
if install -d -m 0750 -o root -g vmail "$storage_root" 2>/dev/null; then
  log "ensured mail storage root $storage_root"
else
  install -d -m 0750 "$storage_root"
  log "ensured mail storage root $storage_root (vmail group unavailable)"
fi

# Install updated unit files before restarting. Custom site configuration is
# intentionally not overwritten by an application update.
for unit in "$TARGET_RELEASE"/deploy/systemd/*.service "$TARGET_RELEASE"/deploy/systemd/*.timer; do
  [[ -e "$unit" ]] || continue
  install -m 0644 "$unit" "/etc/systemd/system/$(basename "$unit")"
done
systemctl daemon-reload
if [[ -f /etc/systemd/system/a8m-office-agent.service && " $A8M_SERVICES " != *" a8m-office-agent "* ]]; then
  A8M_SERVICES="$A8M_SERVICES a8m-office-agent"
fi

# Install the Redis-backed multimap rules once. Existing administrator rules
# are preserved; the Alt8Mail block is appended only when it is absent.
log "installing Rspamd threat-intelligence and local-domain rules"
MULTIMAP_CONF=/etc/rspamd/local.d/multimap.conf
install -d -m 0755 /etc/rspamd/local.d
if [[ ! -e "$MULTIMAP_CONF" ]]; then
  install -m 0644 "$TARGET_RELEASE/deploy/rspamd/multimap.conf" "$MULTIMAP_CONF"
elif ! grep -q 'A8M_REPORTED_SENDER' "$MULTIMAP_CONF"; then
  printf '\n# Alt8Mail threat-intelligence rules\n' >> "$MULTIMAP_CONF"
  cat "$TARGET_RELEASE/deploy/rspamd/multimap.conf" >> "$MULTIMAP_CONF"
fi
if ! grep -q 'A8M_LOCAL_DOMAIN_SPOOF' "$MULTIMAP_CONF"; then
  awk '/^# Domains hosted by this Alt8Mail instance/{copy=1} copy{print}' \
    "$TARGET_RELEASE/deploy/rspamd/multimap.conf" >> "$MULTIMAP_CONF"
fi
chmod 0644 "$MULTIMAP_CONF"
SETTINGS_CONF=/etc/rspamd/local.d/settings.conf
if [[ ! -e "$SETTINGS_CONF" ]]; then
  install -m 0644 "$TARGET_RELEASE/deploy/rspamd/settings.conf" "$SETTINGS_CONF"
elif ! grep -q 'a8m_authenticated_submission' "$SETTINGS_CONF"; then
  printf '\n# Alt8Mail local-domain guard\n' >> "$SETTINGS_CONF"
  cat "$TARGET_RELEASE/deploy/rspamd/settings.conf" >> "$SETTINGS_CONF"
fi
chmod 0644 "$SETTINGS_CONF"
touch /var/lib/rspamd/local_domains.map
chown _rspamd:_rspamd /var/lib/rspamd/local_domains.map 2>/dev/null || true
if command -v rspamadm >/dev/null 2>&1; then
  if rspamadm configtest >/var/log/alt8mail-rspamd-configtest.log 2>&1; then
    systemctl try-reload-or-restart rspamd || log "WARNING: Rspamd reload failed after installing threat-intelligence rules"
  else
    log "WARNING: Rspamd config test failed; see /var/log/alt8mail-rspamd-configtest.log"
  fi
else
  systemctl try-reload-or-restart rspamd || log "WARNING: Rspamd reload failed after installing threat-intelligence rules"
fi

# Keep public share, upload, calendar and add-on OAuth callback pages reachable
# when an older nginx site predates those routes. The helper backs out on a
# nginx -t failure.
if [[ -f "$TARGET_RELEASE/scripts/alt8mail-ensure-nginx-routes.sh" ]]; then
  bash "$TARGET_RELEASE/scripts/alt8mail-ensure-nginx-routes.sh" \
    || log "WARNING: could not install missing public nginx routes"
fi

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
  runuser -u postgres -- psql -d a8m -v ON_ERROR_STOP=1 -q -f - < "$migration"
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

# Installs that predate the Phase 1E license settings keep working without
# re-running the installer. Seed missing values in both directions without
# overwriting explicit configuration, always provision the webmail path (the
# token file itself may be absent, which is OSS mode), and normalize an
# existing token so the unprivileged webmail can read the Control-uploaded file
# after the restart.
APP_ENV_FILE=/etc/a8m/app.env
CONTROL_ENV_FILE=/etc/a8m/control.env
if [[ -f "$APP_ENV_FILE" && -f "$CONTROL_ENV_FILE" ]]; then
  control_license_file="$(env_file_value CONTROL_LICENSE_FILE "$CONTROL_ENV_FILE")"
  webmail_license_file="$(env_file_value ALT8MAIL_LICENSE_FILE "$APP_ENV_FILE")"
  shared_license_file="${control_license_file:-${webmail_license_file:-/etc/a8m/license.token}}"
  set_env_if_missing "$CONTROL_ENV_FILE" CONTROL_LICENSE_FILE "$shared_license_file"
  set_env_if_missing "$APP_ENV_FILE" ALT8MAIL_LICENSE_FILE "$shared_license_file"

  control_license_key="$(env_file_value CONTROL_LICENSE_PUBLIC_KEY "$CONTROL_ENV_FILE")"
  webmail_license_key="$(env_file_value ALT8MAIL_LICENSE_PUBLIC_KEY "$APP_ENV_FILE")"
  set_env_if_missing "$CONTROL_ENV_FILE" CONTROL_LICENSE_PUBLIC_KEY "$webmail_license_key"
  set_env_if_missing "$APP_ENV_FILE" ALT8MAIL_LICENSE_PUBLIC_KEY "$control_license_key"

  if [[ -n "$shared_license_file" && -f "$shared_license_file" ]]; then
    license_group="$(env_file_value CONTROL_LICENSE_GROUP "$CONTROL_ENV_FILE")"
    license_group="${license_group:-a8m}"
    if chown root:"$license_group" "$shared_license_file" 2>/dev/null; then
      chmod 0640 "$shared_license_file"
    else
      log "WARNING: could not set group $license_group on $shared_license_file"
    fi
  fi
fi

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
