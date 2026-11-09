# Alt8Mail Paid Platform Roadmap and Phase Plans

> **For agentic workers:** This is a program-level roadmap for review, not an executable task plan. Do not implement from it directly. After approval, split each phase into per-subsystem implementation plans using `superpowers:writing-plans`, one plan per workstream, with exact files, signatures, migrations and tests. The first plan to write is Phase 1A (identity and entitlements).

**Goal:** Turn Alt8Mail from a broad self-hosted mail suite into a paid, AI-native collaboration and compliance platform for regulated self-hosters, without weakening the open-source core.

**Architecture:** Six overlapping phases: enterprise entry, team operations, scheduling/revenue, AI/MCP, compliance/data governance, and ecosystem/scale. Revenue blockers ship first; the AI/MCP moat follows on top of the identity, audit and entitlement foundations. Every paid capability is enforced server-side and has an OSS on-ramp.

**Tech Stack:** Go HTTP API, PostgreSQL/pgx, existing Control Plane, vanilla JS/CSS webmail, IMAP/SMTP, CalDAV/CardDAV, Sieve, OnlyOffice, Docker/PostgreSQL deployment scripts.

**Spec:** The owner’s 2026-11-09 prioritized product brief, captured in §3. This document is the authoritative scope and sequencing decision for that brief.

## Global Constraints

- Do not build native iOS/Android apps, internal chat/presence, native video meeting infrastructure, shared-draft CRDT co-editing, or PST export in this program.
- Jitsi/Meet **link insertion** is in scope; hosting a media server is not.
- Shared drafts v1 may use locking/section ownership and comments; CRDT is deferred.
- eDiscovery export is MBOX/JSON first; PST is deferred.
- OSS core remains usable without a license: mail, calendar, files, tasks, notes, DAV, Sieve, PWA, basic admin and user-triggered AI with bring-your-own model.
- Paid capabilities must be enforced server-side, never by hiding UI alone.
- Offline/air-gapped installs must activate and run without a mandatory phone-home.
- Every schema change ships with a rollback migration and is covered by upgrade tests.
- Existing release gates remain: `npm run test:all`, `go test ./...`, `go vet ./...`.
- AI features must be tenant-scoped, audited, consent-based, and must not send mailbox contents to a provider without the tenant’s configured policy.
- Accessibility target: WCAG 2.1 AA for webmail and Control Plane; RTL regressions are release-blocking for supported RTL locales.
- Existing deployment assumptions (PostgreSQL, Dovecot, Postfix, OnlyOffice, installer/updater) remain supported; no new mandatory external SaaS dependency.

## Review Focus

These are the failure modes most likely to hurt a real administrator or user, and each must be pinned by tests in the phase that owns the code:

1. **Entitlement bypass or lockout:** a paid feature works without a valid license, or a legitimate expired/grace/air-gapped license locks an administrator out. *Owning tests: Phase 1 license/entitlement suite.*
2. **Identity collision/lockout:** SSO/SCIM creates duplicate accounts, removes the last administrator, or leaves sessions alive after deprovisioning. *Owning tests: Phase 1 SSO/SCIM suite.*
3. **Shared-inbox permission and race failures:** a user sees another team’s mail, an SLA timer double-fires, or two agents reply to the same assignment. *Owning tests: Phase 2 shared-inbox suite.*
4. **AI/MCP exfiltration, prompt injection and cost runaway:** an agent reads mail it was not scoped to, follows instructions embedded in message content, or exhausts a tenant’s AI budget. *Owning tests: Phase 4 MCP/AI security and budget suite.*
5. **Upgrade/restore/migration data loss:** a phase migration, restore, or importer loses flags, attachments, calendar parts, or leaves the mailbox in an unrecoverable state. *Owning tests: Phase 1 restore/upgrade suite plus every phase’s migration rollback test.*

## 1. Strategic Decision

Alt8Mail should not compete on feature count with Gmail, Outlook or Zoho. It should compete on a narrower promise:

> **Sovereign, AI-native mail for organizations that must run their own infrastructure and prove control over their data.**

The first paid cohort should be 10–25 organizations with 50–500 seats, typically regulated or privacy-sensitive: legal, healthcare, finance, public sector, MSPs and EU/self-hosting-first companies. That cohort buys:

- identity and provisioning that their security team will approve;
- team workflows that replace shared-mailbox hacks;
- AI that reduces administrative work without exporting the mailbox;
- compliance evidence, retention and data-control features;
- a supportable, upgradeable deployment they can run themselves.

