#!/usr/bin/env bash
# Alt8Mail interactive installer for Debian 12.
#
# The installer is intentionally conservative: it refuses to run as a normal
# user, keeps all generated credentials out of stdout, and is safe to re-run.
# It can install from a source checkout or from a prebuilt release directory.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
umask 027

DOMAIN="${A8M_DOMAIN:-}"
MAIL_HOSTNAME="${A8M_MAIL_HOSTNAME:-}"
ADMIN_EMAIL="${A8M_ADMIN_EMAIL:-}"
ADMIN_NAME="${A8M_ADMIN_NAME:-Alt8Mail Owner}"
CONTROL_EMAIL="${A8M_CONTROL_EMAIL:-sysadmin@alt8mail.local}"
TENANT_NAME="${A8M_TENANT_NAME:-Alt8Mail}"
TENANT_SLUG="${A8M_TENANT_SLUG:-a8m}"
TLS_MODE="${A8M_TLS_MODE:-letsencrypt}"
TLS_EMAIL="${A8M_TLS_EMAIL:-}"
NONINTERACTIVE=0
SKIP_PACKAGES=0
SKIP_CERTBOT=0
SOURCE_DIR=""
VERSION=""
PUBLIC_IP=""

INSTALL_ROOT=/opt/a8m
CONFIG_ROOT=/etc/a8m
CONTROL_ROOT=/opt/alt8mail
WEB_ROOT=/var/www/a8m
SECRETS_DIR=$INSTALL_ROOT/secrets
RELEASES_DIR=$INSTALL_ROOT/releases
SERVICE_USER=a8m
LOG_FILE=/var/log/a8m-install.log

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
warn() { printf '[%s] WARNING: %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
die() { printf '[%s] ERROR: %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; exit 1; }
usage() {
  cat <<'USAGE'
Usage: sudo bash install.sh [options]

Options:
  --domain NAME             Mail domain (for example a8m.example.com)
  --hostname NAME           Mail hostname (default: mail.<domain>)
  --admin-email EMAIL       First customer/admin owner (default: owner@<domain>)
  --admin-name NAME         Owner display name
  --control-email EMAIL     First System Control superadmin
  --tenant-name NAME        Organization name
  --tenant-slug SLUG        Organization slug
  --tls letsencrypt|selfsigned
  --tls-email EMAIL         Let's Encrypt account email
  --source-dir DIR          Source/release directory (default: script directory)
  --non-interactive         Never prompt; fail if a required value is missing
  --skip-packages           Skip apt package installation (for testing)
  --skip-certbot            Use a self-signed certificate
  --help                    Show this help

Environment equivalents: A8M_DOMAIN, A8M_MAIL_HOSTNAME, A8M_ADMIN_EMAIL,
A8M_ADMIN_NAME, A8M_CONTROL_EMAIL, A8M_TENANT_NAME, A8M_TENANT_SLUG,
A8M_TLS_MODE, A8M_TLS_EMAIL.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain) DOMAIN="${2:-}"; shift 2 ;;
    --hostname) MAIL_HOSTNAME="${2:-}"; shift 2 ;;
    --admin-email) ADMIN_EMAIL="${2:-}"; shift 2 ;;
    --admin-name) ADMIN_NAME="${2:-}"; shift 2 ;;
    --control-email) CONTROL_EMAIL="${2:-}"; shift 2 ;;
    --tenant-name) TENANT_NAME="${2:-}"; shift 2 ;;
    --tenant-slug) TENANT_SLUG="${2:-}"; shift 2 ;;
    --tls) TLS_MODE="${2:-}"; shift 2 ;;
    --tls-email) TLS_EMAIL="${2:-}"; shift 2 ;;
    --source-dir) SOURCE_DIR="${2:-}"; shift 2 ;;
    --non-interactive) NONINTERACTIVE=1; shift ;;
    --skip-packages) SKIP_PACKAGES=1; shift ;;
    --skip-certbot) SKIP_CERTBOT=1; TLS_MODE=selfsigned; shift ;;
    --help|-h) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ "$(id -u)" -eq 0 ]] || die "run the installer as root"
[[ -r /etc/os-release ]] || die "cannot identify the operating system"
# shellcheck disable=SC1091
. /etc/os-release
[[ "${ID:-}" == "debian" && "${VERSION_ID:-}" == "12" ]] || die "Alt8Mail installer targets Debian 12 (bookworm); found ${PRETTY_NAME:-unknown}"

if [[ "$NONINTERACTIVE" -eq 0 && -t 0 ]]; then
  [[ -n "$DOMAIN" ]] || read -r -p "Mail domain: " DOMAIN
  [[ -n "$MAIL_HOSTNAME" ]] || MAIL_HOSTNAME="mail.$DOMAIN"
  [[ -n "$ADMIN_EMAIL" ]] || read -r -p "First owner email [$ADMIN_EMAIL]: " input && ADMIN_EMAIL="${input:-$ADMIN_EMAIL}"
  [[ -n "$ADMIN_EMAIL" ]] || ADMIN_EMAIL="owner@$DOMAIN"
  [[ -n "$TLS_EMAIL" ]] || read -r -p "Let's Encrypt email [$ADMIN_EMAIL]: " input && TLS_EMAIL="${input:-$ADMIN_EMAIL}"
fi

DOMAIN="${DOMAIN,,}"
DOMAIN="${DOMAIN%.}"
MAIL_HOSTNAME="${MAIL_HOSTNAME,,}"
MAIL_HOSTNAME="${MAIL_HOSTNAME%.}"
[[ -n "$MAIL_HOSTNAME" ]] || MAIL_HOSTNAME="mail.$DOMAIN"
ADMIN_EMAIL="${ADMIN_EMAIL,,}"
[[ -n "$ADMIN_EMAIL" ]] || ADMIN_EMAIL="owner@$DOMAIN"
CONTROL_EMAIL="${CONTROL_EMAIL,,}"
TLS_EMAIL="${TLS_EMAIL:-$ADMIN_EMAIL}"
TLS_MODE="${TLS_MODE,,}"

