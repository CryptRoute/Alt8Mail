# Phase 1E — Control Plane License UI and Seat Metering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development`. Steps use checkbox syntax.

**Goal:** Let a System Control operator upload, inspect and monitor the offline Alt8Mail license, see live seat usage against the licensed limit, and understand grace/expiry/warning states without exposing the raw token.

**Architecture:** Add license file/public-key/warning settings to the Control Plane config and a small `control/license.go` service that reads and verifies the same signed token file the webmail uses (via `internal/licensing`). Expose read/upload under `settings.write`, write the token atomically with `0640`/group `a8m` permissions, and audit uploads. Seat usage is counted live from `public.users` (`status='active' AND deleted_at IS NULL`), globally or scoped to the license's optional tenant claim. A new Control web view renders status, feature list, a seat meter and an upload form; the UI states that the webmail must be restarted to load a newly uploaded token.

**Tech Stack:** Go, `internal/licensing`, pgx, Control Plane ES-module web UI, existing `require`/`actorAudit` helpers.

**Spec:** `docs/superpowers/plans/2026-11-09-alt8mail-paid-platform-roadmap.md` §4 Phase 1 (entitlements/license), plus `docs/LICENSING.md`.

## Global Constraints

- No network call and no private key in the repository; verification uses `CONTROL_LICENSE_PUBLIC_KEY` and the local file only.
- OSS mode is the default: a missing/unconfigured license renders an OSS empty state; it must never break Control startup or any core screen.
- The raw token and Ed25519 signature are never returned by any GET, never logged, never put in audit metadata, and never rendered after upload. Upload accepts a token body capped at 64 KiB.
- Only `settings.write` may read status or upload; unauthenticated/insufficient requests follow the existing Control error/route behavior.
- The Control file path and the webmail `ALT8MAIL_LICENSE_FILE` must point at the same file; the UI displays the configured path and docs state the requirement.
- A failed upload (read error, bad key, invalid/tampered/expired token) leaves the existing file byte-for-byte unchanged.
- Atomic write: temp file in the destination directory, mode `0640` with group `a8m` (or `CONTROL_LICENSE_GROUP`), preserve an existing target's uid/gid, fsync file and directory, rename over the target. The file must stay readable by the unprivileged webmail user (`a8m`); a root deployment fails the upload with `LICENSE_WRITE` if ownership cannot be set.
- A newly uploaded token requires a webmail restart; the API reports `restartRequired: true`, the UI shows it, and docs repeat it. Seat usage is live and does not require a restart.
- No database migration, release, deploy or push.

## Review Focus

- Seat counts are accurate and tenant-safe: active, non-deleted users only; the license tenant claim scopes the count; no cross-tenant leakage in the per-tenant list.
- Upload cannot corrupt or replace a valid file: every failure path preserves the old bytes and modes; a successful write is atomic and `0640` with group `a8m`, readable by the webmail service.
- No secret leakage: raw token/signature absent from responses, logs, audit metadata and UI state; errors are fixed categories, not parser internals.
- Lifecycle correctness: `valid`/`grace` remain usable, `expired` disables paid features and is visibly distinct, warning threshold triggers at the configured percentage, unlicensed/invalid are OSS states.
- The webmail/control file-path coupling is visible and documented; an operator cannot mistake a successful upload for a live entitlement change.

Each focus line has tests in the task that owns the code.

---

### Task 1: Control config, license service and API

**Files:**
- Modify: `backend/internal/control/config.go`
- Create: `backend/internal/control/license.go`
- Create: `backend/internal/control/license_test.go`
- Create: `backend/internal/control/license_api_test.go`
- Modify: `backend/internal/control/server.go` (route registration)

**Interfaces:**
- Produces config: `LicenseFile string` (`CONTROL_LICENSE_FILE`, default `/etc/a8m/license.token`), `LicensePublicKey string` (`CONTROL_LICENSE_PUBLIC_KEY`), `LicenseGroup string` (`CONTROL_LICENSE_GROUP`, default `a8m`), `LicenseWarningPercent int` (`CONTROL_LICENSE_WARNING_PERCENT`, default 80, valid 1–100).
- Produces `type licenseStatus struct` with JSON tags: `configured`, `filePath`, `state`, `edition`, `inGrace`, `expired`, `seats`, `usedSeats`, `remainingSeats`, `warning`, `features []string`, `expiresAt *time.Time`, `tenant`, `licenseId`, `lastModified *time.Time`, `error` (fixed category only).
- Produces `(s *Server) licenseStatus(ctx context.Context) licenseStatus`, `(s *Server) licenseSeatUsage(ctx context.Context, tenant string) (used int, byTenant []map[string]any, err error)`, `(s *Server) writeLicenseToken(path, publicKey, token string) error`.
- Produces routes in `server.go`: `GET /control/api/license` and `POST /control/api/license`, both `s.require("settings.write", …)`.

