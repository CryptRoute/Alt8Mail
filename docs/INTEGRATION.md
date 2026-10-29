# Integration blueprint

> **Live Go status (2026-10-28):** most adapters described here are now
> implemented in the Go system backend: Dovecot IMAP/LMTP/drafts/actions,
> Postfix submission with Rspamd/ClamAV, ManageSieve rules and vacation,
> PostgreSQL identity/settings/sessions, real TOTP/WebAuthn/recovery,
> per-user first-time enrollment policy, normalized workspace tables, guest
> shares, CalDAV/CardDAV, incremental large-mailbox sync, MTA-STS, S3 and
> provider OAuth flows, and a separate customer/system admin console with RBAC
> and audit ([ADMIN.md](ADMIN.md)). This document remains the adapter
> blueprint and records production hardening that is still outstanding; see
> [`GO_API.md`](GO_API.md) for the live API and [`OPERATIONS.md`](OPERATIONS.md)
> for the deployed topology. Statements below about the Node demo apply to the
> standalone reference template unless marked otherwise.

This template deliberately keeps every integration point small and named. The
UI calls the same API whether the data comes from the demo JSON store or from
your mail stack. This document maps the demo implementation to the real
components referenced in the feature brief (`internal/store`, `internal/dkim`,
postfwd, Dovecot, Sieve, auditlog, directory, CalDAV).

```text
Browser (Aurora UI) ──JSON──> server/app.mjs ──adapters──> workspace infrastructure
                               │
                               ├── internal/store        identities, aliases, prefs
                               ├── identity provider     WebAuthn / TOTP / recovery
                               ├── Dovecot               mailboxes, quota, drafts
                               ├── postfwd / MTA         send + scheduled sends + caps
                               ├── managesieve           rules + vacation
                               ├── internal/dkim + DNS   SPF/DKIM/DMARC trust
                               ├── object storage        attachments + Files shelf
                               ├── provider APIs         Dropbox / Drive / OneDrive / S3
                               ├── auditlog              security events
                               ├── CalDAV                calendar
                               └── directory             people / assignments
```

The demo implements all adapters against `data/db.json` so the full product can
be exercised end-to-end. Replace adapters incrementally; the API contract is
stable.

> **Staging status (2026-10-28):** the Go system backend under `backend/` serves
> the production-shaped API with real Dovecot data. It synchronizes INBOX, Sent,
> Drafts, Archive, Junk and Trash over IMAP, propagates flags/moves/deletes back
> to Dovecot, persists drafts in Dovecot Drafts and delivers outbound mail through
> Postfix submission. The live staging state is initialized without demo mailbox
> JSON. Remaining adapter work is tracked in `docs/PLATFORM_BUILD_PLAN.md`; the
> sections below remain the detailed contract and production-hardening
> blueprint.

---

## 1. Identity, aliases and sessions (`internal/store`)

**Demo:** `server/seed.mjs` creates a scrypt-hashed demo user and a `sessions`
Map in `server/app.mjs`. `/api/session` accepts password or demo SSO and sets
`a8m_sid` + `a8m_csrf` cookies.

**Production:**

1. Replace the credential check in `POST /api/session` with your SSO
   (OIDC/SAML) or `internal/store` password verification.
2. Keep the session cookie opaque. Store sessions in Redis/your session store,
   not a process Map. Keep the CSRF token per session.
3. Load the user and aliases from `internal/store`:
   - `user.email`, `name`, `initials`, `color`, `role`, `company`.
   - `aliases[]` with `{ id, email, label, kind: primary|alias|shared,
     canSend, reason }`. The UI filters `canSend` and shows read-only shared
     addresses separately.
4. Enforce alias send permission server-side on both draft save and send. The
   demo does this in `aliasForEmail()` / `POST /api/send`.
5. Remove `demo.passwordHint` and the demo seed from anything public.

**API shape:** `/api/bootstrap` returns `user` (without password) and
`aliases`; `/api/session` returns `{ user, csrf, expiresAt }`.

