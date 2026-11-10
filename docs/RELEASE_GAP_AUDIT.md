# Release Gap Audit — Phases 1–6

Audit date: 2026-11-10. Scope: full local tree after Phase 6, before version
bump, push, tag or release. Status codes: **blocker**, **operator** (live
external validation), **follow-up** (accepted post-release).

## Executive summary

The six phases have a substantial working v1 implementation and the automated
gates are green. This audit does **not** certify release readiness. The
following are release blockers or require an explicit product decision:

1. **Paid entitlements are incomplete.** `s.entitled(...)` gates only a subset
   (`sso`, `saml`, `scim`, `shared_inbox`, `shared_drafts`, `team_templates`,
   `automation`, `mail_merge`, `booking_pages`, `white_label`). Phase 3 follow-
   ups/gallery/DMARC/tracking, Phase 4 AI, MCP governance, all Phase 5 controls
   and Phase 6 paid integrations/analytics/migration/security-awareness have no
   entitlement gate. Decide feature keys and reissue/backfill licenses before
   release, otherwise paid scope is available in OSS deployments.
2. **Phase 5 encryption gate is not met.** BYOK encrypts tenant secrets (AI
   credentials) only. Mailbox rows, Office binaries, Cabinet blobs and
   client-side encrypted folders are not application-encrypted; data-residency
   policy is recorded but not enforced by storage placement.
3. **Phase 6 integration scope is incomplete.** Native Gmail API/Microsoft Graph
   importers, Salesforce/Zoho/Pipedrive sync, Slack/Teams interactive actions,
   dual delivery and external free/busy federation are not implemented. HubSpot
   sync and existing add-ons are the shipped paths.
4. **Release mechanics are not started.** `VERSION` is `0.27.6`, while
   `package.json` is `3.0.0`; there are more than 120 commits since `v0.27.6`,
   with no release notes/tag. Bump must be decided and performed only after the blockers above
   are resolved.
5. **Dependency/performance CI is not enforced.** There is no npm lockfile, no
   `govulncheck` in the gate, and performance budgets in `docs/PERFORMANCE.md`
   are not checked in CI. Staging/soak validation is still operator work.

## Fixed during this audit

- Public API `/api/v1/messages/send` and approved MCP `draft_send` now apply
  tenant outbound policy, outbound caps and DLP before queueing. Previously they
  inserted directly into `outbox`.
- Webhook/telemetry URL validation now resolves DNS and rejects private/reserved
  targets (SSRF hardening); local endpoints remain test-only behind an explicit
  test seam.
- File legal-hold creation now verifies that an explicitly supplied user belongs
  to the active tenant.
- Booking slot validation now compares the end time in the booking page's
  timezone instead of UTC; this was rejecting valid late-day slots.
- `tools/migration-upgrade-smoke.sh` passed for all migrations after `v0.27.6`,
  including rollback and reapply through `0080_ecosystem`.

## Phase-by-phase missing / partial scope

### Phase 1 — Enterprise entry

- Status: implemented; operator gates remain.
- **operator:** live Entra/Okta/Google/Keycloak sign-in matrix.
- **operator:** live `doveadm` mailbox restore drill on staging.
- **operator:** real screen-reader/WCAG review; automated a11y is green only.
- Residual: in-process SAML replay cache; per-IP SAML limiter not global.

### Phase 2 — Team operations and collaboration

- Status: implemented; some advertised UI details are incomplete.
- **follow-up:** task recurrence and reminder values are stored but no scheduler
  creates/dispatches recurring tasks or reminders.
- **follow-up:** auto-replies remain staged drafts, not automatically sent.
- **follow-up:** file "versions" store metadata/storage pointers, not a new byte
  copy; shared-draft anchored comment pins and restore UI are not rendered in
  the editor.
- **operator:** three-agent shared-inbox workflow and MCP consent page on staging.

### Phase 3 — Scheduling, revenue and deliverability

- Status: core implemented; several paid items are partial.
- **blocker (entitlement):** follow-ups, template gallery/payment links, DMARC,
  campaign tracking and recipient scheduling have no entitlement checks.
