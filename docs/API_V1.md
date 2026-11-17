# Alt8Mail API v1

Base path: `/api/v1`. Authenticate with a tenant API key as a Bearer token.
Keys are created in **System Control → Integrations → API keys**; the raw token
is shown once. Scopes: `mail.read`, `mail.send`, `tasks.read`, `tasks.write`,
`calendar.read`. Rate limits are per key and per minute (default 120).

OpenAPI: `GET /api/v1/openapi.json`.

## Endpoints

| Method | Path | Scope | Notes |
| --- | --- | --- | --- |
| GET | `/me` | any key | Key owner and scopes |
| GET | `/messages?folder=&q=&limit=&cursor=` | `mail.read` | Newest first, max 200, keyset `nextCursor` |
| GET | `/messages/{id}` | `mail.read` | Body, classification, attachments |
| POST | `/messages/send` | `mail.send` | Queues an outbox message |
| GET | `/tasks?status=` | `tasks.read` | Open/done filter |
| POST | `/tasks` | `tasks.write` | Creates a task and webhook event |
| GET | `/calendar/events?from=&to=` | `calendar.read` | RFC3339 range, max 500 |

Every POST accepts an `Idempotency-Key` header (or `idempotencyKey` query).
Replays return the original status and body; the second request performs no
work. The normal webmail `/api/send` path also accepts an idempotency key, which
is what the offline queue uses when reconnecting.

Errors use `{"error":{"code":"…","message":"…"}}`. Common codes:
`API_KEY`, `API_SCOPE`, `API_RATE_LIMIT`, `API_MESSAGE`, `API_TASK`,
`API_CALENDAR`.

## Webhooks

System Control creates HTTPS endpoints subscribed to `message.queued`,
`task.created`, `calendar.created`, `file.shared`, `crm.synced` and
`webhook.test`. Deliveries include:

- `X-A8M-Event`
- `X-A8M-Delivery` (stable event id; retries reuse it)
- `X-A8M-Timestamp`
- `X-A8M-Signature: <hex HMAC-SHA256(timestamp + "\\n" + raw_body)>`

The signing secret is shown once. Retries back off up to five attempts, then
the delivery is marked failed. Delivery history is available at
`GET /admin/api/webhooks/deliveries`.

## Automation connectors

Zapier, n8n and Make can use the REST API plus signed webhooks without a custom
connector: API-key auth, idempotent writes and event deliveries are the generic
trigger/action contract.
