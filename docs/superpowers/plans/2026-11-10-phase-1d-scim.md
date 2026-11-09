# Phase 1D — SCIM 2.0 Provisioning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development`. Steps use checkbox syntax.

**Goal:** Add license-gated SCIM 2.0 Users and Groups provisioning for existing tenants, with bearer-token authentication, idempotent externalId handling, deprovisioning that revokes sessions without deleting mail, and local group→role mapping.

**Architecture:** New `httpapi/scim*.go` handlers in the existing HTTP API package, mounted at `/scim/v2` outside session auth and protected by per-tenant bearer tokens stored as SHA-256 hashes in a new `scim_tokens` table. Users reuse `provisionUserTx` and `ensureTenantDomain`; groups reuse the existing `groups`/`group_members` tables. SCIM Groups carry membership only; a local admin maps a group to `admin`, `member` or `readonly` (`owner` is never SCIM-managed), and membership changes recompute roles with last-owner protection. All writes are audited with fixed actions; deprovisioning suspends the user, disables the mailbox and deletes web sessions while preserving mail.

**Tech Stack:** Go, PostgreSQL/pgx, existing admin RBAC/session helpers, vanilla admin UI, Node E2E/smoke tools.

**Spec:** `docs/superpowers/plans/2026-11-09-alt8mail-paid-platform-roadmap.md` §4 Phase 1 (SCIM 2.0).

## Global Constraints

- License-gated on the paid `scim` entitlement; unlicensed/expired deployments return 404 for every `/scim/v2` path and never prompt for a token.
- Bearer token format `a8m_scim_<base64url random>`; store only a SHA-256 hash and a short display prefix. The raw token is returned exactly once, on create/rotate.
- Every query and mutation is scoped by the token's `tenant_id`; no cross-tenant reads or writes.
- No JIT login change and no password/role changes from SCIM Users beyond `active`; `owner` can never be granted or removed by SCIM.
- Delete deactivates: `users.status='suspended'`, mailbox inactive, web sessions deleted, mail/attachments/calendar preserved.
- Idempotent externalId: replaying a create with the same externalId returns the existing resource; conflicting externalId/userName returns 409 `uniqueness`.
- Request bodies capped at 1 MiB; responses and requests use `application/scim+json` (accept `application/json` too).
- Never log or audit raw tokens, hashes, passwords or assertion-like payloads. Audit fixed actions and resource IDs/emails only.
- Reuse existing provisioning and session-revocation code; do not fork a second identity store.
- No release, deploy or push. Tests use a local PostgreSQL DSN when integration is needed; unit tests must pass without a DB.

## Review Focus

- Cross-tenant leakage: every SELECT/INSERT/UPDATE/DELETE must include `tenant_id` from the token context; a token from tenant A must never see tenant B by id, email, externalId or group membership.
- Deprovisioning leaves no live access: web sessions deleted, mailbox inactive, but data intact and reactivation safe.
- Lockout: SCIM/group mapping must never demote the last active owner or grant `owner`.
- Token lifecycle: raw token shown once, revocation/rotation immediate, expired tokens rejected, no token material in logs/audit/list responses.
- Input abuse: filter/patch parsing is allowlisted, SQL is parameterized, bodies are bounded, unsupported paths fail closed.

Each focus line has tests in the task that owns the code.

---

### Task 1: Schema, SCIM bearer auth, base endpoints and token admin API

**Files:**
- Create: `backend/migrations/0061_scim.sql`
- Create: `backend/migrations/rollback/0061_scim_down.sql`
- Create: `backend/internal/httpapi/scim.go`
- Create: `backend/internal/httpapi/scim_test.go`
- Modify: `backend/internal/httpapi/server.go`
- Modify: `backend/internal/httpapi/admin_server.go`

**Interfaces:**
- Produces: `type scimTenant struct { TokenID, TenantID, Name string }`; `scimTenantFromContext(context.Context) (scimTenant, bool)`; `(s *Server) scimAuth(http.Handler) http.Handler`; `(s *Server) scimBaseURL() (string, error)`; `writeSCIMJSON`, `writeSCIMError`.
- Produces env-free config: base URL is `strings.TrimRight(cfg.PublicURL, "/") + "/scim/v2"`; HTTPS required except loopback (reuse `samlLoopbackHost`).
- Produces admin routes: `GET/POST /scim/tokens`, `POST /scim/tokens/{id}/rotate`, `DELETE /scim/tokens/{id}` with permission `users.create`.