**Delegation:** to add shared mailboxes, extend `aliases` with delegation
metadata and add a mailbox selector to the folder pane. The send path already
validates the chosen identity; the missing piece is per-mailbox IMAP access
(separate folder namespaces and permissions).

**Preferences:** `GET/PATCH /api/preferences` stores locale, 12/24-hour time,
date format, IANA time zone, week start, working hours/days, default From,
signature, signature toggle and auto-advance. Validate time zones with
`Intl.DateTimeFormat` (the demo does), validate working hours end > start, and
authorize `defaultFrom` against sendable aliases. In production, cache these
with the user record and expose tenant defaults plus per-user overrides. Keep
the response shape `{ preferences, changed }` so the UI can update live.

**Scheduling:** every time value used for scheduled sends is a wall-clock time
in the user's preferred zone. The client converts it to an absolute UTC instant
with `zonedWallTimeToUtc()` before `POST /api/send`; the server stores ISO-8601
only. A tenant policy should decide whether to allow sends outside working
hours, in the past, or more than a year ahead (the demo enforces future-only,
≤1 year, and warns outside work hours).

---

## 1a. Two-factor authentication and passkeys

**Live Go status:** implemented, not a simulation. TOTP is RFC 6238 (SHA-1,
30 s, six digits, ±1 window); secrets are AES-256-GCM sealed; recovery codes are
hashed and marked single-use; WebAuthn uses `github.com/go-webauthn/webauthn`
with Redis ceremony state and PostgreSQL credentials; challenges and trusted
devices are in PostgreSQL; sessions are listed and revocable. The per-user
`users.enrollment_required` flag drives the first-time enrollment screen and is
disabled in staging. Remaining production work: tenant-wide MFA policy,
step-up authentication, KMS/HSM key wrapping and helpdesk recovery workflows.

**Node reference demo:** `/api/session` does not create a cookie when 2FA is
enabled. It returns a five-minute challenge (`challengeId`, methods, passkey list
and, in demo mode, code `123456`). `/api/session/2fa` verifies one of:

- `method: "passkey"` with a known passkey id — **simulated** in the demo;
- `method: "totp"` with a six-digit code checked by `server/twofactor.mjs`
  (RFC 6238 HMAC-SHA1, ±1 step);
- `method: "recovery"` with a single-use code.

Five failed attempts delete the challenge. Success creates the normal session,
optionally with a 30-day trusted-device TTL. Public user payloads expose only
method metadata and recovery-code counts; the TOTP secret and unused codes are
stripped by `publicUser()`.

**Production hardening (Go staging already covers items 1–3 in basic form):**

1. For the Node reference, replace the passkey branch with WebAuthn. Store
   credential id, public key,
   sign count, AAGUID, transports and user handle per credential. Call
   `navigator.credentials.get()` with a server-issued challenge, verify
   origin/RP id, signature and counter server-side, and reject cloned
   authenticators. Require a user-verifying flag for sign-in.
2. Keep TOTP secrets encrypted with a KMS/HSM key; store recovery codes hashed
   and mark them used atomically. Never return unused codes from a general
   bootstrap response; require re-authentication for the reveal endpoint.
3. Persist challenges in Redis (not a process Map) with single-use semantics,
   attempt counters and rate limits per account/IP. TOTP codes must not be
   replayable inside their time step.
4. Treat trusted devices as revocable credentials, not just long sessions.
   Surface them in Security settings with name, last use and a revoke action.
5. Support step-up authentication for sensitive actions (recovery-code reveal,
   new passkey, send-as changes, mailbox delegation) even after sign-in.
6. Add tenant policy: require MFA for all users, require phishing-resistant
   passkeys for admins, set trusted-device lifetime, and allow recovery only
   through helpdesk identity proofing.
7. Remove the demo code path (`allowDemoTwoFactorCode`) and the prefilled login
   credentials before deployment.

---

## 2. Mailbox and threaded conversations (Dovecot)

**Demo:** messages live in `db.messages`; each has a `folder` and `threadId`.
`buildThreads()` in `public/search.mjs` groups a scoped list with all messages
so the reader can show sent replies too.

**Production:**

