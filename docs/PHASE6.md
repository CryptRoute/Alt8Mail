# Phase 6 Implementation Notes

Phase 6 turns Alt8Mail into an integrable platform: a tenant API with keys,
idempotency and signed webhooks; CRM cache/sync; migration fidelity reporting;
tenant analytics, support bundles and optional telemetry; an authorized
phishing-awareness workflow; and webmail productivity/offline improvements.
Migrations: `0079_api_platform.sql`, `0080_ecosystem.sql`.

## 6A API and automation ecosystem

- `GET /api/v1` and `GET /api/v1/openapi.json` publish versioned API metadata
  and an OpenAPI 3.0.3 document. See `docs/API_V1.md`.
- API keys are tenant-scoped, hashed at rest, scope-limited and rate-limited
  per key. The raw token is shown once; revocation is immediate. Admin
  endpoints: `GET/POST /admin/api/api-keys`, `DELETE /admin/api/api-keys/{id}`.
- `/api/v1` supports message list/read/send, task list/create and calendar
  event list. POSTs support `Idempotency-Key`.
- Webhooks store endpoints and per-event deliveries. Delivery is idempotent by
  `(endpoint, event_id)`, HMAC-signed over timestamp + body, retried with
  backoff, and visible in System Control. Test events are supported.
- Zapier/n8n/Make can use the generic REST + webhook contract; no bespoke
  connector is required for the generic trigger/action path.
- Tests: `TestPhase6APIPlatformIntegration` covers key creation, scope
  enforcement, tenant isolation, rate limits, revocation, idempotent send,
  webhook retries/signature headers and OpenAPI presence.

## 6B Integrations

- `crm_records` caches HubSpot contacts and deals. `POST /admin/api/crm/sync`
  uses the existing tenant-scoped HubSpot add-on credential. `GET
  /admin/api/crm/records` and `/crm/state` expose the cache and sync state.
- Sender profiles/meeting prep now include cached CRM company, title and deal
  stage when available.
- Existing add-ons already provide Slack posting, Jira, Linear and CRM logging
  actions. Slack/Teams interactive callback routing and Salesforce/Zoho/
  Pipedrive sync adapters are follow-up work.
- Coexistence: SCIM handles directory synchronization, calendar feeds and
  internal free/busy exist. Dual delivery and external free/busy federation are
  documented operator gaps, not implemented in this slice.
- Tests: `TestPhase6CRMSyncIntegration` uses a fake HubSpot API and verifies
  contact/deal cache, sync state and CRM-aware sender snapshots.

## 6C Migration and adoption

- Existing IMAP/POP migration jobs now produce a fidelity report at
  `GET /admin/api/mail-import/jobs/{id}/fidelity`. It records per-item source/
  imported counts and target message/folder/attachment/unread/starred/calendar
  counts, writes `migration_reports` and audits the generation.
- The importer validation phase remains the rehearsal path: source credentials
  and mailbox access are checked before queued writes. Native Gmail API and
  Microsoft Graph importers are follow-up work; Gmail/M365 can be migrated over
  IMAP/POP today.
- Tests: `TestPhase6MigrationFidelityIntegration` verifies counts, flags,
  attachment/calendar evidence and report persistence.

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
  MDM packaging is the existing manifest/PWA install path; no separate native
  bundle is produced.
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
