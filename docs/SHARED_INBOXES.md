# Shared Inboxes

Shared inboxes are a paid Phase 2 feature gated by the `shared_inbox` license
entitlement. A team inbox wraps an existing mailbox owner (the account whose
mailbox stores the messages) with an active group of agents. Mail stays in the
owner's normal Maildir and IMAP account; agents work it through the existing
delegated-mailbox window.

## Model

- `public.team_mailboxes`: tenant, display name, queue address, backing mailbox
  owner, agent group, assignment strategy (`manual` or `round_robin`), SLA
  targets, send permission and active state.
- `public.team_thread_state`: first-response and resolution targets, breach
  evidence and last-agent activity per thread.
- `public.team_thread_presence`: short-lived viewing/replying heartbeats used
  for collision detection.
- `public.team_thread_watchers`: agents following a conversation.
- `public.team_assignment_log`: auditable assignment history.

One active team inbox is allowed per backing owner and per group. Owner and
group are immutable after creation; delete and recreate to change them.

## Administration

Tenant administrators with `groups.read` / `groups.manage` manage team inboxes
in **Admin → Team inboxes** or through:

```text
GET    /admin/api/team-inboxes
POST   /admin/api/team-inboxes
PATCH  /admin/api/team-inboxes/{id}
DELETE /admin/api/team-inboxes/{id}
```

Creation requires `name`, `ownerEmail` (an active user with a mailbox) and
`groupId` (an active group in the same tenant). The queue address defaults to
the owner's email and must use a verified tenant domain. SLA minutes are
`0–10080`; zero disables that target.

## Agent API

All routes require a session, the `shared_inbox` entitlement and membership in
the team's group. A non-member receives `404 TEAM_NOT_FOUND` so team existence
is not leaked.

```text
GET    /api/team-inboxes
GET    /api/team-inboxes/{id}
GET    /api/team-inboxes/{id}/queue?filter=all|mine|unassigned|breached
POST   /api/team-inboxes/{id}/threads/{threadId}/assign
POST   /api/team-inboxes/{id}/threads/{threadId}/touch
GET    /api/team-inboxes/{id}/threads/{threadId}/presence
POST   /api/team-inboxes/{id}/threads/{threadId}/presence
DELETE /api/team-inboxes/{id}/threads/{threadId}/presence
GET    /api/team-inboxes/{id}/threads/{threadId}/watchers
POST   /api/team-inboxes/{id}/threads/{threadId}/watchers
DELETE /api/team-inboxes/{id}/threads/{threadId}/watchers
```

`assign` accepts a member email or `strategy: "round_robin"`. Round-robin uses a
row lock on the team's cursor, so concurrent requests hand out distinct agents.
Manual assignment through the existing `PATCH /api/threads/{id}/meta` is also
restricted to active group members when the backing mailbox is a team inbox.

Presence TTL is 15–300 seconds (default 120). `GET .../presence` returns active
agents and `collision: true` when another agent is replying. A reply/status
change records the first response; assignment sets the SLA due times once and
does not reset them.

The webmail shows the team folder as **Team name · Inbox**, a shared-inbox
badge with unassigned/breach counts, team-restricted assignment options and a
**Claim** action in the conversation handling bar.

## Audit and isolation

Assignment, status and note changes use the existing thread audit. Team
inboxes are resolved from the live owner mailbox; a request cannot address a
path or another tenant's backing mailbox. Queue reads are restricted to the
team's owner mailbox, and only group members can see the team.

## Limits

No per-agent capacity limits, business-hours calendars, automatic escalation
targets, or cross-team transfer in v1. Round-robin skips inactive/suspended
users but does not consider current workload. Presence is advisory and expires
automatically; it is not a hard write lock.
