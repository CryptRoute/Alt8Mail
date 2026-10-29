# Go feature API

The Go system backend in `backend/` serves the Aurora frontend at `/api` with
real Dovecot mailbox data. nginx proxies both `/api/` and `/system/api/` to
`127.0.0.1:8080`. A separate Go admin backend (`cmd/adminserver`) serves the
customer/system administration console at `/admin/` (or an `admin.` subdomain)
on `127.0.0.1:8081`; its API, RBAC and policy references are in
[ADMIN.md](ADMIN.md). The Node reference service is stopped/disabled; it is not
in the request path and its JSON demo data is not read by the live frontend.

## Authentication

- `POST /api/session` verifies an Argon2id password, creates an opaque session
  in PostgreSQL and sets `meridian_system_sid` + `meridian_system_csrf`.
- `POST /api/password/change` validates the current password, tenant password
  policy and history, rotates both `users.password_hash` and
  `users.mail_password_hash`, records the old hash in `password_history`,
  updates expiry and revokes other sessions/trusted devices. A required or
  expired password restricts the session to bootstrap, session and this
  endpoint until rotation.
- Mutations require `X-CSRF-Token`; the token is returned by sign-in/session.
- Login is rate-limited by client IP. Session cookies are HttpOnly, Secure and
  SameSite=Lax in staging/production.
- `MERIDIAN_DEMO_MODE=true` enables the staging alias
  `avery.chen@meridian.example` / `meridian-demo`, mapped to
  `avery.chen@meridian.test`. Config loading refuses demo mode in production.
  While demo mode is enabled, that mapped account also accepts the demo
  password for current-password re-authentication prompts (app passwords,
  password change and authenticator removal); no other account accepts it.
- Real RFC 6238 TOTP (SHA-1, 30-second step, six digits, ±1 window), hashed
  single-use recovery codes, trusted-device cookies and WebAuthn passkeys are
  implemented in Go. A password sign-in returns a five-minute challenge
  (`auth_challenges`) when TOTP or a passkey is enrolled.
- WebAuthn uses `MERIDIAN_WEBAUTHN_RPID` and `MERIDIAN_WEBAUTHN_ORIGINS`
  (default: the host/origin from `MERIDIAN_PUBLIC_URL`). Ceremony state lives in
  Redis; credential IDs, public keys, sign counters and transports live in
  `webauthn_credentials` in PostgreSQL.
- First-time enrollment is a per-user policy in `users.enrollment_required`
  (migration `0003_real_auth`). When true, a password sign-in that has no
  second factor is stopped at the enrollment screen. Staging has the policy
  **disabled** (`false`) for every user so ordinary password sessions can be
  tested; it is toggled from Security & privacy or with SQL.
- TOTP secrets are sealed at rest with AES-256-GCM using a key derived from the
  session and CSRF secrets; recovery codes, challenges and trusted-device
  tokens are stored only as hashes. Public API payloads never return a TOTP
  secret, an unused recovery code or a passkey public key.

## Mail and workspace endpoints

