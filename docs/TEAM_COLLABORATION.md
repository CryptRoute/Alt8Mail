# Team Collaboration Extensions (Phase 2F)

This slice extends the existing tasks, notes and Cabinet surfaces rather than
replacing them. The webmail remains the client for daily work; the APIs below
are also usable by future clients.

## Tasks

`tasks` now supports:

- `parentTaskId` and `dependsOnTaskId` (same tenant/accessible task; self and
  parent cycles are rejected)
- `kanbanStatus`: `todo`, `in_progress`, `review`, `done`
- `recurrenceRule` / `reminderAt` storage (RFC3339 reminder; recurrence is a
  single-line rule such as `FREQ=WEEKLY;BYDAY=MO`)
- the existing assignment, tags, due date, email link and calendar link

Task creation from email already exists through the linked-thread action; the
task editor now exposes kanban, parent/dependency, reminder and recurrence
fields. Completing a task moves it to `done`; reopening returns it to `todo`
unless an explicit kanban state is sent.

## Files

New file collaboration endpoints:

```text
GET  /api/files/{id}/versions
POST /api/files/{id}/versions                 # snapshot the current file
POST /api/files/{id}/versions/{version}/restore
GET  /api/files/{id}/comments
POST /api/files/{id}/comments
POST /api/files/{id}/comments/{commentId}/resolve
GET  /api/files/{id}/approvals
POST /api/files/{id}/approvals                # {approverEmail, note}
POST /api/files/{id}/approvals/{approvalId}/decide  # approved|changes_requested|rejected
```

Viewers can read and comment. Owners/editors can snapshot versions and request
approval; only the owner restores a version. The approver or owner decides.
File access continues to use the existing owner/user/group permission model.
Shares remain expiring links with the existing revoke/access audit.

## VIP senders and thread muting

```text
GET    /api/vip-senders
POST   /api/vip-senders             { "email": "…" }
DELETE /api/vip-senders/{email}
GET    /api/thread-mutes
POST   /api/threads/{id}/mute
DELETE /api/threads/{id}/mute
```

These are per-user controls. Bootstrap returns `vipSenders` and `threadMutes` so
the conversation bar can show **Mark VIP** and **Mute** toggles. Muting a
thread is a user preference and does not suppress security or assignment
notifications in this slice.

## Meeting links

Compose has a **Jitsi** action that inserts a fresh
`https://meet.jit.si/a8m-…` link into the message. No media server, recording or
calendar provisioning is added; the link is ordinary text.

## Notes and wikis

Existing sticky notes already support shared-mailbox access, links/backlinks and
search. This slice does not add a separate wiki model. Shared wikis, @-page
mentions and mail→note remain follow-up work.

## Limits

Task recurrence is stored but not yet expanded by a scheduler; reminders are
stored but not yet dispatched by a worker. File versions capture metadata and
storage pointers, not a new byte copy. Large-file link conversion and file
approval UI are API-first in this slice. Jitsi links assume the public
meet.jit.si service; self-hosted deployments can substitute their own URL.
