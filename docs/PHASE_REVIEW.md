# Phase review — compose, workspace, protocol and scale phase

Snapshot: **2026-10-29**, webmail staging release **0.9.0-staging** with the
separate **System Control 0.10.0-control** plane, external security assessment
and 1,000-user soak.

This document is the phase gate for the workspace/protocol/scale work that
followed the mail-frontend and administration phase. It records what is
genuinely live on staging, what is implemented but constrained by external
dependencies, and what must not be claimed yet.

## Verdict

The platform now runs the full user-facing mail and workspace experience
against real PostgreSQL, Dovecot, Postfix, Rspamd, ClamAV and Redis. In
addition to the earlier mail, auth and admin work, this phase delivered:

- normalized Tasks, Files, Calendar, Contacts, storage accounts, guest shares
  and workspace audit tables;
- public guest-share pages with expiring view/edit links and file replacement;
- CalDAV and CardDAV discovery and CRUD for the default calendar and address
  book;
- revocable app passwords for DAV clients, mandatory for accounts with MFA;
- incremental, uncapped large-mailbox sync with exact move/delete
  reconciliation and a budgeted backfill;
- normalized mail, attachment, outbox and undo tables with PostgreSQL
  full-text search;
- IMAP IDLE push for new mail, flag and deletion events;
- a rich HTML/plain composer with saved size/position, real identities,
  drag-and-drop/Files attachments and closed-draft persistence;
- live mail/calendar/security notifications with a read-state center and a
  branded boot splash;
- verified external sending identities, dated/permanent Sieve forwarding and
  expiring app-password history;
- a tenant-configured AI assistant for compose, user-triggered conversation
  summaries, translation and Reply with AI;
- MTA-STS policy state, DNS records, public policy endpoint and verification;
- S3/MinIO storage plus Google Drive/Dropbox/OneDrive OAuth flows;
- directory external-contact visibility, profile editing and external-tag
  presentation policies enforced for members;
- a separate root-level System Control plane on TLS port 8808 with its own
  schema, DB role, sessions, TOTP, RBAC, audit, API keys and webhooks, covering
  live host/mail-stack operations from domains and DKIM through Postfix,
  Dovecot, listeners, logs, imports, backups, cron, network/UFW and PostgreSQL.

The platform is **not yet production-complete**. A customer-owned domain still
has to pass public DNS/MTA-STS validation, provider OAuth needs live provider
credentials, external-identity deliverability depends on the sender domain's
SPF/DKIM, a bulk-mailbox volume soak, an independent third-party penetration
test and production operations gates remain, and bootstrap still returns a
whole mailbox without windowing.

## Live and verified

### Mail frontend and workspace

- Aurora layout/UX with the real Go API; no demo mailbox JSON is read.
- Real IMAP sync for INBOX, Sent, Drafts, Archive, Junk and Trash; per-user
  worker pool, locks and bounded concurrency.
- **Mail is normalized.** Messages, attachment metadata/links, outbox and undo
  snapshots live in `mail_messages`, `attachments`,
  `mail_message_attachments`, `outbox` and `undo_actions`. Migrations
  `0011`–`0013` backfilled existing state and removed the `messages`,
  `attachments`, `outbox` and `undo` keys from `app_states`; production has
  zero users with mail keys remaining. Mail handlers persist only changed rows
  in one transaction, serialized per mailbox rather than on one global mutex.
- **IMAP IDLE push:** each active mailbox keeps an INBOX IDLE connection.
  Server events trigger an immediate UID check; expunge/FETCH events trigger the
  flag reconcile, with reconnect backoff and a 25-minute renewal. Dovecot's
  two-minute keepalive is ignored, so it cannot cause reconnect churn or a
  whole-mailbox flag fetch. Staging imports, flag changes and expunges arrived
  in under one second with a deliberately slow 30-second poll, and a restart
  stayed connected past the keepalive without reconnecting; servers without
  IDLE fall back to the 15-second check.
