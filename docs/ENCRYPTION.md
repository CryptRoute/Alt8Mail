# Encryption architecture

Status: Phases 0-5 implemented (2026-11-16): server-managed OpenPGP and S/MIME,
WKD/TOFU trust, OCSP/CRL revocation, optional LDAP certificate discovery,
browser-held OpenPGP and S/MIME client-side E2EE, PKCS#11-backed S/MIME,
guarded server-managed to client-side migration and the webmail/admin controls.
Encryption UI strings are translated in the 33 reviewed locale catalogs. The
feature remains disabled by default through tenant policy.

## Product decision

Alt8Mail will support three distinct encryption models. They must never be
presented as interchangeable:

| Mode | Who can read message content | Protects against | Does not protect against |
| --- | --- | --- | --- |
| `server_managed` | The server and anyone who can administer it | Database/backup/disk theft; accidental plaintext retention | A compromised application server, a privileged administrator, or legal process served on the operator |
| `user_unlocked` | The user while an unlocked session is open; the server during that session | Offline database theft; casual admin access | A live server while the user is unlocked; memory compromise |
| `client_side` | The user's client only, for messages the browser encrypts or that arrive for its published key | Server compromise, operator access, database theft of protected messages | XSS in the client, a compromised device/browser, metadata analysis, plaintext mail sent by others |

**Server-managed encryption is not end-to-end encryption.** The UI, licensing
copy, documentation and sales material must say so explicitly. The OSS core
starts with OpenPGP in `server_managed` mode; this is the lowest-risk way to
prove the MIME/key/policy pipeline. The client-side mode is a later program, not
a flag on the same implementation.

## Protocol sequencing

**Phase 1: OpenPGP server-managed.** `github.com/ProtonMail/go-crypto/openpgp`
is maintained and covers PGP/MIME. The Go S/MIME ecosystem is the weaker link:
`ProtonMail/go-crypto` has no CMS/PKCS#7 package, and the available CMS
libraries are small, pre-1.0 or limited to SignedData. OpenPGP also gives the
OSS/privacy audience a credible feature before the enterprise S/MIME phase.

**Phase 2: OpenPGP trust and product boundaries.** WKD discovery, contact keys,
TOFU, key-change warnings, shared-mailbox policy, header-only search, IMAP/DAV
and notification behaviour.

**Phase 3: S/MIME enterprise.** Certificate import, optional LDAP directory
discovery, chain/expiry/SAN/EKU validation, OCSP/CRL revocation, CMS
signed/enveloped data and sign-then-encrypt. PKCS#11-backed signing and
decryption are available in server-managed mode; see the HSM section below.
Do not become a CA. Smart-card S/MIME in webmail still requires a native
PKCS#11/PCSC helper or the operator's token-backed server identity.

**Phase 4: client-side E2EE.** Implemented for OpenPGP and S/MIME in webmail:
the browser generates or imports the private key or PKCS#12 bundle, publishes
only public material, builds PGP/MIME or CMS itself, downloads raw ciphertext
to decrypt, and keeps decrypted content in memory. Server-managed drafts, DLP,
body search and AI are disabled in this mode. See the Phase 4 and Phase 5
sections below for limits.

## Key storage contract

- Private keys are never stored in `users.preferences` or a plain `TEXT`
  column.
- Server-managed keys use envelope encryption with AES-256-GCM under
  `ALT8MAIL_CRYPTO_SECRET` (with a previous-secret rotation slot); the resulting
  wrapped key, nonce and AAD are stored in PostgreSQL. A KMS/HSM-backed KEK is
  the documented deployment upgrade, not part of the default build.
- Browser-held OpenPGP keys are passphrase-encrypted with OpenPGP.js's iterated
  S2K and stored in IndexedDB. Browser-held S/MIME identities are re-encoded as
  a PKCS#12 bundle protected by the user's chosen passphrase before IndexedDB
  sees them, even when the imported bundle had no password. Argon2id would apply only to a future passphrase-derived
  server vault. If the server can unwrap without user interaction, the mode is
  server-managed and is not E2EE.
- Keys have a lifecycle: created, active, rotated, retired, revoked, destroyed.
  Old keys remain available for decrypting old messages. Signing always uses
  the current key.
- Backups and restore tests include wrapped keys and the KMS key. Losing the key
  loses the mail.
- Per-tenant KEK isolation for multi-tenant deployments.
- HSM/PKCS#11 is an operation (`crypto.Signer`/`crypto.Decrypter`), not just a
  storage location. The server-managed S/MIME path can use a token-backed
  identity from `ALT8MAIL_SMIME_PKCS11_IDENTITIES`; the HSM key never enters
  PostgreSQL. HA, latency and key-ceremony requirements remain deployment
  decisions.