Migration:

```sql
ALTER TABLE users ADD COLUMN IF NOT EXISTS given_name text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS family_name text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS scim_external_id text;
ALTER TABLE users ADD COLUMN IF NOT EXISTS role_source text NOT NULL DEFAULT 'manual'
  CHECK (role_source IN ('manual','scim'));
CREATE UNIQUE INDEX IF NOT EXISTS users_tenant_scim_external_idx
  ON users (tenant_id, scim_external_id) WHERE scim_external_id IS NOT NULL;

ALTER TABLE groups ADD COLUMN IF NOT EXISTS scim_external_id text;
ALTER TABLE groups ADD COLUMN IF NOT EXISTS scim_role text NOT NULL DEFAULT ''
  CHECK (scim_role IN ('','admin','member','readonly'));
CREATE UNIQUE INDEX IF NOT EXISTS groups_tenant_scim_external_idx
  ON groups (tenant_id, scim_external_id) WHERE scim_external_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS scim_tokens (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name text NOT NULL,
  token_hash text NOT NULL UNIQUE,
  token_prefix text NOT NULL,
  created_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  expires_at timestamptz,
  revoked_at timestamptz
);
CREATE INDEX IF NOT EXISTS scim_tokens_tenant_idx ON scim_tokens (tenant_id);
GRANT SELECT, INSERT, UPDATE, DELETE ON scim_tokens TO a8m_app;
INSERT INTO schema_migrations (version) VALUES ('0061_scim')
ON CONFLICT (version) DO NOTHING;
```

- [ ] Write failing tests: unlicensed/expired/missing-feature `/scim/v2/ServiceProviderConfig` → 404; cleartext non-loopback public URL → 404; missing/wrong/revoked/expired token → 401 SCIM error; valid token → 200; over-limit token attempts → 429 without auth; raw token never appears in list responses.
- [ ] Implement migration, `scimBaseURL`, `scimAuth` (Bearer parse, SHA-256 lookup, `tenant_id` context, async `last_used_at`, per-IP `scimLimit`), base `/scim/v2/ServiceProviderConfig` and `/scim/v2/ResourceTypes`, and SCIM error/JSON helpers.
- [ ] Implement admin token list/create/rotate/revoke. Create returns `{"token":"a8m_scim_...", "id", "prefix", ...}` once; rotate revokes the old row and returns a new raw token; revoke sets `revoked_at`; all audit `admin.scim_token_created|rotated|revoked` with the token ID, never the raw value. Use `crypto/rand`/`randomToken(32)`.
- [ ] Register `/scim/` routes before any catch-all; add `scimLimit *loginLimiter` to `Server` with `newLoginLimiter(oidcCallbackRateLimit, oidcCallbackRateWindow)`.
- [ ] Run `go test ./internal/httpapi -run SCIM -count=1`, `go test ./... -count=1`, `go vet ./...`, `go build ./...`.

### Task 2: SCIM Users — create, list, get, replace, patch, delete

**Files:**
- Create: `backend/internal/httpapi/scim_users.go`
- Create: `backend/internal/httpapi/scim_users_test.go`
- Create: `backend/internal/httpapi/scim_users_integration_test.go`
- Modify: `backend/internal/httpapi/scim.go` (routes)

**Interfaces:**
- Consumes: `scimTenantFromContext`, `writeSCIMJSON`, `writeSCIMError`, `provisionUserTx`, `ensureTenantDomain`, `s.userByEmail`, `adminIPAllowed`, `splitEmail`, `validUserRole`.
- Produces: `scimUserResource(row scimUserRow, base string) map[string]any`; `s.scimListUsers`, `s.scimCreateUser`, `s.scimGetUser`, `s.scimReplaceUser`, `s.scimPatchUser`, `s.scimDeleteUser`.
- Produces: `parseSCIMFilter(raw string, allowed map[string]bool) (field, value string, err error)` and `parseSCIMPagination(r *http.Request) (startIndex, count int, err error)`.

