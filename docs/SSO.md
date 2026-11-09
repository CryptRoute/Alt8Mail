# OIDC Single Sign-On

Alt8Mail supports opt-in OpenID Connect (OIDC) single sign-on for existing
accounts. When SSO is not configured, the login page never shows an SSO button
and password, passkey, TOTP and recovery-code sign-in are unchanged.

SSO is a paid entitlement. It is enabled only when every required environment
variable below is set **and** the verified license includes the `sso` feature
(see [LICENSING.md](LICENSING.md)). An unlicensed, invalid, expired or
non-`sso` license behaves like an incomplete configuration: the login page
hides the button and the start and callback endpoints redirect with
`sso_unavailable`. A license in its grace period keeps SSO enabled.

This first deployment is configured through environment variables. Per-tenant
Control Plane SSO configuration, SCIM, group-to-role mapping, IdP-initiated
login and JIT user provisioning are out of scope. Environment-configured SAML
SSO is documented in [SAML.md](SAML.md).

## Required environment variables

| Variable | Required | Purpose |
| --- | --- | --- |
| `ALT8MAIL_OIDC_ISSUER` | Yes | Issuer URL, exactly as published in the IdP discovery document. A trailing slash is preserved and must match the discovery `issuer` value. |
| `ALT8MAIL_OIDC_CLIENT_ID` | Yes | OIDC client ID registered with the IdP. |
| `ALT8MAIL_OIDC_CLIENT_SECRET` | Yes | OIDC client secret. Treat it as a credential and rotate it after any exposure. |
| `ALT8MAIL_OIDC_REDIRECT_URL` | Yes | Absolute callback URL, for example `https://mail.example.com/api/auth/oidc/callback`. It must match the IdP registration exactly. |
| `ALT8MAIL_OIDC_DISPLAY_NAME` | No | Label shown on the login button. When empty, the login page uses its translated "Continue with single sign-on" label. |
| `ALT8MAIL_OIDC_EMAIL_DOMAINS` | No | Comma-separated email-domain allowlist. Empty means no domain restriction. |

All four required variables must be non-empty after trimming, and the `sso`
entitlement must be valid. A partial configuration or missing entitlement
disables SSO rather than failing server startup: there is no button and the
start and callback endpoints redirect to the login page with a generic error.
The disabled callback performs no audit write and does not touch the callback
limiter, so an unconfigured or unlicensed deployment has no new failure path.

## Identity provider registration

Register the Alt8Mail callback URL as an authorized redirect URI:

```
https://<mail-host>/api/auth/oidc/callback
```

The authorization request uses:

- response type `code` (authorization-code flow)
- scopes `openid email profile`
- PKCE `S256`
- a random `state` value and a random `nonce`

The state cookie is `HttpOnly`, `SameSite=Lax`, `Secure` when
`ALT8MAIL_COOKIE_SECURE` is enabled, and expires after 10 minutes. It is
single-use: the callback clears it on every terminal path.

## Required claims

The ID token must be signed by the discovered provider and pass issuer,
audience, signature, expiry and nonce verification.

| Claim | Requirement |
| --- | --- |
| `iss` | Must match `ALT8MAIL_OIDC_ISSUER`. |
| `aud` | Must contain `ALT8MAIL_OIDC_CLIENT_ID`. |
| `exp` | Required and must be in the future when the callback runs. |
| `nbf` | If present, must not be in the future. |
| `iat` | Not validated by Alt8Mail; go-oidc checks `exp` and `nbf`, not `iat`. |
| `nonce` | Must equal the nonce stored in the state cookie. |
| `email` | Required. It is trimmed and lowercased before account lookup. |
| `email_verified` | If present, it must not be `false`. A missing claim is accepted. |
| `amr`, `acr` | Used only when the tenant MFA policy applies (see below). |

`sub` is verified as part of the token but is not used as the account key.
Accounts are matched by the normalized email address.

## Email-domain allowlist

`ALT8MAIL_OIDC_EMAIL_DOMAINS` is a comma-separated list. Values are trimmed,
lowercased, may include an optional leading `@`, and duplicates are dropped:

```
ALT8MAIL_OIDC_EMAIL_DOMAINS="example.com, @example.org"
```

- A non-empty list requires the ID token's email domain to match one entry
  exactly. `user@sub.example.com` does not match `example.com`.
- An empty or unset list means no domain restriction is applied. Any verified
  email from the IdP can then sign in to a matching existing account, so set
  the allowlist in production whenever the IdP can assert more than one
  domain. The empty list remains supported for single-tenant deployments that
  intentionally trust every identity the IdP returns.

## Tenant MFA policy

SSO honours the existing tenant security policy:

- `mfaEnforcement: "all"` requires MFA evidence for every user.
- `mfaEnforcement: "admins"` requires MFA evidence for `owner` and `admin`
  roles.
- `mfaEnforcement: "off"` does not require MFA evidence.

When MFA evidence is required, the ID token must contain at least one of:

- an `amr` value of `mfa`, `otp` or `hwk` (case-insensitive), or
- an `acr` string containing `mfa` (case-insensitive).

Otherwise the callback is rejected with `sso_mfa_required`. Ensure the IdP is
configured to return `amr` or `acr` for users who completed a second factor.

## No just-in-time provisioning

SSO never creates, enables or links an account. The normalized email must match
an existing active, non-deleted user in an active tenant. Unknown, disabled,
deleted or wrong-domain users are rejected. Provision accounts before enabling
SSO.

## Callback behaviour

On success Alt8Mail:

1. verifies the state cookie and clears it;
2. exchanges the authorization code with the PKCE verifier;
3. verifies the ID token with the discovered provider keys;
4. checks the nonce, `email_verified`, the domain allowlist, the tenant IP
   policy and the tenant MFA policy;
5. creates a normal Alt8Mail session using the existing session and CSRF
   cookies, with the session TTL selected by "Remember me";
6. updates `last_login_at` and writes `auth.login` with detail `OIDC SSO`;
7. redirects to `/`.

Failed callbacks redirect to `/?sso_error=<code>` using a fixed, safe code.
Provider responses, authorization codes, tokens and raw claims are never
included in the redirect or exposed to the browser. Every failed callback
within the per-IP limit is audited as `auth.sso_failed` with the fixed error
code as the detail; pre-user failures use an anonymous audit entry, and
post-verification failures may carry the normalized email as the target.
Successful sign-ins are audited as `auth.login` with detail `OIDC SSO`.
Callback attempts are rate limited per client IP. Over-limit callbacks, and all
callbacks on an SSO-disabled deployment, are rejected without an audit write;
the limiter itself is the signal for over-limit requests.

The login page shows a generic error for any `sso_error` value and removes the
query parameter. Password and passkey sign-in remain available throughout.

## Operational notes

- The server discovers the IdP lazily on the first start or callback request,
  with a 10-second discovery timeout and a short retry backoff. An unreachable
  IdP cannot block server startup or the login page.
- Rotate the OIDC client secret like any other credential. Restarting Alt8Mail
  clears the in-memory discovery cache.
- The license file is read at startup. Renewing or changing the `sso`
  entitlement requires restarting Alt8Mail; the license is not reloaded while
  the server runs. Expiry, however, is evaluated live: a running server stops
  offering SSO when the license is already past its grace period.
- If the IdP requires a different issuer form (for example a trailing slash),
  configure `ALT8MAIL_OIDC_ISSUER` exactly as the discovery document returns it.
- IdP-initiated login is not supported; users must begin at the Alt8Mail login
  page.