The program is a menu until it is sequenced. The sequence below deliberately buys **revenue permission** before **moat depth**.

## 2. Edition Split

### OSS core

- Mail, calendar, contacts, files/Cabinet, tasks, notes, DAV, Sieve.
- PWA, web push, offline shell, responsive webmail.
- Basic admin, backups, imports, audit log.
- User-triggered AI (summaries, translation, drafting) with a tenant-provided model key/provider.
- MCP server with read/search/draft scopes, local consent and audit.
- Basic rules, signatures, aliases, forwarding and vacation.
- Single-tenant and self-hosted deployment.

### Paid self-hosted edition

- SSO (OIDC/SAML), SCIM, group-to-role mapping, enforced session policy.
- Offline signed license, seat management, entitlements, support channel.
- Shared inboxes, SLA timers, collision detection and round-robin assignment.
- Shared drafts v1, team templates and approval workflows.
- Visual automation builder and AI classification actions.
- Native mail merge, booking pages and campaign tracking (opt-in).
- AI triage, obligations, NL search, meeting prep and deep CRM sync.
- Advanced DLP/OCR, eDiscovery/MBOX, retention by classification, BYOK/per-tenant keys, client-side encrypted folders.
- White-label, tenant analytics and premium support.
- MCP governance and organization-wide AI policy controls.

### Boundary rules

- A paid flag never disables basic mail delivery, login, backups or data export.
- Paid entitlement is a server capability, not a frontend route guard.
- An OSS deployment can export its data and leave without a license.
- The public MCP server is OSS to seed adoption; governance and autonomous actions are paid.
- AI basic usage is OSS with bring-your-own key; managed AI and advanced AI workflows are paid add-ons with quotas.

## 3. Requirements Inventory and Phase Map

