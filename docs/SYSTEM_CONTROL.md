# Alt8Mail System Control

System Control is the separate root-level operations panel for the Alt8Mail
Mail staging host. It is not the webmail frontend and it is not the customer /
tenant admin console. It manages the actual mail stack: PostgreSQL, Redis,
Postfix, Dovecot, Rspamd, nginx, certificates, firewall, systemd, logs, backups,
imports and the control plane's own users and API keys.

| Item | Value |
| --- | --- |
| Release | `0.10.0-control` |
| URL | `https://130.94.46.22:8808` |
| Listener | `0.0.0.0:8808` (TLS, self-signed bootstrap certificate) |
| Public URL setting | `CONTROL_PUBLIC_URL=https://130.94.46.22:8808` |
| Initial superadmin | `sysadmin@alt8mail.local` |
| Initial password file | `/root/alt8mail-control-initial-password` (root-only, mode 0600) |
| Service | `alt8mail-control.service` (enabled, active, `Restart=on-failure`) |
| Database schema | `control` in the existing `a8m` database |
| Database role | `alt8mail_control` (separate from `a8m_app` and `a8m_mail`) |
| Health | `GET /healthz` (cheap, unauthenticated) |
| Prometheus text | `GET /metrics` (unauthenticated; restrict at the firewall) |
| Stack detail | `GET /healthz/stacks` (authenticated) |
| Realtime | `GET /events` (authenticated SSE) |

The initial password is deliberately not reproduced in this repository or in
any command output. Read it on the server:

```bash
ssh -i ~/.ssh/a8m_deploy_ed25519 root@130.94.46.22 \
  'cat /root/alt8mail-control-initial-password'
```

Sign in, then change it from **My security → Change password** and enable TOTP
before handing the panel to another operator. Delete the bootstrap file after
the first credential rotation.

## Why the URL uses port 8808, not 88008

`88008` is outside the TCP/UDP port range. Ports are 16-bit unsigned integers:
0–65535. Go rejects `listen tcp: address 88008: invalid port`, curl reports
`Bad port`, and UFW cannot open the rule. The deployed panel uses the nearest
valid high port, **8808**. No software or firewall change can make 88008 a
valid TCP port; a URL such as `https://host:88008` is not reachable by design.

The URL must therefore be opened as:

```text
https://130.94.46.22:8808
```

The bootstrap certificate is self-signed, so the browser will show a warning
until an operator replaces `/etc/ssl/alt8mail/control.crt` and `.key` with a
CA-issued certificate for the chosen panel hostname and restarts the service.

## Separation from the other applications

| Application | URL / listener | Purpose | Credential store |
| --- | --- | --- | --- |
| Webmail | `https://130-94-46-22.sslip.io` via nginx → Go `127.0.0.1:8080` | End-user mail, calendar, contacts, tasks, files | `a8m` web sessions; Dovecot mail passwords |
| Customer admin | `https://130-94-46-22.sslip.io/admin/` → Go `127.0.0.1:8081` | Tenants, org units, policy, customer administration | `a8m-admin-server` sessions and audit |
| **System Control** | **`https://130.94.46.22:8808` direct** | **Host, mail stack, certificates, network, operations** | **`control` schema, separate role and secrets** |

System Control does not accept webmail or customer-admin sessions. It has its
own login, session cookie, CSRF secret, session table, TOTP enrolment, roles,
API keys and audit log. Keep the three planes separate during upgrades and
incident response.

## Topology

```text
Internet / operator VPN
  │
  ├── 443  nginx ── webmail + customer admin + mail-stack APIs
  │
  └── 8808 alt8mail-control (TLS, root service)
             ├── control schema ── PostgreSQL 15 (127.0.0.1:5432)
             ├── Postfix ── policy daemon 127.0.0.1:10040
             │              alias lookups via public.postfix_alias_lookup(text)
             ├── Dovecot ── SQL auth, Maildir /var/vmail, doveadm, Sieve
             ├── Rspamd ── /var/lib/rspamd/dkim keys + selectors.map
             ├── systemd ── service inventory, start/stop/enable/disable
             ├── certbot ── webroot /var/www/certbot, renewal wrapper
             ├── /var/log/mail.log + journald ingestion
             └── /var/backups/alt8mail (archives and imports)
```