- Phase 1 should reuse the existing envelope pattern (`tenant_encryption_keys`
  / `sealTenantSecret` in `backend/internal/httpapi/byok.go`), but not treat
  `SessionSecret` as a permanent KEK: rotating the session secret must not make
  stored private keys unreadable. The crypto KEK needs its own versioned
  lifecycle and recovery path.

Schema sketch (not migrated in Phase 0):

```sql
crypto_identities(id, tenant_id, user_id, email, protocol, key_id, fingerprint,
                  public_material, cert_chain, algorithm, capabilities, source,
                  trust, status, not_before, not_after, revoked_at,
                  created_at, rotated_at)
crypto_private_keys(identity_id, wrapped_key, wrap_algorithm, kek_version,
                    nonce, aad, created_at, retired_at)
contact_crypto_keys(tenant_id, user_id, email, protocol, key_id, fingerprint,
                    public_material, cert_chain, source, trust, verified_at,
                    not_after, revoked_at, last_seen_at, key_changed_at)
message_crypto(message_id, protocol, encrypted, signed, signature_state,
               signer_key_id, recipient_key_ids, ciphertext_ref, decrypt_state,
               verified_at, error_code)
```

## Message flow contract

### Inbound

1. Fetch the raw message.
2. Inspect the outer MIME structure only.
3. Store ciphertext in a dedicated table/object store. Do not write a decrypted
   body to `body_text`/`body_html` in E2EE modes.
4. Index only visible metadata: subject, participants, date, flags, labels.
5. Decrypt on open through `POST /api/messages/{id}/decrypt` in server-managed
   mode, or `GET /api/messages/{id}/ciphertext` plus browser decryption in
   client-side mode. Never a `GET ...?decrypt=true` and never with a passphrase
   in a URL.
6. Verify signatures against the exact raw/canonicalised entity before parsing
   or trusting the content, then sanitise HTML with the existing pipeline.
7. Audit every server-managed decrypt, including delegated/shared-mailbox
   access. Client-side mode audits ciphertext downloads; the browser decrypt
   itself cannot be observed by the server.
8. Return structured signature state, not a boolean.

### Outbound

1. Build the MIME entity once.
2. Resolve recipient keys/capabilities.
3. Sign and/or encrypt according to the composer and policy.
4. Encrypt to the sender as an additional recipient.
5. Submit the final raw bytes to `submitSMTPMessage`.
6. Store the Sent copy in the mode's storage form (ciphertext for E2EE).
7. DLP runs on plaintext before encryption in server-managed mode. In
   client-side mode DLP cannot inspect the body; the policy must say so.

### MIME invariants

- Never verify a signature against a re-serialised message. Keep raw entity
  bytes; use `SignedEntity` only for explicit CRLF canonicalisation.
- Phase 0 found that OpenSSL on Windows can emit `\r\r\n` inside an S/MIME
  signed entity. The fixture generator therefore preserves raw entities; a
  blanket CRLF normalisation breaks the digest. Phase 1 must decide
  canonicalisation per protocol and verify against raw bytes first.
- `multipart/signed`, `multipart/encrypted`, `application/pkcs7-mime`,
  `application/x-pkcs7-mime`, `application/pgp-encrypted`, `application/pgp-signature`
  and inline PGP are all represented in the Phase 0 detector.
- Support sign-then-encrypt (outer enveloped, signature inside) and
  encrypt-then-sign (outer signed, encrypted content) without claiming the
  outer view knows the inner state.
- Limit nesting, part count and decompression size before parsing decrypted
  content.
- S/MIME certificate validation must check chain, email SAN, `emailProtection`
  EKU, key usage, validity and revocation. A mathematically valid signature is
  not a trusted signature.

## Trust and UX

Signature badges distinguish:

- `valid` - signature matches the visible `From` and the key is trusted;
- `valid_untrusted` - cryptographically valid, key not verified;
- `key_changed` - a previously pinned key changed; require confirmation;
- `expired`, `revoked`, `invalid`, `none` and `unknown`.

Encryption badges separately show encrypted/not encrypted and whether the
message is decryptable with the current identity. Never imply that encryption
hides the Subject, From/To, timing or size. Neither protocol provides forward
secrecy.

## Product limits that must be documented

- Server-side body search and AI features stop working for E2EE content.
- DLP, antivirus and some spam filtering cannot inspect ciphertext.
- CalDAV/iMIP calendar invitations cannot be both encrypted and consumable by
  ordinary calendar clients; keep invitations signed rather than encrypted, or
  handle them only in webmail.
- IMAP/DAV clients do not inherit webmail-held keys.
- Shared mailboxes and delegation need an explicit key-ownership policy.
- Push notifications must not contain encrypted content or sensitive subjects.
- Legal hold/eDiscovery has no plaintext access in client-side mode.

