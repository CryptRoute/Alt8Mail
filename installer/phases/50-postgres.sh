#!/usr/bin/env bash
set -Eeuo pipefail

phase_postgres() {
  export PGOPTIONS="-c client_min_messages=warning"
  local pg_major="${A8M_PG_MAJOR:?PostgreSQL major version is not set}"
  local pg_conf_dir="/etc/postgresql/${pg_major}/main/conf.d"
  local pg_conf="/etc/postgresql/${pg_major}/main/postgresql.conf"

  ui_task "Preparing PostgreSQL ${pg_major}"
  run postgres -- systemctl enable --now postgresql || return 1
  run postgres -- systemctl restart postgresql || return 1

  install -d -m 0755 "$pg_conf_dir"
  if [[ -f "$A8M_SRC_DIR/deploy/postgresql/a8m.conf" ]]; then
    install -m 0644 "$A8M_SRC_DIR/deploy/postgresql/a8m.conf" "$pg_conf_dir/a8m.conf"
  fi
  if [[ -f "$pg_conf" ]] && ! grep -qE "^[[:space:]]*include_dir[[:space:]]*=[[:space:]]*'conf.d'" "$pg_conf"; then
    printf "\ninclude_dir = 'conf.d'\n" >>"$pg_conf"
  fi
  run postgres -- systemctl restart postgresql || return 1

  ui_task "Creating database roles and the a8m database"
  if ! psql_postgres -v ON_ERROR_STOP=1 -q <<SQL
DO \$\$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'a8m_app') THEN
    CREATE ROLE a8m_app LOGIN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'a8m_mail') THEN
    CREATE ROLE a8m_mail LOGIN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'alt8mail_control') THEN
    CREATE ROLE alt8mail_control LOGIN;
  END IF;
END
\$\$;
ALTER ROLE a8m_app WITH LOGIN PASSWORD '$APP_DB_PASSWORD';
ALTER ROLE a8m_mail WITH LOGIN PASSWORD '$MAIL_DB_PASSWORD';
ALTER ROLE alt8mail_control WITH LOGIN PASSWORD '$CONTROL_DB_PASSWORD';
SQL
  then
    ui_err "Could not create the PostgreSQL roles"
    return 1
  fi

  if ! psql_postgres -Atqc "SELECT 1 FROM pg_database WHERE datname='a8m'" | grep -qx 1; then
    runuser -u postgres -- createdb -O a8m_app a8m || return 1
  fi
  if ! psql_postgres -d a8m -v ON_ERROR_STOP=1 -q \
    -c 'CREATE EXTENSION IF NOT EXISTS citext; CREATE EXTENSION IF NOT EXISTS pgcrypto;'; then
    ui_err "Could not create the PostgreSQL extensions"
    return 1
  fi

  local mig_dir="$A8M_SRC_DIR/migrations"
  if [[ ! -d "$mig_dir" && -d "$A8M_SRC_DIR/backend/migrations" ]]; then
    mig_dir="$A8M_SRC_DIR/backend/migrations"
  fi
  if [[ ! -d "$mig_dir" ]]; then
    ui_err "Migrations directory not found in $A8M_SRC_DIR"
    return 1
  fi

  local migration_tmp=/tmp/a8m-migrations
  rm -rf "$migration_tmp"
  install -d -m 0755 "$migration_tmp"
  cp -a "$mig_dir/." "$migration_tmp/"
  chmod 0644 "$migration_tmp"/*.sql 2>/dev/null || true

  ui_task "Applying database migrations"
  local migration version applied total_migrations pending_migrations=0
  total_migrations="$(find "$migration_tmp" -maxdepth 1 -name '*.sql' | wc -l | tr -d ' ')"
  for migration in "$migration_tmp"/*.sql; do
    [[ -f "$migration" ]] || continue
    version="$(basename "$migration" .sql)"
    applied="$(psql_postgres -d a8m -Atqc \
      "SELECT COUNT(*) FROM schema_migrations WHERE version = '$version'" 2>/dev/null || echo 0)"
    if [[ "$applied" == "1" ]]; then
      log_write postgres "migration $version already applied"
      continue
    fi
    pending_migrations=$((pending_migrations + 1))
    if (( pending_migrations == 1 || pending_migrations % 10 == 0 )); then
      printf '  → migrations: %s/%s applied\n' "$pending_migrations" "$total_migrations"
    fi
    if ! psql_postgres -d a8m -v ON_ERROR_STOP=1 -q -f "$migration"; then
      ui_err "Migration failed: $version"
      rm -rf "$migration_tmp"
      return 1
    fi
  done
  rm -rf "$migration_tmp"

  if ! psql_postgres -d a8m -v ON_ERROR_STOP=1 -q <<'SQL'
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
  then
    ui_err "Could not assign database ownership"
    return 1
  fi
  if [[ -f "$A8M_SRC_DIR/deploy/postgresql/grants.sql" ]]; then
    local grants_file=/tmp/a8m-grants.sql
    install -m 0644 "$A8M_SRC_DIR/deploy/postgresql/grants.sql" "$grants_file"
    if ! psql_postgres -d a8m -v ON_ERROR_STOP=1 -q -f "$grants_file"; then
      rm -f "$grants_file"
      ui_err "Could not apply PostgreSQL grants"
      return 1
    fi
    rm -f "$grants_file"
  fi
  ui_ok "PostgreSQL ready; migrations applied"
}
