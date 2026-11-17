# Calendar invitations (iTIP/iMIP)

Alt8Mail sends Google-style meeting invitations that work with Gmail, Google
Calendar, Outlook/Microsoft 365, Apple Calendar, Thunderbird and any client that
implements iTIP over email (RFC 5546/6047).

## What is sent

Each guest receives a separate message so every RSVP link is bound to that
guest. The message is a three-part MIME tree:

1. `text/plain` with the meeting details and Yes/Maybe/No URLs.
2. `text/html` with the same details and three signed response buttons.
3. `text/calendar; method=REQUEST` containing one `VEVENT`.

The `method` parameter on the MIME part and the `METHOD` property inside the
iCalendar object are always identical. The calendar object keeps a stable
`UID`, increments `SEQUENCE` on every update, refreshes `DTSTAMP`, names the
organizer and sets `PARTSTAT`/`RSVP` on each attendee. Organizers are `CHAIR`
with `RSVP=FALSE`; guests are `REQ-PARTICIPANT` with `RSVP=TRUE`.

Timed events use the event's IANA time zone:

```
DTSTART;TZID=America/New_York:20261104T090000
```

The VCALENDAR also carries a matching `VTIMEZONE` with the relevant offset
changes. UTC events keep the conventional `...Z` form. This prevents Outlook
and Apple Calendar from treating the meeting as floating time.

## Stable identity and sequencing

- Creating an event assigns the UID once. Updates never regenerate it.
- Every organizer edit increments `calendar_events.sequence` and sends a new
  `METHOD:REQUEST`.
- An edit resets guest answers to `NEEDS-ACTION`; the previous response
  sequence is retained so the organizer can see that the guest answered an
  older version.
- Deleting an event increments `SEQUENCE` first, then sends
  `METHOD:CANCEL` with `STATUS:CANCELLED` and `RSVP=FALSE` before the local row
  is removed.
- Invitation delivery is held for the account's undo-send window. The event
  dialog exposes **Undo**; cancelling the queued job prevents every invitation
  from leaving the server. A later edit supersedes an older queued job.

## Signed RSVP links

The email buttons open:

```
GET /calendar/rsvp/{token}
```

The token is an HMAC-SHA256-signed payload containing the event ID, stable
event UID, attendee email, invitation sequence and expiry. Tokens are:
- bound to one attendee and one sequence;
- rejected after expiry;
- rejected after the organizer changes the event (the page explains that the
  link is out of date);
- derived from the instance session/CSRF secrets, so no new deployment secret
  is required.

The GET page is read-only and safe for mail scanners. The guest selects
Yes/Maybe/No and the form POSTs to the same signed URL. A successful response
updates `calendar_attendees`, records an audit entry, writes a
`calendar_invite_replies` row and refreshes the organizer's calendar in real
time. Public pages are `no-store`, `noindex`, framed-deny, and served with a
strict CSP.

Only tokenized RSVP URLs exist. There is no UID-only public calendar or
response endpoint.

## Inbound replies (Path A)

The IMAP fetch path captures every `text/calendar`, `application/ics` and
`.ics` body part, including its MIME `method` parameter. When a message carries
`METHOD:REPLY`:

1. The UID must match an event organized by the signed-in mailbox.
2. The sender's `From` address must match the attendee row on that event.
3. The ICS organizer, when present, must match the event organizer.
4. A reply with an older `SEQUENCE` than the current invitation is recorded as
   stale and does not overwrite the current answer.
5. A reply at the current sequence updates `PARTSTAT`, `last_response_sequence`
   and `last_response_at`, records the reply and notifies the organizer.

Google Calendar can also deliver a proposed time as a `METHOD:REQUEST` whose
`DTSTART` differs from the organizer's event. Alt8Mail treats that as a counter
proposal when the sender is an attendee on the event, and ignores a REQUEST
whose time is identical (an invitation echo). The event's current `SEQUENCE`
is authoritative when recording a proposal; a client that omits or carries an
older sequence in the counter cannot make the proposal stale.

