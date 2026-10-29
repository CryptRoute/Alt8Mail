# Meridian Platform — Build & Production Plan

Status: **active build — Phases 0–4 core live; customer/system administration
console live; Phase 5 workspace, protocol and mail-scale work live** · Target: open-source
enterprise webmail, 1,000-user staging baseline with room to scale · Frontend:
existing Aurora UI, preserved exactly · Backends: Go mail/system API and a
separate Go admin console.

**Live staging:** `https://130-94-46-22.sslip.io` (Let's Encrypt certificate) or
`https://130.94.46.22` (self-signed fallback) · mail domain `meridian.test`. The
root UI and `/api` are served by the Go system backend; the Node reference
service is stopped and disabled. Go mail
sync reads Dovecot over IMAP, actions write back to Dovecot and outbound mail is
delivered through Postfix submission with TLS and SASL. The mailbox contains
**no reference JSON demo messages**; tasks, files and calendar start empty.
The staging login alias is `avery.chen@meridian.example` / `meridian-demo`,
mapped to the canonical `avery.chen@meridian.test` account and gated by
`MERIDIAN_DEMO_MODE` (login only, never data). Settings persist in PostgreSQL,
rules/automatic replies deploy through ManageSieve, and real TOTP/WebAuthn
enrollment plus first-time enrollment policy are implemented in Go. The
enrollment policy is disabled per user in staging. A separate Go admin backend
at `/admin/` provides tenant/user management, RBAC, org units, groups, domains
and DNS verification, policy, audit, admin TOTP and system administration; see
[ADMIN.md](ADMIN.md). See
[OPERATIONS.md](OPERATIONS.md) for the exact build log, commands, secrets
locations and test evidence. [GO_API.md](GO_API.md) is the endpoint, mail
pipeline and transitional-storage reference.

## 1. Scope and boundaries

This plan covers the first delivery track requested:

1. The existing Aurora frontend, unchanged in layout and interaction.
2. The **system backend** that runs the platform API, authentication, mail
   integration, scheduling, tasks, files, calendar, directory, audit and admin
   operations.
3. Real infrastructure on the staging server: PostgreSQL, Redis, Dovecot,
   ManageSieve/Sieve, Postfix, Rspamd, ClamAV, nginx, Go and operating-system
   hardening.

The **customer/system administration console** is now implemented as a separate
Go service (`cmd/adminserver`) with its own sessions, RBAC, tenant context,
policy, audit and UI. It is ready to move to an admin subdomain. Billing and
external IdP/SSO remain later tracks; tenant boundaries are enforced throughout.

The target is "100% of the current Aurora template feature set" plus the
security and operational controls needed to actually run it. Deferred product
features such as OpenPGP, full shared-mailbox delegation and production
provider sync remain separate milestones; they are not silently claimed here.

## 2. Target architecture

```text
Browser (Aurora frontend, untouched)
        │ HTTPS
        ▼
nginx (TLS termination, static frontend, reverse proxy)
        │ 127.0.0.1:8080
        ▼
Go system backend / BFF ─────── PostgreSQL 15
        │                        users, aliases, mail metadata, tasks,
        │                        files, audit, schedules, sessions
        ├── Redis 7              sessions, rate limits, short-lived state,
        │                        durable/worker queue primitives
        ├── Dovecot              IMAP, Maildir, quota, LMTP, Sieve
        │    └── ManageSieve     user rules + vacation/automatic replies
        ├── Postfix              SMTP ingress/submission; Dovecot SASL/LMTP
        ├── Rspamd               SPF/DKIM/DMARC, spam/phishing scoring, DKIM signing
        ├── ClamAV               attachment/message malware scanning
        ├── Maildir / S3         message + attachment storage
        └── future provider APIs Microsoft Graph, Google, Dropbox, S3

No Prometheus/Grafana/Loki/Traefik in this phase. Operate with systemd,
journald, nginx logs, PostgreSQL logs, fail2ban and a small health endpoint.
Observability can be added without changing the application contract.
```

### Mail decisions

- **Dovecot is the mailbox authority.** The Go backend never parses raw
  untrusted MIME or implements SMTP/IMAP itself.
- **Postfix is the MTA.** Inbound mail is delivered to Dovecot LMTP. Submission
  uses Dovecot SASL and a dedicated submission port.
- **Rspamd** provides SPF/DKIM/DMARC evaluation and DKIM signing. **ClamAV**
  scans messages and attachments.
- **ManageSieve** is the only user-rules execution path. No arbitrary
  Python/JavaScript from messages is ever executed.
- Outbound relay policy and real DNS (MX, SPF, DKIM, DMARC, MTA-STS) are
  required before external mail is production-ready. On a temporary IP-only
  staging host, local delivery and internal test domains are used.

### Identity decisions

- The Go system backend provides the app session layer, Argon2id password
  hashing, CSRF, rate limits, real TOTP/WebAuthn/recovery factors and per-user
  first-time enrollment policy, so the existing Aurora login layout remains
  intact.
- Production identity can be moved behind an OIDC provider (Keycloak,
  Authentik or a customer IdP) while keeping the API contract.
- The reference Node template's demo code `123456` and simulated passkey branch
  remain development-only. They are not present in the Go staging login, which
  verifies TOTP and WebAuthn server-side.

## 3. Phased delivery

### Phase 0 — Access, baseline and reproducibility

- [x] Confirm SSH access to staging server.
- [x] Install dedicated ed25519 deploy key; verify key-based login.
- [x] Inventory OS, CPU, RAM, disk, time zone and listening ports.
- [x] Set UTC, install security updates, add swap, create service user.
- [x] Install fail2ban, ufw and unattended security updates.
- [x] Record every command and configuration in `docs/OPERATIONS.md`.

Acceptance: fresh-key SSH works, server baseline is documented, no package
installation has silently changed mail/HTTP exposure.

### Phase 1 — Infrastructure installation

- [x] PostgreSQL 15 with a dedicated `meridian` role/database, local-only
  access and conservative 4 GB-host tuning.
- [x] Redis 7 bound to localhost with a password/ACL and persistence policy.
- [x] Dovecot IMAP/LMTP/Sieve + ManageSieve with virtual mailboxes in
  PostgreSQL; Maildir storage; TLS-ready configuration.
- [x] Postfix with virtual domains, Dovecot SASL, LMTP delivery and dedicated
  submission service.
- [x] Rspamd + ClamAV with local scanning pipelines. EICAR rejection verified
  end-to-end through Postfix.
- [x] nginx reverse proxy, security headers, static frontend and API proxy.
- [x] Go toolchain from an official release; systemd unit for the backend.
- [x] UFW allow only SSH/HTTP/HTTPS/SMTP-submission/IMAPS/ManageSieve as
  required; never expose PostgreSQL or Redis publicly.
- [ ] Worker systemd unit and durable queue worker (Phase 3).

Acceptance: all services start cleanly, local SMTP→LMTP→Dovecot delivery passes,
IMAP login and ManageSieve connection pass, `nginx` serves the frontend and the
Go health endpoint.

### Phase 2 — Go system backend foundation

- [x] Repository layout: `cmd/server`, `cmd/admin`,
  `internal/{config,auth,httpapi,store}`, migrations, deploy notes.
- [x] PostgreSQL schema with tenant id, users, aliases, sessions, normalized
  messages/attachments/outbox/undo, tasks, files, providers, audit, security,
  contacts, calendar and Sieve profiles.
- [x] Argon2id passwords, opaque sessions in PostgreSQL, CSRF header
  validation, secure cookie configuration and login throttling. Real TOTP,
  hashed recovery codes, trusted devices and WebAuthn are implemented; the
  first-time enrollment policy is stored per user.
- [x] Session/bootstrap/preferences/auth APIs matching the current frontend,
  with identity, aliases and directory loaded from PostgreSQL and mail/workspace
  data in normalized tables (no demo mailbox rows).
- [x] Go health/readiness endpoints and structured JSON logs.
- [x] Full feature API: messages, threads/search, read/star/flag, bulk actions
  and undo, drafts, send/outbox, scheduled edit/send-now/cancel, Tasks, Files,
  storage connectors, attachment upload/content, Sieve/vacation, audit/CSV,
  quota and outbound caps.
- [x] Frontend served unchanged by nginx; root `/api` switched to Go after API
  and browser smoke tests passed (`tools/go-api-smoke.py`,
  `tools/go-ui-smoke.mjs`).
- [x] Durable outbox worker (`StartWorkers`) with restart-safe scheduled sends.
- [x] IMAP sync worker for INBOX/Sent/Drafts/Archive/Junk/Trash; attachment
  ingest, MIME parsing, authentication-results extraction, flag reconciliation,
  missing-message removal and message threading.
- [x] Folder-scoped Message-ID matching so self-sends keep distinct Sent/INBOX
  copies in one thread; 15-second UID-based INBOX check and in-place browser
  list/reader refresh with desktop/in-app new-mail notifications.
- [x] Two-way read/star/flag/move/archive/spam/delete propagation with undo,
  deferred expunge and IMAP draft persistence.
- [x] SMTP submission worker with STARTTLS, Dovecot SASL, Postfix/Rspamd/ClamAV
  delivery and Sent-folder append.
- [x] Every frontend settings tab is server-backed: `users.preferences` for
  appearance, reading, notifications and regional values; ManageSieve for rules
  and automatic replies; real tables for TOTP/WebAuthn/recovery, sessions and
  first-time enrollment policy. `POST /api/preferences/reset` restores defaults.

Acceptance evidence: the Go API smoke suite and browser smoke suite pass against
the real mailbox (`artifacts/go-api-smoke.txt`, `artifacts/go-ui-smoke.txt`);
actual Dovecot mail is imported, flags/moves/deletes persist to Dovecot, drafts
live in Dovecot, and outbound mail reaches local recipients through Postfix.
`tools/go-live-mail-smoke.mjs` sends to the signed-in user and verifies the
message appears in the open INBOX without a reload, with a notification. The
original 24-test Node E2E suite remains the reference-template regression suite;
it is not run unchanged against Go because production-like state intentionally
contains no demo fixtures. A Go-targeted expanded browser suite remains a Phase 6
task.

### Phase 3 — Mail core

- [x] IMAP adapter for all standard folders, MIME bodies, attachments, flags,
  authentication results, message threading, draft append/replace and
  UID/Message-ID reconciliation.
- [x] SMTP submission through Postfix 587 with STARTTLS, Dovecot SASL,
  Rspamd/ClamAV, durable scheduled sends, send-now, edit, cancel and undo-send.
- [x] Undo move/delete with server-side snapshots and quota refresh.
- [x] Attachment upload/download through controlled storage with MIME sniffing,
  size limits and preview disposition; inbound IMAP attachments are ingested
  into the same store.
- [x] Sender trust panel from stored SPF/DKIM/DMARC results; Rspamd headers are
  parsed on IMAP ingest.
- [~] Quota and outbound caps: outbound events and queued sends are live;
  mailbox quota is calculated from state + baseline rather than IMAP
  GETQUOTAROOT.
- [~] Audit events for all mutations are stored in the per-user state document;
  the normalized `audit_log` table still receives only identity events.
- [x] Two-way flag/move/delete propagation back to IMAP, including deferred
  permanent deletion and undo.
- [x] Incremental large-mailbox sync with full UID reconciliation, body fetch
  only for unseen UIDs, budgeted newest-first import, `pending` backfill,
  periodic flag reconciliation and UIDVALIDITY reset.
- [x] Mail, attachments, outbox and undo moved from per-user JSONB to
  normalized tables; handlers persist row diffs per mailbox.
- [x] Indexed PostgreSQL full-text search with `websearch_to_tsquery` and GIN,
  including all UI operators; the frontend calls `/api/search` with a 220 ms
  debounce.
- [x] IMAP IDLE push per active mailbox for new mail, flag changes and
  expunges, with reconnect/backoff and polling fallback.
- [x] Rich HTML/plain compose editor with server-side allowlist sanitization,
  HTML MIME delivery, radio priority, actual sender identity, attachment picker,
  drag-and-drop uploads and saved window size/position.
- [x] Closed-draft persistence (`windowMode=closed`) while open/minimized drafts
  restore after reload.
- [x] Live notification center for unread mail, scheduled sends, calendar,
  security audit and quota, with read state and compose-style full window.
- [x] Self-service sending identities with external email verification and
  organization-alias checks; live Default From identity.
- [x] Permanent/dated Sieve mail forwarding with keep-copy and policy checks;
  corrected `require [...]` syntax and ManageSieve literal error reporting.
- [x] Expiring app passwords with soft-revoked history and immediate DAV
  enforcement; redesigned Settings UI.
- [x] Tenant AI assistant admin configuration plus compose generation,
  conversation summaries/translation, 36 languages and a manual split AI menu.
- [x] Reply with AI from the recent email trail; CC-aware Reply all recipient
  handling.
- [x] Font family/size controls in the HTML composer with server allowlisting.
- [x] Outlook-blue minimized compose dock tab with an arrival animation.
- [x] Server-backed Group by conversation / Individual inbox view.
- [x] Branded animated boot splash with a five-second post-authentication hold
  before showing the inbox, removing the login flash.
- [~] APPEND/UIDPLUS edge cases and paginated bootstrap for extreme mailboxes
  remain.

Acceptance: send, schedule, edit, send-now, cancel, draft restore, IMAP folder
changes and undo all pass integration tests against Dovecot/Postfix.

### Phase 4 — Security and identity hardening

- [x] Real TOTP enrollment and passkey/WebAuthn registration and verification
  in Go, including first-time enrollment and a per-user enable/disable policy.
- [x] Recovery codes hashed and single-use; TOTP secrets sealed with AES-GCM;
  sign-in challenges single-use; browser sessions revocable from Account.
- [ ] Step-up authentication for recovery codes, alias changes and admin actions.
- [ ] CSP nonces, Trusted Types where supported, secure headers, no inline
  script execution.
- [ ] Server-side HTML sanitization and sandboxed rendering for message bodies.
- [ ] Remote-image blocking/proxy, link protection, attachment isolation and
  archive/decompression limits.
- [ ] SSRF controls for provider/link/image fetchers; upload authorization
  checks; tenant isolation.
- [ ] Fail2ban jails for SSH/SMTP SASL/Dovecot/ManageSieve; rate-limit every
  sensitive API.
- [ ] Remove demo credentials/codes and document production secret rotation.

Acceptance: security test checklist, auth bypass/IDOR/CSRF/XSS/SSRF tests,
attachment handling tests and an external penetration-test pass.

Authentication evidence: `tools/go-auth-smoke.mjs` exercises real TOTP
enrollment, TOTP login, single-use recovery codes, first-time enrollment policy,
Chrome virtual-authenticator WebAuthn registration and a real passkey assertion.
It cleans all factors and resets the policy to false. See
`artifacts/go-auth-smoke.txt` and `artifacts/go-enrollment-required.png`.
Remaining Phase 4 items (step-up, CSP nonce/Trusted Types, server-side body
sanitization, remote-image/link controls, fail2ban jails, demo-alias removal)
are still open.

### Phase 5 — Workspace features

- [x] Tasks backed by normalized PostgreSQL tables and assignable to
  directory users.
- [x] Files API and normalized file/share tables; internal, S3/MinIO and OAuth
  provider storage, public guest share pages, and signed server-side content
  delivery. S3 is live-tested; provider OAuth needs real app credentials.
- [x] Search is server-side PostgreSQL FTS (GIN + `websearch_to_tsquery`);
  the frontend debounces and renders server results.
- [x] Calendar, contact and directory writes are table-backed, with UI CRUD and
  CalDAV/CardDAV discovery/ETag/sync-token protocol support.
- [x] Profile editing, directory external-contact visibility, external-tag
  presentation and auto-add-external policies are enforced.
- [x] MTA-STS policy state, records, public policy endpoint and verifier.
- [x] Revocable app passwords for DAV clients, with MFA-aware authentication.
- [x] Scheduled center, automatic replies, Sieve rules and audit UI are wired to
  Go API state.
- [x] PWA/service-worker cache versioning and offline-safe behavior.
- [~] Mobile browser smoke passed; accessibility regression suite outstanding.
- [x] Customer/system administration backend and UI: tenant lifecycle, users,
  aliases, groups, org units, custom RBAC roles, domains and real DNS
  verification, tenant policy, audit/CSV, admin TOTP, system administrators,
  tenant suspension and separate admin sessions. See [ADMIN.md](ADMIN.md).
- [x] Postfix group-address expansion, per-user mailbox workers, explicit
  Maildir provisioning, password history/expiry, and enforcement adapters for
  IMAP/POP/SMTP, forwarding, retention, quarantine and external sharing.
- [x] Per-domain DKIM key provisioning, Rspamd selector map, real public-key
  records in the admin DNS table, and exact `p=`/MX/DMARC verification (unit
  and live signing tests).
- [~] Admin-console WebAuthn/SSO remains; directory external-contact
  visibility, profile editing, external-tag presentation and public guest share
  pages are implemented. Named-recipient guest identity remains open.

### Phase 6 — Production readiness

- [~] Health-endpoint load smoke passed at ~4.8k req/s with zero failures;
  1,000-user authenticated mail soak test still outstanding.
- [x] Local encrypted backup/restore of PostgreSQL, Dovecot Maildir, preview
  data, secrets and configuration. Off-site copy and PITR still outstanding.
- [ ] Log rotation and retention, SIEM export hook, alerting thresholds.
- [~] Public staging hostname `130-94-46-22.sslip.io` with a Let's Encrypt
  certificate and automatic renewal. MTA-STS records/policy/verifier are
  implemented; customer-owned production domain, public DNS/MX/SPF/DKIM/DMARC
  validation and a trusted `mta-sts.<domain>` certificate are still outstanding.
- [ ] Runbooks: restore, cert renewal, mail queue, abuse response, incident.
- [ ] CI: unit, API integration, browser E2E, migrations, reproducible build,
  dependency/SBOM checks.
- [ ] Open-source license, security disclosure policy and contribution guide.

## 4. 1,000-user baseline

The current staging host is a 2 vCPU / 4 GB KVM machine with 50 GB disk. That is
sufficient for development, integration and a light staging load — not for
1,000 concurrent users or a full production mail store.

Initial staging targets:

- Keep PostgreSQL `shared_buffers` around 256 MB, `work_mem` small, max
  connections bounded behind a pool.
- Keep Redis local and small; no large message bodies in Redis.
- Dovecot Maildir on the local disk for staging; production should use a
  dedicated volume or object-backed mail store.
- ClamAV is the largest memory consumer; use one scanner process, signature
  update limits and a memory cap.
- Add 4 GB swap to prevent OOM kills during package installation or AV scans.
- For production, split database, mail store, search and workers onto separate
  hosts or managed services as load grows.

## 5. Security invariants

1. No scripts, macros or active content from email are executed by the system.
2. Any user-authored rule runs only through Sieve in Dovecot, with validation
   and quotas.
3. Raw SMTP/IMAP/MIME parsing stays in battle-tested mail daemons.
4. All mutations are tenant-scoped and authz-checked; no IDOR.
5. Every deliverability/security setting is visible in configuration, never
   hidden in one-off shell history.
6. Secrets live in root-readable environment files or a secret manager; they
   are never committed or logged.
7. Demo-only auth shortcuts and credentials are blocked outside development.
8. The frontend layout is preserved; security work adds behavior, not a visual
   rewrite.

## 6. Documentation rule

Each completed phase updates:

- this plan (checkbox + decisions),
- `docs/OPERATIONS.md` (commands, service topology, backup/restore),
- `docs/INTEGRATION.md` (production adapters),
- `README.md` (feature status and test commands),
- test evidence under `artifacts/`.

All staging commands are captured in the run log so a new operator can
replicate the environment from a clean Debian 12 host.

## 7. Immediate next tasks

1. Paginated/windowed bootstrap for extreme mailboxes, APPEND/UIDPLUS
   edge-case coverage and non-English search configurations.
2. Admin-console WebAuthn and SSO/IdP federation; step-up authentication and
   remaining CSP/HTML-sanitization/rate-limit hardening.
3. Customer-owned public DNS verification and live MTA-STS validation with a
   trusted `mta-sts.<domain>` certificate; external deliverability testing.
4. Real provider OAuth and AI credentials (spend-capped, documented data
   processing), external-identity SPF/DKIM deliverability testing, and
   third-party CalDAV/CardDAV client certification.
5. Full 1,000-user soak test, external penetration test, SIEM/alerting,
   PITR/off-site backup and production secret rotation.
