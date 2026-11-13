# Alt8Mail 3.5.1

Release date: 2026-11-13. Swap-file fix for the legacy mail root.

## 3.5.1 swap files work on /var/vmail

- The legacy mail root `/var/vmail` is normally a directory on the root
  filesystem, not a separate mountpoint. The swap-file runner previously
  required an active mount and failed after a valid plan was created.
- The swap planner, candidate list and runner now accept the legacy root when
  its directory and filesystem are readable. Managed volumes still require an
  active mount, so a missing mount cannot silently place a swap file on the
  parent filesystem.
- Existing failed plans can be retried by creating a new swap plan after
  installing 3.5.1.

# Alt8Mail 3.5.0

Release date: 2026-11-13. Swap visibility and safe swap creation in Disk
Management.

## 3.5.0 host swap without leaving the panel

- **Swap card.** Disk Management now reports total, used and free swap, the
  active swap areas, their type, size, usage, priority and whether they persist
  at boot. Host memory, available memory and `vm.swappiness` are shown alongside
  the current swap state.
- **Swap file creation.** An administrator can allocate a hidden
  `.a8m-swapfile` on an online ext4 or XFS mail volume. The wizard recommends a
  size from host memory, enforces at least 64 MiB and protects the existing
  capacity reserve. The file is formatted, activated, restricted to mode 0600
  and persisted with a managed fstab block.
- **Dedicated disk creation.** A blank whole disk can be dedicated to swap.
  Disks with partitions, a mountpoint, system protection or active swap are not
  eligible. The operation requires the `storage.destroy` permission, a typed
  erase confirmation, and revalidates the disk identity and blank state in the
  root runner before the first write.
- **Reviewed operations.** Both modes use the existing storage plan/execute
  token flow and appear in the live Storage operations console. Failure cleanup
  removes a partial swap file, deactivates swap, restores fstab and removes a
  partial swap signature.
- NFS volumes are excluded from swap-file creation. Host disk provisioning must
  be enabled, and the existing runner and operation gates still apply.
- This release adds swap visibility and creation; turning off or removing an
  existing swap area is not included.

# Alt8Mail 3.4.2

Release date: 2026-11-13. Completes the 3.4.1 upgrade recovery.

## 3.4.2 create the storage root even on the first hop from 3.3.1

- The updater that runs during a 3.3.1 → 3.4.x update is the one installed in
  3.3.1, so it cannot run the 3.4.1 directory-creation change. The control unit
  now runs `/opt/a8m/current/scripts/alt8mail-ensure-storage-root.sh` as an
  `ExecStartPre`, creating `CONTROL_STORAGE_ROOT` (default
  `/srv/a8m-storage`) as `root:vmail` mode `0750` inside the service sandbox.
  This makes the first upgrade create the directory even when the old updater
  performed the restart.
- The helper is idempotent and falls back to creating the directory without the
  `vmail` group when that group is unavailable. The unit still marks the path
  optional, so a missing root cannot block the control plane from starting.
- This completes the 3.4.1 fix. Use 3.4.2 for upgrades; 3.4.0 should not be
  installed over an existing 3.3.1 host.

# Alt8Mail 3.4.1

Release date: 2026-11-13. Upgrade hotfix for existing 3.3.1 hosts.

## 3.4.1 upgrades start on hosts that do not yet have a storage root

- The 3.4.0 service units added `/srv/a8m-storage` to `ReadWritePaths`. systemd
  refuses to start a unit when a `ReadWritePaths` entry does not exist, and an
  existing 3.3.1 host did not have that directory yet because it was created
  only by a fresh install. After migrations, `alt8mail-control` therefore failed
  to start and the updater rolled back to 3.3.1.
- The control and mail-provisioning units now mark the storage root optional
  with the systemd `-` prefix. The updater also resolves
  `CONTROL_STORAGE_ROOT` (defaulting to `/srv/a8m-storage`) and creates it as
  `root:vmail` mode `0750` before installing the new units and restarting
  services. A missing directory can no longer block the control plane from
  booting; storage can still be repaired from Disk Management.
- Regression tests assert that both units use the optional path and that the
  updater creates the storage root before its restart loop.
- Use 3.4.1 for upgrades. The 3.4.0 release asset remains available for audit
  history but should not be installed over an existing host.

# Alt8Mail 3.4.0

Release date: 2026-11-13. Managed mail storage, capacity protection and a rebuilt
Disk Management console.

## 3.4.0 mail storage you can see, place and change safely

