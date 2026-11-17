# Phase 3 Implementation Notes

Phase 3 adds scheduling, follow-up, merge, booking, template/payment and
deliverability workflows behind paid entitlements.

## 3A Recipient-time scheduling and undo

- Tenant mail policy now carries `undoSendSeconds` (5–60; default 8),
  `recipientWindowStartHour` and `recipientWindowEndHour`.
- Send clamps the requested undo hold to the tenant maximum; preference updates
  are validated against the same limit. The webmail exposes 5–60 second
  options and clamps cached preferences after bootstrap.
- Recipient suggestions use the recipient's observed UTC offset and the
  tenant's working-hour window, preferring the next weekday slot. Unknown
  timezones fall back to the user's local working hours.
- Tests: `send_time_test.go` covers weekday rollover, offset conversion,
  after-hours rollover and weekends.

## 3B Follow-ups

Migration `0070_follow_ups.sql`, `followups.go`, and reading-pane controls.

- `awaiting_reply` nudges check for an inbound reply in the same thread after
  the follow-up was created; replies complete the follow-up without notifying.
- `remind_me` reminders notify at the due time.
- A worker runs every minute, claims pending rows atomically, suppresses
  duplicate notifications and never sends mail.
- Tests: reply resolution, duplicate suppression and notification counts in
  `TestFollowUpIntegration`.

## 3C Native mail merge

Migration `0071_mail_merge.sql`, `mail_merge.go`, compose merge dialog.

- CSV or JSON recipients, header-row parsing, per-recipient variables,
  duplicate elimination, suppression list and automatic unsubscribe footer.
- Merge preview, throttled outbox scheduling (`1–60s`), campaign report from
  recipient/outbox status and public unsubscribe page.
- The compose merge dialog previews up to three rendered messages, then creates
  and queues the merge. Recipient cap is 500; the acceptance test uses 100.
- Tests: `TestMailMergeIntegration` covers 100 recipients, dedupe, variable
  substitution, suppression, 2-second throttle spacing, unsubscribe and
  template rendering.

## 3D Booking and availability

Migration `0072_booking_pages.sql`, `booking.go`, calendar booking-page modal.

- Public `/book/{slug}` page and `/api/public/booking/{slug}/slots` expose only
  open slots and labels, never event details.
- Working hours, 30-minute grid, duration, buffers, weekdays, IANA timezone,
  weekday rollover and team round-robin.
- Booking uses a transaction-scoped advisory lock and re-checks calendar
  events/bookings before insert; concurrent attempts get one confirmation and
  one `BOOKING_TAKEN`.
- Calendar free/busy endpoint returns only `start`/`end` intervals for internal
  colleagues; resource utilization reports bookings/booked minutes. External
  ICS feeds and CalDAV `free-busy-query` REPORT federation are supported; the
  latter sends request-scoped Basic credentials and stores nothing.
- Booking pages support edit, disable/enable, booking list and cancellation;
  cancelling a booking also cancels its calendar event.
- Resources can require an approver. Pending resource requests notify the
  approver and are approved/denied from the calendar resource dialog.
- Tests: `TestBookingIntegration`, `TestPhaseScopeCompletionIntegration` and
  `TestCalDAVFreeBusyParsing` cover slot privacy, DST change, duplicate
  booking rejection, booking cancellation, resource approval, ICS/CalDAV
  parsing and booking counts.

## 3E Template gallery and payment links

Migration `0073_template_gallery.sql`, `gallery.go`, template manager gallery.

- Packaged intro, invoice reminder, contract follow-up and meeting recap
  templates can be installed into a team template library.
- Tenant mail policy controls payment-link insertion and a host allowlist.
  Payment URLs must be HTTPS and match an allowed host/subdomain; every
  insertion is audited. No payment processing or card data touches Alt8Mail.
- Tests: `TestGalleryAndPaymentIntegration` covers install, duplicate install,
  accepted and blocked payment hosts.
- Compose has **Template**, **Merge**, **Payment link** and **Jitsi** actions.

## 3F Deliverability

Migration `0074_deliverability.sql`, `dmarc.go`, `campaign_tracking.go`.

- DMARC aggregate XML, gzip and ZIP ingestion deduplicates by
  tenant/domain/report/org, stores per-record source/disposition/DKIM/SPF
  results and exposes a 30-day summary with failing sources and sub-90% alerts.
- The Domains page shows the summary and configures a scheduled rua mailbox
  poller (`dmarc_pollers`) with a per-tenant folder cursor. Manual ingestion is
  still available at `POST /admin/api/dmarc/ingest`.
- Campaign tracking is opt-in via tenant policy. When enabled, mail-merge HTML
  links are wrapped through per-campaign tokens and a transparent open pixel is
  appended. Open/click events hash the visitor IP; reports are available at
  `/api/campaign-tracking`. When the policy is off, no tracking records or links
  are created (covered by the merge test).
- No silent tracking: the merge HTML carries an unsubscribe footer; the tenant
  policy must be explicitly enabled.

## Verification

- `go test ./...`, `go vet ./...`, `go build ./...`
- PostgreSQL `internal/httpapi` suite with Phase 3 DSN tests
- `npm run test:all`
- `npm run test:a11y`
- Phase 3 migrations `0070`–`0074` rollback/reapply cycle

## Known limits

- Campaign tracking applies to mail merges, not every outbound message.
- Classified outbound HTML carries an embedded body watermark (banner and
  repeated mark); text and HTML Cabinet downloads carry embedded banners.
  PDF/image watermarking remains a rendering-library integration.
- CalDAV federation requires an HTTPS calendar collection URL; provider-specific
  OAuth discovery remains an operator integration detail.