- **Server-side full-text search:** `/api/search` parses the UI operators and
  runs an indexed PostgreSQL query over a generated `tsvector` (GIN), matching
  both the `simple` and `english` text-search configurations with
  `websearch_to_tsquery`. Free text, quoted phrases, OR/exclusion and
  `from:`/`to:`/`subject:`/`folder:`/`in:`/unread/starred/attachment/date
  operators are covered; the frontend debounces 220 ms and swaps the list to
  server results.
- **Large-mailbox sync:** full UID snapshots for exact move/delete
  reconciliation; body downloads only for unseen UIDs; newest-first import
  within a 45-second pass budget; durable per-folder `pending`/`truncated`
  state; quick INBOX polling above the checkpoint; flag-only reconciliation at
  most every 15 minutes; UIDVALIDITY reset handling. The old 1,000-message cap
  is gone. A 1,100-message mailbox imported fully and a follow-up send imported
  only the 5 new messages with no re-download of existing bodies.
- New-mail push-to-UI over IDLE, notifications and toast; folder-scoped message
  identity; self-send keeps separate Sent/INBOX copies and one thread.
- Threads, server search/operators, read/star/flag, bulk actions, undo,
  deferred expunge, automatic replies and Sieve.
- Server drafts, SMTP submission, Sent append, scheduled send/outbox center,
  real TOTP/WebAuthn, recovery codes, password history/expiry and mailbox
  credential synchronization.
- **Tasks/Files/Calendar/Contacts are table-backed.** No legacy
  tasks/files/calendar/storage arrays remain in `app_states`. Task assignees
  are directory-validated; calendar attendee rows are maintained; personal
  contacts support create/update/delete and appear in directory search subject
  to policy.
- **Calendar writes:** create, edit and delete from the agenda and dialog,
  plus real storage and CalDAV access.
- **Contact writes:** create, edit and delete from People; directory results
  merge tenant users and personal contacts without duplicate emails.
- **Files and guest sharing:** internal, S3/MinIO and OAuth-provider files;
  expiring view/edit share links with SHA-256 token hashes, revocation,
  expiry, access counts and a public `/files/s/<token>` page. Edit links can
  replace the file through the page; S3 deletion removes the object so a later
  sync does not re-import it. Web uploads and share replacements are scanned by
  ClamAV INSTREAM when configured, with fail-closed scanner errors; an EICAR
  upload is rejected with `422 ATTACHMENT_INFECTED` and a clean upload succeeds.
- PWA/service worker, responsive layouts and mobile browser smoke.

### Compose, notifications, identities, forwarding and AI (0.7–0.8)

- **Rich composer:** contenteditable HTML editor with font family, font size,
  headings, emphasis, lists, links, quotes, code, alignment, text/highlight
  color, undo/redo and a per-message HTML/Plain switch. Server-side bluemonday
  allowlist sanitization includes bounded `font-family`/`font-size` values; HTML
  and plain fallback are stored, SMTP MIME is multipart/alternative, and test
  delivery retained `<strong>`/color while stripping a `<script>` and `onclick`.
- **Compose behavior:** default wide (2×) centered window, optional
  standard/full width and center/bottom-left/bottom-right placement, real From
  addresses, radio priority, drag-and-drop/Files attachment picker and a
  multi-file upload path. Closing a draft persists `windowMode=closed`; reload
  restores open/minimized drafts but not deliberately closed ones.
- **Notifications:** live center derived from unread INBOX mail, scheduled sends,
  upcoming events, recent security audit events and quota warnings. Repeat
  sign-ins from the same IP/action are collapsed; read/dismissed state persists
  per browser. Popover and full All/Unread/Mail/System window use no demo rows.
- **Identities:** primary/organization aliases plus custom addresses. External
  addresses are verified through a 48-hour hashed-token email link (POST
  confirmation); internal addresses require an owned alias. Verified identities
  appear in the Settings default From and compose From lists.
- **Forwarding:** permanent or DD/MM/YYYY date range, keep-copy or discard,
  policy checked and compiled to Sieve. A dated forwarding script with
  `date`+`relational` extensions was accepted by Dovecot ManageSieve.
