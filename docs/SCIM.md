# SCIM 2.0 provisioning

Alt8Mail implements a tenant-scoped SCIM 2.0 service for users and groups. An
identity provider (IdP) authenticates with a per-tenant bearer token and can
create, update, deactivate and delete users, manage group membership, and drive
a local group-to-role mapping.

SCIM is an entitlement-gated feature (the `scim` license feature). The admin
console exposes token management in **Governance → Provisioning**; that view
requires the `roles.manage` permission because a SCIM token can change
membership of role-mapped groups.

The implementation follows RFC 7643 (resource schemas) and RFC 7644 (protocol)
for the supported subset documented below. Unsupported features are listed in
[Limitations](#limitations).

## Endpoint and transport

| Item | Value |
| --- | --- |
| SCIM base URL | `https://<public-host>[/<path-prefix>]/scim/v2` |
| Public URL source | `ALT8MAIL_PUBLIC_URL` (trailing slash trimmed, then `/scim/v2` appended) |
| Authentication | `Authorization: Bearer a8m_scim_…` |
| Request content types | `application/scim+json`, `application/json` |
| Response content type | `application/scim+json; charset=utf-8` |
| Request body limit | 1 MiB per SCIM write request |
| Transport | HTTPS required; plain HTTP is accepted only for loopback development |

`ALT8MAIL_PUBLIC_URL` must be an absolute `http`/`https` URL with a host and
without userinfo, query or fragment. It may include a path prefix; the SCIM
base URL is that value with `/scim/v2` appended. Use a host-only value where
possible. A path prefix is supported only when the reverse proxy strips it
before forwarding to the API (see below). If the public URL is missing,
malformed, or uses plain HTTP on a non-loopback host, every SCIM request
returns a plain `404` (fail closed). Use the public hostname that IdPs can
reach; bearer tokens must never cross plain HTTP.

The admin console's Provisioning view displays this authoritative base URL
from the server bootstrap payload, so it is also correct when the admin
console is served from a different host. If the server cannot derive the URL
because `ALT8MAIL_PUBLIC_URL` is missing or invalid, the view points operators
to this document instead of showing a guessed address.

Without a valid `scim` entitlement, every `/scim/v2` request (including
discovery) returns a plain `404`, not a `401`. Tokens cannot be used until the
deployment is licensed.

### nginx reverse proxy

The bundled site configuration proxies SCIM to the API server instead of the
static web app:

```nginx
location ^~ /scim/ {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

`deploy/nginx/a8m-site.conf` contains this block for new installations.
Existing installations can add the missing public route with:

```sh
sudo bash scripts/alt8mail-ensure-nginx-routes.sh
```

The script edits the active nginx site or its managed
`/etc/nginx/snippets/a8m-public-routes.conf` include, validates with `nginx -t`
and restores the previous files if validation fails.

The bundled block and route script assume a host-only
`ALT8MAIL_PUBLIC_URL`. If you configure a path prefix, for example
`ALT8MAIL_PUBLIC_URL=https://mail.example.com/mail`, the advertised base is
`https://mail.example.com/mail/scim/v2`. The proxy must strip `/mail` before
forwarding to the API:

```nginx
location ^~ /mail/scim/ {
    proxy_pass http://127.0.0.1:8080/scim/;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

The trailing `/scim/` in `proxy_pass` is required to replace the prefixed
path; forwarding the prefixed URI unchanged would not match the API routes.

When nginx is managed by Alt8Mail Web Net, the installer detects the
`Managed by Alt8Mail Web Net` site marker, sets `WEB_NET_MANAGED=1` and
preserves that site during upgrades. In that mode the external Web Net proxy
must forward `https://<public-host>/scim/` (or the configured path prefix,
rewritten as above) to `127.0.0.1:8080` with the same headers; the local
installer will not overwrite the managed proxy configuration.

## Authentication and rate limits

Requests use a tenant-scoped bearer token:

```http
Authorization: Bearer a8m_scim_<43 base64url characters>
```

Token values are generated from 32 random bytes with the fixed `a8m_scim_`
prefix. Alt8Mail stores only a SHA-256 hash plus a 16-character display prefix.
The raw value is returned exactly once when the token is created or rotated;
it cannot be retrieved later.