## Phase 1 server-managed OpenPGP (backend)

Implemented behind the tenant policy `encryption.mode=server_managed`:

- Migration `0100_encryption_phase1.sql`: OpenPGP identities, wrapped private
  keys, contact public keys, message crypto metadata and outbox crypto flags.
- `ALT8MAIL_CRYPTO_SECRET` wraps private keys with AES-256-GCM. Development
  falls back to the session secret; production (`ALT8MAIL_ENV=production`)
  refuses key generation and decryption without the dedicated secret. Rotation
  uses `ALT8MAIL_CRYPTO_SECRET_PREVIOUS`.
- `/api/encryption` status, key generation/import/revoke, contact-key
  management and recipient-key lookup.
- Inbound IMAP sync detects encrypted messages before body extraction, stores
  ciphertext metadata with an empty body, and never extracts encrypted
  attachments.
- `POST /api/messages/{id}/decrypt` fetches the raw message, decrypts with the
  active server-managed key, verifies an inner signature and returns the body
  with `Cache-Control: no-store`. It audits the decryption.
- Outbound send/patch accept `encrypt` and `sign`. Delivery resolves contact
  keys, encrypts or signs the MIME entity before `submitSMTPMessage`, and
  refuses encrypted sending for connected external accounts.
- The tenant admin policy now has `encryption`: `mode` (`disabled` or
  `server_managed`), `protocol` (`openpgp`), `allowUserImport` and
  `allowDelegatedDecrypt`. Every encrypted send requires a public key for every
  recipient; the server never falls back to plaintext.

Implemented in later phases: client-side attachment decryption in the reader,
live OCSP/CRL revocation, PKCS#11-backed S/MIME and client-side OpenPGP and
S/MIME. Server-managed decryption currently returns the decrypted text and HTML
body; protected attachment parts are still opened through the original
IMAP/DAV client or a follow-up reader attachment view. Remaining follow-ups also
include per-user shared-mailbox key ownership beyond the delegated decrypt
policy and plaintext legal-hold access in client-side mode. The client-side
limits are listed in the Phase 4 and Phase 5 sections.

## Existing code integration points

- Inbound detection: `backend/internal/httpapi/mail_imap.go` `parseIMAPMessage`
  (Phase 1 calls `mailcrypto.Analyze` before body extraction and before any
  `body_text`/`body_html` write).
- Raw-source viewer: `backend/internal/httpapi/mail_source.go`; Phase 1 needs
  full ciphertext, not only the current 4 MB preview.
- Outbound wrapping: `backend/internal/httpapi/mail_smtp.go` where the final RFC
  822 bytes are built before `submitSMTPMessage`.
- Policy/DLP gate: `backend/internal/httpapi/features_send.go`
  `evaluateOutboundDLP` and the send queue.
- Message storage/index: `backend/migrations/0011_mail_tables.sql`
  (`mail_messages`, FTS on `body_text`).
- Metadata/audit: `appendStateAudit` and the workspace audit views.

## Phase 0 deliverables in this tree

- `backend/internal/mailcrypto` - protocol-neutral types, provider interface,
  policy validation and a raw-byte-preserving MIME analyser.
- `backend/cmd/mailcrypto-analyze` - manual interop helper.
- `backend/cmd/mailcrypto-fixtures` - OpenPGP/OpenSSL fixture generator.
- `backend/internal/mailcrypto/testdata/fixtures` - real S/MIME and PGP/PGP-MIME
  messages plus synthetic edge cases.
- Tests for S/MIME enveloped/signed, PGP/MIME encrypted/signed, inline PGP,
  encrypted attachments, malformed multipart, headerless signed entities,
  CRLF canonicalisation and policy validation.

## Phase 0 non-goals

No decryption, encryption, key generation in production, database migration,
API endpoint, composer toggle, lock badge or mail-pipeline behaviour change.
The analyser is not called from `parseIMAPMessage` yet; Phase 1 will add it
after the storage/decryption contract is reviewed.

## Phase 2 trust and product boundaries

- **WKD discovery.** `mailcrypto.WKDURLs` implements the z-base-32/SHA-1
  local-part hash for both Web Key Directory methods. `POST
  /api/encryption/contacts/{email}/discover` fetches a key over HTTPS with
  redirects disabled, a 1 MiB response cap and a private-address SSRF guard.
  A returned key must carry the requested email UID.
- **TOFU and key changes.** WKD keys are stored as `tofu`; manually imported
  keys default to `unknown`. A different fingerprint for an address marks the
  new key `key_changed_at` and forces it to `unknown` unless the user explicitly
  verifies it. The composer refuses to encrypt to a changed key until it is
  verified through `POST /api/encryption/contacts/{id}/trust`.