| # | Requested capability | Phase | Edition | Notes / dependency |
|---|---|---|---|---|
| 1 | Send at recipient’s local time | 3 | Paid convenience | Reuse existing recipient-time suggestions in schedule picker |
| 1b | Configurable longer undo-send window | 3 | Paid policy | Tenant-configurable window; existing short hold remains default |
| 2 | Boomerang / nudge if no reply; remind if I don’t reply | 3 | Paid | Tasks, notifications, commit extraction |
| 3 | Send-later + follow-up automation | 3 | Paid | Depends on Phase 2 automation engine |
| 4 | Shared drafts / collaborative compose (non-CRDT) | 2 | Paid | Lock/section ownership, comments, presence; CRDT deferred |
| 5 | Native mail merge / personalized bulk send | 3 | Paid | Templates, CSV, throttle, suppression, bounce handling |
| 6 | Visual no-code rules builder | 2 | Paid | Reuses Sieve for delivery-time actions; worker for async actions |
| 7 | Catch-all, per-alias rules and alias-level signatures | 2 | Paid | Alias scope and precedence |
| 8 | Large-file links, expiring links, attachment text search | 2/5 | Paid | Link conversion Phase 2; extraction/OCR search Phase 5 |
| 9 | Shared email templates / canned responses | 2 | Paid | Team variables, permissions, versioning |
| 10 | Contact enrichment and sender intelligence | 4 | Paid | Contact data and connected CRM records plus interaction history |
| 11 | Read receipts and link tracking (opt-in) | 3 | Paid, policy-gated | Tenant policy, transparency, privacy review |
| 12 | Thread muting, VIP senders, AI priority inbox | 4 | Paid AI; VIP/mute Phase 2 | Triage classification in Phase 4 |
| 13 | Offline read, queued actions, compose/send offline | 6 | OSS/Paid | PWA hardening; no native mobile |
| 14 | Booking pages / scheduling links | 3 | Paid | Availability, buffers, round-robin, external free/busy |
| 15 | External free/busy and find-a-time | 3 | Paid | CalDAV federation and privacy controls |
| 16 | Jitsi/Meet link insertion | 2 | OSS | Link generation only; no media server |
| 17 | Calendar resource approvals and utilization | 3 | Paid | Resource booking workflow and reports |
| 18 | Tasks ↔ calendar two-way and time-blocking | 2 | OSS/Paid | Task due dates on calendar; drag to reschedule |
| 19 | Tasks: subtasks, dependencies, kanban, recurring, assignment, reminders, email→task | 2 | OSS/Paid | Core tasks OSS; team assignment/kanban paid |
| 20 | Notes backlinks, @-page mentions, shared wikis | 2 | OSS/Paid | Notes desk, wiki mode, permissions |
| 21 | File versioning UI, comments, approvals, retention, share DLP | 2/5 | Paid | Version UI Phase 2; approvals/retention Phase 5 |
| 22 | OCR and full-text search across Cabinet | 5 | Paid | Extraction worker and index |
| 23 | Client-side encryption for sensitive folders | 5 | Paid | Key management and recovery UX |
| 24 | AI triage / priority inbox | 4 | Paid AI | Action/FYI/newsletter/needs-reply with confidence |
| 25 | AI follow-up tracker / obligations ledger | 4 | Paid AI | Extends existing commit extraction |
| 26 | Natural-language search | 4 | Paid AI | NL→structured query with citations |
| 27 | AI meeting prep | 4 | Paid AI | Mail + calendar + files + connected CRM records |
| 29 | AI document assistant in OnlyOffice | 4 | Paid AI | Summarize/rewrite/translate/Q&A with citations |
| 30 | AI policy/DLP assist | 5 | Paid AI | Classification suggestions, plain-English flags |
| 31 | MCP server | 2/4 | OSS core; paid governance | Read/search/draft first; scoped consent/audit |
| 32 | Edit attachment in place from mail | 2 | OSS/Paid | Attachment→Cabinet→Office→re-attach |
| 33 | Co-editing presence/comments/track changes surfaced in thread | 2/4 | Paid | Shared drafts v1; Office track changes surfacing |
| 34 | Templates gallery (letters, invoices, contracts) | 3 | Paid | Packaged templates |
| 35 | AI in editor + summarize doc and draft reply | 4 | Paid AI | Same document assistant |
| 36 | DLP/legal hold on documents and eDiscovery | 5 | Paid | eDiscovery MBOX; PST deferred |
| 37 | SSO (OIDC/SAML) + SCIM | 1 | Paid | Revenue blocker and identity foundation |
| 38 | Per-tenant encryption keys / BYOK | 5 | Paid | Envelope encryption, rotation, recovery |
| 39 | Data residency and retention by classification | 5 | Paid | Policy engine and audit |
| 40 | Expanded DLP: card/IBAN/national IDs/custom regex, OCR, watermark, prevent-forward | 5 | Paid | Detector library and policy simulation |
| 41 | eDiscovery cross-doc search, hold, review, produce; MBOX export | 5 | Paid | PST deferred |
| 42 | Phishing simulation / security awareness add-on | 6 | Paid add-on | Ties to threat intel |
| 43 | DMARC aggregate report ingestion and dashboard | 3 | Paid | Deliverability value; report mailbox and aggregation |
| 44 | Audit log export and prebuilt SIEM dashboards | 5 | Paid | Splunk/Sentinel/Elastic packs |
| 45 | MDM-friendly PWA, app passwords, DAV; native apps | 6 | OSS/Paid | Native apps deferred |
| 46 | Gmail API / Microsoft Graph importers | 6 | Paid | Mail+calendar+contacts+drive import |
| 47 | Offline license, seat counts, expiry, feature flags | 1 | Paid | Air-gapped activation |
| 48 | White-label custom domain/login/footer | 1 | Paid | Tenant branding and certs |
| 50 | Tenant usage analytics | 1/6 | Paid | v1 for licensing; full analytics Phase 6 |
| 51 | Tenant-scoped public API v1, API keys and webhooks | 6 | OSS/Paid | Versioned API and event subscriptions |
| 52 | Backup/restore UI, DR runbooks, per-folder restore | 1 | Paid | Uses existing backups; restore audit |
| 53 | Command palette | 6 | OSS | UI polish |
| 54 | Keyboard shortcut help and customization | 6 | OSS | UI polish |
| 55 | Shared inbox / team inbox with round-robin, SLA, collision detection | 2 | Paid | Strong support-team revenue feature |
| 56 | Deep CRM sync (HubSpot, Salesforce, Zoho, Pipedrive) | 6 | Paid | Builds on Phase 4 sender intelligence and connector auth |
| 57 | Slack/Teams send-to-channel, reply, approvals | 6 | Paid | Interactive actions |
| 58 | Jira/Linear email→issue and issue updates | 6 | Paid | Integration adapters |
| 59 | Generic webhooks / Zapier / n8n / Make | 6 | OSS/Paid | Automation API |
| 60 | Google Workspace / M365 coexistence, dual delivery, directory sync | 6 | Paid | Migration and coexistence |
| 61 | Payment links (Stripe) in email/templates | 3 | Paid | Templates and tracking |
| 62 | Accessibility WCAG 2.1 AA and screen-reader audit | 1/6 | OSS/Paid | Baseline Phase 1; full audit Phase 6 |
| 63 | RTL polish in compose/calendar | 6 | OSS | Release-blocking for RTL locales |
| 64 | Theme, high-contrast and density controls | 6 | OSS | Mostly existing; add per-folder density |
| 65 | Internal chat/presence and email→chat | Deferred | — | Explicitly excluded |
| 66 | Native video conferencing | Deferred | — | Link insertion remains |
| 67 | Shared-draft CRDT co-editing | Deferred | — | Lock-based shared drafts remain |
| 68 | PST export | Deferred | — | MBOX/JSON export remains |
| 69 | Native iOS/Android apps and native push | Deferred | — | PWA/MDM path remains |

