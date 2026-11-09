# Phase 1B — OIDC SSO Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` to implement this plan task-by-task. Steps use checkbox syntax.

**Goal:** Add a safe, feature-flagged OIDC SSO sign-in path for existing Alt8Mail users, with an enterprise IdP, while keeping password/passkey sign-in and OSS behavior intact.

**Architecture:** Environment-configured OIDC provider for the first enterprise deployment. The backend owns discovery, state/nonce/PKCE, token exchange, ID-token verification and session creation; the login page exposes a single SSO button only when configured. Per-tenant Control Plane configuration is a follow-up task.

**Tech Stack:** Go, `github.com/coreos/go-oidc/v3/oidc`, `golang.org/x/oauth2`, PostgreSQL session store, existing webmail login UI.

**Spec:** `docs/superpowers/plans/2026-11-09-alt8mail-paid-platform-roadmap.md` Phase 1 (SSO).

## Global Constraints

- SSO must be opt-in via configuration; no configuration means no button and no new failure path.
- Never create a user implicitly in this task. Unknown, disabled, deleted or wrong-domain users are rejected.
- Preserve password, passkey, TOTP and recovery-code login.
- State and nonce are single-use, HttpOnly, Secure when cookies are secure, SameSite=Lax, and expire in 10 minutes.
- Use PKCE S256 and verify issuer, audience, signature, nonce and expiry via `go-oidc`.
- No open redirects. The only post-login destination is `/`.
- Audit successful and failed SSO sign-ins; do not log tokens or raw claims.
- All tests use a local mock OIDC server; never require a live IdP.

## Task 1: OIDC configuration and provider

**Files:**
- Modify: `backend/internal/config/config.go`
- Create: `backend/internal/httpapi/oidc.go`
- Create: `backend/internal/httpapi/oidc_test.go`
- Modify: `backend/go.mod`, `backend/go.sum`

**Config fields:** `OIDCIssuer`, `OIDCClientID`, `OIDCClientSecret`, `OIDCRedirectURL`, `OIDCDisplayName`, `OIDCEmailDomains []string`. Environment names are `ALT8MAIL_OIDC_*`.

- [ ] Add failing tests for config parsing and `oidcConfigured()` (all required fields, domain parsing, disabled when issuer/client/secret/redirect empty).
- [ ] Implement lazy provider discovery in `oidc.go`; do not block server startup on an unreachable IdP.
- [ ] Add `/api/auth/oidc/config` public route returning `{enabled, displayName}`.
- [ ] Run `go test ./internal/httpapi -run OIDC -count=1`.

## Task 2: Start and callback flow

- [ ] Add failing tests using a local `httptest` OIDC issuer with discovery, JWKS, authorization and token endpoints.
- [ ] Implement `GET /api/auth/oidc/start`:
  - generate state, nonce and PKCE verifier;
  - store state/nonce/verifier in a signed or random HttpOnly cookie (max 10 minutes);
  - redirect to the provider authorization endpoint with scopes `openid email profile`.
- [ ] Implement `GET /api/auth/oidc/callback`:
  - verify state cookie and clear it;
  - exchange code with PKCE;
  - verify ID token and nonce;
  - require `email_verified == true` when the claim is present;
  - normalize email and enforce the configured domain allowlist;
  - look up an active user with `userByEmail`;
  - create a session and set the existing session cookies;
  - audit `auth.login` with detail `OIDC SSO`;
  - redirect to `/`; on error redirect to `/?sso_error=<code>` without leaking provider details.
- [ ] Add `frontend` login behavior:
  - `GET /api/auth/oidc/config` is fetched on page load;
  - show an SSO button above the password form when enabled;
  - clicking navigates to `/api/auth/oidc/start`;
  - render a generic SSO error message from `sso_error`.
- [ ] Run `go test ./internal/httpapi -run OIDC -count=1` and `go vet ./...`.

## Task 3: Documentation and security review

- [ ] Create `docs/SSO.md` with IdP redirect URI, required claims, email-domain allowlist, MFA caveat and rotation guidance.
- [ ] Confirm state/nonce cookie clearing on every terminal path.
- [ ] Confirm no token/claim logging.
- [ ] Run `npm run test:all`, `go test ./...`, `go vet ./...`.

## Out of scope

- SAML, SCIM, per-tenant Control Plane SSO configuration, JIT user creation, group-to-role mapping, IdP-initiated login.
