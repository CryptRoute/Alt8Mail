# Phase 6 Implementation Notes

Phase 6 turns Alt8Mail into an integrable platform: a tenant API with keys,
idempotency and signed webhooks; CRM cache/sync; migration fidelity reporting;
tenant analytics, support bundles and optional telemetry; an authorized
phishing-awareness workflow; and webmail productivity/offline improvements.
Migrations: `0079_api_platform.sql`, `0080_ecosystem.sql`,
`0082_addon_action_tokens.sql` (interactive Slack/Teams actions). Storage
encryption metadata from `0081_encrypted_storage.sql` is documented in
`docs/PHASE5.md`.

## 6A API and automation ecosystem

- `GET /api/v1` and `GET /api/v1/openapi.json` publish versioned API metadata
  and an OpenAPI 3.0.3 document. See `docs/API_V1.md`.
- API keys are tenant-scoped, hashed at rest, scope-limited and rate-limited
  per key. The raw token is shown once; revocation is immediate. Admin
  endpoints: `GET/POST /admin/api/api-keys`, `DELETE /admin/api/api-keys/{id}`.
- `/api/v1` supports message list/read/send, task list/create and calendar
  event list. Message, task and calendar list endpoints use stable
  `nextCursor` keyset pagination with `limit` and `cursor`; POSTs support
  `Idempotency-Key`.
- Webhooks store endpoints and per-event deliveries. Delivery is idempotent by
  `(endpoint, event_id)`, HMAC-signed over timestamp + body, retried with
  backoff, and visible in System Control. Test events are supported.
- Zapier/n8n/Make can use the generic REST + webhook contract; no bespoke
  connector is required for the generic trigger/action path.
- Tests: `TestPhase6APIPlatformIntegration` covers key creation, scope
  enforcement, tenant isolation, rate limits, revocation, idempotent send,
  webhook retries/signature headers and OpenAPI presence.

## 6B Integrations

- `crm_records` caches contacts and deals from HubSpot, Salesforce, Zoho CRM and
  Pipedrive. `POST /admin/api/crm/sync?provider=` uses the matching tenant-scoped
  add-on credential; `GET /admin/api/crm/records` and `/crm/state` expose the
  cache and sync state.
- Salesforce uses the connected-app refresh token against the production or
  sandbox login host; Zoho exchanges a refresh token at the configured data
  centre; Pipedrive uses an API token. Personal-data-bearing provider payloads
  stay in the tenant's CRM cache table.
- Sender profiles/meeting prep now include cached CRM company, title and deal
  stage when available.
- Slack posting now includes Block Kit actions (mark read, archive, create task)
  when the workspace Slack app has an interactivity signing secret. Button
  presses are verified with the Slack signature over the raw body and consumed
  once. Microsoft Teams is a first-class add-on: it posts Adaptive Cards with
  signed, single-use action links handled at
  `GET /api/addons/actions/{token}`.
- Coexistence: SCIM handles directory synchronization, calendar feeds and
  internal free/busy exist. Outbound dual delivery journals a copy of every
  delivered message to the tenant policy's archive address. External free/busy
  federation accepts up to five public HTTPS iCalendar feeds on
  `GET /api/calendar/free-busy?external=…` and returns busy intervals only.
- Tests: `TestPhase6CRMSyncIntegration`, `TestCRMProviderAdaptersIntegration`,
  `TestAddonInteractiveActionsIntegration`, `TestICSRecurrenceExpansion` and
  `TestBusyIntervalsSkipTransparentAndCancelled`.

## 6C Migration and adoption

- Existing IMAP/POP migration jobs now produce a fidelity report at
  `GET /admin/api/mail-import/jobs/{id}/fidelity`. It records per-item source/
  imported counts and target message/folder/attachment/unread/starred/calendar
  counts, writes `migration_reports` and audits the generation.
- The importer validation phase remains the rehearsal path: source credentials
  and mailbox access are checked before queued writes.
- Native `gmail` and `graph` protocols import through the Gmail API and
  Microsoft Graph. The credential column accepts a raw OAuth access token or a
  JSON bundle (`accessToken`, `refreshToken`, `clientId`, `clientSecret`) for
  refresh-capable long jobs. The token's account must match the mailbox row.
  Source folders map to INBOX/Sent/Drafts/Trash/Junk, flags and received dates
  are preserved, and cursors survive restarts.
- Tests: `TestPhase6MigrationFidelityIntegration`,
  `TestMailImportOAuthTokenAndProfile` and
  `TestMailImportProviderFolderMapping`.

## 6D Scale and operations

- `GET /admin/api/usage/analytics` returns users, storage, mail, file/task/
  calendar counts, AI requests/cost, 30-day send volume and a daily send series.
- `GET /admin/api/support-bundle` returns a redacted diagnostic bundle: version,
  tenant metadata, counts, recent migrations and configuration checks. It never
  includes secrets.
- `GET/PUT /admin/api/telemetry` and `POST /telemetry/send` implement explicit,
  opt-in telemetry; no data is sent unless an administrator enables it.
- Phishing simulation: create draft campaigns with targets, launch authorized
  awareness emails through the admin mailbox, record click/report events at
  `/phish/{token}`, and view campaign reports. This is intentionally
  admin-only and audited.
- Command palette (`Ctrl/Cmd+K`) adds compose, search, navigation, theme,
  density and AI-workspace commands with keyboard navigation.
- Offline compose/send: failed network sends are stored locally with an
  idempotency key and replayed on reconnect. Replays are deduplicated server-
  side, so a reconnect cannot create duplicate messages.
- Existing PWA offline shell, web push, theme and density controls remain.
  The offline queue now persists compose/send, archive/move/read/label mailbox
  actions and task create/update/delete mutations, with server-side idempotency
  for replayed task creation. MDM packaging is the existing manifest/PWA
  install path; no separate native bundle is produced.
- Keyboard shortcuts are customizable in Settings → Keyboard shortcuts and the
  reference dialog reflects the saved bindings.
- Tests: `TestPhase6PlatformOpsIntegration` covers analytics, redacted support
  bundle, telemetry delivery and phishing create/launch/click/report.

## Verification

- `go test ./...`, `go vet ./...`, `go build ./...`
- PostgreSQL `internal/httpapi` suite with Phase 6 DSN tests
- `npm run test:all`
- `npm run test:a11y`
- Phase 6 migrations `0079`–`0080` rollback/reapply cycle

## Known limits

- Native Gmail API/Microsoft Graph migration, deep Salesforce/Zoho/Pipedrive
  sync, Slack/Teams interactive actions and dual delivery/free-busy federation
  are not included. Existing IMAP/POP migration, HubSpot sync and add-on
  connectors are the shipped paths.
- API v1 covers mail/task/calendar core; public contact/file/DLP endpoints and
  cursor pagination are follow-up work.
- Offline queue covers compose/send only; offline archive/label/task mutations
  are not queued.
- Telemetry is manual (an admin sends a heartbeat); there is no scheduled
  opt-in sender yet.
- Performance budgets are documented in `docs/PERFORMANCE.md`; automated
  regression enforcement is follow-up.