| Area | Endpoints |
| --- | --- |
| Session | `POST/GET/DELETE /api/session`, `POST /api/password/change` |
| Bootstrap / navigation | `GET /api/bootstrap`, `/api/folders`, `/api/aliases` |
| Messages | `GET /api/messages`, `PATCH /api/messages/:id`, `POST /api/messages/read`, `POST /api/messages/actions`, `POST /api/undo/:token` |
| Search | `GET /api/search?q=` |
| Drafts | `GET /api/drafts`, `PUT/POST /api/drafts/:id`, `DELETE /api/drafts/:id` |
| Send / outbox | `POST /api/send`, `GET /api/outbox`, `PATCH /api/outbox/:id`, `POST /api/outbox/:id/cancel`, `POST /api/outbox/:id/send-now` |
| IMAP sync | `GET /api/mail/sync`, `POST /api/mail/sync` |
| Trust | `GET /api/trust`, `GET/POST /api/messages/:id/trust` |
| Tasks | `GET/POST /api/tasks`, `PATCH/DELETE /api/tasks/:id` |
| Calendar | `GET/POST /api/calendar/events`, `PATCH/DELETE /api/calendar/events/:id` |
| Contacts / directory | `GET/POST /api/contacts`, `PATCH/DELETE /api/contacts/:id`, `GET /api/directory` |
| Profile | `GET/PATCH /api/profile` (policy-gated) |
| Storage / files | `GET /api/storage`, `POST /api/storage/:id/:action`, `GET/POST /api/files`, `PATCH/DELETE /api/files/:id`, `POST /api/files/:id/share`, `GET /api/files/:id/content` |
| Storage OAuth | `GET /api/storage/oauth/:provider/start`, `GET /api/storage/oauth/:provider/callback` |
| Guest shares | `GET /api/public/shares/:token`, `GET /api/public/shares/:token/content`, `POST /api/public/shares/:token/replace`, `GET /files/s/:token` |
| Attachments | `POST /api/attachments`, `GET /api/attachments/:id/content` |
| CalDAV/CardDAV | `/dav/`, `/.well-known/caldav`, `/.well-known/carddav` (PROPFIND/REPORT/GET/PUT/DELETE, Basic auth) |
| App passwords | `GET/POST /api/security/app-passwords`, `DELETE /api/security/app-passwords/:id` (permanent or 1–730-day expiry; soft-revoked history) |
| Identities | `GET/POST /api/identities`, `POST /api/identities/:id/resend`, `DELETE /api/identities/:id`, `GET/POST /api/identities/verify` |
| Forwarding | `GET/PUT /api/forwarding` (dated or permanent, keep-copy, Sieve-generated) |
| AI assistant | `POST /api/ai/compose`, `POST /api/ai/messages/:id/summary`, `POST /api/ai/messages/:id/translate`, `POST /api/ai/messages/:id/reply` (tenant-enabled) |
| Admin AI | `GET/PATCH /admin/api/ai`, `POST /admin/api/ai/test` (policies read/manage) |
| MTA-STS policy | `GET /.well-known/mta-sts.txt` (Host `mta-sts.<domain>`) |
| Sieve | `GET/PUT /api/sieve`, `GET /api/sieve/script` (saved rule/vacation JSON is compiled and pushed to Dovecot ManageSieve) |
| Audit | `GET /api/audit`, `GET /api/audit.csv` |
| Settings | `GET/PATCH /api/preferences`, `POST /api/preferences/reset` |
| Security | `GET/POST /api/security/recovery-codes`, `POST /api/security/2fa/totp/begin\|finish\|disable`, `POST /api/security/webauthn/register/begin\|finish`, `DELETE /api/security/webauthn/:id` |
| Security policy / sessions | `GET/PATCH /api/security/policy`, `GET /api/security/sessions`, `POST /api/security/sessions/revoke-others`, `POST /api/security/sessions/:id/revoke` |
| Sign-in factors | `POST /api/session/2fa` (TOTP/recovery), `POST /api/session/2fa/webauthn/begin\|finish`, `POST /api/enrollment/totp/begin\|finish`, `POST /api/enrollment/webauthn/begin\|finish` |
| Usage | `GET /api/quota`, `GET /api/outbound` |

## Settings persistence

All user-facing settings are stored in PostgreSQL, not in browser storage or a
demo document:

- `users.preferences` (JSONB) is the authoritative store for Appearance,
  Mail & reading (including the `grouped`/`individual` `inboxView` choice,
  default `grouped`), Notifications, Language & time, sender warnings, default
  identity and undo-window choices. `PATCH /api/preferences` validates each
  field, writes the row and mirrors it into the per-user `app_states` document
  so the bootstrap response stays a single read. `POST /api/preferences/reset`
  writes the server defaults and audits the change.
- `GET /api/preferences` returns the persisted values plus the server default
  object, so a fresh client can render every control without local mock data.
  The browser keeps only a pre-authentication theme cache and replaces it from
  the server on bootstrap.
