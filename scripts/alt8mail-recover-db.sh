#!/usr/bin/env bash
# Restore an Alt8Mail pre-update database backup after an interrupted rollback.
#
# Usage: alt8mail-recover-db.sh /var/backups/alt8mail/update-YYYYMMDDTHHMMSSZ
#
# The script stops the application services, keeps the current database under
# a8m_pre_recovery_<timestamp>, creates a fresh a8m database, restores the dump,
# reapplies owners/grants and starts the services again.
set -Eeuo pipefail

CONFIG=/etc/a8m/update.conf
INSTALL_ROOT=/opt/a8m
CURRENT="$INSTALL_ROOT/current"
DATABASE=a8m
DATABASE_OWNER=a8m_app

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
die() { printf '[%s] ERROR: %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; exit 1; }

[[ "$(id -u)" -eq 0 ]] || die "run this recovery as root"
[[ -r "$CONFIG" ]] || die "missing $CONFIG"
# shellcheck disable=SC1090
. "$CONFIG"
SERVICES="${A8M_SERVICES:-a8m-server a8m-admin alt8mail-control}"
BACKUP_DIR="${1:-}"
[[ -n "$BACKUP_DIR" ]] || die "usage: alt8mail-recover-db.sh /var/backups/alt8mail/update-YYYYMMDDTHHMMSSZ"
[[ -s "$BACKUP_DIR/database.dump" ]] || die "missing $BACKUP_DIR/database.dump"
release="$(readlink -f "$CURRENT" 2>/dev/null || true)"
[[ -n "$release" ]] || die "cannot resolve the current release at $CURRENT"

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
old_database="a8m_pre_recovery_${stamp}"
restore_dump="/tmp/a8m-recovery-${stamp}.dump"
restore_log="/tmp/a8m-recovery-${stamp}.log"
trap 'rm -f "$restore_dump"' EXIT

for svc in $SERVICES; do
  systemctl stop "$svc" || true
done
install -m 0600 -o postgres -g postgres "$BACKUP_DIR/database.dump" "$restore_dump"
runuser -u postgres -- psql -d postgres -v ON_ERROR_STOP=1 -q -c \
  "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$DATABASE' AND pid <> pg_backend_pid();" >/dev/null

log "keeping the current database as $old_database"
runuser -u postgres -- psql -d postgres -v ON_ERROR_STOP=1 -q -c \
  "ALTER DATABASE $DATABASE RENAME TO \"$old_database\""

if ! runuser -u postgres -- createdb -O "$DATABASE_OWNER" "$DATABASE"; then
  runuser -u postgres -- psql -d postgres -v ON_ERROR_STOP=1 -q -c \
    "ALTER DATABASE \"$old_database\" RENAME TO $DATABASE" || true
  die "could not create a fresh $DATABASE database"
fi

log "restoring $BACKUP_DIR/database.dump into a fresh $DATABASE"
if ! runuser -u postgres -- pg_restore --no-owner --no-privileges \
    --single-transaction --exit-on-error -d "$DATABASE" "$restore_dump" >"$restore_log" 2>&1; then
  log "restore failed; see $restore_log"
  runuser -u postgres -- psql -d postgres -v ON_ERROR_STOP=1 -q -c \
    "DROP DATABASE IF EXISTS $DATABASE WITH (FORCE)" || true
  runuser -u postgres -- psql -d postgres -v ON_ERROR_STOP=1 -q -c \
    "ALTER DATABASE \"$old_database\" RENAME TO $DATABASE" || true
  for svc in $SERVICES; do systemctl start "$svc" || true; done
  die "the backup could not be restored; the previous database was put back"
fi

runuser -u postgres -- psql -d "$DATABASE" -v ON_ERROR_STOP=1 -q <<'SQL'
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
if [[ -f "$release/deploy/postgresql/grants.sql" ]]; then
  runuser -u postgres -- psql -d "$DATABASE" -v ON_ERROR_STOP=1 -q -f "$release/deploy/postgresql/grants.sql"
fi

for svc in $SERVICES; do
  systemctl start "$svc" || true
done
log "database restored; previous database retained as $old_database"
log "backup: $BACKUP_DIR"
