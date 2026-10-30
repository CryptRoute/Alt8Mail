# Customer and system administration

Alt8Mail now has a separate administration control plane for customer/tenant
management and system operations. It is a second Go binary, not a set of routes
inside the mail user API, so it can move to `admin.example.com` without changing
the mail application.

A third, root-level plane — **System Control** on `https://130.94.46.22:8808` —
handles host and mail-stack operations (Postfix, Dovecot, systemd, TLS, UFW,
backups, logs and server resources). It has its own schema, role, sessions and
audit. The customer admin console described here does not grant that host-level
access. See [SYSTEM_CONTROL.md](SYSTEM_CONTROL.md).

| Item | Value |
| --- | --- |
| Binary | `/opt/a8m/bin/a8m-admin-server` |
| Source | `backend/cmd/adminserver` + `backend/internal/httpapi/admin_*.go` |
| Staging URL | `https://130-94-46-22.sslip.io/admin/` |
| Listener | `127.0.0.1:8081` (`ALT8MAIL_ADMIN_LISTEN`) |
| Static UI | `/opt/a8m/admin` (`ALT8MAIL_ADMIN_DIR`) |
| Database migration | `backend/migrations/0004_customer_admin.sql` |
| Audit table | `admin_audit_log` |
| Admin sessions | `admin_sessions`, cookie `a8m_admin_sid` |
| Admin CSRF | `a8m_admin_csrf`, header `X-CSRF-Token` |

## Identity model

An administration identity is a normal Alt8Mail user with administrative
entitlement:

- **System administrator** (`users.is_system_admin = true`) — full access to every
  tenant, organization lifecycle, system administrators and the system views.
- **Owner / admin** (`users.role`) — full tenant administration for their own
  organization.
- **Delegated custom role** — tenant-scoped role assigned through
  `admin_role_assignments`; permissions are the union of active assignments.
- A regular member/readonly user has no admin console access unless a custom role
  is assigned.

There is no shared admin password and no demo admin login. Admin authentication
uses the existing Argon2id web password, plus the user's TOTP/recovery factor
when enrolled. The admin console has its own session and CSRF cookies; a mail
session cannot be used as an admin session.

The console supports:

- password sign-in;
- TOTP challenge verification with the existing encrypted secret;
- recovery-code fallback;
- TOTP enrollment and removal for the signed-in administrator;
- tenant-scoped session listing and “sign out other admin sessions”.

WebAuthn is accepted by the mail frontend and remains a user sign-in method, but
admin-console passkey authentication is not implemented yet. Admins with only a
passkey are instructed to add an authenticator. This is recorded as a gap below.

## Permissions

Permissions are tenant-scoped and checked server-side on every route. The UI
hides navigation it cannot use, but hiding is not the authorization boundary.

| Permission | Purpose |
| --- | --- |
| `tenant.read`, `tenant.manage` | Organization profile and branding |
| `users.read`, `users.create`, `users.update`, `users.delete` | User lifecycle |
| `users.reset_password`, `users.security`, `users.aliases` | Credential, MFA and alias control |
| `groups.read`, `groups.manage` | Groups and membership |
| `orgunits.read`, `orgunits.manage` | Organization unit tree |
| `roles.read`, `roles.manage` | Custom roles and role assignments |
| `policies.read`, `policies.manage` | Security, mail, sharing and directory policy |
| `domains.read`, `domains.manage` | Domains, primary domain and DNS verification |
| `audit.read` | Admin audit and CSV export |
| `sessions.manage` | Revoke user/admin sessions |
| `system.tenants.manage` | Create, suspend and configure any tenant |
| `system.admins.manage` | Grant/revoke system administrators |
| `system.health` | System health views |

Seeded built-in roles for every tenant: **User administrator**, **Security
administrator**, **Helpdesk** and **Auditor**. Owners and admins are implicit
full-tenant roles. Custom roles can be created, edited and revoked once they have
no assignments.

Safety rules:

- the last active owner cannot be suspended, demoted, closed or deleted;
- a delegated administrator cannot create/promote an owner/admin or edit,
  reset, close or revoke a privileged account unless they also hold
  `roles.manage`;
- an administrator cannot suspend or demote their own account;
- system administrators cannot revoke their own system flag;
- admin and user deletions are soft closes; data is retained and restorable.

## Tenant data model

Migration `0004_customer_admin.sql` adds:

