# Alt8Mail 3.1.2

Release date: 2026-11-11. OAuth popup and transient IMAP recovery.

## 3.1.2 connected-account resilience and OAuth result page

- A temporary Gmail IMAP failure such as `Temporary system problem. Please try
  again later` no longer sets the mailbox to `Reconnect required`. It is stored
  as a retrying sync error, the account stays in the sync worker, and the worker
  retries with backoff (2, 4, 8, then every 10 minutes). A successful pass
  restores the `Connected` status automatically.
- The account summary endpoint now returns `status` and `lastError`; the webmail
  refreshes them with its existing 15-second summary poll. Connected accounts
  settings, the account menu warning and the status row update without a page
  reload. Transient errors read `Sync error · retrying`; only real credential
  failures ask for a reconnect.
- Migration `0085_mail_sync_transient_status.sql` moves accounts that the old
  build already marked `Reconnect required` because of Gmail's temporary
  system-problem error back into the retrying worker, so they recover without
  asking the user to reconnect.
- Gmail's XOAUTH2 challenge scope is no longer appended to transient errors,
  where it was misleading. It remains in genuine authentication failures.
- The OAuth result window is a designed success/error card instead of an
  auto-closing page. It shows the connected mailbox, has a real **Close window**
  button and a **Return to Alt8Mail** button that focuses the main window. It no
  longer closes itself after 900 ms.
- On success the result page posts to the opener, focuses it immediately and the
  main app reloads bootstrap, re-renders connected accounts and opens the new
  mailbox, so the main window shows the `Connected` state while the result card
  is still visible.

# Alt8Mail 3.1.1

Release date: 2026-11-11. Gmail IMAP OAuth diagnostics.

## 3.1.1 explain Gmail and Outlook IMAP sign-in failures

- A successful Google/Microsoft sign-in can still fail at IMAP when the token
  did not receive the mail scope or the mailbox has IMAP disabled. The callback
  now checks the scopes returned by the token endpoint before opening IMAP:
  - Google must grant `https://mail.google.com/`.
  - Microsoft must grant `https://outlook.office.com/IMAP.AccessAsUser.All`.
  A missing scope now names the exact console setting and how to force a fresh
  grant instead of showing a generic “Invalid credentials” message.
- When the token has the scope, the error explains that Gmail accepted the
  sign-in but IMAP was refused, with the Gmail/Workspace IMAP and Microsoft
  365 IMAP/permission checks to perform.
- The IMAP XOAUTH2 client now captures Gmail's JSON authentication challenge
  and includes the server-requested scope in the error detail, distinguishing a
  consent/scope problem from a mailbox policy problem.
- The Mail OAuth page notes that adding a scope or API permission after a
  mailbox already connected requires reconnecting that mailbox so the provider
  records the new consent.

# Alt8Mail 3.1.0

Release date: 2026-11-11. Gmail and Outlook OAuth from System Control.

## 3.1.0 mail OAuth applications

- System Control has a new **Mail OAuth** page under Mail for the Google and
  Microsoft OAuth applications used by the multi-mailbox inbox. It replaces
  hand-editing `ALT8MAIL_GOOGLE_MAIL_*` and `ALT8MAIL_MICROSOFT_MAIL_*` in
  `/etc/a8m/app.env`.
- Each provider has an enable switch, client ID, encrypted client secret,
  Microsoft tenant (common, organizations, consumers, directory ID or verified
  domain), the exact authorized redirect URI with copy, requested scopes and
  step-by-step registration guidance for Google Cloud Console and Microsoft
  Entra.
- Secrets are sealed with the control-plane key before being stored in
  `control.mail_oauth_providers`. The API returns only whether a secret exists
  and where it came from; it never returns a stored secret.
- **Apply to webmail** writes the complete provider configuration to `app.env`,
  restarts the webmail and admin services and waits for their health checks.
  A failed restart restores the previous environment and brings the old
  services back. Saving a draft alone does not affect users.
- **Test connection** saves the draft, verifies the HTTPS redirect URI, checks
  that each enabled provider's OIDC metadata is reachable, and reports the
  authorize and token endpoints. Disabled providers are reported as skipped.
