# SAML 2.0 Single Sign-On

Alt8Mail supports opt-in SAML 2.0 service-provider (SP) initiated single
sign-on for existing accounts. When SAML is not configured, the login page never
shows a SAML button and OIDC, password, passkey, TOTP and recovery-code sign-in
are unchanged.

SAML is a paid entitlement. It is enabled only when every required environment
variable below is set **and** the verified license includes the `saml` feature
(see [LICENSING.md](LICENSING.md)). An unlicensed, invalid, expired or
non-`saml` license behaves like an incomplete configuration: the login page
hides the button and the start and ACS endpoints redirect with the generic
`saml_error`. A license in its grace period keeps SAML enabled.

This tranche is configured through environment variables and supports
SP-initiated logins only. Per-tenant Control Plane SAML configuration,
IdP-initiated SSO and just-in-time provisioning are out of scope.

## Endpoints

All paths are relative to `ALT8MAIL_PUBLIC_URL`:

| Endpoint | URL | Purpose |
| --- | --- | --- |
| SP metadata | `https://<mail-host>/api/auth/saml/metadata` | Metadata to register with the IdP. Served only while SAML is licensed and configured. |
| ACS | `https://<mail-host>/api/auth/saml/acs` | Assertion Consumer Service. HTTP-POST binding only. |
| Start | `https://<mail-host>/api/auth/saml/start` | Starts the SP-initiated login and redirects to the IdP. Not sent to the IdP. |
| Config probe | `https://<mail-host>/api/auth/saml/config` | Returns `{"enabled": bool, "displayName": string}` for the login page. |

The IdP metadata URL is the public URL of the IdP metadata document. It is
configured with `ALT8MAIL_SAML_IDP_METADATA_FILE` or
`ALT8MAIL_SAML_IDP_METADATA_URL`; Alt8Mail reads it as a client, it is not an
Alt8Mail endpoint.

## Required environment variables

| Variable | Required | Purpose |
| --- | --- | --- |
| `ALT8MAIL_PUBLIC_URL` | Yes | Public origin of this deployment. It must be an absolute `https://` URL; plain `http://` is accepted only for loopback development/test hosts. The ACS and SP metadata URLs are derived from it. |
| `ALT8MAIL_SAML_SP_CERT_FILE` | Yes | PEM-encoded X.509 certificate that identifies the SP to the IdP. |
| `ALT8MAIL_SAML_SP_KEY_FILE` | Yes | PEM-encoded RSA private key matching the certificate. |
| `ALT8MAIL_SAML_IDP_METADATA_FILE` | One of these | Path to a local IdP metadata XML file. It takes precedence over the URL. |
| `ALT8MAIL_SAML_IDP_METADATA_URL` | One of these | HTTPS URL of the IdP metadata document. HTTP is accepted only for loopback test endpoints. |
| `ALT8MAIL_SAML_SP_ENTITY_ID` | No | Overrides the SP entity ID. Defaults to the SP metadata URL. |
| `ALT8MAIL_SAML_DISPLAY_NAME` | No | Label shown on the SAML login button. When empty the translated "Continue with SAML SSO" label is used. |
| `ALT8MAIL_SAML_EMAIL_DOMAINS` | No | Comma-separated exact email-domain allowlist. Empty means no domain restriction; see the warning below. |
| `ALT8MAIL_SAML_EMAIL_ATTRIBUTE` | No | Exact SAML `Attribute` `Name` that carries the user's email. Empty means the assertion `NameID` must be an email address. |
| `ALT8MAIL_SAML_MFA_AUTHN_CONTEXT_CLASSES` | No | Comma-separated additional **exact** `AuthnContextClassRef` values that count as MFA evidence. |

All values are trimmed. Email-domain entries are lowercased, accept an optional
leading `@`, and duplicates and empty entries are dropped. MFA context class
entries keep their case, and only surrounding whitespace, empty entries and
exact duplicates are removed.

## SP certificate and key requirements

- The private key must be RSA with at least 2048 bits. EC keys are rejected.
- The certificate must be currently valid (`NotBefore` to `NotAfter`) and, when
  its `KeyUsage` extension is present, include `digitalSignature`.
- The certificate public key must match the private key.
- PEM encoding is required for both files; the key may be PKCS#1 or PKCS#8.
- The same key signs AuthnRequests and the SP metadata signing descriptor. The
  SP advertises `AuthnRequestsSigned="true"` and `WantAssertionsSigned="true"`;
  unsigned AuthnRequests and unsigned assertions are not accepted.

