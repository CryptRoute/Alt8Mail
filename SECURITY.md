# Security policy

## Supported version

This repository is at webmail staging release **0.9.0-staging** with the
separate **System Control 0.10.0-control** plane. Security fixes are applied to
the current staging line; there is no long-term-support branch yet.

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Send a private report
to the deployment maintainer through the private security contact used to
receive this build, including:

- affected endpoint or component;
- reproduction steps and the minimum data needed to confirm the issue;
- impact (confidentiality, integrity, availability, tenant boundary);
- whether the issue is already public.

Do not include real passwords, private keys, customer mail, or third-party
personal data in a report. Use synthetic accounts and redact secrets. We will
acknowledge the report, reproduce it, and coordinate disclosure and a fix.

## Security model in this build

- Argon2id web passwords; Dovecot-compatible mailbox hashes in a separate
  column; opaque PostgreSQL sessions; CSRF validation on every mutating JSON
  request; rate limits on password and DAV authentication.
- Real TOTP/WebAuthn; encrypted TOTP secrets; hashed single-use recovery codes;
  revocable sessions and trusted devices.
- CalDAV/CardDAV uses HTTP Basic over TLS and requires a revocable app password
  for accounts with MFA. App-password tokens are stored as SHA-256 hashes and
  shown once.
- Tenant policy gates IMAP/POP/SMTP, forwarding, link sharing, directory
  presentation, profile editing, quarantine release and retention.
- Mailbox workers use Dovecot master auth; per-user locks isolate work. S3 and
  OAuth credentials stay server-side; OAuth tokens and tenant AI API keys are
  AES-GCM sealed.
- Rich compose HTML is sanitized server-side with a bluemonday allowlist.
  Scripts, forms, iframes, embedded objects, event handlers, javascript: URLs
  and unsupported inline styles are removed before storage or MIME delivery.
- External sending identities use 32-byte random tokens stored as SHA-256 hashes
  with a 48-hour expiry. The public verification endpoint requires a POST
  confirmation so link scanners do not consume the token. Internal identity
  addresses must already belong to the user as an organization alias.
- App passwords can be permanent or 1–730-day expiring; revocation is soft for
  history. DAV authentication checks expiry and revocation on every request, so
  an expired/revoked token stops working immediately.
- The tenant AI endpoint is admin-configurable (thus an SSRF surface limited to
  trusted `policies.manage` holders). Calls disable redirects, use a bounded
  timeout, cap responses at 4 MiB and never return the key. AI output is
  untrusted text rendered as text, not HTML.
- Notification read state is per-browser localStorage; notification content is
  derived from real mailbox/calendar/audit data and contains no demo rows.
- Web uploads and guest-share replacements are scanned with ClamAV INSTREAM
  when configured; scanner failure fails closed. Mail is scanned by
  Rspamd/ClamAV before LMTP delivery.
- MTA-STS verification fetches only public IPs, requires HTTPS, does not follow
  redirects and ignores environment proxies.
- Guest share tokens are hashed server-side; expiry, revocation and edit access
  are enforced; public pages are no-store/no-index with a restrictive CSP.

### System Control plane (0.10.0-control)

- System Control is a separate Go service with its own `control` schema,
  `alt8mail_control` database role, environment file, systemd unit, session
  cookie, CSRF secret and encryption key. It does not accept webmail or
  customer-admin sessions.
- Panel passwords use Argon2id; sessions are opaque server-side records with
  TTL and explicit revocation; session cookies are HttpOnly and SameSite=Strict.
  Mutating requests require a signed CSRF token in addition to the session.
- Five failed passwords in 15 minutes lock the account for 15 minutes. TOTP
  challenges allow five attempts and expire. TOTP secrets are AES-GCM sealed;
  panel account changes and lockouts generate audit events and notifications.
- Server-side RBAC covers four roles and a 35-permission group matrix. At least
  one active superadmin must remain. Permission checks are applied to every
  route, not only hidden in the UI.
- Panel API keys use the `a8m_` prefix, are shown once, stored only as SHA-256
  hashes, scoped `read`/`write`/`admin`, optionally expiring and immediately
  revocable. Webhooks are signed with HMAC-SHA256 over the exact body and are
  sent without following redirects.
- The Postfix policy daemon binds `127.0.0.1:10040` and defaults to `DUNNO`, so
  a panel outage does not reject mail. Alias lookups are read-only SQL functions
  that merge, rather than overwrite, customer-admin aliases.
- Postfix/Dovecot/nginx/systemd/UFW changes run only after authenticated,
  CSRF-protected, audited requests with explicit server-side permission checks.
  Managed-file writes are backed up and validated (`postfix check`, `doveconf`)
  with rollback on failure.
- `/healthz` is intentionally cheap and unauthenticated; `/healthz/stacks` is
  authenticated; `/metrics` is unauthenticated and exposes counts/gauges only,
  not message content or secrets. Restrict port 8808 at the network edge.