`calendar_invite_replies` uses a dedup key over the Message-ID, UID, attendee,
sequence and response so an IMAP rescan cannot apply the same reply twice.

## Reader card, calendar dock and external invites

- Every message carrying a `text/calendar` part is marked with a calendar badge
  in the list. The reader renders the parsed invite/response/proposal card
  (title, date tile, current vs proposed time, guest states, RSVP or
  accept/decline actions) immediately below the message header. The raw
  `invite.ics` attachment is folded into the card for iTIP messages.
- Pending guest proposals appear in a floating dock above the workspace. The
  organizer can accept or keep the original time without opening the source
  email; accepting updates the event and notifies every guest.
- A received invitation from an external organizer offers **Add to Calendar**.
  The import path stores the event and guests in the signed-in user's default
  calendar; later RSVP actions update the local copy and send `METHOD:REPLY`
  to the external organizer.

When an Alt8Mail user is invited by an external organizer, accepting, declining
or marking Maybe also sends a `METHOD:REPLY` message back to that organizer.
Local organizers see the row update directly.

## Proposed times (`METHOD:COUNTER`)

Guests can propose another time from the signed RSVP page or from the webmail
event dialog. The proposal is stored in `calendar_meeting_proposals` and the
guest is moved to `TENTATIVE` for the current sequence.

- An Alt8Mail guest proposing a time to an external organizer sends a
  `text/calendar; method=COUNTER` message with the same stable UID, current
  `SEQUENCE`, the attendee as `PARTSTAT=TENTATIVE`, and the proposed
  `DTSTART`/`DTEND`.
- An external client's `METHOD:COUNTER` (and the REQUEST-shaped proposals some
  clients send) is parsed on inbound sync. The sender must match an attendee,
  the ICS organizer must match the local organizer and the proposed time must
  differ from the event on record. The event's current sequence is used for the
  proposal, so a client-side sequence mismatch does not drop the request.
- The organizer guest panel lists proposed times with the guest note. Accepting
  moves the event (or creates a recurrence exception when the counter names an
  occurrence), bumps `SEQUENCE` and sends updated invitations. Declining keeps
  the original time and emails the guest a plain decision notice.
- The signed-link path cannot send mail as an external guest; it writes the
  proposal directly and notifies the organizer in-app.

## Recurrence exceptions (`RECURRENCE-ID`)

`calendar_event_exceptions` stores one override per original occurrence. The
event dialog opens a recurring occurrence in **This event** scope by default;
switching to **All events** applies the change to the series.

- Exception rows carry their own `RECURRENCE-ID`, replacement
  `DTSTART`/`DTEND`, title/location and `SEQUENCE`.
- The invitation/feed VCALENDAR contains the master `VEVENT` plus each
  exception `VEVENT` with the same UID.
- Cancelling one occurrence writes a cancelled exception with
  `STATUS:CANCELLED`; the master series remains.
- Every occurrence edit advances the master sequence and resets non-organizer
  guest responses, so clients and the guest panel never confuse an old answer
  with the new time.

## Delegated invitations

Events on somebody else's shared calendar record the calendar owner as
`ORGANIZER` and the acting delegate as `SENDER`. Invitations use the delegate's
DKIM-aligned mailbox for delivery, emit the iCalendar `SENDER` property and
show "Sent by … on behalf of …" in the email body and guest panel.

## Localized email, client reminders and feeds

- Invitation and cancellation emails are localized into the sender's interface
  language (English, German, French, Spanish, Portuguese, Dutch, Italian and
  Arabic, including RTL HTML for Arabic). Subjects include the event date.
- Every timed `VEVENT` carries a `VALARM` when the event has a reminder;
  exceptions inherit or override the reminder.
- If the event's calendar has a published webcal feed, the VCALENDAR gets a
  `SOURCE;VALUE=URI` property and the email body gets a **Subscribe to this
  calendar** link. Google Calendar, Apple Calendar and Outlook can then
  cross-sync the same collection.

