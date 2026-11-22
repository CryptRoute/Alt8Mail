# Offline License Verification

Alt8Mail accepts an optional, signed offline license. Without one the product
runs as the **free community edition**: no network call is made, core mail
delivery never depends on the license, and up to 25 active users are included.
A signed license raises the seat limit and enables paid capabilities.

## Trust anchor

Production binaries compile the vendor Ed25519 public key into the binary. The
public key is not secret; the private signing key must stay in KMS/HSM or an
offline signing device. Set `A8M_LICENSE_PUBLIC_KEY` (and optionally
`A8M_LICENSE_PUBLIC_KEY_ID`) when building a release:

```bash
A8M_LICENSE_PUBLIC_KEY=<base64-ed25519-public-key> A8M_LICENSE_PUBLIC_KEY_ID=2026-11 A8M_LICENSE_PORTAL_URL=https://licenses.example.com   bash scripts/build-release.sh 3.11.4 dist
```

The GitHub release workflow reads the public key from the repository secret
`ALT8MAIL_LICENSE_PUBLIC_KEY` and the optional key id from the repository
variable `ALT8MAIL_LICENSE_PUBLIC_KEY_ID`. A release built without the secret
is a valid community build, but paid licenses cannot be activated.

### Key rotation

Set `A8M_LICENSE_PUBLIC_KEYS` to the rotation history when building:
`2025-01:<base64>,2026-11:<base64>`. The current key remains
`BuiltinPublicKey`/`BuiltinPublicKeyID`; the ring lets tokens signed under an
older `kid` keep verifying while customers renew onto the current key. Remove
an old key only after every active license signed with it has expired or been
reissued. A token whose `kid` is not in the ring is rejected with a key-id
error rather than falling back to the current key.

The GitHub release workflow reads the ring from the repository variable
`ALT8MAIL_LICENSE_PUBLIC_KEYS`. For a migration window, a deployment can use
`ALT8MAIL_LICENSE_PUBLIC_KEYS` / `CONTROL_LICENSE_PUBLIC_KEYS` alongside the
configured single key; the same development/migration restrictions apply.

The legacy `ALT8MAIL_LICENSE_PUBLIC_KEY` and `CONTROL_LICENSE_PUBLIC_KEY`
environment values are honored only in development/test builds, or when an
operator explicitly sets `ALT8MAIL_LICENSE_ALLOW_CONFIG_KEY=1` /
`CONTROL_LICENSE_ALLOW_CONFIG_KEY=1` for a migration window. A production
deployment must use the compiled key.

## Configuration

| Variable | Purpose |
|---|---|
| `ALT8MAIL_LICENSE_FILE` | Webmail path to the signed license token |
| `ALT8MAIL_LICENSE_PUBLIC_KEY` | Development/migration-only configured Ed25519 public key |
| `ALT8MAIL_LICENSE_PUBLIC_KEYS` | Development/migration-only rotation ring (`kid:base64,kid:base64`) |
| `ALT8MAIL_LICENSE_ALLOW_CONFIG_KEY` | Explicitly allow the configured key in a non-development build |
| `ALT8MAIL_LICENSE_PORTAL_URL` | Optional licensing-portal base URL for the upgrade link |
| `ALT8MAIL_LICENSE_USAGE_URL` | Optional deployment usage heartbeat URL |
| `ALT8MAIL_LICENSE_USAGE_TOKEN` | Deployment usage token issued by the portal |
| `ALT8MAIL_LICENSE_USAGE_INTERVAL_HOURS` | Heartbeat interval, 1–168 hours (default 24) |
| `ALT8MAIL_LICENSE_TENANT` | Optional expected tenant claim; a missing or different claim makes the license invalid |
| `CONTROL_LICENSE_FILE` | System Control path to the same token (default `/etc/a8m/license.token`) |
| `CONTROL_LICENSE_PUBLIC_KEY` | Development/migration-only Control public key |
| `CONTROL_LICENSE_PUBLIC_KEYS` | Development/migration-only Control rotation ring |
| `CONTROL_LICENSE_ALLOW_CONFIG_KEY` | Explicitly allow the configured key in a non-development build |
| `CONTROL_LICENSE_PORTAL_URL` | Optional licensing-portal base URL for the Control upgrade link |
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