- The bootstrap panel certificate is self-signed. Replace
  `/etc/ssl/alt8mail/control.{crt,key}` with a CA-issued pair before exposing the
  panel broadly. The panel runs as root by necessity and must be treated as
  root-equivalent; use named accounts, TOTP and an IP allowlist/VPN.
- **Import certificate** accepts a PEM certificate chain and matching private
  key (RSA, ECDSA or PKCS#8), validates the key pair and expiry, normalizes the
  key, and stores it root-only under `/etc/ssl/alt8mail/imported`. Private key
  material is never returned by the API or written to audit metadata.
- **Assign certificate** changes the live nginx/Postfix/Dovecot paths only
  after an authenticated, audited `tls.manage` request. nginx runs `nginx -t`,
  Postfix runs `postfix check` and Dovecot runs `doveconf -n`; reload failures
  roll the changed files and service paths back. The Postfix key is made
  readable by the unprivileged `postfix` user with ACLs (and directory search
  permission), never by making it world-readable. Certbot deploy hooks reapply
  the ACL after renewal.
- **Software updates** are restricted to packages currently returned by
  `apt list --upgradable`; package names are validated, commands are executed
  without a shell, and one update job runs at a time. The job itself runs in a
  transient systemd unit outside the control service's read-only filesystem
  sandbox so `dpkg` can install files, while the control process remains
  sandboxed. Output is streamed only to authenticated `server.manage`
  operators and the start/finish is audited.
- **Terminal** is a root-equivalent SSH shell. It requires `server.manage`,
  connects only to loopback on the port detected from the live `sshd`
  configuration (never an assumed port 22), uses a one-time two-minute ticket,
  enforces same-origin WebSocket upgrades, limits concurrent sessions, and is
  audited without recording passwords or private keys. SSH authentication
  failures are returned as 422 and do not invalidate the panel session. Password
  or SSH-key authentication is supported. A loopback-only `sshd` Match block
  permits password authentication for this terminal; external SSH remains
  key-only and advertises only `publickey`. Host-key verification is
  intentionally skipped only for `127.0.0.1`; do not change the target away
  from loopback.
- The dashboard reputation widget distinguishes an actual DNSBL listing from
  resolver-policy replies such as Spamhaus `127.255.255.254`. Unverified zones
  are shown as **Unverified**, not green. Configure a resolver permitted by the
  zone operator before treating Spamhaus results as actionable.

## Deployment checklist

Before exposing a production host:

1. Use a customer-owned domain with trusted TLS certificates, including
   `mta-sts.<domain>` if MTA-STS is enabled.
2. Publish and verify ownership/MX/SPF/DKIM/DMARC and the `_mta-sts` TXT id.
3. Set all secrets outside the repository (`/opt/meridian/secrets` or a secret
   manager), enforce 0600 root-only files, and rotate any staging credentials.
4. Keep PostgreSQL, Redis, Dovecot, Postfix SASL, the Go APIs and the control
   policy daemon on loopback; expose only 25/587/993/995/4190 and 80/443 as
   required. Restrict System Control on 8808 to an operator VPN or source-IP
   allowlist; never publish it broadly.
5. Keep the S3 bucket private; use a dedicated bucket policy and short-lived
   credentials. OAuth token encryption derives from `MERIDIAN_SESSION_SECRET`;
   rotating that secret requires connector reauthorization.
6. Keep `MERIDIAN_CLAMAV_ADDR` configured in production and verify an EICAR
   upload is rejected.
7. Disable demo mode/aliases, first-time enrollment remains opt-in per user,
   and administrator accounts must use MFA. Rotate the bootstrap System Control
   password, enable TOTP on `sysadmin@alt8mail.local`, create named operators and
   remove `/root/alt8mail-control-initial-password`.
8. If AI is enabled, use a dedicated spend-capped key, an HTTPS endpoint, a
   documented data-processing agreement and a reviewed organization prompt.
   Restrict `policies.manage`; the endpoint is an intentional outbound SSRF
   surface for self-hosted gateways.
9. Verify external sending-identity domains authorize Meridian in SPF/DKIM
   before advertising the identity feature; verification proves control, not
   deliverability.
10. Replace the System Control self-signed bootstrap certificate with a
    CA-issued certificate for the panel hostname, and verify `/metrics` is not
    reachable from untrusted networks.
11. The 1,000-user read/session soak and external operator assessment have
    passed (see the 2026-10-29 report). Before accepting production traffic, run
    a bulk-mailbox/large-attachment soak, an independent third-party penetration
    test, backup/PITR drill and off-site secret backup, including the System
    Control plane and its root-equivalent actions.

The external assessment, remediated findings, open staging risks and the
1,000-user soak are recorded in
[`docs/PENTEST_REPORT_2026-10-29.md`](docs/PENTEST_REPORT_2026-10-29.md).
Known limitations and the production gate are tracked in
[`docs/SYSTEM_CONTROL.md`](docs/SYSTEM_CONTROL.md),
[`docs/PHASE_REVIEW.md`](docs/PHASE_REVIEW.md) and
[`docs/OPERATIONS.md`](docs/OPERATIONS.md).