- **App passwords:** permanent/7/30/90/365-day/custom expiry, soft revocation
  history, UI redesign and immediate DAV enforcement. An expired token returned
  401 while an active token returned 207. The current-password prompt accepts
  the mapped staging demo password in demo mode as well as the account hash,
  fixing alias sign-in re-authentication without weakening non-demo accounts.
- **AI assistant:** admin-configurable OpenAI-compatible/Anthropic endpoint,
  model, sealed key, tokens/temperature/timeout/system prompt. Compose 8 styles,
  3 lengths, creativity and 36 languages. Reader AI is now strictly manual:
  opening or hovering mail never generates a result. The split **AI** button
  (command bar on desktop, reader toolbar on mobile) offers **Summarise this
  email** over up to eight recent conversation messages, and **Translate Email**
  in the user's language. **Reply with AI** opens a prefilled reply composer and
  drafts from up to ten recent messages. Results are cached per user/language
  with a thread-aware cache version.
- **Reply all:** the button, using the double-arrow reply-all glyph, appears only
  when the replied message has additional To/Cc recipients; those addresses are
  populated into the compose Cc field, excluding the sender, the signed-in
  account and its aliases.
- **Minimized compose:** the dock tab is a high-contrast Outlook-blue gradient
  with white text and a two-second arrival animation plus a toast, so the
  minimize action is unmistakable.
- **Inbox view:** server-backed `grouped` (default) or `individual` row view in
  Settings → Mail & reading. Individual mode opens the selected message and
  preserves one row per message.
- **Boot splash:** animated envelope covers session restore and fades only after
  bootstrap. Interactive sign-in now holds the "Opening your workspace" screen
  for five seconds before revealing the inbox; a delayed reveal was verified at
  approximately 5.7 seconds including the fade.

### Protocols and credentials

- **CalDAV:** `/.well-known/caldav`, principal discovery,
  `calendar-home-set`, default calendar collection, PROPFIND, calendar-query
  and multiget REPORT, GET/PUT/DELETE, ETags, iCalendar parsing/generation,
  all-day events, attendees, recurrence rule pass-through and sync tokens.
- **CardDAV:** `/.well-known/carddav`, `addressbook-home-set`, default address
  book, PROPFIND, addressbook-query/multiget REPORT, GET/PUT/DELETE, ETags and
  vCard 3.0 parsing/generation.
- **App passwords:** `/api/security/app-passwords` create/list/revoke; 192-bit
  random tokens shown once, stored as SHA-256 hashes; DAV Basic auth uses them.
  Accounts with TOTP or a passkey cannot use the interactive password for DAV,
  so the second factor is not silently bypassed. Password changes invalidate
  cached DAV password checks, and failed Basic-auth attempts are rate-limited
  per client IP with successful authentication resetting the counter.
- Protocol smoke covers discovery plus event/contact create, read, update,
  report and delete; the reusable client is `tools/dav-smoke.py`.

### DNS, MTA-STS and storage

- **Real per-domain DKIM:** the root provisioner generates/reuses RSA keys,
  stores the actual public `dkim_record`, writes the Rspamd selector map and
  reloads Rspamd. Verification requires the published `p=` to match the active
  key exactly.
- **MTA-STS:** per-domain `none`/`testing`/`enforce` mode and max-age;
  `_mta-sts` TXT record generation; public
  `/.well-known/mta-sts.txt` policy endpoint selected by Host; verification of
  TXT id, HTTPS policy body, mode, max-age and MX. The verifier fetches only
  public IPs (no loopback/private/link-local/CGNAT) and does not follow
  redirects or use an environment proxy. Admin domain records show the TXT
  record and the policy document; the mode/max-age controls are live.
- **S3/MinIO:** AWS SDK v2, optional path-style, head-bucket check, upload,
  download, delete and prefix sync. Live-tested against a staging
  S3-compatible service: connect, upload, sync, download, delete and
  delete-does-not-reappear all pass.