- `org_units` with a tenant-scoped hierarchy and materialized path;
- `admin_roles` and `admin_role_assignments`;
- `groups` and `group_members`;
- `admin_sessions` and `admin_auth_challenges`;
- `admin_audit_log`;
- tenant plan/region/billing/suspension fields;
- user profile, manager, org unit, force-password-change and soft-delete fields;
- domain verification state, DNS checks, DMARC policy and catch-all fields.

Policy lives in `tenants.settings` under `security`, `mail`, `sharing`,
`directory` and `branding`. Every save is validated server-side and written to
PostgreSQL.

## Admin API

All paths are under the admin server root. In staging they are reachable as
`/admin/api/...`; on the admin subdomain they are `/api/...`. Both prefixes are
served by the same mux.

| Method | Path | Permission |
| --- | --- | --- |
| POST/GET/DELETE | `/session`, `/session/2fa` | public + challenge |
| GET | `/bootstrap` | authenticated admin |
| POST | `/context` | system admin |
| GET | `/health` | public service health |
| GET/POST/PATCH | `/tenants`, `/tenants/:id` | system.tenants.manage |
| GET/POST/GET/PATCH/DELETE | `/users`, `/users/:id` | users.* |
| GET | `/users/:id` | users.read |
| POST | `/users/:id/reset-password`, `/reset-mfa`, `/revoke-sessions` | corresponding permission |
| POST/DELETE | `/users/:id/aliases`, `/users/:id/aliases/:aliasId` | users.aliases |
| POST | `/users/bulk` | users.update |
| GET | `/users/:id/sessions` | sessions.manage |
| GET/POST/GET/PATCH/DELETE | `/groups`, `/groups/:id` | groups.* |
| POST/DELETE | `/groups/:id/members`, `/groups/:id/members/:userId` | groups.manage |
| GET/POST/PATCH/DELETE | `/org-units`, `/org-units/:id` | orgunits.* |
| GET/POST/PATCH/DELETE | `/roles`, `/roles/:id` | roles.* |
| POST/DELETE | `/roles/:id/assign`, `/roles/:id/assign/:userId` | roles.manage |
| GET/PATCH | `/policies` | policies.read/manage |
| GET/PATCH | `/ai` | policies.read/manage; tenant provider settings (key never returned) |
| POST | `/ai/test` | policies.manage; save-and-test provider connectivity |
| POST | `/maintenance/retention` | policies.manage; audited manual retention pass |
| GET | `/quarantine` | users.security; tenant-wide Junk/quarantine queue |
| POST | `/quarantine/:userId/:messageId/release` | users.security; moves Junk mail to the recipient Inbox and audits the release |
| GET/POST/PATCH/DELETE | `/domains`, `/domains/:id` | domains.* |
| POST | `/domains/:id/verify` | domains.manage |
| GET | `/audit`, `/audit.csv` | audit.read |
| GET | `/security`, `/security/sessions` | authenticated admin |
| POST | `/security/totp/begin`, `/finish`, `/disable` | authenticated admin |
| POST | `/security/sessions/revoke-others` | authenticated admin |
| GET/PATCH | `/settings` | tenant.read/manage |
| GET/POST/DELETE | `/system/admins`, `/system/admins/:id` | system.admins.manage |

Every mutating request requires the admin CSRF header. POST bodies are limited
to 12 MB (user attachment flows are in the mail API, not the admin API).

## User lifecycle

Creating a user through the console:

1. verifies that the email domain is verified for the tenant;
2. creates `users`, a primary `user_aliases` row and a `mailboxes` row;
3. generates a one-time temporary web password (Argon2id) and mailbox password;
4. produces a `{SHA512-CRYPT}` mailbox hash through the local `doveadm pw` tool
   so the deployed Dovecot SQL passdb can authenticate it;
5. inserts the mailbox with `provision_status=pending`; the root
   `a8m-mailprovision` timer creates the Maildir and marks it `ready`, and
   the admin user detail shows the path/attempts/error;
6. builds and inserts the user's empty workspace state with `stateinit.Build`;
7. writes an admin audit event, returning each credential only once.

Aliases can be added/removed after creation. Password and MFA resets revoke
sessions and trusted devices. Closing an account sets `deleted_at`, suspends the
user, deactivates the mailbox row and invalidates sessions; restore reverses the
soft close.

## Groups and org units

Groups are managed as distribution/security/shared groups with a real email
address, status, description and membership in PostgreSQL. The Postfix virtual
alias SQL map unions normal aliases with active group membership and returns
each active member as a recipient, so a message to a group address is delivered
to every member by LMTP. Disabled users, inactive groups, suspended tenants and
unverified domains are excluded by the map.