- Existing installations keep working without a click: before the first save
  the page mirrors the credentials already in `app.env`, including the shared
  OIDC client fallback, and marks whether mail-specific or shared credentials
  are in use.
- Disabling a provider writes an explicit `ALT8MAIL_*_MAIL_OAUTH_ENABLED=false`
  so shared OIDC credentials can no longer silently enable mail sign-in.
- The webmail now receives provider availability at bootstrap. When an
  administrator has not configured an OAuth application, the Add mail account
  wizard explains the missing setup and disables the provider button instead
  of opening a popup that immediately fails. Gmail still offers the app-password
  path in that state.
- The old “OAuth is not configured for this provider” error now names the
  provider and points to **System Control → Mail OAuth**.
- Added migration `0084_mail_oauth_config.sql` and unit coverage for credential
  resolution, encrypted draft storage, disable semantics, environment import,
  redirect URI validation and provider discovery.

# Alt8Mail 3.0.6

Release date: 2026-11-11. Enterprise interaction polish.

## 3.0.6 compose, scheduling and dialog UX

- The compose editor no longer paints a focus rectangle on mouse click. A
  keyboard-only focus ring remains when tabbing into the editor.
- Booking pages use a redesigned scheduler modal: a title/slug URL preview,
  duration presets, a full searchable list of IANA timezones, real time
  pickers for the availability window, weekday selection, a round-robin
  switch and inline validation. The public booking page is rebuilt as a
  responsive two-column enterprise scheduling page with grouped time slots,
  selected-time summary, proper form fields and a booking confirmation state.
- Meeting polls inherit the same form-field system and the full timezone list;
  their date/time option rows remain proper pickers.
- All native `window.prompt` dialogs are replaced with an in-app prompt modal.
  Link insertion, template variables, payment links and AI category feedback
  now use styled dialogs with validation and focus return.
- Follow up / Remind me use a designed reminder dialog with day presets, a
  custom day count and a due-date summary. Active reminders show a day-count
  badge on the button, can be updated in place or removed.
- Marking a sender VIP now visibly switches the button to a gold active state;
  active mute/follow-up states use the accent state style.
- Fixed the booking list date formatter, which previously threw after opening
  a page's bookings.

# Alt8Mail 3.0.5

Release date: 2026-11-11. Large-file scanning, settings UI and license-gate UX.

## 3.0.5 large-file scans and settings polish

- Go is updated to `1.26.11`, fixing the `net/http` `MaxBytesReader`
  header-race crash (GO-2026-6859). The CI vulnerability gate now passes with
  only the reviewed `GO-2026-6909` SAML advisory allowlisted.
- ClamAV scan limits are raised automatically to 320 MB (`StreamMaxLength`,
  `MaxFileSize`, `MaxScanSize`) with a ten-minute `MaxScanTime` by the
  control-plane startup check, so a 169 MB Cabinet share no longer fails with
  “The file could not be scanned for malware.” The scanner connection timeout
  is now ten minutes, and a scanner that still rejects a file returns a specific
  `FILE_SCAN_LIMIT` message with the exact limits to raise.
  `scripts/alt8mail-ensure-clamav-limits.sh` applies the same change manually
  and restores the previous config if clamd fails to restart.
- Keyboard shortcuts settings now use the settings-row design system with
  styled key fields, duplicate-key highlighting and a proper reset action.
- The AI workspace settings page uses consistent cards, list rows and form
  fields for triage, obligations, mailbox search and sender intelligence.
  License-gated AI capabilities render a dedicated notice instead of a bare
  error string.
- High contrast mode is a real on/off switch with `role="switch"`; per-folder
  density is rendered and wired to folder changes (it previously had no render
  call, so overrides never appeared).
- The Rules page reaction-rule section is wrapped in a settings card with a
  designed empty state and footer.
- The admin Integrations, Compliance and Team inboxes pages now show a proper
  “Upgrade to unlock …” license card with the feature key, copy action and
  organization-settings link instead of a generic “Something went wrong” page.
  Feature-gate errors now carry the missing feature key in the API payload.

# Alt8Mail 3.0.4

Release date: 2026-11-11. Large-file Cabinet uploads for the compose share flow.

