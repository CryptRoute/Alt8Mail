# Phase 1 Acceptance Tracker

Living evidence map for the Phase 1 enterprise-entry release gate. Update each
row as its workstream lands; the release is blocked while any required row is
not green.

Status: `done`, `in progress`, `planned`, or `operator` (requires a live
external system/credential and is documented for the operator run).

## Workstreams

| Workstream | Status | Evidence |
| --- | --- | --- |
| 1A Offline license verification and entitlements | done | `backend/internal/licensing`, `backend/internal/httpapi/license.go`, `docs/LICENSING.md`; verifier/lifecycle tests |
| 1B OIDC SSO | done | `backend/internal/httpapi/oidc*.go`, `docs/SSO.md`; mock-IdP, negative-token, state/PKCE, domain/IP/MFA and license-gate tests |
| 1C SAML SSO | done | `backend/internal/httpapi/saml*.go`, `docs/SAML.md`; mock-IdP suite, security gate with artifact/body-bound/rogue-key tests |
| 1D SCIM 2.0 | done | `backend/internal/httpapi/scim*.go`, migrations `0061`, `docs/SCIM.md`; final gate adds system-administrator protection and the exact `/scim/v2` nginx route; DSN token/Users/Groups suites pass |
| 1E License seat/tenant enforcement | done | `backend/internal/control/license.go`, `backend/internal/httpapi/license.go`, `docs/LICENSING.md`; seat/tenant DSN tests 15/15 and race/lock coverage |
| 1E Control Plane license UI and seat metering | done | License status/upload/seat API and web view, group-readable atomic install, installer/updater provisioning, `docs/SYSTEM_CONTROL.md`; full suite green |
| 1F White-label and custom webmail domains | done | Paid white_label branding, login logo/links/attribution, custom hostname resolution against Web Net route/cert inventory, migration 0062, `docs/WHITE_LABEL.md`; unit + PostgreSQL integration tests |
| 1G Per-mailbox/per-folder restore and DR | done | Additive Maildir preview/apply, `control.restore_jobs` (migration 0063), Control UI/audit, `docs/DR.md`; unit path/extraction tests plus disposable-PostgreSQL preview/job/cross-tenant integration twice, including migration rollback/reapply |
| 1H Accessibility baseline and keyboard gate | done | `tools/a11y-audit.mjs`, `tests/a11y-baseline.mjs`, `tests/a11y-keyboard.mjs`, `docs/ACCESSIBILITY.md`; 9 surfaces audited at 0 Critical/High, keyboard compose/send and Arabic RTL 360px pass |
| Upgrade/rollback from v0.27.6 | done | `tools/migration-upgrade-smoke.sh` (apply tag migrations, upgrade, rollback new, reapply) |

## Release acceptance checklist

| Criterion | Status | Evidence / action |
| --- | --- | --- |
| External IdP matrix (Entra, Okta, Google, Keycloak) passes | operator | Protocol behavior covered by OIDC/SAML test suites; provider examples in `docs/SSO.md` and `docs/SAML.md`; run a live sign-in per provider before enablement |
| SCIM conformance tests pass | done | `A8M_SCIM_TEST_DSN` suites pass for tokens, Users and Groups; system-administrator and exact `/scim/v2` gate tests included |
| Deprovisioning revokes active sessions | done | SCIM deprovision deletes sessions/auth challenges/admin sessions/trusted devices; integration tests |
| License valid/expired/grace/tampered/wrong-tenant/seat-exceeded/air-gapped | done | 1A covers valid/grace/expired/tampered/wrong key/malformed and no network; 1E adds tenant and seat enforcement tests |
| Mailbox restore to yesterday with audit and no cross-tenant access | operator | Preview/apply code, audit and cross-tenant PostgreSQL integration are green; execute the live `doveadm` drill on staging per `docs/DR.md` before release |
| Automated accessibility scan has no critical violations | done | `npm run test:a11y` audits login, 2FA, inbox, reader, compose, settings, RTL 360px, Control login and Control shell: 0 Critical, 0 High |
| Manual keyboard-only compose/send completes | done | `tests/a11y-keyboard.mjs`: Tab/Enter sign-in, C compose, Ctrl/Cmd+Enter send, spell-check via keyboard, settings return, Arabic RTL send |
| Upgrade from 0.27.6 applies migrations and rolls back cleanly | done | `tools/migration-upgrade-smoke.sh` passed on disposable PostgreSQL |
| No release/deploy/push without separate authorization | done | All Phase 1 work remains local; no release, deploy or push performed |

## Residual risks to revisit

- The live `doveadm` mailbox-restore drill has not run in this Windows
  environment; execute it on staging per `docs/DR.md` before release.
- The Control Plane accessibility shell is audited through a static harness.
  Run `npm run test:a11y` against staging with a real admin session before
  release; third-party WCAG certification remains out of scope.
- SAML replay cache is in-process (1024 entries) and tracking-cookie single-use
  depends on relay state; documented, shared-store work is out of scope.
- SAML per-IP limiter is not a global bound; start remains unthrottled.
- SCIM integration tests require a real PostgreSQL DSN; CI must run them or
  the gate is staging-only.
- Race detector is unavailable on the Windows toolchain (cgo disabled).