1. Back `GET /api/bootstrap` and `/api/messages` with IMAP/Search results from
   Dovecot (or your cached SQL store). Keep the message shape:
   ```js
   {
     id, threadId, folder,
     from: { name, email, initials, color },
     to: [{ name, email }], cc: [], bcc: [],
     subject, snippet, bodyHtml, bodyText,
     date: "ISO-8601", unread, starred, flagged, draft,
     attachments: [{ id, name, type, size, sizeBytes, previewable, contentUrl }],
     categories: [], classification: "Internal|Confidential|...",
     external, auth: { spf, dkim, dmarc, tls, ip, signer, firstContact, warnings },
     spamReport: null | { at, reason, note, source },
     event: null, sizeBytes,
     messageId, inReplyTo
   }
   ```
   Preserve `flagged` and `spamReport` through folder moves and undo snapshots.
   Production report routing should create a security-case record and feed the
   tenant phishing queue; the demo stores the report on the message and writes
   `message.report` to the audit log.
2. Derive `threadId` from `References` / `In-Reply-To` (fall back to the
   message id for a new thread). Do not group by normalized subject alone.
3. Keep `folder` on each message. Archive/delete in a folder view should target
   the messages in that folder; the demo does this in `actionMessageIds()` and
   `POST /api/messages/actions`. A conversation in the reader still shows the
   whole thread.
4. Paginate large folders. The current UI renders the full list; add a windowed
   loader (e.g. `visibleThreads` grows on scroll) and keep `state.viewScope`
   consistent. Thread grouping needs the messages in the visible window plus
   any referenced thread members.
5. For realtime new mail, add an SSE/WebSocket endpoint and call
   `refreshMailbox({ quiet: true })` on the client. The existing polling for the
   send queue is in `startOutboxPolling()` and is a good template.

**Threading contract:** the client expects all messages of a listed thread in
`state.messages`; if you paginate, fetch thread members on open.

---

## 3. Search (instant local now, Bleve/FTS5 later)

**Demo:** `MailIndex` in `public/search.mjs` builds an inverted index in the
browser after bootstrap, and the server uses the same module for
`GET /api/search`. Free-text terms use AND + prefix matching; operators narrow
the document set. The operator parser is shared, so tests and UI agree.

**Production options:**

- **Client-first (current):** stream a recent window of messages to the browser
  and build the index there. Instant, no round trip, but memory grows with the
  mailbox window.
- **Server-side:** keep `/api/search?q=` but replace `getIndex().search()` in
  `server/app.mjs` with Bleve or SQLite FTS5. Return `{ parsed, count, messages }`.
  The UI only needs `messages`; the parsed object drives the chips/error row.
- **Hybrid:** keep the client index for the loaded window for typing latency,
  and query the server for older mail after a short debounce. Merge by thread
  id. This is the recommended path at scale.
- **Index maintenance:** update on message create/move/delete, draft save and
  read/star changes. In the demo `invalidateIndex()` marks the server index
  dirty at every mutation. In production, use outbox/queue events or a search
  indexer worker.
- **Attachments:** index the names now. Extract file text in a sandboxed worker
  if you want attachment-body search; never run extraction in the request path.

**Operator parser:** `parseQuery()` supports quoted values and returns
`{ textTerms, from, to, cc, subject, body, filename, hasAttachment, before,
after, unread, starred, folder, larger, smaller, errors }`. Keep this contract
when moving to a server engine; the UI independently displays parsed tokens.
Invalid date/size values surface in `errors` and are ignored for filtering (the
typeahead behavior avoids flashing empty results while the user is still typing).

---

## 4. Drafts and resilient compose sessions

**Demo:** every compose window is persisted through `PUT /api/drafts/:id` on a
debounce (`flushDraft`) and on tab hide via `keepalive`. Draft documents carry
`toText/ccText/bccText`, `bodyText`, attachments metadata, `fromEmail`,
`threadId`, `inReplyTo`, `windowMode` and timestamps. On reload,
`restoreDraftSessions()` reopens the windows.

**Production:**

1. Use the draft id as your client key and map it to a Dovecot `Drafts` message
   (or a server draft record) via a stable `draftId` column/header
   (`X-Alt8Mail-Draft-Id`). IMAP `APPEND` with `\Draft` is usually sufficient.