## 3.0.4 chunked Cabinet uploads

- Oversized compose attachments now upload to the Cabinet in 8 MB chunks, so a
  169 MB file or larger no longer trips the reverse proxy's 25 MB request-body
  limit with `Request failed (413)`.
- The share dialog shows real upload progress (percentage, bar and button
  state) before creating and inserting the secure link.
- The Cabinet accepts uploads up to 300 MB through the resumable chunk API:
  start, append at a verified offset, then complete. Incomplete or abandoned
  uploads leave only a temp part file and can be cancelled.
- The chunk API works through the existing `/api/` proxy rules, so upgrading
  does not require an nginx body-size change.
- Added an integration test for start/offset/chunk/complete and an end-to-end
  compose share test that uploads an oversized file, validates a private link
  and inserts it into the message.

# Alt8Mail 3.0.3

Release date: 2026-11-11. Compose attachment-limit redesign.

## 3.0.3 compose share dialog

- Replaced the bare attachment-limit prompt with an interactive Cabinet-link
  flow: a clear oversized-file summary, one-tap expiry presets plus a custom
  day count, selectable public/private link cards, a conditional recipient
  email field, an optional message note, a live link summary and a busy state
  while the file is uploaded and shared.
- The share dialog now layers above the Compose attachments picker instead of
  behind it, returns focus when dismissed, validates private links inline, and
  closes the picker after the link is inserted so the message is visible.
- Fixed the share insertion using an undefined date formatter, which previously
  failed after the Cabinet link was created.

# Alt8Mail 3.0.2

Release date: 2026-11-11. Branding and shell-cache patch.

## 3.0.2 branding and cache fix

- The webmail service worker now versions its cache with the release, requests
  all shell assets with the current version, and serves CSS/JS network-first
  with an offline cache fallback. Previously the worker and every asset URL
  were unchanged across releases, so returning browsers kept the old
  stylesheet and script while loading the new HTML. That mix rendered the new
  logo images at intrinsic size and injected the old `AL` initials tile.
- Every brand image now declares its rendered width and height, so a stale or
  missing stylesheet can never let a 512 px logo cover the header.
- Generated initial tiles (`AL`/`A8`) are removed from the app shell, tenant
  row, passkey prompt and onboarding rail; those marks now use the supplied
  icon artwork. White-label tenants show their product name without a
  generated monogram.
- The customer admin app ships its own brand assets under `/brand/` and no
  longer requests webmail icon paths from the admin host. Brand images that
  fail to load hide cleanly instead of showing the browser's broken-image
  glyph.
- CI and `npm run test:all` verify that `VERSION`, `package.json`, the shell
  asset query string, the service-worker cache version and the control-plane
  version stay in sync.

# Alt8Mail 3.0.1

Release date: 2026-11-11. Security and release-gate patch for 3.0.0.

## 3.0.1 security patch

- Go is pinned to `1.26.10`; all standard-library vulnerabilities reported by
  `govulncheck` in the 3.0.0 build are fixed.
- The `crewjam/saml` advisory `GO-2026-6909` has no fixed upstream release.
  SAML assertion panics are contained and audited, and the CI vulnerability
  gate allowlists only that reviewed ID.
- CI now installs Chrome before web E2E tests, pins the vulnerability gate and
  separates the allowlisted advisory from unapproved findings.

## 3.0.0 highlights

- **Enterprise entry:** OIDC/SAML SSO, SCIM, seat/license enforcement, white-
  label and custom webmail hosts, per-folder restore/DR, accessibility gates.
- **Team operations:** shared inboxes with round-robin/SLA/presence, shared
  drafts with versions/comments, team templates, automation builder, team
  collaboration extensions and the MCP seed.
- **Scheduling and deliverability:** recipient-time scheduling, follow-ups,
  native mail merge, booking pages, template gallery/payment links, DMARC
  ingestion, opt-in campaign tracking.
- **AI/MCP moat:** governed MCP write tools with approvals and dry-run, tenant
  AI budget, AI triage, obligations ledger, natural-language search, meeting
  prep, Office document AI and DLP policy assist.