Internal workers started by the service:

- mail-event ingestion from `mail.log` or journald;
- host metrics sampling and SSE broadcast;
- service health monitoring and notification dispatch;
- DNS-blocklist checks and certificate-expiry monitoring;
- log retention pruning and package-update checks;
- one-at-a-time mail import worker;
- Postfix policy daemon for per-domain rolling send caps.

The service runs as root because it manages systemd, Postfix, Dovecot, UFW,
certificates and Maildirs. The unit still applies `ProtectSystem=full`,
`ProtectHome=true`, `PrivateTmp=true`, kernel/control-group protections, and a
narrow `ReadWritePaths` allowlist. Treat access to port 8808 as root-equivalent.

## Feature surface

### Dashboard and diagnostics

- Mail-event traffic KPIs and 7/30-day volume chart.
- CPU, memory, disk, load, uptime and package-update summary with sparklines.
- Compact service-health fragment and full services page.
- Diagnostics dialog with PostgreSQL, Redis, Postfix, Dovecot, services, disk,
  memory, log-ingestion and mail-resource checks.
- `/healthz` for load balancers; `/healthz/stacks` for authenticated stack
  detail; `/metrics` for a Prometheus-compatible scrape; `/events` for SSE.
- Config-sync status banner, drift detail and run history.
- A 30-route command palette with `Ctrl/Cmd+K`.

### Domains and DNS

- Create, rename and delete domains with previews and typed confirmation.
- Transactional SQL rewriting of users, aliases, mailboxes and DKIM state when
  a domain is renamed.
- Primary-domain selection and postmaster mailbox provisioning.
- DKIM key generation through Rspamd/OpenDKIM-compatible selectors.
- DNS plan (MX, SPF, DKIM, DMARC, autodiscover, MTA-STS), live resolver checks,
  copy and zone-file export.
- PTR/provider action notes when the reverse zone is outside this host.
- Per-domain rolling outbound caps with current usage from PostgreSQL.
- Domain detail exposes alias, identity and group counts.

### Mailboxes, users, aliases and Sieve

- Paginated mailbox inventory: 100 rows by default with 100/150/250/500 row
  pages, search, all/enabled/suspended status filters, and sorting by address,
  name, status, quota use, storage use, last sign-in or creation time. The API
  also reports total/pages and aggregate counts so the UI never labels a page
  as the whole tenant.
- Create mailbox, show one-time credentials and provision the Dovecot Maildir.
- Password reset with an alternate-email SMTP delivery and a `doveadm auth
  test` login probe.
- Up to 365 days of per-mailbox history from ingested mail events.
- Sieve rules with conditions/actions, preview, non-clobbering deployment and
  automatic target-folder creation.
- Aliases, forwarders, catch-alls and distribution-list expansion, all merged
  into the Postfix alias lookup without replacing customer-admin aliases.

### TLS

- Certbot issue and renewal with the nginx webroot challenge.
- Dry-run renewal with `--no-random-sleep-on-renew` so the request finishes
  quickly and deterministically.
- Certificate inventory: subject, issuer, SANs, not-before/not-after, days
  left, key presence and self-signed classification.
- Self-signed certificate generator for internal hostnames, stored under
  `/etc/ssl/alt8mail` and included in inventory.
