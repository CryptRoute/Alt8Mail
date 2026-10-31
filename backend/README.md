# Alt8Mail system backend (Go)

This is the Go system backend for the Alt8Mail platform. The existing Aurora
frontend in `../public/` remains the user interface; nginx serves it and proxies
`/api/` (and the `/system/api/` namespace) to this service.

## Current scope

- PostgreSQL migrations for tenants, users, aliases, mailboxes, sessions,
  normalized messages/attachments/outbox/undo, tasks/files/calendar/contacts,
  guest file shares, storage accounts and OAuth state, workspace audit, app
  passwords, sending identities, tenant AI settings/cache and Sieve profiles.
  Mail, workspace, auth, identity and AI state are table-backed; `app_states`
  retains only small per-user presentation state (people, folder metadata,
  pending expunges, Sieve/forwarding profile and feature audit).
- Argon2id web password hashing; Dovecot-compatible mailbox password hashes are
  stored separately in `users.mail_password_hash`.
- Opaque PostgreSQL sessions, Secure/HttpOnly cookies, CSRF header validation,
  login throttling and structured audit events.
- Full Aurora feature API: bootstrap, messages/threads/search, read/star/flag,
  bulk actions + undo, server drafts, send/outbox, scheduled edit/send-now/
  cancel, normalized Tasks/Files/Calendar/Contacts, storage connectors,
  attachment upload/content, public guest file shares, Sieve/vacation,
  audit/CSV, quota, outbound caps and preferences.
- Normalized workspace writes: task CRUD with assignee/directory validation,
  calendar event CRUD with attendees, personal contact CRUD and directory
  search. Guest share pages support expiring view/edit links and file
  replacement. The legacy tasks/files/calendar/storage JSONB keys are removed
  by `0007_workspace.sql`.
- CalDAV and CardDAV discovery (`/.well-known/caldav`, `/.well-known/carddav`),
  PROPFIND/REPORT/GET/PUT/DELETE, iCalendar/vCard mapping, ETags and sync
  tokens. Basic auth accepts an app password (required when MFA is enabled) or
  the account password when no second factor is enrolled.
- Expiring and revocable app passwords under `/api/security/app-passwords`,
  stored as SHA-256 hashes of 192-bit random tokens and shown once at creation.
  Expiry is enforced on every DAV request; revocation is soft so the owner keeps
  an active/expired/revoked security history.
- Optional ClamAV scanning of every web attachment upload and guest-share
  replacement through clamd INSTREAM. `ALT8MAIL_CLAMAV_ADDR` enables it
  (unix socket or `host:port`); scanner failure fails closed so malware cannot
  bypass detection by making clamd unavailable. An empty value disables scanning
  for local development only.
- MTA-STS: per-domain mode/max-age, `_mta-sts` TXT record generation and a
  public `/.well-known/mta-sts.txt` endpoint. Verification checks the TXT id,
  fetches the HTTPS policy with a public-IP-only dialer, and compares mode,
  max_age and MX.
- S3/MinIO connector (AWS SDK v2, path-style support, upload/download/delete
  and prefix sync; a successful listing removes local rows for objects removed
  from the bucket) and Google Drive/Dropbox/OneDrive OAuth authorization, token
  refresh, upload/download and provider sync. Tokens are AES-GCM sealed at rest.
  S3 is live-tested against a staging S3-compatible service; OAuth requires
  customer app credentials.
- Incremental large-mailbox sync: full UID snapshots for exact move/delete
  reconciliation, body downloads only for unseen UIDs, newest-first budgeted
  import with a durable `pending` count, periodic flag-only reconciliation and
  UIDVALIDITY reset handling. The time budget is shared fairly across folders
  and every folder gets at least one batch per pass, so a large Archive/INBOX
  cannot starve Sent/Drafts/Junk/Trash. No 1,000-message folder cap.
- Normalized mail storage: `mail_messages` (one row per mailbox copy, with
  generated `search_vector` and GIN index), `attachments` (SHA-256 byte
  metadata), `mail_message_attachments`, `outbox` and `undo_actions`. Migrations
  `0011`–`0013` backfilled legacy `app_states` documents and removed their mail
  keys. Handlers load rows, mutate and persist only the diff in one
  transaction, serialized per mailbox.
