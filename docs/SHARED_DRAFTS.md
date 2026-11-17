# Shared Drafts v1

Shared drafts let agents in a team inbox edit the same compose draft without
CRDT. The feature requires a license with `shared_drafts` and membership in the
team's agent group.

## Concurrency model

- Every draft has a monotonically increasing `version`.
- A single-writer lease is acquired before editing (`writerToken`, default
  five minutes, renewable 30–900 seconds).
- A save must send the lease token and the `baseVersion` it edited.
- A stale `baseVersion` returns `409 DRAFT_CONFLICT` with the latest version
  and payload; a missing/expired/foreign lease returns `409 DRAFT_LEASED`.
- The webmail pauses autosave after a conflict and points the agent at the
  current team draft instead of silently overwriting another agent's work.

## API

```text
GET    /api/shared-drafts?teamId={teamId}
POST   /api/shared-drafts
GET    /api/shared-drafts/{id}
PATCH  /api/shared-drafts/{id}
DELETE /api/shared-drafts/{id}
POST   /api/shared-drafts/{id}/lease
DELETE /api/shared-drafts/{id}/lease
GET    /api/shared-drafts/{id}/versions
POST   /api/shared-drafts/{id}/versions/{version}/restore
POST   /api/shared-drafts/{id}/comments
POST   /api/shared-drafts/{id}/comments/{commentId}/resolve
```

`PATCH` body: `{ "baseVersion": 3, "writerToken": "…", "payload": { … } }`.
The payload is the normal compose payload (`toText`, `ccText`, `bccText`,
`subject`, `bodyText`, `bodyHtml`, `fromEmail`, `threadId`, and so on), capped
at 1 MB. The server extracts the subject for list views.

`POST /api/shared-drafts` accepts `{ teamId, threadId?, payload }` and returns
the created draft plus a `writerToken`. It writes version 1 and starts the
creator's lease.

`POST …/versions/{version}/restore` requires the current `baseVersion` and
lease token; restoring creates a new head version rather than deleting history.

Comments accept `{ body, anchor }`; `anchor` is stored as JSON so clients can
attach a selection, region or field. Resolving a comment records the agent and
timestamp.

## Webmail flow

When a team folder is selected, the header shows **Shared**, the queue counts
and a **Team drafts** button. The team draft list shows version, last editor,
unresolved comment count and any active editor. **New draft** creates a shared
draft; **Open** acquires the lease and opens it in the normal compose window.
Autosave switches to the shared draft endpoint. Empty/discarded drafts are
deleted; closing a non-empty draft saves and releases the lease by expiry.

## Isolation

Drafts are keyed to a team mailbox and tenant. `teamMailboxForUser` checks
active group membership before any read or write. Deleting a team or tenant
cascades the drafts. Payload sizes are bounded and stored as JSONB, never
executed or rendered as HTML outside the normal compose sanitizer.

## Limits

No CRDT/offline merge, no field-level merge, no section ownership, no live
presence inside the draft editor in v1. Presence/collision is provided for
conversations; draft presence is represented by the lease. Comments are shown
through the API/team-draft list but the compose editor does not yet render
anchored comment pins. A future version can add section leases and a merge UI.