Behavior:
- Load config; empty public key or file path does not fail startup. Status `state` is `unlicensed` when no file is configured, `invalid` when the file/key/verify fails, otherwise the licensing lifecycle (`valid`, `grace`, `expired`); `edition` is `oss` when unlicensed/invalid, otherwise the payload edition. `features` is empty when expired/invalid/unlicensed.
- Read the file on each GET (cheap; no caching complexity); cap reads at 64 KiB with `io.LimitReader`. Map errors to fixed categories (`unreadable`, `malformed`, `signature`, `not_configured`) — never include the parser or path contents beyond the configured path.
- Seat count: if `license.tenant` is non-empty, count only that tenant; otherwise count all active non-deleted users. `byTenant` returns `[{tenantId,name,activeSeats}]` ordered by name, and is empty for a tenant-scoped license. `warning` is true when `seats > 0` and `usedSeats*100 >= seats*LicenseWarningPercent`.
- `POST` body `{"token":"A8M1...."}`; 64 KiB cap; require `Content-Type: application/json`; parse and verify before writing. Accept `valid` and `grace`; reject `expired`, invalid, wrong-key, malformed and a missing configured public key/path with 400/503 and a fixed error code. On success write atomically (temp file in `filepath.Dir(path)`, mode `0640` with group `a8m`/`CONTROL_LICENSE_GROUP`, preserved existing uid/gid, `Sync`, `Rename`, directory sync), re-read status, audit `license.uploaded` with `licenseId`, `edition`, `seats` and `expiresAt` only, and return `{"ok":true,"restartRequired":true,"license":<status>}`.
- Never log the token or verifier error; log fixed messages with the configured path only.

- [ ] Write failing unit tests: config defaults/validation; unlicensed/invalid/missing-file states; valid/grace/expired snapshot; tampered/wrong-key/malformed token; 64 KiB cap; seat counting with a license tenant claim and global; warning threshold boundaries (79/80/100%); fixed error categories.
- [ ] Write failing handler tests with a temp dir and generated Ed25519 keys: `GET` status shape and no token/signature; `POST` valid token writes `0640` (group `a8m`, preserving existing ownership) and reports restartRequired; invalid token preserves the old bytes/mode; expired token rejected; missing permission 403; audit metadata has no raw token.
- [ ] Implement config, `license.go`, routes. Use `licensing.ParsePublicKey`/`Verifier.Verify`/`Status(time.Now())`; do not duplicate the envelope logic.
- [ ] Run `go test ./internal/control -run License -count=1`, `go test ./... -count=1`, `go vet ./...`, `go build ./...`.

### Task 2: Webmail seat and tenant enforcement

**Files:**
- Modify: `backend/internal/httpapi/license.go`
- Modify: `backend/internal/httpapi/config.go` (expected license tenant)
- Modify: `backend/internal/httpapi/admin_users.go`
- Modify: `backend/internal/httpapi/admin_users_import.go`
- Modify: `backend/internal/httpapi/scim_users.go`
- Modify: `backend/internal/httpapi/license_test.go`
- Modify: `backend/internal/httpapi/scim_users_integration_test.go`

**Interfaces:**
- Produces config `LicenseTenant string` (`ALT8MAIL_LICENSE_TENANT`, optional). When set, a license whose payload `tenant` differs is `invalid` (wrong-tenant protection).
- Produces `(s *Server) activeSeatCount(ctx context.Context, tenantID string) (int, error)`, `(s *Server) seatEnforced() bool` (true only for a valid/grace license with seats > 0), `(s *Server) seatAvailable(ctx context.Context, tenantID string) (bool, error)`, and `errSeatLimit`.
- Produces `licenseSnapshot()` fields `usedSeats` and `seatLimit` (existing `seats` retained) for the webmail UI/tests.

Behavior:
- Enforcement applies only to a loaded license in `valid` or `grace` state. OSS, unlicensed, invalid and expired deployments keep core user creation available; expired paid features are already gated separately.
- A license with an empty `tenant` claim counts active users across all tenants; a license with a tenant claim enforces seats only for that tenant and ignores other tenants for this check. `LicenseTenant`, when configured, must equal the payload claim or the license is invalid.
- Count `users WHERE status='active' AND deleted_at IS NULL AND is_system_admin = false` (platform administrators do not consume seats). Serialize seat-consuming changes per tenant by locking the tenant row (`SELECT id FROM tenants WHERE id=$1 FOR UPDATE`) inside the provisioning transaction, then re-count before insert/activation so concurrent creates cannot exceed the limit.
- Enforce on: admin create (active status), admin restore (deleted → active), admin bulk import, admin status changes that activate an invited/suspended user, admin bulk activation, SCIM create with `active=true`, and SCIM reactivation (`active:false → true`). Invited/suspended users do not consume a seat until active.
- On rejection return a fixed `SEAT_LIMIT` error (HTTP 409 for admin; SCIM 409 `invalidValue`), audit `license.seat_limit_blocked` with the tenant and limit, and create no user/mailbox/session.
- Do not count system administrators or deleted users; do not silently deactivate anyone when a license shrinks.
- Update `docs/LICENSING.md` with the enforcement rules and OSS/expired behavior.