- Server-side full-text search via `/api/search`: UI operators become
  parameterized predicates; free text runs both `websearch_to_tsquery('simple')`
  and `websearch_to_tsquery('english')` against the GIN index (subjects are
  indexed simple, bodies english), capped at 500 results. The frontend calls it
  with a 220 ms debounce, so search no longer depends on browser-resident
  messages.
- IMAP IDLE (`ALT8MAIL_IMAP_IDLE`, default true): an INBOX push connection per
  active mailbox imports new mail and refreshes flags/expunges as events
  arrive. Keepalive OKs are ignored; new mail runs the UID check while
  expunge/FETCH events run the flag reconcile. The IDLE command renews at 25
  minutes and reconnects with backoff. Servers without IDLE fall back to the
  15-second UID check.
- Rich compose: HTML bodies are accepted from the WYSIWYG editor, sanitized
  server-side with an allowlist (scripts, handlers, iframes, forms and
  javascript: URLs are removed), stored in `mail_messages.body_html` and
  `outbox.body_html`, used in MIME multipart/alternative and in IMAP drafts.
  Plain-text bodies are generated as the fallback.
- Self-service sending identities: primary/alias identities come from
  `user_aliases`; custom identities live in `mail_identities`. External
  addresses receive a 48-hour hashed-token verification email; only verified
  identities are sendable. Internal addresses require an owned alias.
- Dated or permanent mail forwarding under `/api/forwarding`, compiled into the
  same Dovecot Sieve script as rules/vacation with policy checks, keep/discard
  copy behavior, and DD/MM/YYYY UI dates converted to ISO.
- Tenant AI assistant (`tenant_ai_settings`): admin-configurable
  OpenAI-compatible or Anthropic endpoint/model/key (AES-GCM sealed), max tokens,
  temperature, timeout and system prompt. User APIs cover compose generation,
  message summaries and translation; summaries/translations cache in
  `mail_ai_cache` by message, user and language. The AI UI is hidden when the
  tenant has not enabled the assistant.
- Notifications are derived live in the frontend from unread mail, scheduled
  sends, upcoming calendar events, security audit entries and quota warnings.
  Read/dismissed state is per-browser; no demo notification rows are shipped.
- A branded boot splash covers the gap between HTML parse and session/bootstrap
  completion, preventing the login screen from flashing during reload with a
  valid session.
- Every frontend settings tab is DB-backed: `users.preferences` for appearance,
  reading, notifications and regional values; ManageSieve deployment for rules
  and automatic replies; real tables for TOTP, WebAuthn, recovery codes,
  sessions and the per-user first-time enrollment policy. No settings are mock
  or browser-only.
- Real RFC 6238 TOTP, AES-GCM-sealed TOTP secrets, hashed single-use recovery
  codes, trusted devices and WebAuthn passkey registration/verification
  (`go-webauthn`). Password login issues a real `auth_challenges` challenge when
  a factor is enrolled. `users.enrollment_required` turns on the first-time
  enrollment screen; staging keeps it false.
- Root `/api` is served by this backend. The Node reference preview is stopped;
  no preview JSON is read by this service or the staging frontend.
- Per-user mailbox worker pool (isolated locks, bounded concurrency), durable
  outbox worker, IMAP IDLE plus a 15-second UID-based INBOX fallback check,
  daily retention pass and deferred-expunge worker started by `cmd/server`.
- Real Dovecot mailbox sync over verified IMAP TLS: INBOX, Sent, Drafts,
  Archive, Junk and Trash; MIME body/attachment parsing, authentication-results
  parsing, threading, flag updates and missing-message reconciliation.
- Message identity is folder-scoped, so a self-send keeps distinct Sent and
  INBOX copies (same Message-ID) and both share one conversation thread.
- IDLE imports new mail immediately; if IDLE is unavailable, the INBOX check
  fetches only UIDs above the checkpoint, so new mail reaches the API in about
  15 seconds. The frontend polls and updates in place with a notification/toast.
  Full scans still reconcile moves and flags outside INBOX.