[[ "$DOMAIN" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?\.[a-z]{2,}$ ]] || die "invalid mail domain: $DOMAIN"
[[ "$MAIL_HOSTNAME" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]] || die "invalid mail hostname: $MAIL_HOSTNAME"
[[ "$ADMIN_EMAIL" =~ ^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$ ]] || die "invalid owner email: $ADMIN_EMAIL"
[[ "$ADMIN_EMAIL" == *"@$DOMAIN" ]] || die "owner email must use $DOMAIN"
[[ "$CONTROL_EMAIL" =~ ^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$ ]] || die "invalid control-plane email"
[[ "$TLS_MODE" == "letsencrypt" || "$TLS_MODE" == "selfsigned" ]] || die "--tls must be letsencrypt or selfsigned"
[[ "$TENANT_SLUG" =~ ^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$ ]] || die "invalid tenant slug"

if [[ -n "$SOURCE_DIR" ]]; then
  SOURCE_DIR="$(cd "$SOURCE_DIR" && pwd)"
elif [[ -x "$ROOT_DIR/bin/a8m-server" ]]; then
  SOURCE_DIR="$ROOT_DIR"
elif [[ -d "$ROOT_DIR/backend" ]]; then
  SOURCE_DIR="$ROOT_DIR"
else
  die "provide --source-dir pointing to a source checkout or extracted release"
fi
[[ -r "$SOURCE_DIR/VERSION" ]] || die "missing VERSION in $SOURCE_DIR"
chmod 0755 "$SOURCE_DIR/bin/"* 2>/dev/null || true
VERSION="$(tr -d '[:space:]' < "$SOURCE_DIR/VERSION")"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+ ]] || die "invalid VERSION: $VERSION"

mkdir -p "$(dirname "$LOG_FILE")"
exec > >(tee -a "$LOG_FILE") 2>&1
trap 'die "installation failed at line $LINENO"' ERR

log "Alt8Mail $VERSION installer starting for $DOMAIN"

# ---------------------------------------------------------------------------
# Host and network
# ---------------------------------------------------------------------------
if ! command -v curl >/dev/null 2>&1; then
  log "installing curl for preflight"
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ca-certificates curl
fi
cd /tmp
PUBLIC_IP="$(curl -4fsS --max-time 8 https://api.ipify.org 2>/dev/null || true)"
if [[ -z "$PUBLIC_IP" ]]; then
  PUBLIC_IP="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i=1;i<=NF;i++) if ($i=="src") {print $(i+1); exit}}')"
fi
[[ -n "$PUBLIC_IP" ]] || die "could not determine the public IPv4 address"

if [[ "$SKIP_PACKAGES" -eq 0 ]]; then
  log "updating apt indexes"
  apt-get update -qq
  log "installing base packages"
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
    ca-certificates curl gnupg lsb-release apt-transport-https software-properties-common \
    ufw fail2ban unattended-upgrades chrony jq git rsync unzip zip build-essential \
    dnsutils htop ncdu vim-tiny acl openssl rsyslog python3-systemd swaks netcat-openbsd
else
  log "skipping package installation by request"
fi

timedatectl set-timezone UTC
hostnamectl set-hostname "$MAIL_HOSTNAME"
if ! grep -qE "^127\\.0\\.1\\.1[[:space:]]+$MAIL_HOSTNAME([[:space:]]|$)" /etc/hosts; then
  sed -i '/^127\.0\.1\.1[[:space:]]/d' /etc/hosts
  printf '127.0.1.1 %s %s\n' "$MAIL_HOSTNAME" "$DOMAIN" >> /etc/hosts
fi
printf '%s\n' "$DOMAIN" > /etc/mailname

if [[ "$(swapon --show=SIZE --noheadings --bytes 2>/dev/null | awk '{s+=$1} END {print s+0}')" -lt 2147483648 ]]; then
  if [[ ! -f /swapfile ]]; then
    log "adding a 2 GiB swap file"
    fallocate -l 2G /swapfile
    chmod 600 /swapfile
    mkswap /swapfile >/dev/null
    swapon /swapfile
    grep -q '^/swapfile ' /etc/fstab || printf '/swapfile none swap sw 0 0\n' >> /etc/fstab
  fi
fi

if [[ "$SKIP_PACKAGES" -eq 0 ]]; then
  log "installing Rspamd repository"
  install -d -m 0755 /usr/share/keyrings
  curl -fsSL https://rspamd.com/apt-stable/gpg.key | gpg --batch --yes --dearmor -o /usr/share/keyrings/rspamd.gpg
  chmod 0644 /usr/share/keyrings/rspamd.gpg
  printf 'deb [signed-by=/usr/share/keyrings/rspamd.gpg] http://rspamd.com/apt-stable/ bookworm main\n' > /etc/apt/sources.list.d/rspamd.list
  apt-get update -qq
  log "installing mail stack packages"
  echo 'postfix postfix/main_mailer_type select Internet Site' | debconf-set-selections
  echo "postfix postfix/mailname string $MAIL_HOSTNAME" | debconf-set-selections
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
    postgresql postgresql-contrib postgresql-client redis-server redis-tools \
    dovecot-core dovecot-imapd dovecot-pop3d dovecot-lmtpd dovecot-managesieved \
    dovecot-sieve dovecot-pgsql postfix postfix-pgsql rspamd clamav clamav-daemon \
    clamav-freshclam nginx certbot
fi