- Rules and Automatic replies are saved in the user's `sieve` state, compiled
  by `renderSieve`, and deployed to Dovecot before the API returns success. A
  ManageSieve failure returns `SIEVE_SERVICE` and leaves the previous rule set
  active. Redirect actions are validated against tenant internal/external
  forwarding policy; changing that policy re-pushes every affected script while
  preserving the stored rule for later editing.
- Security & privacy is backed by real tables: `users.security` (TOTP secret,
  enabled flag), `webauthn_credentials`, `recovery_codes`, `auth_challenges`,
  `trusted_devices` and `sessions`. Account shows the authenticated PostgreSQL
  identity, aliases and live revocable sessions; Sign out other sessions
  deletes those rows.
- `PATCH /api/security/policy` persists `enrollment_required`. It is
  per-user in this staging slice; a tenant/org policy belongs in the customer
  backend.

## Mail pipeline

```text
IMAP 993 (verified mail.meridian.test certificate)
  → ensure INBOX/Sent/Drafts/Archive/Junk/Trash exist
  → full UID snapshot per folder (uncapped, for exact reconciliation)
  → fetch envelope + flags for known UIDs at most every 15 minutes
  → fetch BODY.PEEK[] only for UIDs missing locally, newest-first within a
    shared 45-second per-pass budget; persist pending backfill
  → MIME decode (text, HTML, attachments)
  → Authentication-Results parsing (SPF/DKIM/DMARC)
  → thread by Message-ID / References
  → upsert normalized mail_messages and attachment rows in one transaction;
    update flags/folder; remove messages absent from every successfully
    scanned folder; reset a folder on UIDVALIDITY change
  → attachment bytes stored under MERIDIAN_UPLOAD_DIR

Per-user worker (one per active mailbox, bounded by
MERIDIAN_MAIL_WORKER_CONCURRENCY, serialized per user)
  → IMAP IDLE on INBOX when MERIDIAN_IMAP_IDLE=true (the default)
    → EXISTS runs the UID check; EXPUNGE/FETCH run the flag reconcile
    → ignore untagged OK keepalives so they cannot trigger reconnects
    → reconnect with backoff and a 25-minute server logout timeout
    → fall back to polling if the server does not advertise IDLE
  → every MERIDIAN_IMAP_POLL_SECONDS (default 15): SELECT INBOX
    → UID SEARCH only UIDs above the saved INBOX checkpoint
    → fetch envelopes/bodies for those UIDs only
    → merge without reconciling other folders
    → advance the checkpoint; the open webmail refreshes in place
  → every MERIDIAN_IMAP_SYNC_SECONDS (default 300): full folder reconciliation
  → daily retention: UID SEARCH BEFORE cutoff, AddFlags \Deleted, UID EXPUNGE
    (UIDPLUS; never a mailbox-wide EXPUNGE), then delete rows and audit

Read/star/flag/move/spam/delete
  → mutate the loaded mail rows and persist the diff transactionally
  → IMAP UID SEARCH by Message-ID + UID STORE / MOVE
  → permanent deletes mark \Deleted and queue an expunge for after the
    undo window; undo clears the flag and cancels the expunge

Compose draft → PUT /api/drafts/:id
  → stable Message-ID
  → replace existing Drafts copy, APPEND new copy with \Draft \Seen

Compose → POST /api/send → outbox
  → remove the IMAP draft
  → worker claims due item
  → MIME builder (multipart/alternative + mixed)
  → SMTP 587 STARTTLS + Dovecot SASL (Postfix)
  → Rspamd/ClamAV/queue
  → commit Sent copy to mail_messages
  → IMAP APPEND to Sent

Sieve rules / automatic replies → PUT /api/sieve
  → validate fields, operators, folder targets, dates and message lengths
  → render a complete Sieve script (fileinto, imap4flags, body, date, vacation)
  → ManageSieve 4190 STARTTLS + AUTH PLAIN (mailbox password)
  → PUTSCRIPT "meridian" + SETACTIVE "meridian"
  → only then persist the structured rules in app state
  → Pigeonhole applies fileinto/vacation at LMTP delivery

WebAuthn sign-in → POST /api/session/2fa/webauthn/begin
  → issue a challenge for the user's registered credential IDs
  → browser navigator.credentials.get() assertion
  → POST /api/session/2fa/webauthn/finish verifies origin, RP ID,
    signature and sign counter in Go
  → session created; challenge consumed

First-time enrollment (policy enabled)
  → password accepted → auth_challenges kind 'enrollment'
  → TOTP or WebAuthn enrollment endpoint → credential stored
  → recovery codes issued → session created
```

