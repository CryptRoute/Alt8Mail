# Phase 1H — Accessibility Baseline and Keyboard Gate

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development`. Steps use checkbox syntax.

**Goal:** Establish a repeatable WCAG 2.1 AA-oriented accessibility baseline for the webmail and Control Plane: automated checks for critical violations, a keyboard-only compose/send path, labels/focus/contrast fixes, and an RTL regression checklist.

**Architecture:** Add a dependency-free CDP-based audit tool that runs against the existing Node reference server and the Go-backed screens where available, injecting in-page DOM checks (accessible names, labels, landmarks, heading order, duplicate IDs, image alt, focus indicators, reduced-motion). A separate keyboard-driven script exercises login → compose → send and Control login/route navigation. Findings drive targeted frontend fixes; `docs/ACCESSIBILITY.md` records the checks, manual procedure, RTL checklist and known exceptions.

**Tech Stack:** Node 22, existing `tools/cdp.mjs` browser harness, vanilla JS/CSS webmail and Control UI, Go tests where semantics are server-side.

**Spec:** `docs/superpowers/plans/2026-11-09-alt8mail-paid-platform-roadmap.md` §4 Phase 1 (Accessibility foundation).

## Global Constraints

- No new npm runtime dependencies; reuse `tools/cdp.mjs` and the E2E server harness.
- Automated checks must fail the command on Critical findings (missing accessible name on an interactive control, unlabeled form field, duplicate ID referenced by a label, missing `lang`, missing `<title>`, focusable content inside `aria-hidden`, keyboard trap, horizontal overflow at 360px).
- Manual keyboard path must complete compose/send without a mouse: login, open compose, fill To/Subject/Body, send with the documented shortcut, verify the message is in Sent/outbox.
- Preserve existing behavior and visual identity; fixes are semantic/label/focus/contrast improvements, not redesigns.
- RTL (`ar`) must render `dir="rtl"`, have no horizontal page scroll at 360px, and keep compose fields usable by keyboard.
- Respect `prefers-reduced-motion`: no essential animation, and the reduced-motion path is testable.
- No release, deploy or push.

## Review Focus

- Keyboard reachability: every interactive control on login, compose and the main settings has a visible focus indicator and a logical tab order; no traps.
- Names/labels: inputs, icon-only buttons, menus, dialogs and the new SAML/SCIM-related controls expose accessible names; errors are announced (`aria-live`/role=alert).
- Structure: landmarks, one `<h1>`, heading order, `lang`, page title, form grouping.
- Contrast/zoom: text and focus indicators readable in light/dark; 200% zoom and 360px width do not clip essential controls.
- RTL/regression: Arabic layout and compose remain usable; no directional icon/label regressions.

Each focus line has tests in the task that owns the code.

---

### Task 1: Audit harness and baseline report

**Files:**
- Create: `tools/a11y-audit.mjs`
- Create: `tests/a11y-baseline.mjs` (entry that starts the reference server/browser)
- Modify: `package.json` (`test:a11y` script)
- Create: `docs/ACCESSIBILITY.md`

- [ ] Use `tools/cdp.mjs` to open the login screen and the authenticated app (demo login), then run an in-page audit function returning `{rule, severity, selector, message}` findings for: `lang`/`title`, one h1, heading order, landmark presence, form labels, accessible names of buttons/links (text/aria-label/title), image alt, duplicate IDs, `aria-hidden` focusables, focus-visible style on tab, horizontal overflow at 360px, reduced-motion media query presence, and dialog names/roles.
- [ ] Run the audit on login, mail list, reader, compose, settings, and the Control Plane login/shell if the reference server exposes it; write a JSON report to `artifacts/a11y-<surface>.json` and fail on Critical findings.
- [ ] `docs/ACCESSIBILITY.md`: scope, rules, severity definitions, commands, manual keyboard script, RTL checklist, known exceptions and remediation notes.
- [ ] Run `node tools/a11y-audit.mjs` (or the test entry) and record the baseline findings; commit the harness and doc before fixes.

### Task 2: Critical fixes and keyboard/RTL paths

**Files:**
- Modify: `public/index.html`, `public/app.js`, `public/styles.css` (semantic/label/focus fixes only)
- Modify: `admin/index.html`, `admin/admin.js`, `admin/admin.css` (Control Plane equivalents)
- Create: `tests/a11y-keyboard.mjs`
- Modify: `package.json` (`test:a11y` includes audit + keyboard)

- [ ] Fix every Critical/High finding from the baseline: missing names/labels, duplicate IDs, focus traps, unreachable dialogs, contrast failures on primary text/focus, missing `aria-live` for login/compose errors, reduced-motion handling.
- [ ] Keyboard script: Tab/Enter through demo login; open compose; fill To, Subject, Body; send via the app shortcut; verify Sent/outbox; open settings and navigate back; assert focus returns to a sensible element after each dialog.
- [ ] RTL script: switch to Arabic (or set `dir=rtl`/locale), assert no horizontal overflow at 360px, compose fields reachable, send works; capture screenshots to `artifacts/`.
- [ ] Add `npm run test:a11y` wiring (audit + keyboard) and keep `test:all` unchanged unless the team wants it in the release gate.
- [ ] Run the audit/keyboard/RTL scripts and attach the report paths to the task report.

### Task 3: Accessibility gate and final review

- [ ] Re-run the full audit; no Critical findings and no unexplained High findings on login, compose, settings.
- [ ] Keyboard-only compose/send passes; RTL 360px passes; reduced-motion path passes.
- [ ] Run `go test ./...`, `npm run test:all`, `npm run test:a11y`; fix regressions.
- [ ] Final review of the accessibility diff; one fix round and scoped re-review if findings.

## Out of scope

- A full third-party WCAG audit/certification (Phase 6), screen-reader testing on every platform, native app accessibility, video captions, and a visual redesign.