- Two-way read/star/flag/move/archive/spam/delete propagation, undo and deferred
  expunge. Compose drafts are stored in Dovecot Drafts.
- SMTP submission to Postfix 587 with STARTTLS, Dovecot SASL, Rspamd/ClamAV in
  path and Sent-folder append.
- Rules and automatic replies are compiled to Sieve and pushed over ManageSieve
  (port 4190, STARTTLS, AUTH PLAIN) as the active `a8m` script before the
  API acknowledges the save. Redirect rules are checked against tenant
  forward-internal/forward-external policy, and existing scripts are re-pushed
  after a policy change.
- `cmd/mailprovision` explicitly creates Maildir++ folders as the `vmail` owner
  from pending/error rows; `deploy/systemd/a8m-mailprovision.timer` runs it
  every minute. Provisioning state is exposed in the admin user detail.
- `cmd/dkimprovision` runs as root from a timer, generates/reuses a 2048-bit RSA
  key per domain/selector under `/var/lib/rspamd/dkim`, stores the real DKIM
  public record in PostgreSQL, writes the Rspamd selector map and reloads
  Rspamd when keys change. The admin domain screen reads that exact record; no
  placeholder is synthesized.
- Tenant policy adapters enforce password history/expiry, IMAP/POP/SMTP access
  (Dovecot SQL but also Go-side checks), sharing link restrictions, quarantine
  release and retention (UIDPLUS `UID EXPUNGE`, no collateral expunge).
- `GET /api/mail/sync` status and `POST /api/mail/sync` on-demand sync.
- `cmd/inituser` creates a non-demo mailbox state from PostgreSQL identity,
  aliases and directory. Tasks, Files, Calendar and Audit start empty.

A separate Go admin backend (`cmd/adminserver`, `admin_*.go`) serves the
customer/system administration console at `/admin/` or an admin subdomain. It
has independent admin sessions/cookies, RBAC (owner/admin, custom roles and
`is_system_admin`), tenant lifecycle, users/aliases, groups, org units, real DNS
domain verification, policy, admin TOTP, admin audit and system administrators.
See `docs/ADMIN.md`.

Staging has real Go auth users including `avery.chen@a8m.test` (system
administrator) and `jordan.lee@a8m.test`. Every active mailbox has a
per-user worker. Postfix group addresses expand to active members, and Maildirs
are provisioned explicitly by the root timer. Normalized workspace tables,
CalDAV/CardDAV, guest share pages, S3 sync and MTA-STS policy publication are
implemented. Public DNS verification and MTA-STS validation require a
customer-owned domain; provider OAuth requires real Google/Dropbox/Microsoft
app credentials. Staging status and gaps are tracked in
`docs/PHASE_REVIEW.md` and `docs/OPERATIONS.md`.

## Build

```bash
go mod tidy
go test ./...
go build -trimpath -o bin/a8m-server ./cmd/server
go build -trimpath -o bin/a8m-admin-server ./cmd/adminserver
go build -trimpath -o bin/a8m-admin ./cmd/admin
go build -trimpath -o bin/a8m-inituser ./cmd/inituser
go build -trimpath -o bin/a8m-mailprovision ./cmd/mailprovision
go build -trimpath -o bin/a8m-dkimprovision ./cmd/dkimprovision
```

## Configuration

Configuration is read from environment variables. See
`docs/OPERATIONS.md` for the staging `app.env` layout and secret handling. The
required identity values are:

```text
ALT8MAIL_DB_HOST, ALT8MAIL_DB_PORT, ALT8MAIL_DB_NAME,
ALT8MAIL_DB_USER, ALT8MAIL_DB_PASSWORD,
ALT8MAIL_REDIS_ADDR, ALT8MAIL_REDIS_PASSWORD,
ALT8MAIL_SESSION_SECRET, ALT8MAIL_CSRF_SECRET
```

Mail workers add:

```text
ALT8MAIL_IMAP_ADDR, ALT8MAIL_IMAP_MASTER_USER,
ALT8MAIL_IMAP_MASTER_PASSWORD_FILE, ALT8MAIL_MAILDIR_BASE,
ALT8MAIL_MAIL_WORKER_CONCURRENCY (default 8),
ALT8MAIL_IMAP_TLS_NAME, ALT8MAIL_IMAP_SYNC_SECONDS,
ALT8MAIL_IMAP_POLL_SECONDS (new-mail fallback, default 15, min 5),
ALT8MAIL_IMAP_IDLE (default true),
ALT8MAIL_ORPHAN_SWEEP_SECONDS (default 3600, min 60),
ALT8MAIL_ORPHAN_GRACE_SECONDS (default 300, min 60),
ALT8MAIL_SMTP_ADDR, ALT8MAIL_SMTP_TLS_NAME, ALT8MAIL_MAIL_CA_FILE,
ALT8MAIL_MANAGESIEVE_ADDR, ALT8MAIL_MANAGESIEVE_TLS_NAME

# Legacy single-mailbox fallback, used only when master auth is absent:
ALT8MAIL_IMAP_USER, ALT8MAIL_IMAP_PASSWORD_FILE
```

WebAuthn and enrollment add:

```text
ALT8MAIL_PUBLIC_URL=https://mail.example.com
ALT8MAIL_WEBAUTHN_RPID=mail.example.com
ALT8MAIL_WEBAUTHN_ORIGINS=https://mail.example.com
```

`ALT8MAIL_WEBAUTHN_RPID` must be the bare hostname; origins are comma-separated
and are validated by the WebAuthn ceremony. Both default to
`ALT8MAIL_PUBLIC_URL`. Apply migrations `0003_real_auth.sql` through
`0014_compose_identity_ai.sql` before deploying this release; `0011`–`0014`
must run before normalized mail, identity verification and AI paths are used.

Storage, DNS and DAV add:

```text
ALT8MAIL_MAIL_HOSTNAME=mail.example.com
ALT8MAIL_DKIM_DIR=/var/lib/rspamd/dkim
ALT8MAIL_DKIM_SELECTOR_MAP=/var/lib/rspamd/dkim/selectors.map

ALT8MAIL_S3_ENDPOINT=127.0.0.1:9000
ALT8MAIL_S3_REGION=us-east-1
ALT8MAIL_S3_BUCKET=a8m
ALT8MAIL_S3_ACCESS_KEY=...
ALT8MAIL_S3_SECRET_KEY=...
ALT8MAIL_S3_USE_SSL=false
ALT8MAIL_S3_PATH_STYLE=true

ALT8MAIL_CLAMAV_ADDR=/var/run/clamav/clamd.ctl
ALT8MAIL_IMPORT_SCAN=true
ALT8MAIL_IMPORT_SCAN_MAX_MB=24

ALT8MAIL_GOOGLE_CLIENT_ID / ALT8MAIL_GOOGLE_CLIENT_SECRET
ALT8MAIL_DROPBOX_CLIENT_ID / ALT8MAIL_DROPBOX_CLIENT_SECRET
ALT8MAIL_MICROSOFT_CLIENT_ID / ALT8MAIL_MICROSOFT_CLIENT_SECRET
```

OAuth refresh/access tokens are encrypted with AES-GCM using a key derived from
`ALT8MAIL_SESSION_SECRET`. Rotating that secret invalidates stored connector
tokens, so reconnect providers after rotation. The tenant AI provider API key is
sealed with the same key material; rotating the session secret also requires an
administrator to re-enter the AI key.

The AI assistant itself has no environment credentials. Configure it per tenant
in Admin → AI assistant: enable, choose `openai` (for OpenAI,
DeepSeek, Azure-compatible or local gateways) or `anthropic`, then set endpoint,
model, optional API key, max tokens, temperature, timeout and system prompt. The
user-facing AI controls appear only while the tenant setting is enabled.

The admin backend adds:

```text
ALT8MAIL_ADMIN_LISTEN=127.0.0.1:8081
ALT8MAIL_ADMIN_DIR=/opt/a8m/admin
ALT8MAIL_ADMIN_URL=https://admin.example.com
ALT8MAIL_ADMIN_SESSION_HOURS=8
```