- Import a CA-issued certificate chain and matching private key (RSA, ECDSA or
  PKCS#8 PEM, optional legacy passphrase). The pair, expiry and key type are
  validated before writing; the key is normalized and stored root-only under
  `/etc/ssl/alt8mail/imported`.
- Assign a saved certificate to nginx, Postfix, Dovecot or a combination. The
  panel updates the real paths, validates with `nginx -t`, `postfix check` or
  `doveconf -n`, reloads the affected service, and rolls back prior targets if
  a later target fails. Postfix key access is granted with ACLs and directory
  search permission, never world-readable permissions; a certbot deploy hook
  re-applies the ACL after renewal.
- Every detail record resolves by certificate name; stale database rows fall
  back to a fresh disk scan rather than failing the detail dialog.
- Stale inventory rows are pruned when a certificate file disappears.
- SSL admin email, warning threshold, notification test mail and a twice-daily
  renewal wrapper (`/usr/local/bin/ssl_renewal.sh`,
  `/etc/cron.d/alt8mail-ssl`).

### Delivery and reputation

- SMTP test-send with full transcript and test history.
- Postfix queue list, detail, flush, single delete and delete-all.
- Smarthost relay with mode-0600 secret handling and a test send.
- Outbound IP registry and per-sender routing rendered into
  `sender_transport`/`master.cf`, with removal support.
- Deliverability dashboard from ingested events (sent, delivered, bounced,
  deferred) by domain and sender.
- DNSBL checks, blocklist scheduling and suppression maps in both directions.
- Dashboard **Deliverability Health** shows the sending IPs detected from the
  Postfix registry/bind address/public IP, with a pulsing green clear state, a
  pulsing red listed state, and an amber unverified state when a zone declines
  the query. Resolver-policy replies such as Spamhaus `127.255.255.254` are not
  treated as listings.
- Postfix policy daemon reports the real rolling-cap reason on a `450` deferral.
- Spamhaus and some other zones reject queries sent through public resolvers.
  Configure `CONTROL_DNS_RESOLVER` to a resolver permitted by the zone operator
  before treating **Unverified** as a clean result.

### Ports, services and Postfix

- Inventory and edit of SMTP, submission, SMTPS, IMAP, IMAPS, POP3 and POP3S
  listeners with validation and a preview diff.
- Postfix listener editor only manages `smtp`, `submission` and `smtps`; it
  never adds IMAP/POP3 services to `master.cf`.
- Core stack ports display with live ownership/state.
- systemd start, stop, restart, enable, disable and status for the monitored
  stack.
- Postfix `main.cf`/`master.cf` editor with `postfix check`, hash-map rebuild,
  reload and restart. Managed files are backed up before replacement.
- Listener-save rollback if Postfix reload fails.

### Software updates and terminal

- **Infrastructure → Software updates** lists real `apt list --upgradable`
  candidates, highlights security updates, shows reboot-required state, and
  supports individual or bulk updates. Updates are restricted to packages in
  the current available list, run one job at a time in a transient systemd unit
  outside the control service's read-only filesystem sandbox, stream `apt-get`
  output into a dark terminal panel, and record start/finish audit events.
- **Infrastructure → Terminal** opens a real SSH shell to loopback using a
  system account and password or private key. The port is discovered from
  `sshd -T` (with config and listening-socket fallbacks), never assumed to be
  22. It requires `server.manage`, uses a one-time two-minute ticket and an
  origin-checked WebSocket, limits concurrent sessions, renders with a vendored
  xterm.js, and audits the connection without storing credentials. Password
  authentication is allowed only from loopback; external SSH stays key-only.
  Failed SSH credentials return 422 and do not expire the panel session.
  Host-key verification is skipped only for `127.0.0.1`.

### Logs and queue

- Ingestion from `/var/log/mail.log` or journald (Postfix, Dovecot, Rspamd).
- Search, source/status/direction filters, live tail via SSE and event detail.
- Queue detail from `postqueue`/`postcat`.
- Per-user history from mail events.
- Retention prune (365 days by default), with configurable source and retention.

### Import, backups and scheduled tasks

- IMAP, mbox and Maildir import; folders, flags, read state and sent dates are
  preserved where the source provides them.
- One background import at a time, with progress, log, stop, retry and remove.
- Import staging under `/var/vmail/.control-imports/<job>` with vmail ownership
  so `doveadm import` can read the source.
- `.tar.gz` backups under `/var/backups/alt8mail`, with selectable contents,
  download, delete and restore confirmation.
- Cron jobs in `/etc/cron.d/alt8mail` that invoke
  `alt8mail-control cron run <id>`; five-field validation, run-as user, stdin,
  date range, enable/disable, run-now, run log and run history.

### Server, network, firewall and database

- Hostname, mail hostname, webmail URL, DNS resolver, public IPv4/IPv6 and
  default mail-from settings, reconciled by config sync where applicable.
- Time zone and forced NTP step, with live synchronization state.
- Interface/address inventory, runtime address add/remove and connectivity
  tests.
- UFW rule inventory with add/delete and firewall state control.
- PostgreSQL status, database size, activity, index/table counts, role and
  database provisioning, connection test, background migration progress and
  database restart.

### Access, API, webhooks and notifications

- Panel roles `superadmin`, `admin`, `operator`, `readonly`; custom groups with
  a 35-permission matrix.
- TOTP enrolment (QR/manual secret), recovery-code display, session TTL,
  session revocation, last-active tracking.
- Five failed passwords in 15 minutes lock the account for 15 minutes; TOTP
  challenges allow five code attempts.
- CSRF validation on every mutating browser request; full audit log with CSV
  export.
- API keys (`a8m_…`) stored only as SHA-256 hashes, scopes `read`, `write` and
  `admin`, optional expiry and immediate revocation.
- Public `/api/v1` for suppressions, message sending, message history and
  templates.
- HMAC-SHA256 webhooks with `X-Alt8Mail-Event` and `X-Alt8Mail-Signature`, plus
  delivery history.
- Per-operator Slack, Discord, Telegram, generic webhook and email handles,
  event catalogue and delivery log.

## Install / replicate

### 1. Build

From the repository's `backend` directory:

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=0.10.0-control" \
  -o ../build/alt8mail-control ./cmd/control
```

The result is a static Linux amd64 binary. Deployment assets live in
`build/control-deploy/`:

```text
install.sh
alt8mail-control
migrations/0015_control_core.sql
migrations/0016_control_mail.sql
migrations/0017_mail_pin.sql
migrations/0018_control_tls_assignments.sql
migrations/0019_blocklist_sentinel.sql
```

### 2. Copy and run the installer

From the operator workstation:

```bash
scp -i ~/.ssh/a8m_deploy_ed25519 -r build/control-deploy \
  root@130.94.46.22:/tmp/
ssh -i ~/.ssh/a8m_deploy_ed25519 root@130.94.46.22 \
  'bash /tmp/control-deploy/install.sh'
```

The installer is idempotent for files and schema, but it generates a fresh
database password and fresh session/CSRF/secret keys on every run. Re-running
it invalidates existing panel sessions. For a binary-only upgrade, follow
step 5 instead.

The installer:

1. installs `/opt/alt8mail/bin/alt8mail-control`;
2. creates/resets the `alt8mail_control` PostgreSQL role with a random
   password;
3. applies migrations 0015 and 0016 to database `a8m`;
4. generates the self-signed control certificate if absent;
5. writes `/opt/alt8mail/control.env` mode 0600;
6. installs `alt8mail-control.service`, enables and restarts it;
7. creates `sysadmin@alt8mail.local` only if no such account exists, saving its
   random initial password to `/root/alt8mail-control-initial-password`;
8. opens `8808/tcp` in UFW;
9. prints health, version and certificate summary without printing secrets.

### 3. Environment reference

| Variable | Default | Purpose |
| --- | --- | --- |
| `CONTROL_ENV` | `staging` | Environment label shown in the UI/API |
| `CONTROL_LISTEN` | `0.0.0.0:8808` | TLS listener |
| `CONTROL_DATABASE_URL` | required | PostgreSQL DSN for `alt8mail_control` |
| `CONTROL_SESSION_SECRET` | required, ≥32 chars | Session signing material |
| `CONTROL_CSRF_SECRET` | required, ≥32 chars | CSRF-signing key |
| `CONTROL_SECRET_KEY` | required, ≥32 chars | AES-GCM key for sealed settings |
| `CONTROL_PUBLIC_URL` | `https://130.94.46.22:8808` | Links in notifications and UI |
| `CONTROL_MAIL_HOSTNAME` | `mail.a8m.test` | SMTP greeting/HELO identity |
| `CONTROL_WEBMAIL_URL` | empty | Webmail link in DNS plans/notifications |
| `CONTROL_DNS_RESOLVER` | `1.1.1.1:53` | Resolver for DNS/DNSBL checks |
| `CONTROL_MAIL_LOG` | `/var/log/mail.log` | Mail-event source |
| `CONTROL_VMAIL_ROOT` | `/var/vmail` | Maildir root |
| `CONTROL_DKIM_DIR` | `/var/lib/rspamd/dkim` | DKIM keys and selector map |
| `CONTROL_POSTFIX_DIR` | `/etc/postfix` | Postfix configuration |
| `CONTROL_DOVECOT_DIR` | `/etc/dovecot` | Dovecot configuration |
| `CONTROL_SSL_DIR` | `/etc/ssl/alt8mail` | Panel and self-signed certificates |
| `CONTROL_TLS_CERT` | `/etc/ssl/alt8mail/control.crt` | Panel certificate |
| `CONTROL_TLS_KEY` | `/etc/ssl/alt8mail/control.key` | Panel private key |
| `CONTROL_BACKUP_DIR` | `/var/backups/alt8mail` | Backup/import archives |
| `CONTROL_CRON_FILE` | `/etc/cron.d/alt8mail` | Managed panel cron file |
| `CONTROL_CERTBOT_DIR` | `/etc/letsencrypt/live` | Certificate inventory source |
| `CONTROL_CERTBOT_WEBROOT` | `/var/www/certbot` | Certbot webroot challenge |
| `CONTROL_SMTP_ADDR` | `127.0.0.1:25` | Local SMTP test path |
| `CONTROL_POLICY_LISTEN` | `127.0.0.1:10040` | Postfix policy daemon |
| `CONTROL_REDIS_PASSWORD_FILE` | `/opt/a8m/secrets/redis_password` | Redis auth file; contents are never logged |
| `CONTROL_INGEST_INTERVAL` | `8s` | Mail-log polling interval |
| `CONTROL_HEALTH_INTERVAL` | `15s` | Service-monitor interval |
| `CONTROL_METRICS_INTERVAL` | `5s` | Host metric sampling |
| `CONTROL_RETENTION_DAYS` | `365` | Default log retention (1–3650) |
| `CONTROL_TRUSTED_PROXY` | `false` | Trust `X-Forwarded-For` from a local proxy |

The environment file is parsed by systemd, not by a shell. Do not put spaces or
unquoted `#` characters in values. Keep the file root-owned and mode 0600.

### 4. Systemd

```ini
[Unit]
Description=Alt8Mail System Control
After=network-online.target postgresql.service

[Service]
Type=simple
User=root
WorkingDirectory=/opt/alt8mail
EnvironmentFile=/opt/alt8mail/control.env
ExecStart=/opt/alt8mail/bin/alt8mail-control serve
Restart=on-failure
RestartSec=3
ProtectSystem=full
ReadWritePaths=/etc/postfix /etc/dovecot /etc/nginx /etc/letsencrypt /etc/ssl/alt8mail /etc/cron.d /etc/ufw /etc/hostname /etc/hosts /etc/localtime /etc/timezone /usr/local/bin /var/backups/alt8mail /var/vmail /var/lib/rspamd/dkim /var/lib/letsencrypt /var/log /var/spool/postfix
PrivateTmp=true
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true

[Install]
WantedBy=multi-user.target
```

Reload after editing:

```bash
systemctl daemon-reload
systemctl restart alt8mail-control
systemctl status alt8mail-control --no-pager
```

### 5. Binary-only upgrade

```bash
systemctl stop alt8mail-control
install -m 0755 /tmp/alt8mail-control /opt/alt8mail/bin/alt8mail-control
systemctl start alt8mail-control
curl -sk https://127.0.0.1:8808/healthz
```

Run any new migrations explicitly before starting the new binary if a release
adds one. Migrations in this release are `0015` and `0016`.

### 6. Verify the first install

```bash
curl -sk https://127.0.0.1:8808/healthz
systemctl is-active alt8mail-control
ss -ltnp | grep ':8808 '
ufw status | grep 8808
openssl x509 -in /etc/ssl/alt8mail/control.crt -noout -subject -dates \
  -ext subjectAltName
/opt/alt8mail/bin/alt8mail-control version
/opt/alt8mail/bin/alt8mail-control doctor
```

Open `https://130.94.46.22:8808`, sign in with `sysadmin@alt8mail.local`, read
the initial password from the root-only file, then:

1. change the password;
2. enable TOTP;
3. create named operator accounts instead of sharing superadmin;
4. set **Server & network** and **TLS** settings;
5. install the SSL renewal wrapper;
6. run **Config sync** and confirm zero files changed on the second run;
7. enable notifications and test one handle;
8. restrict UFW source addresses to the operator network.

## Stack integration

### Postfix

- Aliases are resolved through `public.postfix_alias_targets` and
  `public.postfix_alias_lookup(text)`. The view merges customer-admin aliases
  and groups with control aliases and catch-alls, so neither plane overwrites
  the other.
- The policy daemon listens on `127.0.0.1:10040`. Postfix calls it from
  `smtpd_recipient_restrictions` with
  `check_policy_service inet:127.0.0.1:10040`; the default action is `DUNNO`,
  so a control-plane outage does not stop mail.
- Relay transport, sender-dependent transport, TLS maps and policy config are
  rebuilt by config sync from the control database.
- The panel never edits generated map files by hand; change settings in the UI
  and run config sync.

### Dovecot

- Mailboxes are SQL users in `public.users`; Maildirs are under
  `/var/vmail/<domain>/<local>/Maildir`.
- Provisioning creates the Maildir with the `vmail` UID/GID 5000 and a
  Dovecot-compatible password hash. Web and mail passwords remain separate.
- Password resets use `doveadm pw` plus `doveadm auth test`; a failed probe is
  reported rather than silently accepted.
- Sieve deployment uses `doveadm sieve`; target folders are created with
  `doveadm mailbox create` before the script is activated. Existing custom
  scripts are preserved.
- Imports run through `doveadm import` and inherit the vmail read permission on
  their staging directory.

### TLS and certbot

- Certbot uses HTTP-01 webroot authentication against `/var/www/certbot`.
- The panel does not use the certbot nginx plugin because it is not installed.
- Renewal runs twice daily at 03:17 and 15:17 via
  `/etc/cron.d/alt8mail-ssl`; the wrapper reloads nginx, Postfix and Dovecot
  after a successful renewal.
- The control panel's own TLS certificate is separate: replacing
  `/etc/ssl/alt8mail/control.crt` and `.key` with a CA-issued pair and
  restarting the service removes the browser warning.
- Do not point `/etc/ssl/alt8mail/control.{crt,key}` at a certbot `live/`
  symlink directory without granting the certbot renewal path and reloads; copy
  or mount the pair explicitly and automate the copy.

### nginx

nginx serves the webmail and customer admin on 443. System Control is not
proxied through nginx; it terminates TLS on 8808 directly. This is deliberate:
a fault in nginx remains observable from a plane that does not depend on nginx.

## Configuration sync

Config sync reconciles control-database resources into live mail files. It is
idempotent: a second run on an unchanged system reports `filesChanged: 0`.

It manages:

- domains and DKIM selector mapping;
- missing Maildirs;
- Postfix alias maps/views;
- suppression maps and restriction classes;
- relay and sender-dependent transport;
- the policy-daemon Postfix stanza;
- SASL password maps when a relay is configured.

Run it from **Config sync** or:

```bash
curl -sk -b /tmp/control.cookies -H "X-Alt8Mail-CSRF: $CSRF" \
  -H 'Content-Type: application/json' -X POST \
  https://127.0.0.1:8808/control/api/config-sync/run -d '{}'
```

Use the UI's **Inspect server Maildirs** preview before adopting unmanaged
mailboxes. Importing is an explicit action.

## Backups and retention

- Panel-created archives live under `/var/backups/alt8mail`, not in the
  database.
- Backup contents can include database dumps, Postfix, Dovecot, nginx, DKIM,
  cron, SSL configuration and selected mail data.
- Restores require typing a confirmation and are limited to a selected set of
  contents; a Postfix-only restore is non-destructive and tested.
- Copy archives off-host on a schedule. The panel does not yet ship an
  off-site replication target.
- Mail-event retention defaults to 365 days and can be pruned from **Mail
  logs**. Audit events are retained separately for accountability.

## API

### Browser session API

1. `GET /control/api/session` returns an anonymous CSRF token and sets the
   session/CSRF cookies.
2. `POST /control/api/login` with `{email,password}` and `X-Alt8Mail-CSRF`.
3. If `totpRequired` is true, post `{challengeId,code}` to
   `/control/api/login/totp`.
4. Every subsequent mutation must carry `X-Alt8Mail-CSRF` plus the session
   cookie.

Errors use an HTTP status and a JSON body shaped like:

```json
{"error":{"code":"FORBIDDEN","message":"Permission denied"}}
```

Sessions are opaque server-side records. Revoke one or all others from **My
security**; an administrator can revoke any panel session from **Access**.

### Public API v1

Create an API key in **API & webhooks → API keys**. The raw key is shown once:

```bash
curl -H 'Authorization: Bearer a8m_…' https://130.94.46.22:8808/api/v1
```

Scopes:

| Scope | Grants |
| --- | --- |
| `read` | Read suppressions, messages and templates |
| `write` | Read + send messages and manage templates |
| `admin` | Read + write + create/delete suppressions |

Keys are stored as SHA-256 hashes, can expire, and stop working immediately on
revocation. The public API is rate-limited by the same panel middleware and does
not accept browser cookies.

### Webhooks

Webhook deliveries are `POST` requests with a JSON body:

```json
{"event":"ssl.failed","timestamp":"2026-10-29T10:00:00Z","data":{}}
```

Headers:

```text
X-Alt8Mail-Event: ssl.failed
X-Alt8Mail-Signature: <hex HMAC-SHA256 of the exact request body>
```

Compute the HMAC with the webhook secret over the raw bytes, compare with a
constant-time function, and reject stale timestamps according to your replay
policy. Redirects are not followed by the control plane, and delivery results
are recorded for review.

## Security model

System Control is a root-equivalent management surface. Protect it accordingly.

- Separate database role with grants only to `control` and the required
  `public` mail views/tables. It is not a superuser and cannot read webmail
  sessions by default.
- Argon2id panel passwords, opaque sessions, HttpOnly/SameSite cookies and
  CSRF double-submit validation.
- Five failed sign-ins in 15 minutes locks the account for 15 minutes. Unknown
  accounts still pay a password-hash cost to reduce enumeration oracles.
- TOTP secret material is AES-GCM sealed with `CONTROL_SECRET_KEY`; disable
  requires the current password and an audit record.
- Role and group permissions are enforced server-side on every route, not only
  hidden in the UI. At least one active superadmin must remain.
- API keys are random, shown once, hashed at rest, scoped and revocable.
- Webhook secrets are sealed at rest; signatures are HMAC-SHA256.
- Audit entries include actor, action, target, description, IP, user agent and
  request metadata where applicable.
- The service stores its environment and DB credential in mode-0600 files.
  Never cat `control.env` or the Dovecot SQL configuration into a terminal
  transcript; redact password fragments before sharing diagnostics.
- `/metrics` and `/healthz` are unauthenticated by design. `/metrics` exposes
  counts and host gauges, not message contents or secrets. Restrict 8808 at the
  network edge and do not publish it to the public internet without a VPN or IP
  allowlist.
- The panel intentionally writes Postfix/Dovecot/systemd configuration as root.
  A compromised superadmin is equivalent to root. Use MFA, named accounts,
  short session TTLs where practical, and prompt revocation.
- The TLS bootstrap certificate is self-signed. Browsers warn; do not teach
  operators to click through warnings indefinitely. Install a CA certificate.

## Verification performed for this release

On 2026-10-29 against `https://130.94.46.22:8808`:

- `tools/control-api-smoke.mjs`: 21/21 checks, including auth/health/metrics,
  CSRF rejection, 52 authenticated read endpoints, access/TOTP/session
  revocation, aliases, suppressions, cron, backup download/delete, webhooks,
  notifications, API key send/history/revoke, TLS test mail, nginx reload, port
  preview, Postfix config roundtrip, UFW add/delete, DB credential test, domain
  create/rename, mailbox provision, password probe, Sieve, domain delete, mail
  event ingestion and 0/0 diagnostics.
- `tools/control-ui-smoke.mjs`: 32/32 routes, 0 page errors, including the new
  Software updates and Terminal routes.
- `tools/control-ui-interactions.mjs`: 28/28 checks across theme, command
  palette, modals, ports preview, Postfix editor, TOTP QR and mobile layout.
- `tools/control-ui-writes.mjs`: 7/7 alias, cron and API-key UI writes.
- `tools/control-0.10-smoke.mjs`: 43/43 checks covering mailbox pagination and
  sorting, TLS inventory detail, import/duplicate validation, web/Postfix/
  Dovecot assignment, software-update listing/check/validation, a no-op bulk
  apt job through the transient unit, and dashboard reputation rendering.
- `tools/control-terminal-smoke.mjs`: 18/18 checks for live SSH port detection,
  failed-password session survival, successful password and key authentication,
  WebSocket command output and the xterm page.
- `tools/control-tls-live-swap.mjs`: 14/14 checks that temporarily swap nginx
  and Postfix to an imported certificate, verify the symlink and mail key ACL,
  then restore the original paths. Temporary certificate material was removed.
- Config sync: `filesChanged 0`, `warnings 0` on a clean rerun.
- Certbot dry-run: succeeds in roughly 9 seconds without changing the cert.
- Rolling caps: second send deferred with the hourly-cap reason in the SMTP
  transcript.
- Journald ingestion, mbox/Maildir folder import, backup restore,
  outbound-routing render/removal, firewall rules, self-signed inventory and
  stale-certificate pruning were exercised with temporary resources and
  cleaned up afterwards.

## Known limitations

- Port 88008 cannot be used; the deployed port is 8808.
- The panel's bootstrap TLS certificate is self-signed until replaced.
- The service runs as root; there is no fine-grained Linux capability or
  per-action privilege broker yet. The new SSH terminal is deliberately
  root-equivalent and must be restricted to trusted `server.manage` operators.
- Package updates do not automatically restart services or reboot the host; the
  UI shows the reboot flag and leaves that decision to the operator.
- Imported certificate keys are stored root-only; mail-service assignment
  applies ACLs and directory search permission. Review ACLs if the host is
  shared with other security domains.
- Spamhaus and similar zones may return resolver-policy replies through a
  public resolver; the dashboard marks those zones **Unverified** rather than
  clear or listed.
- `/metrics` is not authenticated.
- The control plane targets the single-host staging topology; multi-host
  replication needs a shared database, distributed secret store and per-host
  agents.
- Off-site backup replication, SIEM forwarding, external identity providers and
  hardware-backed admin MFA are not included.
- Real 1,000-user soak and third-party penetration testing still gate
  production, per [`PHASE_REVIEW.md`](PHASE_REVIEW.md).
