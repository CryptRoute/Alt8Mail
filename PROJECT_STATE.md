# Current Project State

## Active Phase: Phase 1 (Enterprise Entry)

### Completed
- 1A Licensing ✅
- 1B OIDC ✅
- 1C SAML ✅
- 1D SCIM ✅ Verified 2026-11-10: DSN token 14/14, Users 16/16, Groups 21/21 before and after migration 0061 rollback/reapply; full Go suite, npm test:all (E2E 0 failures), node and nginx static checks pass.
- 1E License UI, seat metering and enforcement ✅ Verified 2026-11-10: Control license view/API, atomic group-readable upload, installer/updater provisioning; seat enforcement DSN 15/15 plus Control seat SQL; full Go suite, npm test:all (E2E 0 failures), node/bash checks pass.
- 1F White-label ✅ Verified 2026-11-10: paid white_label branding (logo/support/legal/attribution), custom webmail host validation against Web Net routes/certs, migration 0062 apply/rollback/reapply, cross-tenant host integration, full Go suite and npm test:all (E2E 0 failures).
- 1G Restore/DR ✅ Verified 2026-11-10: additive per-mailbox/folder preview and apply from completed backups, tenant/path guards, `control.restore_jobs`, Control UI and audit; migration 0063 apply/rollback/reapply, disposable-PostgreSQL preview/job/cross-tenant integration twice, full Go suite and npm test:all (E2E 0 failures). Live `doveadm` restore drill remains an operator staging step in `docs/DR.md`.
- 1H Accessibility ✅ Verified 2026-11-10: CDP audit across webmail login/2FA/inbox/reader/compose/settings, RTL 360px and Control login/shell — 0 Critical/High; keyboard-only login, compose, Ctrl/Cmd+Enter send, settings return and Arabic RTL send pass; Go suite and npm test:all (E2E 0 failures) green.

### In Progress
- None.

### Not Started
- None.

Phase 1 implementation is complete. No release, deploy or push has been performed.