- **Recipient preference.** Key lookup prefers `enterprise` then `verified`,
  then `tofu`, then `unknown`; revoked and expired keys are not used.
- **Shared mailboxes.** `encryption.allowDelegatedDecrypt` defaults to false.
  A delegate can decrypt an encrypted delegated message only when the
  organization explicitly enables it; the owner's key and state are used and
  the delegate action is audited.
- **Search and AI.** Encrypted messages have no `body_text`, so server-side FTS,
  AI summaries and triage cannot inspect ciphertext. Subject/participant
  headers remain indexable and visible by protocol design.
- **Notifications.** Browser push uses sender/subject metadata only; it never
  decrypts or includes the encrypted body.
- **IMAP/DAV.** Native clients receive the original PGP/MIME ciphertext. The
  webmail decrypt endpoint does not expose plaintext through IMAP or DAV.
- **Webmail UI.** Settings → Security & privacy shows active OpenPGP and
  S/MIME identities, fingerprints, certificate metadata and public material,
  and supports OpenPGP generation/import plus S/MIME PEM/PKCS#12 import and
  revocation. The composer exposes Encrypt and Sign toggles and a protocol
  selector when both are enabled, with live recipient-key checks; the reader
  shows an encrypted banner, decrypts on explicit action and can verify a
  signed-only message against a saved contact key. People manages OpenPGP keys
  and S/MIME certificates, discovery/import, key-change warnings and explicit
  fingerprint verification. All controls stay hidden when the tenant policy is
  disabled.
- **Admin UI.** Policies → Message encryption sets `mode`, `protocol`
  (`openpgp`, `smime` or `both`), `allowUserImport` and
  `allowDelegatedDecrypt`. The panel states that server-managed encryption is
  not end-to-end encryption, that encrypted bodies are not searchable or
  available to AI features, and that every encrypted send needs a key or
  certificate for every recipient.

## Phase 3 server-managed S/MIME

- **CMS library.** `go.mozilla.org/pkcs7` provides CMS SignedData and
  EnvelopedData. Content encryption is pinned to AES-256-CBC; signing uses the
  certificate key (RSA or ECDSA) with SHA-256. OpenSSL interop tests cover both
  signed and enveloped output.
- **Certificate storage.** Migration `0102_encryption_phase3.sql` adds
  certificate metadata to identities and contact certificates. Private keys
  are PEM PKCS#8 wrapped by the same crypto vault as OpenPGP; the database
  never sees unwrapped key material.
- **Import.** `POST /api/encryption/keys/import` accepts `protocol=smime` with
  a PKCS#12/PFX file or PEM certificate, private key and optional chain. PKCS#12
  is decoded with `software.sslmate.com/src/go-pkcs12`. Legacy encrypted PEM
  and encrypted PKCS#8 keys are rejected with a clear instruction.
- **Validation.** Identity and recipient certificates must be within validity,
  contain the address in an email SAN, permit `emailProtection` when an EKU is
  present, and permit signing or key encipherment as appropriate. Chain
  validation uses the system roots plus `ALT8MAIL_SMIME_CA_FILE`; an unknown
  chain is reported as untrusted, not silently trusted. OCSP and CRL revocation
  are enforced according to `ALT8MAIL_SMIME_REVOCATION_MODE` (`off`, `soft` or
  `require`); unknown revocation status is a warning in `soft` mode and an
  invalid certificate in `require` mode.
- **Directory discovery.** Optional LDAP lookup through `ALT8MAIL_SMIME_LDAP_*`
  requires StartTLS or LDAPS outside development, escapes the search filter and
  imports matching `userCertificate;binary` or `userSMIMECertificate;binary`
  values. Manual People import remains available without a directory. The
  directory and CA bundle are deployment-wide settings; per-tenant directory
  credentials are a follow-up.
- **Sending and reading.** The composer can select OpenPGP or S/MIME when policy
  is `both`; sign-then-encrypt is supported. Outbound recipients are validated
  and encrypted-to-self is used for reading the Sent copy. Inbound S/MIME
  ciphertext stays opaque until explicit decrypt; an inner signature is
  verified and mapped to contact trust. Signed-only messages can be verified
  through `POST /api/messages/{id}/verify`.
- **Rotation.** Active and retired S/MIME identities remain available for
  decrypting older messages; importing the same certificate reactivates its
  existing identity instead of duplicating it.

PKCS#11-backed signing and decryption are described in the Phase 5 section.
Direct smart-card access from the browser remains a native-helper follow-up.

## Phase 4 browser-held client-side E2EE

