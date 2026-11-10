# White-Label Branding

White-label branding is a paid `white_label` entitlement. It extends the free
tenant branding (product name, primary color, support email, login message)
with:

- a login logo (`logoUrl`);
- support and legal links (`supportUrl`, `legalUrl`);
- removal of the “via Alt8Mail” attribution (`hideAttribution`);
- validated custom webmail hostnames (`webmailHosts`).

Without a valid or grace `white_label` license, the public branding payload
strips these fields, and an admin update that requests a non-empty/true value
returns `403 WHITE_LABEL`. Changing ordinary product name/color/support/login
message remains free.

## Custom webmail hostnames

A hostname must be a lowercase DNS name (no scheme, path, port, wildcard or IP
address), at most 253 characters, and at most five per tenant. It must be
globally unique: a hostname already assigned to another tenant cannot be
claimed.

The Control Plane remains authoritative for DNS, TLS and nginx. Before a
hostname can be saved:

1. add it as an enabled **webmail** route in **System Control → Web Net**;
2. issue or assign a certificate for it;
3. save the hostname in the tenant's Organization settings.

The webmail verifies the enabled route and certificate assignment through the
Control inventory and stores the hostname in `tenant_webmail_hosts`. Unknown
hosts fall back to the tenant's mail domain; if neither matches, the default
Alt8Mail identity is used. An unknown host never resolves to another tenant.

If the admin console runs on a different host (split-origin deployment), the
webmail origin is still `ALT8MAIL_PUBLIC_URL`; add the route/certificate for
that host. `WEB_NET_MANAGED=1` installations must add the same route and
certificate through their external proxy.

## Login behavior

`GET /api/branding` resolves the tenant by custom webmail host first, then by
mail domain, and returns `productName`, `primaryColor`, `supportEmail`,
`loginMessage`, `logoUrl`, `supportUrl`, `legalUrl` and `hideAttribution`.
Responses are cached for five minutes (`Cache-Control: public, max-age=300`).

The login page applies the logo with `alt` text and hides it if loading fails,
adds Support/Legal links, and removes product attribution only when
`hideAttribution` is true. TLS and security notices remain. Branding values are
text/URLs only; they are never inserted as raw HTML.

## Troubleshooting

- **403 `WHITE_LABEL`:** the license is missing, expired or does not include
  `white_label`. Renew the license and restart the webmail.
- **Host rejected:** check the Web Net route is enabled, `service=webmail`, has
  a certificate, and is not owned by another tenant.
- **Logo missing:** use an absolute `https://` URL without credentials.
- **Branding not visible:** confirm the request host matches the custom host or
  a tenant mail domain; the five-minute cache can delay a change.
- **TLS/nginx errors:** fix the Web Net route/certificate in Control; the
  webmail does not manage DNS or certificates.
