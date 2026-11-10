# Offline License Verification

Alt8Mail accepts an optional, signed offline license. Without one the product
runs in OSS mode; no network call is made and core mail delivery never depends
on the license.

## Configuration

| Variable | Purpose |
|---|---|
| `ALT8MAIL_LICENSE_FILE` | Webmail path to the signed license token |
| `ALT8MAIL_LICENSE_PUBLIC_KEY` | Base64 Ed25519 public key (standard or URL-safe) |
| `ALT8MAIL_LICENSE_TENANT` | Optional expected tenant claim; a missing or different claim makes the license invalid |
| `CONTROL_LICENSE_FILE` | System Control path to the same token (default `/etc/a8m/license.token`) |
| `CONTROL_LICENSE_PUBLIC_KEY` | System Control copy of the same Ed25519 public key |
| `CONTROL_LICENSE_GROUP` | Group that owns the installed token (default `a8m`) |
| `CONTROL_LICENSE_WARNING_PERCENT` | Seat warning threshold, 1–100 (default 80) |

`CONTROL_LICENSE_FILE` and `ALT8MAIL_LICENSE_FILE` must point at the same
file. The Control Plane upload writes that shared file; the **webmail server**
reads it at startup, while Control re-reads it on every status request. A
missing token file is a valid OSS configuration: both processes treat a
configured-but-absent file as `unlicensed`, so a fresh install with the shared
path already provisioned never reports `invalid` and never fails startup. A
missing or unusable public key only matters once a token file exists.

The installed file is mode `0640` with group `a8m` so the unprivileged
webmail can read it. When replacing an existing token, Control preserves the
file's owner uid, normalizes its group to `a8m` (or `CONTROL_LICENSE_GROUP`),
applies `0640`, and fails the upload with `LICENSE_WRITE` if it runs as root but
cannot set the ownership. This repairs a legacy root:root token on the next
upload, so a successful upload is actually readable by the webmail after the
restart. `install.sh` provisions `CONTROL_LICENSE_FILE`,
`CONTROL_LICENSE_PUBLIC_KEY`, `CONTROL_LICENSE_GROUP`,
`CONTROL_LICENSE_WARNING_PERCENT` and `ALT8MAIL_LICENSE_FILE` unconditionally;
it preserves and restores the webmail-only `ALT8MAIL_LICENSE_TENANT` pin (or
takes an initial `A8M_LICENSE_TENANT`), seeds Control values from existing
`ALT8MAIL_*` values when Control has none (and the webmail values from Control),
preserves every manually configured value across reinstalls, and normalizes an
existing token to root:`a8m` `0640`. `scripts/alt8mail-update.sh` applies the
same fallback seeding and permission normalization when that updater version
runs, and never rewrites an existing `ALT8MAIL_LICENSE_TENANT`. Because the
running updater installs the bundled script only at the end of an update, an
installation updating from an older release should re-run `install.sh`
(idempotent) or add the missing values manually if a Control upload must load
immediately after the first restart.

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
- `unlicensed`: no `ALT8MAIL_LICENSE_FILE` is configured, or the configured
  file does not exist. The product runs as `edition: "oss"` with no paid
  features. This is the normal state of a fresh install whose shared path is
  provisioned before the first token is uploaded.
- `invalid`: a license file exists but could not be read or verified; the
  product also runs as `edition: "oss"` with no paid features. The Control
  Plane and the webmail use the same rule, so a configured-but-missing file is
  `unlicensed` and shows an empty state rather than an error.

The state, grace and expiry fields are computed from the current clock every
time the snapshot or an entitlement check runs, so lifecycle changes do not
require a restart. Loading a new license file does require a restart.

## Control Plane status and upload

System Control manages the shared file under **Infrastructure → License**; the
menu item and both endpoints require `settings.write`, so read-only roles never
see the view or its data.

The view shows:

- a status card with the edition, a text label and tone for `valid`, `grace`,
  `expired`, `unlicensed` and `invalid` (never color alone), the expiry date,
  days remaining, feature entitlements, tenant scope, license ID, file
  modification time and the configured path;
- an OSS empty state when no path is configured or the file is missing, and a
  fixed explanation per `not_configured`/`unreadable`/`malformed`/`signature`
  category when the license is invalid (parser text is never shown);
- a live seat meter with used, licensed and remaining counts, the configured
  warning percentage (shown in the legend and as a track marker) and whether it
  is reached, `role="meter"` with `aria-valuenow`/`aria-valuemin`/
  `aria-valuemax`/`aria-valuetext`, and a visible text fallback;
- a per-tenant table (tenant, tenant ID, active seats and a total row) for a
  global license, or an empty state explaining the tenant scope for a
  tenant-scoped license;
- an upload form that accepts a token file or pasted token, enforces the
  64 KiB limit client-side, sends `POST /control/api/license` as
  `{"token":"…"}`, shows an in-flight state, maps every fixed error code to an
  operator-facing message, and clears the file/paste inputs after any outcome;
- on success, the exact instruction **“Restart the Alt8Mail webmail service to
  activate this license”** together with the API's `restartRequired` flag. The
  token is never rendered back, logged, or written to browser storage.

Both endpoints require `settings.write` and the CSRF header for the upload:

- `GET /control/api/license` returns `{"license": {...}, "byTenant": [...]}`.
  `license` contains `configured`, `filePath`, `state`, `edition`, `inGrace`,
  `expired`, `seats`, `usedSeats`, `remainingSeats`, `warning`,
  `warningPercent`, `features`, `expiresAt`, `tenant`, `licenseId`,
  `lastModified` and `error`. The token and signature are never returned.
  `byTenant` is populated only for a global license.