The policy value `encryption.mode=client_side` selects a different trust model.
It is available with `protocol=openpgp`, `smime` or `both`; delegated
decryption is rejected by policy validation.

- **Key generation and storage.** The browser uses the vendored OpenPGP.js
  6.3.2 bundle (`public/vendor/openpgp.min.mjs`, LGPL-3.0-or-later; upstream
  `https://github.com/openpgpjs/openpgpjs`) to generate
  or import a curve25519 key. The private key is stored passphrase-encrypted in
  IndexedDB per browser profile. The passphrase and unwrapped key never leave
  the browser. Export returns the still-encrypted armored private key.
- **Directory.** `POST /api/encryption/client-identity` accepts only a public
  OpenPGP key or S/MIME certificate chain, verifies that it carries the
  requested owned address and that it is currently usable for encryption, then
  stores it with `source='client'`. Alias addresses covered by the same key or
  certificate are stored as separate active rows under the same fingerprint.
  `GET /api/encryption/directory` returns active public material for at most 50
  tenant addresses and for the selected protocol. In client-side mode it filters
  to `source='client'` and skips revoked, expired or unusable material, so a
  leftover server-managed identity can never be used for a message the user
  believes is zero-access.
- **Outbound.** The composer resolves recipient public keys or certificates,
  builds the plaintext MIME entity in the browser, wraps it as PGP/MIME
  `multipart/encrypted` or S/MIME EnvelopedData and submits only the ciphertext
  plus outer headers. Attachments are encrypted inline inside that MIME entity;
  the JSON attachment array must be empty. `POST /api/send` rejects plaintext
  bodies, server-side attachment metadata, non-native accounts and ciphertext
  that does not analyse as the selected protocol's encrypted MIME shape. A
  referenced legacy draft is removed after a successful queue but its plaintext
  is never used for the encrypted message. The outbox stores the ciphertext in
  `outbox.client_mime`; delivery keeps the server-built outer headers
  (From/To/Subject/Message-ID) and replaces the body with that client entity.
  The server never receives a plaintext body in this flow.
- **Inbound and reading.** IMAP sync leaves the ciphertext in the mailbox and
  stores only crypto metadata with an empty searchable body.
  `GET /api/messages/{id}/ciphertext` returns the raw message up to 32 MiB with
  `Cache-Control: no-store`; it refuses synthesized header-only sources and
  truncated ciphertext. For OpenPGP the browser finds the armored message and
  decrypts it. For S/MIME it extracts the CMS EnvelopedData, unwraps the content
  key with the browser-held PKCS#12 key and parses the decrypted MIME. Both
  paths parse text/plain and text/html alternatives, return encrypted
  attachments, verify an inner signature when possible, and render HTML through
  the existing sanitiser. Decrypted bodies are kept in memory only and are
  cleared when the key is locked.
- **Server restrictions.** Client-side mode rejects ordinary plaintext sends;
  `POST /api/send` requires a browser-built OpenPGP/MIME ciphertext. It also
  disables server key generation/import, server-side message decryption, DLP
  body checks and server-side draft saving, including new shared-draft
  content and comments. `POST /api/messages/{id}/decrypt` refuses the mode.
  Existing drafts and shared drafts remain readable and deletable for
  migration, but client-side composers do not save to them. The composer
  suppresses AI/tone tools and server-side drafts. Search can use metadata
  immediately and bodies only after this browser has decrypted them in the
  current session.
- **Plaintext inbound mail is not retroactively encrypted.** A sender who does
  not encrypt still sends ordinary plaintext mail. That message remains
  server-readable and indexable; client-side mode only protects messages that
  the browser encrypts outbound or that arrive as ciphertext for the user's
  published key. Do not describe the whole mailbox as zero-access unless a
  separate browser re-encryption pipeline is deployed.
- **Policy changes.** Switching from `server_managed` to `client_side` does not
  retroactively make old server-encrypted mail readable in the browser. The
  guarded migration workflow requires every active or retired server-held
  private key to be exported by its user first, then deletes those wrapped keys,
  retires the server identities, records the preflight report and switches the
  policy in one transaction. A direct policy PATCH from `server_managed` to
  `client_side` is refused with `MIGRATION_REQUIRED`. A key that the server
  already manages cannot be republished as a browser key; users create a new
  browser key. Browser identities never receive a server-side private key.
  Destructive media sanitisation and historical backup expiry remain the
  operator's retention and key-destruction procedure.
- **Cancellation.** A queued client-side send has no server-side plaintext, so
  Undo/Cancel stops delivery without restoring a draft. The pending queue labels
  the action as Cancel and the toast does not offer an Undo action that would
  produce an empty draft.

### Limits that are part of the design

- Client-side attachments are encrypted inline (at most 12 files, 8 MiB total
  before encoding, 24 MiB encrypted MIME). They are not uploaded to the server.