Behavior:
- Create: `userName` required and normalized lowercase email; must use an existing verified tenant domain (`mail_domains.status IN ('verified','manual')`); `externalId` optional, empty string normalized to SQL NULL; `active` default true; `name.formatted` → `users.name` (fallback: the userName local part), `givenName`/`familyName` → new columns; always role `member`, `role_source='manual'`; generate unusable web/mail password hashes (`auth.HashPassword(randomToken(32))`, `hashMailPassword(randomToken(32))`), `forcePasswordChange=true`; `ensureTenantDomain` + `provisionUserTx` in one transaction; `200` with the existing resource when an identical `externalId`+`userName` already exists, else `409 uniqueness` for duplicate `userName`/`externalId`.
- List: filter only `userName eq "<value>"` and `externalId eq "<value>"`; default `startIndex=1`, `count=100`, max 500; reject other filters with 400 `invalidFilter`; return `totalResults`, `itemsPerPage`, `startIndex`, `Resources`.
- Get by UUID within tenant, 404 otherwise.
- PUT/PATCH: mutable fields are `name.*`, `externalId`, `active`; `userName` immutable (same value allowed, change → 409 `mutability`); `emails` must equal `userName`. PATCH supports `add`/`replace`/`remove` for the mutable paths and path-less replace objects; unsupported paths → 400 `invalidPath`.
- `active=false`: suspend user, set mailbox active=false, `DELETE FROM sessions`, audit `scim.user_deactivated`; `active=true`: reactivate and audit `scim.user_reactivated`; never delete mail data.
- DELETE: same deprovision as `active=false`, return 204, audit `scim.user_deleted` with “deactivated; sessions revoked; mailbox preserved”.
- Every write audit uses `userRecord{ID:"", TenantID: tenant, Name: "SCIM token "+token.Name}` and a fixed `scim.*` action.
- Resource shape: `schemas`, `id`, `externalId`, `userName`, `name{formatted,givenName,familyName}`, `displayName`, `emails[{value,type:"work",primary:true}]`, `active`, `roles[{value}]`, `meta{resourceType:"User",created,lastModified,location,version}`.

- [ ] Write failing unit tests for filter/pagination parsing, patch operations, resource JSON, immutable userName, duplicate externalId idempotency.
- [ ] Write failing integration tests (skip unless `A8M_SCIM_TEST_DSN`): create provisions user+mailbox+aliases; duplicate create/idempotent replay; list/get; PUT/PATCH name/active/externalId; deprovision deletes sessions, disables mailbox, preserves mail rows; reactivation; cross-tenant id/filter access returns 404/empty; unknown domain rejected; no `activation`/password handling.
- [ ] Implement handlers and register routes. Cap bodies at 1 MiB and require JSON.
- [ ] Run focused tests with and without the DSN, then `go test ./... -count=1`, `go vet ./...`, `go build ./...`.

### Task 3: SCIM Groups, membership and role mapping

**Files:**
- Create: `backend/internal/httpapi/scim_groups.go`
- Create: `backend/internal/httpapi/scim_groups_test.go`
- Create: `backend/internal/httpapi/scim_groups_integration_test.go`
- Modify: `backend/internal/httpapi/admin_structure.go` (accept `scimRole`)
- Modify: `backend/internal/httpapi/admin_users.go` (manual role changes set `role_source='manual'`)
- Modify: `backend/internal/httpapi/scim.go` (routes)

**Interfaces:**
- Produces: `scimGroupResource(group scimGroupRow, members []scimGroupMember, base string) map[string]any`; `s.scimListGroups`, `s.scimCreateGroup`, `s.scimGetGroup`, `s.scimReplaceGroup`, `s.scimPatchGroup`, `s.scimDeleteGroup`.
- Produces: `(s *Server) scimRecomputeUserRoleTx(ctx context.Context, tx pgx.Tx, tenantID, userID string) (oldRole, newRole string, err error)`.
- Consumes: existing `groups`/`group_members` tables and `adminGroupDetail`; `adminUpdateGroup` gains optional `scimRole` (`""|admin|member|readonly`; `owner` → 400).

