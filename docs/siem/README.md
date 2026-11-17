# SIEM and audit export packs

Alt8Mail emits signed audit webhooks (configured in **System Control →
Integrations → SIEM**) and signed export bundles (`GET /admin/api/audit/export`).
This directory contains starter field mappings for common SIEMs.

## Event shape

```json
{
  "source": "user|admin",
  "id": "event id",
  "actor": "user id",
  "actorEmail": "person@example.com",
  "action": "mail.sent",
  "target": "message or object",
  "detail": "human-readable detail",
  "ip": "203.0.113.10",
  "userAgent": "…",
  "metadata": {},
  "createdAt": "2026-11-10T12:00:00Z"
}
```

Admin webhooks additionally carry an `X-A8M-Signature` HMAC over the
timestamp and raw body. Verify it before indexing. Export bundles include a
`manifest.contentSha256` and an HMAC `signature` over the manifest; recompute
both when ingesting to preserve tamper evidence.

## Splunk

See `splunk.spl` for saved-search starters. Index the event JSON without
flattening `metadata`, and extract `action`, `actorEmail`, `source`, `ip`.
Suggested alerts:

- `action="admin.legal_hold_released"` — any release outside a change window.
- `action="admin.encryption_locked"` — key lock changes.
- `action="mcp.action_approved"` — autonomous write approvals.
- `action="file.share_blocked"` or `action="mail.forward_blocked"` — DLP friction.

## Microsoft Sentinel

See `sentinel.kql`. Ingest through the Logs Ingestion API or a custom table
`Alt8MailAudit_CL`. Join `actorEmail` with Entra ID sign-in logs to correlate
administrative actions with identity risk.

## Elastic

See `elastic.ndjson` for an index template starter. Use the `action` field as
a keyword and `metadata` as a flattened object. Time field: `createdAt`.

## Export verification

1. Download `GET /admin/api/audit/export?download=1`.
2. Recompute SHA-256 over the canonical JSON array in `events`; compare with
   `manifest.contentSha256`.
3. Verify `signature` as HMAC-SHA256 over the canonical JSON manifest using the
   deployment's workspace signing secret.
4. Store the bundle id and hash in the case management system.
