# Offline License Verification

Alt8Mail accepts an optional, signed offline license. Without one the product
runs in OSS mode; no network call is made and core mail delivery never depends
on the license.

## Configuration

| Variable | Purpose |
|---|---|
| `ALT8MAIL_LICENSE_FILE` | Path to the signed license token |
| `ALT8MAIL_LICENSE_PUBLIC_KEY` | Base64 Ed25519 public key (standard or URL-safe) |

The server reads and verifies the token once, at startup. An unreadable or
invalid token is logged and paid features remain disabled; the server still
starts. There is no hot reload: to install a renewed or changed license,
replace the file and restart Alt8Mail. Expiry is evaluated against the current
clock on each check, so a running server does move from `valid` to `grace` to
`expired` without restarting.

## Token format

```
A8M1.<base64url(payload JSON)>.<base64url(ed25519 signature)>
```

The signature covers the literal `A8M1.<payload>` bytes. The payload is:

```json
{
  "version": 1,
  "id": "lic_01J...",
  "tenant": "optional-tenant-id",
  "edition": "enterprise",
  "seats": 50,
  "features": ["sso", "scim", "shared_inbox"],
  "issuedAt": "2026-11-09T12:00:00Z",
  "expiresAt": "2027-11-09T12:00:00Z",
  "graceDays": 14
}
```

Required claims: `version`, `id`, `edition`, `seats` (positive),
`expiresAt`. `tenant`, `features`, `issuedAt` and `graceDays` are optional.

## Lifecycle

- `valid`: current time is at or before `expiresAt`.
- `grace`: current time is after expiry but inside `graceDays`; paid features
  remain usable.
- `expired`: outside expiry and grace; paid entitlements are disabled while
  core mail continues. The redacted snapshot reports `expired: true` and an
  empty `features` list so clients do not treat expired features as usable.
- `unlicensed`: no `ALT8MAIL_LICENSE_FILE` is configured; the product runs as
  `edition: "oss"` with no paid features.
- `invalid`: a license file is configured but could not be read or verified;
  the product also runs as `edition: "oss"` with no paid features.

The state, grace and expiry fields are computed from the current clock every
time the snapshot or an entitlement check runs, so lifecycle changes do not
require a restart. Loading a new license file does require a restart.

The webmail bootstrap exposes a redacted `license` snapshot with edition,
state, seats, features, remaining days and expiry. It never exposes the token
or signature. `GET /api/entitlements` returns the same snapshot.

## Signing

Generate and keep the Ed25519 private key outside the repository. Sign the
`A8M1.<payload>` bytes and encode the signature with unpadded base64url. The
corresponding public key is the only key material that belongs on the server.

Example payload creation:

```sh
payload="$(printf '%s' "$json" | openssl base64 -A | tr '+/' '-_' | tr -d '=')"
# Sign the literal bytes "A8M1.$payload" with your Ed25519 private key.
# Append the base64url signature as the third dot-separated segment.
```

## Development

`internal/licensing` contains verifier tests that generate ephemeral test keys.
Do not commit a production private key. The Control Plane license upload UI,
seat metering and per-tenant license records are Phase 1 follow-up work.
