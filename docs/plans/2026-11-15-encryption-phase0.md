# Encryption Phase 0 implementation record

Date: 2026-11-15
Branch: `feat/encryption-phase0`
Status: implementation complete; not wired into the mail pipeline

## Goal

Prove the MIME shapes, byte-preservation rules, library choices and policy
vocabulary before any production encryption or storage migration.

## Scope

- [x] Threat model and mode definitions (`docs/ENCRYPTION.md`)
- [x] Protocol-neutral crypto provider interface
- [x] Raw-byte-preserving MIME analyser
- [x] S/MIME enveloped and signed detection (inner signature deferred until decrypt)
- [x] PGP/MIME encrypted and signed detection
- [x] Inline PGP encrypted and clear-signed detection
- [x] Encrypted/signed attachment distinction
- [x] Headerless MIME entity support (OpenSSL S/MIME shape)
- [x] CRLF canonicalisation helpers
- [x] Policy validation for protocol, recipient key, signature and mode
- [x] Real OpenSSL S/MIME and OpenPGP fixture corpus
- [x] PGP/MIME signature verification against the preserved MIME entity
- [x] S/MIME signature verification through OpenSSL
- [x] Raw-byte preservation finding for OpenSSL CRLF quirks
- [x] CLI interop helper
- [x] Fixture regeneration command
- [x] Unit and fixture tests

## Deliberately not included

- No database migration or key storage
- No API endpoint, composer toggle or reader badge
- No decrypt/encrypt/sign implementation in production code
- No change to `parseIMAPMessage`, `mail_messages` or search indexing
- No S/MIME production provider until the CMS library decision is reviewed
- No claim of E2EE for server-managed keys

## Library findings

- `github.com/ProtonMail/go-crypto/openpgp` v1.5.2 is used for the generated
  OpenPGP fixtures and is the intended Phase 1 provider. It does not provide
  CMS/PKCS#7.
- `github.com/github/smimesign/ietf-cms` covers SignedData but not
  EnvelopedData.
- `github.com/fullsailor/pkcs7` and `github.com/digitorus/pkcs7` are legacy
  options that require a separate supply-chain and interop review.
- `github.com/misiektoja/go-pkcs7` is a newer EnvelopedData implementation, but
  it is pre-1.0 and RSA-focused. It is a Phase 3 candidate, not a Phase 1
  dependency.

## Verification

```bash
cd backend
go test ./internal/mailcrypto ./cmd/mailcrypto-fixtures
go run ./cmd/mailcrypto-analyze -parts internal/mailcrypto/testdata/fixtures/smime-signed.eml
go run ./cmd/mailcrypto-fixtures
```

The fixture generator uses OpenSSL when available for S/MIME and Go for
OpenPGP. Generated private keys remain in a temporary directory and are never
written into testdata.

## Phase 1 entry criteria

- Confirm the storage model for ciphertext and message metadata.
- Decide the exact signature canonicalisation rules against the fixtures and
  RFC 5751/3156.
- Add the key service with envelope encryption and audit.
- Add the inbound analyser call before writing body fields.
- Add the outbound wrapping point before `submitSMTPMessage`.
- Add a protocol/provider feature flag and tenant policy default `disabled`.
- Add end-to-end tests that prove plaintext is never persisted in E2EE mode.