- **Storage-aware Dovecot homes.** The managed userdb query now returns
  `mail_home` when a mailbox has one and falls back to the legacy
  `/var/vmail/<domain>/<local-part>` path for existing mail. The storage doctor
  backs up, validates and reloads the query and verifies a mailbox before
  placement is unlocked. Upgrading an existing host does not move mail.
- **Managed volume registry and placement.** The Control Plane registers
  ext4, XFS and NFS mail volumes and records their mount path, UUID, device,
  fstab block, reboot verification and health. New mailboxes resolve placement
  by mailbox override, then domain default, then global default, then the legacy
  root. Changing a default never moves existing mail.
- **Reviewed host operations.** Add, adopt, extend, retire and wipe volumes run
  through plan and execute steps, a typed confirmation, a root storage runner
  and a replay-resistant plan token. The runner revalidates the device, serial
  or WWN, mount state and filesystem before writing. fstab edits use marked
  blocks and restore the original when validation fails.
- **Verified mailbox moves.** Moves copy, verify and switch mailbox homes while
  retaining the source for a configurable grace window. Operators can roll back
  or finalize from the live operation console; progress, step state, logs and
  per-mailbox results are retained.
- **Capacity policy and protection.** Configurable advisory through emergency
  thresholds, reserve and minimum free space, a 14-day linear forecast,
  projected-full alerts, alert re-arming, low-I/O usage scans and mailbox-level
  rebalance recommendations. At critical or emergency capacity the storage guard
  holds new inbound mail with SMTP `4.3.1` and webmail/Cabinet uploads with HTTP
  `507` while existing mail stays readable.
- **Hardware awareness.** Physical disk inventory now includes SMART health,
  topology detection for LVM, RAID, LUKS and multipath, clear protection reasons
  and safe candidates. Mounted NFS filesystems can be adopted without changing
  the export. Growth that cannot be performed safely, such as thin-LVM or
  LVM-RAID extension, is refused with an explanation.
- **Rebuilt Disk Management UI.** OS-style drive cards show capacity, path, type
  and health; status filtering covers online, attention, offline, retired and
  wiped volumes; clicking a volume opens full identity, health, forecast and
  action details. Capacity outlook moves directly below the KPI cards, capacity
  policy lives in a confirmed modal with a before/after change review, and
  storage operations paginate ten per page with sortable Operation, Target,
  Mailboxes and Status columns.
- **Scalable move picker.** The move wizard searches and pages the mailbox
  directory on the server, shows user, quota, used bytes, usage and current
  volume, and keeps checkbox selection while paging. It is validated against a
  5,120-mailbox directory. A single individually selected move is limited to 500
  mailboxes by the planner; larger batches use a whole-domain move or multiple
  jobs.
- Migrations `0088_storage_volumes` through `0094_storage_forecast_alert` and
  the new `a8m-storage-runner` binary ship in this release.

# Alt8Mail 3.3.1

Release date: 2026-11-12. Office window polish and Reply-To delivery fix.

## 3.3.1 a calmer Office window and a Reply-To that reaches the recipient

- The Office minimize, maximize and close controls now sit at the far right edge
  of the header, matching the compose window. **Open** and **Versions** keep
  their place next to the centered search.
- The dark page scrim is gone. The Office window floats over the workspace like a
  compose window, so the underlying page stays visible and interactive while the
  editor is open.
- Window position and size are stored locally and in the user's server
  preferences (`officeWindow`). Last-write-wins timestamps keep both copies in
  step, so signing in on another system restores the remembered geometry for
  every Office type.
- A new Office window opens at a focused, centered 78% × 82% size rather than
  almost filling the viewport. All four edges and corners are draggable resize
  handles, alongside the visible bottom-right grip. Double-clicking the header
  still maximizes and restores.
- The recent-documents dropdown now closes on any click outside the search
  field: elsewhere in the header, another part of the Office chrome, the page
  behind the window, or inside the ONLYOFFICE editor.
- PWA install screenshots were refreshed at the manifest's exact sizes
  (1440x1000 wide, 390x844 narrow) with the current Alt8Mail icon and logo.
  The manifest and page description now read “AI-powered enterprise email with
  workspace for business email, calendar, files, tasks, office and AI.”
- The mobile footer's Settings tab is replaced by a four-square **Menu** icon
  that opens the mailbox and folder drawer, matching the mobile folder control.
- **Reply-To delivery fix**: the normalized `outbox` table did not carry
  `reply_to`. The send handler stored the value in the state document, but the
  delivery worker rebuilds the queue from the outbox table and therefore dropped
  it before building the MIME message. Migration `0087_outbox_reply_to.sql` adds
  the column, persistence and loading now include it, and delivery resolves the
  identity reply-to from the database as a fallback for older queued items. An
  integration test asserts the submitted message contains the Reply-To header.