2. Keep the autosave endpoint idempotent. The demo replaces the document; add
   `If-Match`/revision checks if multiple browser tabs can edit the same draft.
   On a 409, keep the local text and prompt for a merge or duplicate.
3. Store attachments separately from the JSON body. The demo now accepts
   base64 JSON uploads at `POST /api/attachments` (8 MB), sniffs the real MIME
   from magic bytes, writes bytes under `data/uploads/` and serves
   `/api/attachments/:id/content` with `inline` for previewable PDFs/images and
   `attachment` for everything else. For production:
   - accept multipart or presigned object storage, not base64 JSON;
   - virus-scan and content-disarm before release;
   - enforce per-user/attachment and mailbox quota limits;
   - keep `attachments[]` metadata in the draft and message;
   - serve user content from a separate origin or sandboxed response (the demo
     adds `nosniff` and a sandbox CSP for SVG);
   - never render HTML attachments inline without sanitization;
   - generate short-lived signed URLs instead of exposing opaque API paths.
4. On send, remove the draft only after the send is accepted into the queue.
   The demo does this transactionally in `POST /api/send`.
5. If you add collaborative editing, move the draft body to a CRDT/document
   service; the compose window API stays the same.

---

## 5. Sending, undo send and outbound caps

**Demo:** `/api/send` validates From alias, recipients and caps, removes the
draft, and pushes a queue item with `sendAt = now + hold`. A timer commits the
message into `Sent` (unless `saveCopy` is false), updates outbound usage and
writes audit events. `cancel` restores a draft from the queue snapshot.

**Production:**

1. **Queue:** use Redis (or your job queue) with a due-time index instead of
   in-memory timers. Persist enough to resume after restart. The demo resumes
   pending items on startup; do the same. Scheduled sends use the same queue
   with a much later `sendAt` and `scheduled: true`, so they survive restarts
   and are cancellable until their due time. For long schedules, consider a
   separate durable schedule table plus a short-horizon dispatch queue so a
   year-long schedule does not sit in the hot queue.
2. **Submission:** the queue worker calls your MTA/submission service. Make it
   idempotent with the queue item id as the message idempotency key.
3. **Undo:** cancellation is a queue delete before submission. Once submitted,
   return `409 SEND_ALREADY_COMMITTED` (the UI handles this) and, optionally,
   offer a "send a correction" flow.
4. **Sent copy:** append to the user's Sent mailbox after submission; store the
   IMAP UID mapping. The demo creates a message with `threadId` and
   `inReplyTo` so the conversation stays intact.
5. **Alias envelope:** use the selected alias as envelope/From and enforce
   permission server-side. Spoofing a From must not be possible with a custom
   request.
6. **Outbound caps:** check the same counter postfwd enforces, before enqueue.
   Count queued messages too, as the demo does. Expose usage through
   `/api/outbound` and let the status bar/security card poll it. Decide on the
   reset semantics (rolling vs calendar) and make the UI text match.
7. **Failure handling:** if delivery bounces or the MTA rejects, surface a
   per-message state in Sent/queue and write an audit entry. Do not silently
   drop.
8. **Recipients:** validate against your policy (max recipients, external
   domains, DLP). The demo parses `Name <email>` lists and rejects malformed
   addresses before queueing.
9. **Scheduled management:** the Scheduled center uses
   `PATCH /api/outbox/:id` (recipients, Cc/Bcc, subject, body, attachments,
   send time), `POST /api/outbox/:id/send-now`, and the existing cancel
   endpoint. In production:
   - use optimistic concurrency/versioning so an edit cannot race a dispatch;
   - reject edits after a "dispatching" state begins, not just after `sendAt`;
   - revalidate recipients, alias permissions, DLP and outbound caps on every
     edit;
   - make send-now idempotent with a per-item operation key and commit through
     the same MTA path as scheduled delivery;
   - audit `send.rescheduled` and `send.sent_now` with the original and new
     schedule, actor and changed fields;
   - cancel must restore the canonical draft atomically and never leave both a
     scheduled item and a draft behind.

