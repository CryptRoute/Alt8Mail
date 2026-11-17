# Phase 3 Acceptance Tracker

Status: `done`, `in progress`, `planned`, or `operator` (requires live external
systems or credentials).

## Workstreams

| Workstream | Status | Evidence |
| --- | --- | --- |
| 3A Recipient-time scheduling and undo | done | Tenant undo 5–60s, preference/send enforcement, working-hour suggestions, DST/rollover tests |
| 3B Follow-ups and reply reminders | done | Migration 0070, worker, reply detection, duplicate suppression, reading-pane controls, PostgreSQL tests |
| 3C Native mail merge | done | Migration 0071, CSV/JSON import, variables, suppression/unsubscribe, throttled outbox and 100-recipient integration test |
| 3D Booking and availability | done | Migration 0072, public booking page, timezone/DST slots, buffers, round-robin, race-safe booking, free/busy and utilization APIs |
| 3E Template gallery and payment links | done | Migration 0073, packaged templates/install UI, tenant payment policy/host allowlist, compose insertion and tests |
| 3F Deliverability | done | Migration 0074, DMARC XML/gzip ingestion/dedupe/summary dashboard, opt-in merge open/click tracking and policy tests |

## Release acceptance checklist

| Criterion | Status | Evidence / action |
| --- | --- | --- |
| 100-recipient merge preserves variables, suppression and throttling | done | `TestMailMergeIntegration`: 99 queued + 1 suppressed, duplicate removed, 2s spacing, rendered subject |
| Recipient-time scheduling and tenant undo pass timezone/policy tests | done | `send_time_test.go` and tenant policy enforcement; DST booking coverage |
| Booking tests pass across DST and concurrent attempts | done | `TestBookingIntegration`: spring DST and one-confirmation/one-conflict behavior |
| Follow-ups do not fire when a reply exists; no silent auto-send | done | `TestFollowUpIntegration`; worker only notifies, never sends |
| DMARC aggregates correctly and tracking honors tenant policy | done | `TestDMARCAndCampaignIntegration`; merge test asserts no tracking when disabled |
| No release/deploy/push without authorization | done | Work remains local on `main` |

## Operator / follow-up risks

- Live staging runs for the six workflows and a real external MCP-style client
  are still required before release.
- External CalDAV federation and resource approval workflow are not included;
  internal free/busy and utilization are.
- DMARC ingestion is API-driven; a scheduled rua mailbox poller is operator
  integration work.
- Campaign tracking covers mail merges only and must be enabled by tenant
  policy; it uses a transparent unsubscribe footer and hashed IPs.
- Booking pages have create/list/member UI; edit/delete/cancellation UI remain
  follow-up work.
- Undo working hours use observed recipient offsets, not IANA recipient zones;
  DST changes are reflected when a newer offset is observed.