- [ ] Write failing unit tests for `seatEnforced` state matrix (OSS/unlicensed/invalid/expired/valid/grace), tenant-scoped vs global counting, `LicenseTenant` mismatch → invalid, and snapshot fields.
- [ ] Write failing integration tests with a real PostgreSQL: seats=2 blocks the third active create; invited does not count; delete frees a seat; grace still enforces; expired/unlicensed allows core creation; SCIM create/reactivate blocked at limit; concurrent creates never exceed seats; audit action present with no secret.
- [ ] Implement the helpers and wire the five mutation paths; keep all checks inside the existing provisioning transactions.
- [ ] Run `go test ./internal/httpapi -run 'SCIM|License|Seat' -count=1`, the DSN integration suite, `go test ./... -count=1`, `go vet ./...`, `go build ./...`.

### Task 3: Control web view and docs

**Files:**
- Create: `backend/internal/control/web/views-license.js`
- Modify: `backend/internal/control/web/app.js` (import, nav item, route map)
- Modify: `backend/internal/control/web/styles.css` only if the meter needs it
- Modify: `install.sh`, `scripts/build-release.sh` (license env provisioning/preservation)
- Modify: `docs/SYSTEM_CONTROL.md`
- Modify: `docs/LICENSING.md`

**Interfaces:**
- Consumes: `GET/POST /control/api/license` from Task 1.
- Produces: `renderLicense(ctx)` exported ES module, route `#/license`, nav item under System/Infrastructure with `settings.write`.

UI:
- Status card: edition, state with distinct OSS/valid/grace/expired styling and text (not color alone), expiry date, days remaining, feature chips, tenant scope when present.
- Seat meter: used / licensed, remaining, warning threshold; accessible `role="meter"`/`aria-valuenow`/`aria-valuemin`/`aria-valuemax` and a text fallback; warning banner at the configured threshold. No chart library.
- Per-tenant seat table when the license is global (tenant, active seats), with a total row; empty state for tenant-scoped licenses.
- Upload form: file or paste-token input, 64 KiB client-side guard, submit, in-flight state, success panel with “Restart the Alt8Mail webmail service to activate this license”, and error messages for fixed codes. Clear the token input after any outcome; never render the token back.
- Empty/unconfigured/invalid states link to the configuration docs and show the configured path.
- Keep layout responsive at phone width, keyboard reachable, no raw HTML injection from token/name fields (`textContent`/escaped helpers).

- [ ] Add the view, nav and route; verify the permission gate hides it for read-only roles.
- [ ] Write a handler-level UI/API test if the control test harness supports it, and extend `tools/control-api-smoke.mjs` to cover GET status and a rejected invalid upload (no real license required).
- [ ] Update docs: config variables, shared file path with webmail, upload flow, restart requirement, seat counting rules, warning threshold, failure preservation, no network call, and the `0640`/`a8m` readability requirement.
- [ ] Provision and preserve license settings in `install.sh` and the release template: `CONTROL_LICENSE_FILE`, `CONTROL_LICENSE_PUBLIC_KEY` and (when a license file exists) `ALT8MAIL_LICENSE_FILE`; do not drop manually added values on reinstall. Ensure the license file is root-owned with group `a8m` and mode `0640` after install/upload.
- [ ] Run `node --check backend/internal/control/web/app.js` and the view if it is a separate module, then `npm run test:all`; run a manual or scripted Control UI smoke if the environment exposes 8808.

### Task 4: Security and UX gate

- [ ] Adversarial review: permission bypass, CSRF on upload, path handling, atomicity/mode, token leakage in logs/audit/UI, seat count cross-tenant leakage, expired/grace gating, oversized body, error enumeration.
- [ ] Verify no network calls and no private key material; verify OSS mode leaves core Control screens working.
- [ ] Run `go test ./... -count=1`, `go vet ./...`, `go build ./...`, `npm run test:all`; run the Control API smoke against a disposable instance when available.
- [ ] Final whole-branch review; one fix round and scoped re-review if findings.

## Out of scope

- Per-tenant license records or multiple license files, online activation, hot-reloading the webmail process, license deletion/downgrade, payment/billing integration.