`ALT8MAIL_COOKIE_SECURE` must be `true` when the app is served over HTTPS.
`ALT8MAIL_ENV=production` refuses insecure cookies and refuses
`ALT8MAIL_DEMO_MODE=true`.

Staging demo login is opt-in only:

```text
ALT8MAIL_DEMO_MODE=true
ALT8MAIL_DEMO_EMAIL=avery.chen@a8m.example
ALT8MAIL_DEMO_PASSWORD=<staging secret>
ALT8MAIL_DEMO_USER_EMAIL=avery.chen@a8m.test
```

## Set an account password

```bash
printf '%s\n' "$NEW_PASSWORD" \
  | ./bin/a8m-admin -email avery.chen@a8m.test
```

The admin command writes the Argon2id web hash. Dovecot uses the separate
`mail_password_hash` column; admin creation/reset issues a distinct mailbox
password hashed by `doveadm pw`, while a user's self-service web password change
rotates both hashes. Workers authenticate with Dovecot master auth and never
store per-user plaintext mailbox passwords.

## First-time enrollment policy

`users.enrollment_required` defaults to false. Set it per user with the
Security & privacy toggle (`PATCH /api/security/policy`) or SQL:

```sql
UPDATE users SET enrollment_required = true, updated_at = now()
WHERE email = 'avery.chen@a8m.test';
```

With the policy on, a password sign-in with no enrolled factor is stopped at
`/api/enrollment/totp/begin|finish` or
`/api/enrollment/webauthn/begin|finish`. Staging deliberately keeps it false so
normal sessions can be tested. Tenant-wide MFA/IP policy is managed from the
admin console and enforced at mail sign-in.

## Initialize a real mailbox state

This replaces any existing `app_states` document with a clean, non-demo state
built from PostgreSQL identity, aliases and tenant directory. It intentionally
starts with zero messages, tasks, files and calendar events; the IMAP worker then
fills the mailbox from Dovecot:

```bash
set -a; . /opt/a8m/secrets/app.env; set +a
./bin/a8m-inituser -email avery.chen@a8m.test
systemctl restart a8m-server
```

`cmd/mailprovision` creates the Maildir and its `Sent`, `Drafts`, `Archive`,
`Junk` and `Trash` folders explicitly; `ensureIMAPFolders` is a second safety
net on sync. `cmd/seedstate` and `seed/state.json` belong to the old reference
preview and are not used by the live Go service.

## Verification

Repository helpers:

```bash
python3 ../tools/go-api-smoke.py     # API, including real SMTP when configured
ALT8MAIL_UI_PASSWORD="$(cat ./staging_avery_password)" \
  node ../tools/go-ui-smoke.mjs      # Chromium UI smoke
ALT8MAIL_UI_PASSWORD="$(cat ./staging_avery_password)" \
  node ../tools/go-settings-smoke.mjs  # all settings tabs + DB readback
ALT8MAIL_UI_PASSWORD="$(cat ./staging_avery_password)" \
  node ../tools/go-auth-smoke.mjs    # TOTP, recovery, WebAuthn, enrollment
ALT8MAIL_UI_PASSWORD="$(cat ./staging_avery_password)" \
  node ../tools/go-live-mail-smoke.mjs  # open-page self-delivery + notification
ALT8MAIL_UI_PASSWORD="$(cat ./staging_avery_password)" \
  node ../tools/compose-notifications-smoke.mjs  # 0.7 compose/notifications

ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password \
  python3 ../tools/admin-api-smoke.py
ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password \
  python3 ../tools/admin-auth-smoke.py
ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password \
  python3 ../tools/admin-policy-smoke.py
ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password \
  python3 ../tools/admin-tenant-smoke.py
ALT8MAIL_ADMIN_PASSWORD="$(cat ./staging_avery_password)" \
  node ../tools/admin-ui-smoke.mjs
ALT8MAIL_SMTP_HOST=127.0.0.1 ALT8MAIL_SMTP_PORT=1587 \
  python ../tools/mail-policy-smoke.py
```

The settings test resets preferences on the server, changes every tab, and reads
`/api/preferences` back. The auth test enrolls TOTP and a virtual passkey,
verifies both sign-in paths and the first-time enrollment screen, then removes
all factors and sets `enrollment_required=false`.