Org units form a tenant-scoped tree with a six-level limit, unique sibling
names, cycle prevention, path maintenance and user counts. Deleting a unit is
blocked while it has children or users.

## Domains and DNS

Domains are created in `mail_domains` with a verification token and pending
status. A missing token is generated on verification, and migration 0006
backfills legacy domains. `POST /domains/:id/verify` performs real DNS lookups:

- `_a8m-verification.<domain>` must contain `a8m-verify=<token>`;
- at least one MX must point to `ALT8MAIL_MAIL_HOSTNAME`;
- an SPF TXT prefix `v=spf1` must exist;
- the TXT at `<selector>._domainkey.<domain>` must be a `v=DKIM1` record whose
  `p=` exactly matches the active DKIM key in `mail_domains.dkim_record`—any
  unrelated DKIM record no longer passes;
- DMARC TXT at `_dmarc.<domain>` must carry `p=none|quarantine|reject`.

The response distinguishes ownership (`ok`), full mail readiness (`ready`),
individual checks and human-readable `details`. A domain that was already
verified is not downgraded when a later check fails; the error is recorded and
`mailReady` becomes false. The seeded staging domain is marked verified because
the staging host has no public DNS for `a8m.test`, so its checks correctly
remain incomplete.

DKIM keys are generated by the root `a8m-dkimprovision` timer (see
`OPERATIONS.md`). The admin screen reads the public record from PostgreSQL and
shows the real `p=` value; if the key is still being generated it shows a
provisioning state instead of a fake key. Changing a selector clears the old
public record and triggers generation of a new key.

### MTA-STS

Each domain has `mta_sts_mode` (`none`, `testing`, `enforce`) and
`mta_sts_max_age`. Enabling a mode adds two entries to the DNS table:

- `TXT _mta-sts.<domain>` with `v=STSv1; id=<policy-id>`;
- a `POLICY` entry showing the exact document served at
  `https://mta-sts.<domain>/.well-known/mta-sts.txt`.

The policy id is a hash of the policy body, so changing mode, max-age or the
platform MX host changes the required TXT value. Verification first checks the
TXT id, then fetches the HTTPS policy and compares version, mode, max_age and
MX. The fetch is deliberately restricted to public IPs and does not follow
redirects. A domain cannot become fully MTA-STS verified until the customer
publishes the TXT record and serves the policy with a trusted certificate for
`mta-sts.<domain>`; see `OPERATIONS.md` §5.12 for the nginx pattern.

## Policies and enforcement

Policy defaults are defined in `backend/internal/httpapi/admin_policies.go`.
Current enforcement:

| Policy | Enforcement |
| --- | --- |
| Password length/character requirements | Admin-created users, admin resets and the user's own password-change flow |
| Password history | Keeps the configured number of prior hashes; self-service and admin resets reject reuse |
| Password expiry | Policy changes seed expiry for existing accounts; an expired login is restricted to `/api/bootstrap`, `/api/session` and `/api/password/change` until rotation |
| Mailbox password sync | A self-service web password change also rotates the Dovecot `mail_password_hash`, so IMAP/POP/SMTP immediately use the new password |
| MFA: off / admins / all | Admin login and mail user login; un-enrolled users are routed to the first-time enrollment screen |
| Allowed admin/network IP ranges | Admin login and mail user login |
| Admin idle timeout | Admin session validation |
| Admin session max lifetime | Applied at admin login from the active tenant policy |
| Session max count | Enforced when a new mail-web session is created (oldest sessions are retired) |
| Tenant suspension | Blocks mail user login, invalidates user sessions, and is enforced in Postfix/Dovecot SQL maps |
| SMTP access | Blocks outbound submission through the Go send API and authenticated Postfix submission through the Dovecot SQL passdb |
| Allowed external recipients | Blocks disallowed external recipients |
| Blocked attachment extensions / max attachment size | Blocks outbound messages that violate the policy |
| Domain verification | Real ownership/MX/SPF/DKIM/DMARC lookups, exact DKIM `p=` match and verified-domain requirement for new users; when MTA-STS is enabled, also validates the `_mta-sts` TXT id and the HTTPS policy body (mode, max-age, MX) |
| IMAP access | Dovecot rejects IMAP authentication; per-user sync, IMAP actions, drafts and Sieve pushes are skipped while disabled |
| POP access | Dovecot rejects POP3 authentication on 995 |
| External / internal auto-forwarding | Sieve redirects are validated at save time and existing generated scripts are re-pushed after a policy change; blocked rules stay stored but are omitted from the live script |
| Retention | Daily worker and an audited "Run retention" action remove messages older than the policy from synchronized folders using UID EXPUNGE; Drafts, Junk and Trash are excluded |
| Restrict Junk release to administrators | Off by default, so users can report not spam or move their own Junk to Inbox. When on, non-admin users cannot move a message out of Junk; a delegate with `users.security` must release any tenant mailbox message from the admin Quarantine view, which moves it to INBOX and audits the action |
| External sharing | Link creation is refused when external sharing is off, link access is disabled, or a domain allowlist is configured (a bearer link cannot prove a viewer domain); a view-only default caps edit links. Public guest pages at `/files/s/<token>` support expiry, revocation and optional file replacement |
| External sender tag | When off, the reading pane hides the external banner/chip, removes “external” wording from the trust line/details and disables the user warning toggle |
| External directory | When off, personal contacts outside the tenant's mail domains are omitted from directory/search for members; admins retain their override |
| Profile editing | When off, members get read-only profile fields and `PATCH /api/profile` returns 403; when on, name/title/department self-service is allowed |
| Auto-add external | When on, external recipients of sent mail are inserted as personal contacts without duplicating existing addresses |
| Group delivery | Postfix alias map expands active groups to their active tenant members |