- **OAuth connectors:** Google Drive, Dropbox and OneDrive authorization,
  state rows, callback, token exchange/refresh, account lookup, upload,
  download and provider sync. Tokens are AES-GCM sealed at rest. Paths are
  implemented but cannot be live-validated without real provider apps.

### Policy presentation

- `mail.externalTag=false` hides the external banner/chip, removes “external”
  wording from the trust line and details, and disables the user warning
  toggle. Internal mail is unaffected.
- `directory.externalDirectory=false` hides personal contacts outside the
  tenant's mail domains from directory/autocomplete for members; enabling it
  restores them. Admins retain their directory override.
- `directory.profileEditing=false` makes `/api/profile` read-only for members
  and returns 403 for profile writes; enabling it restores self-service.
- `directory.autoAddExternal=true` adds external recipients of sent mail as
  personal contacts (source `auto`) without duplicating existing contacts.
- Policy values are stored in `tenants.settings`; bootstrap exposes the
  frontend-safe subset and updates apply immediately.

### Verification evidence

- `artifacts/mail-normalization-smoke.txt` — normalized row counts, migrated
  app-state keys absent, CRUD/draft/outbox/undo persistence across restart.
- `artifacts/server-search-smoke.txt` — FTS free text and operator queries.
- `artifacts/imap-idle-smoke.txt` — IDLE new-mail import, flag refresh and
  expunge with a deliberately slow 30-second poll, plus keepalive handling.
- `artifacts/dav-smoke.txt` — discovery and CalDAV/CardDAV CRUD.
- `artifacts/large-mailbox-sync-smoke.txt` — 1,100-message import, incremental
  5-message pass, deletion/flag paths covered by unit tests.
- `artifacts/workspace-ui-smoke.txt` and `workspace-ui-smoke.png` — calendar
  create/edit/delete, contact create/edit/delete, profile edit, app-password
  create/revoke.
- `artifacts/policy-enforcement-smoke.txt` — member directory/profile policy
  enforcement.
- `artifacts/s3-delete-smoke.txt` — S3 upload, delete and re-sync check.
- `artifacts/mta-sts-smoke.txt` — policy endpoint, admin records and verifier
  unit tests.
- `artifacts/upload-clamav-smoke.txt` — EICAR rejection and clean upload through
  clamd INSTREAM.
- `artifacts/compose-0.7-smoke.txt` — consolidated notifications, composer,
  priority, attach, size/position, identities, forwarding, app-password and
  boot-splash results.
- `artifacts/compose-notifications-smoke.png` — browser UI run for the same
  surfaces.
- `artifacts/ai-assistant-smoke.txt` — admin/provider configuration, user
  compose/summary/translation/caching and AI-disabled behavior using a local
  mock; provider stopped and settings disabled afterward.
- `artifacts/notification-center-deduped.png`, `notification-popover.png`,
  `settings-compose.png`, `settings-identities.png`,
  `settings-app-passwords.png` — 0.7 UI surfaces.
- `artifacts/ai-compose-reader-smoke.png` and managed AI mock run — compose
  generation, reader summary/translation and cache from the 0.7 phase.
- `artifacts/ai-reading-compose-0.8-smoke.txt` and
  `ai-reading-compose-0.8.png` — 0.8 five-second splash, manual-only AI menu,
  summary/translation, Reply with AI, font formatting persisted through the
  server sanitizer, Outlook-blue minimized dock tab and grouped/individual view.
  `ai-provider-live-smoke.txt` records the real tenant DeepSeek summary/reply
  calls; `app-password-demo-smoke.txt` records the alias-password regression.
- `artifacts/ai-commandbar-menu-0.8.png`, `ai-reader-toolbar-menu-0.8.png`,
  `ai-reader-menu-mobile-0.8.png`, `compose-font-toolbar-0.8.png`,
  `minimized-dock-outlook-blue-0.8.png`, `reply-all-0.8.png`,
  `settings-inbox-view-0.8.png` and `boot-splash-0.8.png` — 0.8 UI evidence.
- `artifacts/admin-ai-smoke.png` — admin AI form (toggle, provider, endpoint,
  model, key, test/save).