> IDs 28 (CRM autopilot) and 49 (add-on marketplace) are intentionally absent: both were removed from this program by owner decision after this inventory was drafted.

## 4. Phase Plans

### Phase 1 — Enterprise Entry (Paid)

**Objective:** Remove the procurement blockers that prevent a signed contract, and create the licensing/entitlement spine used by every later paid feature.

**Window:** Months 0–3. **Critical path:** yes. **Assumption:** 2–3 senior engineers; SSO/identity and Control Plane work can run in parallel with UI.

**Workstreams**

1. **Entitlements and offline license**
   - Signed license file: tenant id, edition, seat limit, expiry, feature list, support tier.
   - Ed25519 signature verification with embedded public key; no mandatory network call.
   - Grace period, clock-skew handling, tamper/expiry tests, air-gapped activation flow.
   - Server-side entitlement middleware for HTTP and background workers.
   - Control Plane license upload/status UI; seat usage from active users; warning thresholds.
   - Migration for tenant entitlement/edition fields and license metadata.
   - Files: new `backend/internal/control/entitlements.go`, `backend/internal/httpapi/entitlements.go`, migration `0061_tenant_entitlements.sql`, Control Plane views, tests.

2. **OIDC and SAML SSO**
   - Tenant identity-provider configuration; discovery, redirect URIs, client secrets/sealed secrets.
   - OIDC first (Google, Microsoft Entra, Okta, Keycloak), SAML 2.0 second.
   - JIT user provisioning, domain discovery, group→role mapping, session policy.
   - Break-glass local administrator; explicit re-auth for sensitive operations.
   - Lockout, duplicate-email, disabled-user and deprovisioning tests.

3. **SCIM 2.0**
   - `/scim/v2/Users` and `/scim/v2/Groups` with bearer-token auth.
   - Create/update/deactivate/delete; group membership→role mapping; idempotent retries.
   - SCIM token rotation and audit.
   - Tests: duplicate create, patch semantics, group removal, deprovision revokes sessions.

4. **White-label**
   - Tenant webmail domain, login branding, support/legal links, removal of “via Alt8Mail” attribution under paid policy.
   - Certificate/domain validation and fallback to default domain.
   - Control Plane UI and docs; no hardcoded brand copy in new surfaces.

5. **Backup/restore and DR**
   - Per-mailbox and per-folder point-in-time restore from existing backups.
   - Restore preview, dry-run, audit, conflict handling, and progress UI.
   - Documented DR runbook; quarterly restore drill becomes an acceptance gate.

6. **Accessibility foundation**
   - Keyboard/focus audit for login, compose, settings, Control Plane.
   - Labels, contrast, reduced motion, screen-reader smoke paths.
   - RTL regression checklist for compose/calendar.

**Acceptance / release gate**

- External IdP matrix passes with at least Entra, Okta, Google and Keycloak.
- SCIM conformance tests pass; deprovisioning revokes active sessions.
- License tests cover valid, expired, grace, tampered, wrong-tenant, seat-exceeded and air-gapped cases.
- A mailbox can be restored to yesterday in staging with audit evidence and no cross-tenant access.
- Automated accessibility scan has no critical violations; manual keyboard-only path completes compose/send.
- Upgrade from 0.27.6 applies migrations and rolls back cleanly.

**Effort:** 10–14 engineer-weeks, parallelizable across two engineers. **Paid:** all of Phase 1 except accessibility improvements and DR documentation, which remain OSS quality work.

### Phase 2 — Team Operations and MCP Seed

**Objective:** Make Alt8Mail a team product, not a collection of individual mailboxes, and seed the developer/AI ecosystem with a safe OSS MCP server.