# Go is required for source installs and for the tag-based updater.
install_go() {
  local arch go_version go_json go_sha archive
  arch="$(uname -m)"
  case "$arch" in
    x86_64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) die "unsupported architecture: $arch" ;;
  esac
  if command -v go >/dev/null 2>&1 && go version | awk '{print $3}' | grep -qE '^go1\.(2[6-9]|[3-9][0-9])'; then
    log "using existing $(go version)"
    return
  fi
  go_json="$(curl -fsSL 'https://go.dev/dl/?mode=json')"
  go_version="$(printf '%s' "$go_json" | jq -r '[.[] | select(.stable == true)][0].version')"
  [[ -n "$go_version" && "$go_version" != "null" ]] || die "could not determine the current Go release"
  go_sha="$(printf '%s' "$go_json" | jq -r --arg v "$go_version" --arg a "$arch" '
    .[] | select(.version == $v) | .files[] |
    select(.os == "linux" and .arch == $a and .kind == "archive") | .sha256' | head -1)"
  [[ -n "$go_sha" && "$go_sha" != "null" ]] || die "could not find a Go SHA-256 for $go_version/$arch"
  archive="/tmp/${go_version}.linux-${arch}.tar.gz"
  log "downloading $go_version"
  if ! curl -fsSL -o "$archive" "https://go.dev/dl/${go_version}.linux-${arch}.tar.gz"; then
    # Some hosting networks block the Google download endpoint. The mirror is
    # only trusted because the official sha256 from go.dev is checked below.
    warn "official Go download unavailable; trying the Aliyun mirror"
    curl -fsSL -o "$archive" "https://mirrors.aliyun.com/golang/${go_version}.linux-${arch}.tar.gz"
  fi
  printf '%s  %s\n' "$go_sha" "$archive" | sha256sum -c - >/dev/null
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "$archive"
  ln -sfn /usr/local/go/bin/go /usr/local/bin/go
  rm -f "$archive"
}
install_go

# ---------------------------------------------------------------------------
# Users, directories and secrets
# ---------------------------------------------------------------------------
if ! getent group "$SERVICE_USER" >/dev/null; then groupadd --system "$SERVICE_USER"; fi
if ! id "$SERVICE_USER" >/dev/null 2>&1; then
  useradd --system --home-dir "$INSTALL_ROOT" --shell /usr/sbin/nologin --gid "$SERVICE_USER" "$SERVICE_USER"
fi
if ! getent group vmail >/dev/null; then groupadd --system --gid 5000 vmail; fi
if ! id vmail >/dev/null 2>&1; then
  useradd --system --uid 5000 --gid 5000 --home-dir /var/vmail --shell /usr/sbin/nologin vmail
fi

install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$INSTALL_ROOT"
chmod 0755 "$INSTALL_ROOT"
install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" "$SECRETS_DIR" "$INSTALL_ROOT/uploads"
install -d -m 0755 "$RELEASES_DIR" "$CONFIG_ROOT" "$CONTROL_ROOT" "$CONTROL_ROOT/bin"
install -d -m 0700 /var/backups/alt8mail /etc/ssl/alt8mail /etc/cron.d
install -d -m 0750 -o vmail -g vmail /var/vmail
install -d -m 0755 /var/www/certbot

rand_hex() { openssl rand -hex "$1"; }
rand_password() { openssl rand -base64 36 | tr -d '/+=\n' | cut -c1-28; }
read_or_create_secret() {
  local path="$1" length="${2:-32}"
  if [[ ! -s "$path" ]]; then
    rand_hex "$length" > "$path"
    chmod 600 "$path"
    chown "$SERVICE_USER:$SERVICE_USER" "$path"
  fi
  tr -d '\n' < "$path"
}
APP_DB_PASSWORD="$(read_or_create_secret "$SECRETS_DIR/postgres_app_password" 32)"
MAIL_DB_PASSWORD="$(read_or_create_secret "$SECRETS_DIR/postgres_mail_password" 32)"
CONTROL_DB_PASSWORD="$(read_or_create_secret "$SECRETS_DIR/postgres_control_password" 32)"
REDIS_PASSWORD="$(read_or_create_secret "$SECRETS_DIR/redis_password" 32)"
SESSION_SECRET="$(read_or_create_secret "$SECRETS_DIR/session_secret" 32)"
CSRF_SECRET="$(read_or_create_secret "$SECRETS_DIR/csrf_secret" 32)"
CONTROL_SESSION_SECRET="$(read_or_create_secret "$SECRETS_DIR/control_session_secret" 32)"
CONTROL_CSRF_SECRET="$(read_or_create_secret "$SECRETS_DIR/control_csrf_secret" 32)"
CONTROL_SECRET_KEY="$(read_or_create_secret "$SECRETS_DIR/control_secret_key" 32)"
IMAP_MASTER_PASSWORD="$(read_or_create_secret "$SECRETS_DIR/imap_master_password" 32)"
OWNER_WEB_PASSWORD="$(read_or_create_secret "$SECRETS_DIR/owner_web_password" 24)"
OWNER_MAIL_PASSWORD="$(read_or_create_secret "$SECRETS_DIR/owner_mail_password" 32)"
CONTROL_ADMIN_PASSWORD="$(read_or_create_secret "$SECRETS_DIR/control_admin_password" 24)"

# The app reads these files through the a8m service account.
chown -R "$SERVICE_USER:$SERVICE_USER" "$SECRETS_DIR"
chmod 0700 "$SECRETS_DIR"
find "$SECRETS_DIR" -type f -exec chmod 0600 {} \;

# ---------------------------------------------------------------------------
# PostgreSQL
# ---------------------------------------------------------------------------
log "configuring PostgreSQL"
install -d -m 0755 /etc/postgresql/15/main/conf.d
install -m 0644 "$SOURCE_DIR/deploy/postgresql/a8m.conf" /etc/postgresql/15/main/conf.d/a8m.conf
systemctl enable --now postgresql >/dev/null
systemctl restart postgresql

runuser -u postgres -- psql -v ON_ERROR_STOP=1 -q <<SQL
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
if ! runuser -u postgres -- psql -Atqc "SELECT 1 FROM pg_database WHERE datname='a8m'" | grep -qx 1; then
  runuser -u postgres -- createdb -O a8m_app a8m
fi
runuser -u postgres -- psql -d a8m -v ON_ERROR_STOP=1 -q -c 'CREATE EXTENSION IF NOT EXISTS citext; CREATE EXTENSION IF NOT EXISTS pgcrypto;'