- `artifacts/compose-attachment-smoke.png`, `app-password-ui-smoke.png` —
  drag-drop attachment upload and app-password create/revoke history.
- Existing mail/admin evidence: `go-api-smoke.txt`, `go-ui-smoke.txt`,
  `go-live-mail-smoke.txt`, `mail-policy-smoke.txt`,
  `password-expiry-smoke.txt`, `group-delivery-smoke.txt`, `dkim-smoke.txt`,
  `dkim-signing-smoke.txt`, `admin-*-smoke.txt`.
- `go test ./...` and `go vet ./...` pass; `/api/health` reports
  `0.9.0-staging` with PostgreSQL and Redis healthy.

### System Control 0.10.0-control

- Separate service `alt8mail-control` on `https://130.94.46.22:8808`, its own
  `control` schema and `alt8mail_control` role, sessions, CSRF secret, TOTP,
  RBAC groups, audit, API keys and webhooks. `88008` is not a valid TCP port;
  the deployed port is 8808.
- Live functionality: domain/DNS/DKIM management, mailbox provisioning and
  password probes, aliases/catch-alls, Sieve, certbot issue/dry-run and
  self-signed inventory, SMTP tests, queue control, relay/outbound routing,
  deliverability, DNSBL/suppressions, listener editing, Postfix config editor,
  systemd actions, mail-log/journald ingestion, imports, backups/restore, cron,
  server/time/network/UFW, PostgreSQL status/provision/migration, access
  control, API v1, webhooks and notification handles.
- `/healthz` returns `0.10.0-control`; diagnostics report 0 failures and 0
  warnings. `control-api-smoke.mjs` passed 21/21 checks, `control-ui-smoke.mjs`
  rendered 32/32 routes with no page errors, `control-ui-interactions.mjs`
  passed 28/28 checks, and `control-ui-writes.mjs` passed 7/7 UI writes.
- Config sync is idempotent (`filesChanged=0`, `warnings=0`). Certbot dry-run
  succeeds quickly, rolling caps defer with a real reason, journald ingestion
  and mbox/Maildir imports succeed, Postfix-only restore succeeds, and
  self-signed inventory/pruning was verified.
- Install, replication, API, security and limitations:
  [`SYSTEM_CONTROL.md`](SYSTEM_CONTROL.md).


### Webmail 0.9 and System Control 0.10 feature phase (2026-10-29)

- Webmail pin/unpin persists in `mail_messages.pinned`; individual reading view
  hides conversation-only reply/forward and expand/collapse controls; compose
  minimization is server-persisted and survives reload, logout and a fresh
  browser profile; folder/settings/message hash routes restore the exact page;
  the About page is Alt8Mail-branded and publishes live IMAP/POP3/SMTP client
  setup. `webmail-0.9-smoke.mjs` passed 39 checks with 0 page errors.
- Mailboxes paginate at 100 rows with 100/150/250/500 options and sort by
  address, name, status, quota use, storage, last sign-in or creation while
  reporting total/pages and aggregate counts.
- TLS certificate detail no longer fails; certificates can be imported with
  pair/expiry validation and assigned to nginx, Postfix or Dovecot with native
  validation, reload and rollback. Postfix key access uses ACLs and directory
  search permission, with a certbot deploy hook. A live swap test passed 14
  checks and restored the original nginx and Postfix paths.
- Infrastructure → Software updates and Infrastructure → Terminal are live.
  The terminal uses the detected sshd port (never assumed 22), one-time tickets,
  origin-checked WebSockets and audited key/password SSH authentication, with
  password auth restricted to loopback and failed SSH credentials kept separate
  from the panel session. Update
  jobs run in a transient systemd unit outside the control read-only sandbox;
  a no-op bulk apt job verified the full job/output/exit path.
- Dashboard Host health shows Postfix sending-IP deliverability with pulsing
  green clear/red listed and amber unverified states. Spamhaus resolver-policy
  replies are not misreported as listings.