## Meeting polls (Find a time)

Polls collect availability from external guests without accounts.

- `POST /api/calendar/polls` creates 1–20 candidate slots and up to 50 invitees.
- Each invitee receives a personal signed link at `/calendar/poll/{token}`.
- The guest marks Yes/Maybe/No for every slot; responses can be updated until
  the organizer closes the poll.
- The calendar **Polls** center shows aggregate tallies. Scheduling a slot
  creates a normal event, sends standard iTIP invitations to the invitees and
  marks the poll scheduled.
- Links are HMAC-signed, attendee-bound, expiring, and rejected after the poll
  closes or is deleted.

## RSVP analytics

For events with more than 10 guests, the event payload includes
`rsvpAnalytics`:

- overall response rate and responded count;
- delivered/failed invitation count (per-attendee delivery state is recorded
  when the SMTP submission succeeds or fails);
- average time from invitation to response;
- the same response/delivery statistics grouped by recipient domain.

The organizer guest panel renders this as a compact analytics strip and a
per-domain disclosure.

## Delegated organizer

When a delegate creates or edits an event on somebody else's shared calendar,
the calendar owner is `ORGANIZER` and the acting user is `SENDER`.

- Invitation email `From` is the organizer (DKIM-aligned with `ORGANIZER`).
- The delegate is emitted in the iCalendar `SENDER` property and the email
  `Sender` header, and the body says “Sent by … on behalf of …”.
- Both the organizer and the delegate see the event and its guest status via the
  shared calendar.
- The server refuses to send an invitation when `ORGANIZER` is not a local,
  active mailbox, preventing a phishing-looking From/Organizer mismatch.

## Rooms and resources

- Every account owns its own rooms and equipment. The webmail event dialog
  offers **Add a room or resource**; the row is saved to that account and is
  not visible to other users in the tenant.
- Early releases seeded tenant-wide defaults; migration
  `0043_calendar_user_resources.sql` deactivates those unowned rows.
- Booked resources are stored in `calendar_event_resources` and emitted as
  `ATTENDEE;CUTYPE=RESOURCE` plus a `RESOURCES:` property.
- Resource addresses never receive SMTP invitations.
- Booking conflicts are rejected with `RESOURCE_BUSY`. Per-resource
  `pg_advisory_xact_lock` serializes the conflict check and insert, preventing a
  double-booking race.
- The event dialog exposes a room/resource picker; conflicts surface as an
  inline API error.

## Organizer view

The event dialog has a **Guest responses** panel for owners and calendar
editors. It shows:

- counts for Accepted / Maybe / Declined / No response;
- a response chip per guest;
- **Awaiting response on the updated time** when a guest answered an earlier
  sequence;
- **Resend to N waiting**, which sends the current sequence only to guests who
  have not responded to it.

