# Phase 4 Implementation Notes

Phase 4 turns Alt8Mail's AI from a per-message assistant into a governed
workspace layer: scoped MCP tools, a tenant AI budget, triage, obligations,
natural-language search, sender/meeting intelligence, document AI and DLP
policy assist. Migration `0075_ai_moat.sql` adds the shared tables and
tenant AI policy columns.

## 4A MCP governance and AI budget

- MCP scopes now include `calendar.write`, `tasks.write`, `labels.write`,
  `draft.send` and `crm.read`. Write tools are listed only when the matching
  scope was granted.
- Write and send tools never execute from a tool call. They create a pending
  `mcp_action_requests` row; a signed-in user approves or denies it in
  **Settings → Security & privacy → AI actions waiting for review**. Dry-run
  (`dryRun` or `_meta.dryRun`) returns `pending_approval` without writing.
- Organization policy (`policy.mcp`) controls the allowed scope list, the
  default approval requirement, a per-tool matrix (`inherit`, `approval`,
  `auto`, `deny`) for `calendar_write`, `tasks_write`, `labels_write` and
  `draft_send`, and an hourly per-connection request budget. Request counts use
  `mcp_rate_events` with a PostgreSQL advisory lock, so the limit is enforced
  across server instances.
- The MCP connection inventory and revocation remain in the same settings
  page. Authorization pages explain that write scopes require review.
- `crm_read` returns only active CRM connector names. Deep CRM record access
  is Phase 6 work.
- AI calls record usage in `ai_usage`. A tenant monthly budget in
  `tenant_ai_settings.monthly_budget_cents` caps requests; when the estimated
  cost reaches the cap, AI features return a budget error until the next month
  or until an administrator raises the limit. The estimate is deterministic:
  1 unit per input character and 4 per output character. System Control shows
  usage and spend and lets administrators set the budget in dollars.
- Tests: `TestPhase4MCPGovernanceIntegration` covers write scope mapping,
  dry-run, missing arguments, approval execution, denial, tenant isolation,
  organization scope policy, rate limits, CRM read and revocation.
  `TestPhase4AIBudgetIntegration` covers cap enforcement (the provider is not
  called), usage recording and the usage endpoint.

## 4B AI triage / priority inbox

Migration `ai_triage`, `ai_triage.go`, webmail **Settings → AI workspace**.

- Categories are `action`, `fyi`, `newsletter`, `spam_like` and
  `needs_reply`, each with confidence, a short explanation and the model name.
- Email content is delimited and described as untrusted data in the prompt;
  the parser rejects unknown categories and clamps confidence.
- Results are stored per user/message and re-triage is idempotent.
- When `triage_auto_label` is on and confidence meets
  `triage_min_confidence`, an `AI/<category>` label is added. Low-confidence
  results remain suggestions. No message is moved, deleted or sent by triage.
- Users can mark a result correct or wrong and optionally supply the corrected
  category. Feedback is stored for evaluation.
- Tests: parser validation, stored categories/confidence, auto-label threshold,
  re-triage idempotence, feedback and cross-user isolation.
- Known limit: auto-labelling writes the label directly; it does not yet invoke
  a Phase 2 automation rule action.

## 4C Obligations ledger

Migration `ai_obligations`, `ai_obligations.go`, webmail AI workspace.

- Extracts promises, requests and deadlines from a thread with kind, text,
  supporting quote and optional due date. Blank, low-confidence and invalid
  entries are discarded.
- Obligations link to the source thread/message and are deduped by a normalized
  text/kind/due key, so repeated extraction does not duplicate rows.
- When organization policy `obligations_auto_task` is on (or the request asks
  for tasks), linked tasks are created without duplicating existing tasks.
- Inbound `request` obligations resolve when the mailbox owner has sent a later
  reply in the thread. Outbound promises remain open until completed.
- Views: open/complete/dismiss filters, a `waiting=1` request view, and linked
  task completion. The webmail lists open obligations with Complete, Dismiss
  and Create task actions.