- Verification: `control-0.10-smoke.mjs` passed 43 checks,
  `control-terminal-smoke.mjs` passed 18 checks, `go test ./...` passes, and all
  five core services remain active. The tenant AI configuration was preserved.

### External assessment and 1,000-user soak (2026-10-29)

- External application probes: **102 checks, 0 findings after remediation**;
  full TCP scan exposed only 22, 80, 443, 993, 995, 4190 and 8808. TLS 1.0/1.1
  were rejected; IMAPS/POP3S and ManageSieve authenticated as expected.
- Fixed during the assessment: forged `X-Forwarded-For` bypassing login limits
  and audit attribution, read-only panel accounts creating outbound
  notification callbacks, missing HSTS, duplicate admin security headers, and
  password-capable root SSH. Residual staging risks: self-signed control/mail
  certificates, unauthenticated `/metrics`, webmail/admin SameSite=Lax cookies,
  and SMTP ports filtered from the test vantage.
- **1,000 distinct users**: 1,000/1,000 logins, 0 failures; 92,278 requests with
  0 HTTP 5xx (3 transport errors, 0.00325%); p50 15.1 ms, p95 47.6 ms, p99
  105.7 ms. 1,022 IMAP connections hibernated in one process; host load1
  averaged 2.23 and available memory averaged 468 MB; all 202 admin/control
  health probes returned 200.
- The soak's 1,000-recipient mail seed exposed **P-12 (critical)**: a shared
  Message-ID collapsed recipients into one global `mail_messages` row. Ids are
  now user/folder/Message-ID scoped; the three-recipient fan-out retest produced
  three rows with three distinct owners. Test users and Maildirs were removed.
- Full report: [`PENTEST_REPORT_2026-10-29.md`](PENTEST_REPORT_2026-10-29.md);
  scope: [`PENTEST_SCOPE.md`](PENTEST_SCOPE.md).

## Partial / constrained

- **Public DNS and MTA-STS:** staging uses the reserved `a8m.test`, which
  has no public DNS. The admin screen shows the real records, the policy
  endpoint answers on the correct Host, and unit tests cover the verifier, but
  ownership/MX/SPF/DKIM/DMARC/MTA-STS cannot pass public verification until a
  customer delegates a real domain and adds a trusted certificate for
  `mta-sts.<domain>`.
- **Provider OAuth:** code paths exist for Google, Dropbox and OneDrive, but no
  live provider app credentials are configured on staging. S3 is the only
  connector validated end to end.
- **CalDAV/CardDAV scope:** one default calendar and one default address book
  per user; no VTODO/VJOURNAL, multiple collections, sharing/ACLs or iTIP
  scheduling (attendees are stored but invitation replies are not processed).
  Third-party clients should be smoke-tested before rollout.
- **Mail storage scale:** messages, attachments, outbox and undo are normalized
  and search is index-backed. Bootstrap still returns the whole mailbox in one
  response and keeps it in browser memory, so extreme mailboxes need
  pagination/windowed bootstrap before very large single-user deployments.
  IDLE watches INBOX; flag/move changes in other folders are reconciled by the
  periodic full scan or `POST /api/mail/sync`. Search indexes subjects and
  participants with `simple` and bodies with `english`, so stemming is still
  English-centric, and the API caps a result set at 500.
- **AI assistant:** provider plumbing, caching, admin configuration, compose
  controls and reader summaries/translation are implemented. Staging has a
  tenant-configured DeepSeek OpenAI-compatible provider and the summary,
  Reply with AI and translated reader flows were exercised against it. AI output
  is untrusted text; email content is treated as data by the prompts, but
  summaries are not a substitute for reading legal/security-sensitive mail. The
  endpoint is admin-configured and therefore an outbound SSRF surface limited to trusted
  `policies.manage` holders.
- **External identities:** verification proves control of the address, but
  deliverability still depends on that domain's SPF/DKIM alignment and
  reputation; the UI warns against using an address the domain has not
  authorized Alt8Mail to send for.
- **Notifications:** read/dismissed state is per-browser localStorage and does
  not sync across devices. Content is derived live; there is no server push
  channel for non-mail events beyond the existing browser poll/IDLE mail path.
