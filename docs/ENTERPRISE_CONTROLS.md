# Enterprise controls

Alt8Mail 0.12 adds policy-linked classification, legal preservation and
eDiscovery, one-click offboarding, signed SIEM audit delivery and tenant
branding. The customer console exposes these under **Policies → Classification
& DLP**, **Compliance** and **Integrations**. All operations are written to the
administrator audit log.

## Classification and DLP

Classification labels are `Public`, `Internal`, `Confidential` and `Restricted`.
The organization policy sets the default label and up to 30 rules. A rule scans
the subject, body and attachment names for up to 20 case-insensitive terms and
can:

- **Label only** — raise the message classification.
- **Warn sender** — show the match and ask the sender to confirm.
- **Block send** — reject the send with HTTP `422 DLP_BLOCKED`.

The webmail composer calls `POST /api/dlp/check` before sending so the sender
sees the policy result in place. The send endpoint evaluates the same policy
again; a direct API caller cannot bypass it. Scheduled messages are re-checked
when edited and again immediately before delivery, so a policy change made
after scheduling returns the message to Drafts instead of sending it.
Classification is stored on drafts, queued sends and the Sent copy.

## Legal hold

A hold is either tenant-wide or scoped to one mailbox. While a hold is active:

- API message deletion and folder emptying return `409 LEGAL_HOLD`.
- Tenant retention skips the held mailbox and reports it under `skippedHolds`
  in the retention result.
- Mailbox data stays available to administrators and eDiscovery.

Release the hold from **Compliance** only when the matter is formally closed.
Creating and releasing a hold is audited.

## eDiscovery

**Compliance → eDiscovery search** searches every mailbox in the tenant by
keyword, mailbox, date range, folder and classification. The archive export
returns a UTF-8 CSV with a BOM for spreadsheet applications and is capped at
5,000 messages per export. The endpoint is `GET /api/legal/discovery` and the
CSV variant is `GET /api/legal/discovery.csv`.

Permissions:

- `legal.read` — view holds, search and export.
- `legal.manage` — place and release holds.

## One-click offboarding

**Users → [user] → Offboard** performs the following in one workflow:

1. Revokes webmail, admin, trusted-device and challenge sessions, and rotates the
   mailbox credential so the departing user's IMAP/POP password stops working.
2. Suspends the account and marks it closed.
3. Optionally grants a coworker read and send access to the mailbox.
4. Optionally places a legal hold before the account is closed.
5. Optionally enables an automatic reply.

The job is recorded in `user_offboarding_jobs` and shown on the Compliance page.
Mail data is never deleted by the workflow; retention and legal hold continue to
govern it.

## SIEM audit webhooks

Configure **Integrations → SIEM webhook** with an HTTPS endpoint on port 443.
The endpoint must resolve to a public address; private, loopback and redirecting
targets are rejected. Alt8Mail seals the signing secret with AES-GCM and shows
the plaintext only once when it is created or rotated.

Each event is a `POST` with a JSON body and these headers:

| Header | Meaning |
| --- | --- |
| `X-A8M-Delivery` | Unique delivery identifier |
| `X-A8M-Source` | `user`, `admin` or `test` |
| `X-A8M-Timestamp` | Unix timestamp in seconds |
| `X-A8M-Signature` | `sha256=<hex HMAC>` |

The signed message is `<timestamp>.<raw request body>` using HMAC-SHA256 and the
tenant signing secret. Verify with a constant-time comparison and reject stale
timestamps. A `2xx` response marks the event delivered. Failures retry with
backoff and are visible in the delivery table and audit log. Delivery IDs are
unique per `(tenant, source, audit id)` so receivers can deduplicate redelivery
attempts.

The worker starts from the current audit cursor when the integration is first
configured, so enabling SIEM does not replay the entire historical audit log.
Use eDiscovery for historical mail searches.

## Tenant branding

Set **Organization settings → Branding** or **Policies → Branding**:

- Product name and primary color apply to the public sign-in screen and the
  authenticated webmail shell.
- Login message appears on the sign-in card.
- Support email appears as a contact link on sign-in.
- A custom product name or support address adds a small footer to outbound mail.

The public branding endpoint is `GET /api/branding`; it resolves the request
host against the tenant's mail domains and returns only the four public
branding fields. The default Alt8Mail identity does not add an email footer.