### Search

`GET /api/search?q=` parses the same operators as the UI (`from:`, `to:`,
`subject:`, `folder:`, `in:`, `is:unread`, `is:starred`, `has:attachment`,
`before:`/`after:` and free text), then runs one indexed query scoped to the
authenticated mailbox:

- `mail_messages.search_vector` is a stored generated `tsvector` over subject,
  sender, recipients and body text, indexed with GIN;
- subject/participants are indexed with `simple` and bodies with `english`; free
  text runs both `websearch_to_tsquery('simple', ...)` and
  `websearch_to_tsquery('english', ...)` (OR'ed, each GIN-backed), so quoted
  phrases, OR and `-exclusion` work and stemming cannot hide an unstemmed
  subject or sender match;
- operators become parameterized SQL predicates;
- results are capped at 500 and returned in the existing `/api/search` shape.

The frontend debounces input by 220 ms and replaces the visible message list
with the server result set; clearing the query restores the folder list. Search
is server-side and does not depend on messages already downloaded into the
browser.

## Compose, identities, forwarding and AI

**Rich compose.** `PUT /api/drafts/:id` and `POST /api/send` accept `bodyHtml`
and `bodyText`. The Go backend never trusts editor HTML: `sanitizeComposeHTML`
uses a bluemonday allowlist and removes scripts, forms, iframes, event handlers,
`javascript:` URLs and unsupported styles before the body enters
`mail_messages.body_html` or `outbox.body_html`. A plain-text fallback is derived
when the client supplies only HTML. `buildMIMEMessage` emits the sanitized HTML
in the `multipart/alternative` part and uses it for IMAP Drafts/Sent copies.
Drafts carry `windowMode` (`floating`, `minimized`, `inline` or `closed`); the
frontend restores open/minimized windows but never a deliberately closed one.

**Sending identities.** `mail_identities` stores custom addresses with a
`pending`/`verified` status, hashed 48-hour verification token and optional
reply-to/signature. An external address receives a real email through Postfix
containing `/api/identities/verify?token=...`; the GET page requires a POST
confirmation so link scanners do not consume the token. Internal addresses must
already be an alias owned by the user. Verified identities are merged with
`user_aliases` into the bootstrap `identities` list and the legacy state alias
list, so compose/draft/send validation is consistent. The admin-managed primary
and alias rows cannot be deleted by the user.

**Forwarding.** `GET/PUT /api/forwarding` stores `enabled`, `target`,
`keepCopy`, `permanent`, `start` and `end` in the user's Sieve state. Validation
rejects self-forwarding and checks tenant auto-forwarding/external-forwarding
policy. The renderer emits a dated `currentdate` block when not permanent and
only emits `stop` when `keepCopy` is false. It is compiled into the same script
as rules and vacation and pushed before the API returns success. The frontend
accepts DD/MM/YYYY dates and converts them to ISO; invalid dates are rejected.

**AI assistant.** Admin policy endpoints manage `tenant_ai_settings`
(`openai`/`anthropic`, endpoint, model, sealed API key, max tokens, temperature,
timeout and system prompt). `GET /api/bootstrap` exposes only enabled/provider/
model plus the 36-language and 8-style catalogs. `POST /api/ai/compose` uses the
current editor text as context and style/length/creativity/language controls.
`POST /api/ai/messages/:id/summary` loads the authenticated user's recent
conversation (up to eight non-draft messages, oldest to newest) and caches a
thread-aware result in `mail_ai_cache`; `translate` translates the latest
message into the user's configured interface language. Reader AI is strictly
user-triggered: no reader or hover summary is generated automatically.
`POST /api/ai/messages/:id/reply` loads up to ten recent messages in the thread
and returns a ready-to-send reply body for the composer, written in the user's
language. Provider calls use a bounded timeout, no redirects, a 4 MiB response
cap, treat quoted mail as untrusted data, and never return the API key. If the
tenant setting is off or the request fails, the user UI is hidden/returns
`AI_DISABLED` or `AI_PROVIDER`.

**App passwords.** Creation accepts `expiresInDays` (1–730) or `expiresAt`;
DAV authentication checks `revoked_at IS NULL` and `expires_at > now()` on every
request. Revocation is soft so the UI can show an active/expired/revoked
history. The raw token is shown once and stored only as a SHA-256 hash.

**Notifications.** Notification content is derived client-side from real unread
INBOX messages, pending/scheduled sends, upcoming calendar events, recent
`auth.*`/`security.*` audit entries and quota/outbound warnings. Read/dismissed
IDs are stored per account/browser in localStorage; no demo rows or separate
notification table are used. The popover shows the latest items and the "View
all" window provides All/Unread/Mail/System filters.

The mailbox password is separate from the web password. It lives in
`/opt/meridian/secrets/avery_mail_password` and is referenced by
`MERIDIAN_IMAP_PASSWORD_FILE`. The IMAP/SMTP/ManageSieve clients verify the CA in
`MERIDIAN_MAIL_CA_FILE` and the hostname in `MERIDIAN_IMAP_TLS_NAME` /
`MERIDIAN_SMTP_TLS_NAME` / `MERIDIAN_MANAGESIEVE_TLS_NAME`.

## Storage model

Mail is normalized. `mail_messages` holds one row per mailbox copy — tenant,
user, folder and IMAP folder, UID/UIDVALIDITY, Message-ID, thread and reply
headers, envelope JSON plus flattened recipient text, subject, snippet, text
and HTML bodies, flags, timestamps, size, draft/classification/trust fields and
a generated `search_vector`. `attachments` stores byte metadata keyed by
SHA-256 and `stored_path`; `mail_message_attachments` joins attachments to
messages. Scheduled/send state is in `outbox` and undo snapshots are in
`undo_actions`. `0011_mail_tables.sql` through `0013_undo_table.sql` create the
tables, backfill any legacy documents and then remove the `messages`,
`attachments`, `outbox` and `undo` keys from `app_states`.

`0014_compose_identity_ai.sql` adds `mail_identities` (custom verified sending
addresses and hashed verification tokens), `tenant_ai_settings` (encrypted
provider configuration) and `mail_ai_cache` (per-message/user/language
summaries and translations), adds `app_passwords.revoked_at` and
`outbox.body_html`, and grants them to `meridian_app`. `app_passwords` retains
expiry and revocation history.

`app_states` remains only for small per-user presentation state (people, folder
metadata, pending expunges, Sieve/forwarding profile and feature audit). For a new
mailbox, `meridian-inituser` builds that document from PostgreSQL identity,
aliases, tenant directory, quota and trusted senders; mail is imported
exclusively from Dovecot. `seed/state.json` is not read by the Go service or
the staging frontend.

Workspace data is normalized in `tasks`, `files`, `file_shares`,
`calendar_events`, `calendar_attendees`, `contacts`, `storage_accounts`,
`storage_oauth_states` and `workspace_audit`; legacy
tasks/files/calendar/storage arrays are removed by `0007_workspace.sql`.
Access is scoped by the authenticated user and tenant. Mail sync and API
mutations for one mailbox serialize on that user's worker/state lock, not a
process-wide document mutex. A handler loads the rows it needs, mutates them in
memory and persists only the diff in one transaction, so unchanged messages are
not rewritten.

Authentication state is normalized: `users` (including `enrollment_required`),
`sessions`, `auth_challenges` (kind `two_factor` or `enrollment`),
`webauthn_credentials`, `recovery_codes` and `trusted_devices` from
`migrations/0001_core.sql` plus `0003_real_auth.sql`. Admin identity, RBAC,
groups, org units, admin sessions/audit and tenant policy are added by
`0004_customer_admin.sql` and documented in [ADMIN.md](ADMIN.md). Migrations
`0003`/`0004` must be applied before deploying backends that select their new
columns, or authentication/admin requests fail closed.

Workspace tables are normalized by `migrations/0007_workspace.sql`; DAV fields
and app passwords are added by `0008`–`0010`; mail, outbox and undo are
normalized by `0011`–`0013`, with identity/AI/credential additions in `0014`.
`app_states` retains only small presentation state and the feature audit. Apply
all fourteen migrations before deploying this release.

## Reliability notes

- Scheduled sends survive process restarts because they live in PostgreSQL.
- The worker claims an item before SMTP, retries with backoff, and records the
  last error on the outbox document.
- Delivery is at-least-once: an SMTP success followed by a crash before the
  database commit can retry. Keep a stable Message-ID and add an idempotency
  store before high-volume production use.
- Outbound submission consults the tenant admin policy before queueing: SMTP
  access, external-recipient allowlists, blocked attachment extensions and the
  configured attachment-size ceiling can reject a send with `MAIL_POLICY`.
- IMAP actions are two-way for read/star/flag/move/archive/spam/delete, with
  undo and deferred expunge. If an IMAP mutation fails, the next full sync
  reconciles the UI cache back to the server, which is the source of truth.
- Message identity is scoped by mailbox as well as Message-ID. A self-send
  produces two real copies (Sent and INBOX) with the same Message-ID; they stay
  distinct `mail_messages` rows and share a conversation thread. Matching prefers
  the IMAP UID/folder, then the folder-local Message-ID, then an unambiguous
  move of a message whose previous folder copy disappeared.
- IMAP IDLE keeps an INBOX push connection per active mailbox when
  `MERIDIAN_IMAP_IDLE=true` (default). A pushed new-mail event runs the UID
  check only; expunge/FETCH events refresh flags and remove rows for UIDs that
  are gone. Dovecot's two-minute untagged OK keepalive is ignored, so it no
  longer causes reconnects or whole-mailbox flag fetches. The IDLE command
  renews at 25 minutes and the worker reconnects with backoff on errors.
  Servers without IDLE fall back to the 15-second INBOX check via
  `UID SEARCH UID <lastUid+1>:*`, which fetches only new bodies and never
  reconciles other folders. Moves and deletions in other folders are picked up
  by the full scan every five minutes or by `POST /api/mail/sync`.
- The browser polls the inbox every 15 seconds, compares message IDs and updates
  the list/reader via `applyBootstrap` without a reload. It shows a desktop
  notification when permission is granted and asks the user to enable it; the
  in-app toast is the fallback and does not depend on notification permission.
- Full sync is incremental and uncapped: it takes a complete UID snapshot per
  folder for exact move/delete reconciliation, downloads bodies only for UIDs
  missing from local state, imports newest-first within a 45-second pass budget,
  and stores `pending`/`truncated` when older messages remain for the next pass.
  Flag-only reconciliation runs at most every 15 minutes (or when folder counts
  change) without re-downloading bodies. UIDVALIDITY changes reset that folder's
  local copies. The 1,000-message cap and per-folder truncation are gone.
- A 1,100-message staging mailbox imported fully with `pending=0`; a follow-up
  sync fetched only the five newly delivered bodies. Messages now persist as
  individual `mail_messages` rows with attachment joins, so a sync updates only
  changed rows instead of rewriting one document. Search is index-backed
  PostgreSQL FTS and new-mail push is IDLE, not a five-minute poll.
- The staging deployment uses Dovecot master auth
  (`MERIDIAN_IMAP_MASTER_USER`/`MERIDIAN_IMAP_MASTER_PASSWORD_FILE`) so every
  active mailbox has an independent worker without storing each user's
  plaintext mailbox password. `jordan.lee@meridian.test` and the seeded
  directory users are synced the same way as Avery.
- Postfix group addresses are real lists. The SQL alias map returns all active
  members of an active group, subject to tenant/domain/user status checks.
- Maildirs are provisioned explicitly by the root `cmd/mailprovision` timer;
  Dovecot folder auto-create remains as a fallback. The admin console exposes
  `provision_status`, attempts, error and path.
- Mail, workspace and auth storage are normalized. Mail sync and mutations
  serialize per mailbox, so one large mailbox does not block other users, and
  `MERIDIAN_MAIL_WORKER_CONCURRENCY` bounds how many run at once. Use the
  per-folder `pending` sync metadata to monitor backfill. Bootstrap still
  returns the full message set in one response; windowing/pagination is the
  remaining step for extreme mailbox sizes before the 1,000-user soak.

## Verification

```bash
cd backend
go test ./...        # unit/feature tests
go vet ./...
go build -trimpath -o bin/meridian-inituser ./cmd/inituser
./bin/meridian-inituser -email avery.chen@meridian.test

# on the operator workstation, with a local password file
GO_API=https://130-94-46-22.sslip.io/system/api \
GO_PASSWORD_FILE=./staging_avery_password \
python3 tools/go-api-smoke.py

MERIDIAN_UI_PASSWORD="$(cat ./staging_avery_password)" \
node tools/go-ui-smoke.mjs

# settings persistence across every tab
MERIDIAN_UI_PASSWORD="$(cat ./staging_avery_password)" \
node tools/go-settings-smoke.mjs

# real TOTP, recovery codes, WebAuthn, first-time enrollment policy
MERIDIAN_UI_PASSWORD="$(cat ./staging_avery_password)" \
node tools/go-auth-smoke.mjs

# self-delivery reaches INBOX while the page stays open; toast/browser notice
MERIDIAN_UI_PASSWORD="$(cat ./staging_avery_password)" \
node tools/go-live-mail-smoke.mjs

# calendar/contact/profile/app-password browser flows
MERIDIAN_UI_PASSWORD="$(cat ./staging_avery_password)" \
node tools/workspace-ui-smoke.mjs

# compose/notification/identity/app-password UI and closed-draft reload
MERIDIAN_UI_PASSWORD="$(cat ./staging_avery_password)" \
node tools/compose-notifications-smoke.mjs

# CalDAV/CardDAV discovery and CRUD (use an app password when MFA is enabled)
GO_API=https://130-94-46-22.sslip.io \
DAV_EMAIL=avery.chen@meridian.test \
DAV_PASSWORD="$(cat ./staging_avery_password)" \
python3 tools/dav-smoke.py
```

`go-settings-smoke.mjs` starts from a server-side preference reset, exercises
Appearance, Mail & reading, Notifications, Language & time, Rules, Automatic
replies, Audit, Security & privacy and Account, and reads `/api/preferences`
back to prove the values were persisted. `go-auth-smoke.mjs` enrolls TOTP via
the UI, signs in with a real code, flips the enrollment policy in PostgreSQL,
completes first-time TOTP and WebAuthn enrollment with Chrome's virtual
authenticator, signs in with a passkey assertion, then removes every factor and
restores `enrollment_required = false`. `go-live-mail-smoke.mjs` sends a message
to the signed-in user through the real outbox/SMTP path, waits for it to appear
in the open INBOX without a reload, and asserts the in-app notification.

The latest recorded outputs are in `artifacts/go-api-smoke.txt`,
`artifacts/go-ui-smoke.txt`, `artifacts/go-settings-smoke.txt`,
`artifacts/go-auth-smoke.txt`, `artifacts/go-live-mail-smoke.txt`,
`artifacts/go-live-mail.png`, `artifacts/go-settings-security.png`,
`artifacts/go-settings-account.png`, `artifacts/go-enrollment-required.png`,
`artifacts/go-feature-api.png`, `artifacts/compose-0.7-smoke.txt`,
`artifacts/compose-notifications-smoke.png`,
`artifacts/notification-center-deduped.png`, `artifacts/ai-assistant-smoke.txt`,
`artifacts/ai-compose-reader-smoke.png` and `artifacts/admin-ai-smoke.png`.