The signature covers the literal `A8M1.<payload>` bytes. The envelope prefix is
unchanged across schema versions; the payload's `version` selects the claim
set. Current tokens are version 2:

```json
{
  "version": 2,
  "id": "lic_01J...",
  "kid": "2026-11",
  "tenant": "optional-tenant-id",
  "edition": "enterprise",
  "plan": "enterprise",
  "seats": 50,
  "limits": {
    "storageBytes": 5497558138880,
    "domains": 4,
    "mailboxes": 500
  },
  "deploymentId": "dep_01J...",
  "trial": false,
  "features": ["sso", "scim", "shared_inbox"],
  "issuedAt": "2026-11-09T12:00:00Z",
  "expiresAt": "2027-11-09T12:00:00Z",
  "graceDays": 14
}
```

Required claims in version 2: `version`, `id`, `edition`, `seats` (positive),
`issuedAt`, `expiresAt`. `kid`, `tenant`, `plan`, `limits`, `deploymentId`,
`trial`, `features` and `graceDays` are optional. `plan` defaults to `edition`.
A trial token must not carry a `graceDays` value other than zero. A verifier
built with a key id rejects a token whose `kid` names a different key; a
verifier with no configured key id accepts any `kid` for migration.

Version 1 tokens (`version: 1`) remain valid: they cannot carry `kid`, `plan`,
`limits`, `deploymentId` or `trial`, and get the edition as their effective
plan. New issuance always uses version 2. The product rejects unknown versions
with `unsupported` rather than guessing at claims.

`limits` values are advisory ceilings for product features that opt into them;
`seats` remains the enforced active-user claim.

### Gate decisions

- `data_residency` and `client_encryption` are not in the paid catalog. They
  remain ungated until the product decision is made; do not advertise them as
  paid capabilities.
- API keys and webhooks remain OSS for adoption. Their routes are not paid
  gates; only the audit/SIEM export of the events they produce is paid.
- Backup and mailbox restore are paid (`backup_restore`) because they are a
  root-level recovery capability.

## Optional usage heartbeat

A deployment can report license usage to the private portal by setting
`ALT8MAIL_LICENSE_USAGE_URL` and `ALT8MAIL_LICENSE_USAGE_TOKEN`. The heartbeat
runs at most once per interval (default 24 hours) and sends only the license ID,
deployment ID, product version, active seat count, licensed seat count and the
enabled-feature keys. It never sends mail, contacts, subjects, message bodies or
mailbox content. The heartbeat is opt-in, has no kill switch and is never
required for core mail, login or data access.

The portal exposes `POST /api/v1/usage` for the report and stores the history
for the operator console. Rotate a deployment's usage token after a suspected
exposure; the old token stops working immediately.

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
  `license` contains `configured`, `filePath`, `state`, `edition`, `plan`,
  `trial`, `deploymentId`, `limits`, `inGrace`, `expired`, `seats`,
  `usedSeats`, `remainingSeats`, `warning`, `warningPercent`, `features`,
  `expiresAt`, `tenant`, `licenseId`, `lastModified` and `error`. The token and
  signature are never returned.
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
does not require a restart.

Control verifies only the license envelope; it does not read or validate
`ALT8MAIL_LICENSE_TENANT`. In a tenant-pinned deployment, a token whose
`tenant` claim differs from the webmail pin (or is absent) is accepted by
Control and returns 200, but the webmail rejects it as invalid after restart.
Verify the tenant claim before uploading to a pinned deployment.

If the file is not readable by the `a8m` user
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

The effective active-user limit is the signed `seats` claim while the license
is `valid` or `grace`; otherwise it is the **25-user community allowance**.
Existing users above the limit are never silently deactivated, but new
activations are refused until usage falls below the effective limit. A
tenant-scoped paid license limits only its tenant while valid; the community
allowance and an expired/invalid license apply across all tenants.

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

The webmail enforces every activation path in the webmail/admin/SCIM API
inside its existing provisioning transaction:

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

System Control uses the same seat policy and advisory/tenant locks for mailbox
creation, status/active changes, postmaster creation, shell-mailbox import and
sync import. The former platform-operator override is removed. Direct SQL or
binary replacement by a root operator remains a deliberate bypass, not a
supported path.

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

