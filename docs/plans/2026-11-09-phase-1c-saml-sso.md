# Phase 1C — SAML 2.0 SSO Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development`. Steps use checkbox syntax.

**Goal:** Add opt-in, license-gated SAML 2.0 SP-initiated SSO for existing Alt8Mail users, alongside OIDC and password/passkey login.

**Architecture:** Use `github.com/crewjam/saml/samlsp` for SP metadata, redirect AuthnRequests, request tracking, signed assertion parsing and ACS handling. A custom `SessionProvider` bridges a validated SAML assertion to Alt8Mail’s existing `createSession`/cookie flow. Configuration is environment-based for this tranche; per-tenant Control Plane configuration follows later.

**Tech Stack:** Go, `github.com/crewjam/saml/samlsp`, RSA SP key/certificate, PostgreSQL sessions, existing login UI.

**Spec:** `docs/superpowers/plans/2026-11-09-alt8mail-paid-platform-roadmap.md` Phase 1 (SSO).

## Global Constraints

- SAML is opt-in and server-side gated on `entitled("saml")`; without a valid license the config endpoint, start, metadata and ACS are unavailable.
- No JIT user creation. Existing active user lookup only.
- Preserve OIDC, password, passkey, TOTP and recovery-code login.
- SP-initiated flow only; IdP metadata must be HTTPS (allow HTTP only for loopback tests).
- Require signed assertions and signed AuthnRequests; no unsigned response fallback.
- Request tracking/relay state is single-use and bounded to 10 minutes; no open redirects; post-login destination `/`.
- Exact email-domain allowlist when configured; normalized email; no suffix matching.
- Enforce tenant IP and MFA policy. Accept only explicit strong `AuthnContextClassRef` values for MFA; do not use substring matching.
- Audit success and in-limit failures with fixed codes; never log assertions, attributes, tokens or certificates.
- No user creation, release, deploy or push.
- Tests use a local mock IdP; no live IdP.

## Task 1: SAML config, metadata and SP construction

**Files:**
- Modify: `backend/internal/config/config.go`
- Create: `backend/internal/httpapi/saml.go`
- Create: `backend/internal/httpapi/saml_test.go`
- Modify: `backend/internal/httpapi/server.go`
- Modify: `backend/go.mod`, `backend/go.sum`

**Config:** `SAMLIDPMetadataURL`, `SAMLIDPMetadataFile`, `SAMLSPEntityID`, `SAMLSPCertFile`, `SAMLSPKeyFile`, `SAMLDisplayName`, `SAMLEmailDomains []string`. Env prefix `ALT8MAIL_SAML_*`. ACS and metadata URLs derive from `ALT8MAIL_PUBLIC_URL` unless overridden.

- [ ] Add failing tests for config parsing, `samlConfigured()` (license + metadata source + SP cert/key), email-domain normalization and loopback metadata validation.
- [ ] Implement lazy IdP metadata loading from file or HTTPS URL with timeout/cache/retry, `samlsp.New` construction, SP metadata and public `/api/auth/saml/config` (`enabled`, `displayName`).
- [ ] Add `/api/auth/saml/metadata`; gate all endpoints on license entitlement `saml`.
- [ ] Tests: disabled/unlicensed/expired/missing-feature, metadata parse failure, metadata endpoint content type and entity ID.
- [ ] Run `go test ./internal/httpapi -run SAML -count=1`.

## Task 2: Start, ACS, session bridge and UI

- [ ] Add failing tests using a local mock SAML IdP (or `saml.IdentityProvider`) for metadata, start redirect, ACS success, invalid signature, wrong audience/recipient, expired assertion, replay, domain mismatch, unknown user, MFA policy and sanitized errors.
- [ ] Implement `/api/auth/saml/start` using `samlsp.Middleware.HandleStartAuthFlow` with `TrackedRequestMaxAge=10m`.
- [ ] Implement `/api/auth/saml/acs` using `ServeACS` and a custom `SessionProvider.CreateSession`:
  - extract NameID/email and display name;
  - require verified email semantics from a configured attribute/NameID;
  - exact domain allowlist;
  - active existing user only;
  - tenant IP policy;
  - strong MFA `AuthnContextClassRef` when tenant policy requires MFA;
  - create Alt8Mail session, set cookies, update last login, audit, redirect `/`.
- [ ] Add a SAML button to the login UI, fetch `/api/auth/saml/config`, show only when enabled, label fallback, generic `saml_error` message. Keep OIDC button behavior independent.
- [ ] Add `docs/SAML.md` with IdP metadata/ACS URLs, SP cert/key requirements, attribute mapping, MFA class refs, no-JIT behavior and empty-domain warning.
- [ ] Run full tests and E2E.

## Task 3: Security review gate

- [ ] Adversarial review of signature validation, audience/recipient/destination, request-ID replay, relay-state, XML canonicalization, key/cert handling, MFA class refs, audit bounds and license gate.
- [ ] Run `go test ./...`, `go vet ./...`, `npm run test:all`.

## Out of scope

- IdP-initiated SSO, SLO, encrypted assertions, per-tenant Control Plane SAML config, JIT, group-to-role mapping, metadata signing rotation.