Authentication failures (missing, malformed, unknown, revoked or expired
token) return the same SCIM `401` response with `WWW-Authenticate: Bearer`, so
token state is not disclosed. A per-IP failure limiter allows 20 failed
attempts per 10 minutes; further attempts return `429` with `Retry-After`. A
successful authentication resets the counter for that IP. Each authenticated
request also updates the token's `last_used_at` asynchronously (timestamps are
approximate).

### Token lifecycle

SCIM token administration lives in the admin console API and requires
`roles.manage`:

| Method and path | Purpose |
| --- | --- |
| `GET /admin/api/scim/tokens` | List tokens: `id`, `name`, `prefix`, `createdAt`, `lastUsedAt`, `expiresAt`, `revokedAt`, `status` |
| `POST /admin/api/scim/tokens` | Create a token (`name` required, optional future RFC3339 `expiresAt`) |
| `POST /admin/api/scim/tokens/{id}/rotate` | Revoke the old token and return a new one-time token with the same name and expiry |
| `DELETE /admin/api/scim/tokens/{id}` | Revoke a token immediately |

List responses never contain a raw token or hash. Create and rotate responses
include a one-time `token` field. Expired or revoked tokens cannot be rotated
(`409`); create a new token instead. Rotation and revocation take effect
immediately: the old or revoked token receives `401` on its next SCIM request.

The permission is `roles.manage` rather than `users.create` because a token
holder can modify SCIM groups, and membership of a role-mapped group grants the
mapped tenant role. Token minting is therefore a role-management operation.
A User administrator without `roles.manage` cannot open the Provisioning view
and receives `403` from all four token routes.

## Supported resources

| Resource | Methods |
| --- | --- |
| `/scim/v2/ServiceProviderConfig` | `GET` |
| `/scim/v2/ResourceTypes` | `GET` |
| `/scim/v2/ResourceTypes/{User,Group}` | `GET` |
| `/scim/v2/Users` | `GET`, `POST` |
| `/scim/v2/Users/{id}` | `GET`, `PUT`, `PATCH`, `DELETE` |
| `/scim/v2/Groups` | `GET`, `POST` |
| `/scim/v2/Groups/{id}` | `GET`, `PUT`, `PATCH`, `DELETE` |

Unsupported methods on a supported path return a SCIM `405` with an `Allow`
header. Unknown paths under `/scim/v2/` return a SCIM `404` after
authentication. All resources are scoped to the token's tenant; ids belonging
to another tenant behave as unknown (`404`).

### ServiceProviderConfig

Advertised capabilities:

- `patch.supported: true`
- `bulk.supported: false`
- `filter.supported: true`, `maxResults: 500`
- `changePassword.supported: false`
- `sort.supported: false`
- `etag.supported: false`
- bearer-token authentication scheme

### User resource

```json
{
  "schemas": ["urn:ietf:params:scim:schemas:core:2.0:User"],
  "id": "8f1c2f9e-3d2a-4f8d-9c2a-1b2c3d4e5f60",
  "externalId": "object-id-from-idp",
  "userName": "avery.chen@example.com",
  "name": {
    "formatted": "Avery Chen",
    "givenName": "Avery",
    "familyName": "Chen"
  },
  "displayName": "Avery Chen",
  "emails": [{ "value": "avery.chen@example.com", "type": "work", "primary": true }],
  "active": true,
  "roles": [{ "value": "member" }],
  "meta": {
    "resourceType": "User",
    "created": "2026-01-04T10:00:00Z",
    "lastModified": "2026-01-04T10:00:00Z",
    "location": "https://mail.example.com/scim/v2/Users/8f1c2f9e-3d2a-4f8d-9c2a-1b2c3d4e5f60",
    "version": "W/\"...\""
  }
}
```

Notes:

- `userName` is required, lowercased and email-shaped. It is the account's
  primary email address and is immutable after creation.
- `externalId` is optional and unique per tenant when present. An omitted or
  empty value is stored as `NULL` and omitted from the resource.
- New users receive the `member` role, a generated mailbox, an unusable
  password hash and `force_password_change`; no password or role can be set
  through SCIM Users.