**Window:** Months 3–6. **Depends on:** Phase 1 identity, audit and entitlements.

**Workstreams**

1. **Shared inboxes**
   - Team mailbox model, assignment, round-robin, watchers, internal notes.
   - SLA policies and timers, breach warnings, escalation.
   - Collision detection (another agent is replying), ownership handoff.
   - Team-scoped permissions and audit; no cross-team leakage.
   - Tests: concurrent assignment, timer idempotency, permission denial, agent collision.

2. **Shared drafts v1 (non-CRDT)**
   - Optimistic concurrency/version tokens; section ownership or single-writer lock.
   - Comments and presence indicators; merge/rebase conflict UI.
   - Full version history and restore.
   - Tests: two editors, stale write rejection, conflict resolution, permission checks.

3. **Team templates and alias controls**
   - Shared canned responses/templates with variables, categories, permissions and versioning.
   - Alias-level signatures, rules and catch-all routing; precedence tests.
   - Team template picker in compose; audit.

4. **Visual automation builder v1**
   - Conditions: sender/domain/subject/attachment/labels/headers/recipient.
   - Actions: label, move, auto-reply, webhook, create task, notify.
   - Delivery-time rules compile to Sieve; asynchronous actions run through an idempotent worker.
   - Versioning, enable/disable, dry-run and execution log.
   - Tests: retry idempotency, loop prevention, permission checks, dry-run fidelity.

5. **Tasks, notes, files and sender controls**
   - Tasks: subtasks, dependencies, kanban, recurring, assignment, reminders, email→task button, task↔calendar.
   - Notes: backlinks, @-page mentions, shared wikis, mail→note.
   - Files: versioning UI, comments, approval requests, share audit, edit-attachment-in-place loop.
   - Large-file link conversion and expiring attachment links.
   - VIP senders and thread muting controls.
   - Jitsi/Meet link insertion in compose/calendar (no media server).

6. **MCP server seed (OSS)**
   - MCP JSON-RPC server with OAuth 2.1; scopes: mail.read, mail.search, mail.draft, calendar.read, tasks.read.
   - Per-tool consent, user-visible connection list, rate limits, audit, prompt-injection defenses.
   - Read/search/draft only; no send, delete or admin scopes in v1.
   - Tests: scope denial, consent revocation, malicious-content handling, audit completeness.

**Acceptance / release gate**

- Three end-to-end team workflows run in staging: shared inbox assignment/SLA, shared draft conflict, automation with webhook and task creation.
- No permission test can read another team’s mailbox or automation log.
- MCP cannot perform an action outside its granted scopes or without consent.
- Shared draft stale-write and conflict tests pass.

**Effort:** 12–16 engineer-weeks. **Paid:** shared inbox, shared drafts, team templates, automation builder, approvals. **OSS:** basic tasks/notes, MCP seed, Jitsi/Meet link.

### Phase 3 — Scheduling, Revenue and Deliverability

**Objective:** Convert scheduling and outbound workflows into paid, high-value features while protecting deliverability.

**Window:** Months 5–8. **Depends on:** Phase 2 automation and template foundations.

**Workstreams**

1. **Recipient-time scheduling and undo window**
   - Wire recipient-time suggestions into the schedule picker.
   - Timezone/DST tests, recipient working hours, “send now at their 9am” preview.
   - Fallback when timezone is unknown.
   - Tenant-configurable undo window (for example, 5–60 seconds), with policy defaults and a visible countdown.

2. **Follow-ups and obligations**
   - Boomerang: nudge me if no reply in N days; remind me to reply.
   - Follow-up automation: create task, draft reminder, or notify; never auto-send without confirmation.
   - Tests: reply detection across threads, cancellation, duplicate suppression.

3. **Native mail merge**
   - CSV/JSON import, template variables, preview, per-recipient rendering.
   - Throttling, suppression list, unsubscribe, bounce handling, send-window controls.
   - Campaign report: sent, delivered, bounced, replied, unsubscribed.
   - Tests: opt-out enforcement, rate limiting, malformed CSV, duplicate suppression.

4. **Booking and availability**
   - Public booking pages, buffers, working hours, round-robin for teams.
   - External free/busy via CalDAV; privacy-preserving “busy” output.
   - Resource booking approvals and utilization reports.
   - Tests: timezone/DST, double-booking race, cancellation, privacy scope.

5. **Templates gallery and payment links**
   - Packaged letters/invoices/contracts; team template installation.
   - Stripe payment links in compose/templates with audit and policy controls.

