# Phase 1A — Offline Entitlements and License Spine

> **For agentic workers:** This is the first executable slice of the approved Phase 1 plan. Implement with TDD, one task at a time. Steps use checkbox syntax.

**Goal:** Verify signed offline license files, expose tenant entitlements to the application, and keep the OSS core fully usable when no license is present.

**Architecture:** A standalone `internal/licensing` package owns the Ed25519 license envelope and lifecycle state. The HTTP server loads the license at startup from `ALT8MAIL_LICENSE_FILE`, verifies it with `ALT8MAIL_LICENSE_PUBLIC_KEY`, and exposes a non-secret entitlement snapshot in bootstrap. No feature is gated in this slice; later Phase 1 work consumes the snapshot.

**Tech Stack:** Go 1.24, `crypto/ed25519`, `encoding/base64`, `encoding/json`.

**Spec:** `docs/superpowers/plans/2026-11-09-alt8mail-paid-platform-roadmap.md` §4 Phase 1.

## Global Constraints

- No mandatory network call. License verification is local.
- OSS mode is the default and must never fail startup.
- A missing or invalid license is visible in server logs; paid gates fail closed later, core mail does not.
- License payloads are base64url JSON; signatures cover the version prefix and payload.
- No private key is stored in the repository.

## Task 1: License package and verifier

**Files:**
- Create: `backend/internal/licensing/license.go`
- Create: `backend/internal/licensing/license_test.go`

**Interfaces:**
- Produces: `type License`, `type Status`, `type Verifier`, `ParsePublicKey(string) (ed25519.PublicKey, error)`, `(Verifier) Verify(string) (License, error)`, `(License) Status(time.Time) Status`.

- [ ] Write failing tests for:
  - valid license (`State == "valid"`, feature list preserved, seat limit preserved);
  - expired-with-grace (`State == "grace"`, `InGrace == true`);
  - expired beyond grace (`State == "expired"`, `Expired == true`);
  - tampered payload, wrong key, malformed envelope, empty ID, non-positive seats.
- [ ] Run `go test ./internal/licensing -run Test -count=1` and confirm failure.
- [ ] Implement the package.
- [ ] Re-run tests and commit only when the package is green.

## Task 2: Server integration

**Files:**
- Modify: `backend/internal/config/config.go`
- Modify: `backend/internal/httpapi/server.go`
- Modify: `backend/internal/httpapi/server_test.go` or add `license_test.go`
- Modify: `backend/internal/httpapi/state_payload.go` if bootstrap needs the snapshot.

**Interfaces:**
- Consumes: `licensing.ParsePublicKey`, `licensing.Verifier.Verify`, `licensing.License.Status`.
- Produces: `Server.licenseSnapshot() map[string]any`; bootstrap key `license`.

- [ ] Add `LicenseFile` and `LicensePublicKey` config fields from `ALT8MAIL_LICENSE_FILE` and `ALT8MAIL_LICENSE_PUBLIC_KEY`.
- [ ] Add a failing test for `loadLicense` covering no file, invalid key, invalid envelope, valid license.
- [ ] Implement `loadLicense` in `httpapi` using `os.ReadFile` and `licensing`.
- [ ] Add the snapshot to bootstrap with `edition`, `state`, `inGrace`, `expired`, `seats`, `features`, `expiresAt`; never expose the raw token or signature.
- [ ] Run `go test ./internal/httpapi ./internal/licensing` and `go vet ./...`.

## Task 3: Documentation and release note

- [ ] Document `ALT8MAIL_LICENSE_FILE` and `ALT8MAIL_LICENSE_PUBLIC_KEY` in `docs/SYSTEM_CONTROL.md` or the deployment README.
- [ ] Add a sample license envelope to docs **without a private key**.
- [ ] Run `go test ./...` and `go vet ./...`.

## Out of scope for Phase 1A

- Control Plane license upload UI.
- Per-tenant license records and seat metering.
- SSO/SCIM, white-label, restore, accessibility.
- Enforcement of paid feature flags.
