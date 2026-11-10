# Phase 5 Acceptance Tracker

Status: `done`, `in progress`, `planned`, or `operator` (requires live external
systems or credentials).

## Workstreams

| Workstream | Status | Evidence |
| --- | --- | --- |
| 5A Advanced DLP | done | Credit-card/IBAN/national-ID/regex/dictionary detectors, masked findings, prevent-forward, watermarking, simulation and false-positive review; unit and PostgreSQL tests |
| 5B eDiscovery and retention | done | Cross-domain discovery, signed reproducible JSON/MBOX export with manifest, reviewer queue, file holds, classification retention for mail/Cabinet, hold overrides |
| 5C BYOK and tenant encryption | done | Versioned envelope keys, enable/unlock/lock/rotate, tenant-sealed AI credentials, no-fallback lock error and tenant isolation; see limits for non-secret blobs |
| 5D Files governance and OCR | done | File holds, share approvals, DLP on share, download watermarks, file/version retention and OCR/file text index with search |
| 5E Audit and SIEM | done | Signed audit bundles, reproducibility test, Splunk/Sentinel/Elastic starter packs |

## Release acceptance checklist

| Criterion | Status | Evidence / action |
| --- | --- | --- |
| DLP accuracy corpus meets agreed thresholds | operator | Detector unit corpus is committed; run the labeled customer corpus against the configured provider before release |
| OCR path handles scanned PDFs/images | operator | `A8M_OCR_COMMAND` sidecar is implemented and fixture-tested; install/validate the real OCR runtime and scanned-PDF fixtures |
| eDiscovery export reproduces a case set with manifest and audit trail | done | `TestPhase5LegalGovernanceIntegration`: two exports have the same `contentSha256`, manifest/signature present, MBOX headers, stored export rows and audit events |
| BYOK rotation and recovery drills pass; no mailbox data readable without tenant key | in progress | Secret encryption and rotation are tested; mailbox rows/Office/Cabinet blobs still rely on infrastructure encryption and client-side Cabinet encryption is not shipped |
| Legal hold blocks deletion across mail, files and documents | done | Mail hold retention skip + `TestPhase5LegalGovernanceIntegration` file-hold deletion/retention denial; documents are Cabinet files |
| Share-link expiry and approval | done | Existing share expiry plus `file_share_requests` approval flow and tests |
| Audit event completeness and tamper evidence | done | Signed bundle test covers user/admin events and stable content hash |
| No release/deploy/push without authorization | done | Work remains local on `main` |

## Operator / follow-up risks

- **BYOK scope:** application-level encryption covers tenant secrets (AI keys)
  in this slice. Mailbox rows, Office binaries and Cabinet blobs require
  PostgreSQL/storage-at-rest encryption. Client-side encrypted Cabinet folders
  are not implemented. This is the largest remaining gap against the Phase 5
  release gate.
- **OCR:** a real Tesseract/OCRMypdf-style sidecar must be installed and tested
  against scanned PDFs/images; only deterministic fixture coverage is committed.
- **DLP attachment scanning:** detectors run on message subject/body and
  attachment names; OCR text is indexed for search but is not yet fed into the
  send-time DLP engine.
- **Watermarking:** mail gets a visible footer and downloads get a watermark
  header; binary document watermarking is not embedded.
- **Data residency:** the region code is recorded in tenant policy but does not
  itself relocate storage; deployment/storage configuration must enforce it.
- **eDiscovery/file binaries:** case JSON/MBOX includes mail bodies and file
  metadata, not Cabinet file bytes. A binary export/production format is
  follow-up work.
- **SIEM:** dashboards are starter queries; validate field extraction against
  the customer's pipeline and signing-secret custody.