6. **Deliverability**
   - DMARC rua mailbox ingestion, XML parsing, aggregation, dashboard and alerts.
   - Campaign tracking (opt-in): transparent read receipts and link clicks, tenant-policy gated, no silent tracking.
   - Tests: report dedupe, privacy disclosure, tenant opt-out.

**Acceptance / release gate**

- A 100-recipient merge test preserves per-recipient variables, suppression and throttling without exceeding configured caps.
- Recipient-time scheduling and the tenant-configured undo window pass DST and policy tests.
- Booking tests pass across DST and concurrent booking attempts.
- Follow-up tests do not fire when a reply exists; no silent auto-send.
- DMARC reports aggregate correctly and link tracking honors tenant policy.

**Effort:** 10–14 engineer-weeks. **Paid:** all, except basic recipient-time scheduling helper may remain OSS.

### Phase 4 — AI/MCP Moat

**Objective:** Make Alt8Mail’s AI useful, auditable and hard to copy: a governed agent layer over mail, calendar, files and connected CRM records.

**Window:** Months 7–11. **Depends on:** Phase 1 audit/identity; Phase 2 MCP seed; Phase 3 obligations patterns. Some work can start in parallel with Phase 3.

**Workstreams**

1. **MCP governance and tools**
   - Expand scopes: calendar.write, tasks.write, labels.write, draft.send (explicit confirmation), CRM.read.
   - Organization consent policies, per-tool approval, connection inventory, revocation, audit.
   - Rate limits and AI budget controls; dry-run tool mode.
   - Tests: prompt injection, scope escalation, revocation, tenant isolation, cost caps.

2. **AI triage / priority inbox**
   - Classify Action / FYI / Newsletter / Spam-like / Needs reply with confidence and explanation.
   - Auto-label/route through Phase 2 rules; user feedback loop.
   - Tests: evaluation set, feedback correction, no destructive action without threshold and policy.

3. **Obligations ledger**
   - Extract promises, requests and deadlines from threads; link to source messages.
   - Auto-create tasks/nudges; “waiting on them” and “I promised” views.
   - Tests: false-positive review set, reply resolution, task dedupe.

4. **Natural-language search**
   - NL→structured query over FTS and metadata; citations to source messages/files.
   - Tests: ambiguous queries, attachment filters, no fabricated results.

5. **Meeting prep and sender intelligence**
   - Pre-meeting brief: related threads, files, CRM record, last interaction, open obligations.
   - Sender profile: company, role, last contact, CRM/deal link, first-contact badge.
   - Tests: stale CRM data, missing meeting, privacy scope.

6. **AI document assistant**
   - In OnlyOffice: summarize, rewrite, translate, Q&A over the document with citations to source mail thread.
   - “Extract action items” into tasks; “draft reply with this document.”
   - Tests: document permissions, citation accuracy, DLP classification.

7. **AI policy assist**
   - Suggest DLP classification before send; explain existing flags in plain English.
   - Tests: no sensitive content leakage to the model provider; policy overrides.

**Acceptance / release gate**

- AI evaluation suite meets agreed precision/recall thresholds for triage and obligations.
- No MCP tool can exceed its scope; prompt-injection corpus cannot trigger tool calls.
- AI cost per tenant is capped and visible; BYO model path works.
- Every AI write action is approval-gated and audited.

**Effort:** 14–18 engineer-weeks plus ongoing evaluation. **Paid:** AI triage, obligations, document assistant, advanced MCP governance and managed AI. **OSS:** basic user-triggered AI and MCP read/search/draft.

### Phase 5 — Compliance and Data Governance

**Objective:** Win regulated buyers with evidence, retention, eDiscovery and key control.

**Window:** Months 9–14. **Depends on:** Phase 1 audit/restore; Phase 2 files; Phase 4 AI policy hooks.

**Workstreams**

1. **Advanced DLP**
   - Detectors: credit cards, IBAN, national IDs, custom regex/dictionaries, OCR for attachments/images.
   - Watermarking, prevent-forward, policy simulation, false-positive review.
   - Tests: detector accuracy corpus, OCR fixtures, bypass attempts.

2. **eDiscovery and retention**
   - Cross-mail/file/document search, legal hold, reviewer roles, review queue, export.
   - MBOX/JSON export first; chain-of-custody manifest and audit.
   - Retention by classification/folder with legal-hold override; data-residency controls.
   - Tests: hold bypass denial, export reproducibility, retention boundary.

