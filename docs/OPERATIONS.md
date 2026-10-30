# Staging Operations Runbook

This runbook records the reproducible build of the first live Alt8Mail staging
slice on the throw-away KVM host. It intentionally excludes secret values; all
secrets live on the server under `/opt/a8m/secrets` with owner-only
permissions. Never commit them or paste them into tickets.

## 1. Host baseline

| Item | Value |
| --- | --- |
| Public IP | `130.94.46.22` |
| OS | Debian GNU/Linux 12 (bookworm) |
| Kernel | `6.1.0-54-amd64` (patched; reboot completed) |
| CPU / RAM / disk | 2 vCPU / 3.8 GiB RAM / 50 GiB root volume |
| Swap | 4 GiB `/swapfile` |
| Time zone | UTC |
| Hostname | `mail.a8m.test` |
| Staging mail domain | `a8m.test` (reserved test domain; no public DNS) |
| Staging mail users | Every active mailbox is synced by its own Go worker (Avery, Jordan and the directory users) |
| Second real auth user | `jordan.lee@a8m.test` (Go login + Maildir + per-user worker) |
| Staging UI/API user | `avery.chen@a8m.test` (Argon2id web password) |
| Staging demo alias | `avery.chen@a8m.example` / `a8m-demo` → canonical account, demo mode only |
| Web URL | `https://130-94-46-22.sslip.io` (Let's Encrypt, valid) |
| IP fallback | `https://130.94.46.22` (self-signed certificate) |
| Root UI + feature API | nginx `/api` → Go system backend on `127.0.0.1:8080` |
| Go system API namespace | nginx `/system/api` → the same Go service |
| Admin console | `https://130-94-46-22.sslip.io/admin/` → Go admin backend `127.0.0.1:8081` |
| Admin backend | `a8m-admin-server` (separate cookies, sessions and audit) |
| System Control | `https://130.94.46.22:8808` → `alt8mail-control` (separate schema, role, sessions and audit) |
| Control bootstrap | `sysadmin@alt8mail.local`; password in `/root/alt8mail-control-initial-password` (root-only) |
| Reference preview | `a8m-preview` Node service (stopped and disabled; not used) |
| SSH | key `~/.ssh/a8m_deploy_ed25519` on the operator workstation |

> **Architecture note:** the root Aurora UI and `/api` are served by the Go
> system backend. The mailbox is real Dovecot data: IMAP sync/actions, draft
> persistence, SMTP submission and deferred expunge are live. A second Go
> process, `a8m-admin-server`, serves the customer/system administration
> console with its own sessions and audit. A third, independent service,
> `alt8mail-control`, serves the root-level System Control panel on TLS port
> 8808 with its own `control` schema and `alt8mail_control` role. The reference
> Node service is stopped, is not proxied, and its `seed/state.json` is not read
> by the Go frontend. The staging-only login alias is accepted only because
> `ALT8MAIL_DEMO_MODE=true`; config loading refuses demo mode when
> `ALT8MAIL_ENV=production`. See [ADMIN.md](ADMIN.md) and
> [SYSTEM_CONTROL.md](SYSTEM_CONTROL.md).

SSH was hardened on 2026-10-29: external root login is key-only
(`PermitRootLogin prohibit-password`), password authentication is disabled for
non-loopback peers, and fail2ban remains active. A loopback-only `sshd` Match
block permits password authentication for the System Control terminal; it does
not change the externally advertised methods. Keep the operator key as the
break-glass credential and back it up outside the host.

### Deploy key

The operator key was installed into `/root/.ssh/authorized_keys`. Verify:

```bash
ssh -i ~/.ssh/a8m_deploy_ed25519 \
  -o BatchMode=yes root@130.94.46.22 'hostname; id'
```

Expected: `mail.a8m.test`, `uid=0(root)`.

## 2. Live topology

```text
Internet / operator VPN
  │ 80/443 (nginx redirect + TLS)      │ 25/587/993/995/4190
  │ 8808 (System Control TLS, restrict by source IP)
  ▼                                     ▼
nginx                                  Postfix / Dovecot / ManageSieve
  ├── /api/* ── Go system backend 127.0.0.1:8080
  │              ├── PostgreSQL 15 (identity, sessions, per-user feature state)
  │              ├── Redis 7 (rate-limit/session foundation)
  │              ├── IMAP sync ── Dovecot 993 (verified TLS)
  │              └── SMTP submission ── Postfix 587 (STARTTLS + SASL)
  ├── /system/api/* ── the same Go system backend
  ├── /admin/* ── Go admin backend 127.0.0.1:8081
  │              ├── admin sessions, RBAC, tenant policy and audit
  │              └── /opt/a8m/admin (admin UI)
  ├── 8808 alt8mail-control ── control schema
  │              ├── Postfix policy daemon 127.0.0.1:10040
  │              ├── Postfix/Dovecot/certbot/systemd/UFW management
  │              └── logs, imports, backups, cron and API v1
  ├── reference preview ── Node 127.0.0.1:8001 (stopped/disabled)
  └── /var/www/a8m (Aurora frontend)
                         │
                         ├── Dovecot (IMAP, LMTP, Sieve, quota)
                         ├── Postfix (SMTP ingress + submission)
                         ├── Rspamd (spam, DKIM, milter) ── ClamAV
                         └── /var/vmail (Maildir)
```

PostgreSQL and Redis listen only on loopback. Dovecot, Postfix, nginx and
ManageSieve and POP3S are exposed through UFW. UFW allows only `22, 80, 443,
25, 587, 993, 995, 4190` plus `8808` for System Control. Port 8808 is
root-equivalent and must be restricted to an operator VPN or source-IP
allowlist in production. Port `88008` is invalid (the TCP range is 0–65535), so
the panel URL is `https://130.94.46.22:8808`.

## 3. Installed components

| Component | Version | Notes |
| --- | --- | --- |
| PostgreSQL | 15.19 | Database `a8m`; app role `a8m_app`; mail role `a8m_mail` |
| Redis | 7.0.15 | Loopback, password, AOF, 256 MB no-eviction limit |
| Dovecot | 2.3.19.1 | IMAP + POP3S + LMTP + Sieve; PostgreSQL virtual users; Maildir |
| Pigeonhole / ManageSieve | 0.5.19 | Port 4190, STARTTLS, auth tested |
| Postfix | 3.7.11 | Virtual domains, Dovecot SASL + LMTP, submission 587 |
| Rspamd | 4.2.3 | Official apt repo; DKIM signing; ClamAV integration |
| ClamAV | 1.4.6 | `clamav-daemon`, freshclam active |
| nginx | 1.22.1 | TLS, static frontend, API reverse proxy, CSP |
| Go | 1.27.3 | Official tarball, `/usr/local/go` |
| Node.js | 24.22.1 | Reference feature-preview service only |
| fail2ban | 1.0.2 | `sshd` jail using systemd backend + UFW action |
| System Control | 0.10.0-control | Separate root Go service on TLS 8808; `control` schema and `alt8mail_control` role |

The public staging hostname is `130-94-46-22.sslip.io`, which resolves to the
server IP through sslip.io. nginx uses a Let's Encrypt certificate for that
hostname (auto-renewal through `certbot.timer`, deploy hook reloads nginx). The
IP fallback serves the earlier self-signed certificate at
`/etc/ssl/a8m/mail.crt`. Production still needs a customer-owned domain,
CA validation for the mail hostname and proper DNS ownership; sslip.io is only a
staging convenience.

## 4. Server file layout

```text
/opt/alt8mail/
  bin/alt8mail-control     System Control binary
  control.env              control-plane environment, 0600 root-only
/etc/ssl/alt8mail/         control.crt/key and panel-generated self-signed certs
/etc/cron.d/alt8mail      panel-managed scheduled jobs
/etc/cron.d/alt8mail-ssl  twice-daily certbot renewal wrapper
/usr/local/bin/ssl_renewal.sh
/var/backups/alt8mail/    control backups and import archives (0700)
/var/vmail/.control-imports/  staged imports (vmail-owned, cleaned per job)
/etc/systemd/system/alt8mail-control.service

/opt/a8m/
  app/backend/            Go source (module + migrations)
  app/public/             source frontend copy (deployment reference)
  preview/                reference Node service source (stopped; not used)
  data/preview/db.json    reference preview data archive (not used)
  bin/a8m-server     Go API + per-user IMAP/retention/expunge workers
  bin/a8m-mailprovision  root Maildir provisioner (systemd oneshot/timer)
  bin/a8m-dkimprovision  root DKIM key/selector-map provisioner
  bin/a8m-admin-server  customer/system administration backend
  bin/a8m-admin      password/support CLI (includes system admin grant)
  app/deploy/systemd/     mail/DKIM provisioning units (source copy)
  admin/                  admin UI served by a8m-admin-server
  bin/a8m-inituser   real per-user state initializer (non-demo)
  secrets/                secrets, 0600, owner a8m
    app.env               service environment
    postgres_app_password
    postgres_mail_password
    redis_password
    session_secret
    csrf_secret
    staging_avery_password
    avery_mail_password   Dovecot IMAP/submission credential
  uploads/                uploaded and IMAP-ingested attachment binaries
  app/backend/seed/demo_mailboxes.sql  staging directory recipient rows only
  app/backend/migrations/ PostgreSQL migrations
/etc/dovecot/conf.d/99-a8m.conf
/etc/dovecot/dovecot-sql.conf.ext
/etc/postfix/pgsql-virtual-*.cf
/etc/rspamd/local.d/antivirus.conf
/etc/rspamd/local.d/dkim_signing.conf
/etc/rspamd/local.d/redis.conf
/etc/nginx/sites-available/a8m
/etc/systemd/system/a8m-server.service
/etc/systemd/system/a8m-admin.service
/etc/systemd/system/a8m-mailprovision.service
/etc/systemd/system/a8m-mailprovision.timer
/etc/systemd/system/a8m-dkimprovision.service
/etc/systemd/system/a8m-dkimprovision.timer
/var/lib/rspamd/dkim/    per-domain signing keys + selectors.map
/var/www/a8m/        deployed static frontend
/var/vmail/a8m.test/avery.chen/Maildir/
```

### Application credentials

The staging web password is intentionally not written in this document. Read it
on the server as root:

```bash
ssh -i ~/.ssh/a8m_deploy_ed25519 root@130.94.46.22 \
  'cat /opt/a8m/secrets/staging_avery_password'
```

This random password is for the **Go system backend** web identity. The
Dovecot/Postfix mailbox has a separate random password stored at
`/opt/a8m/secrets/avery_mail_password` and is used by the Go IMAP/SMTP
workers. It is never exposed to the browser.

Jordan Lee is a second real Go authentication user. Its web password is
`/opt/a8m/secrets/staging_jordan_password`; its mailbox password is
`/opt/a8m/secrets/jordan_mail_password`; its Maildir is
`/var/vmail/a8m.test/jordan.lee/Maildir`. Jordan and every other active
mailbox now has its own per-user IMAP worker. The Go server authenticates to
Dovecot with the master user, while end-user web self-service password changes
rotate both the web password and the Dovecot `mail_password_hash`.

The root UI also accepts the staging demo alias configured in `app.env`:

```text
avery.chen@a8m.example / a8m-demo
```

That alias is accepted only when `ALT8MAIL_DEMO_MODE=true` and
`ALT8MAIL_ENV` is not `production`; it maps to `avery.chen@a8m.test`. It only
affects the login identity and never restores mailbox or workspace JSON data.
While demo mode is enabled, the mapped account also accepts the same demo
password for current-password re-authentication prompts such as app-password
creation, password change and authenticator removal; no other account accepts
it. The Go login is not password-only: it issues a real TOTP/WebAuthn challenge
when the user has enrolled a factor, and a first-time enrollment challenge when
the user's `enrollment_required` policy is true. Remove the demo alias,
prefilled credentials and demo-mode fallback before production.

The Go web password is stored in PostgreSQL as Argon2id; the mail password is
stored as a Dovecot `{SHA512-CRYPT}` hash. Rotate both separately for production.

## 5. Reproducible build steps

### 5.1 Base OS

```bash
apt-get update
apt-get install -y ca-certificates curl gnupg lsb-release apt-transport-https \
  software-properties-common ufw fail2ban unattended-upgrades chrony jq git \
  rsync unzip zip build-essential dnsutils htop ncdu vim-tiny acl openssl
timedatectl set-timezone UTC
hostnamectl set-hostname mail.a8m.test
printf '\n127.0.1.1 mail.a8m.test mail\n' >> /etc/hosts
fallocate -l 4G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile
echo '/swapfile none swap sw 0 0' >> /etc/fstab
```

### 5.2 Firewall and fail2ban

```bash
ufw default deny incoming
ufw default allow outgoing
ufw allow 22/tcp
ufw allow 80/tcp
ufw allow 443/tcp
ufw allow 25/tcp
ufw allow 587/tcp
ufw allow 993/tcp
ufw allow 995/tcp
ufw allow 4190/tcp
ufw --force enable
apt-get install -y python3-systemd rsyslog
```

`/etc/fail2ban/jail.d/a8m.local`:

```ini
[DEFAULT]
backend = systemd
banaction = ufw
# The System Control terminal uses loopback SSH; never ban 127.0.0.1/::1.
ignoreip = 127.0.0.1/8 ::1
bantime = 1h
findtime = 10m
maxretry = 5

[sshd]
enabled = true
port = ssh
```

The repository copy is `deploy/fail2ban/a8m.local`.

### 5.3 Package installation

```bash
echo 'postfix postfix/main_mailer_type select Internet Site' | debconf-set-selections
echo 'postfix postfix/mailname string mail.a8m.test' | debconf-set-selections
apt-get install -y postgresql postgresql-contrib postgresql-client \
  redis-server redis-tools dovecot-core dovecot-imapd dovecot-lmtpd \
  dovecot-managesieved dovecot-sieve dovecot-pgsql postfix postfix-pgsql \
  rspamd clamav clamav-daemon clamav-freshclam nginx swaks netcat-openbsd
```

Rspamd official repository:

```bash
curl -fsSL https://rspamd.com/apt-stable/gpg.key \
  | gpg --dearmor -o /usr/share/keyrings/rspamd.gpg
echo 'deb [signed-by=/usr/share/keyrings/rspamd.gpg] http://rspamd.com/apt-stable/ bookworm main' \
  > /etc/apt/sources.list.d/rspamd.list
apt-get update
apt-get install -y rspamd
```

Go:

```bash
version=$(curl -fsSL 'https://go.dev/VERSION?m=text' | head -1)
curl -fsSLO "https://go.dev/dl/${version}.linux-amd64.tar.gz"
# Verify the SHA-256 from https://go.dev/dl/?mode=json before extracting.
rm -rf /usr/local/go && tar -C /usr/local -xzf "${version}.linux-amd64.tar.gz"
ln -sf /usr/local/go/bin/go /usr/local/bin/go
```

### 5.4 PostgreSQL

```bash
runuser -u postgres -- psql -c "CREATE ROLE a8m_app LOGIN PASSWORD '<random>';"
runuser -u postgres -- psql -c "CREATE ROLE a8m_mail LOGIN PASSWORD '<random>';"
runuser -u postgres -- createdb -O a8m_app a8m
runuser -u postgres -- psql -d a8m -c 'CREATE EXTENSION citext; CREATE EXTENSION pgcrypto;'
```

Then apply the repository migrations:

```bash
cd /opt/a8m/app/backend
PGPASSWORD="$(cat /opt/a8m/secrets/postgres_app_password)" \
  psql -h 127.0.0.1 -U a8m_app -d a8m -v ON_ERROR_STOP=1 \
  -f migrations/0001_core.sql
# postgres cannot traverse /opt/a8m (0750); stage later migrations first.
for n in 0002_app_state 0003_real_auth 0004_customer_admin 0005_mail_provisioning 0006_dkim_keys 0007_workspace 0008_mta_sts 0009_dav 0010_app_passwords 0011_mail_tables 0012_outbox 0013_undo_table 0014_compose_identity_ai; do
  install -m 644 "migrations/${n}.sql" "/tmp/${n}.sql"
  runuser -u postgres -- psql -d a8m -v ON_ERROR_STOP=1 -f "/tmp/${n}.sql"
  rm -f "/tmp/${n}.sql"
done
```

`0002_app_state.sql` creates the per-user `app_states` JSONB document used as
the transitional feature-state layer and grants the `a8m_app` role access.


`conf.d/a8m.conf` sets `listen_addresses = '127.0.0.1, ::1'`,
`shared_buffers = 256MB`, `work_mem = 4MB`, UTC and bounded connections. The
`a8m_mail` role receives `SELECT` only on the mailbox/alias/domain/group
map tables.

### 5.5 Redis

`/etc/redis/a8m.conf`:

```ini
bind 127.0.0.1 -::1
protected-mode yes
port 6379
requirepass <random>
appendonly yes
appendfsync everysec
maxmemory 256mb
maxmemory-policy noeviction
```

Include it from `/etc/redis/redis.conf`, then `systemctl restart redis-server`.
PostgreSQL and Redis must never be exposed in UFW.

### 5.6 Dovecot

Create a `vmail` account (UID/GID 5000) and `a8m.test` mailbox tree under
`/var/vmail`. The SQL auth file:

```ini
driver = pgsql
connect = host=127.0.0.1 dbname=a8m user=a8m_mail password=<random>
default_pass_scheme = SHA512-CRYPT
password_query = SELECT m.email AS user, u.mail_password_hash AS password \
  FROM mailboxes m \
  JOIN users u ON u.id = m.user_id \
  JOIN tenants t ON t.id = u.tenant_id \
  JOIN mail_domains d ON d.tenant_id = m.tenant_id AND d.domain = m.domain \
  WHERE m.email = '%u' AND m.active = true AND u.status = 'active' \
    AND u.deleted_at IS NULL AND t.status = 'active' \
    AND d.status IN ('verified','manual') \
    AND (('%s' <> 'pop3') OR COALESCE((t.settings->'mail'->>'popAccess')::boolean, false)) \
    AND (('%s' <> 'imap') OR COALESCE((t.settings->'mail'->>'imapAccess')::boolean, true)) \
    AND (('%s' <> 'smtp') OR COALESCE((t.settings->'mail'->>'smtpAccess')::boolean, true))
user_query = SELECT '/var/vmail/' || m.domain || '/' || m.local_part AS home, \
  5000 AS uid, 5000 AS gid FROM mailboxes m JOIN users u ON u.id = m.user_id \
  JOIN tenants t ON t.id = u.tenant_id \
  WHERE m.email = '%u' AND m.active = true AND u.status = 'active' \
    AND u.deleted_at IS NULL AND t.status = 'active'
iterate_query = SELECT m.email AS user FROM mailboxes m JOIN users u ON u.id = m.user_id \
  JOIN tenants t ON t.id = u.tenant_id \
  WHERE m.active = true AND u.status = 'active' AND u.deleted_at IS NULL AND t.status = 'active'
```

`99-a8m.conf` sets Maildir, vmail ownership, SQL passdb/userdb, TLS,
Postfix auth/LMTP sockets, Sieve, quota and ManageSieve. `protocols = imap pop3
lmtp sieve` enables POP3S on 995; UFW must allow 995. Install `dovecot-pop3d`
before enabling the protocol or Dovecot fails with `service(pop3): No such file`.
`mail_plugins = quota` must appear before protocol filters.

The SQL passdb evaluates the tenant's `imapAccess`, `popAccess` and `smtpAccess`
switches on every authentication. Postfix submission uses the same Dovecot auth
socket, so the `smtp` row blocks both IMAP/POP clients and authenticated
submission when an administrator turns the switch off.

### Master user for internal workers

The Go server must read every mailbox without storing each user's mailbox
password. Dovecot master auth is configured in
`/etc/dovecot/conf.d/05-a8m-master.conf`:

```ini
auth_master_user_separator = *
passdb {
  driver = passwd-file
  master = yes
  args = /etc/dovecot/master-users
  pass = yes
}
```

`/etc/dovecot/master-users` contains one long random SHA512-CRYPT hash for
`a8m-worker`, mode `0600 root:dovecot`. The plaintext is stored separately
as `/opt/a8m/secrets/imap_master_password` (mode 0600, owner `a8m`)
and referenced from `app.env`:

```text
ALT8MAIL_IMAP_MASTER_USER=a8m-worker
ALT8MAIL_IMAP_MASTER_PASSWORD_FILE=/opt/a8m/secrets/imap_master_password
ALT8MAIL_MAIL_WORKER_CONCURRENCY=8
```

A mailbox login is `<mailbox-email>*a8m-worker`. Master auth is verified for
IMAP, SMTP submission and ManageSieve. Revoke or rotate it by replacing the
`master-users` entry and the secret file, then restarting Dovecot and
`a8m-server`. The IMAP certificate is the same
`mail.a8m.test` certificate Postfix uses. Copy it into the Dovecot private
directory so the master process can read the key:

```bash
install -m 640 -o root -g dovecot \
  /etc/ssl/a8m/mail.crt /etc/dovecot/private/alt8mail.crt
install -m 640 -o root -g dovecot \
  /etc/ssl/a8m/mail.key /etc/dovecot/private/alt8mail.key
# 99-a8m.conf:
ssl_cert = </etc/dovecot/private/alt8mail.crt
ssl_key = </etc/dovecot/private/alt8mail.key
```

Set the mailbox password and verify it before starting the Go workers:

```bash
PW=$(openssl rand -base64 36 | tr -d '\n')
umask 077
printf '%s' "$PW" > /opt/a8m/secrets/avery_mail_password
chown a8m:a8m /opt/a8m/secrets/avery_mail_password
HASH=$(doveadm pw -s SHA512-CRYPT -p "$PW")
printf "UPDATE users SET mail_password_hash = :'hash' WHERE email='avery.chen@a8m.test';\n" \
  | runuser -u postgres -- psql -d a8m -v hash="$HASH" -f -
doveadm auth test avery.chen@a8m.test "$PW"
```

### 5.7 Postfix

Main settings used:

```text
myhostname = mail.a8m.test
mydestination = localhost
myorigin = a8m.test
virtual_mailbox_domains = pgsql:/etc/postfix/pgsql-virtual-domains.cf
virtual_mailbox_maps = pgsql:/etc/postfix/pgsql-virtual-mailbox.cf
virtual_alias_maps = pgsql:/etc/postfix/pgsql-virtual-alias.cf
virtual_alias_domains =
virtual_transport = lmtp:unix:private/dovecot-lmtp
smtpd_sasl_type = dovecot
smtpd_sasl_path = private/auth
smtpd_milters = inet:127.0.0.1:11332
non_smtpd_milters = inet:127.0.0.1:11332
smtpd_tls_cert_file = /etc/ssl/a8m/mail.crt
smtpd_tls_key_file = /etc/ssl/a8m/mail.key
smtpd_tls_auth_only = yes
```

The `submission` service listens on 587 with `smtpd_tls_security_level=encrypt`
and requires SASL auth. SQL map files are root:postfix 0640. The SMTP service's
chroot is disabled so it can read the TLS key and SQL map files.

`virtual_alias_domains` must be empty. Postfix defaults it to
`$virtual_alias_maps`; pointing the domain classifier at a recipient-rewriting
map makes every lookup interpret alias results as domains and causes
`451 Temporary lookup failure`. `virtual_mailbox_domains` already owns domain
classification.

The alias map performs real mailing-list expansion. Group addresses are unioned
with normal user aliases, and each member is returned as a comma-separated
recipient. The `a8m_mail` role therefore needs `SELECT` on `groups` and
`group_members` in addition to the mailbox tables:

```sql
GRANT SELECT ON tenants, users, mail_domains, user_aliases, mailboxes,
  groups, group_members TO a8m_mail;
```

`/etc/postfix/pgsql-virtual-alias.cf` query (one line in the file):

```sql
SELECT target FROM (
  SELECT u.email::text AS target
  FROM user_aliases a
  JOIN users u ON u.id = a.user_id
  JOIN tenants t ON t.id = u.tenant_id
  WHERE a.email = '%s' AND a.active = true AND u.status = 'active'
    AND u.deleted_at IS NULL AND t.status = 'active'
  UNION ALL
  SELECT string_agg(u.email::text, ',') AS target
  FROM groups g
  JOIN group_members gm ON gm.group_id = g.id
  JOIN users u ON u.id = gm.user_id
  JOIN tenants t ON t.id = g.tenant_id
  WHERE g.email = '%s' AND g.status = 'active' AND u.status = 'active'
    AND u.deleted_at IS NULL AND t.status = 'active'
  GROUP BY g.id
) expanded LIMIT 1
```

Use a SQL file on `/tmp` when applying grants/migrations because `postgres`
cannot traverse `/opt/a8m` (mode 0750, owner `a8m`). After changing
grants or a map, run `postmap -q <group-address>
pgsql:/etc/postfix/pgsql-virtual-alias.cf` and `postfix reload`; a `451` already
cached by a running `trivial-rewrite` process disappears after the reload.

### 5.8 Rspamd, DKIM and ClamAV

- Antivirus: `clamav` block with `action = "reject"`, whole-message scanning
  and the `/var/run/clamav/clamd.ctl` socket.
- DKIM: key generated for `a8m.test`, selector `a8m`; DNS TXT record
  is in `/var/lib/rspamd/dkim/a8m.test.a8m.txt`.
- Signing applies to local/authenticated submission. Tested header:
  `DKIM-Signature: ... d=a8m.test ...`.
- Rspamd Redis is configured with the local Redis password.
- Milfers use `inet:127.0.0.1:11332`.

Publish the DKIM public key and SPF record in real DNS before external mail:

```text
a8m._domainkey.a8m.test. TXT "<contents of the generated .txt file>"
a8m.test.                       TXT "v=spf1 mx -all"
_dmarc.a8m.test.                TXT "v=DMARC1; p=reject; rua=mailto:dmarc@a8m.test"
```

#### DKIM signing keys and DNS records

Rspamd signs outbound mail using per-domain keys under
`/var/lib/rspamd/dkim/$domain.$selector.key`.
`/etc/rspamd/local.d/dkim_signing.conf` must contain:

```text
enabled = true;
path = "/var/lib/rspamd/dkim/$domain.$selector.key";
selector = "a8m";
selector_map = "/var/lib/rspamd/dkim/selectors.map";
use_domain = "header";
allow_username_mismatch = true;
allow_hdrfrom_mismatch = true;
sign_networks = ["127.0.0.0/8", "::1"];
```

`a8m-dkimprovision.timer` runs a root oneshot every minute. For every
`mail_domains` row it:

1. reuses the existing private key if present, otherwise generates a 2048-bit
   RSA key owned by `_rspamd` with mode 0600;
2. derives the DKIM `p=` value from the public key and stores
   `dkim_public_key` / `dkim_record` in PostgreSQL;
3. rewrites `/var/lib/rspamd/dkim/selectors.map` as `domain selector` lines;
4. reloads Rspamd after a key or selector-map change.

The admin domain screen reads the public record from PostgreSQL, so it always
shows the actual current key—never a placeholder. If a key is still being
generated, the record is shown as provisioning and becomes copyable after the
next timer pass. Changing a domain's selector clears the stale key material and
the next pass creates the new selector key.

Domain verification checks:

- ownership TXT at `_a8m-verification.<domain>`;
- MX records point at `ALT8MAIL_MAIL_HOSTNAME`;
- an SPF TXT record exists;
- the selector TXT is `v=DKIM1` and its `p=` exactly matches the active signing
  key (not merely any DKIM record);
- DMARC has a valid `p=none|quarantine|reject`.

`a8m.test` is a reserved test domain with no public DNS, so the staging
verification screen will correctly report missing DNS records even though the
real DKIM key and Rspamd signature are working locally. Use a customer-owned
domain with public DNS for a successful end-to-end verification.

Inspect without exposing private key material:

```bash
cd /tmp
runuser -u postgres -- psql -d a8m -c   "SELECT domain, dkim_selector, dkim_record FROM mail_domains ORDER BY domain;"
cat /var/lib/rspamd/dkim/selectors.map
doveadm fetch -u nora.patel@a8m.test hdr mailbox INBOX |
  grep -A4 -i '^DKIM-Signature'
```

### 5.9 Go system backend

```bash
cd /opt/a8m/app/backend
go mod tidy
go test ./...
go build -trimpath -ldflags "-s -w -X main.version=0.9.0-staging" \
  -o /opt/a8m/bin/a8m-server ./cmd/server
go build -trimpath -ldflags "-s -w -X main.version=0.9.0-staging" \
  -o /opt/a8m/bin/a8m-admin-server ./cmd/adminserver
go build -trimpath -ldflags "-s -w" -o /opt/a8m/bin/a8m-admin ./cmd/admin
go build -trimpath -ldflags "-s -w" -o /opt/a8m/bin/a8m-inituser ./cmd/inituser
go build -trimpath -ldflags "-s -w" -o /opt/a8m/bin/a8m-mailprovision ./cmd/mailprovision
go build -trimpath -ldflags "-s -w -X main.version=0.9.0-staging" -o /opt/a8m/bin/a8m-dkimprovision ./cmd/dkimprovision
install -d -o a8m -g a8m /opt/a8m/admin
cp -a /path/to/a8m/admin/. /opt/a8m/admin/
install -m 644 ../deploy/systemd/a8m-mailprovision.service /etc/systemd/system/
install -m 644 ../deploy/systemd/a8m-mailprovision.timer /etc/systemd/system/
install -m 644 ../deploy/systemd/a8m-dkimprovision.service /etc/systemd/system/
install -m 644 ../deploy/systemd/a8m-dkimprovision.timer /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now a8m-mailprovision.timer a8m-dkimprovision.timer
systemctl restart a8m-server a8m-admin
```

Migration `0003_real_auth.sql` adds `users.enrollment_required` and
`auth_challenges.kind`; `0004_customer_admin.sql` adds the admin RBAC, groups,
org units, admin sessions/audit, domain verification and tenant policy fields;
`0005_mail_provisioning.sql` adds explicit Maildir provisioning state and
`password_history` for password reuse enforcement; `0006_dkim_keys.sql` adds
the DKIM public record/key state and backfills missing domain ownership tokens;
`0007_workspace.sql` creates normalized tasks, files, guest shares, calendar,
contacts, storage accounts, OAuth state and workspace audit tables and moves
legacy JSONB workspace documents into them; `0008_mta_sts.sql` adds the MTA-STS
policy state; `0009_dav.sql` adds DAV resource names and contact UID/ETag
columns; `0010_app_passwords.sql` creates revocable client credentials for
CalDAV/CardDAV and other clients that cannot complete interactive MFA;
`0011_mail_tables.sql` creates normalized mail messages, attachment metadata,
message-attachment links and the PostgreSQL full-text index, backfills existing
messages/attachments and removes those keys from `app_states`; `0012_outbox.sql`
normalizes the scheduled/undo-send queue; `0013_undo_table.sql` moves undo
snapshots into `undo_actions`; `0014_compose_identity_ai.sql` adds verified
sending identities, tenant AI settings/cache, soft app-password revocation and
HTML bodies in the outbox. After `0014`, `app_states` contains no mail, draft,
attachment, outbox or undo content.
Apply migrations **before** restarting a backend that selects the new columns;
without them, authentication and admin routes fail closed. Migrations are
idempotent (`IF NOT EXISTS` / `ON CONFLICT DO NOTHING`). See [ADMIN.md](ADMIN.md)
for the admin console, permissions and subdomain deployment.

Environment is `/opt/a8m/secrets/app.env`, loaded by systemd as user
`a8m`. The unit uses `NoNewPrivileges`, `PrivateTmp`, `PrivateDevices`,
`ProtectSystem=strict`, `ProtectHome`, an empty capability set and only
`/opt/a8m/uploads` writable. Mail worker and security settings:

```text
ALT8MAIL_PUBLIC_URL=https://130-94-46-22.sslip.io
ALT8MAIL_MAIL_HOSTNAME=mail.a8m.test
ALT8MAIL_DKIM_DIR=/var/lib/rspamd/dkim
ALT8MAIL_DKIM_SELECTOR_MAP=/var/lib/rspamd/dkim/selectors.map
ALT8MAIL_IMAP_ADDR=127.0.0.1:993
ALT8MAIL_IMAP_MASTER_USER=a8m-worker
ALT8MAIL_IMAP_MASTER_PASSWORD_FILE=/opt/a8m/secrets/imap_master_password
ALT8MAIL_MAILDIR_BASE=/var/vmail
ALT8MAIL_MAIL_WORKER_CONCURRENCY=8
# Legacy single-mailbox fallback; master auth takes precedence when configured.
ALT8MAIL_IMAP_USER=avery.chen@a8m.test
ALT8MAIL_IMAP_PASSWORD_FILE=/opt/a8m/secrets/avery_mail_password
ALT8MAIL_IMAP_TLS_NAME=mail.a8m.test
ALT8MAIL_IMAP_SYNC_SECONDS=300
ALT8MAIL_IMAP_POLL_SECONDS=15
ALT8MAIL_IMAP_IDLE=true
ALT8MAIL_SMTP_ADDR=127.0.0.1:587
ALT8MAIL_SMTP_TLS_NAME=mail.a8m.test
ALT8MAIL_MAIL_CA_FILE=/etc/ssl/a8m/mail.crt
ALT8MAIL_MANAGESIEVE_ADDR=127.0.0.1:4190
ALT8MAIL_MANAGESIEVE_TLS_NAME=mail.a8m.test
ALT8MAIL_WEBAUTHN_RPID=130-94-46-22.sslip.io
ALT8MAIL_WEBAUTHN_ORIGINS=https://130-94-46-22.sslip.io
ALT8MAIL_ADMIN_LISTEN=127.0.0.1:8081
ALT8MAIL_ADMIN_DIR=/opt/a8m/admin
ALT8MAIL_ADMIN_URL=https://130-94-46-22.sslip.io/admin/
ALT8MAIL_ADMIN_SESSION_HOURS=8
ALT8MAIL_CLAMAV_ADDR=/var/run/clamav/clamd.ctl

# Optional storage connectors; leave OAuth IDs empty until the provider app is ready.
ALT8MAIL_S3_ENDPOINT=127.0.0.1:9000
ALT8MAIL_S3_REGION=us-east-1
ALT8MAIL_S3_BUCKET=a8m
ALT8MAIL_S3_ACCESS_KEY=...
ALT8MAIL_S3_SECRET_KEY=...
ALT8MAIL_S3_USE_SSL=false
ALT8MAIL_S3_PATH_STYLE=true
ALT8MAIL_GOOGLE_CLIENT_ID=
ALT8MAIL_GOOGLE_CLIENT_SECRET=
ALT8MAIL_DROPBOX_CLIENT_ID=
ALT8MAIL_DROPBOX_CLIENT_SECRET=
ALT8MAIL_MICROSOFT_CLIENT_ID=
ALT8MAIL_MICROSOFT_CLIENT_SECRET=
```

`ALT8MAIL_WEBAUTHN_RPID` is a bare hostname (no scheme/port) and
`ALT8MAIL_WEBAUTHN_ORIGINS` is a comma-separated origin allowlist. If unset, both
derive from `ALT8MAIL_PUBLIC_URL`. Changing the public hostname requires updating
these values; existing passkeys are bound to the old RP ID.

Dovecot and Postfix both present the `mail.a8m.test` certificate from
`/etc/ssl/a8m/mail.crt`; Dovecot reads a copy under
`/etc/dovecot/private/alt8mail.{crt,key}`. ManageSieve presents the same
certificate. The Go client verifies that CA and the `mail.a8m.test` name
for IMAP, SMTP and ManageSieve.

`ALT8MAIL_IMAP_SYNC_SECONDS` controls the full reconciliation scan. Each worker
keeps an INBOX `IDLE` connection when `ALT8MAIL_IMAP_IDLE=true` (the default):
new mail, flag changes and expunges wake the worker immediately, and
`refreshInboxFlagsForUser` applies flags/deletions without re-downloading bodies.
If a server does not advertise IDLE, the worker logs that fact and falls back to
the new-mail check (`ALT8MAIL_IMAP_POLL_SECONDS`, default 15, minimum 5), which
performs a cheap `UID SEARCH` on INBOX and fetches only new bodies. The browser
also polls the inbox every 15 seconds and updates the open list/reader in place;
new mail therefore appears without a page refresh and raises a desktop
notification or an in-app toast. Lowering the poll interval increases IMAP
connections per mailbox. The Go process runs one worker per active mailbox,
serialized by a per-user mutex and bounded by
`ALT8MAIL_MAIL_WORKER_CONCURRENCY` (default 8). IDLE uses one additional IMAP
connection per active mailbox; size Dovecot's `mail_max_userip_connections`,
process limits and file descriptors before enabling it for a very large user
base. That isolates users while keeping a restart from opening every mailbox at
once.

Set the web password:

```bash
printf '%s\n' "$(cat /opt/a8m/secrets/staging_avery_password)" \
  | /opt/a8m/bin/a8m-admin -email avery.chen@a8m.test
```

Jordan's credentials are managed the same way. Its web password file is
`staging_jordan_password`, and the mailbox password was set through `doveadm pw`
and stored in `jordan_mail_password`:

```bash
printf '%s\n' "$(cat /opt/a8m/secrets/staging_jordan_password)" \
  | /opt/a8m/bin/a8m-admin -email jordan.lee@a8m.test
```

Jordan is a real Go authentication identity with its own Maildir. Every active
mailbox now has a per-user worker; Avery, Jordan and the seeded directory users
are mirrored independently.

Initialize a **real, non-demo** mailbox state. This replaces the user's
`app_states` document with identity, aliases and directory contacts from
PostgreSQL and leaves messages, tasks, files, calendar and audit empty; the IMAP
worker then imports the mailbox from Dovecot:

```bash
set -a; . /opt/a8m/secrets/app.env; set +a
/opt/a8m/bin/a8m-inituser -email avery.chen@a8m.test
systemctl restart a8m-server
```

`seed/state.json` and `a8m-seedstate` belong to the retired Node preview
and are not used by the live service. Do not run them against the staging Go
mailbox.

Create the staging-only directory recipients so delivery to seeded addresses
resolves (they have no web password and cannot sign in):

```bash
runuser -u postgres -- psql -d a8m \
  -f /opt/a8m/app/backend/seed/demo_mailboxes.sql
for d in elena.vasquez marcus.webb nora.patel priya.shah \
         tomas.ribeiro lena.fischer daniel.kim aisha.bello; do
  install -d -o vmail -g vmail "/var/vmail/a8m.test/$d/Maildir"
done
```


#### Per-user workers, Maildir provisioning and retention

`a8m-server` starts a worker per active mailbox after its initial directory
refresh and refreshes that list every 60 seconds. Each worker keeps an INBOX
IDLE connection when `ALT8MAIL_IMAP_IDLE=true` and otherwise performs a UID-only
INBOX check every `ALT8MAIL_IMAP_POLL_SECONDS`; both also run a full folder
reconciliation every `ALT8MAIL_IMAP_SYNC_SECONDS`. A per-user mutex serializes
the worker, web sync, mailbox actions and retention; different users proceed in
parallel.

`a8m-mailprovision` is a root-run oneshot invoked by
`a8m-mailprovision.timer` every minute. It claims only `pending`/`error`
mailboxes and creates the Maildir++ tree (`cur`, `new`, `tmp`, `.Sent`,
`.Drafts`, `.Archive`, `.Junk`, `.Trash`), chowns it to `vmail:vmail` (UID/GID
5000) and sets the provisioning fields surfaced in the admin console. The unit
uses `ProtectSystem=strict`, `PrivateTmp`, no home access, only `/var/vmail`
writable and a minimal capability set (`CAP_CHOWN`, `CAP_DAC_OVERRIDE`,
`CAP_FOWNER`). Run `-all` manually to re-verify every mailbox:

```bash
/opt/a8m/bin/a8m-mailprovision -all
```

Retention follows the tenant `mail.retentionDays` policy. A daily background
pass, plus `POST /admin/api/maintenance/retention` from the Policies screen,
removes messages older than the cutoff from INBOX, Sent, Archive and other
synchronized folders. Drafts, Junk and Trash are excluded. It uses the IMAP
UIDPLUS `UID EXPUNGE` extension so it never purges another client's pending
deletions, removes matching messages from the webmail state and records a
per-user audit entry. A manual run is audited in the admin log.

### 5.10 nginx, frontend and reference feature service

- Deployed frontend: `/var/www/a8m` (owned `www-data`).
- Site: `/etc/nginx/sites-available/a8m` (symlinked into `sites-enabled`).
- HTTP 80 redirects to HTTPS 443.
- `/api/` proxies to the **Go system backend** on `127.0.0.1:8080`.
- `/system/api/` proxies to the **same Go system backend** under a
  namespace that avoids collisions with a future reverse-proxy split.
- `location ^~ /admin/` proxies to the **Go admin backend** on
  `127.0.0.1:8081`; `location = /admin` also proxies there so the admin mux can
  redirect to `/admin/`. `^~` is required so the static `.js`/`.css` regex in
  the mail site does not serve admin assets from `/var/www/a8m`.
- The admin binary serves its own UI from `/opt/a8m/admin`; relative asset
  and API paths let the same UI run at `/admin/` or at an admin subdomain root.
- `.mjs` must be served as `application/javascript`; this required adding
  `mjs` to `/etc/nginx/mime.types`.
- `location ~ ^/(dav/|\.well-known/(caldav|carddav))` proxies CalDAV/CardDAV
  discovery and resources to `127.0.0.1:8080` with a 120-second read timeout
  and a 4 MB request limit. `location = /.well-known/mta-sts.txt` proxies the
  MTA-STS policy. Both blocks are in
  `deploy/nginx/a8m-dav-mta.conf`.
- CSP is enabled. HSTS is deferred until a production-owned domain is in use so
  the self-signed IP fallback does not get pinned by browsers.
- Self-signed certificate SANs include the public IP.

The admin backend runs under a sibling unit:

```ini
# /etc/systemd/system/a8m-admin.service
[Unit]
Description=Alt8Mail customer and system administration backend
After=network-online.target postgresql.service redis-server.service
Wants=network-online.target
Requires=postgresql.service redis-server.service

[Service]
Type=simple
User=a8m
Group=a8m
WorkingDirectory=/opt/a8m
EnvironmentFile=/opt/a8m/secrets/app.env
ExecStart=/opt/a8m/bin/a8m-admin-server
Restart=on-failure
RestartSec=2
UMask=0027
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=true
LockPersonality=true
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target
```

Grant the first system administrator after migration:

```bash
set -a; . /opt/a8m/secrets/app.env; set +a
/opt/a8m/bin/a8m-admin -grant-system-admin -email avery.chen@a8m.test
/opt/a8m/bin/a8m-admin -list-admins
```

The reference feature service is stopped and disabled. Its unit and source are
retained only as an archive; it is not proxied and its demo JSON is not used by
the staging frontend. The old unit definition was:

```ini
# /etc/systemd/system/a8m-preview.service
[Service]
User=a8m
WorkingDirectory=/opt/a8m/preview
Environment=PORT=8001
Environment=HOST=127.0.0.1
Environment=DATA_FILE=/opt/a8m/data/preview/db.json
ExecStart=/usr/local/bin/node server.js
ProtectSystem=strict
ProtectHome=true
NoNewPrivileges=true
PrivateTmp=true
ReadWritePaths=/opt/a8m/data/preview
CapabilityBoundingSet=
```

Deploy frontend updates only:

```bash
tar -czf /tmp/a8m-public.tgz -C public .
scp /tmp/a8m-public.tgz root@130.94.46.22:/tmp/
ssh root@130.94.46.22 '
  tar -xzf /tmp/a8m-public.tgz -C /var/www/a8m &&
  chown -R www-data:www-data /var/www/a8m &&
  nginx -t && systemctl reload nginx
'
```

Do **not** restart `a8m-preview` or copy its demo data onto the real
mailbox host. The old preview unit above is documented only for audit/history.

The deployed `index.html` keeps the template's staging login prefill
(`avery.chen@a8m.example` / `a8m-demo`) so testing is easy; Go maps
that staging alias to the canonical `.test` identity. It does not supply any
mailbox or workspace content. The server copy does not alter the local repository
template.

**Go API transition completed:** nginx `location /api/` proxies to
`127.0.0.1:8080`; the full API and browser smoke suites pass against Go, and the
UI reads only real Dovecot/PostgreSQL data. `a8m-preview` is stopped and
disabled. The saved nginx fragment
`/etc/nginx/sites-available/a8m.node-preview` and the old unit are archived
for historical comparison only; do not re-enable the demo preview on a real
mailbox host.

### 5.11 CalDAV, CardDAV and app passwords

CalDAV and CardDAV are served by the Go backend under `/dav/`:

```text
Discovery:   /.well-known/caldav  -> 301 /dav/
             /.well-known/carddav -> 301 /dav/
Principal:   /dav/principals/<user-id>/
Calendars:   /dav/calendars/<user-id>/default/
Events:      /dav/calendars/<user-id>/default/<resource>.ics
Addresses:   /dav/addressbooks/<user-id>/default/
Contacts:    /dav/addressbooks/<user-id>/default/<resource>.vcf
```

The collection paths are discovered from the principal PROPFIND response; do
not hard-code `<user-id>` in a client. Authentication is HTTP Basic. When the
account has TOTP or a passkey enrolled, the interactive password is rejected:
create a revocable app password in **Settings → Security & privacy → App
passwords** (API: `POST /api/security/app-passwords`). The token is shown once.
Create it permanent or with a 7/30/90/365-day/custom expiry. A revoked or
expired token is rejected on the next DAV request and remains in the owner's
active/expired/revoked history; revocation is a soft delete. Without a second
factor, the normal account password works. App passwords are stored as SHA-256
hashes of 192-bit random tokens; use them only over TLS.

Failed Basic-auth attempts are rate-limited per client IP (30 failures per 10
minutes; successful authentication resets the counter), so a desktop client
with a stale password cannot be used for unbounded guessing.

Supported methods: `OPTIONS`, `GET`, `HEAD`, `PROPFIND`, `PROPPATCH`, `REPORT`,
`PUT`, `DELETE`. Calendar/contact writes map to the normalized
`calendar_events`, `calendar_attendees` and `contacts` tables; ETags and
sync-tokens are returned in DAV responses. A card without an `EMAIL` property is
rejected because the personal contact model requires an email address.

Verify from a shell (use an app password, never put the account password in
shell history):

```bash
BASE=https://mail.example.com
USER_ID=$(curl -fsS -u "$EMAIL:$APP_PASSWORD" -X PROPFIND -H 'Depth: 0' \
  "$BASE/dav/" | sed -n 's:.*<D:current-user-principal><D:href>/dav/principals/\([^/]*\)/.*:\1:p')
curl -fsS -u "$EMAIL:$APP_PASSWORD" -X PROPFIND -H 'Depth: 1' \
  "$BASE/dav/calendars/$USER_ID/"
```

Run `python3 tools/dav-smoke.py` after setting `GO_API`, `DAV_EMAIL` and
`DAV_PASSWORD` to exercise discovery plus event/contact create, read, report and
delete. The smoke deletes everything it creates.

### 5.12 MTA-STS publication

MTA-STS state is stored per domain in `mail_domains.mta_sts_mode`,
`mta_sts_max_age` and `mta_sts_verified`. Enable it in **Admin → Domains → MTA-STS
mode** (`none`, `testing`, `enforce`) after the customer domain is verified.
The admin DNS table then shows:

```text
TXT  _mta-sts.<domain>  v=STSv1; id=<policy-id>
POLICY https://mta-sts.<domain>/.well-known/mta-sts.txt
```

The API serves the policy body at `/.well-known/mta-sts.txt` when the request
`Host` is `mta-sts.<domain>`, the domain is verified and its mode is not
`none`:

```text
version: STSv1
mode: enforce
mx: mail.example.com
max_age: 604800
```

Requirements:

- Publish the `_mta-sts` TXT record with the exact `id` shown in the admin UI.
  The id changes whenever mode, max-age or MX changes.
- Obtain a publicly trusted TLS certificate whose SAN includes
  `mta-sts.<domain>`; MTA-STS requires HTTPS with a valid certificate. Add a
  dedicated nginx server block for the host and proxy only the policy path:

```nginx
server {
    listen 443 ssl http2;
    server_name mta-sts.customer.example;
    ssl_certificate     /etc/letsencrypt/live/mta-sts.customer.example/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/mta-sts.customer.example/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;

    location = /.well-known/mta-sts.txt {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
    location / { return 404; }
}
```

Run **Run DNS verification** in the domain drawer. Verification checks the
`_mta-sts` TXT id, fetches the policy over HTTPS through a public-IP-only
dialer (private, loopback and link-local destinations are refused), and compares
`version`, `mode`, `max_age` and the MX host. `a8m.test` cannot pass public
verification because it has no public DNS; the endpoint and verifier are tested
with unit fakes and a Host-header policy request on staging.

### 5.13 S3/MinIO and provider OAuth

S3/MinIO is configured entirely by environment and is connected per user from
the Files workspace:

```text
ALT8MAIL_S3_ENDPOINT=127.0.0.1:9000
ALT8MAIL_S3_REGION=us-east-1
ALT8MAIL_S3_BUCKET=a8m
ALT8MAIL_S3_ACCESS_KEY=...
ALT8MAIL_S3_SECRET_KEY=...
ALT8MAIL_S3_USE_SSL=false
ALT8MAIL_S3_PATH_STYLE=true
```

Web attachment uploads and guest-share replacements are scanned through clamd
INSTREAM when `ALT8MAIL_CLAMAV_ADDR` is set. Staging uses
`/var/run/clamav/clamd.ctl` (socket mode 666); the app's systemd sandbox already
allows AF_UNIX. When scanning is configured, a scanner error fails the upload
closed with `503 ATTACHMENT_SCAN`; an infected file returns
`422 ATTACHMENT_INFECTED`. Leave the variable empty only for local development.

Objects are stored under
`tenants/<tenant>/users/<user>/files/<file-id>/<name>`. Connect, upload, sync,
download and delete are live-tested. Deleting a file removes the object before
the row, so a later sync does not re-import it. A successful full sync also
removes local rows whose object no longer exists in the bucket; objects written
outside the web API are indexed but not re-scanned, so use a private bucket and
bucket policy for that trust boundary. Staging uses an S3-compatible
rclone server on `127.0.0.1:9000`; production should use MinIO, Ceph RGW, AWS S3
or another S3-compatible service with TLS and a dedicated bucket policy. Keep
the bucket private; the API serves authorized downloads.

Google Drive, Dropbox and OneDrive use OAuth 2 authorization-code flows. Create
an app in each provider and register the callback:

```text
https://mail.example.com/api/storage/oauth/google/callback
https://mail.example.com/api/storage/oauth/dropbox/callback
https://mail.example.com/api/storage/oauth/microsoft/callback
```

Set `ALT8MAIL_GOOGLE_CLIENT_ID`, `ALT8MAIL_GOOGLE_CLIENT_SECRET`,
`ALT8MAIL_DROPBOX_CLIENT_ID`, `ALT8MAIL_DROPBOX_CLIENT_SECRET`,
`ALT8MAIL_MICROSOFT_CLIENT_ID` and `ALT8MAIL_MICROSOFT_CLIENT_SECRET`. Providers
are shown as unconfigured until both ID and secret are present. Refresh/access
tokens are AES-GCM sealed with a key derived from `ALT8MAIL_SESSION_SECRET`;
rotating that secret requires users to reconnect. OAuth cannot be live-tested
without real provider apps; the authorization, callback, refresh, upload,
download and sync paths are implemented and unit/API boundaries are documented
in `backend/README.md`.

### 5.14 Large-mailbox sync, workspace data and scale

Full sync keeps the authoritative per-folder UID set, downloads bodies only for
UIDs missing locally, imports newest-first within a 45-second per-pass budget,
and persists `mailSync.pending` plus per-folder `truncated` when older messages
remain. The budget is divided fairly across folders still to scan and every
folder gets at least one batch, so a huge Archive or INBOX cannot starve the
other folders. Subsequent worker passes continue the backfill. Quick INBOX polling
uses only UIDs above the checkpoint and has its own 15-second budget. Flag-only
reconciliation runs at most every 15 minutes (or immediately when the folder
message/UIDNEXT counts change) and never re-downloads bodies. A UIDVALIDITY
change drops the stale local copies for that folder and re-imports from the new
UID space. Deletions and moves made by other clients reconcile against the full
UID snapshot. There is no 1,000-message folder cap.

Mail is normalized in PostgreSQL. `mail_messages` stores one row per mailbox
message/draft with folder, IMAP identity, threading, body, flags, authentication
results and a generated `search_vector`; `attachments` stores attachment bytes
metadata and `mail_message_attachments` links messages to files. `outbox` holds
scheduled/undo-send items and `undo_actions` holds short-lived undo snapshots.
After migrations `0011`–`0013`, `app_states` contains no messages, drafts,
attachments, outbox bodies or undo snapshots; it retains folders, people, Sieve,
trusted senders, outbound counters, audit entries and sync checkpoints.

`/api/search` uses PostgreSQL full-text search (`search_vector` GIN index) plus
operator predicates for sender/recipient/subject/body, filename, flags, folder,
date and size. Subject and participant text is indexed with the `simple`
configuration and bodies with `english`; the query runs both
`websearch_to_tsquery('simple', ...)` and `websearch_to_tsquery('english', ...)`
so stemming cannot hide an unstemmed subject or sender match. The webmail sends
search strings to that endpoint and renders the returned messages in place of
the folder list, so searching does not require loading old mail into the
browser. `GET /api/messages` and `/api/bootstrap` read from the normalized
tables; the UI currently receives the mailbox view in one response, so
pagination is the next large-mailbox API step.

IMAP IDLE keeps an INBOX push connection per active mailbox and refreshes flags
and deletions in place; the 15-second poll remains the fallback when a server
does not support IDLE. Dovecot's two-minute `* OK Still here` keepalive is
ignored, so it does not cause a reconnect or a mailbox-wide flag fetch. A new
mail event runs the UID check only; expunge/FETCH events run the flag reconcile.
The connection renews its 25-minute IDLE command and reconnects after each
mailbox event. The per-user worker pool defaults to 8 concurrent mailboxes
(`ALT8MAIL_MAIL_WORKER_CONCURRENCY`), a 5-minute full sync and a 15-second
INBOX poll. At 1,000 users, tune the pool to CPU/IO, watch `mail_messages`
index/table size, and budget one additional IMAP connection per active mailbox
for IDLE. Guest share tokens are stored as SHA-256 hashes with
expiry, revocation and access counters; share pages are served at
`/files/s/<token>`. Back up the database and `ALT8MAIL_UPLOAD_DIR` together so
internal attachment/file content and rows stay consistent.

### 5.15 Compose 0.7, identities, forwarding and AI

The 0.7 frontend adds a rich HTML compose editor with a server-side allowlist
sanitizer (`compose_html.go`). Drafts and outbox items carry `bodyHtml`; the
MIME builder uses the sanitized HTML in the `multipart/alternative` part. Draft
`windowMode` values are `floating`, `minimized`, `inline` or `closed`. Open and
minimized drafts are restored on bootstrap; closed drafts are not. Compose size
(`standard`, `wide` = 2×, `full`) and position (`center`, `bottom-right`,
`bottom-left`) are user preferences in `users.preferences`.

**Sending identities.** `mail_identities` holds custom addresses. Internal
domains only accept an address already assigned as a user alias; external
addresses are `pending` until a 48-hour hashed-token link is confirmed by a POST
to `/api/identities/verify`. The link email is delivered through the normal
Postfix submission path, so DNS/SPF/reputation of the external domain can still
affect delivery. The manager UI is Settings → Identities & forwarding. Default
From in Settings → Mail & reading is populated from the live identity list.

**Forwarding.** `PUT /api/forwarding` stores a dated or permanent target,
keep-copy flag and period. `renderSieve` emits a `currentdate` block using the
`date` + `relational` extensions and outputs the full `require [...]` list.
`pushSieveScript` now consumes ManageSieve literal error text, so compiler
errors include Dovecot's real message. A forwarding-only script with dates is
valid; verify via `GET /api/sieve/script`.

**AI assistant.** Admin → AI assistant toggles `tenant_ai_settings` per tenant
and stores the API key AES-GCM sealed. OpenAI-compatible (OpenAI, DeepSeek,
Azure/local gateways) and Anthropic Messages providers are supported. The user
compose panel exposes 8 styles, 3 lengths, a creativity slider and 36 languages.
The reader never generates AI output automatically. A split **AI** button offers
**Summarise this email** over the recent conversation or **Translate Email** into
the user's language, and **Reply with AI** opens a generated reply in compose.
The server caches summaries/translations in `mail_ai_cache`; Reply with AI is
not cached. The UI is hidden while the tenant toggle is off. Use a dedicated
provider key with a spending limit and verify the endpoint with "Save & test"
before enabling it for users.

**App passwords.** Expiry is stored in `expires_at`; `revoked_at` makes
revocation a history-preserving soft delete. DAV checks both on every request,
so expiry/revocation takes effect immediately even if the browser has a cached
session. Settings → Security & privacy shows active/expired/revoked entries and
supports permanent, 7/30/90/365-day or custom expiry.

**Notifications and splash.** The bell is a live notification center (unread
mail, scheduled sends, calendar, security audit, quota) with read state in
browser localStorage and a full All/Unread/Mail/System window. The boot splash
covers session restore on reload and fades only after bootstrap completes.
Interactive authentication (password, SSO, TOTP, passkey or enrollment) holds
the branded "Opening your workspace" screen for five seconds before the inbox
is revealed. Reader AI summaries and translations are never generated
automatically; the user must use the split AI button or Reply with AI.

## 6. Verification evidence

Run these on the server.

### 6.1 Service health

```bash
systemctl is-active postgresql redis-server dovecot postfix rspamd clamav-daemon nginx a8m-server fail2ban
curl -ksS https://127.0.0.1/api/health
curl -ksS https://127.0.0.1/system/api/health
curl -ksS https://127.0.0.1/admin/api/health
```

Expected backends:

```json
{"checks":{"postgres":"ok","redis":"ok"},"env":"staging","ok":true,"service":"a8m-system-backend"}
```

### 6.2 Local mail delivery

```bash
swaks --server 127.0.0.1:25 --from staging-test@external.test \
  --to avery.chen@a8m.test --header 'Subject: delivery test' \
  --body 'test' --suppress-data
```

Observed: `250 2.0.0 Ok: queued`, Dovecot LMTP `saved mail to INBOX`, file in
`/var/vmail/a8m.test/avery.chen/Maildir/new`.

### 6.3 Authenticated submission and IMAP/ManageSieve

```bash
MAIL_PW=$(cat /opt/a8m/secrets/avery_mail_password)
swaks --server 127.0.0.1:587 --tls --auth PLAIN \
  --auth-user avery.chen@a8m.test --auth-password "$MAIL_PW" \
  --from avery.chen@a8m.test --to avery.chen@a8m.test \
  --header 'Subject: auth test' --body 'test' --suppress-data
```

Observed: `235 Authentication successful`, queued and delivered. Python
`imaplib.IMAP4_SSL` login and `LIST/SEARCH` succeeded. ManageSieve STARTTLS,
PLAIN auth, `LISTSCRIPTS` and logout succeeded.

### 6.4 Antivirus enforcement

```bash
printf 'X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*' \
  | clamdscan --fdpass -
```

Observed: `Eicar-Test-Signature FOUND`. Sending the same content through Postfix
submission produced `554 5.7.1 clamav: virus found: "Eicar-Signature"`.

### 6.5 Web frontend and API

Both `/api` and `/system/api` reach the Go service. Use `/system/api` for
direct operator checks so the browser cookie names do not overlap with a
temporary Node preview session.

```bash
# Public root API health (Go)
curl -ksS https://127.0.0.1/api/health

# Namespaced Go API health
curl -ksS https://127.0.0.1/system/api/health

# Go API login
curl -ksS -c /tmp/go.cookies -H 'Content-Type: application/json' \
  -d "{\"email\":\"avery.chen@a8m.test\",\"password\":\"$(cat /opt/a8m/secrets/staging_avery_password)\"}" \
  https://127.0.0.1/system/api/session
curl -ksS -b /tmp/go.cookies https://127.0.0.1/system/api/bootstrap \
  | jq '{user:.user.email,messages:(.messages|length),tasks:(.tasks|length)}'
```

Observed: Go login 200, bootstrap 200 with `avery.chen@a8m.test`, real IMAP
messages and `mailboxMode: "imap"`. No reference JSON subjects are present.
Tasks, Files and Calendar start empty. The API smoke test
`tools/go-api-smoke.py` passes all login, bootstrap, action/undo, tasks,
draft persistence, scheduled send, real SMTP, attachment, storage, Sieve,
preference, search, audit and sender-trust checks. The browser smoke test
`tools/go-ui-smoke.mjs` signs in through the actual UI and renders real mail,
search, Scheduled, Tasks, Files, People and Calendar with no runtime errors.
Latest screenshot: `artifacts/go-feature-api.png`.

### 6.6 IMAP sync and real submission

```bash
# Force an IMAP sync (requires the session CSRF token)
curl -ksS -b /tmp/go.cookies -H "X-CSRF-Token: $CSRF" \
  -H 'Content-Type: application/json' -d '{}' \
  https://127.0.0.1/system/api/mail/sync | jq

# Check the synced UID checkpoints and last error
curl -ksS -b /tmp/go.cookies https://127.0.0.1/system/api/mail/sync | jq
```

Observed on 2026-10-28 after `a8m-inituser` replaced all demo state:

- The worker created `Sent`, `Drafts`, `Archive`, `Junk` and `Trash` and synced
  the real Maildir (the live state includes the preserved self-send, its reply
  and the user's open draft);
  bootstrap reported `mailboxMode: "imap"`, zero tasks, zero files and zero
  calendar events.
- Star/read actions appeared in Dovecot flags. An archive action moved the
  message to IMAP Archive; undo moved it back; Trash count returned to zero.
- A permanent delete marked `\Deleted`, restored correctly on undo, and was
  expunged by the deferred worker after the undo window.
- A saved compose draft appeared in `Drafts` via `doveadm search`; deleting it
  removed the IMAP copy. A scheduled send advanced with **Send now** returned
  200, delivered through Postfix submission, produced a message in
  `/var/vmail/a8m.test/nora.patel/Maildir`, and appended to Dovecot Sent.
- `tools/go-api-smoke.py` passed all real-mail assertions, and
  `tools/go-ui-smoke.mjs` rendered the real inbox in Chromium.

See `journalctl -u a8m-server` for `outbound send committed` and the IMAP
action warnings (if any).

### 6.7 Settings persistence

```bash
# Run from the repository root with the staging web password file
ALT8MAIL_UI_PASSWORD="$(cat ./staging_avery_password)" \
  node tools/go-settings-smoke.mjs
ALT8MAIL_UI_PASSWORD="$(cat ./staging_avery_password)" \
  node tools/go-settings-smoke.mjs > artifacts/go-settings-smoke.txt 2>&1

# Inspect the persisted row directly
runuser -u postgres -- psql -d a8m -x \
  -c "SELECT email, preferences FROM users WHERE email='avery.chen@a8m.test'"
```

Observed: appearance, reading behavior, notification choices, language/date/
time zone/work hours, signatures, undo window, rules, automatic replies, audit
filters, security policy and account/session rendering all passed. The test
reads `/api/preferences` back after the UI changes and verifies the DB-backed
values, then exercises `POST /api/preferences/reset`. Screenshots:
`artifacts/go-settings-security.png` and `artifacts/go-settings-account.png`.
Sieve rules are additionally verified in Dovecot with:

```bash
doveadm sieve list -u avery.chen@a8m.test
doveadm sieve get -u avery.chen@a8m.test a8m
```

Both showed the generated `a8m` script as active; a probe message was filed
into `Archive` and an automatic reply was delivered to a second local mailbox.

### 6.8 TOTP, WebAuthn and first-time enrollment

```bash
ALT8MAIL_UI_PASSWORD="$(cat ./staging_avery_password)" \
  node tools/go-auth-smoke.mjs

# Policy is per user and disabled in staging
runuser -u postgres -- psql -d a8m -Atc \
  "SELECT email, enrollment_required FROM users ORDER BY email"

# Factors and sessions after the test cleanup
runuser -u postgres -- psql -d a8m -Atc \
  "SELECT (SELECT count(*) FROM webauthn_credentials) AS passkeys,
          (SELECT count(*) FROM auth_challenges) AS challenges,
          (SELECT count(*) FROM sessions) AS sessions"
```

Observed on 2026-10-28:

- TOTP enrollment from Security & privacy generated a real secret; a code from a
  local RFC 6238 implementation enabled the factor in PostgreSQL.
- Signing out and back in returned a `two_factor` challenge. There was no demo
  bypass; the current TOTP code opened the mailbox. A used code could not be
  replayed because the challenge is consumed.
- Recovery codes were issued hashed, one code worked once, and reuse was
  rejected. The test restored the factor afterwards.
- Flipping `enrollment_required=true` (UI policy endpoint) changed the next
  password sign-in to the first-time enrollment screen, for both TOTP and
  Chrome's virtual WebAuthn authenticator.
- A passkey registered in Chrome produced a real `navigator.credentials.get()`
  assertion that the Go server verified, and the passkey opened the mailbox.
- Cleanup removed every passkey and TOTP factor, revoked test sessions and set
  `enrollment_required=false` for avery. Re-check the SQL above before handover.
- Test transcript: `artifacts/go-auth-smoke.txt`; screenshot:
  `artifacts/go-enrollment-required.png`.

The policy is **disabled at the database level in staging** as requested:
`users.enrollment_required = false` for avery and jordan. Enable it per user
from Security & privacy (the toggle is real) or:

```sql
UPDATE users SET enrollment_required = true, updated_at = now()
WHERE email = 'avery.chen@a8m.test';
```

There is no tenant-wide enrollment policy yet; that belongs to the customer
backend.

### 6.9 Session hygiene

Staging tests create browser sessions quickly. Before handing the environment to
someone, revoke others from Account → Sign-in sessions, or clear expired rows:

```bash
runuser -u postgres -- psql -d a8m -c \
  "DELETE FROM sessions WHERE expires_at <= now();"
```

Do not delete the session you are currently using unless you intend to sign out.

### 6.10 Live delivery and new-mail notifications

```bash
ALT8MAIL_UI_PASSWORD="$(cat ./staging_avery_password)" \
  node tools/go-live-mail-smoke.mjs

# External inbound check (local Postfix → LMTP → Dovecot)
swaks --server 127.0.0.1:25 --from nora.patel@a8m.test \
  --to avery.chen@a8m.test --header "Subject: live inbound check" \
  --body "delivery test" --suppress-data
```

Observed on 2026-10-28 after the sync fix:

- Sending from avery to avery produced **two** Maildir messages with the same
  Message-ID: UID 8 in Sent and UID 11 in INBOX. The old merge keyed only on
  Message-ID, so the INBOX copy continually overwrote the Sent row in JSON and
  never appeared in the inbox. The merge now keys by folder + Message-ID, keeps
  both copies and links them to one thread.
- The UID-based INBOX check imported the new message within one poll window, and
  the open Chromium page added the conversation to the inbox list without a
  reload (`window.__liveMarker` unchanged). The in-app toast fired when desktop
  notification permission was denied; with permission granted the same path
  creates a browser `Notification`.
- A separately injected Postfix/LMTP message from `nora.patel@a8m.test`
  appeared in avery's state as unread within 20 seconds.
- Test messages were then expunged and `POST /api/mail/sync` reconciled the
  state. The user's real `self` message and open `Re: self` draft were kept.
- Transcript: `artifacts/go-live-mail-smoke.txt`; screenshot:
  `artifacts/go-live-mail.png`.

The test closes any compose windows restored from server drafts before clicking
settings, because an open draft can overlay the right-side settings controls.
That was a test-harness issue, not a settings persistence failure.

### 6.11 Customer and system administration

```bash
# API, RBAC, users, groups, org units, roles, domains, policies, audit, tenants
ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password \
  python3 tools/admin-api-smoke.py

# Admin TOTP enrollment, challenge login and removal
ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password \
  python3 tools/admin-auth-smoke.py

# Tenant MFA policy reaches the mail user login
ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password \
  python3 tools/admin-policy-smoke.py

# Tenant create -> owner login -> suspend -> blocked -> reactivate
ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password \
  python3 tools/admin-tenant-smoke.py

# Full admin browser flows and mobile drawer
ALT8MAIL_ADMIN_PASSWORD="$(cat ./staging_avery_password)" \
  node tools/admin-ui-smoke.mjs
```

Observed on 2026-10-28:

- Admin login, bootstrap, user create/edit/reset/soft-close/restore, group and
  org-unit lifecycle, custom roles, policy persistence, domain pending/DNS
  failure handling, audit CSV, tenant create/context switch and system-admin
  grant/revoke all passed.
- Admin TOTP enrollment produced real recovery codes; password-only login then
  returned a `twoFactorRequired` challenge; a wrong code was rejected and the
  current code opened the console; removal required password plus a valid code.
- Setting `mfaEnforcement=all` caused an un-enrolled mail user login to return an
  enrollment challenge, then the policy was restored and password-only login
  worked again.
- A new tenant owner could sign in while active; suspending the tenant
  invalidated the existing session and blocked a fresh sign-in; reactivation
  restored access. The tenant and owner rows were removed after the test.
- The admin UI smoke passed desktop and a 390 px mobile viewport (no horizontal
  page overflow, navigation drawer opens). Screenshots are under `artifacts/`.
- Admin test tenants, users, groups and roles were removed after evidence
  capture. The tenant/user policies were restored to their pre-test values.

See [ADMIN.md](ADMIN.md) for the permission catalog, policy enforcement matrix,
subdomain deployment and known adapters (notably admin WebAuthn and guest-scoped
sharing).

### 6.12 Policy adapters, group delivery and provisioning

```bash
# Password flow, IMAP/POP/SMTP gates, forwarding, sharing, quarantine,
# retention and explicit Maildir provisioning. Open a local tunnel first if
# the operator network blocks submission port 587:
# ssh -f -N -L 1587:127.0.0.1:587 root@130.94.46.22
ALT8MAIL_SMTP_HOST=127.0.0.1 ALT8MAIL_SMTP_PORT=1587 \
  python3 tools/mail-policy-smoke.py
```

Observed on 2026-10-28:

- A fresh test user was provisioned by `a8m-mailprovision` to a
  `vmail`-owned Maildir shown in the admin detail. The temporary web password
  forced a password change; the old password stopped working for webmail and
  IMAP, the new web password authenticated to Dovecot, and password history
  rejected reuse of the temporary credential.
- Setting `imapAccess=false`, `popAccess=false` and `smtpAccess=false` produced
  real authentication failures for IMAPS, POP3S and submission; enabling them
  restored access.
- A redirect rule was rejected for an internal target when internal forwarding
  was off and for an external target when external forwarding was off. When the
  policy changed after a permitted rule was saved, the generated script was
  re-pushed without the `redirect` while the stored rule remained editable.
- Disabling external sharing returned `SHARING_DISABLED`; a view-only link
  default rejected an edit link and allowed a view link. Configuring an allowed
  sharing domain returned `SHARING_DOMAIN_RESTRICTED` for a bearer link.
- With the admin-only Junk release policy enabled, a self-delivered message
  reached INBOX, was moved to Junk, and a member was denied release with
  `QUARANTINE_ADMIN_ONLY`. The item appeared in the tenant-wide admin
  Quarantine queue; an administrator released it through the admin API, and it
  returned to the member's INBOX.
- A 2020 message appended directly to Archive was deleted by the retention
  endpoint while no recent message was touched. The worker used UIDPLUS
  `UID EXPUNGE` and pruned the webmail state.
- Group delivery was tested separately by creating a distribution group with
  Avery and Jordan, sending through Postfix and finding the message in both
  mailboxes. Evidence: `artifacts/group-delivery-smoke.txt`. The SQL alias map
  now expands active groups.
- A separate expired-timestamp test forced `passwordChangeRequired=true` at
  both sign-in and bootstrap, allowed the restricted password-change endpoint,
  then cleared the flag on the next session. Evidence:
  `artifacts/password-expiry-smoke.txt`.
- Transcript: `artifacts/mail-policy-smoke.txt`. Test users, tenants, maildirs
  and groups were removed after the pass.

### 6.13 DKIM keys and domain DNS verification

```bash
ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password   python3 tools/dkim-smoke.py

# Real signing test (local Postfix -> Rspamd -> Dovecot LMTP)
swaks --to nora.patel@a8m.test --from avery.chen@a8m.test   --server 127.0.0.1:25 --header "Subject: DKIM check" --body "test"
doveadm fetch -u nora.patel@a8m.test hdr mailbox INBOX uid <uid> |
  grep -A5 -i '^DKIM-Signature'
```

Observed on 2026-10-28:

- The migrated `a8m.test` key is imported into PostgreSQL and served by
  `/admin/api/domains` as the real `v=DKIM1; k=rsa; p=...` value. No placeholder
  remains; `dkim-smoke.py` passes.
- A test domain with selector `testsel2` caused the root worker to generate a
  2048-bit key, write `selectors.map`, update the database and reload Rspamd.
  A local message from that domain was signed with
  `DKIM-Signature: ... d=dkim-smoke-<stamp>.test; s=testsel2;`.
- The test domain, key and message were removed after the check.
- The verification endpoint checks the exact DKIM `p=` against the active key,
  MX against `ALT8MAIL_MAIL_HOSTNAME`, SPF, DMARC and ownership. Unit tests cover
  the success/mismatch paths with a fake DNS resolver.
- Public-DNS success cannot be demonstrated on `a8m.test` because that
  reserved test domain has no public DNS. The staged failure output is honest:
  ownership/MX/SPF/DKIM/DMARC lookups return NXDOMAIN while the local key is
  ready. Use a customer-owned domain for the public end-to-end gate.

Transcripts: `artifacts/dkim-smoke.txt` and
`artifacts/dkim-signing-smoke.txt`; worker and signing output is recorded in
this runbook.

### 6.14 DAV, workspace, MTA-STS, storage and upload scanning

```bash
GO_API=https://130-94-46-22.sslip.io \
DAV_EMAIL=avery.chen@a8m.test \
DAV_PASSWORD="$(cat /opt/a8m/secrets/staging_avery_password)" \
  python3 tools/dav-smoke.py

ALT8MAIL_UI_PASSWORD="$(cat /opt/a8m/secrets/staging_avery_password)" \
  node tools/workspace-ui-smoke.mjs

# MTA policy on the correct Host (mode must be testing/enforce)
curl -ksS -H 'Host: mta-sts.a8m.test' \
  https://127.0.0.1/.well-known/mta-sts.txt
```

Observed on 2026-10-28:

- DAV discovery, event/contact create/read/update/report/delete all pass;
  passwords work for non-MFA accounts and app passwords work for DAV after
  creation and stop working immediately after revocation.
- The workspace UI smoke creates/edits/deletes a calendar event and contact,
  edits/restores the profile title and creates/revokes an app password.
- A 1,100-message test mailbox imported fully with `pending=0` in one sync; a
  follow-up five-message append fetched only those five bodies. See
  `artifacts/large-mailbox-sync-smoke.txt`.
- S3 upload/delete/re-sync does not resurrect the deleted file. EICAR upload is
  rejected with 422 `ATTACHMENT_INFECTED` and a clean file is accepted. See
  `artifacts/s3-delete-smoke.txt` and `artifacts/upload-clamav-smoke.txt`.
- MTA-STS policy mode/max-age, TXT+POLICY records, endpoint output and verifier
  units are recorded in `artifacts/mta-sts-smoke.txt`. Public verification is
  still blocked by the reserved staging domain and missing
  `mta-sts.<domain>` certificate.

### 6.15 Compose 0.7, notifications, identities, forwarding and AI

```bash
ALT8MAIL_UI_PASSWORD="$(cat /opt/a8m/secrets/staging_avery_password)" \
  node tools/compose-notifications-smoke.mjs
```

Observed on 2026-10-29:

- Notification popover/center contained real unread mail and deduplicated
  security events; the former hardcoded Roadmap/sign-in rows were absent.
- HTML draft save stripped `<script>`/`onclick`, preserved allowed formatting,
  persisted `windowMode=closed`, and a reload did not reopen the closed draft.
- Compose From resolved to `avery.chen@a8m.test`, priority rendered as a
  three-way radio group, HTML↔Plain switching preserved text, and drag-drop
  uploaded `compose-smoke.txt` through `/api/attachments`.
- Settings → Compose persisted HTML/wide/center; changing to standard +
  bottom-left opened the next compose at `left≈28,width≈580`; restoring the
  preference returned to wide/center.
- Identity list showed primary/Product/support with verified/read-only states;
  `product@a8m.test` was rejected as an already-owned alias; an external
  `.invalid` identity reached `pending`, had its token confirmed by POST, became
  `verified`, and was deleted.
- Dated forwarding with DD/MM/YYYY input compiled and activated a Sieve rule
  containing `currentdate`, `redirect` and `stop`; disabling removed it.
- App password UI creation with 7-day expiry succeeded, revocation appeared in
  history, and a DB-expired token returned DAV 401 while the active token
  returned 207.
- Admin AI settings accepted provider/endpoint/model/tokens/temperature and
  rejected a `javascript:` endpoint. With a local OpenAI-compatible mock, user
  compose, summary (cached on second call), translation and hover summary all
  returned mock content; the tenant setting was then disabled and the mock
  stopped. See `artifacts/ai-compose-reader-smoke.png` and
  `artifacts/admin-ai-smoke.png`.
- Boot splash was hidden after authenticated reload and no login form flash was
  observed in the browser run. The 0.7 hover summary was removed in 0.8; reader
  AI is now manual-only.

### 6.16 0.8 AI, reading, typography and dock behavior

```bash
ALT8MAIL_UI_PASSWORD="$(cat /opt/a8m/secrets/staging_avery_password)" \
  node tools/ai-reading-compose-smoke.mjs
```

Observed on 2026-10-29:

- Interactive sign-in held "Opening your workspace" for 5,696 ms including the
  fade; the app was not interactable before it cleared.
- Opening a conversation produced no AI card. The split AI button exposed
  **Summarise this email** and **Translate Email**; both only ran after the user
  selected them.
- Reply with AI opened a prefilled `Re:` compose with the latest thread context
  and inserted a generated reply; no message was sent.
- Font family and 24 px size survived the client editor and the server's
  bluemonday allowlist into the saved draft HTML.
- Minimizing changed the dock tab to a white-on-Outlook-blue gradient, applied
  the arrival animation and reported the destination in a toast.
- Settings → Mail & reading switched from 5 grouped rows to 6 individual
  message rows for the same 6-message inbox, persisted `individual`, then
  restored `grouped`.
- Reply-all was separately verified against a temporary CC message: it was
  absent on a no-CC thread and, on the CC thread, opened compose to
  `jordan.lee@a8m.test` with `nora.patel@a8m.test` in Cc.
- App-password creation was verified after signing in through the staging demo
  alias using that alias password, then revoked; the test history row was
  removed.
- Real tenant DeepSeek calls returned a non-empty conversation summary and a
  Reply with AI body. The tenant AI configuration was not changed.

### 6.17 System Control 0.10.0-control (2026-10-29)

System Control runs separately from nginx, the webmail Go service and the
customer admin backend. It listens with TLS on **8808**; `88008` is not a valid
TCP port. The complete install, replication, API and security guide is
[SYSTEM_CONTROL.md](SYSTEM_CONTROL.md).

```bash
curl -sk https://127.0.0.1:8808/healthz
systemctl status alt8mail-control --no-pager
ss -ltnp | grep ':8808 '
ufw status | grep 8808
/opt/alt8mail/bin/alt8mail-control version
/opt/alt8mail/bin/alt8mail-control doctor
```

Observed on 2026-10-29:

- `healthz` returned `ok`, `version=0.10.0-control`; the unit was active and
  enabled; UFW allowed 8808/tcp v4 and v6; diagnostics reported 0 failures and
  0 warnings (PostgreSQL, authenticated Redis PING, `postfix check`, Dovecot
  config, core services, disk, memory, mail-log ingestion and mail resources).
- `tools/control-api-smoke.mjs` passed 21/21 checks, including CSRF rejection,
  52 authenticated read endpoints, TOTP/session revocation, domain
  create/rename/delete, mailbox provisioning and password probe, Sieve, backup
  download/delete, API key/template/send/revoke, webhooks, notifications, nginx
  reload, listener preview, Postfix config roundtrip, UFW add/delete, database
  credential test and mail-event ingestion.
- `tools/control-ui-smoke.mjs` rendered all 32 routes with 0 page errors.
  `tools/control-ui-interactions.mjs` passed 28/28 interaction checks, including
  theme, command palette, modals, listeners, Postfix tabs, TOTP QR and mobile
  navigation. `tools/control-ui-writes.mjs` passed 7/7 alias, cron and API-key
  writes.
- Config sync reran with `status=ok`, `filesChanged=0`, `warnings=0`.
- Certbot dry-run completed in about 9 seconds and did not change the live
  certificate. The twice-daily renewal wrapper was installed at
  `/usr/local/bin/ssl_renewal.sh` with `/etc/cron.d/alt8mail-ssl`.
- Rolling caps accepted the first send and deferred the second with the hourly
  cap reason in the SMTP transcript. Journald ingestion, mbox/Maildir import,
  Postfix-only backup restore, sender-route render/removal and firewall
  rule add/delete were exercised with temporary resources and cleaned up.
- The certificate inventory now discovers panel-generated self-signed
  certificates and prunes rows when their files are removed. A self-signed leaf
  is correctly reported as `selfSigned=true` (a CA-issued leaf remains false).
- Server settings were set to `mail.a8m.test`, webmail URL
  `https://130-94-46-22.sslip.io`, resolver `1.1.1.1:53`, public IPv4
  `130.94.46.22` and mail-from `postmaster@a8m.test`; TLS admin email is
  `postmaster@a8m.test` with a 14-day warning threshold.
- The webmail 0.9 stack and the real DeepSeek tenant AI configuration were left
  running and unchanged.

### 6.18 1,000-user soak and capacity tuning (2026-10-29)

A true 1,000-user soak was prepared with distinct accounts, not a shared login:
1,000 synthetic `soak-20261029-*@a8m.test` users were created through the
real admin user-provisioning path (1,000 created, 0 failed, 0 state-init
failures), all 1,000 mailboxes were provisioned, and forced password rotation
was cleared for the test cohort. A pre-soak database/config backup was written
to `/var/backups/soak-preflight-20261029T073523Z/`.

Capacity changes required before the run:

- Dovecot hibernation is enabled with `imap_hibernate_timeout = 30s`; idle IDLE
  sessions move into one `imap-hibernate` process. `imap-master` must be
  reachable by the internal Dovecot user, otherwise hibernation succeeds but
  unhibernation fails with `net_connect_unix(/run/dovecot/imap-master)
  Permission denied`. The exact config is in
  `deploy/dovecot/99-a8m-soak.conf`.
- IMAP/POP3 process and client limits were raised, and inotify limits were
  increased via `deploy/sysctl/99-alt8mail-mail.conf`; inotify exhaustion at
  UID 5000 otherwise disables mailbox change notifications.
- The webmail poll interval is 300 seconds (`ALT8MAIL_IMAP_POLL_SECONDS=300`)
  because IDLE is the primary new-mail path. Polling every 15 seconds
  un-hibernates all users and defeats the memory saving at 1,000 users.
- The worker initial-check and first-full-sync delays now use a true random
  spread. The previous `UnixNano % poll` calculation gave every worker created
  in the same refresh nearly the same delay, producing a thundering herd.
- nginx `worker_connections` was raised to 4096 with `multi_accept on` and
  `worker_rlimit_nofile 65535`; `net.ipv4.tcp_max_syn_backlog` was raised.
  The current main config is saved at `deploy/nginx/nginx.conf`.
- SSH was switched to key-only root access during the security assessment;
  the drop-in is `deploy/ssh/99-alt8mail-hardening.conf`.

Load profile: 1,000 distinct sessions, 10-minute ramp, 20-minute hold, one
request every 15 seconds per user after login. Each user logs in, fetches the
workspace bootstrap once, then cycles message list, folders, quota, search,
tasks, calendar and preferences. A separate low-concurrency probe polls
webmail health, the admin bootstrap and the control dashboard throughout.
Host metrics are sampled every five seconds. The load driver is
`backend/cmd/soakload`; the host monitor is `tools/soak_monitor.sh`.

Observed result on 2026-10-29:

- 1,000/1,000 distinct logins succeeded; 1,000 sessions were active throughout
  the 20-minute hold.
- 92,278 requests, 92,275 HTTP 200, 0 HTTP 5xx and 3 transport errors
  (0.00325%); p50 15.1 ms, p95 47.6 ms, p99 105.7 ms, max 626 ms.
- 1,022 IMAP connections were held in one `imap-hibernate` process at ~9.9 MB;
  transient IMAP processes peaked at 365 while the one-message-per-mailbox seed woke workers.
- Host load1 averaged 2.23 and peaked at 6.88 during the seed burst; available
  memory averaged 468 MB (minimum 201 MB). PostgreSQL peaked at 39 connections;
  Go RSS peaked at 336 MB. All 202 web/control health probes were 200 and all
  ten monitored services stayed active.
- Plane p95: webmail health 26.8 ms, admin bootstrap 170.8 ms, control dashboard
  365.7 ms and control health 15.6 ms.
- The mid-run 1,000-recipient message seed exposed a critical message-row
  collision (P-12): `mail_messages.id` was derived from Message-ID alone, so
  recipients overwrote one global row. Ids are now scoped by user, folder and
  Message-ID; the focused three-recipient fan-out retest produced three rows
  with three distinct owners. Test accounts and Maildirs were removed; 11 real
  active users remain.

The full report is `docs/PENTEST_REPORT_2026-10-29.md`; raw evidence is under
`artifacts/soak/` and `artifacts/pentest/`.

## 7. Operations

```bash
systemctl status a8m-server
journalctl -u a8m-server -f
systemctl status postfix dovecot rspamd clamav-daemon nginx fail2ban
tail -f /var/log/mail.log
nginx -t && systemctl reload nginx
```

Do not expose 5432/6379/8080. The API is bound to `127.0.0.1:8080`.

### Backups (implemented)

`a8m-backup.timer` runs `/usr/local/sbin/a8m-backup.sh` daily at 02:30
UTC with a randomized delay and 14-day retention:

- PostgreSQL custom-format dump at
  `/var/backups/a8m/a8m-postgres-<stamp>.dump`.
- `/var/vmail`, `/opt/a8m/secrets`, preview data, Dovecot/Postfix/Rspamd/
  nginx/TLS/systemd configuration packed and AES-256 encrypted at
  `/var/backups/a8m/a8m-files-<stamp>.tar.gz.enc`.
- The backup passphrase is `/opt/a8m/secrets/backup_passphrase` (0600).
  **Copy it into an off-server secret manager.** A backup and its passphrase on
  the same lost server is not a backup.
- Verified on 2026-10-28: PostgreSQL restore into an isolated
  `a8m_restore_check` database produced 1 user and 1 tenant; the encrypted
  archive decrypted and listed the expected Maildir/config entries.
- Before production: add off-site object storage, WAL/PITR, retention/legal
  policy, and quarterly restore drills.

### Load and host security baseline

- `ab -n 2000 -c 20` against the Go health endpoint (`/api/health`): 0 failed
  requests, ~4,818 req/s on the 2 vCPU staging host. Earlier reference-preview
  baseline was ~4,846 req/s. This is a health-endpoint throughput smoke, not a
  1,000-user mail load test.
- nmap loopback review found only the intended ports: 22, 25, 80, 443, 587, 993
  995 and 4190.
- Lynis hardening index: 66 (268 tests). Suggested improvements include
  host-level SSH/GRUB/auditd hardening, file-integrity tooling, external log
  shipping, and a kernel reboot. These remain before production.
- Full authenticated load testing, external penetration testing, mail-flow
  stress testing and a 1,000-user soak test are still outstanding.

## 8. Current limitations and next phase

The staging environment is intentionally not marked production-complete.

**Live in Go** (root UI + `/api` + `/system/api`):

- Login/session/CSRF/bootstrap/preferences, per-user feature state in
  PostgreSQL, Argon2id web passwords and a staging-only demo alias.
- Threads/search, read/star/flag, bulk actions and 30-second undo, drafts,
  send/outbox, undo-send, scheduled edit/send-now/cancel and durable worker.
- Real IMAP sync for INBOX/Sent/Drafts/Archive/Junk/Trash with MIME bodies,
  attachment ingest, SPF/DKIM/DMARC header parsing and threading.
- Two-way read/star/flag/move/archive/spam/delete propagation with undo and
  deferred expunge; drafts stored in Dovecot Drafts.
- SMTP submission through Postfix 587 with STARTTLS + Dovecot SASL, Rspamd and
  ClamAV in path; Sent append to Dovecot.
- No seeded mailbox data: `a8m-inituser` builds identity/aliases/directory
  from PostgreSQL and leaves messages/tasks/files/calendar empty; Dovecot is the
  source of mailbox truth.
- Tasks, Files/storage connectors, calendar reads, directory, Sieve/vacation,
  audit/CSV, quota/outbound, attachment upload/download and preferences.
- All settings tabs are server-backed: `users.preferences` for appearance,
  reading, notifications and regional settings; ManageSieve for rules and
  automatic replies; real security tables for TOTP, WebAuthn, recovery,
  sessions and first-time enrollment policy.
- Real RFC 6238 TOTP, hashed single-use recovery codes, trusted devices and
  WebAuthn passkey registration/verification in Go. First-time enrollment is a
  per-user policy (`users.enrollment_required`), disabled for staging users.
- nginx serves the exact Aurora frontend; PostgreSQL/Redis/Dovecot/Postfix/
  Sieve/ManageSieve/Rspamd/ClamAV are installed and tested.
- A separate Go admin backend and UI at `/admin/` provide tenant/user management,
  org units, groups, custom RBAC roles, domains and real DNS verification,
  tenant policy, admin audit, admin TOTP, tenant suspension and system-admin
  management. Tenant MFA/IP policy is enforced at mail login; outbound mail
  policy enforces SMTP access, external-recipient allowlists, blocked attachment
  extensions and size limits.
- Tenant suspension is enforced by the Go login path and by the Postfix/Dovecot
  SQL maps; suspended tenants cannot authenticate mail clients or receive mail.

**Not yet production-complete:**

- Mail, drafts, attachments, outbox items and undo snapshots are normalized and
  server-side full-text search plus Dovecot IDLE are live. Bootstrap still
  returns the mailbox view without pagination, and a very large mailbox needs
  query/window tuning; mail-table sharding/archival is future scale work.
- AI assistant plumbing is live but staging has no real OpenAI/Claude/DeepSeek
  credentials configured; external identity verification also depends on the
  external domain's deliverability and SPF/DKIM alignment when that identity
  sends. Notification read state is per-browser, not synced across devices.
- Admin-console WebAuthn, SSO/IdP federation and step-up authentication.
- Public mail DNS/MX/SPF/DKIM/DMARC/MTA-STS verification requires a
  customer-owned domain and `mta-sts.<domain>` TLS certificate. Provider OAuth
  needs real Google/Dropbox/Microsoft app credentials.
- CalDAV/CardDAV is limited to one default calendar/address book per user and
  does not implement VTODO, collection sharing/ACLs or iTIP scheduling.
- Guest share links are bearer links; named-recipient/email-verified access is
  not built.
- Real 1,000-user soak test, external penetration test, SIEM/alerting, PITR/
  off-site backup and production secret rotation.

The next priorities are paginated/windowed bootstrap for extreme mailboxes,
public domain and MTA-STS validation, live provider OAuth credentials, DAV
client certification and the production security/scale gates.
See `docs/PLATFORM_BUILD_PLAN.md` for phase checkboxes and acceptance criteria.

### 6.19 Webmail 0.9 and System Control 0.10 features (2026-10-29)

Webmail `0.9.0-staging` and System Control `0.10.0-control` were deployed
together. The tenant AI configuration (`openai`, `https://api.deepseek.com/v1`,
`deepseek-flash`) was not changed.

Webmail changes and verification:

- Individual reading view hides the conversation reply/forward footer and the
  expand-all/collapse-all controls; grouped view keeps them. Verified by
  `tools/webmail-0.9-smoke.mjs`.
- Pin/unpin is persisted per message in `mail_messages.pinned`. The UI exposes
  pin controls in the reader toolbar and list row, shows a pinned marker, and
  survives reload.
- Minimized compose state is saved as `windowMode=minimized` before the draft
  flush, flushed before logout, and restored after reload and in a second,
  clean browser profile representing another computer.
- Hash routes preserve `#/Sent`, `#/Inbox/<id>`, `#/Settings` and
  `#/Settings/rules` across reload. The canonical settings anchor is
  `#/Settings`; subsection anchors use `#/Settings/<tab>`.
- The About page is Alt8Mail-branded, removes the old concept section, describes
  AI assistance and advanced security, and shows live IMAP/POP3/SMTP host,
  port, security and username details plus setup instructions.
- Verification: `tools/webmail-0.9-smoke.mjs` passed **39 checks** with 0 page
  errors, including a second browser profile; existing `go-ui-smoke.mjs` and
  `workspace-ui-smoke.mjs` passed.

System Control changes and verification:

- Mailboxes now default to 100 rows, offer 100/150/250/500 row sizes, and sort
  by address, name, status, quota usage, storage usage, last sign-in or created
  date, with status filtering and page/total metadata. The API accepts page,
  pageSize, sort and dir parameters and returns aggregate counts.
- TLS certificate details resolve correctly by route name, with a disk-scan
  fallback for stale rows. Certificates can be imported with certificate/key
  pair and expiry validation. A saved certificate can be assigned to nginx,
  Postfix, Dovecot or a combination; each target is validated, reloaded and
  rolled back on failure. Postfix key access uses ACLs plus directory search
  permission, never world-readable keys; a certbot deploy hook reapplies ACLs.
- Infrastructure → Software updates lists real apt candidates, supports
  individual/bulk update jobs. The job runs in a transient systemd unit outside
  the control service's read-only sandbox and streams output into a terminal
  panel; a no-op bulk job was used to verify job creation, output, exit status
  and cleanup.
- Infrastructure → Terminal provides an authenticated loopback SSH shell with
  password or key authentication, detects the live sshd port with `sshd -T`,
  uses one-time tickets and an origin-checked WebSocket, and renders with
  vendored xterm.js. Password auth is loopback-only; failed SSH passwords now
  return 422 and no longer expire the panel session.
- Dashboard Host health now includes Deliverability Health for Postfix sending
  IPs: pulsing green clear, pulsing red listed, amber unverified. Resolver
  policy replies such as Spamhaus `127.255.255.254` are not counted as
  listings; configure a permitted resolver for definitive Spamhaus results.
- Verification: `tools/control-0.10-smoke.mjs` passed **43 checks**;
  `tools/control-terminal-smoke.mjs` passed **18 checks**; the live TLS swap
  test passed **14 checks** and restored the original nginx/Postfix paths;
  `tools/control-ui-smoke.mjs` rendered **32 routes** with 0 page errors; the
  interaction suite passed 28/28. Backend unit tests include apt parser,
  package-name validation, TLS PEM/key matching, nginx directive replacement,
  WebSocket framing, blocklist sentinel handling and mailbox sort reversal.

Operational notes:

- The control listener is intentionally HTTP/1.1 only because the interactive
  terminal uses a WebSocket; TLS 1.2/1.3 constraints are unchanged.
- `/metrics` remains unauthenticated and port 8808 remains root-equivalent;
  restrict it at the network edge.
- The System Control initial password file and all server secrets remain
  root-only and were not printed in deployments or test output.

