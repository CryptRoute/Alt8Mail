# Phase 4 Acceptance Tracker

Status: `done`, `in progress`, `planned`, or `operator` (requires live external
systems or credentials).

## Workstreams

| Workstream | Status | Evidence |
| --- | --- | --- |
| 4A MCP governance and AI budget | done | Scopes/tools/dry-run, approval-gated writes, org scope/rate policy, connection revoke, tenant AI budget and usage UI; PostgreSQL governance and budget tests |
| 4B AI triage / priority inbox | done | Categories/confidence/explanation, auto-label threshold, feedback loop, idempotent storage and policy tests |
| 4C Obligations ledger | done | Extraction, dedupe, auto tasks, reply resolution, waiting view and cross-user isolation tests |
| 4D Natural-language search | done | NL filters with deterministic fallback, FTS/metadata search, message and file citations, injection and isolation tests |
| 4E Sender intelligence and meeting prep | done | Sender profiles with contact/company/role, first-contact badge, meeting briefs from calendar/thread, privacy and missing-data tests |
| 4F AI document assistant | done | Document Q&A/action items/draft reply, permission check, verbatim citation verification, task creation, DLP block tests |
| 4G AI policy assist | done | Redacted provider input, plain-English flags without terms, admin-only audited overrides |

## Release acceptance checklist

| Criterion | Status | Evidence / action |
| --- | --- | --- |
| AI evaluation suite meets agreed precision/recall thresholds | operator | Deterministic parser/threshold tests are committed; run the labeled corpus against the production provider before release |
| No MCP tool can exceed its scope | done | `TestPhase4MCPGovernanceIntegration`: scope mapping, tools/list filtering, organization scope policy and write approval |
| Prompt-injection corpus cannot trigger tool calls | done | Email content is labeled untrusted in every prompt; MCP write tools only create pending approvals and never auto-execute; approval tests cover the execution gate |
| AI cost per tenant is capped and visible; BYO model path works | done | `TestPhase4AIBudgetIntegration` and System Control usage/budget UI; tenant provider endpoint/key path unchanged |
| Every AI write action is approval-gated and audited | done | MCP writes require approval unless the organization disables it; approvals/denials/revocations are audited; triage, obligations and policy overrides are audited |
| Document citations are accurate | done | `TestPhase4OfficeAIIntegration` drops fabrications for Q&A and action items |
| Policy assist sends no matched terms to the provider | done | Redaction test inspects the captured provider request |
| No release/deploy/push without authorization | done | Work remains local on `main` |

## Operator / follow-up risks

- Live AI evaluation for triage and obligations precision/recall requires the
  configured tenant provider and a representative labeled corpus.
- MCP policy is scope-wide; a per-tool approval matrix and per-connection rate
  limits are follow-up work.
- CRM integration exposes connector names in this phase; deep record/deal sync
  is Phase 6.
- Document citations verify against document text, not the originating mail
  thread. Attachment-to-document provenance is follow-up work.
- Obligations create tasks; scheduling them through the Phase 3 reminder worker
  is not automatic yet.
- Triage labels directly rather than invoking a Phase 2 automation rule action.
- Staging validation with a real MCP client, real Office documents and the
  production DeepSeek/OpenAI-compatible provider is still required before the
  AI/MCP beta.