- Drafts are never persisted server-side in client-side mode. The browser saves
  up to 25 passphrase-protected OpenPGP or CMS-encrypted drafts in IndexedDB;
  losing the browser profile loses those local drafts.
- The server still sees recipients, Subject, timestamps, sizes, folder and
  message flags, IP/user-agent, search queries and delivery metadata. E2EE does
  not hide metadata.
- Inbound plaintext from a sender who did not encrypt is still readable by the
  server. Client-side mode protects browser-encrypted messages, not the entire
  mailbox.
- Server-side FTS, AI summaries, classification/DLP, antivirus inspection,
  translation and server-side push-content generation cannot inspect the body.
  Legal hold/eDiscovery has no plaintext access. Server-side branding footers
  and classification watermarks are not applied to the encrypted body because
  the server cannot modify it; apply equivalent copy in the browser if policy
  requires it.
- Recipient discovery is limited to the tenant directory plus keys the user has
  saved in People (including manually discovered WKD keys). External addresses
  without a known usable key block sending; there is no plaintext fallback.
- Browser keys and certificates are published for every owned send-as address
  whose key UID or certificate SAN covers that address. A key that does not
  carry the alias still cannot be discovered for it.
- The client directory is tenant-only. Browser OpenPGP keys are also published
  through the advanced WKD method at
  `/.well-known/openpgpkey/hu/{hash}` for verified tenant domains. The direct
  `openpgpkey.<domain>` host needs its own DNS record and certificate before
  external clients can use it. S/MIME browser certificates are not published to
  a public directory; external senders need the certificate out-of-band or an
  LDAP/S/MIME directory.
- Connected external mailboxes (for example Gmail or Microsoft 365) are outside
  client-side E2EE: the server syncs and stores their plaintext. Sending from
  those accounts is blocked while the tenant is in client-side mode; use the
  provider's own client for that account.
- Losing the browser profile or forgetting the passphrase loses the private
  key. Recovery requires a user-exported encrypted key backup. Client-side
  accounts may also store a passphrase-protected OpenPGP private key in the
  account backup table; the server stores only the encrypted blob. S/MIME
  recovery uses the exported PKCS#12 bundle. The server has no escrow or
  recovery key and cannot decrypt either form.
- A compromised browser profile, XSS in the webmail origin, or malicious
  JavaScript served by the operator defeats client-side E2EE. The mode protects
  against database theft and routine operator access, not a fully malicious
  application host.
- Notes, tasks, files, calendar entries, signatures, templates, rules and other
  workspace stores are outside the client-side mail envelope. Saving decrypted
  message content into those features stores it on the server in their normal
  form; the UI does not re-encrypt it with the browser key.
- Server-generated mail such as account notifications, calendar invitations,
  unsubscribe requests, reactions and delivery reports is composed on the
  server and is not browser-encrypted. The webmail's own plaintext queue send
  features — automation auto-replies, mail merge, MCP/API v1 plaintext send and
  phishing-campaign delivery — are blocked while client-side mode is active.
- A Sieve vacation responder runs on the mail server, outside the webmail
  queue. It may still send its configured plaintext reply in client-side mode;
  disable any active responder when switching a tenant if that content must not
  leave in plaintext.
- Add-on actions and integrations run outside the browser key envelope. An
  add-on that sends content to a third party, such as a notes or CRM connector,
  transmits that content in the connector's own form; review each add-on's
  privacy and scope before using it with protected mail.
- There is no forward secrecy, no IMAP/DAV access to browser keys and no live
  multi-device key sync. Multi-device transfer uses the passphrase-protected
  account key backup (OpenPGP) or an exported PKCS#12 bundle (S/MIME); each new
  browser must restore the key explicitly.
- A single PGP/MIME ciphertext is encrypted to all To/Cc/Bcc recipients. PGP
  key packets can reveal recipient key IDs, so Bcc is not hidden from the other
  recipients by encryption alone. Avoid Bcc when that relationship is sensitive;
  per-recipient ciphertext delivery is a follow-up.


## Phase 5 hardening (2026-11-16)

Phase 5 closes the migration and browser gaps that made the earlier modes hard
to operate at scale. It is implemented in the same tree and remains disabled by
default through tenant policy.

### Client-side S/MIME

- **Library.** `public/vendor/forge.min.mjs` is node-forge 1.4.3, loaded lazily
  only when the account uses S/MIME. Signing and encryption use CMS SignedData
  and EnvelopedData with SHA-256 and AES-256-CBC, matching the server-managed
  S/MIME output.