---

## 6. Sender trust (`internal/dkim` + DNS)

**Demo:** each message has structured `auth` results seeded to represent the
receiving MTA's Authentication-Results. `/api/messages/:id/trust` formats the
panel; `POST` persists per-sender trust in `user.trustedSenders`. The UI shows
external warnings, failure chips and security details.

**Production:**

1. Parse the message's `Authentication-Results` header (and any ARC results) on
   ingest. Store `spf`, `dkim`, `dmarc`, `signer`, `ip`, `tls`, `warnings` and
   domain alignment. Never infer DMARC from SPF alone.
2. For a live "re-check", resolve `_dmarc.<from-domain>`, the selector TXT
   record for DKIM and the SPF record. Use a DNS client with strict timeouts,
   caching and no user-controlled resolver; treat DNS as untrusted input.
3. Cross-check the visible From domain, envelope sender and Reply-To. Add
   warnings for lookalike domains, first contact, urgency/credential patterns or
   Display Name spoofing. The demo seeds representative warnings.
4. Trust decisions belong to the user, not the sender. The reader already has a
   **Report email** flow with phishing/spam/malware/other reasons and an analyst
   note. In production, send that payload to the security case system (with
   message headers and attachments), move the message to Junk, and keep the
   `message.report` audit event. Add tenant allow/block lists and report-status
   feedback to the reporter.
5. In the reader, keep failed-auth messages visibly distinct even if the user
   marks a sender trusted; trust suppresses the banner, not the evidence.
6. If you render remote content, gate it behind a "load images" action and
   proxy through a privacy-preserving image proxy.

---

## 7. Sieve rules and vacation (managesieve)

**Demo:** `server/sieve.mjs` validates structured rules, generates a Sieve
script and returns it through `/api/sieve`. The UI edits the rule list,
enablement, ordering, action and vacation responder. `PUT /api/sieve` is the
only write path and increments a revision.

**Production:**

1. Decide where the structured rules live. Recommended: the webmail owns the
   structured JSON (database), and generates the Sieve script pushed via
   managesieve. Keep a revision counter and an "apply" result; offer a rollback
   if the server rejects the script.
2. Preserve admin-managed rules. Use a named block/comment marker and only
   replace the user-managed section, or push a separate include. The demo
   generates the whole script for clarity.
3. Check the extensions your Dovecot advertises (`managesieve` capability
   list) before emitting `body`, `date`, `vacation`, `imap4flags`,
   `fileinto`, `redirect`. The generator already chooses requires based on the
   rules; add a capability check before apply and surface unsupported
   extensions in the UI.
4. Validate destinations against real folder names (case and hierarchy). The
   demo restricts to `inbox/archive/spam/trash`; production should accept the
   user's folder list, reject reserved names, and quote safely.
5. Vacation: use the `vacation` extension with `:days` for rate limiting and
   ISO dates via `currentdate`. The Automatic replies settings panel writes the
   same model with separate internal and external subject/message branches; the
   external branch is emitted only when enabled and a domain is available.
   Handle time zones explicitly if you add them; the demo treats dates as
   calendar dates. Decide whether external replies may be sent at all under
   tenant policy.
6. Audit every rule/vacation change (who, revision, before/after summary). Do
   not log message bodies.
7. Optionally add a "preview what this rule matches" dry run using a cached
   mailbox sample; this is a strong enterprise differentiator and can reuse the
   search index.

**Script generation:** `conditionTest()` maps fields and operators; `quote()`
escapes quotes/backslashes; `renderSieve()` emits requires, rules in priority
order and the vacation block. Unit tests cover quoting and size/date output.

---

## 8. Quota (Dovecot)

**Demo:** `quotaInfo()` computes `baselineUsedBytes + sum(message.sizeBytes)`
and stores a folder breakdown. Permanent deletes reduce dynamic bytes; moves
to Trash do not. `/api/quota` and bootstrap expose it; the folder pane, status
bar, account card and popover consume the same object.

**Production:**

1. Read `doveadm quota get -u <user>` or the Dovecot quota API. Cache it for a
   short interval; invalidate after sends/deletes or on refresh.