- `POST /control/api/license` accepts
  `{"token":"A8M1.<payload>.<signature>"}` with
  `Content-Type: application/json` (parameters such as `charset=utf-8` are
  allowed), capped at 64 KiB with no trailing data after the JSON object. It
  verifies the token before touching disk, then writes atomically: a temporary
  file in the destination directory, mode `0640`, group `a8m` (or
  `CONTROL_LICENSE_GROUP`), preserved owner uid, file sync, rename over the
  target, directory sync. It returns
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
token at startup, so restart the Alt8Mail webmail service
(`systemctl restart a8m-server`) to activate it. The Control Plane reads the
new file immediately, so the status card and seat meter update without a
restart; the webmail keeps its previously loaded token until it restarts. The
uploaded token is not rendered back to the operator. Seat usage is live and
does not require a restart. If the file is not readable by the `a8m` user
because of ownership/permissions, the webmail starts in OSS mode; keep the file
root-owned with group `a8m` (or `CONTROL_LICENSE_GROUP`) and mode `0640`.

### Seat metering

Seats are counted from `public.users` where `status='active'`,
`deleted_at IS NULL` and `is_system_admin = false` (platform system
administrators do not consume a seat, matching webmail enforcement):

- A license with a `tenant` claim counts only that tenant. `byTenant` is empty
  so another tenant's usage cannot leak through a tenant-scoped license.
- A global license (no `tenant` claim) counts every tenant and returns
  `byTenant` rows `{tenantId, name, activeSeats}` ordered by tenant name.

`warning` is true when `seats > 0` and
`usedSeats * 100 >= seats * CONTROL_LICENSE_WARNING_PERCENT`; the effective
threshold is returned as `warningPercent` (default 80) so the UI can show it
and mark it on the meter. `remainingSeats` never drops below zero, and
`usedSeats` may exceed `seats` to show overage.

### Seat enforcement

User activation is gated only by a loaded license whose lifecycle state is
`valid` or `grace` and whose `seats` claim is positive. OSS, unlicensed,
`invalid` and `expired` deployments keep core user creation available; expired
paid features are gated separately by the entitlement checks.

A license with an empty `tenant` claim meters and enforces the seat limit
across all tenants. A license with a `tenant` claim meters and enforces only
that tenant; every other tenant remains unlimited (core mail and user creation
stay available there). When `ALT8MAIL_LICENSE_TENANT` is configured it must
equal the payload's `tenant` claim exactly; an empty or different claim makes
the whole license `invalid`. A claim that names no existing tenant counts zero
users, so enforcement limits nobody until that tenant exists; the webmail logs
a startup warning when that lookup finds no tenant.

Seats are consumed by users with `status='active'`, `deleted_at IS NULL` and
`is_system_admin = false`. Invited and suspended users do not consume a seat
until they become active. Deleting or suspending an active user frees its seat.
Platform system administrators never consume seats. A license that shrinks
below current usage remains over-limit: existing users are not silently
deactivated, and new activations are refused until usage falls below the limit.

The webmail enforces every activation path inside its existing provisioning
transaction:

- admin create with `status: "active"` (invited/suspended creates are free);
- admin restore of a closed account;
- admin status update that activates an invited or suspended user;
- admin bulk activation;
- each CSV/spreadsheet import row (all imported users are active);
- SCIM create with `active: true` (or no `active` attribute);
- SCIM PUT/PATCH reactivation (`active: false` to `true`);
- creating an organization with an active owner (a global license counts the
  new owner; a tenant-scoped license does not count a new, unrelated tenant);
- revoking platform system-administrator access from an active user, which
  converts a non-counted user into a counted one. Granting system-administrator
  access frees a seat and is not gated.

Non-activation edits and deactivations are unaffected. Before creating or
activating, the transaction locks the tenant row
(`SELECT id FROM tenants WHERE id = $1 FOR UPDATE`); a global license also takes
a transaction-scoped advisory lock keyed by the license, then re-counts active
seats. Decisions that depend on the target user's current status re-read the
target `FOR UPDATE` after taking the seat lock, so a concurrent suspend,
restore or create cannot be missed. Concurrent activations therefore cannot
exceed the licensed limit, both within one tenant and across tenants of a
global license.

A rejected activation creates no user, mailbox, tenant or session. The admin
API returns HTTP 409 with the fixed code `SEAT_LIMIT`; SCIM returns HTTP 409
with `scimType: "invalidValue"` and the fixed detail `License seat limit
reached`. Both write the audit action `license.seat_limit_blocked` with the
tenant, limit and live usage (admin operations in `admin_audit_log`, SCIM in
`audit_log`). The raw license token and signature are never logged or audited.

Control Plane mailbox/user status operations, provisioning and sync jobs are
not gated by this webmail check; whether they should enforce the same limit or
remain an explicit operator override is a Task 4 decision. A Control Plane
action that activates users can therefore exceed the webmail limit until that
decision is made.

The webmail bootstrap and `GET /api/entitlements` expose `usedSeats` and
`seatLimit` (alongside `seats`) so the UI can show live usage without reading
the token.

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
Do not commit a production private key. System Control provides the
**Infrastructure → License** view, the status and upload API, and live seat
metering; the webmail enforces the limit on user activation as described above. Per-tenant license records, online activation
and hot reloading remain out of scope.