Domain-scoped sharing (`allowedDomains`) blocks public bearer-link creation until
named-recipient guest identity is built. `guestInvites` has no recipient-facing
guest-account surface yet. The matrix is kept explicit rather than claiming every
field is wired into every subsystem.

## AI assistant settings

Admins with `policies.read` see **AI assistant** in the Governance navigation;
`policies.manage` is required to save or test it. The settings are stored in
`tenant_ai_settings`, not in the general tenant policy JSON, so the API key is
never part of `GET /policies` or the member bootstrap.

- Toggle the assistant on/off for the tenant.
- Provider: `openai` (any OpenAI-compatible chat-completions endpoint including
  OpenAI, DeepSeek, Azure gateways and local servers) or `anthropic`.
- Endpoint, model, optional API key, max tokens (64–8192), temperature (0–2),
  timeout (5–120 s) and an optional organization system prompt.
- The API key is AES-GCM sealed with key material derived from the server
  session secret; only `hasKey` is returned. Leaving the key blank keeps the
  current key. Rotating the session secret requires re-entering the key.
- **Save & test** saves the configuration and sends a tiny connectivity prompt;
  the reply and latency are shown. Provider error text is surfaced without the
  key.
- The member frontend receives only `enabled`, provider, model and the fixed
  36-language/8-style catalogs. When disabled, compose and reader AI controls
  are hidden and the APIs return `AI_DISABLED`.

The endpoint is an outbound SSRF surface by design (self-hosted AI gateways are a
supported use case), so restrict `policies.manage` to trusted administrators and
prefer HTTPS. The Go client disables redirects, caps the response at 4 MiB and
uses the configured timeout.

## Admin UI

The UI is dependency-free HTML/CSS/ES modules under `admin/`:

- split-screen sign-in and TOTP/recovery step;
- overview with user, domain, storage, MFA and audit posture;
- quarantine queue with tenant-wide Junk items and an audited release-to-Inbox action;
- users table with search/filter/bulk actions and a detail drawer (profile,
  access, security, aliases, sessions);
- user create/edit, one-time credential display, reset password/MFA, suspend,
  close and restore;
- groups, hierarchy tree, domains and DNS record drawer;
- custom role permission matrix and assignment flow;
- policy editor for security, mail, sharing, directory and branding;
- AI assistant provider configuration with masked key, model parameters and a
  save-and-test flow;
- audit log and CSV export;
- organization profile, security modal and system administrator management;
- tenant switcher for system administrators;
- light/dark theme, mobile drawer and responsive tables.

The HTML is served by the admin Go binary, not nginx static roots. Relative asset
and API paths make the same UI work at `/admin/` and at an admin subdomain root.

## Production subdomain

Create `admin.example.com`, issue a certificate, and point nginx at the admin
listener. A minimal server block:

```nginx
server {
    listen 443 ssl http2;
    server_name admin.example.com;

    ssl_certificate     /etc/letsencrypt/live/admin.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/admin.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8081;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Set `ALT8MAIL_ADMIN_URL=https://admin.example.com` and keep
`ALT8MAIL_COOKIE_SECURE=true`. The admin cookies are host-only in production,
so mail and admin sessions remain separate origins as well as separate cookies.
Restrict the subdomain with the tenant's allowed-network policy, a VPN/zero-trust
proxy, or a network ACL until the customer backend has its own SSO.