2. Report `usedBytes`/`limitBytes` in bytes and provide the folder breakdown if
   your quota backend can; otherwise omit `folders` (the UI handles it).
3. Enforce limits on the write path (send, draft attachment upload, move) and
   surface a specific error, not a generic 500.
4. Do not count the demo's `baselineUsedBytes`; it exists to make the seeded
   mailbox look realistic. Keep the same response shape and the UI updates
   automatically.
5. Make the "source" label match reality (`Dovecot` now; change if using a
   different store) and show the refresh timestamp.

---

## 9. Audit log

**Demo:** `addAudit()` prepends entries with `{ id, ts, action, target, actor,
detail, ip, userAgent }`, capped at 1000. `/api/audit` filters by category and
`/api/audit.csv` exports. The UI maps action codes to human labels.

**Production:**

1. Write to `internal/auditlog` (or your append-only store). The event codes
   used by the UI are:
   `auth.login`, `auth.failed`, `auth.logout`, `auth.challenge`, `auth.2fa`,
   `auth.2fa.failed`, `send.queued`, `send.scheduled`, `send.rescheduled`,
   `send.sent_now`, `send.cancelled`, `send.committed`, `message.archive`,
   `message.move`,
   `message.delete`, `message.markRead`, `message.markUnread`,
   `message.star`, `message.unstar`, `message.flag`, `message.unflag`,
   `message.spam`, `message.notSpam`, `message.report`, `message.undo`,
   `message.received`, `draft.created`, `draft.deleted`, `rule.saved`,
   `trust.marked`, `trust.unmarked`, `task.created`, `task.updated`,
   `task.completed`, `task.reopened`, `task.deleted`, `file.created`,
   `file.updated`, `file.shared`, `file.deleted`, `storage.connected`,
   `storage.disconnected`, `attachment.uploaded`, `preferences.updated`,
   `security.recovery_regenerated`, `demo.reset`.
2. Keep the categories used by the UI (`login`, `send`, `message`, `rule`,
   `draft`, `task`, `files`, `preferences`) or extend `filterAudit()` and the
   select options together.
3. Do not log passwords, message bodies or attachment contents. IP/User-Agent
   are useful; consider privacy policy and retention (the UI says 7 years).
4. Make export streaming for large logs; the demo exports in one response.
5. Consider tamper evidence (hash chain/signing) for enterprise compliance.
6. Login failures are audit-worthy; rate-limit them so the log cannot be
   spammed.

---

## 10. Calendar and directory

**Current state:** the calendar reads seeded `calendarEvents` and new events are
demo-local. The directory reads seeded `people` and renders teams, detail and
recent conversations. Both preserve Aurora's responsive views.

**Calendar (CalDAV):**

- Add `GET /api/calendar?from=&to=` and `POST /api/calendar/events`; return
  `{ id, date, start, end, title, color, where, attendees }`.
- Use a CalDAV client with per-user credentials; map VTIMEZONE to the user's
  timezone and store events with a calendar id.
- Replace the in-memory `state.calendarEvents.push` in `saveEvent()` with the
  API call and refresh.
- Add recurrence, invitations/RSVP and reminders incrementally; the UI already
  has meeting cards and RSVP buttons in mail.

**Directory:**

- Back `/api/people` with your panel's people data. Keep name, role, company,
  team, email, phone, location, reportsTo, initials/color.
- Add server-side search for large directories; the current client filters 8
  seeded contacts.
- Respect visibility/ACL; do not expose fields the person has hidden.

---

## 11. PWA, offline and mobile

**Demo:** `manifest.webmanifest` has 192/512/maskable icons and three shortcuts.
`sw.js` precaches the app shell and never caches `/api/`. The UI shows an
offline banner, disables nothing destructively, and refreshes when online. The
mobile layout uses the existing Aurora breakpoints plus full-screen compose,
reader/tab navigation and a compose FAB above the draft dock.

**Production:**

- Version the cache with each deploy and keep `sw.js` cache headers short. The
  current strategy is network-first navigations + stale-while-revalidate for
  assets. API is always network-only.