- `active=false` suspends the account. `active=true` reactivates it.
- The Users list is the tenant's full directory, including accounts created
  manually in the admin console, so deprovisioning can address any account the
  IdP knows.
- `meta.version` is a weak ETag derived from the last modification time. It is
  informational; conditional `If-Match` requests are not implemented.

User create/update rules:

- `POST /Users` requires `userName`; optional `externalId`, `name.formatted`,
  `name.givenName`, `name.familyName` and `active` are accepted. `userName` and
  `emails` are immutable after creation; a later write that changes them is
  rejected.
- An exact replay of `POST /Users` (same `externalId` and `userName`, including
  both empty) returns `200` with the existing resource. A duplicate `userName`
  or a conflicting `externalId` returns `409` with `scimType: "uniqueness"`.
- `PUT` and `PATCH` can change `name.*`, `externalId` and `active`. `userName`
  and `emails` must keep their current values if supplied; any change returns
  `409 mutability`.
- `PUT` treats omitted attributes as unchanged. For `externalId`, an omitted
  field keeps the value, `null` or `""` clears it.

### Group resource

```json
{
  "schemas": ["urn:ietf:params:scim:schemas:core:2.0:Group"],
  "id": "19d7a4cb-7f4e-4a1d-bbdf-2c3a4b5c6d7e",
  "externalId": "group-object-id-from-idp",
  "displayName": "Engineering",
  "members": [
    {
      "value": "8f1c2f9e-3d2a-4f8d-9c2a-1b2c3d4e5f60",
      "display": "Avery Chen",
      "$ref": "https://mail.example.com/scim/v2/Users/8f1c2f9e-3d2a-4f8d-9c2a-1b2c3d4e5f60"
    }
  ],
  "meta": {
    "resourceType": "Group",
    "created": "2026-01-04T10:05:00Z",
    "lastModified": "2026-01-04T10:05:00Z",
    "location": "https://mail.example.com/scim/v2/Groups/19d7a4cb-7f4e-4a1d-bbdf-2c3a4b5c6d7e",
    "version": "W/\"...\""
  }
}
```

Notes:

- `displayName` is required on create (trimmed, non-empty, no control
  characters, at most 100 characters).
- Groups created through SCIM are always `security` groups with status
  `active`. Alt8Mail synthesizes their internal group email from the tenant's
  primary verified or manual mail domain; without a usable domain creation is
  `400 invalidValue`.
- `externalId` is optional and unique per tenant when present; `""` clears it.
- Member `value` is an Alt8Mail user UUID (the Users resource `id`), not a
  `userName`. Every referenced user must belong to the same tenant. Adding a
  soft-closed account is `400 invalidValue`; removing a valid same-tenant user
  who is not a member is an idempotent no-op. Unknown, malformed or
  cross-tenant member ids are `400 invalidValue`.
- The local `scimRole` role mapping is deliberately not exposed in the SCIM
  resource. It is configured only in the Alt8Mail admin console.
- Groups have no SCIM representation of archive status: `status` is not a SCIM
  attribute, and group lists include archived groups.

Group create/update rules:

- `POST /Groups` requires `displayName`, accepts optional `externalId` and
  initial `members`, and returns `201` with a `Location` header.
- Duplicate `externalId` returns `409 uniqueness`. Group create is not
  replay-idempotent; an IdP retry of `POST` should first resolve the group with
  `GET /Groups?filter=externalId eq "..."` and use that resource.
- `PUT` treats omitted attributes as unchanged. Supplying `members` replaces
  the full membership; omitting `members` leaves membership unchanged.
- `PATCH` supports `add`, `replace` and `remove` (case-insensitive) for
  `displayName`, `externalId` and `members`, plus path-less `replace` objects
  containing those attributes. A single `{ "value": "<user id>" }` and an array
  are both accepted for membership values. Membership operations apply in
  order; duplicates are deduplicated.
- Unknown paths or non-replace path-less operations return `400 invalidPath`;
  malformed values, empty `displayName` and control characters return
  `400 invalidValue`.
- A write that changes nothing returns `200` without an update audit.
- `DELETE /Groups/{id}` removes the group and its membership, recomputes every
  former member's role, and returns `204`. A second delete returns `404`.

