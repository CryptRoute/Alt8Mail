# Phase 1F — White-Label Branding and Custom Webmail Domains

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development`. Steps use checkbox syntax.

**Goal:** Let a paid tenant present Alt8Mail as its own product on login and in the webmail, including a validated custom webmail hostname, logo/support/legal links, and removal of “via Alt8Mail” attribution, while unlicensed deployments keep the Alt8Mail identity.

**Architecture:** Extend the existing tenant branding policy (`tenant.settings.branding`) with `logoUrl`, `supportUrl`, `legalUrl`, `hideAttribution` and `webmailHosts`. `GET /api/branding` resolves a tenant by an enabled custom webmail hostname first, then by mail domain, and returns a sanitized public payload. Extended fields and attribution removal are gated on the paid `white_label` entitlement; unentitled tenants get defaults. Custom hostnames are validated against the Control Plane Web Net route/certificate inventory (Control remains authoritative for DNS/TLS/nginx); the login UI applies the returned branding progressively.

**Tech Stack:** Go, pgx, `adminTenantPolicy`, webmail login UI, Control Web Net inventory, tenant admin UI.

**Spec:** `docs/superpowers/plans/2026-11-09-alt8mail-paid-platform-roadmap.md` §4 Phase 1 (White-label).

## Global Constraints

- White-label is paid: `logoUrl`, `supportUrl`, `legalUrl`, `hideAttribution` and custom `webmailHosts` are ignored in the public payload unless `entitled("white_label")`. Unentitled tenants keep `productName`, color, support email and login message as today.
- The Control Plane remains authoritative for hostname routes, certificates and nginx. A custom hostname is accepted for branding only if a matching enabled `webmail` Web Net route exists and has a certificate; otherwise the update is rejected with a fixed error and the current hostname set is preserved.
- Hostnames must be lowercase DNS names (no scheme, path, port, wildcard or IP), max 5 per tenant, unique across tenants. URLs must be absolute `https://` URLs without userinfo, max 512 chars.
- No raw HTML from branding fields; the login UI sets text and attributes (`textContent`, safe URL assignment, no `innerHTML` interpolation). Logo images must not break the page when missing.
- Attribution removal only removes “via Alt8Mail”/product attribution; security notices (TLS, password/2FA policy) stay.
- Existing branding behavior (product name/color/support/login message) and the default Alt8Mail identity remain unchanged when fields are empty.
- No release, deploy or push.

## Review Focus

- Entitlement bypass: an unlicensed/expired tenant cannot set or receive extended branding or hide attribution by editing settings directly.
- Hostname confusion: tenant A cannot claim tenant B's hostname, a mail domain owned by another tenant, or a host with no enabled Web Net route/certificate; an unknown host falls back to the default identity, never another tenant.
- Injection: branding URLs/logo/names cannot inject HTML/JS, redirect off-origin, or break the login page when malformed.
- Cross-tenant isolation: public hostname lookup and admin updates are tenant-scoped; no settings leak in the public payload.
- No regression: default login, OIDC/SAML/SCIM login, product name/color/support/footer behavior, and accessibility of the login form.

Each focus line has tests in the task that owns the code.

---

### Task 1: Branding policy, hostname resolution and public payload

**Files:**
- Modify: `backend/internal/httpapi/admin_policies.go`
- Modify: `backend/internal/httpapi/branding.go`
- Create: `backend/internal/httpapi/branding_test.go` (or extend `phase4_test.go`)
- Create: `backend/migrations/0062_tenant_webmail_hosts.sql`
- Create: `backend/migrations/rollback/0062_tenant_webmail_hosts_down.sql`
- Modify: `deploy/postgresql/grants.sql`

**Interfaces:**
- Extends `adminBrandingPolicy` with `LogoURL string`, `SupportURL string`, `LegalURL string`, `HideAttribution bool`, `WebmailHosts []string`.
- Produces `(s *Server) tenantBrandingForHost(ctx context.Context, host string) (adminTenantPolicy, bool)`; `(s *Server) validateBrandingPolicy(ctx context.Context, tenantID string, policy *adminTenantPolicy) []string`; `(s *Server) applyBrandingEntitlement(policy adminTenantPolicy) adminTenantPolicy`.
- Produces public payload fields `logoUrl`, `supportUrl`, `legalUrl`, `hideAttribution` in addition to the existing fields.