# Alt8Mail 3.3.0

Release date: 2026-11-12. Office window, open dialog and email-to-task.

## 3.3.0 an Office window that behaves like the desktop app

- The Office editor header has a centered search field (also **Ctrl K**). It
  searches only the file type of the open editor — documents, spreadsheets or
  presentations — across Cabinet names and email attachments, including
  server-side attachment filename search beyond the loaded mailbox page. Results
  show in a grouped dropdown; choosing one opens it in the shared Office window
  and docks the previously active document.
- The Office window can be moved by dragging its header and resized from its
  bottom-right grip, with the size and position remembered per user. Double-click
  the header to maximize or restore. Mobile keeps the full-screen editor.
- A new **Open** button beside Versions opens a moveable Open window with three
  columns: matching Cabinet files, matching email attachments and a device
  column. The device column browses the local PC or shows a QR code for a phone
  upload. A file uploaded from the phone lands in Cabinet and opens in the
  Office window automatically; the upload session now reports the created file
  id so the desktop can open the exact file.
- Emails can now be turned into tasks. The reader toolbar has an **Add to task**
  action and the message menu has **Add to task** as well. The task editor shows
  the linked email, pre-fills the title, sender and snippet, and saves the task
  with `linkedThreadId` / `linkedSubject` so the conversation stays attached.

# Alt8Mail 3.2.2

Release date: 2026-11-12. Compose regression fix.

## 3.2.2 compose keeps its identity, layout and calm typing area

- A message is now always routed through the account that owns its From
  identity. Choosing a native identity inside a compose window while a
  connected Gmail/Outlook mailbox is open no longer fails drafts or sends with
  `Choose the connected account address as the From identity`. The compose
  account is derived from the selected From address everywhere: draft save,
  send, schedule, AI tone and team templates, and it is repaired when identities
  refresh.
- The forwarding card's recipient field styles are scoped to forwarding again.
  The compose To, Cc and Bcc rows keep their original flat layout with no
  rounded border or focus box.
- The compose writing area no longer draws a rectangle while typing. The
  keyboard focus ring is shown only when the editor is reached with Tab, so the
  keyboard accessibility gate still passes.

# Alt8Mail 3.2.1

Release date: 2026-11-12. CI reliability for the 3.2.0 line.

## 3.2.1 deterministic mail refresh in the HTML reply fixture test

- The end-to-end HTML reply fixture no longer waits on the webmail's
  15-second new-mail poll. Headless CI browsers throttle that timer, so the
  test now clicks the toolbar **Refresh** button and waits for the injected
  message to render. Application behavior is unchanged from 3.2.0.

# Alt8Mail 3.2.0

Release date: 2026-11-12. Identity signatures, defaults and forwarding polish.

## 3.2.0 sending identity settings that behave like they look

- **Edit sending identity** now edits the From display name, reply-to and the
  signature in one place. Display name and reply-to work for organization
  aliases, including the primary account, not just self-service identities.
  Outgoing mail uses the edited From name, and a configured reply-to is written
  as a `Reply-To` header when the message leaves the server.
- The identity editor no longer embeds its own rich-text signature pad. It shows
  the signature library from **Settings → Mail & reading → Signatures** as a
  selectable list with a preview and its automatic-use chips. **New signature**
  opens the existing signature editor on top of the identity dialog; a saved
  signature is selected immediately and available on every identity.
- A **Default sending identity** switch sets the identity used for new messages.
  Turning it on clears the previous default, so exactly one identity is the
  default; the identity list shows a `Default` badge. An identity must be
  verified before it can become the default, and the last default cannot be
  switched off.
- The identity list now shows the signature actually attached to each address
  (`Signature · Sales follow-up`) or `No signature`, plus the reply-to and the
  default badge. Compose inserts the selected identity signature when that From
  address is chosen, including when switching identity inside an open message.
- Identities now reference a named signature from the library, so editing a
  signature updates every identity that uses it. Deleting a signature clears it
  from those identities. Legacy per-identity signatures are preserved as a
  `Current signature` choice until they are replaced.
- **Mail forwarding** is rebuilt as a focused card: a forwarding switch, a
  recipient-style **Forward to** field with a clear control and a live resolved
  address chip, and options that dim while forwarding is off. Invalid and
  self-forwarding addresses are explained inline before anything is saved.
- The **Attached to this message** side of the compose attachment manager is now
  a vertical list like the Cabinet column instead of a horizontal card strip, so
  long file names are readable and many attachments stay scannable.
- Migration `0086_identity_signature_choices.sql` adds the named-signature link
  and the per-address display-name and reply-to overrides.

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