The staging server currently exposes the same UI at `/admin/` through nginx so
it can be evaluated before DNS is ready. Do not expose `/admin/` publicly on a
production mail host without the admin policy and network controls.

## Deployment

```bash
cd /opt/a8m/app/backend
go test ./...
for n in 0004_customer_admin 0005_mail_provisioning 0006_dkim_keys 0007_workspace 0008_mta_sts 0009_dav 0010_app_passwords 0011_mail_tables 0012_outbox 0013_undo_table 0014_compose_identity_ai; do
  install -m 644 "migrations/${n}.sql" "/tmp/${n}.sql"
  runuser -u postgres -- psql -d a8m -v ON_ERROR_STOP=1 -f "/tmp/${n}.sql"
  rm -f "/tmp/${n}.sql"
done
go build -trimpath -ldflags "-s -w -X main.version=0.8.0-staging" \
  -o /opt/a8m/bin/a8m-admin-server ./cmd/adminserver
rsync -a --delete /path/to/a8m/admin/ /opt/a8m/admin/
chown -R a8m:a8m /opt/a8m/admin

# systemd: /etc/systemd/system/a8m-admin.service
systemctl daemon-reload
systemctl enable --now a8m-admin
```

Grant the first system administrator from the host:

```bash
set -a; . /opt/a8m/secrets/app.env; set +a
/opt/a8m/bin/a8m-admin -grant-system-admin -email avery.chen@a8m.test
/opt/a8m/bin/a8m-admin -list-admins
```

The `a8m-admin` CLI also supports `-revoke-system-admin` and
`-list-admins`.

## Verification

```bash
ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password \
  python3 tools/admin-api-smoke.py

ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password \
  python3 tools/admin-auth-smoke.py

ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password \
  python3 tools/admin-policy-smoke.py

ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password \
  python3 tools/admin-tenant-smoke.py

ALT8MAIL_ADMIN_PASSWORD="$(cat ./staging_avery_password)" \
  node tools/admin-ui-smoke.mjs

ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password \
  python3 tools/dkim-smoke.py
```

Evidence is stored under `artifacts/`:

- `admin-api-smoke.txt` — CRUD, policies, domains, audit, tenant context,
  system administrator grant/revoke;
- `admin-auth-smoke.txt` — admin TOTP enrollment/challenge/removal;
- `admin-policy-smoke.txt` — tenant MFA policy reaches mail login;
- `mail-policy-smoke.txt` — forced password change/history, IMAP/POP/SMTP gates,
  Sieve forwarding policy, sharing limits, quarantine release, retention and
  explicit Maildir provisioning;
- `admin-tenant-smoke.txt` — create, suspend, block and reactivate a tenant;
- `admin-ui-smoke.txt` and `admin-overview.png`, `admin-users.png`,
  `admin-policies.png`, `admin-roles.png`, `admin-tenants.png`,
  `admin-mobile.png` — browser flows including the quarantine view, the real
  DKIM DNS record and responsive layout.
- `dkim-smoke.txt` — actual DKIM record in the admin API, exact `p=` presence,
  provisioning state and readiness/ownership distinction.
- `dkim-signing-smoke.txt` — real Rspamd signatures for the existing domain and
  a newly provisioned domain/selector, plus selector rotation and cleanup.

## Known limitations / next adapters

1. Admin passkeys/WebAuthn are not enabled in the admin login; TOTP/recovery is.
2. No tenant/org-wide directory of system administrators beyond the system
   admin card; broader system jobs/health dashboards are next.
3. Directory external-contact visibility, profile editing and external-tag
   presentation are applied to the webmail surfaces; inter-tenant directory
   sharing and inbound auto-add are not built.
4. Public guest share pages and edit-link replacement are live; named-recipient
   guest identity and domain-proven access are not built, so public links remain
   bearer links governed by external-sharing and link-default policies.
5. The customer backend/admin UI is a single console with system and tenant
   scopes; billing/subscription workflows and external IdP/SSO are not built.
6. Admin UI assets are served from `ALT8MAIL_ADMIN_DIR`; there is no embedded
   bundle, so deploy the `admin/` directory with the binary.
7. Retention permanently deletes mail and is therefore opt-in (`0` disables it);
   take a backup before the first production retention pass.
8. The AI assistant is live-tested on staging with the tenant's DeepSeek
   OpenAI-compatible endpoint (summary and Reply with AI). Before enabling it
   for a customer, use a spend-capped key, agree the data-processing terms, test
   the exact endpoint/model read and reply flows and review the organization
   system prompt.