## Supported PATCH paths

PATCH bodies use RFC 7644 `Operations`. `add`, `replace` and `remove` are
matched case-insensitively. All operations are validated before anything is
written and then applied in one transaction.

Users (`PATCH /Users/{id}`):

- Paths: `active`, `externalId`, `userName`, `emails`, `name`,
  `name.formatted`, `name.givenName`, `name.familyName`.
- Path-less `replace` objects containing those same attributes are supported.
- `remove active` deactivates; `remove externalId` clears it; removing a name
  attribute restores the local-part/empty default.
- `userName` and `emails` are immutable: supplying their current value is
  accepted, changing them is `409 mutability`.
- `password`, `roles`, `displayName`, deep paths, unknown paths and non-replace
  path-less operations return `400 invalidPath`; wrong value types return
  `400 invalidValue`. In a path-less `replace` object only, read-only
  `schemas`/`id`/`meta` echoes are ignored; supplying them as explicit paths
  returns `400 invalidPath`.

Groups (`PATCH /Groups/{id}`):

- Paths: `displayName`, `externalId`, `members`.
- Path-less `replace` objects containing those same attributes are supported.
- `members` accepts a single `{ "value": "<user id>" }` object or an array;
  `add` of an existing member and `remove` of a valid same-tenant non-member
  are idempotent no-ops. Unknown, malformed or cross-tenant member ids return
  `400 invalidValue`. Membership operations apply in order.
- Removing `externalId` clears it. `displayName` is required, so
  `remove displayName` returns `400 invalidValue`.
- Unknown paths, unsupported path-less keys and non-replace path-less
  operations return `400 invalidPath`; malformed values and control characters
  return `400 invalidValue`.

Group membership changes recompute the roles of every affected user in the same
transaction. Referenced member ids must be users in the same tenant; unknown,
malformed or cross-tenant ids reject the whole request.

## Filtering and pagination

Only equality filters on an allowlisted attribute are supported. Attributes
and the `eq` operator are matched case-insensitively; the value must be a
quoted string. Any other operator or attribute returns
`400` with `scimType: "invalidFilter"`.

| Resource | Supported filters | Matching |
| --- | --- | --- |
| Users | `userName eq "..."` | Lowercased exact primary email |
| Users | `externalId eq "..."` | Exact, trimmed |
| Groups | `displayName eq "..."` | Case-insensitive exact display name |
| Groups | `externalId eq "..."` | Exact, trimmed |

Pagination:

- `startIndex` defaults to `1` and must be a positive integer.
- `count` defaults to `100`, may be `0`, and is capped at `500`.
- Invalid or negative values return `400 invalidValue`.
- Results are ordered by `email, id` for Users and `name, id` for Groups.
- Responses use a SCIM ListResponse:
  `schemas`, `totalResults`, `itemsPerPage`, `startIndex`, `Resources`.
- The Users list excludes soft-deleted (closed) accounts. The Groups list
  includes archived groups.

## Idempotency and deprovisioning

Idempotency summary:

| Operation | Behaviour |
| --- | --- |
| `POST /Users` exact replay | `200` with the existing user |
| `POST /Users` other conflicts | `409 uniqueness` |
| `POST /Groups` duplicate `externalId` | `409 uniqueness` (not replay-idempotent) |
| `PUT` / `PATCH` | Full replacement or targeted mutation; repeating the same write is safe |
| Group `add` existing member / `remove` valid same-tenant non-member | Idempotent no-op; unknown/malformed/cross-tenant ids are `400 invalidValue` |
| `DELETE /Users/{id}` | Idempotent `204`; each explicit call is audited |
| `DELETE /Groups/{id}` | First call `204`, second call `404` |
| Token create/rotate | Never idempotent; each call creates new token material |

Deprovision (`PUT`/`PATCH` with `active=false`, or `DELETE /Users/{id}`):

- `users.status` becomes `suspended` and the mailbox is marked inactive.
- `sessions`, `auth_challenges`, `admin_sessions`, `trusted_devices` and
  `admin_auth_challenges` are deleted in the same transaction.
- WebAuthn credentials are retained.
- Mail messages, attachments, calendars, calendar events and the user row are
  preserved. The account is never hard-deleted by SCIM.