- If you want offline reading, store a mailbox window in IndexedDB and add a
  clear "offline copy" lifecycle (encryption at rest, sign-out wipe, retention).
- Push notifications require a push service (Web Push) and user permission;
  the demo only has the notification center and preferences.
- Test install prompts on HTTPS; localhost is treated as secure for testing.
- Keep the safe-area insets and the FAB above the compose dock when adding
  bottom bars.

---

## 11a. Tasks workspace

**Demo:** `GET/POST /api/tasks` and `PATCH/DELETE /api/tasks/:id` persist
`{ title, status, priority, due, dueTime, notes, tags, assignee,
createdBy, source, linkedThreadId, linkedSubject, createdAt, updatedAt,
completedAt }`. Title/date/time/priority are validated; the assignee must be in
the directory; linked thread ids can open the source email. Completion and
deletion are audited and the UI receives `task.*` events.

**Production:**

- Put tasks behind a task service or CalDAV VTODO collection; map status
  `open|done` to `NEEDS-ACTION|COMPLETED`, and write `PERCENT-COMPLETE`/`COMPLETED`
  timestamps for interoperability.
- Enforce per-user and team ACLs; tasks are company data. Add assignment
  notifications, due reminders, recurrence, subtasks, comments and attachments.
- Link email by stable message/thread id, not subject. When a linked message is
  deleted or moved, keep the task with a snapshot of the subject.
- Add optimistic concurrency (`If-Match`/revision) and an activity log.
- Make “team” tasks query-backed rather than filtering the full store client-side
  once the workspace grows.
- Index task text for the global search surface; keep tasks out of mail search
  unless the user explicitly searches tasks.

---

## 11b. Files and storage connectors

**Demo:** `db.storageProviders` contains Alt8Mail Drive, OneDrive, Dropbox,
Google Drive and S3 with connected/account/usage/sync state. Connecting a
provider is idempotent, adds two seeded file records and writes
`storage.connected`. Files support list/filter, metadata edits, star, delete,
upload, preview and expiring share links (`/api/files/:id/share`). Share URLs
are illustrative (`files.a8m.example`) and are not resolvable.

**Production:**

- Use OAuth 2.0 with PKCE per provider, encrypt refresh tokens with KMS, and
  support tenant-wide vs per-user consent. Never expose provider tokens to the
  browser.
- Sync with provider delta APIs/webhooks into your file index; keep provider
  item ids and etags. Handle permission changes, revoked grants and rate limits.
- Use the provider file picker so mail attachments can be linked rather than
  duplicated. For “upload to Files”, write to the provider first, then index.
- Enforce DLP, external-share policy, domain allowlists, expiry, revoke and
  download audit. Map provider permissions to your own share roles.
- Serve large downloads via short-lived signed URLs or a streaming proxy with
  range support; do not buffer GB files in JSON or in memory.
- Run virus/content scanning before preview and before attaching to mail.
- Add per-tenant storage quotas and surface provider usage vs mailbox quota
  separately (the UI already labels them distinctly).
- For S3, prefer STS/IAM role assumption scoped to the user and prefix; never
  ship long-lived keys.

---

## 11c. Attachment preview and pop-out windows

**Demo:** `POST /api/attachments` accepts base64 JSON (13 MB request, 8 MB
file), sniffs PDF/PNG/JPEG/GIF/WebP magic bytes, stores the file under
`data/uploads/`, and returns `{ contentUrl, previewable, mime, sizeBytes }`.
`GET /api/attachments/:id/content` authenticates the session, sets inline
disposition for previewable files, sets `nosniff`, and sandboxes SVG responses.
The UI renders images with zoom/fit and PDFs in an `<iframe>`; other types get a
download fallback. Double-click or the toolbar opens a conversation in a
same-origin popout route (`?thread=…&popout=1`).

**Production:**

- Prefer multipart or presigned upload; keep the 8 MB UI limit configurable by
  tenant. Scan with ClamAV and, where required, content-disarm PDFs/Office docs.
- Generate thumbnails server-side and serve previews from a separate cookieless
  origin with unguessable signed URLs. Do not let user content share the app
  origin if you can avoid it.