- **follow-up:** external CalDAV free/busy and find-a-time federation are absent;
  only internal free/busy and utilization exist.
- **follow-up:** calendar resource approval workflow is absent (utilization only).
- **follow-up:** DMARC ingestion is API-driven; no scheduled rua mailbox poller.
- **follow-up:** campaign tracking wraps mail-merge HTML only, not all outbound.
- **follow-up:** booking edit/delete/cancellation UI is missing.
- **operator:** live booking/DMARC/merge workflows on staging.

### Phase 4 — AI/MCP moat

- Status: implemented v1; governance and evaluation gaps remain.
- **blocker (entitlement):** AI triage/obligations/search/meeting prep/Office AI
  and advanced MCP governance are not gated.
- **operator:** labeled AI evaluation corpus and precision/recall thresholds.
- **operator:** real MCP client and prompt-injection corpus against staging.
- **follow-up:** triage labels directly instead of invoking a Phase 2 automation
  rule; obligations create tasks but do not schedule nudges; document citations
  verify document text, not the source mail thread. Auto-labeling and
  obligation auto-task creation are policy-gated, not per-action approval-gated,
  despite the Phase 4 acceptance wording.
- **follow-up:** MCP policy is scope-wide; no per-tool approval matrix or
  per-connection distributed rate limit.

### Phase 5 — Compliance and data governance

- Status: controls implemented; release gate not met for encryption.
- **blocker:** application-level encryption for mailbox/Office/Cabinet blobs and
  client-side encrypted Cabinet folders.
- **blocker (entitlement):** all Phase 5 controls lack entitlement gates.
- **operator:** real OCR sidecar (`A8M_OCR_COMMAND`) and scanned-PDF fixtures;
  committed OCR tests use a deterministic fake.
- **operator:** labeled DLP accuracy corpus and false-positive thresholds.
- **follow-up:** send-time DLP scans subject/body and attachment names, not
  attachment binaries/OCR text; watermarks are headers/footers, not embedded;
  eDiscovery JSON/MBOX includes file metadata, not Cabinet bytes; data-residency
  is policy-only; audit bundles are HMAC-signed without an external timestamp
  anchor.
- **operator:** BYOK lost-key/rotation drill on staging and key-ceremony review.

### Phase 6 — Ecosystem and scale

- Status: API platform, HubSpot sync, fidelity, analytics and productivity
  slices implemented; integration and offline acceptance partially met.
- **blocker (scope/decision):** native Gmail API/Microsoft Graph importers,
  deep Salesforce/Zoho/Pipedrive sync, Slack/Teams interactive callbacks and
  dual delivery/external free/busy are absent.
- **blocker (entitlement):** paid migration/analytics/security-awareness and
  API governance have no entitlement gates.
- **follow-up:** offline queue covers compose/send only; archive/label/task
  mutations are not queued. API v1 is limited to mail/task/calendar and has no
  cursor pagination; telemetry is manual; MDM packaging is the existing PWA/
  manifest path only.
- **follow-up:** command palette exists, but shortcut customization/help,
  high-contrast theme and per-folder density are absent.
- **operator:** live Google/M365 coexistence and migration rehearsal on staging.
- **operator:** performance budgets and dependency scanning in CI.

## Release decision checklist

Before bumping:

- [ ] Decide entitlement feature keys and which Phase 3–6 capabilities are paid.
- [ ] Backfill/reissue customer licenses or provide a migration policy.
- [ ] Decide whether Phase 5 encryption gate is satisfied by infrastructure
      encryption or needs application/client-side work first.
- [ ] Accept or implement the Phase 6 integration/coexistence gaps.
- [ ] Run the staging operator gates (IdP, restore, MCP, AI eval, OCR/DLP,
      team workflows, migration fidelity).
- [ ] Add dependency/performance checks to CI.
- [ ] Choose release version; update `VERSION`, `package.json`, release notes.
- [ ] Re-run full Go, DSN, npm, a11y, upgrade smoke and staging smoke gates.
- [ ] Tag, push and release only with separate authorization.
