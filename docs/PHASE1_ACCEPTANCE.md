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
| 1E License seat/tenant enforcement | in progress | `docs/superpowers/plans/2026-11-10-phase-1e-license-control-plane.md` Task 2; Control status/upload/seat API landed in Task 1 |
| 1E Control Plane license UI and seat metering | in progress | same plan, Tasks 1/3; Control API landed, web view pending |
| 1F White-label and custom webmail domains | done | Paid white_label branding, login logo/links/attribution, custom hostname resolution against Web Net route/cert inventory, migration 0062, `docs/WHITE_LABEL.md`; unit + PostgreSQL integration tests |
| 1G Per-mailbox/per-folder restore and DR | planned | `docs/superpowers/plans/2026-11-10-phase-1g-restore-dr.md`, `docs/DR.md` once written |
| 1H Accessibility baseline and keyboard gate | planned | `docs/superpowers/plans/2026-11-10-phase-1h-accessibility.md`, `tools/a11y-audit.mjs` once written |
| Upgrade/rollback from v0.27.6 | done | `tools/migration-upgrade-smoke.sh` (apply tag migrations, upgrade, rollback new, reapply) |

## Release acceptance checklist

| Criterion | Status | Evidence / action |
| --- | --- | --- |
| External IdP matrix (Entra, Okta, Google, Keycloak) passes | operator | Protocol behavior covered by OIDC/SAML test suites; provider examples in `docs/SSO.md` and `docs/SAML.md`; run a live sign-in per provider before enablement |
| SCIM conformance tests pass | done | `A8M_SCIM_TEST_DSN` suites pass for tokens, Users and Groups; system-administrator and exact `/scim/v2` gate tests included |
| Deprovisioning revokes active sessions | done | SCIM deprovision deletes sessions/auth challenges/admin sessions/trusted devices; integration tests |
| License valid/expired/grace/tampered/wrong-tenant/seat-exceeded/air-gapped | in progress | 1A covers valid/grace/expired/tampered/wrong key/malformed and no network; seat/tenant enforcement in 1E Task 2 |
| Mailbox restore to yesterday with audit and no cross-tenant access | planned | 1G preview/apply + DR drill; disposable-PostgreSQL integration test |
| Automated accessibility scan has no critical violations | planned | 1H audit harness and fixes |
| Manual keyboard-only compose/send completes | planned | 1H keyboard script |
| Upgrade from 0.27.6 applies migrations and rolls back cleanly | done | `tools/migration-upgrade-smoke.sh` passed on disposable PostgreSQL |
| No release/deploy/push without separate authorization | done | All Phase 1 work remains local on `phase-1-enterprise` |

## Residual risks to revisit

- SAML replay cache is in-process (1024 entries) and tracking-cookie single-use
  depends on relay state; documented, shared-store work is out of scope.
- SAML per-IP limiter is not a global bound; start remains unthrottled.
- SCIM integration tests require a real PostgreSQL DSN; CI must run them or
  the gate is staging-only.
- Race detector is unavailable on the Windows toolchain (cgo disabled).