3. **BYOK and tenant encryption**
   - Envelope encryption per tenant; customer key upload/wrap; rotation and recovery.
   - Air-gapped key ceremony; lost-key runbook; no silent fallback to vendor key.
   - Client-side encryption option for sensitive Cabinet folders.
   - Tests: rotation, restore after key loss warning, tenant isolation.

4. **Files governance**
   - Version retention, comments/approvals, share approvals, DLP on share, watermarking, legal hold.
   - Tests: approval enforcement, retention deletion blocked by hold, share-link expiry.

5. **Audit and SIEM**
   - Prebuilt Splunk/Sentinel/Elastic dashboards; signed export bundles; admin action coverage.
   - Tests: event completeness, tamper evidence, time synchronization.

**Acceptance / release gate**

- DLP accuracy corpus meets agreed thresholds; OCR path handles scanned PDFs/images.
- eDiscovery export reproduces a case set with manifest and audit trail.
- BYOK rotation and recovery drills pass; no mailbox data is readable without the tenant key.
- Legal hold blocks deletion across mail, files and documents.

**Effort:** 14–18 engineer-weeks. **Paid:** all Phase 5 features.

### Phase 6 — Ecosystem and Scale

**Objective:** Turn Alt8Mail into a platform and a supportable product at scale.

**Window:** Months 12–18+. **Depends on:** Phases 1–5 interfaces.

**Workstreams**

1. **API and automation ecosystem**
   - Tenant-scoped public API v1, API keys, rate limits, OpenAPI, webhooks.
   - Zapier/n8n/Make connectors; generic triggers/actions.

2. **Integrations**
   - Deep CRM sync (HubSpot, Salesforce, Zoho, Pipedrive).
   - Slack/Teams interactive actions; Jira/Linear.
   - Google Workspace/M365 coexistence: dual delivery, calendar free/busy federation, directory sync.

3. **Migration and adoption**
   - Gmail API and Microsoft Graph importers (mail, calendar, contacts, drive).
   - Migration rehearsal, progress, rollback, fidelity reports.

4. **Scale and operations**
   - Tenant usage analytics (storage, AI tokens, send volume, feature adoption).
   - PWA offline read/queued actions/compose/send; MDM packaging; web push improvements.
   - Command palette, shortcut customization, theme/density, full accessibility audit and RTL polish.
   - Phishing simulation/security awareness add-on.
   - Performance budgets, support bundle, upgrade telemetry (opt-in).

**Acceptance / release gate**

- API versioning and webhook retries are idempotent and documented.
- Migration fidelity report proves flags, folders, attachments and calendar data.
- Offline actions reconcile without duplicates on reconnect.
- Accessibility audit has no critical findings; RTL flows pass.

**Effort:** 16+ engineer-weeks, ongoing. **Paid:** deep integrations, migration, analytics, security add-on. **OSS:** command palette, accessibility, PWA, basic API.

## 5. Cross-Cutting Architecture

### Identity and entitlements

- Add an identity service boundary around OIDC/SAML/SCIM; keep existing password/TOTP/passkey flows as fallback.
- Add a tenant entitlement context to every request and worker job.
- License verification belongs in one package used by HTTP, background workers and Control Plane.
- Never gate by frontend routes alone; tests must call the API directly.

### Data model

- Tenant/edition/license tables; identity-provider and SCIM token tables.
- Shared-inbox teams, assignments, SLA policies, events.
- Draft versions/locks/comments; template and automation tables.
- CRM connector cache for deep sync (contacts, companies, deals, activities).
- AI conversation, consent, tool-grant, usage and audit tables.
- Attachment extraction/OCR index tables; eDiscovery cases/holds/exports.
- All migrations include rollback and upgrade tests.

### AI and MCP

- Tenant AI policy: enabled providers, allowed data classes, retention, model routing, token quotas.
- BYO provider keys sealed with tenant key; no global provider key required.
- Prompt-injection defenses: content is data, never instructions; tool calls require explicit scope and approval.
- Evaluation harness for triage, obligations, search and document Q&A.
- Cost dashboard per tenant and per feature; hard caps and graceful degradation.

### Operations

- Offline license activation and air-gapped support.
- Per-folder restore and DR drills.
- Support bundle with redacted logs and dependency health.
- Upgrade/rollback validation for every release; no mandatory external services.
- Security package: threat model, SBOM, dependency policy, pen-test readiness.

## 6. Sequencing and Team Shape

