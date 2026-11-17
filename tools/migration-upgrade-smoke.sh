#!/usr/bin/env bash
# Verify the Phase 1 upgrade path on a disposable PostgreSQL.
#
# Applies the current migration files up to a baseline version, upgrades with
# the remaining current migrations, rolls the new migrations back, reapplies
# them and checks schema_migrations. This keeps the smoke test self-contained
# after the public history reset removed the historical release tags.
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASE_VERSION="${A8M_UPGRADE_BASE_VERSION:-0060}"
IMAGE="${A8M_UPGRADE_POSTGRES_IMAGE:-postgres:15}"
CONTAINER="a8m-upgrade-smoke-$$"
DB="a8m" # migrations grant CONNECT on the deployment database name
PASSWORD="upgrade-smoke-password"

[[ "$BASE_VERSION" =~ ^[0-9]{4}$ ]] || { echo "invalid upgrade base version: $BASE_VERSION" >&2; exit 2; }
command -v docker >/dev/null || { echo "docker is required" >&2; exit 2; }

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "== starting disposable PostgreSQL ($IMAGE)"
docker run -d --name "$CONTAINER" -e POSTGRES_PASSWORD="$PASSWORD" -p 127.0.0.1::5432 "$IMAGE" >/dev/null
for _ in $(seq 1 60); do
  if docker exec "$CONTAINER" pg_isready -U postgres >/dev/null 2>&1; then break; fi
  sleep 1
done
docker exec "$CONTAINER" pg_isready -U postgres >/dev/null

psql_db() {
  docker exec -i "$CONTAINER" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 -q "$@"
}

echo "== creating installer roles and database"
docker exec -i "$CONTAINER" psql -U postgres -v ON_ERROR_STOP=1 -q <<SQL
CREATE ROLE a8m_app LOGIN PASSWORD 'upgrade-smoke';
CREATE ROLE a8m_mail LOGIN PASSWORD 'upgrade-smoke';
CREATE ROLE alt8mail_control LOGIN PASSWORD 'upgrade-smoke';
CREATE DATABASE $DB OWNER postgres;
SQL

base_files=()
for path in "$ROOT"/backend/migrations/[0-9][0-9][0-9][0-9]_*.sql; do
  name="$(basename "$path")"
  version="${name:0:4}"
  [[ "$version" < "$BASE_VERSION" || "$version" == "$BASE_VERSION" ]] && base_files+=("$name")
done
[[ ${#base_files[@]} -gt 0 ]] || { echo "no migrations at or below $BASE_VERSION" >&2; exit 1; }

echo "== applying ${#base_files[@]} migrations up to $BASE_VERSION"
for name in "${base_files[@]}"; do
  echo "   base $name"
  psql_db < "$ROOT/backend/migrations/$name" >/dev/null
done
base_max="${base_files[${#base_files[@]}-1]:0:4}"
echo "== base migration head: $base_max"

new_files=()
for path in "$ROOT"/backend/migrations/[0-9][0-9][0-9][0-9]_*.sql; do
  name="$(basename "$path")"
  version="${name:0:4}"
  [[ "$version" > "$base_max" ]] && new_files+=("$name")
done
if [[ ${#new_files[@]} -eq 0 ]]; then
  echo "no new migrations after $base_max" >&2
  exit 1
fi

echo "== applying ${#new_files[@]} current migrations"
for name in "${new_files[@]}"; do
  echo "   upgrade $name"
  psql_db < "$ROOT/backend/migrations/$name" >/dev/null
done

echo "== rolling back new migrations in reverse order"
for ((i=${#new_files[@]}-1; i>=0; i--)); do
  name="${new_files[$i]}"
  down="$ROOT/backend/migrations/rollback/${name%.sql}_down.sql"
  if [[ ! -f "$down" ]]; then
    echo "   no rollback for $name (skipped)"
    continue
  fi
  echo "   rollback $name"
  psql_db < "$down" >/dev/null
done

echo "== reapplying new migrations"
for name in "${new_files[@]}"; do
  echo "   reapply $name"
  psql_db < "$ROOT/backend/migrations/$name" >/dev/null
done

versions=()
for name in "${new_files[@]}"; do
  versions+=("${name%.sql}")
done
missing="$(psql_db -Atc "
  SELECT count(*) FROM (
    VALUES $(printf "('%s')," "${versions[@]}" | sed 's/,$//')
  ) AS expected(version)
  WHERE NOT EXISTS (SELECT 1 FROM schema_migrations m WHERE m.version = expected.version)
")"
if [[ "$missing" != "0" ]]; then
  echo "schema_migrations is missing $missing current migration(s)" >&2
  exit 1
fi

echo "== upgrade/rollback/reapply smoke passed"
echo "   database: $DB on disposable $IMAGE"
