# Phase 5 Implementation Notes

Phase 5 adds compliance and data-governance controls: advanced DLP detectors,
legal hold and retention across mail and Cabinet files, signed eDiscovery and
audit exports, a review queue, BYOK envelope keys, an OCR/file text index and
SIEM export packs. Migrations: `0076_governance.sql`, `0077_legal_review.sql`,
`0078_audit_exports.sql`.

## 5A Advanced DLP

- DLP rules now support detectors: `terms`, `dictionary` (word boundaries),
  `regex`, `credit_card` (Luhn), `iban` (mod-97) and `national_id`
  (US SSN / UK NINO heuristics).
- Detector findings are masked (`•••• 1234` or `[redacted]`) before they appear
  in warnings, matches, policy simulation, AI policy assist or the provider
  payload. Raw card/IBAN/ID values are never returned.
- Tenant mail policy can prevent forwarding Confidential or Restricted mail and
  append a visible classification/sender/timestamp watermark to classified
  outbound messages.
- System Control exposes a detector editor and `POST /admin/api/dlp/simulate`
  returns the effective classification, warnings, detector names, blocked flag
  and a redacted preview. `dlp_false_positives` stores review items with
  confirmed/false-positive decisions.
- Tests: `TestDLPDetectorCorpus`, `TestDLPDetectorPolicyDoesNotLeakValues`,
  `TestPhase5AdvancedDLPIntegration` and the forward-block policy test.

## 5B eDiscovery and retention

- Legal holds exist for mail (`mail_legal_holds`) and Cabinet files
  (`file_legal_holds`). A hold can be tenant-, user- or file-scoped and blocks
  file deletion and file/version retention. Mail retention already skips
  tenant/user holds.
- Mail retention now honours `mail.retentionByClassification`: the IMAP
  candidate set is filtered by each message's synchronized classification and
  class-specific window. Deletion still uses UIDPLUS and skips Drafts/Junk/Trash.
- Cabinet retention supports global and per-classification windows and Office
  version age retention. Each deletion/version purge is counted and held files
  are skipped with a `skippedHolds` count.
- eDiscovery offers mail search/CSV (existing), Office metadata CSV and a new
  cross-domain `/legal/discovery/all` result set. `GET /legal/export` produces
  reproducible JSON or MBOX case exports (mail bodies + file/document metadata)
  with a SHA-256 content hash, HMAC signature, stored `discovery_exports` row
  and audit event. MBOX manifests and signatures are sent in headers.
- A reviewer queue (`discovery_review_items`) supports pending/approved/rejected
  decisions with reviewer identity and notes. `legal.review` is a distinct
  permission.
- Tests: `TestPhase5LegalGovernanceIntegration` covers file-hold deletion
  denial, retention hold override, share approval, DLP share blocking,
  cross-domain search, reproducible signed JSON/MBOX export and review queue.
- Annual/legal-hold override is always checked before deletion; mail legal holds
  are still enforced by the retention worker.

## 5C BYOK and tenant encryption

- `tenant_encryption_keys` stores versioned wrapped data-encryption keys
  (AES-256-GCM envelope wrapping with a customer-supplied 32-byte KEK).
  System Control can enable, unlock, lock and rotate the tenant key, and shows
  fingerprints/versions but never key material.
- When BYOK is enabled, tenant AI provider keys are sealed with the tenant DEK
  (`byok:v1:<version>:…`). A locked key returns an explicit error; there is no
  silent fallback to the platform key. Legacy workspace-sealed values remain
  readable until rotated.
- Unlocking loads all active/retired DEK versions for the current process
  (30-minute cache). Locking clears them. Rotation writes a new active version
  and retires the previous one; old ciphertexts remain decryptable after an
  unlock.
- Tests: `TestPhase5BYOKIntegration` covers enable, sealing, locked error,
  wrong-key rejection, unlock, rotation, v2 sealing, isolation and key-material
  non-disclosure.
- Known limit: BYOK covers tenant secrets (AI credentials in this slice).
  Application-level encryption of mailbox rows, Office binaries and Cabinet
  blobs, and client-side encrypted Cabinet folders, are not implemented here;
  use PostgreSQL/storage-at-rest encryption for those layers. See the lost-key
  runbook note below.

## 5D Files governance, OCR and file search

- File holds, share approval (`file_share_requests`), DLP-on-share and
  classified-download watermark headers are enforced in the Cabinet file paths.
  External shares that require approval return `202` with a pending request;
  after an administrator approves, the owner retries and receives the link.
- `file_text_index` stores extracted text per file. Text extraction supports
  Office/text formats through `office.ExtractText`; images and scanned PDFs use
  an operator-configured OCR sidecar command (`A8M_OCR_COMMAND`, with `{}`
  replaced by a temporary file path). When OCR is not configured the index is
  recorded as `unavailable` rather than failing the file.
- `POST /api/files/{id}/index` and `GET /api/files/search?q=` expose index and
  search to the owning user. Search uses PostgreSQL FTS plus ILIKE fallback and
  returns excerpts with OCR provenance.
- Tests: `TestPhase5FileTextIndexIntegration` covers text extraction, search,
  OCR sidecar indexing and provenance.
- Known limit: the OCR sidecar is operator-provided; the committed tests use a
  deterministic fake command, so real scanned-PDF accuracy is an operator gate.
  DLP currently scans message/attachment names and Office text, not arbitrary
  attachment binaries.

## 5E Audit and SIEM

- Existing per-tenant SIEM webhooks remain. `GET /admin/api/audit/export`
  produces a signed JSON bundle of user and admin audit events with a canonical
  event array, SHA-256 content hash, HMAC manifest signature, range filters and
  a stored `audit_exports` row.
- `docs/siem/` contains Splunk, Sentinel and Elastic starter mappings/alert
  queries and export-verification steps.
- Tests: `TestPhase5AuditExportIntegration` verifies event completeness, both
  audit sources, signature presence and content-hash reproducibility for a
  fixed range.
- Known limit: tamper evidence is the manifest HMAC plus a deployment-held
  signing secret; there is no external timestamp authority or append-only
  remote anchor.

## Verification

- `go test ./...`, `go vet ./...`, `go build ./...`
- PostgreSQL `internal/httpapi` suite with Phase 5 DSN tests
- `npm run test:all`
- `npm run test:a11y`
- Phase 5 migrations `0076`–`0078` rollback/reapply cycle

## Lost-key runbook

1. If the customer key is lost, stop unlock attempts and do not rotate.
2. Restore the key from the customer's key-ceremony escrow.
3. If escrow is unavailable, tenant secrets sealed with BYOK cannot be
   recovered. The application has no vendor-key fallback. Restore the tenant
   from a pre-BYK back up or re-enter provider credentials with a new key
   ceremony, then rotate.
4. Record the incident and all unlock/rotation/lock audit events.