- **Phase 1 is the critical path.** Do not start Phase 2 shared inbox work until identity/audit foundations are stable.
- **Parallel tracks once Phase 1 is underway:** identity/license (Track A), team workflows (Track B), MCP seed (Track C).
- **Recommended team:** 2–3 senior engineers, one product/design owner, one QA/security part-time, one support/ops owner. With a solo engineer, multiply timelines by roughly 1.7–2.5× and sequence strictly.
- **Release train:** monthly minor releases; paid beta after Phase 1; first paid cohort after Phase 2; AI/MCP beta during Phase 4; compliance packaging after Phase 5.
- **Do not** start native mobile, chat, native video, CRDT or PST before the first paid cohort.

## 7. Risks and Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| Scope sprawl | Nothing ships | Enforce phase gates; one paid theme per release |
| SSO/SCIM identity edge cases | Lockouts, security incidents | IdP matrix, break-glass admin, deprovision tests |
| Offline license friction | Lost sales/support load | Signed offline files, grace periods, clear recovery |
| AI cost and quality | Margin loss, trust loss | BYO keys, quotas, evaluation set, human approval |
| MCP data exfiltration | Catastrophic trust loss | Scoped OAuth, consent, audit, no raw credentials |
| Shared-inbox permission bug | Cross-team data leak | Team-scoped queries and adversarial tests |
| Upgrade/restore failure | Data loss | Rollback migrations, restore drills, upgrade tests |
| Deliverability damage from merge | Reputation harm | Throttle, suppression, bounce handling, send windows |
| Open-core trust erosion | Community backlash | Publish edition boundary; no core-mail hostage features |
| Support matrix explosion | Unprofitable support | Supported deployment matrix, health checks, support bundle |

## 8. Explicitly Deferred and Removed

- Native iOS/Android apps and native push.
- Internal chat/presence next to mail, and email→chat.
- Native video conferencing/SFU; Jitsi/Meet link insertion remains.
- Shared-draft CRDT co-editing; lock-based shared drafts remain.
- PST export; MBOX/JSON eDiscovery export remains.
- Add-on marketplace and CRM autopilot are removed from this program by owner decision.

## 9. First 90 Days

**Weeks 1–2 — Decisions and spikes**
- Confirm target cohort, supported IdPs, license format and AI provider policy.
- Threat-model SSO/SCIM/licence/MCP.
- Design entitlement schema and migration/rollback.
- Write Phase 1A implementation plan with exact files and tests.

**Weeks 3–6 — OIDC + license spine**
- OIDC login, JIT provisioning, tenant IdP config.
- Signed license verification, feature flags, seat usage, admin UI.
- Direct API entitlement tests.

**Weeks 7–9 — SCIM + SAML**
- SCIM users/groups and deprovisioning.
- SAML SP and IdP matrix.
- Break-glass admin and session policy.

**Weeks 10–12 — White-label, restore, accessibility gate**
- Custom domain/branding.
- Per-folder restore and DR drill.
- Accessibility baseline and security package.
- Paid beta release gate.

## 10. Decisions Needed at Review

1. Is the first paid cohort the regulated self-hoster/MSP segment above, or a different segment?
2. How many engineers and how much parallel capacity are available?
3. Which IdPs must be certified in the first release: Entra, Okta, Google, Keycloak, others?
4. Is the paid edition licensed per seat, per tenant, or both; what grace period is acceptable?
5. Is AI bring-your-own-key the default for OSS and managed AI the paid add-on?
6. Which Phase 1 paid features may remain OSS to protect community trust?
7. Is the first commercial milestone Phase 1 (signed contracts) or Phase 2 (team workflows)?
8. Are booking pages, mail merge and campaign tracking in the first paid release or the second?
9. What is the supported deployment matrix (Ubuntu/Debian versions, PostgreSQL, Dovecot, OnlyOffice)?
10. Which compliance claims may be marketed before an external audit: WCAG, SOC 2 readiness, pen test, DMARC?

## 11. Definition of Program Success

- A regulated buyer can complete SSO/SCIM, license activation and a restore drill without vendor assistance.
- A support team can run a shared inbox with SLA evidence and no cross-team leakage.
- A sales team can run a throttled, compliant mail merge with suppression and bounce handling.
- An AI agent can read/search/draft through MCP with scoped consent, complete audit and capped cost.
- A compliance officer can place a legal hold, export an eDiscovery case and verify chain of custody.
- The OSS core remains fully usable, exportable and deployable without a license.
- The paid edition has clear, defensible reasons to buy and measurable support boundaries.