- Keep an allowlist of previewable MIME types; never inline HTML, SVG without
  sandboxing, or active content. PDF.js or the browser viewer both need a
  careful CSP (`frame-src 'self'` in the demo) and download auditing.
- Strip EXIF/GPS from previews where policy requires; enforce image dimension
  and decompression-bomb limits.
- Bind attachment authorization to the message, not just the attachment id, so
  forwarded/revoked mail does not leak files. Expire content URLs.
- Popout windows inherit the same session and origin; keep `noopener` for
  external links and ensure the popout route cannot be framed by third parties
  (`frame-ancestors 'self'`).

---

## 12. Security and operations checklist

- Terminate TLS in front of the app; set `Secure` cookies (the server does when
  it sees HTTPS), HSTS at the edge, and a strict CSP. The template ships a CSP
  that allows only same-origin scripts and inline style attributes (the design
  uses style attributes); tighten if you remove them.
- Keep CSRF on every mutation. If you expose the API cross-origin, switch to a
  token-based design and do not share cookies.
- Sanitize all HTML on ingest as well as render, and consider a sandboxed iframe
  for full-fidelity messages. The client sanitizer is a safety net, not the only
  line of defense.
- Do not fetch arbitrary URLs found in messages from the server (SSRF). Use a
  dedicated image proxy/DNS policy.
- Rate-limit login, send, search and draft autosave per user/IP.
- Encrypt data at rest, including drafts and the queue; back up the audit log
  separately with a retention policy.
- Rotate session secrets on privilege changes; log out all sessions on password
  reset.
- Add health/readiness checks (`/api/health` exists) and metrics for queue age,
  send failures, cap rejections, search latency and quota pressure.
- Run the API and E2E suites in CI; the E2E needs a Chrome/Chromium binary.

---

## 13. Test adaptation

- `tests/api.test.mjs` (24 tests) creates isolated apps with a temp `dataFile`
  and exercises auth/2FA, preferences, mail, search, tasks, files/storage,
  attachment uploads, scheduling/edit/send-now, spam/report, trust, Sieve and
  audit. When swapping an adapter, keep these tests and point the adapter at a
  test double.
- `tests/e2e.mjs` starts its own app with a short undo window, drives Chromium
  via a tiny CDP helper (`tools/cdp.mjs`), and writes screenshots to
  `artifacts/`. It exercises the complete two-factor sign-in, workspace, preview,
  scheduling + Scheduled center, mobile and PWA flows and asserts no unexpected
  runtime errors.
- The API test client performs the 2FA step automatically with the demo code;
  use a real credential fixture when the passkey branch becomes WebAuthn.
- Useful deterministic options for local tests: `createApp({ dataFile, holdMs,
  undoTtlMs, outboundLimit, allowReset, demoTwoFactorCode })`.
- Add tests for your real integration: WebAuthn signature/counter validation,
  IMAP thread mapping, alias ACLs, Sieve capability negotiation, postfwd counter
  agreement, Dovecot quota units, provider OAuth refresh/revocation, DNS
  timeouts and audit event schema.

---

## 14. Suggested delivery order

1. Swap identity/session/aliases (`internal/store`), keep demo seed behind a
   flag.
2. Back the mailbox read path with Dovecot/IMAP + thread mapping.
3. Move search to Bleve/FTS5 and keep the operator parser/index contract.
4. Wire drafts and send queue to your MTA + Sent append; verify postfwd caps.
5. Wire Sieve/managesieve apply + capability checks.
6. Wire DKIM/DMARC/DNS into the trust panel and route the existing Report
   action into the security case queue.
7. Wire Dovecot quota and internal auditlog.
8. Add CalDAV and the directory.
9. Replace the simulated passkey with WebAuthn and move 2FA challenges/secrets
   to shared encrypted storage.
10. Back Tasks with your task/CalDAV service and Files with provider OAuth +
    sync workers; replace base64 uploads with multipart/signed object URLs.
11. Then tackle OpenPGP, delegation/shared mailboxes and richer search
    (attachment text, saved searches).
