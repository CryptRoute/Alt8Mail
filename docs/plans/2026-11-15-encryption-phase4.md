# Encryption Phase 4 implementation record

Date: 2026-11-15
Branch: `feat/encryption-phase0`
Status: implemented and translated for browser-held OpenPGP; attachments and
multi-device recovery remain follow-ups

## Scope

A true client-side E2EE mode in webmail, separate from the server-managed
OpenPGP/S/MIME modes:

- Browser-generated or browser-imported OpenPGP private keys, protected by a
  user passphrase and stored in IndexedDB.
- Public-key publication to a tenant directory; the server never receives the
  private key or passphrase.
- PGP/MIME built and encrypted in the browser before sending.
- Ciphertext-only outbound storage and delivery.
- Raw ciphertext download for browser decryption.
- In-memory decrypted bodies, local session search and reader/composer/settings
  UI.
- Server-side guards that prevent the server-managed path from being used while
  `mode=client_side`.

## Threat model and promise

Client-side mode protects message bodies from database/backup theft, routine
operator access and a compromised application backend after the ciphertext is
stored. It does not protect against a compromised browser or device, XSS in the
application origin, a maliciously modified JavaScript bundle, or metadata
analysis. The server still sees recipients, Subject, timestamps, sizes, folder
state, IP/user-agent, delivery metadata and search queries. Plaintext inbound
mail from a sender who did not encrypt also remains readable by the server; the
mode does not re-encrypt inbound plaintext or make the whole mailbox
zero-access.

The server is trusted for public-key directory integrity and owns JavaScript
delivery. A fully malicious server could serve substituted public keys or
modified code. Do not market this mode as protection from a hostile service
operator.

## Library decision

Vendored OpenPGP.js 6.3.2 as `public/vendor/openpgp.min.mjs` with its license
(`LICENSE-openpgp.txt`) and version marker. The browser uses curve25519 keys,
AES-encrypted private-key export, and PGP/MIME. The bundle is precached by the
service worker with the rest of the module graph.

## Storage

Migration `0103_encryption_phase4.sql`:

- extends `user_crypto_identities.source` with `client`;
- adds `outbox.client_mime text NOT NULL DEFAULT ''`.

Only public keys are stored in `user_crypto_identities`. The private key lives
in IndexedDB; the server's crypto vault is not involved in the client-side
private-key path. Queued client-side sends store ciphertext, not plaintext.

## Endpoints

- `POST /api/encryption/client-identity` publishes a public key for an owned
  address. It validates the UID/address, rejects revoked, expired and
  non-encryption keys, retires other active OpenPGP identities for the user,
  and records an audit event.
- `GET /api/encryption/directory?protocol=openpgp&emails=...` returns up to 50
  active tenant public keys. In client-side mode it returns only
  `source='client'` keys and skips unusable keys.
- `GET /api/messages/{id}/ciphertext` returns the raw source (32 MiB cap,
  no-store) only in client-side mode. It refuses synthesized index-only sources
  and truncated ciphertext.
- `POST /api/send` accepts `clientEncrypted` / `clientEncryptedMIME`, rejects
  plaintext bodies, attachments and non-native accounts, and requires the MIME
  to analyse as encrypted OpenPGP. In client-side mode it rejects any send that
  does not carry browser-built ciphertext, even when the request omits the
  encryption flags. API v1 plaintext send, MCP send, mail merge, automation
  auto-replies and phishing-campaign delivery are blocked for the same reason.
- Existing `POST /api/messages/{id}/decrypt`, key generation/import, draft
  storage and DLP checks refuse client-side mode.

## Client implementation

`public/src/crypto/client-e2ee.js` owns:

- IndexedDB identity records keyed by address;
- passphrase generation/import/unlock/lock/export;
- public-key publication;
- recipient-key resolution across the tenant directory and saved contacts,
  filtering revoked, expired, changed-unverified and unusable keys;
- plaintext MIME construction, signature (optional), PGP/MIME wrapping and
  encryption to recipients plus the sender;
- armored-message extraction, browser decryption, signature verification and
  text/plain + text/html multipart parsing.

`public/app.js` adds the security settings panel, key setup/unlock dialogs,
composer mode, recipient checks, send/schedule encryption, reader decrypt
action, local decrypted-body cache and session search merge. AI/tone tools,
DLP body checks and server-side draft saves are suppressed in this mode.

## UI and language

The new Settings, composer, reader, People, pending-queue and Admin strings are
part of the canonical `locales/en.json` catalog. Released locale catalogs are
updated through the local draft/review/promote pipeline; no translation content
leaves the machine.

## Tests

- Go: policy validation, vault AAD, OpenPGP key usability/expiry, client-send
  validation, directory and ciphertext endpoint guards in `httpapi` and
  `mailcrypto` packages.
- Browser E2E: generates browser keys, exports/imports the encrypted private
  key, rejects an import under another address, encrypts a PGP/MIME message with
  a signature, confirms plaintext is absent from the envelope, decrypts as the
  recipient, verifies the signature state and confirms an expired directory key
  is not selected. A second browser flow drives the real composer with a mocked
  policy/directory, captures `POST /api/send`, and confirms the request carries
  only OpenPGP/MIME ciphertext with empty body and attachment fields.
- GnuPG interop: `tests/crypto-interop.test.mjs` imports the browser-generated
  key into an isolated GnuPG home, decrypts the OpenPGP.js ciphertext, verifies
  the signature status and compares the plaintext MIME bytes. The test skips
  when `gpg` is unavailable.
- Node: API and i18n suites, service-worker/module-manifest check and full E2E.

## Limits

- Attachments are rejected in client-side mode, including attachments inherited
  from an existing draft.
- Drafts are not persisted server-side.
- A queued client-side send can be cancelled but not restored to a draft; the
  UI labels the action Cancel and does not offer Undo.
- External recipients need a usable public key in the tenant directory or saved
  contacts; there is no plaintext fallback.
- No multi-device key sync, key escrow or recovery. Exporting the encrypted
  private key is the backup path.
- No client-side S/MIME, no forward secrecy and no browser access from
  IMAP/DAV.
- One PGP/MIME ciphertext covers To/Cc/Bcc. PGP key packets expose recipient
  key IDs, so Bcc is not hidden from the other recipients by encryption alone;
  per-recipient ciphertext delivery is a follow-up.
- Decrypted bodies are memory-only and disappear on reload/lock; local search
  covers only messages decrypted in the current session.
- Switching a tenant from server-managed to client-side requires a planned key
  transition; old server-encrypted messages are not readable in the browser.
  A server-managed key cannot be republished as a browser key; a new browser key
  is required, and browser identities never get a server-side private-key row.
  Retired keys and historical backups require an operator key-destruction and
  retention process.

## Verification

```bash
cd backend
go test -count=1 ./...
go vet ./...

cd ..
npm test
npm run test:all
```

## Follow-ups

- [ ] Encrypted attachment upload/download in client-side mode
- [ ] Browser-local encrypted draft persistence (IndexedDB) with explicit UX
- [ ] Multi-device key backup/restore and key revocation distribution
- [ ] Per-tenant directory controls and optional cross-tenant discovery
- [ ] Publish browser public keys to WKD/keyservers for external senders
- [ ] Client-side TOFU pinning of tenant directory fingerprints to detect server key substitution
- [ ] Client-side S/MIME
- [ ] Migration tooling for server-managed to client-side transitions
