# Current Project State

## Active Phase: Phase 3 (Scheduling, Revenue and Deliverability)

### Completed
- Phase 1 (1A–1H) ✅ See `docs/PHASE1_ACCEPTANCE.md`.
- Phase 2 (2A–2G) ✅ See `docs/PHASE2_ACCEPTANCE.md`.
- 3A Recipient-time scheduling and undo ✅ Tenant undo 5–60s, working-hour suggestions, preference/send enforcement and timezone/rollover tests.
- 3B Follow-ups and obligations ✅ Awaiting-reply and remind-me follow-ups, reply detection, duplicate suppression, worker and reading-pane controls.
- 3C Native mail merge ✅ CSV/JSON import, variables, suppression/unsubscribe, throttled outbox scheduling, campaign report and 100-recipient integration test.
- 3D Booking and availability ✅ Public booking pages, DST-aware slots, buffers, round-robin, race-safe bookings, free/busy and utilization APIs and booking UI.
- 3E Template gallery and payment links ✅ Packaged templates/install UI, payment-link policy and host allowlist, compose insertion and tests.
- 3F Deliverability ✅ DMARC XML/gzip ingestion, dedupe/aggregation, Domains dashboard, opt-in merge open/click tracking and policy-gated tests.
- Verification 2026-11-10: full Go suite/vet/build; Phase 3 PostgreSQL integration tests; `npm run test:all`; `npm run test:a11y`; migrations `0070`–`0074` rollback/reapply cycle.

### In Progress
- None.

### Not Started
- Phase 4 (AI/MCP Moat)

Phase 3 implementation is complete. No release, deploy or push has been performed.
