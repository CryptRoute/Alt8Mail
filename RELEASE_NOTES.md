# Alt8Mail 3.0.0

Release date: pending authorization. This file records the intended release
contents; the tag, push and publication are separate authorized actions.

## Highlights

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