- **Import and storage.** The user selects a PKCS#12 bundle and passphrase. The
  browser validates the leaf certificate, email SAN, validity, emailProtection
  EKU and RSA key usage, then re-encodes the bundle under the passphrase before
  storing it in the `smime-identities` IndexedDB store. The server never sees
  the private key or the passphrase. Unlocking parses the stored bundle in
  memory; locking drops it.
- **Publication.** The public certificate and chain are published once per owned
  address through `POST /api/encryption/client-identity` with
  `protocol=smime`. The server verifies the certificate covers the address,
  is currently usable for encryption and is not a server-managed identity, then
  stores a `source='client'` row with certificate metadata and no private key.
- **Composition and reading.** The composer offers OpenPGP or S/MIME when the
  tenant policy enables both. S/MIME attachments are encrypted inline. The
  reader extracts and decrypts the CMS envelope, parses the MIME entity and
  verifies an inner multipart/signed CMS signature, or an attached CMS
  SignedData inside the envelope, against the embedded signer certificate.
  Signature trust is reported as `valid_untrusted` because the
  browser does not run a full enterprise PKI path; the certificate fingerprint
  and signer address are shown. Certificate changes for a recipient are pinned
  in `smime-directory-pins` and require explicit user review.
- **Limits.** S/MIME encryption requires RSA certificates. ECDSA signing is not
  exposed in the browser. S/MIME browser identity backup is an exported PKCS#12
  bundle; there is no server-held recovery copy. The 12-file/8 MiB attachment
  and 24 MiB encrypted-MIME limits apply to both protocols.

### Browser-local encrypted drafts

- OpenPGP drafts are encrypted to the user's own public key with OpenPGP.js.
  S/MIME drafts are CMS-encrypted to the user's own certificate. Both live in
  the `drafts` IndexedDB store, keyed by draft id, with a `protocol` field.
- Draft plaintext never enters IndexedDB. The stores cap at 25 drafts per
  address; the oldest are pruned. Drafts survive a browser restart but not a
  lost browser profile.
- The composer reports "Encrypting draft…", "Encrypted draft saved in this
  browser" or a failure state. Locking the key first flushes the current draft.
- Hydration loads both protocols when either key is unlocked. A draft encrypted
  to a different or damaged key is skipped without blocking the rest.

### Account key backup and recovery

- `POST /api/encryption/key-backup` accepts only a passphrase-protected OpenPGP
  private key whose fingerprint matches the public key and an owned address.
  `GET` returns the encrypted blob with `Cache-Control: no-store`; `DELETE`
  removes it. The server never receives the passphrase or an unwrapped key.
- The webmail settings panel can back up, restore and delete the account backup.
  Restore verifies the fingerprint before replacing the local identity and then
  republishes the public key. This is the supported multi-device transfer path;
  it is not live key synchronization.

### Directory trust, aliases and WKD

- Recipient directory fingerprints are pinned in IndexedDB. A changed
  fingerprint blocks encryption until the user reviews and accepts the old and
  new values; the new value is then pinned. This applies to OpenPGP keys and
  S/MIME certificates.
- Browser OpenPGP keys are published for every owned send-as address covered by
  the key's UIDs. Migration `0104_encryption_client_hardening.sql` changes the
  unique key to `(user_id, protocol, fingerprint, email)`, so one key can have
  alias-specific active rows.
- Advanced WKD is served at
  `/.well-known/openpgpkey/hu/{z-base-32 SHA-1 local-part hash}?l={localpart}`.
  The direct method is served at
  `/.well-known/openpgpkey/{domain}/hu/{hash}` for the
  `openpgpkey.<domain>` host once DNS and its certificate are configured. Only
  active `source='client'` keys from verified manual/verified mail domains are
  published. Nginx passes `/.well-known/openpgpkey/` to the Go server; the
  direct host name is not added to certificates automatically.

### OCSP and CRL revocation

- `ALT8MAIL_SMIME_REVOCATION_MODE` is `off` (default), `soft` or `require`.
  `ALT8MAIL_SMIME_REVOCATION_TIMEOUT_SECONDS` defaults to 5,
  `ALT8MAIL_SMIME_REVOCATION_CACHE_SECONDS` to 300, and
  `ALT8MAIL_SMIME_REVOCATION_ALLOW_PRIVATE` to false.
- The validator tries OCSP first, then a CRL from the certificate's CRL
  distribution points. Redirects are rejected and private/loopback targets are
  refused unless explicitly allowed. Responses are capped at 8 MiB and cached.
- A revoked certificate is always invalid. In `soft` mode an unreachable or
  unknown status adds a warning; in `require` mode it invalidates the
  certificate. Identity import, contact certificate storage, recipient
  validation and identity readiness all use the same check.

### PKCS#11 / HSM S/MIME