The same key signs AuthnRequests and decrypts assertions the IdP encrypts to
the SP certificate. `samlsp` may also advertise the certificate as an
encryption key in the SP metadata; an IdP that encrypts assertions to it works
because the matching private key is loaded.

## Registering with the IdP

1. Configure the IdP with the SP metadata URL
   (`https://<mail-host>/api/auth/saml/metadata`), or upload the document it
   serves.
2. Set the IdP's ACS URL to `https://<mail-host>/api/auth/saml/acs` with the
   HTTP-POST binding.
3. Ensure the IdP signs assertions (and, preferably, responses) with a
   certificate published in its metadata.
4. Ensure users' email addresses are available as the NameID or in the
   configured email attribute.

IdP-initiated SSO is not supported: `AllowIDPInitiated` is false, so an
unsolicited response is rejected.

## Attribute mapping and email extraction

The signed assertion is the trust boundary. The email is resolved as follows:

- If `ALT8MAIL_SAML_EMAIL_ATTRIBUTE` is set, Alt8Mail scans every
  `AttributeStatement` for an `Attribute` whose `Name` equals the configured
  value exactly. `FriendlyName` is display metadata and is never matched.
  Exactly one non-empty value after trimming is required; zero values or
  multiple conflicting values reject the sign-in. If the attribute is
  configured but missing, there is **no fallback** to the NameID.
- If the variable is unset, the assertion's `Subject/NameID` value must itself
  be a syntactically valid email: exactly one `@`, non-empty local and domain
  parts, and no whitespace. The NameID format may be `emailAddress` or
  unspecified; the format alone is never trusted.

The resulting value is trimmed, lowercased and then matched against
`ALT8MAIL_SAML_EMAIL_DOMAINS` and an existing active user.

There is no display-name attribute. The signed-in user keeps the Alt8Mail
name, email, aliases, password and role already stored in the account.

## Email-domain allowlist

`ALT8MAIL_SAML_EMAIL_DOMAINS` is a comma-separated list:

```
ALT8MAIL_SAML_EMAIL_DOMAINS="example.com, @example.org"
```

- Matching is exact and case-insensitive after normalization.
  `user@sub.example.com` does not match `example.com`.
- An empty or unset list means **any domain is allowed**. Any email the IdP
  asserts can then sign in to a matching existing account. Set the allowlist in
  production whenever the IdP can assert more than one domain.

## Tenant MFA policy

SAML honours the existing tenant security policy:

- `mfaEnforcement: "all"` requires MFA evidence for every user.
- `mfaEnforcement: "admins"` requires MFA evidence for `owner` and `admin`
  roles.
- Any other value does not require MFA evidence.

MFA evidence must be an exact `AuthnContextClassRef` in at least one
`AuthnStatement`. Substring matching is never used. The built-in allowlist is:

- `urn:oasis:names:tc:SAML:2.0:ac:classes:MobileTwoFactorContract`
- `urn:oasis:names:tc:SAML:2.0:ac:classes:MobileTwoFactorUnregistered`
- `urn:oasis:names:tc:SAML:2.0:ac:classes:Smartcard`
- `urn:oasis:names:tc:SAML:2.0:ac:classes:SmartcardPKI`
- `urn:oasis:names:tc:SAML:2.0:ac:classes:SoftwarePKI`
- `urn:oasis:names:tc:SAML:2.0:ac:classes:TimeSyncToken`
- `urn:oasis:names:tc:SAML:2.0:ac:classes:Token`
- `http://schemas.microsoft.com/ws/2008/06/identity/authenticationmethod/mfa`
- `http://schemas.microsoft.com/ws/2008/06/identity/authenticationmethod/multipleauthn`
- `http://schemas.microsoft.com/claims/multipleauthn`
- `https://schemas.microsoft.com/ws/2008/06/identity/authenticationmethod/mfa`
- `https://schemas.microsoft.com/ws/2008/06/identity/authenticationmethod/multipleauthn`

Add tenant-specific exact values with
`ALT8MAIL_SAML_MFA_AUTHN_CONTEXT_CLASSES`. `PasswordProtectedTransport` alone
never satisfies the policy. Rejected sign-ins are audited as
`auth.sso_failed` with detail `saml_mfa_required`.

## No just-in-time provisioning, no profile mutation

SAML never creates, enables or links an account. The normalized email must
match an existing active, non-deleted user in an active tenant. Unknown,
disabled, deleted, wrong-domain or inactive users are rejected with
`saml_unknown_user` (or the earlier fixed code for the failing check). Create
accounts before enabling SAML.

The only account change on a successful sign-in is the usual
`last_login_at` update. Alt8Mail does not write the SAML display name, email,
aliases, password or role.

## Session behaviour

On success Alt8Mail:

1. validates the request-tracking cookie, signature, audience, recipient,
   destination, conditions and assertion replay;
2. applies the domain allowlist, tenant IP policy and tenant MFA policy;
3. creates a normal Alt8Mail session using the existing session and CSRF
   cookies;
4. updates `last_login_at` and writes `auth.login` with detail `SAML SSO`;
5. redirects to `/`.

SAML sessions do **not** support "Remember me" in this tranche: the session
always uses the configured `SessionTTL` (default 12 hours). The login page does
not send a `remember` parameter for SAML.

Failed sign-ins redirect to `/?saml_error=1` only. Assertions, attributes,
NameIDs, certificates, tokens and raw errors are never placed in the URL or the
response. Each in-limit failure is audited as `auth.sso_failed` with one of the
fixed codes `saml_assertion`, `saml_email`, `saml_domain`,
`saml_unknown_user`, `saml_ip_blocked`, `saml_mfa_required` or `saml_session`;
`CreateSession` failures are recorded once. The ACS endpoint is rate limited
per client IP with the same budget as the OIDC callback. Over-limit requests,
and all requests on a disabled/unlicensed deployment, are rejected without an
audit write and without extending the limiter.

The ACS accepts only `application/x-www-form-urlencoded` POSTs up to 1 MiB;
other content types and larger bodies are rejected before parsing. Artifact
binding is not supported and requests carrying `SAMLart` are rejected, so a
client cannot consume the replay cache through the artifact branch.

Request tracking cookies expire after 10 minutes. The middleware stops tracking
a request when the ACS response carries a RelayState, which a conforming IdP
normally echoes; a response without one leaves the tracking cookie in place,
but replay is still blocked by the replay cache below. Accepted assertion IDs
are also recorded in an in-process replay cache with 1024 entries, so a
captured response cannot be posted twice. An assertion ID is consumed before
the account and policy checks, so a validly signed assertion that is later
rejected (for example for a wrong domain or MFA policy) still occupies its
entry until it expires. When all 1024 entries are unexpired the cache refuses
new assertions rather than evicting one, so a large tenant can be locked out
of new sign-ins until an entry expires. The cache protects a single process
only: large tenants or deployments with several server processes need a
larger or shared replay store, which is not part of this tranche.

## SP metadata availability and metadata caching

IdP metadata is loaded lazily on the first start, ACS or metadata request and
is cached for the process lifetime; restart Alt8Mail to pick up IdP key or
endpoint rotation. Failed loads are retried after a 30-second backoff.

The SP metadata endpoint needs the IdP metadata to construct the service
provider. If the IdP metadata URL is unreachable at startup, the SP metadata
endpoint returns 503 and SAML sign-in fails until the retry succeeds. When the
IdP cannot be reached during deployment, provision
`ALT8MAIL_SAML_IDP_METADATA_FILE` and restart so the SP metadata can be served
and registered out of band.

If both `ALT8MAIL_SAML_IDP_METADATA_FILE` and
`ALT8MAIL_SAML_IDP_METADATA_URL` are set, the file always wins and Alt8Mail
logs one first-load warning. Prefer exactly one source.

## Cleartext and loopback rules

`ALT8MAIL_PUBLIC_URL` must use `https://`. A plain `http://` origin is
accepted only when its host is loopback (`localhost`, `127.0.0.0/8` or `::1`),
so a test deployment cannot silently put SAML session material on the wire in
cleartext. IdP metadata URLs follow the same rule: HTTPS is required except for
loopback test endpoints, and an HTTPS metadata URL may not redirect to
cleartext.

## Single logout is unsupported

The crewjam library advertises a SingleLogoutService in the SP metadata by
default, but Alt8Mail does not implement SLO handlers. A user who signs out of
Alt8Mail ends the Alt8Mail session only; the IdP session is not terminated, and
an IdP-initiated logout message is not accepted. If an IdP requires SLO, do not
rely on the advertised endpoint.

## Operational notes

- The login page probes `/api/auth/oidc/config` and `/api/auth/saml/config`
  independently. Each button is shown only when its own probe reports enabled,
  and the "or sign in with your work email" divider is shown when either one is
  enabled.
- The login page shows one generic message for both `sso_error` and
  `saml_error`: "Single sign-on could not be completed. Sign in with your
  password or try again."
- Rotating the SP key pair requires updating the IdP registration and
  restarting Alt8Mail.
- The license file is read at startup. Renewing or changing the `saml`
  entitlement requires a restart; expiry is evaluated live.
- IdP metadata, SP key/cert and the replay cache are in memory only. A restart
  clears the replay cache and re-reads the IdP metadata.