- **Compliance:** advanced DLP detectors and simulation, legal holds,
  classification retention, eDiscovery JSON/MBOX exports with manifests and
  review queue, BYOK envelope keys, file governance, OCR/file index, signed
  audit bundles and SIEM packs.
- **Platform:** `/api/v1` with scoped API keys, idempotency, OpenAPI and signed
  webhooks; HubSpot/Salesforce/Zoho/Pipedrive CRM cache; native Gmail API and
  Microsoft Graph mailbox import; Slack/Teams interactive actions; dual
  delivery journaling; external iCalendar free/busy federation; migration
  fidelity reports; tenant analytics, support bundle, opt-in telemetry,
  phishing-awareness simulation, command palette and offline compose/send
  queue.
- **Final scope completion:** embedded outbound/file watermarks, immutable
  byte-copy Cabinet versions, shared-draft anchored comment pins and version
  restore, high-contrast mode and per-folder density, and a compose attachment
  fallback that uploads oversized files to the Cabinet and inserts an
  expiring public or recipient-locked share link.
- **Phase scope completion:** recurring tasks and reminder notifications,
  policy-checked auto-sent auto-replies, booking edit/disable/cancel with
  booking lists, scheduled DMARC rua polling, per-tool MCP approvals and
  distributed per-connection rate limits, API cursor pagination, persistent
  offline mailbox/task mutation queues, customizable keyboard shortcuts,
  calendar resource approval workflows and external CalDAV free/busy.
- **Paid entitlements:** every Phase 3–6 paid capability is gated by canonical
  license feature keys. Licenses may use `all`/`*`; System Control and the
  webmail license snapshot report `availableFeatures`/`missingFeatures` for
  reissue.
- **Encrypted storage:** BYOK now seals Office binaries and Cabinet file blobs
  with versioned tenant DEKs (AES-256-GCM) and supports client-encrypted
  folders. Mail message rows remain protected by database/storage-at-rest
  encryption; see the known limits below.
- **Branding:** production icon/logo assets used across webmail, System Control,
  PWA manifest and install surfaces.

## Migrations

The release applies migrations `0061_scim` through
`0083_phase_scope_completion` on top of the `v0.27.6`/`0060` schema. The upgrade smoke test applies the
release tag, upgrades, rolls back new migrations in reverse order and reapplies
them. `0081_encrypted_storage` adds blob encryption metadata;
`0082_addon_action_tokens` adds single-use Slack/Teams action capabilities;
`0083_phase_scope_completion` adds task recurrence/reminder state, DMARC poller
state, distributed MCP rate events and resource-approval metadata.

## Upgrade notes

- Reissue or confirm customer licenses include the Phase 3–6 feature keys (or
  `all`). Legacy licenses keep their existing features but do not silently gain
  new paid capabilities.
- BYOK is opt-in. It encrypts tenant secrets, Office binaries and Cabinet file
  blobs; client-encrypted Cabinet folders are never decrypted server-side.
  `mail_messages` rows and their full-text index still rely on PostgreSQL
  storage encryption. See `docs/RELEASE_GAP_AUDIT.md`.
- Slack interactive buttons require the workspace signing secret in the Slack
  app's Interactivity settings. Teams action links use the configured public
  URL (`ALT8MAIL_PUBLIC_URL`) and expire after seven days or first use.
- Gmail/Graph migration needs an OAuth token with mail read access; use a JSON
  refresh-token bundle for migrations longer than the provider access-token
  lifetime.
- OCR requires an operator-provided `A8M_OCR_COMMAND` sidecar.
- Webhooks, telemetry, SIEM and external free/busy feeds must be public HTTPS
  endpoints; private and local addresses are rejected outside test seams.

## Verification

Local gates before the tag:

- `go test ./...`, `go vet ./...`, `go build ./...`
- PostgreSQL `internal/httpapi` suite via `A8M_SCIM_TEST_DSN`
- `npm run test:all`
- `npm run test:a11y`
- `tools/migration-upgrade-smoke.sh`
- Tag-triggered `.github/workflows/release.yml` builds the archive and
  SHA-256 checksum.

## Known release decisions

See `docs/RELEASE_GAP_AUDIT.md` for the remaining scope decisions and operator
drills required before publication.
