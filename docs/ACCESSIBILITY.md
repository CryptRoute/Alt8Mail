# Accessibility Baseline and Keyboard Gate

Phase 1H establishes a repeatable WCAG 2.1 AA-oriented baseline for the webmail
and the Control Plane shell. It is a regression gate, not a certification.

## Scope

Automated DOM checks run against:

- webmail login and two-factor challenge
- inbox, reader, compose and settings
- Arabic RTL inbox at 360px
- Control Plane login and shell (static harness)
- reduced-motion emulation and 360px horizontal-overflow checks

Not covered here: screen-reader testing on every platform, native apps, video
captions, a full third-party WCAG audit, and an authenticated live Control Plane
run. The static Control harness loads the real markup and scripts but stubs the
API; run the same checks against a staging Control Plane before release.

## Commands

```bash
npm run test:a11y      # DOM audit plus keyboard/RTL gate
npm run test:a11y:audit
npm run test:a11y:keyboard
```

Reports are written to `artifacts/a11y-<surface>.json`; the index is
`artifacts/a11y-report.json`. `artifacts/a11y-rtl-compose.png` and
`artifacts/a11y-keyboard-complete.png` capture the RTL and completed keyboard
paths. The audit command exits non-zero on any Critical finding.

## Severity

- **Critical** — blocks the command: missing accessible name on an interactive
  control, unlabeled form field, duplicate ID referenced by a label or ARIA
  relationship, missing `lang` or `<title>`, focusable content inside an
  `aria-hidden` subtree, unnamed visible dialog, horizontal overflow at 360px.
- **High** — report and fix before release: missing main landmark, no visible
  `h1`, heading-level jumps, duplicate unreferenced IDs, images without `alt`,
  no `:focus-visible` rule, no `prefers-reduced-motion` rule.
- **Medium / Low** — informational; triage with the next design pass.

## Automated rules

The harness in `tools/a11y-audit.mjs` checks document language and title, one
visible h1 and heading order, main landmark, form labels, accessible names for
links/buttons/role controls, image alt text, duplicate IDs, focusable content
inside `aria-hidden`, visible dialog names, `:focus-visible` and
`prefers-reduced-motion` stylesheet rules, and horizontal overflow. The browser
emulates `prefers-reduced-motion: reduce` and light mode for every surface.

## Manual keyboard procedure

1. Load the webmail login. Press Tab until the email field has a visible focus
   ring; type the email.
2. Tab to the password, type it, Tab to **Sign in**, press Enter.
3. At the two-factor challenge the first code box receives focus. Type six
   digits, Tab to **Verify and sign in**, press Enter.
4. In the mailbox press `C` to compose. Type the recipient, Enter to commit,
   Tab to the subject and body. Confirm both show a visible focus ring.
5. Press `Ctrl+Enter` (`Cmd+Enter` on macOS) to send. If spell check opens,
   Tab to **Send anyway** and press Enter. Verify the subject appears in the
   outbox or Sent.
6. Tab to **Settings**, press Enter. Focus moves to the active settings section;
   Tab to **Done** and press Enter. Focus returns to the Settings button.
7. Confirm no step required a pointer, and no focus gets trapped in a hidden
   panel or dialog.

The automated equivalent is `tests/a11y-keyboard.mjs`.

## RTL checklist

- Switch the locale to Arabic (`ar`); `<html>` must have `dir="rtl"` and
  `lang="ar"`.
- At 360px the page must not scroll horizontally.
- Compose must remain usable by keyboard: To, subject and body are reachable,
  visible inside the viewport, and `Ctrl+Enter` sends.
- Check directional icons/labels in a real browser pass: back/forward arrows,
  quote markers, and date/time alignment should mirror where meaning requires
  it, not merely where layout flips.
- Re-check the login, inbox, reader and settings at 360px after any layout
  change.

## Reduced motion

`prefers-reduced-motion: reduce` is emulated in the audit. Webmail and Control
styles reduce animations and transitions to near-zero. Motion must never be the
only signal for state; the reduced-motion path is part of the keyboard gate.

## Known exceptions

- The authenticated Control Plane is audited through a static login/shell
  harness because the Node reference server does not expose the Go Control API.
  Run `npm run test:a11y` against staging with a real session for release.
- Third-party WCAG certification, screen-reader matrix testing and native app
  accessibility remain out of scope for Phase 1.
- Dense Control Plane typography below 12px is an existing product-wide design
  choice; changing it is a visual redesign, not a Phase 1H fix.

## Remediation notes

- Webmail and Control login screens now expose a `main` landmark.
- The Control account chip no longer duplicates the login email ID.
- Compose supports `Ctrl/Cmd+Enter` and documents it in the shortcuts dialog.
- The rich/plain compose editor shows a visible focus ring.
- Opening Settings moves focus to the active section; Done returns focus to the
  Settings button.
- Control Plane CSS honours reduced motion.
- Regression checks: `tests/a11y-baseline.mjs`, `tests/a11y-keyboard.mjs`,
  `tools/a11y-audit.mjs`.
