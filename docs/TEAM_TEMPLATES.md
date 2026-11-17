# Team Templates and Alias Controls

Team templates are shared canned responses for a team inbox. They require a
license with `team_templates` or `shared_inbox` and membership in the team's
agent group.

## Model

- `team_templates`: name, category, optional sending alias, subject, text/HTML
  body, extracted variables, version, active state and creator/editor.
- `team_template_versions`: immutable JSON snapshots for every version.
- Variables use `{{name}}` (letters, digits, `_`, `.`, `-`; up to 64 chars).
  The server extracts them automatically and caps a template at 30 variables.
- An alias template must reference an active alias owned by the team's backing
  mailbox. This prevents an agent from sending through an unrelated identity.

Only a team group owner or a tenant owner/admin can create, edit or archive
templates. All active group members can list and use them. Changes are audited.

## API

```text
GET    /api/team-templates?teamId={teamId}&category={category}
POST   /api/team-templates
GET    /api/team-templates/{id}
PATCH  /api/team-templates/{id}
DELETE /api/team-templates/{id}
GET    /api/team-templates/{id}/versions
POST   /api/team-templates/{id}/render
```

Create/update body:

```json
{
  "teamId": "…",
  "name": "Refund received",
  "category": "Billing",
  "aliasEmail": "billing@example.com",
  "subject": "Refund for {{order_id}}",
  "bodyText": "Hi {{name}}, …",
  "bodyHtml": "<p>Hi {{name}}, …</p>",
  "active": true
}
```

`POST …/render` accepts `{"variables":{"name":"Avery","order_id":"A8-1042"}}`
and returns the substituted `subject`, `bodyText`, `bodyHtml`, `aliasEmail`
and version. HTML values are escaped during substitution; text values are
inserted literally. Missing variables remain as `{{name}}` so nothing is
silently dropped.

## Compose integration

The compose footer has a template button. When composing from a team folder or
replying to a team thread, it lists the team's templates. Choosing one prompts
for any variables, renders it and inserts the subject/body (and alias when
allowed) into the current compose session. The normal autosave path then
persists the result.

## Alias controls

Alias-level signatures already exist through identity signatures. This slice
adds alias-scoped templates and validates template aliases against the backing
mailbox's active aliases. Catch-all routing and domain-level alias rules remain
in the Control Plane (`mail_domains.catch_all` and Sieve policy).

## Limits

No visual template editor, approvals, per-category permissions or localized
template variants in v1. Version snapshots are stored but the webmail has no
restore UI yet; use the versions API.
