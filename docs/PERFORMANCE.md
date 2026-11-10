# Performance and support budgets

These are release budgets for the Phase 6 scale work. Measure on a warm
production-like instance, not a development laptop.

## API

| Path | Budget |
| --- | --- |
| `GET /api/v1/messages` (50 rows, indexed folder) | p95 < 250 ms |
| `GET /api/v1/messages/{id}` | p95 < 150 ms |
| `POST /api/v1/messages/send` (queued, no provider call) | p95 < 300 ms |
| `POST /api/v1/tasks` | p95 < 200 ms |
| `GET /api/v1/calendar/events` (30 days) | p95 < 250 ms |
| `GET /admin/api/usage/analytics` | p95 < 500 ms |
| `GET /admin/api/support-bundle` | p95 < 500 ms |

Rate limits: default 120 requests/minute per API key; webhook worker processes
up to 50 due deliveries per tick.

## Webmail

- First contentful shell: < 2 s on a 4G profile.
- Folder switch / cached search: < 300 ms interaction latency.
- Compose open: < 200 ms.
- Offline reconnect: queued sends replay sequentially; no duplicate message
  may be created (idempotency key + server dedupe).

## Migration

- Fidelity report generation for up to 500 import items: < 5 s.
- Import jobs remain bounded by `maxMailImportItems=500` and per-message size
  limits already enforced by the importer.

## Measurement

Use the existing CDP harness, `tools/cdp.mjs`, or an operator APM trace. Add a
budget check to CI when a stable staging host is available. Failures should
report p50/p95 and the endpoint, not just a pass/fail.