- **Rich editor:** formatting toolbar covers common email styles but does not
  embed inline images; images and documents travel as MIME attachments.
- **Guest shares:** links are bearer tokens scoped by expiry/access; there is
  no recipient email verification or guest account. The public edit link can
  replace the file (audited) but cannot modify metadata.
- **Directory policy:** `autoAddExternal` applies to outbound recipients only,
  not inbound senders. `externalDirectory` is implemented as external personal
  contact visibility, which is narrower than an inter-tenant directory.
- **App passwords:** manual revocation only; no automatic expiry UI or per-token
  scope. The stored hash is SHA-256 because the token is high-entropy, not an
  interactive password.
- **MTA-STS:** one MX host per domain, no TLS-RPT record generation and no
  policy version history. Customers must publish `_mta-sts` TXT and operate
  `mta-sts.<domain>` TLS themselves or through the provided nginx pattern.
- **Admin console:** still password/TOTP only for the console; WebAuthn and
  SSO/IdP federation are future work.
- **Scale and production validation:** the 1,000-user read/session soak and
  external operator assessment are complete, including the critical message-ID
  isolation fix. A bulk-mailbox/large-attachment soak, independent third-party
  penetration test, SIEM/alert shipping, PITR/off-site backup and production
  secret rotation are still outstanding. Backups exist and have been
  restore-drilled only at staging scale.

## Not built yet

1. Customer-owned production domain passing public MX/SPF/DKIM/DMARC/MTA-STS
   verification and external deliverability/reputation testing.
2. Live provider OAuth and AI validation with real Google/Dropbox/Microsoft and
   OpenAI/Anthropic/DeepSeek credentials.
3. External sending-identity deliverability testing and domain SPF/DKIM
   authorization guidance.
4. Multiple CalDAV calendars/address books, VTODO, sharing/ACLs, iTIP/iMIP
   scheduling and third-party client certification.
5. Paginated/windowed bootstrap and message-list fetch for extreme mailboxes;
   non-English search configurations.
6. Guest identity/email verification and named-recipient share access.
7. Admin-console WebAuthn/passkeys and SSO/IdP federation.
8. Billing/subscription lifecycle for the customer backend.
9. Bulk-mailbox/large-attachment scale test, independent third-party
   penetration test, SIEM/alert shipping, PITR/off-site backups and production
   secret rotation. The 1,000-user read/session soak has passed; its empty-to-one
   message workload is not a bulk-body benchmark.
10. TLS-RPT reporting and MTA-STS policy version history.
11. License and contribution guide for the intended open-source release;
    `SECURITY.md` now documents responsible disclosure.
12. System Control multi-host/HA replication, external IdP/SSO, hardware-backed
    admin MFA, SIEM forwarding and CA-issued panel TLS. The current plane is
    single-host and uses a self-signed bootstrap certificate; treat port 8808 as
    root-equivalent and restrict it to an operator network.

## Gate to the next phase

Do not mark the platform production-complete until:

- a customer-owned domain passes real ownership/MX/SPF/DKIM/DMARC and MTA-STS
  verification, and signed outbound mail validates externally;
- provider OAuth and the AI assistant are exercised with real provider
  credentials, including a spend-capped key and a documented data-processing
  agreement;
- an external sending identity completes verification and sends successfully
  through its domain's SPF/DKIM policy;
- DAV is tested with the target mail clients and its collection/iTIP limits are
  either implemented or accepted;
- bootstrap pagination/windowing is implemented or explicitly accepted for the
  customer's maximum mailbox size, and non-English search needs are assessed;
- admin SSO/passkey and production network controls are in place, and System
  Control is restricted to an operator network with a CA-issued panel
  certificate and named MFA-enrolled operators;
- the 1,000-user read/session soak (done), a bulk-mailbox/attachment scale
  test, backup/PITR, off-site control-plane backup and independent external
  security test have passed.

The current release is ready for continued staging evaluation, workspace use
and protocol testing, not for public production traffic.
