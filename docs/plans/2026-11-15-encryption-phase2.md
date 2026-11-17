# Encryption Phase 2 implementation record

Date: 2026-11-15
Branch: `feat/encryption-phase0`
Status: implemented, including webmail/admin UI and released locale catalogs

## Completed

- [x] WKD URL generation with the z-base-32/SHA-1 local-part hash
- [x] WKD key parsing, email-UID validation and HTTP discovery
- [x] HTTPS-only fetch, redirect rejection, 1 MiB cap and private-address SSRF guard
- [x] TOFU defaults for discovered keys and manual-import defaults
- [x] Key-change detection and `key_changed_at`
- [x] Explicit recipient-key trust endpoint
- [x] Recipient key preference (enterprise, verified, tofu, unknown)
- [x] Revoked/expired key exclusion
- [x] Delegated decryption policy and owner-key handling
- [x] Migration `0101_encryption_phase2.sql`
- [x] Search/AI/notification/IMAP boundary documentation
- [x] Webmail Settings key generation, import, fingerprint/public-key display and revocation
- [x] Composer Encrypt/Sign toggles with recipient-key and key-change checks
- [x] Reader encrypted banner and explicit decrypt flow with signature state
- [x] Contact-key discovery, import, verification and removal in People
- [x] Admin policy UI for mode, recipient-key requirement, import and delegated decryption
- [x] English i18n keys for the new controls
- [x] Signed-only inbound verification against saved contact keys

## Still pending

- [x] Translate the new encryption UI keys into the released locale catalogs
- [ ] WKD host allowlist/rotation policy for internal domains
- [ ] Key-change notification emails or in-app alerts
- [ ] Decrypt protected attachments in the reader
- [x] Client-side E2EE (Phase 4); S/MIME is implemented in Phase 3

## Verification

```bash
cd backend
go test ./...
go vet ./...
go test -count=1 ./internal/mailcrypto ./internal/httpapi
```