Behavior:
- `tenantBrandingForHost`: normalize host (strip port, lowercase, trailing dot); first find an active tenant whose `settings->'branding'->'webmailHosts'` contains the host; if none, fall back to the existing mail-domain lookup; return the default policy when no match.
- Migration 0062 adds `public.tenant_webmail_hosts (hostname citext PRIMARY KEY, tenant_id uuid REFERENCES tenants ON DELETE CASCADE, created_at)` for uniqueness/auditability, backfilled from existing `settings` if present; grant SELECT/INSERT/UPDATE/DELETE to `a8m_app`; add SELECT on `control.webnet_routes` (columns needed: hostname, service, enabled, certificate_name) and `control.tls_assignments` if required, to `a8m_app` for validation. Keep `settings` as the source read by the webmail; the table is the uniqueness index. Rollback drops the table/grants.
- Validation: hostname regex `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`, max 253; max 5; no duplicate within the tenant; globally unique via the table; a matching enabled `control.webnet_routes` row with `service='webmail'` and a non-empty certificate assignment is required. `logoUrl`/`supportUrl`/`legalUrl` must parse as absolute https URLs with no userinfo, host, max 512. `hideAttribution` is stored but only effective when entitled.
- `applyBrandingEntitlement`: when not `entitled("white_label")`, force extended fields empty and `hideAttribution=false` in the public payload; preserve the existing product/color/support/message fields.
- Admin policy update: validate the merged branding object; reject with fixed field errors (no HTML echo) and audit `admin.tenant_updated` as today. Entitled tenants may store extended fields; unentitled updates to extended fields return 403 `WHITE_LABEL`.
- Public `/api/branding` cache key/`Cache-Control` unchanged; do not include tenant id/name or settings beyond the payload.

- [ ] Write failing tests: hostname resolution priority and fallback; cross-tenant hostname rejection; missing webnet route/cert rejection; URL/hostname validation; entitlement stripping (unlicensed/expired vs valid/grace); payload shape/no extra fields; default policy on unknown host; admin update 403 for extended fields without entitlement.
- [ ] Implement migration/grants, policy fields/validation, host lookup and payload gating.
- [ ] Run `go test ./internal/httpapi -run 'Branding|WhiteLabel' -count=1`, `go test ./... -count=1`, `go vet ./...`, `go build ./...`; apply/rollback the migration on a disposable PostgreSQL.

### Task 2: Login/branding UI, tenant admin form, Control Web Net docs

**Files:**
- Modify: `public/app.js` (`applyTenantBranding`, reaction attribution if applicable)
- Modify: `public/index.html` (only if a stable logo/legal link container is needed)
- Modify: `admin/admin.js` (branding form fields)
- Modify: `admin/index.html` (branding form markup)
- Modify: `docs/ADMIN.md`, `docs/SYSTEM_CONTROL.md`, `docs/LICENSING.md` if needed
- Create: `docs/WHITE_LABEL.md`

- [ ] Apply `logoUrl` (img with alt = product name, max height, graceful on error), `supportUrl`/`legalUrl` links in the login footer, and hide “via Alt8Mail” attribution only when `hideAttribution` is true in the payload. Keep TLS/security notices.
- [ ] Replace hardcoded login-surface product strings with the branding value where the existing progressive enhancement already does; do not rewrite unrelated app copy in this task.
- [ ] Extend the tenant admin branding form: logo URL, support URL, legal URL, hide-attribution toggle (disabled/explained when unentitled), custom webmail hostnames (add/remove, max 5, validation errors). Keep existing product name/color/support email/login message.
- [ ] `docs/WHITE_LABEL.md`: paid gate, hostname requirements, Control Web Net route + certificate steps, tenant branding steps, fallback behavior, attribution rules, cache behavior, troubleshooting (DNS/TLS/nginx).
- [ ] Add `docs/SYSTEM_CONTROL.md` note that Web Net owns custom hostnames/certs; Control UI lists tenant hostnames if practical.
- [ ] Run `node --check public/app.js`, `node --check admin/admin.js`, locale check if copy keys are added, `npm run test:all`, and a manual/scripted login render check for default and branded payloads.

### Task 3: Security and UX gate

- [ ] Adversarial review: entitlement bypass, hostname takeover/cross-tenant lookup, URL/logo injection, public payload leakage, cache poisoning, unlicensed fallback.
- [ ] Verify default identity, OIDC/SAML/SCIM login, accessibility (labels/contrast/keyboard), and no regression to outbound branding footer.
- [ ] Run full Go tests/vet/build, `npm run test:all`, migration rollback/reapply; fix findings with one scoped re-review.

## Out of scope

- Per-tenant certificate issuance UI changes (reuses Control Web Net), multiple logos per hostname, email-template theming beyond the existing footer, white-label of the Control Plane itself, custom SMTP/IMAP hostnames, and removing security notices.