- Reactivation (`active=true`) restores the active user and mailbox state.
  Revoked sessions stay revoked; the user must sign in again.
- An active `owner` cannot be deactivated or deleted through SCIM
  (`409 mutability`). Owners are managed manually only.

Group deletion removes membership rows and recomputes roles for every former
member; it never deletes users or mail.

## Local group-to-role mapping

SCIM group membership does not grant a tenant role directly. An administrator
maps a **security group** to one local role in the admin console:

**Groups → open a security group → Role mapping → None / Admin / Member /
Read-only**.

Rules:

- Mapped values are `admin`, `member` and `readonly` only. `owner` can never be
  granted, removed or demoted by SCIM or by a group mapping.
- When a user belongs to several active mapped groups, the highest mapping
  wins (`admin` > `member` > `readonly`).
- While a mapped group exists, its mapping is authoritative: a manual role
  change for a mapped user is re-applied on the next membership or mapping
  recompute. To demote a mapped user, remove the user from the mapped group or
  change/clear the group mapping (both require `groups.manage` and
  `roles.manage`).
- Removing a user from all mapped groups resets a mapping-managed account to
  `member`; an explicit manual role is preserved when no mapping applies.
- Archived mapped groups do not apply. Archiving/reactivating a mapped group,
  changing its mapping, changing membership of a mapped group or deleting it
  requires `roles.manage` in addition to `groups.manage`.
- Only security groups can hold a mapping. A mapped group must remain a
  security group; clear the mapping before changing its type. Legacy mappings
  on non-security groups are inert. The admin console shows such a stale
  mapping as read-only and offers an administrator with both permissions a
  **Clear mapping** action.
- Mapping and recompute changes are audited. SCIM-origin role changes are
  attributed to the token actor; admin-origin changes to the administrator.

Recompute triggers: SCIM group create with initial members, SCIM user
membership add/remove/replace, SCIM group delete, admin group membership
add/remove/delete, admin `scimRole` change, admin group type changes, and
admin archive/reactivate (as a safety measure). Role changes are applied in
the same transaction as the membership, mapping or group change.

## Identity provider setup

These are field-mapping guides, not certified connector configurations. Use the
IdP's "test connection" and start with a small pilot group before enabling
production sync. The SCIM endpoint is the authoritative base URL:
`ALT8MAIL_PUBLIC_URL` (trailing slash trimmed) with `/scim/v2` appended, for
example:

```text
https://<public-host>/scim/v2
https://<public-host>/<path-prefix>/scim/v2
```