## API

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/api/calendar/events` | Create event and queue invitations |
| `PATCH` | `/api/calendar/events/{id}` | Update event, increment sequence, reset guest answers |
| `DELETE` | `/api/calendar/events/{id}` | Bump sequence and send cancellation |
| `POST` | `/api/calendar/events/{id}/rsvp` | Signed-in guest response |
| `POST` | `/api/calendar/events/{id}/resend` | Resend current sequence to non-responders |
| `DELETE` | `/api/calendar/events/{id}/invitations` | Undo queued invitations |
| `POST` | `/api/calendar/events/{id}/counter` | Signed-in guest proposes a new time |
| `POST` | `/api/calendar/events/{id}/proposals/{proposalId}/accept` | Organizer accepts a proposed time |
| `POST` | `/api/calendar/events/{id}/proposals/{proposalId}/decline` | Organizer declines a proposed time |
| `PATCH` | `/api/calendar/events/{id}` with `scope=occurrence` | Edit one recurrence occurrence |
| `DELETE` | `/api/calendar/events/{id}?scope=occurrence&occurrenceDate=...` | Cancel one occurrence |
| `GET`/`POST` | `/calendar/rsvp/{token}` | Public tokenized response/proposal page |
| `GET` | `/api/messages/{id}/calendar-card` | Parsed invitation, response or proposal for the reader card |
| `POST` | `/api/messages/{id}/calendar-import` | Add a received invitation to the signed-in user's calendar |
| `GET`/`POST` | `/api/calendar/resources` | List or create the account's own rooms/resources |
| `GET`/`POST` | `/api/calendar/polls` | List or create meeting polls |
| `POST` | `/api/calendar/polls/{id}/close` | Close a poll |
| `POST` | `/api/calendar/polls/{id}/schedule` | Turn a poll slot into an event |
| `DELETE` | `/api/calendar/polls/{id}` | Delete a poll |
| `GET`/`POST` | `/calendar/poll/{token}` | Public signed availability page |

## Schema

Migration `0037_calendar_itip.sql` adds:

- `calendar_events.method_last_sent`, `invite_sent_at`;
- `calendar_attendees.role`, `rsvp`, `last_invite_sequence`, `last_invite_at`,
  `last_response_sequence`, `last_response_at`;
- `calendar_invite_replies` for inbound reply idempotency and audit;
- `calendar_invite_jobs` for the undo-send window and delivery worker.

Migration `0038_calendar_phase2.sql` adds:

- `calendar_events.sender_email`, `sender_name` for delegated invitations;
- `calendar_event_exceptions` for `RECURRENCE-ID` overrides;
- `calendar_meeting_proposals` for `METHOD:COUNTER` state and organizer
  decisions.

Migration `0039_calendar_phase3.sql` adds:

- attendee delivery state for RSVP analytics;
- `calendar_resources` and `calendar_event_resources` for room/resource booking;
- `calendar_polls`, `calendar_poll_slots`, `calendar_poll_invitees` and
  `calendar_poll_responses` for Find a time.

## Protocol guardrails

The implementation deliberately enforces the usual iTIP failure points:

1. **DKIM alignment** — the send path resolves the calendar `ORGANIZER` to a
   local active mailbox; `From` is that mailbox and delegated sends use a
   `Sender` header plus iCalendar `SENDER` instead of a foreign From domain.
2. **One calendar part** — the MIME builder emits exactly one `text/calendar`
   body part; tests assert the body is not duplicated.
3. **Matching METHOD** — the MIME `method=` parameter and the VCALENDAR
   `METHOD:` are built from the same value; a mismatch aborts the send.
4. **Stable UID** — the database UID is created once and never regenerated on
   update, exception or cancellation.
5. **SEQUENCE** — every series update, exception edit and cancellation increments
   the sequence before the ICS is built; stale replies/counters are rejected.
6. **TZID + VTIMEZONE** — timed events use the organizer's IANA zone, not a
   floating UTC wall clock, and the VCALENDAR carries offset observances.
7. **Line folding** — all content lines are folded at 74/75 octets with CRLF +
   whitespace, including UTF-8 boundaries.
8. **No unauthenticated ICS** — only the signed RSVP/poll pages and the existing
   tokenized calendar feed expose calendar data; there is no UID-addressed public
   `.ics` route.

Automated coverage lives in `backend/internal/httpapi/calendar_ics_test.go`
(folding, TZID/VTIMEZONE, METHOD alignment, single calendar part, SENDER,
RECURRENCE-ID, RESOURCES) and the API/E2E suites.

## Compatibility testing

Before release, test the complete loop with:
- Gmail / Google Calendar (open the `.ics`, RSVP from Google, confirm reply);
- Outlook on the web and desktop (accept/decline, time-zone rendering);
- Apple Calendar on macOS/iOS;
- Thunderbird with Lightning.

Check that an update keeps the same UID, bumps SEQUENCE, moves the event in the
calendar and resets the RSVP state; check that a cancellation removes the event.
