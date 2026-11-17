# Encryption Phase 3 implementation record

Date: 2026-11-15
Branch: `feat/encryption-phase0`
Status: implemented, translated; revocation and HSM remain follow-ups

## Scope

Server-managed S/MIME alongside server-managed OpenPGP:

- Certificate and private-key import (PEM and PKCS#12).
- Per-protocol identities and recipient certificates.
- CMS SignedData and EnvelopedData for outbound sign, encrypt and sign-then-encrypt.
- Inbound S/MIME decryption and inner-signature verification.
- X.509 validity, email SAN, `emailProtection` EKU, key-usage and chain checks
  against system roots plus an optional enterprise CA bundle.
- Optional LDAP directory discovery for recipient certificates.
- Policy values `openpgp`, `smime` and `both`; existing `openpgp` policies keep
  working.
- Webmail and People UI for both protocols.

## Library decision

Use `go.mozilla.org/pkcs7 v0.10.0`. It is the maintained Mozilla line with both
CMS SignedData and EnvelopedData, RSA and ECDSA signing, and AES content
encryption. It is a small pure-Go dependency with a long public history. The
library's process-wide content-encryption setting is pinned to AES-256-CBC so
S/MIME interop does not depend on a caller's earlier setting.

PKCS#12 is decoded with `software.sslmate.com/src/go-pkcs12 v0.7.4`, and the
optional LDAP lookup uses `github.com/go-ldap/ldap/v3 v3.4.15`. Both are
established Go libraries; the LDAP dependency adds Kerberos/NTLM support that
is not used by the directory path.

## Non-goals

- Becoming a certificate authority.
- Smart-card access from the browser without a native helper.
- PKCS#11/HSM signing in the default build. The private-key seam is kept
  behind `crypto.Signer`; an HSM deployment needs an operator-provided signer
  service, key ceremony, HA plan and latency budget.
- Live OCSP/CRL fetching in this slice. Certificate validity, SAN/EKU/key usage
  and chain are checked, but revocation checking is a follow-up that needs a
  configured responder/CRL policy and an SSRF-safe fetch path. Do not claim
  revocation validation until that exists.

## Storage

Migration `0102_encryption_phase3.sql` adds certificate metadata to identities
and contact certificates. Private keys remain AES-GCM-wrapped by the crypto
vault; the database stores the wrapped PEM, never the unwrapped key.

## Endpoints

- `POST /api/encryption/smime/identity` imports a PEM/PKCS#12 identity.
- `GET /api/encryption` returns both protocol identities and enabled protocols.
- Contact certificate import, trust, delete and discovery reuse the contacts
  endpoints with `protocol=smime`.
- `GET /api/encryption/recipients?protocol=smime&emails=...` returns recipient
  certificates.
- Existing decrypt, send and outbox endpoints select the protocol from the
  message or outbox item.

## Configuration

- `ALT8MAIL_SMIME_CA_FILE` - optional PEM bundle of enterprise roots.
- `ALT8MAIL_SMIME_LDAP_URL` / `_BASE_DN` / `_BIND_DN` / `_BIND_PASSWORD` -
  optional directory lookup. LDAP requires TLS unless the development-only
  insecure flag is set.
- `ALT8MAIL_SMIME_LDAP_FILTER` / `_ATTRIBUTE` - optional search overrides.

## Completed

- [x] `go.mozilla.org/pkcs7` CMS SignedData/EnvelopedData provider
- [x] PEM and PKCS#12 S/MIME identity import
- [x] OpenSSL interop tests for generated signed and enveloped messages
- [x] Certificate validity, email SAN, EKU, key-usage and chain validation
- [x] Migration `0102_encryption_phase3.sql`
- [x] Optional LDAP directory discovery with StartTLS/LDAPS guard
- [x] Policy `openpgp`, `smime` and `both`
- [x] Outbound sign, encrypt and sign-then-encrypt with protocol selection
- [x] Inbound S/MIME decrypt and inner-signature trust mapping
- [x] Signed-only message verification endpoint and reader action
- [x] Settings, composer, reader, People and Admin UI for both protocols

## Still pending

- [ ] OCSP/CRL revocation checking
- [ ] HSM/PKCS#11 `crypto.Signer` deployment and key ceremony
- [ ] Smart-card S/MIME in webmail without a native helper
- [x] Translate the Phase 3 UI strings into released locale catalogs

## Verification

```bash
cd backend
go test ./internal/mailcrypto ./internal/httpapi
go vet ./internal/mailcrypto ./internal/httpapi
```

Plus the OpenSSL S/MIME fixture corpus and new round-trip tests for CMS signing,
encryption, certificate validation and PKCS#12 import.
