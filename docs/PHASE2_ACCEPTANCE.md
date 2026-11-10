# Phase 2 Acceptance Tracker

Living evidence map for the Phase 2 team-operations release gate.

Status: `done`, `in progress`, `planned`, or `operator` (requires a live
external system/credential).

## Workstreams

| Workstream | Status | Evidence |
| --- | --- | --- |
| 2A Shared inbox core | done | migration 0064, `backend/internal/httpapi/team_inboxes.go`, admin team-inbox UI, delegated team folders, PostgreSQL assignment/round-robin/SLA/presence tests |
| 2B Shared inbox operations | done | SLA breach state/notifications, claim, handoff notes, presence collision heartbeat, watchers, team queue badge/automation header |
| 2C Shared drafts v1 | done | migration 0065, lease/version/conflict APIs, version history, comments, team-draft webmail flow, stale-write/lease integration tests |
| 2D Team templates and alias controls | done | migration 0066, versioned templates, variables, alias validation, compose picker and team template manager |
| 2E Automation builder v1 | done | migration 0067, conditions/actions, compiled Sieve preview, idempotent runs, dry-run, webhook/task/label/notify/auto-reply, builder UI |
| 2F Team collaboration extensions | done | migration 0068, task subtasks/dependencies/kanban/recurrence/reminders, file versions/comments/approvals, VIP/mute, Jitsi insertion, PostgreSQL integration test |
| 2G MCP server seed | done | migration 0069, OAuth 2.1 PKCE, consent, scoped read/search/draft tools, rate limit, audit, untrusted-content labeling, connection revocation UI, token/scope/revocation tests |

## Release acceptance checklist

| Criterion | Status | Evidence / action |
| --- | --- | --- |
| Shared inbox assignment/SLA workflow | operator | PostgreSQL round-robin, SLA, presence and permission tests pass; run the three-agent workflow on staging |
| Shared draft conflict workflow | done | Version token, lease expiry, stale baseVersion and permission tests pass |
| Automation with webhook and task creation | done | Integration test proves label, task, webhook, idempotent replay and run log |
| No permission test can read another team's mailbox | done | Non-member, cross-tenant and team-scope tests |
| MCP cannot act outside granted scopes | done | Per-tool scope denial, token revocation and read/search/draft-only tests |
| MCP requires consent and is revocable | done | OAuth registration/consent/code exchange implementation; signed-in connection list and revoke; browser consent flow is operator-checked |
| Shared draft stale-write/conflict tests pass | done | `TestSharedDraftIntegration` |
| No release/deploy/push without authorization | done | All work remains local on `main`; no release, deploy or push performed |

## Residual risks to revisit

- The three end-to-end team workflows have automated API/DB coverage but have
  not been run against the live staging Control Plane/webmail.
- MCP browser consent is implemented but not part of the automated CDP suite;
  verify the consent page and dynamic registration with a real MCP client.
- Task recurrence and reminders are stored but not yet expanded/dispatched by a
  scheduler. Auto-replies are staged drafts, not automatically sent.
- File versions capture metadata/storage pointers, not a new byte copy. Jitsi
  links depend on the public meet.jit.si service.
- MCP rate limiting is per process, not distributed. No refresh tokens yet.
- Shared draft comments/version history have APIs but the compose editor does
  not yet render anchored comment pins or a restore UI.