log "applying database migrations"
MIGRATION_TMP=/tmp/a8m-migrations
rm -rf "$MIGRATION_TMP"
install -d -m 0755 "$MIGRATION_TMP"
cp -a "$SOURCE_DIR/migrations/." "$MIGRATION_TMP/"
chmod 0644 "$MIGRATION_TMP"/*.sql
for migration in "$MIGRATION_TMP"/*.sql; do
  migration_version="$(basename "$migration" .sql)"
  applied="$(runuser -u postgres -- psql -d a8m -Atqc "SELECT COUNT(*) FROM schema_migrations WHERE version = '$migration_version'" 2>/dev/null || echo 0)"
  if [[ "$applied" == "1" ]]; then
    log "migration $migration_version already applied; skipping"
    continue
  fi
  runuser -u postgres -- psql -d a8m -v ON_ERROR_STOP=1 -q -f "$migration"
done
rm -rf "$MIGRATION_TMP"

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
runuser -u postgres -- psql -d a8m -v ON_ERROR_STOP=1 -q -f - < "$SOURCE_DIR/deploy/postgresql/grants.sql"

# ---------------------------------------------------------------------------
# Redis, Dovecot, Postfix, Rspamd and ClamAV
# ---------------------------------------------------------------------------
log "configuring Redis"
install -d -m 0755 /etc/redis
sed "s|__REDIS_PASSWORD__|$REDIS_PASSWORD|g" "$SOURCE_DIR/deploy/redis/a8m.conf" > /etc/redis/a8m.conf
chmod 0640 /etc/redis/a8m.conf
if ! grep -q '^include /etc/redis/a8m.conf' /etc/redis/redis.conf; then
  printf '\ninclude /etc/redis/a8m.conf\n' >> /etc/redis/redis.conf
fi
systemctl enable --now redis-server >/dev/null
systemctl restart redis-server

ML_HASH="$(printf '%s\n%s\n' "$IMAP_MASTER_PASSWORD" "$IMAP_MASTER_PASSWORD" | doveadm pw -s SHA512-CRYPT | tr -d '\r' | awk '/^\{SHA512-CRYPT\}/ {print; exit}')"
[[ -n "$ML_HASH" ]] || die "could not create the Dovecot master-user hash"
umask 077
printf 'a8m-worker:%s\n' "$ML_HASH" > /etc/dovecot/master-users
chown root:dovecot /etc/dovecot/master-users
chmod 0640 /etc/dovecot/master-users

log "configuring Dovecot"
install -m 0644 "$SOURCE_DIR/deploy/dovecot/10-auth.conf" /etc/dovecot/conf.d/10-auth.conf
install -m 0644 "$SOURCE_DIR/deploy/dovecot/10-ssl.conf" /etc/dovecot/conf.d/10-ssl.conf
install -m 0644 "$SOURCE_DIR/deploy/dovecot/10-mail.conf" /etc/dovecot/conf.d/10-mail.conf
sed "s|__DOMAIN__|$DOMAIN|g" "$SOURCE_DIR/deploy/dovecot/99-a8m.conf" > /etc/dovecot/conf.d/99-a8m.conf
install -m 0644 "$SOURCE_DIR/deploy/dovecot/05-a8m-master.conf" /etc/dovecot/conf.d/05-a8m-master.conf
sed "s|__DB_PASSWORD__|$MAIL_DB_PASSWORD|g" "$SOURCE_DIR/deploy/dovecot/dovecot-sql.conf.ext" > /etc/dovecot/dovecot-sql.conf.ext
chmod 0640 /etc/dovecot/dovecot-sql.conf.ext
chown root:dovecot /etc/dovecot/dovecot-sql.conf.ext || true

log "configuring Postfix"
install -m 0644 /etc/postfix/main.cf "/etc/postfix/main.cf.pre-a8m-$(date +%Y%m%d%H%M%S)"
sed -e "s|__MAIL_HOSTNAME__|$MAIL_HOSTNAME|g" -e "s|__DOMAIN__|$DOMAIN|g" "$SOURCE_DIR/deploy/postfix/main.cf" > /etc/postfix/main.cf
for map in pgsql-virtual-domains.cf pgsql-virtual-mailbox.cf pgsql-virtual-alias.cf; do
  sed "s|__DB_PASSWORD__|$MAIL_DB_PASSWORD|g" "$SOURCE_DIR/deploy/postfix/$map" > "/etc/postfix/$map"
  chown root:postfix "/etc/postfix/$map"
  chmod 0640 "/etc/postfix/$map"
done
touch /etc/postfix/suppressions /etc/postfix/suppressions_in
chown root:postfix /etc/postfix/suppressions /etc/postfix/suppressions_in
chmod 0640 /etc/postfix/suppressions /etc/postfix/suppressions_in
postmap /etc/postfix/suppressions
postmap /etc/postfix/suppressions_in
newaliases
postconf -M submission/inet="submission inet n - n - - smtpd"
postconf -P submission/inet/syslog_name=postfix/submission
postconf -P submission/inet/smtpd_tls_security_level=encrypt
postconf -P submission/inet/smtpd_sasl_auth_enable=yes
postconf -e smtpd_tls_security_level=may

log "configuring Rspamd and ClamAV"
install -d -m 0755 /etc/rspamd/local.d /var/lib/rspamd/dkim
install -m 0644 "$SOURCE_DIR/deploy/rspamd/antivirus.conf" /etc/rspamd/local.d/antivirus.conf
install -m 0644 "$SOURCE_DIR/deploy/rspamd/dkim_signing.conf" /etc/rspamd/local.d/dkim_signing.conf
sed "s|__REDIS_PASSWORD__|$REDIS_PASSWORD|g" "$SOURCE_DIR/deploy/rspamd/redis.conf" > /etc/rspamd/local.d/redis.conf
chown root:_rspamd /etc/rspamd/local.d/redis.conf 2>/dev/null || true
chmod 0640 /etc/rspamd/local.d/redis.conf
chmod 0644 /etc/rspamd/local.d/antivirus.conf /etc/rspamd/local.d/dkim_signing.conf
touch /var/lib/rspamd/dkim/selectors.map
chown -R _rspamd:_rspamd /var/lib/rspamd/dkim 2>/dev/null || true

# ---------------------------------------------------------------------------
# Release and application configuration
# ---------------------------------------------------------------------------
if [[ ! -x "$SOURCE_DIR/bin/a8m-server" ]]; then
  log "building the release from source"
  BUILD_ROOT=/tmp/a8m-build
  rm -rf "$BUILD_ROOT"
  "$SOURCE_DIR/scripts/build-release.sh" "$VERSION" "$BUILD_ROOT" >/dev/null
  SOURCE_DIR="$BUILD_ROOT/alt8mail-$VERSION"
  [[ -x "$SOURCE_DIR/bin/a8m-server" ]] || die "release build did not produce binaries"
fi

RELEASE_DIR="$RELEASES_DIR/$VERSION"
if [[ ! -d "$RELEASE_DIR" ]]; then
  log "installing release $VERSION"
  install -d -m 0755 "$RELEASE_DIR"
  cp -a "$SOURCE_DIR/." "$RELEASE_DIR/"
  chmod 0755 "$RELEASE_DIR/bin/"* 2>/dev/null || true
fi
ln -sfn "$RELEASE_DIR" "$INSTALL_ROOT/current"
ln -sfn "$INSTALL_ROOT/current/bin" "$INSTALL_ROOT/bin"
ln -sfn "$INSTALL_ROOT/current/admin" "$INSTALL_ROOT/admin"
rm -rf "$WEB_ROOT"
ln -s "$INSTALL_ROOT/current/public" "$WEB_ROOT"
ln -sfn "$INSTALL_ROOT/current" "$INSTALL_ROOT/app"
install -d -m 0755 "$CONTROL_ROOT/bin"
ln -sfn "$INSTALL_ROOT/current/bin/alt8mail-control" "$CONTROL_ROOT/bin/alt8mail-control"
install -m 0755 "$SOURCE_DIR/scripts/alt8mail-update.sh" /usr/local/bin/alt8mail-update

UPDATE_KEY=/etc/a8m/github_readonly
if [[ ! -s "$UPDATE_KEY" ]]; then
  ssh-keygen -q -t ed25519 -N '' -C "a8m-readonly-$(hostname)" -f "$UPDATE_KEY"
  chmod 0600 "$UPDATE_KEY"
fi
cat > "$CONFIG_ROOT/update.conf" <<EOF
A8M_REPO=git@github.com:CryptRoute/Alt8Mail.git
A8M_GIT_KEY=$UPDATE_KEY
A8M_CHANNEL=stable
A8M_SERVICES="a8m-server a8m-admin alt8mail-control"
EOF
chmod 0640 "$CONFIG_ROOT/update.conf"
chown root:$SERVICE_USER "$CONFIG_ROOT/update.conf"

cat > "$CONFIG_ROOT/app.env" <<EOF
ALT8MAIL_ENV=production
ALT8MAIL_LISTEN=127.0.0.1:8080
ALT8MAIL_PUBLIC_URL=https://$MAIL_HOSTNAME
ALT8MAIL_MAIL_DOMAIN=$DOMAIN
ALT8MAIL_MAIL_HOSTNAME=$MAIL_HOSTNAME
ALT8MAIL_MAILDIR_BASE=/var/vmail
ALT8MAIL_DB_HOST=127.0.0.1
ALT8MAIL_DB_PORT=5432
ALT8MAIL_DB_NAME=a8m
ALT8MAIL_DB_USER=a8m_app
ALT8MAIL_DB_PASSWORD=$APP_DB_PASSWORD
ALT8MAIL_DB_SSLMODE=disable
ALT8MAIL_REDIS_ADDR=127.0.0.1:6379
ALT8MAIL_REDIS_PASSWORD=$REDIS_PASSWORD
ALT8MAIL_SESSION_SECRET=$SESSION_SECRET
ALT8MAIL_CSRF_SECRET=$CSRF_SECRET
ALT8MAIL_COOKIE_SECURE=true
ALT8MAIL_IMAP_ADDR=127.0.0.1:993
ALT8MAIL_IMAP_MASTER_USER=a8m-worker
ALT8MAIL_IMAP_MASTER_PASSWORD_FILE=$SECRETS_DIR/imap_master_password
ALT8MAIL_IMAP_TLS_NAME=$MAIL_HOSTNAME
ALT8MAIL_SMTP_ADDR=127.0.0.1:587
ALT8MAIL_SMTP_TLS_NAME=$MAIL_HOSTNAME
ALT8MAIL_MAIL_CA_FILE=/etc/ssl/a8m/mail.crt
ALT8MAIL_MANAGESIEVE_ADDR=127.0.0.1:4190
ALT8MAIL_MANAGESIEVE_TLS_NAME=$MAIL_HOSTNAME
ALT8MAIL_WEBAUTHN_RPID=$MAIL_HOSTNAME
ALT8MAIL_WEBAUTHN_ORIGINS=https://$MAIL_HOSTNAME,https://$DOMAIN
ALT8MAIL_ADMIN_LISTEN=127.0.0.1:8081
ALT8MAIL_ADMIN_DIR=$INSTALL_ROOT/current/admin
ALT8MAIL_ADMIN_URL=https://$MAIL_HOSTNAME/admin/
ALT8MAIL_ADMIN_SESSION_HOURS=8
ALT8MAIL_DKIM_DIR=/var/lib/rspamd/dkim
ALT8MAIL_DKIM_SELECTOR_MAP=/var/lib/rspamd/dkim/selectors.map
ALT8MAIL_CLAMAV_ADDR=/var/run/clamav/clamd.ctl
ALT8MAIL_UPLOAD_DIR=$INSTALL_ROOT/uploads
ALT8MAIL_MAIL_WORKER_CONCURRENCY=8
ALT8MAIL_IMAP_SYNC_SECONDS=300
ALT8MAIL_IMAP_POLL_SECONDS=15
ALT8MAIL_IMAP_IDLE=true
ALT8MAIL_DEMO_MODE=false
EOF
chown root:$SERVICE_USER "$CONFIG_ROOT/app.env"
chmod 0640 "$CONFIG_ROOT/app.env"

cat > "$CONFIG_ROOT/control.env" <<EOF
CONTROL_ENV=production
CONTROL_LISTEN=0.0.0.0:8808
CONTROL_DATABASE_URL=postgres://alt8mail_control:$CONTROL_DB_PASSWORD@127.0.0.1:5432/a8m?sslmode=disable
CONTROL_SESSION_SECRET=$CONTROL_SESSION_SECRET
CONTROL_CSRF_SECRET=$CONTROL_CSRF_SECRET
CONTROL_SECRET_KEY=$CONTROL_SECRET_KEY
CONTROL_PUBLIC_URL=https://$MAIL_HOSTNAME:8808
CONTROL_WEBMAIL_URL=https://$MAIL_HOSTNAME
CONTROL_MAIL_HOSTNAME=$MAIL_HOSTNAME
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
CONTROL_REDIS_PASSWORD_FILE=$SECRETS_DIR/redis_password
CONTROL_RETENTION_DAYS=365
CONTROL_TRUSTED_PROXY=false
EOF
chown root:root "$CONFIG_ROOT/control.env"
chmod 0600 "$CONFIG_ROOT/control.env"

# ---------------------------------------------------------------------------
# TLS
# ---------------------------------------------------------------------------
tls_ready=0
if [[ "$TLS_MODE" == "letsencrypt" && "$SKIP_CERTBOT" -eq 0 ]]; then
  dns_mail="$(dig +short A "$MAIL_HOSTNAME" | awk 'NF && $1 ~ /^[0-9.]+$/ {print $1; exit}')"
  dns_domain="$(dig +short A "$DOMAIN" | awk 'NF && $1 ~ /^[0-9.]+$/ {print $1; exit}')"
  if [[ "$dns_mail" != "$PUBLIC_IP" || "$dns_domain" != "$PUBLIC_IP" ]]; then
    warn "DNS for $MAIL_HOSTNAME/$DOMAIN does not both point to $PUBLIC_IP; using a self-signed certificate"
    TLS_MODE=selfsigned
  fi
fi

write_http_nginx() {
  cat > /etc/nginx/sites-available/a8m <<EOF
server {
    listen 80;
    listen [::]:80;
    server_name $MAIL_HOSTNAME $DOMAIN $PUBLIC_IP localhost;
    location /.well-known/acme-challenge/ { root /var/www/certbot; }
    location / { return 301 https://\$host\$request_uri; }
}
EOF
  ln -sfn /etc/nginx/sites-available/a8m /etc/nginx/sites-enabled/a8m
  rm -f /etc/nginx/sites-enabled/default
  if ! grep -qE 'application/javascript[[:space:]]+.*mjs' /etc/nginx/mime.types; then
    sed -i '0,/application\/javascript[[:space:]]\+js;/s//application\/javascript js mjs;/' /etc/nginx/mime.types
  fi
  nginx -t
}
write_http_nginx
systemctl enable --now nginx >/dev/null
systemctl reload nginx

if [[ "$TLS_MODE" == "letsencrypt" ]]; then
  log "requesting a Let's Encrypt certificate for $MAIL_HOSTNAME and $DOMAIN"
  if certbot certonly --webroot -w /var/www/certbot \
      -d "$MAIL_HOSTNAME" -d "$DOMAIN" --cert-name "$MAIL_HOSTNAME" \
      --email "$TLS_EMAIL" --agree-tos --no-eff-email --non-interactive; then
    LE_LIVE="/etc/letsencrypt/live/$MAIL_HOSTNAME"
    install -d -m 0755 /etc/ssl/a8m /etc/ssl/alt8mail
    install -m 0644 "$LE_LIVE/fullchain.pem" /etc/ssl/a8m/mail.crt
    install -m 0640 "$LE_LIVE/privkey.pem" /etc/ssl/a8m/mail.key
    chown root:postfix /etc/ssl/a8m/mail.key
    tls_ready=1
  else
    warn "Let's Encrypt failed; using a self-signed certificate"
    TLS_MODE=selfsigned
  fi
fi
if [[ "$tls_ready" -eq 0 ]]; then
  log "generating a self-signed certificate"
  install -d -m 0755 /etc/ssl/a8m /etc/ssl/alt8mail
  openssl req -x509 -nodes -newkey rsa:3072 -sha256 -days 825 \
    -subj "/CN=$MAIL_HOSTNAME/O=Alt8Mail" \
    -addext "subjectAltName=DNS:$MAIL_HOSTNAME,DNS:$DOMAIN,DNS:localhost,IP:$PUBLIC_IP,IP:127.0.0.1" \
    -keyout /etc/ssl/a8m/mail.key -out /etc/ssl/a8m/mail.crt >/dev/null 2>&1
  chmod 0644 /etc/ssl/a8m/mail.crt
  chmod 0640 /etc/ssl/a8m/mail.key
  chown root:postfix /etc/ssl/a8m/mail.key
fi

install -m 0644 /etc/ssl/a8m/mail.crt /etc/ssl/alt8mail/control.crt
install -m 0600 /etc/ssl/a8m/mail.key /etc/ssl/alt8mail/control.key
install -d -m 0755 /etc/dovecot/private
install -m 0640 -o root -g dovecot /etc/ssl/a8m/mail.crt /etc/dovecot/private/a8m-mail.crt
install -m 0640 -o root -g dovecot /etc/ssl/a8m/mail.key /etc/dovecot/private/a8m-mail.key

sed -e "s|@MAIL_HOSTNAME@|$MAIL_HOSTNAME|g" \
    -e "s|@DOMAIN@|$DOMAIN|g" \
    -e "s|@SERVER_IP@|$PUBLIC_IP|g" \
    -e "s|@WEB_ROOT@|$WEB_ROOT|g" \
    -e "s|@TLS_CERT@|/etc/ssl/a8m/mail.crt|g" \
    -e "s|@TLS_KEY@|/etc/ssl/a8m/mail.key|g" \
    "$SOURCE_DIR/deploy/nginx/a8m-site.conf" > /etc/nginx/sites-available/a8m
nginx -t
systemctl reload nginx

if [[ "$TLS_MODE" == "letsencrypt" ]]; then
  install -d -m 0755 /etc/letsencrypt/renewal-hooks/deploy
  cat > /etc/letsencrypt/renewal-hooks/deploy/a8m-reload.sh <<'HOOK'
#!/bin/sh
set -eu
install -m 0644 /etc/letsencrypt/live/__MAIL_HOSTNAME__/fullchain.pem /etc/ssl/a8m/mail.crt
install -m 0640 /etc/letsencrypt/live/__MAIL_HOSTNAME__/privkey.pem /etc/ssl/a8m/mail.key
chown root:postfix /etc/ssl/a8m/mail.key
install -m 0644 /etc/ssl/a8m/mail.crt /etc/dovecot/private/a8m-mail.crt
install -m 0640 /etc/ssl/a8m/mail.key /etc/dovecot/private/a8m-mail.key
chown root:dovecot /etc/dovecot/private/a8m-mail.crt /etc/dovecot/private/a8m-mail.key
install -m 0644 /etc/ssl/a8m/mail.crt /etc/ssl/alt8mail/control.crt
install -m 0600 /etc/ssl/a8m/mail.key /etc/ssl/alt8mail/control.key
systemctl reload nginx >/dev/null 2>&1 || true
systemctl reload postfix >/dev/null 2>&1 || true
systemctl reload dovecot >/dev/null 2>&1 || true
systemctl restart alt8mail-control >/dev/null 2>&1 || true
HOOK
  sed -i "s|__MAIL_HOSTNAME__|$MAIL_HOSTNAME|g" /etc/letsencrypt/renewal-hooks/deploy/a8m-reload.sh
  chmod 0755 /etc/letsencrypt/renewal-hooks/deploy/a8m-reload.sh
  systemctl enable --now certbot.timer >/dev/null 2>&1 || true
fi

# ---------------------------------------------------------------------------
# Services and first accounts
# ---------------------------------------------------------------------------
log "installing systemd units"
for unit in "$SOURCE_DIR"/deploy/systemd/*.service "$SOURCE_DIR"/deploy/systemd/*.timer; do
  install -m 0644 "$unit" "/etc/systemd/system/$(basename "$unit")"
done
systemctl daemon-reload

systemctl enable --now postgresql redis-server dovecot rspamd clamav-daemon >/dev/null
systemctl restart dovecot rspamd clamav-daemon
systemctl enable --now postfix >/dev/null
postfix check

# Wait briefly for ClamAV/Rspamd before mail delivery.
for svc in dovecot postfix rspamd clamav-daemon; do
  if ! systemctl is-active --quiet "$svc"; then warn "$svc is not active after configuration"; fi
done

log "creating the first tenant, domain and owner"
runuser -u postgres -- psql -d a8m -v ON_ERROR_STOP=1 -q \
  -v tenant_name="$TENANT_NAME" -v tenant_slug="$TENANT_SLUG" \
  -v domain="$DOMAIN" -v email="$ADMIN_EMAIL" -v name="$ADMIN_NAME" <<'SQL'
INSERT INTO tenants (slug, name, plan, region, billing_email)
VALUES (:'tenant_slug', :'tenant_name', 'business', 'us-east-1', :'email')
ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name, updated_at = now()
RETURNING id AS tenant_id \gset
INSERT INTO mail_domains (tenant_id, domain, is_primary, dkim_selector, status, verification_token, verified_at, mx_verified, spf_verified, dmarc_verified)
VALUES (:'tenant_id', :'domain', true, 'a8m', 'manual', encode(gen_random_bytes(18), 'hex'), now(), true, true, true)
ON CONFLICT (domain) DO UPDATE SET
  tenant_id = EXCLUDED.tenant_id, is_primary = true, dkim_selector = 'a8m', status = 'manual',
  verified_at = COALESCE(mail_domains.verified_at, now()), mx_verified = true, spf_verified = true
RETURNING id AS domain_id \gset
SELECT EXISTS (SELECT 1 FROM users WHERE email = :'email') AS owner_exists \gset
INSERT INTO users (tenant_id, email, name, initials, color, role, status, password_hash, mail_password_hash,
                   quota_bytes, is_system_admin)
VALUES (:'tenant_id', :'email', :'name', 'OW', '#0f6cbd', 'owner', 'active', '', '', 17179869184, true)
ON CONFLICT (email) DO UPDATE SET
  tenant_id = EXCLUDED.tenant_id, name = EXCLUDED.name, role = 'owner',
  is_system_admin = true, status = 'active', deleted_at = NULL, updated_at = now()
RETURNING id AS owner_id \gset
INSERT INTO user_aliases (tenant_id, user_id, email, label, kind, can_send, active)
VALUES (:'tenant_id', :'owner_id', :'email', 'Primary', 'primary', true, true)
ON CONFLICT (email) DO UPDATE SET user_id = EXCLUDED.user_id, active = true;
INSERT INTO mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path, active)
VALUES (:'tenant_id', :'owner_id', :'email', :'domain', split_part(:'email', '@', 1),
        '/var/vmail/' || :'domain' || '/' || split_part(:'email', '@', 1) || '/Maildir', true)
ON CONFLICT (email) DO UPDATE SET user_id = EXCLUDED.user_id, active = true;
SQL

set -a
# shellcheck disable=SC1090
. "$CONFIG_ROOT/app.env"
set +a

if [[ "$(runuser -u postgres -- psql -d a8m -Atqc "SELECT COALESCE(password_hash,'') = '' FROM users WHERE email='$ADMIN_EMAIL'")" == "t" ]]; then
  printf '%s
' "$OWNER_WEB_PASSWORD" | "$INSTALL_ROOT/current/bin/a8m-admin" -email "$ADMIN_EMAIL" >/dev/null
fi
if [[ "$(runuser -u postgres -- psql -d a8m -Atqc "SELECT COALESCE(mail_password_hash,'') = '' FROM users WHERE email='$ADMIN_EMAIL'")" == "t" ]]; then
  OWNER_MAIL_HASH="$(printf '%s\n%s\n' "$OWNER_MAIL_PASSWORD" "$OWNER_MAIL_PASSWORD" | doveadm pw -s SHA512-CRYPT | tr -d '\r' | awk '/^\{SHA512-CRYPT\}/ {print; exit}')"
  [[ -n "$OWNER_MAIL_HASH" ]] || die "could not create the owner mailbox password hash"
  runuser -u postgres -- psql -d a8m -v ON_ERROR_STOP=1 -q -v hash="$OWNER_MAIL_HASH" -v email="$ADMIN_EMAIL" <<'SQL'
UPDATE users SET mail_password_hash = :'hash' WHERE email = :'email';
SQL
fi
if [[ "$(runuser -u postgres -- psql -d a8m -Atqc "SELECT COUNT(*) FROM app_states s JOIN users u ON u.id=s.user_id WHERE u.email='$ADMIN_EMAIL'")" == "0" ]]; then
  "$INSTALL_ROOT/current/bin/a8m-inituser" -email "$ADMIN_EMAIL" >/dev/null
fi
"$INSTALL_ROOT/current/bin/a8m-mailprovision" -all >/dev/null

log "starting Alt8Mail application services"
systemctl enable --now a8m-server a8m-admin >/dev/null
systemctl enable --now a8m-mailprovision.timer a8m-dkimprovision.timer >/dev/null
systemctl restart a8m-server a8m-admin

# Control plane: migrations, first superadmin, then service.
set -a
# shellcheck disable=SC1090
. "$CONFIG_ROOT/control.env"
set +a
if [[ "$(runuser -u postgres -- psql -d a8m -Atqc "SELECT COUNT(*) FROM control.admin_users WHERE email='$CONTROL_EMAIL'")" == "0" ]]; then
  printf '%s\n' "$CONTROL_ADMIN_PASSWORD" | "$CONTROL_ROOT/bin/alt8mail-control" admin \
    --email "$CONTROL_EMAIL" --name 'System Administrator' --role superadmin --password-stdin >/dev/null
fi
systemctl enable --now alt8mail-control >/dev/null
systemctl restart alt8mail-control

# DKIM keys and DNS record material.
"$INSTALL_ROOT/current/bin/a8m-dkimprovision" >/dev/null 2>&1 || warn "DKIM provisioning will retry from its timer"
systemctl try-restart rspamd >/dev/null 2>&1 || true

# ---------------------------------------------------------------------------
# Firewall and credentials
# ---------------------------------------------------------------------------
SSH_PORT="$(sshd -T 2>/dev/null | awk '/^port / {print $2; exit}')"
if [[ -n "${SSH_CONNECTION:-}" ]]; then
  CONNECTED_PORT="$(awk '{print $4}' <<<"$SSH_CONNECTION")"
  [[ "$CONNECTED_PORT" =~ ^[0-9]+$ ]] && SSH_PORT="$CONNECTED_PORT"
fi
SSH_PORT="${SSH_PORT:-22}"
log "configuring UFW (SSH port $SSH_PORT)"
ufw --force reset >/dev/null 2>&1 || true
ufw default deny incoming >/dev/null
ufw default allow outgoing >/dev/null
ufw allow "$SSH_PORT/tcp" >/dev/null
for port in 80 443 25 587 993 995 4190 8808; do
  ufw allow "$port/tcp" >/dev/null
done
ufw --force enable >/dev/null

install -m 0644 "$SOURCE_DIR/deploy/fail2ban/a8m.local" /etc/fail2ban/jail.d/a8m.local
sed -i "s/^port = ssh$/port = $SSH_PORT/" /etc/fail2ban/jail.d/a8m.local
systemctl restart fail2ban >/dev/null 2>&1 || true
install -m 0644 "$SOURCE_DIR/deploy/sysctl/99-alt8mail-mail.conf" /etc/sysctl.d/99-alt8mail-mail.conf
sysctl --system >/dev/null 2>&1 || true

DKIM_RECORD="$(runuser -u postgres -- psql -d a8m -Atqc "SELECT dkim_record FROM mail_domains WHERE domain='$DOMAIN'")"
umask 077
if [[ ! -s /root/alt8mail-initial-credentials.txt ]]; then
  cat > /root/alt8mail-initial-credentials.txt <<EOF
Alt8Mail staging credentials
Generated: $(date -u +%Y-%m-%dT%H:%M:%SZ)
Webmail: https://$MAIL_HOSTNAME/
Customer admin: https://$MAIL_HOSTNAME/admin/
Owner email: $ADMIN_EMAIL
Owner web password: $OWNER_WEB_PASSWORD
Owner mailbox password: $OWNER_MAIL_PASSWORD
System Control: https://$MAIL_HOSTNAME:8808/
Control email: $CONTROL_EMAIL
Control password: $CONTROL_ADMIN_PASSWORD
Secrets directory: $SECRETS_DIR
Database: a8m (app role a8m_app, mail role a8m_mail)
EOF
  chmod 0600 /root/alt8mail-initial-credentials.txt
fi
cat > /root/alt8mail-dns-records.txt <<EOF
MX: $DOMAIN. 10 $MAIL_HOSTNAME.
SPF: v=spf1 mx -all
DMARC: v=DMARC1; p=none; rua=mailto:dmarc@$DOMAIN
DKIM selector a8m: a8m._domainkey.$DOMAIN. TXT "$DKIM_RECORD"
EOF
chmod 0600 /root/alt8mail-dns-records.txt

# ---------------------------------------------------------------------------
# Verification
# ---------------------------------------------------------------------------
log "running health checks"
failed=0
for svc in postgresql redis-server dovecot postfix rspamd clamav-daemon nginx a8m-server a8m-admin alt8mail-control; do
  if systemctl is-active --quiet "$svc"; then
    log "service $svc: active"
  else
    warn "service $svc: not active"
    failed=1
  fi
done
curl -fsS --max-time 8 http://127.0.0.1:8080/api/health >/dev/null || { warn "webmail API health failed"; failed=1; }
curl -fsS --max-time 8 http://127.0.0.1:8081/api/health >/dev/null || { warn "admin API health failed"; failed=1; }
curl -kfsS --max-time 8 https://127.0.0.1:8808/healthz >/dev/null || { warn "control health failed"; failed=1; }
nginx -t >/dev/null 2>&1 || { warn "nginx configuration check failed"; failed=1; }
postfix check >/dev/null 2>&1 || { warn "postfix check failed"; failed=1; }

if [[ "$failed" -ne 0 ]]; then
  warn "some checks failed; review $LOG_FILE and journalctl -u a8m-server -u a8m-admin -u alt8mail-control"
  exit 1
fi

log "installation complete"
log "credentials: /root/alt8mail-initial-credentials.txt"
log "DNS records: /root/alt8mail-dns-records.txt"
log "webmail: https://$MAIL_HOSTNAME/"
log "control: https://$MAIL_HOSTNAME:8808/"