If `ALT8MAIL_PUBLIC_URL` includes a path prefix, configure the reverse proxy to
strip it as described in [nginx reverse proxy](#nginx-reverse-proxy). The
admin console's Provisioning view shows the exact URL for this deployment.

The secret is a token created in **Governance → Provisioning**. Treat it as a
credential: it can create users and change mapped-group membership for the
whole tenant.

For users, map the IdP's stable identifier to `externalId` and its sign-in
address to `userName`. The address must be the user's Alt8Mail mailbox
address; `userName` cannot be renamed through SCIM afterwards.

### Microsoft Entra ID

1. Entra admin center → **Identity → Applications → Enterprise applications →
   New application → Create your own application** (non-gallery).
2. Open **Provisioning**, set **Provisioning Mode** to **Automatic**.
3. Set **Tenant URL** to `https://<public-host>/scim/v2` and **Secret Token**
   to the Alt8Mail SCIM token. Use **Test Connection** before saving.
4. Under **Mappings**, map users:
   - `userName` ← `userPrincipalName` (or `mail` if that is the mailbox address)
   - `externalId` ← `objectId`
   - `name.givenName` ← `givenName`
   - `name.familyName` ← `surname`
   - `active` ← `accountEnabled` / `IsSoftDeleted` expression
5. Map groups if you use SCIM groups:
   - `displayName` ← `displayName`
   - `externalId` ← `objectId`
   - `members` ← `members`
6. Start provisioning. Disabling or deleting a user in Entra sends
   `active=false` or `DELETE /Users/{id}`; the account is suspended and its
   sessions are revoked while mail is preserved.

### Okta

1. Create or open the application and enable **Provisioning →
   Configure API Integration**.
2. Set **SCIM 2.0 Base URL** to `https://<public-host>/scim/v2` and **API
   Token** to the Alt8Mail SCIM token; run **Test API Credentials**.
3. In **To App** mappings, map `userName` to the Okta username (the mailbox
   email), `givenName`, `familyName` and `externalId` to the Okta user id when
   the mapping is available.
4. Enable **Create Users**, **Update User Attributes** and **Deactivate
   Users**. Use **Push Groups** for SCIM groups.
5. Deactivating a user in Okta sends `active=false`; deleting may also send
   `DELETE`. Both suspend the Alt8Mail account and revoke active sessions.

### Google Workspace

1. In the Google Admin console, open the app's **User provisioning** settings
   and enable **Auto-provisioning**.
2. Enter the SCIM endpoint `https://<public-host>/scim/v2` and the Alt8Mail
   SCIM token as the authorization credential.
3. Map the primary email to `userName`, first/last name to
   `name.givenName`/`name.familyName`, the Google user id to `externalId`
   where offered, and the suspended state to `active`.
4. Suspending or deleting a user in Google Workspace sends `active=false` or
   `DELETE`; Alt8Mail preserves mail and revokes web/API sessions.

### Keycloak

Keycloak does not ship a first-party outbound SCIM client. Use a SCIM-capable
Keycloak extension or an identity-orchestration tool (for example midPoint)
that can send SCIM 2.0 requests. Configure it with:

- Base URL `https://<public-host>/scim/v2` and an Alt8Mail SCIM bearer token.
- `userName` = Keycloak email, `externalId` = Keycloak user UUID,
  `name.givenName`/`name.familyName` = Keycloak first/last name, `active` from
  the enabled flag.
- Groups: `displayName`, `externalId` and `members` (member `value` must be
  the Alt8Mail user id returned by the Users resource).

If Keycloak is also the SSO identity provider, keep the email/`userName`
attribute aligned with the Alt8Mail account to avoid duplicate accounts.

## Audit actions

SCIM writes are recorded in the tenant audit log with actor
`SCIM token <name>` (no interactive user id). Admin-console token and mapping
operations are recorded in the admin audit log with the administrator actor.
Raw tokens, token hashes, passwords and activation material are never written
to audit details.

| Action | Source | Meaning |
| --- | --- | --- |
| `scim.user_created` | SCIM | User provisioned |
| `scim.user_updated` | SCIM | Name/externalId metadata changed |
| `scim.user_deactivated` | SCIM | `active=false` / delete |
| `scim.user_reactivated` | SCIM | `active=true` |
| `scim.user_deleted` | SCIM | Explicit `DELETE` (idempotent; each call audited) |
| `scim.group_created` | SCIM | Group created |
| `scim.group_updated` | SCIM | Group attributes or membership changed |
| `scim.group_deleted` | SCIM | Group deleted |
| `scim.role_mapped` / `scim.role_unmapped` | SCIM | Membership/mapping recompute changed a user's role |
| `admin.scim_token_created` | Admin | Token created |
| `admin.scim_token_rotated` | Admin | Token rotated |
| `admin.scim_token_revoked` | Admin | Token revoked |
| `admin.group_role_mapping_updated` | Admin | Group `scimRole` changed |
| `admin.role_mapped` / `admin.role_unmapped` | Admin | Admin-origin recompute changed a user's role |

## Limitations

- No SCIM Bulk operations (`/Bulk`), sorting, ETag/`If-Match` concurrency
  control, or `/Me`.
- No password sync, password-change handling, JIT account creation on login,
  or enterprise user extension.
- SCIM Users cannot set roles. The only role effects are `active` state and the
  local group-to-role mapping; `owner` is never SCIM-managed.
- No dynamic schema discovery beyond the static ServiceProviderConfig and
  ResourceTypes documents.
- `meta.version` is a weak timestamp-derived value and is not enforced.
- Group archive status is not represented in SCIM; lists include archived
  groups.
- Search is limited to the equality filters above and pages are capped at 500
  resources.
- SCIM tokens are tenant-wide and long-lived unless an expiry is set; there is
  no per-token scope or automatic expiry policy.