## One-click enterprise upgrade

A release built with `A8M_LICENSE_PORTAL_URL` and the vendor trust anchors
offers **Infrastructure → License → One-click upgrade** in System Control.
The flow is:
1. Control creates a deployment id, records a pending upgrade under
   `/var/lib/alt8mail-control/license-upgrade.json` and opens the portal's
   `/upgrade` page with the deployment id and a Control callback URL.
2. The customer starts a trial, buys a plan or signs in to an existing account.
   The portal issues a signed licence and returns a one-time claim code to the
   Control callback.
3. Control claims the licence once, verifies it with the compiled trust anchor
   or `CONTROL_LICENSE_PUBLIC_KEY`, writes the shared token file, and updates
   both `app.env` and `control.env` with the licence path and public key.
4. If the portal publishes an enterprise bundle, Control downloads it, checks
   the SHA-256, verifies the Ed25519 release signature (when a release public
   key is configured), extracts it safely, copies the current release, overlays
   the enterprise binaries and switches `/opt/a8m/current` to the new release.
5. Control restarts the webmail, admin and control services. The previous
   release stays on disk, and a failed step leaves the running release untouched.

Only the presence of the enterprise binary makes the paid capabilities execute.
A valid enterprise token on a community binary is reported as
`binaryEdition: community`; the licence page shows an **Install enterprise
release** action instead of claiming the paid features are active. That action
sends the uploaded token to `POST /api/v1/enterprise/install` on the portal,
which authorizes a one-time bundle claim without reissuing the licence. The
same portal endpoint is the source for Software updates after the enterprise
binary is installed.

The portal serves the newest
`alt8mail-enterprise-<version>-linux-amd64.tar.gz` from
`PORTAL_ENTERPRISE_BUNDLE_DIR`, together with its `.sha256`, `.sig` and
`enterprise-release-public.key`. Build an enterprise release with
`build-enterprise.sh` and `ENTERPRISE_RELEASE_SIGNING_KEY`; the signature covers
the tarball digest using the `a8m-enterprise-release-sha256:<hex>` message.
`CONTROL_ENTERPRISE_RELEASE_PUBLIC_KEY` pins the release key; when it is empty,
Control uses the public key returned by the portal for the same response.

Existing customers can also download a licence from the portal console or use
**Send to Alt8Mail** while a deployment is waiting for a claim. In both cases the
portal returns the signed token through the same one-time callback, so an
operator never has to copy a token onto the server.

## Paid feature keys (Phase 3–6)

The license `features` array is the server-side entitlement vocabulary. A
license may list individual keys or the wildcard `all` / `*`. The canonical
Phase 3–6 keys are:

```
recipient_scheduling, mail_followups, template_gallery, deliverability,
campaign_tracking, ai_triage, ai_obligations, ai_search, ai_meeting_prep,
office_ai, ai_policy_assist, mcp_governance, ai_budget, dlp_advanced,
ediscovery, byok, file_governance, audit_export, crm_sync, migration,
analytics, security_awareness, support_ops
```

Legacy keys still work for the features they already gate (`sso`, `saml`,
`scim`, `shared_inbox`, `shared_drafts`, `team_templates`, `automation`,
`mail_merge`, `booking_pages`, `white_label`). Existing licenses do **not**
silently gain new Phase 3–6 capabilities. Reissue customer licenses with the
new keys (or `all` for an enterprise/suite license). System Control and the
webmail license snapshot expose `availableFeatures` and `missingFeatures` so
operators can identify licenses that need reissue. The webmail/API return
`LICENSE_FEATURE` for a gated capability when the key is absent or expired.

### Upgrade/reissue runbook

1. Read the current license snapshot in System Control → License.
2. Copy the existing tenant/seat/expiry claims.
3. Add the missing Phase 3–6 keys, or `all` where the contract covers the full
   suite; keep seat and expiry unchanged.
4. Re-sign with the offline Ed25519 key and upload the new token.
5. Verify the webmail license snapshot shows no unexpected `missingFeatures`.
6. Record the reissue and the old/new license IDs in the customer record.

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