- Build the server with `-tags pkcs11` and `CGO_ENABLED=1` to compile the
  token backend (`github.com/miekg/pkcs11`). Default builds include a stub and
  report that PKCS#11 is unavailable.
- Configure one or more identities as JSON in
  `ALT8MAIL_SMIME_PKCS11_IDENTITIES`:

```json
{
  "module": "/usr/lib/softhsm/libsofthsm2.so",
  "tokenLabel": "alt8mail-smime",
  "pin": "token-pin",
  "identities": [{
    "email": "ceo@example.com",
    "certificatePem": "-----BEGIN CERTIFICATE-----\n...",
    "chainPem": "",
    "keyLabel": "ceo-signing-key",
    "keyId": "",
    "pin": ""
  }]
}
```

- The server matches the configured certificate to the email SAN, performs a
  startup sign/verify self-test against the certificate public key, keeps the
  private key inside the token, and implements both `crypto.Signer` and
  `crypto.Decrypter` for CMS. Signing uses `CKM_RSA_PKCS` over a SHA-256
  DigestInfo; decryption unwraps the CMS content key and decrypts AES-CBC or
  AES-GCM content. The HSM identity is used only in `server_managed` mode.
- A user imports the matching public certificate (certificate only, no private
  key). The server verifies it matches the configured token key and stores a
  cert-only identity row. `loadActiveSMIMEIdentity` and the decryption identity
  list fall back to the token; a revoked or mismatched row is not used.
- Smart-card S/MIME in webmail still needs a native PCSC/PKCS#11 helper on the
  user's device. The supported server-side path is an operator-configured
  token-backed identity; no browser API can reach a smart card directly.

### Guarded migration from server-managed to client-side

- `GET /admin/api/policies/encryption-migration` returns the preflight report:
  users, server-held private-key identities, export status, encrypted message
  count, active legal holds, retention, delegated-decrypt state, blockers and
  warnings.
- `POST /api/encryption/keys/{id}/export` requires the user's current sign-in
  password, is audited, sets `Cache-Control: no-store`, and records an export
  row `(user_id, protocol, fingerprint)`. OpenPGP returns the still-encrypted
  armored private key. S/MIME requires an export passphrase of at least eight
  characters and returns a PKCS#12 bundle. Webmail exposes this under
  Settings → Security → Private keys for migration for every active and retired
  server-held identity.
- `POST /admin/api/policies/encryption-migration` requires an administrator
  password and the exact confirmation `migrate to client-side`. It rechecks the
  exports under a tenant row lock, refuses if any private key lacks an export,
  requires explicit acknowledgement when active legal holds exist, then deletes
  all wrapped private keys for the tenant, retires server-managed identities,
  switches `encryption.mode` to `client_side`, disables delegated decryption and
  records `tenant_encryption_migrations` with the report and deleted-key count.
- A direct `PATCH /admin/api/policies` from `server_managed` to `client_side` is
  refused with `MIGRATION_REQUIRED`, so an operator cannot delete keys by
  changing the mode in the ordinary form.
- Migration `0105_encryption_migration.sql` adds `user_crypto_key_exports`,
  `tenant_encryption_migrations` and a trigger that rejects new server-held
  private keys for a tenant already in `client_side` mode. Both 0104 and 0105
  were rolled back and reapplied on a cloned PostgreSQL database during
  validation.

### Configuration summary added in Phase 5

| Variable | Default | Purpose |
| --- | --- | --- |
| `ALT8MAIL_SMIME_REVOCATION_MODE` | `off` | `off`, `soft` or `require` OCSP/CRL enforcement |
| `ALT8MAIL_SMIME_REVOCATION_TIMEOUT_SECONDS` | `5` | Per-request revocation timeout |
| `ALT8MAIL_SMIME_REVOCATION_CACHE_SECONDS` | `300` | Revocation result cache lifetime |
| `ALT8MAIL_SMIME_REVOCATION_ALLOW_PRIVATE` | `false` | Allow private/loopback OCSP or CRL targets |
| `ALT8MAIL_SMIME_PKCS11_IDENTITIES` | empty | JSON token/identity configuration for HSM S/MIME |

### Test coverage added in Phase 5

- Browser E2E: encrypted inline attachments and reader download; browser-local
  OpenPGP and S/MIME drafts; directory fingerprint change/review; alias UID
  generation; S/MIME PKCS#12 import, CMS encrypt/decrypt, signature
  verification, publication per alias and S/MIME composer send.
- Go integration: browser key publication, alias rows, WKD advanced/direct,
  encrypted account backup, client S/MIME certificate publication and directory
  filtering, guarded migration preflight/export/switch, PKCS#11 signer and
  decrypter adapters, OCSP/CRL modes and PKCS#12 export round-trip.
