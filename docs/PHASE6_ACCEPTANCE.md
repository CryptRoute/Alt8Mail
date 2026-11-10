# Phase 6 Acceptance Tracker

Status: `done`, `in progress`, `planned`, or `operator` (requires live external
systems or credentials).

## Workstreams

| Workstream | Status | Evidence |
| --- | --- | --- |
| 6A API and automation ecosystem | done | Versioned `/api/v1`, scoped hashed keys, per-key rate limits, idempotent POSTs, OpenAPI, signed retrying webhooks and admin UI/tests |
| 6B Integrations | in progress | HubSpot contact/deal cache and CRM-aware sender profiles; Slack/Jira/Linear/CRM actions remain add-on based. Salesforce/Zoho/Pipedrive sync and coexistence dual delivery/free-busy are not shipped |
| 6C Migration and adoption | done | Existing IMAP/POP migration with rehearsal validation and fidelity report covering messages, folders, attachments, flags and calendar evidence |
| 6D Scale and operations | in progress | Analytics, support bundle, opt-in telemetry, phishing simulation, command palette and offline send queue; offline non-send actions and MDM packaging remain in progress |

## Release acceptance checklist

| Criterion | Status | Evidence / action |
| --- | --- | --- |
| API versioning and webhook retries are idempotent and documented | done | `docs/API_V1.md`, `TestPhase6APIPlatformIntegration`: idempotent send, delivery event id, retry backoff and signature headers |
| Migration fidelity report proves flags, folders, attachments and calendar data | done | `TestPhase6MigrationFidelityIntegration`: per-item and total counts, flags, attachments, calendar evidence and persisted report |
| Offline actions reconcile without duplicates on reconnect | done (send) | Offline compose/send queue + server idempotency; offline non-send mutations remain follow-up |
| Accessibility audit has no critical findings; RTL flows pass | done | `npm run test:a11y:audit` and keyboard/RTL gate; command palette uses dialog/listbox roles and Escape/arrow/Enter support |
| No release/deploy/push without authorization | done | Work remains local on `main` |

## Operator / follow-up risks

- Deep CRM adapters beyond HubSpot and Slack/Teams interactive callbacks need
  provider credentials and staging validation.
- Gmail API/Microsoft Graph native importers are not implemented; use the
  existing IMAP/POP migration path (rehearsal validation + fidelity report).
- Dual delivery, external free/busy federation and M365/Google coexistence
  beyond SCIM directory sync and ICS feeds are not shipped.
- API v1 is intentionally small (mail/task/calendar); files, contacts, DLP and
  cursor pagination are follow-up.
- Offline queue handles compose/send; queued archive/label/task mutations are
  not replayed yet.
- Telemetry is manual opt-in; a scheduled sender and retention policy around
  telemetry records need operator sign-off.
- Performance budgets are documented but not enforced in CI yet.
