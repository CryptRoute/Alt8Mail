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