- Tests: parser false positives, dedupe, auto tasks, reply resolution, waiting
  view, task completion and cross-user isolation.

## 4D Natural-language search

`ai_search.go`, webmail AI workspace.

- The model converts a question into structured filters: keywords, subject,
  from, to, folder, unread, starred, attachments, after and before. A
  deterministic parser is used when AI is disabled or the model output is not
  usable.
- Search uses PostgreSQL full-text search plus metadata filters and returns
  only the signed-in user's messages. Every result includes a message citation
  with id, subject, sender, excerpt and timestamp; file name/path matches are
  returned as separate file citations.
- Invalid dates are dropped instead of guessed. Empty/ambiguous filters fall
  back to deterministic parsing. All values are parameterized.
- Tests: parser and fallback, keyword/date search, empty results, attachment
  table correctness, injection-shaped input and cross-tenant isolation.

## 4E Sender intelligence and meeting prep

`ai_insights.go`, webmail AI workspace.

- Sender profile: name, company/role from the user's contacts, message counts,
  unread/last-30-day counts, first/last seen, first-contact badge, recent
  messages, open obligations, attachment names and active CRM connector names.
- Meeting prep: calendar event or thread context, attendee snapshots,
  related messages, open obligations, attachment names, CRM connector names
  and a privacy note.
- AI summaries are grounded in the supplied facts and are omitted when AI is
  disabled, unavailable or over budget; structured facts remain available.
- Tests: populated and missing sender profiles, meeting briefs, missing events,
  stale data and mailbox scoping.

## 4F AI document assistant

`office_ai.go`, `office_editor.js` / `office_editor.html`.

- Existing summarize/rewrite/translate/continue/custom actions remain.
- New actions: `ask` (Q&A), `action_items` and `draft_reply`, all tied to a
  `documentId`.
- The server reads the latest Office version only after
  `fileAccessForUser` confirms Cabinet access; a different user gets 404.
- Q&A citations and action-item quotes are verified verbatim against the
  document text. Fabricated quotes are removed; unsupported action items are
  dropped. Action items can be turned into tasks with `createTasks: true`.
- Draft replies run through tenant DLP. A blocking rule returns 422 without
  returning the generated text; warnings and sanitized matches are included.
- Tests: document permission, citation accuracy, fabricated citation removal,
  DLP block/allow and missing document validation.

## 4G AI policy assist

`ai_policy_assist.go`.

- Runs the local DLP rule engine, returns the enforced and suggested
  classification, warnings and matches without echoing matched terms.
- Matched terms are redacted before any text is sent to the model provider.
  AI explanations are redacted again before return.
- Classification overrides are restricted to organization administrators and
  written to the audit log from/to the requested classification.
- Tests: redaction, provider payload redaction, suggestion, administrator-only
  override and audit evidence.

## Verification

- `go test ./...`, `go vet ./...`, `go build ./...`
- PostgreSQL `internal/httpapi` suite with Phase 4 DSN tests
- `npm run test:all`
- `npm run test:a11y`
- Phase 4 migration `0075` rollback/reapply cycle

## Known limits

- Live AI precision/recall evaluation against a labeled corpus is an operator
  acceptance step; the committed tests are deterministic parser, threshold,
  permission and storage tests with a local fake provider.
- MCP per-tool approval and distributed per-connection rate limits are
  implemented; live multi-node soak validation remains an operator step.
- `crm_read`, sender profiles and meeting prep expose connector names only;
  deep CRM record sync and deal links remain Phase 6.
- Document Q&A citations point into the document. Linking a citation back to the
  originating mail thread/attachment is follow-up work.
- Obligation nudges are represented by linked tasks; the Phase 3 follow-up
  worker is not automatically scheduled from an obligation.
- Triage auto-labelling does not yet execute Phase 2 automation rules; it adds
  the `AI/...` label directly.