Behavior:
- Group create requires `displayName`, optional `externalId`; always `group_type='security'`, status active; synthesize a unique internal email `scim-<uuid>@<tenant primary verified domain>` (409 `invalidValue` if no verified domain); optional initial `members`.
- Group list filter only `displayName eq` / `externalId eq`; pagination as Users.
- PUT/PATCH mutable: `displayName`, `externalId`, `members`; PATCH supports add/remove/replace members with `{value: userID}`; unknown user or cross-tenant user → 400 `invalidValue`; missing user on remove is idempotent.
- Membership changes recompute roles for every affected user in the same transaction:
  - owners are never auto-changed;
  - take the highest `groups.scim_role` among the user's active groups (`admin` > `member` > `readonly`);
  - if a mapped role exists, set `users.role` and `role_source='scim'`; otherwise set `role='member'`, `role_source='manual'`;
  - never grant `owner`; audit `scim.role_mapped` / `scim.role_unmapped` only when the role changes.
- Group delete removes the row (membership cascade), recomputes affected users, audits `scim.group_deleted`; `owner` protection still applies.
- `adminUpdateGroup` with `scimRole` recomputes all group members in one transaction and audits `admin.group_role_mapping_updated`; `owner` is rejected with 400 `ROLE`.
- Manual role changes (`adminUpdateUser`) set `role_source='manual'` so a later recompute does not silently override an explicit admin decision.

- [ ] Write failing unit tests for group resource/membership patch parsing, role precedence, and owner rejection.
- [ ] Write failing integration tests: create/list/get/put/patch/delete; add/remove/replace members; cross-tenant member rejected; group delete recomputes roles; mapped admin/member/readonly applied; unmapped returns member; owner never demoted/granted; manual role change survives recompute; audit actions.
- [ ] Implement groups, mapping and admin `scimRole` support; register routes.
- [ ] Run focused tests with and without the DSN, then full Go tests/vet/build.

### Task 4: Admin SCIM UI, docs and E2E smoke

**Files:**
- Modify: `admin/index.html` (nav)
- Modify: `admin/admin.js` (provisioning view; groups drawer role select)
- Modify: `admin/admin.css` if needed
- Create: `docs/SCIM.md`
- Modify: `tools/admin-ui-smoke.mjs` or add `tools/scim-admin-smoke.mjs`

- [ ] Add a “Provisioning” view under Governance (`data-view="provisioning"`, permission `users.create`) listing SCIM tokens (name, prefix, created, last used, expiry, status) with create, one-time raw-token display + copy, rotate and revoke. Never fetch or render a hash.
- [ ] Add a group role-mapping select (`None|Admin|Member|Readonly`) to the existing group drawer for `security` groups; saving sends `scimRole` and refreshes members’ roles.
- [ ] Add `docs/SCIM.md`: base URL `/scim/v2`, token creation/rotation, Entra/Okta setup notes, supported resources/filters/PATCH paths, 1 MiB limit, idempotency, deprovision semantics (sessions revoked, mail preserved), local group→role mapping and owner restriction, license gate, HTTPS requirement, audit actions, unsupported features (bulk, sort, password sync, JIT).
- [ ] Add a handler-level admin token UI test and extend the admin smoke tool to create/rotate/revoke a token against a test instance when available; skip cleanly otherwise.
- [ ] Run `node --check admin/admin.js`, locale check if touched, and `npm run test:all`.

### Task 5: Security and conformance gate

- [ ] Adversarial review: auth bypass, cross-tenant access, token leakage/rotation, filter/PATCH injection, body bounds, idempotency races, deprovision/session revocation, owner protection, audit bounds, license gate.
- [ ] Conformance matrix: duplicate create, patch add/replace/remove, group membership removal, deprovision/revocation, list/filter/pagination, 401/403/404/409/429 shapes, `ServiceProviderConfig`.
- [ ] Run `go test ./... -count=1`, `go vet ./...`, `go build ./...`, `npm run test:all`; run the SCIM integration suite against the local PostgreSQL with `A8M_SCIM_TEST_DSN`.
- [ ] Final whole-branch review; one fix round + scoped re-review if findings.

## Out of scope

- Bulk operations, sort, ETag/If-Match concurrency, password sync, enterprise user extension, dynamic schema discovery beyond static ServiceProviderConfig/ResourceTypes, IdP-initiated login, JIT accounts, automatic SCIM token expiry policy, Control Plane (infrastructure) UI.
