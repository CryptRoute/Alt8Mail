# Current Project State

## Active Phase: Phase 6 (Ecosystem and Scale)

### Completed
- Phase 1 (1A–1H) ✅ See `docs/PHASE1_ACCEPTANCE.md`.
- Phase 2 (2A–2G) ✅ See `docs/PHASE2_ACCEPTANCE.md`.
- Phase 3 (3A–3F) ✅ See `docs/PHASE3_ACCEPTANCE.md`.
- Phase 4 (4A–4G) ✅ See `docs/PHASE4_ACCEPTANCE.md`.
- Phase 5 (5A–5E) ✅ See `docs/PHASE5_ACCEPTANCE.md`.
- 6A API and automation ecosystem ✅ Versioned `/api/v1`, scoped hashed keys, rate limits, idempotent POSTs, OpenAPI, signed retrying webhooks, developer UI and tests.
- 6B Integrations ✅ HubSpot contact/deal cache and CRM-aware sender intelligence; existing add-ons cover Slack/Jira/Linear/CRM actions.
- 6C Migration and adoption ✅ IMAP/POP migration rehearsal validation, per-job fidelity reports with attachments/flags/calendar evidence and persisted reports.
- 6D Scale and operations ✅ Tenant analytics, redacted support bundle, opt-in telemetry, authorized phishing simulation, command palette and offline compose/send queue.
- 6E Accessibility/productivity ✅ Command palette keyboard flow; existing a11y/RTL gates pass (see `docs/PHASE6_ACCEPTANCE.md`).
- Verification: Phase 6 PostgreSQL integration tests pass; see `docs/PHASE6_ACCEPTANCE.md` and `docs/API_V1.md`.

### In Progress
- None in code. Operator/follow-up gaps remain documented: native Gmail/Graph importers, non-HubSpot deep CRM adapters, Slack/Teams interactive callbacks, dual delivery/free-busy federation, offline non-send actions, scheduled telemetry, and CI-enforced performance budgets.

### Not Started
- Post-roadmap work only.

Phase 6 implementation is complete for the shipped v1 slices. 3.12.2 hardens the installer for minimal Debian and Ubuntu images: it installs `cron` and the other tools later phases assume, audits missing commands and systemd units after package installation, enables and starts services individually, defers Fail2Ban until its systemd-backend jail is installed, reloads generated secrets on resumed installs, and reports the failing command in the installer dialog. 3.12.1 restores the AI assistant to the community edition: provider configuration and the webmail compose, reading-pane, commitment and reply actions are community surfaces, while AI triage, obligations automation, search, meeting prep, policy assist, budget controls, BYOK and Office AI remain Enterprise entitlements. 3.12.0 replaces the oversized Collabora notebookbar with a compact desktop-suite skin: 12px tabs, icon-first controls without group captions, a horizontal text style strip that applies the real native paragraph styles, narrow left and right icon rails, and side decks that open between the canvas and the right rail. The editor waits for the document type before enabling the ruler, applies 100% zoom on load, follows the webmail light/dark theme, and hides unnecessary style tiles progressively as the editor narrows. The webmail document window chrome is reduced to a compact title bar, and the duplicate title inside the embedded editor is suppressed. The skin ships through the agent-mounted `branding.css`/`branding.js` and the webmail assets without rebuilding the Collabora image. The 3.11.10 AI work remains: the floating assistant uses the same tenant AI settings, sealed provider key, DLP redaction and budget hooks as the webmail `/api/ai/*` routes, with selected-text and document-level actions through `/api/office/ai`. The earlier hardening remains: engine/image validation, failure-safe settings, honest health reporting, engine switching that replaces the previous engine's Compose container, webmail-origin framing and postMessage origin, the Alt8Mail 8 icon, branding cache-busting, storage-move verification when a target-only delivery changes the Maildir directory mtime, and the Debian/Ubuntu Docker Compose v2 remote prerequisite fix. See `RELEASE_NOTES.md`. The Phase 5 encryption operator gate in `docs/RELEASE_GAP_AUDIT.md` remains.
