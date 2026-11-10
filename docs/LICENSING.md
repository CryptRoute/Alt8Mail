# Offline License Verification

Alt8Mail accepts an optional, signed offline license. Without one the product
runs in OSS mode; no network call is made and core mail delivery never depends
on the license.

## Configuration

| Variable | Purpose |
|---|---|
| `ALT8MAIL_LICENSE_FILE` | Webmail path to the signed license token |
| `ALT8MAIL_LICENSE_PUBLIC_KEY` | Base64 Ed25519 public key (standard or URL-safe) |
| `CONTROL_LICENSE_FILE` | System Control path to the same token (default `/etc/a8m/license.token`) |
| `CONTROL_LICENSE_PUBLIC_KEY` | System Control copy of the same Ed25519 public key |
| `CONTROL_LICENSE_GROUP` | Group that owns the installed token (default `a8m`) |
| `CONTROL_LICENSE_WARNING_PERCENT` | Seat warning threshold, 1–100 (default 80) |

`CONTROL_LICENSE_FILE` and `ALT8MAIL_LICENSE_FILE` must point at the same
file. The Control Plane upload writes that shared file; the **webmail server**
reads it at startup, while Control re-reads it on every status request. A
missing file or public key is a valid OSS configuration and never stops System
Control from starting.

The installed file is `0640` (root-owned, group `a8m`) so the unprivileged
webmail can read it. When replacing an existing token, Control preserves that
file's uid/gid, applies `0640`, and fails the upload with `LICENSE_WRITE` if it
runs as root but cannot set the ownership. `install.sh` provisions and
preserves `CONTROL_LICENSE_FILE`, `CONTROL_LICENSE_PUBLIC_KEY`,
`CONTROL_LICENSE_GROUP`, `CONTROL_LICENSE_WARNING_PERCENT` and, when a license
file already exists, `ALT8MAIL_LICENSE_FILE`; re-running the installer does not
drop manually added values and normalizes an existing token to root:`a8m`
`0640`.

The webmail server reads and verifies the token once, at startup; Control
re-reads the configured file on every status request. An unreadable or invalid
token is logged and paid features remain disabled; the webmail still starts.
There is no hot reload: to install a renewed or changed license, replace the
file and restart Alt8Mail. Expiry is evaluated against the current clock on
each check, so a running server does move from `valid` to `grace` to `expired`
without restarting.

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
  the product also runs as `edition: "oss"` with no paid features. The Control
  Plane treats a configured-but-missing file as `unlicensed` so a fresh OSS
  deployment shows an empty state rather than an error.

The state, grace and expiry fields are computed from the current clock every
time the snapshot or an entitlement check runs, so lifecycle changes do not
require a restart. Loading a new license file does require a restart.

## Control Plane status and upload

System Control manages the shared file under **License**; both endpoints
require `settings.write`:

- `GET /control/api/license` returns `{"license": {...}, "byTenant": [...]}`.
  `license` contains `configured`, `filePath`, `state`, `edition`, `inGrace`,
  `expired`, `seats`, `usedSeats`, `remainingSeats`, `warning`, `features`,
  `expiresAt`, `tenant`, `licenseId`, `lastModified` and `error`. The token and
  signature are never returned. `byTenant` is populated only for a global
  license.
- `POST /control/api/license` accepts
  `{"token":"A8M1.<payload>.<signature>"}` with
  `Content-Type: application/json` (parameters such as `charset=utf-8` are
  allowed), capped at 64 KiB with no trailing data after the JSON object. It
  verifies the token before touching disk, then writes atomically: a temporary
  file in the destination directory, mode `0640`, group `a8m` (or
  `CONTROL_LICENSE_GROUP`), preserved existing uid/gid, file sync, rename over
  the target, directory sync. It returns
  `{"ok":true,"restartRequired":true,"license":{...}}`.

Every rejected upload (read error, missing public key or path, malformed,
tampered, wrong key, expired, unwritable target) leaves the previous file
byte-for-byte unchanged and returns a fixed error code: `LICENSE_NOT_CONFIGURED`
(503), `LICENSE_TOKEN` (400), `LICENSE_EXPIRED` (400), `BAD_JSON` (400),
`LICENSE_TOO_LARGE` (413), `CONTENT_TYPE` (415) or `LICENSE_WRITE` (500).
`valid` and `grace` tokens are accepted; `expired` tokens are not. The raw
token and verifier errors are never logged and never enter audit metadata; the
audit record `license.uploaded` contains only `licenseId`, `edition`, `seats`
and `expiresAt`.

A successful upload sets `restartRequired: true`: the webmail verifies the
token at startup, so restart the Alt8Mail webmail service to activate it. The
uploaded token is not rendered back to the operator. Seat usage is live and
does not require a restart.

### Seat metering

Seats are counted from `public.users` where `status='active'`,
`deleted_at IS NULL` and `is_system_admin = false` (platform system
administrators do not consume a seat, matching webmail enforcement):

- A license with a `tenant` claim counts only that tenant. `byTenant` is empty
  so another tenant's usage cannot leak through a tenant-scoped license.
- A global license (no `tenant` claim) counts every tenant and returns
  `byTenant` rows `{tenantId, name, activeSeats}` ordered by tenant name.

`warning` is true when `seats > 0` and
`usedSeats * 100 >= seats * CONTROL_LICENSE_WARNING_PERCENT`. `remainingSeats`
never drops below zero, and `usedSeats` may exceed `seats` to show overage.

### Status error categories

`license.error` is always one of these fixed values, never parser or path
detail:

| Value | Meaning |
|---|---|
| `not_configured` | No configured path, an empty/invalid public key, or any configured license file is absent |
| `unreadable` | Read failure or a file larger than 64 KiB |
| `malformed` | Bad envelope, unsupported version or invalid claims |
| `signature` | Tampered payload or a token signed by a different key |

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
Do not commit a production private key. System Control provides the status and
upload API plus live seat metering; per-tenant license records, online
activation and hot reloading remain out of scope.
