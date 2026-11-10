# Automation Builder v1

Automation rules are team-inbox workflows gated by the `automation` license
feature. Conditions are evaluated against the cached message metadata; delivery
actions run through an idempotent worker, and manual/dry-run endpoints support
testing.

## Rule model

`public.automation_rules` stores a name, enabled flag, priority, conditions,
actions, compiled Sieve preview and version. `public.automation_runs` stores one
idempotent execution per `(rule, message)` with status, result and error.

### Conditions

At least one of:

- `from` — sender address contains
- `fromDomain` — sender domain equals
- `subjectContains`
- `recipientContains`
- `category` — an existing message category/label
- `hasAttachment` — boolean

### Actions

- `label` — add a category to the cached message
- `move` — change the folder (delivery-time rules also compile a Sieve preview)
- `notify` — create an in-app team notification
- `createTask` — create a task linked to the conversation
- `webhook` — HTTPS POST with rule/message metadata (no message body)
- `autoReply` — stage a reply draft for agent review; messages sent by the team
  mailbox itself are skipped to prevent loops

Webhook URLs must be HTTPS and resolve to a public address. Redirects stay on
HTTPS and are capped. The worker evaluates the latest 50 inbox messages per
active rule over a 14-day window and skips any run already recorded.

## API

```text
GET    /api/automation/rules?teamId={teamId}
POST   /api/automation/rules
PATCH  /api/automation/rules/{id}
DELETE /api/automation/rules/{id}
POST   /api/automation/rules/{id}/dry-run
GET    /api/automation/runs?teamId={teamId}
```

Create/update body:

```json
{
  "teamId": "…",
  "name": "Route urgent renewals",
  "enabled": true,
  "priority": 100,
  "conditions": { "subjectContains": "urgent", "hasAttachment": true },
  "actions": [
    { "type": "label", "value": "urgent" },
    { "type": "createTask", "title": "Handle renewal", "priority": "high" },
    { "type": "webhook", "url": "https://hooks.example.com/a8m" }
  ]
}
```

`POST …/dry-run` with `{"messageId":"…"}` returns `matched`, `wouldRun` and the
actions that would execute; it writes no run, task, webhook or message change.

A team group owner or tenant owner/admin manages rules. All team members can
list rules/runs and dry-run. Creation, updates and deletes are audited.

## Webmail

Selecting a team folder shows an **Automation** button. The dialog lists rules
with Edit / Enable / Delete and a compact builder for conditions and actions.
Saving displays the compiled Sieve preview when a move action can run at
delivery time. Execution history is available through the runs API.

## Limits

No visual drag-and-drop graph, arbitrary header conditions, loop tracing beyond
the team self-send guard, scheduled rule windows, or per-rule rate budgets in
v1. Webhooks carry metadata only. Auto-replies are drafts, never silently sent;
that is a deliberate safety constraint for v1.
