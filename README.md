# Alt8Mail — Enterprise Email Platform

> **Naming note:** Alt8Mail was previously developed under the working name
> Meridian. That name is retained only in this note and historical records; all
> current product, code, configuration, database and service names use
> Alt8Mail/A8M.

Alt8Mail is an enterprise email platform with a real HTTP backend, persistent
mailbox state, threaded conversations, indexed search, server-backed drafts,
Outlook-style message actions with reply/forward activity, optimistic queued
deletes, undo send and scheduled send, Sieve rules and automatic replies, sender trust,
two-factor sign-in with passkeys, enterprise Tasks and Files workspaces,
a sticky multi-source Cabinet uploader (computer, URL and authenticated mobile
QR), recipient-confirmed and lock-request file sharing, PDF/image attachment preview with a multi-file pager, BIMI/favicon sender
icons, outbound caps, live quota, shared-mailbox collaboration with assignment,
internal notes and @mentions, newsletter subscriptions with unsubscribe
management, quick, semantic and sent reactions, a full categorized emoji
picker, recipient-time scheduling suggestions, reading-time estimates, a
personal yearly review, a **Mail Home / Pulse** analytics workspace, a
shared-or-personal **Notes desk** with multiple notes, typed conversation,
event and file attachments, global and archive search, team indicators, one
Inbox pin and a persistent launcher, policy-linked classification and
DLP, legal holds and eDiscovery, one-click offboarding, signed SIEM audit
webhooks, tenant branding, an audit log, PWA support and a responsive mobile
layout. The Go build adds a separate
customer/system administration console for tenants, users, groups, org units,
custom RBAC roles, domains, policy, audit and system administrators, plus a
separate root-level **System Control** plane for the host and mail stack.

## Installation and updates

The supported deployment target is Debian 12. A fresh host can be installed
from a source checkout or an extracted release bundle:

```bash
sudo bash install.sh \
  --domain a8m.example.com \
  --hostname mail.a8m.example.com \
  --admin-email owner@a8m.example.com \
  --non-interactive
```

The installer prompts for missing values when run interactively. It creates the
first tenant owner, mailbox, administration credentials and System Control
superadmin, installs PostgreSQL, Redis, Postfix, Dovecot, Rspamd, ClamAV and
nginx, obtains a Let's Encrypt certificate when DNS is ready, configures the
firewall and writes root-only credentials to
`/root/alt8mail-initial-credentials.txt`.

New to the platform? The [first-time administrator guide](docs/FIRST_TIME_ADMIN_GUIDE.md)
walks through preflight, installation, the three sign-in planes, DNS and TLS,
user onboarding, backups and the go-live checklist.

Release updates use Git tags and are applied transactionally:

```bash
sudo alt8mail-update --check
sudo alt8mail-update
```

The updater uses the read-only deploy key in `/etc/a8m/github_readonly`, builds
the selected tag, backs up the database, configuration and systemd units under
`/var/backups/alt8mail`, applies new migrations and waits up to 60 seconds for
all three services to report the target version. If readiness fails, it restores
the previous release, database and unit files.

Register `/etc/a8m/github_readonly.pub` as a read-only deploy key on the
repository before using the updater. Release artifacts are built by
`.github/workflows/release.yml`.

## Localization

UI catalogs live under `public/locales/<language>/`. JSON files are the source
of truth so translations are versioned with releases. User and tenant locale
preferences are stored in PostgreSQL; Redis is only a cache. The webmail loader
supports fallback-to-English, interpolation, `Intl.PluralRules`, RTL direction,
and date/number/time formatting. English, German, French, Spanish, Portuguese,
Dutch, Italian and Arabic catalogs ship in this release; additional languages
can be added without application changes. Run `npm run test:i18n` to validate
manifest entries, key coverage and UI key references.


The **0.22.0** release extends the curated Add-ons directory to 20 connectors with **Evernote** and **Mergo**. Evernote saves the selected email to the user's private Evernote notes address through the mailbox's own submission service, with the address encrypted at rest and restricted to `@m.evernote.com`. Mergo adds the sender to a mail-merge campaign through its documented Google Sheets workflow: a user-hosted Google Apps Script endpoint appends the contact to the campaign sheet, and the connector restricts webhook URLs to `script.google.com/macros/s/*/exec`. Both connectors carry exact local logos, provider support, documentation and privacy links across Webmail, Customer Admin and System Control. The release includes migration `0051_addon_notes_mailmerge`.

The **0.21.0** release adds the curated, first-party-reviewed Add-ons directory. Webmail now has a Settings → Add-ons page and an **Add-on** shortcut in the workspace rail, with 18 reviewed connectors: DeepL, Todoist, Slack, HubSpot, Asana, PagerDuty, Zoom, Pipedrive, Zoho CRM, ClickUp, Zendesk, Salesforce, Freshdesk, Intercom, TickTick, Calendly, Jira and Linear. Every add-on shows its exact provider logo stored locally, vendor and category details, a support link on the card, and a Details dialog with website, support, documentation, privacy, authentication, scope, connector version, permissions and reader actions. Workspace administrators can choose self-service or approval-required installation, review setup/install requests, and enable or disable add-ons for the organization. The same catalog appears in Customer Admin under Governance → Add-ons for workspace policy and approvals, and in System Control under Infrastructure → Add-ons for system-wide enablement. Slack supports managed OAuth relay, bring-your-own-app OAuth and signed Events API webhooks; provider calls use fixed or allowlisted hosts and encrypted connection storage. The release includes migrations `0046_addons` through `0050_addon_wave3_connectors`.

The **0.20.0** release adds separate-window delegated mailboxes and closes four webmail issues. When a mailbox is delegated directly to a user or through an admin-managed group, the signed-in user now sees a **Delegated** button beside the notification icon. It opens a searchable list of mailbox names and addresses, and choosing one launches that mailbox in its own browser window with a scoped session; the original mailbox stays usable in its own window, and closing a delegated window leaves the primary sign-in untouched. Signing out of the primary mailbox revokes every delegated window opened from it, including windows in other browsers, and delegates are revalidated on every request so a revoked grant or group membership closes an open window. Delegated windows can be read-only or read-and-send; read-only blocks sending, scheduled-send edits, calendar invitations and RSVPs, sent reactions, Sieve changes and external file or calendar sharing, while account-security and credential endpoints stay reserved for the primary sign-in. The release also makes dark-theme email text readable when a message relies on inline colors, legacy `<font color>` markup or the wider CSS color grammar, while preserving dark text on light-background cards; fixes saving an existing calendar event so it no longer reports “You cannot move the event to that calendar”; enlarges the month-view **+** control and keeps it visible without hovering; and keeps double-clicking a month date opening **New event**. The release includes migration `0045_delegated_sessions`.

The **0.19.0** release adds a duplicate-subject reply reminder and finishes the Subscriptions cleanup work. When a person already sent another message with the same subject in the last three days, opening a reply now shows a dismissible warning directly above the composer: **“You received another message from … with this exact subject in the last 3 days. Check the earlier message before replying.”** The comparison ignores `Re:`, `Fwd:` and `Fw:` prefixes, only considers separate conversations, and names the sender. The same release keeps every `List-Unsubscribe` message out of Inbox: inbox delivery is stored in the logical **Subscriptions** folder, the physical IMAP copy stays in INBOX for correct move/delete behavior, and migration `0044_subscriptions_folder` moves existing newsletters out of Inbox. The Subscriptions folder now has its own right-click/ellipsis menu with Rename, Move and Delete disabled, plus **Empty subscriptions**, **Unsubscribe from all** and **Mark all as read**. Unsubscribe from all calls the sender’s one-click, page or mailto endpoint and reports per-sender success or failure in a cleanup dialog with the number of messages permanently deleted; attempted senders are removed even when their unsubscribe endpoint fails, while manual-only senders are kept. A failed unsubscribe from the reader also permanently removes that message, and deleting or emptying Subscriptions never creates a Trash copy and does not offer undo. The release includes migration `0044_subscriptions_folder`.

The **0.18.0** release turns calendar invitations into a first-class mail experience and closes a local-domain spoofing hole. Calendar messages now show a **Calendar** badge in the list and a Gmail-style card under the message header with the date tile, current versus proposed time, guest states, RSVP controls and Accept/Decline actions. Gmail proposals are recognised whether Google sends `METHOD:COUNTER` or a REQUEST-shaped counter, sequence mismatches no longer drop valid replies, and a floating proposal dock shows guests waiting on the organizer without opening the source email. Received invitations from external organizers gain **Add to Calendar**, and the public RSVP page is now a calendar-themed confirmation page that lets a guest change their answer. The share, upload and public calendar routes are added to older nginx sites automatically at control-plane startup; the installer and Web Net generator include them directly. Rooms and resources are now personal to the account that created them, with a **+** control in the event dialog and migration `0043_calendar_user_resources` deactivating the old tenant-wide defaults. The release also fixes light-theme icon contrast on the notes count, jump-to-date and upload manager, adds the missing Compose and “What did I commit to?” icons, changes the jump-to-date icon to a curved-down-left arrow, and opens **New event** when a month day is double-clicked. The calendar email template gains a date tile, boxed event details and a clearer agenda/RSVP layout. For mail security, Rspamd now rejects inbound messages whose MIME `From` domain is hosted by this Alt8Mail instance but fails aligned SPF/DKIM/DMARC authentication; the hosted-domain map is refreshed from `mail_domains`, authenticated submission is excluded, and the rule is installed automatically on existing servers when System Control restarts. See [Calendar invitations](docs/CALENDAR_INVITATIONS.md) and the updated [security notes](SECURITY.md). The release includes migration `0043_calendar_user_resources`.

The **0.17.1** release is a deployment hotfix. Upload-session routes moved from `/api/files/upload-sessions/...` to `/api/upload-sessions/...` because the former pattern conflicted with `GET /api/files/{id}/shares` and made Go's ServeMux panic during startup. A route-registration regression test now runs before release builds. The updater rollback path was also rebuilt: it restores the pre-update dump into a fresh database and keeps the previous database under a timestamped name instead of running `pg_restore --clean` against a database that a failed update had already migrated. A companion `alt8mail-recover-db.sh` script can restore the pre-update backup after an interrupted rollback:

```bash
sudo bash scripts/alt8mail-recover-db.sh /var/backups/alt8mail/update-YYYYMMDDTHHMMSSZ
```

No new migration is required; 0.17.0 already includes migrations `0037_calendar_itip` through `0042_file_transfer_security`.

The **0.17.0** release is the largest collaboration update since Mail Home. It ships the complete **calendar invitation program** (signed iMIP/iTIP invites, replies, counters, recurrence exceptions, polls, resources, analytics and delegated organizers), the **Notes desk** with typed conversation/event/file attachments, global and archive search, shared-mailbox team indicators and a migrated legacy note, and a refreshed **Mail Home** that replaces the old monogram with an envelope illustration. The mail list gains an Outlook-style **Jump to date** menu with Today, Yesterday, Last week, Last month, Last year and a custom date picker that loads older pages as needed. Cabinet now has a draggable, sticky **multi-source upload manager** covering computer uploads with per-file progress, direct URL imports with SSRF protection, and a 60-minute authenticated mobile QR flow that can be extended from the desktop; uploads continue while navigating the workspace. File-sharing links now require the intended recipient to confirm their email on the device, locked files show a request-access page and notify the owner, and Cabinet exposes a lock/unlock control. New messages default to **Arial 12**, and the duplicate React action was removed from the command bar. This release includes migrations `0037_calendar_itip` through `0042_file_transfer_security`; see [Calendar invitations](docs/CALENDAR_INVITATIONS.md), [Mail Home and Notes](docs/MAIL_HOME_AND_NOTES.md) and [Cabinet uploads and sharing](docs/CABINET_UPLOADS_AND_SHARING.md).

The **0.16.0** release adds the enterprise calendar layer. **Meeting polls** let organizers collect Yes/Maybe/No availability from external guests through signed links, then schedule the best slot as a normal event and invitation. **RSVP analytics** appear for larger meetings with response rate, average response time, invitation delivery/failure counts and per-domain breakdowns. **Rooms and resources** are bookable from the event dialog, emitted as `CUTYPE=RESOURCE` and `RESOURCES` in the ICS, and protected against double-booking by transactional conflict checks. **Delegated organizers** now send DKIM-aligned `From` mail with the delegate in the `Sender` header and iCalendar `SENDER` property; both people see the event and guest status. This release also hardens the protocol guardrails: one calendar MIME part, matching MIME/ICS `METHOD`, stable UID, sequence checks, TZID+VTIMEZONE, 75-octet folding and signed-only public calendar pages. See [docs/CALENDAR_INVITATIONS.md](docs/CALENDAR_INVITATIONS.md). The release includes migration `0039_calendar_phase3`.

The **0.15.0** release polishes the invitation loop. Guests can **propose a new time** from the signed RSVP page or webmail; Alt8Mail sends `METHOD:COUNTER` to external organizers, parses inbound counters, and gives the organizer Accept/Decline controls in the guest panel. Recurring events support **this-occurrence-only** edits and cancellations through `RECURRENCE-ID`, with the master and exception VEVENTs sent together. Events on shared calendars now use a stable `ORGANIZER` plus a delegated `SENDER`, invitations and cancellations are localized into all eight interface languages, every timed event carries a client `VALARM`, and published calendars are linked from the VCALENDAR through `SOURCE` plus a subscribe link in the email. See [docs/CALENDAR_INVITATIONS.md](docs/CALENDAR_INVITATIONS.md). The release includes migration `0038_calendar_phase2`.

The **0.14.0** release makes calendar invitations interoperable with Gmail, Outlook, Apple Calendar and Thunderbird. Invitations now use one message per guest with a three-part MIME body (plain text, styled Yes/Maybe/No buttons and `text/calendar; method=REQUEST`), a stable UID and sequence, organizer time-zone `VTIMEZONE`, signed and expiring RSVP links and a public confirmation page. Inbound `METHOD:REPLY` messages are verified against the attendee address and UID, stale sequences are ignored, and accepted/tentative/declined state updates the event in real time. Organizers get a **Guest responses** panel with PARTSTAT chips, “awaiting response on the updated time” markers and **Resend to waiting guests**. Updates bump `SEQUENCE` and reset guest answers; deleting sends a correctly sequenced `METHOD:CANCEL`. Invitations share the account’s undo-send window. See [docs/CALENDAR_INVITATIONS.md](docs/CALENDAR_INVITATIONS.md). The release includes migration `0037_calendar_itip`.

The **0.13.0** release turns the empty reading pane into **Mail Home**, with a second **Pulse** tab for a year-selectable, month/week mail-flow chart and interactive period details. A search started from Pulse opens a persistent **Search report** tab with sender, folder and monthly-match analytics; the tab survives a reload in the same browser session and clears on sign-out or tab close. Mailboxes gain **one short sticky note** with markdown-lite text, four tints, a one-line collapsed state, session or permanent dismissal, drag-to-pin conversations and an optional prompt after snoozing. Notes live in the existing PostgreSQL user-preferences document, appear above Inbox, on Mail Home and in the Today/Calendar agenda, and become an audited team note when a delegated mailbox is open. **Reactions** now use a full categorized emoji catalog with search and recent emoji, expose **React** beside Reply with AI, disable themselves for newsletters, Spam, Drafts, Sent and Subscriptions, and require a confirmation before the reacted email is sent. The reaction email shows the emoji at Gmail-like size with `{sender} from {organization} reacted via Alt8Mail` underneath. Sticky notes and reactions have tenant policy switches under **Policies → Features**. See [docs/MAIL_HOME_AND_NOTES.md](docs/MAIL_HOME_AND_NOTES.md). This release has no database migration.

The **0.12.0** release completes the enterprise layer. Outbound mail now carries an organization classification and is evaluated against up to 30 **DLP rules** that can label, warn or block a send; the composer runs the same check before delivery and scheduled edits are re-evaluated. **Legal holds** preserve tenant-wide or individual mailboxes against deletion, folder emptying and retention, and the new **Compliance** console provides keyword, mailbox, date, folder and classification **eDiscovery** with CSV export. **One-click offboarding** closes an account, revokes every session and trusted device, optionally hands mailbox access to a coworker, places a legal hold and enables an automatic reply in one audited workflow. **Signed SIEM audit webhooks** stream user and administrator audit events over HTTPS with HMAC-SHA256 signatures, sealed signing secrets and retry/backoff delivery tracking. **Tenant branding** now reaches the public sign-in screen, webmail shell, browser title, accent color and outbound mail footer. New permissions are `legal.read`, `legal.manage`, `siem.read` and `siem.manage`; see [docs/ENTERPRISE_CONTROLS.md](docs/ENTERPRISE_CONTROLS.md). The release includes migration `0036_phase4`.

The **0.11.4** patch moves Year in review higher in the account overview and opens external unsubscribe pages without giving the page access to the Alt8Mail window.

The **0.11.3** patch lets AI reaction suggestions use the existing AI cache with their own cached kind, and keeps the Subscriptions folder focused on the signed-in user's own mailbox.

The **0.11.2** patch keeps the virtual Subscriptions folder available after folder create, rename, reorder or delete operations and prevents folder-management actions from targeting it.

The **0.11.1** patch keeps the Subscriptions badge in sync when a newsletter is read or marked unread, and stops empty months from drawing misleading bars in the Year in review chart.

The **0.11.0** release adds the everyday-use layer. A **Subscriptions** folder collects mail with `List-Unsubscribe` headers and offers a safe server-side one-click unsubscribe, with a sender page or compose handoff when one-click is unavailable. Every message and conversation now shows a reading-time estimate. **Send later** suggests recipient-aware times based on when each person usually writes, and **Settings → Account → Year in review** builds a private recap with monthly volume, busiest day and hour, top contacts, reading time, unsubscribes and reactions. Messages support quick and AI-assisted semantic emoji reactions. Reactions can stay private or be sent to the sender as a small threaded email, with an eight-second undo window for outbound reactions and local undo after delivery. **Reaction rules** automatically add private reactions to new matching mail, and AI conversation summaries now include visible reaction signals. The release includes migration `0034_phase3`.

The **0.10.2** patch refines the conversation list: handling status and assignee chips now sit on the preview line instead of compressing the sender name and subject, and compact density hides those row chips while keeping the reader controls.

The **0.10.1** patch keeps folder and unread badges in sync with closed conversations. Closing or reopening a thread updates sidebar totals immediately, and the server-side Inbox, Sent, starred and snoozed counts now exclude closed threads so a closed conversation no longer inflates unread badges after a reload.

The **0.10.0** release adds collaboration inside conversations. Mailbox owners and delegates can set a handling status, assign a conversation to a coworker, close and reopen threads, and keep audited internal team notes; closed conversations move to a dedicated **Closed conversations** view. The composer now supports **@mentions** for active coworkers, and mention and assignment alerts are stored per user, merged into the notification center with mail, calendar and security activity, and refreshed while Alt8Mail is open. Customer administrators also get a **Blocklist health** card on the overview showing confirmed threats, blocks fired in the last seven days and dismissed reports. The release includes migration `0033_collaboration`.

The **0.9.1** patch makes the ambient-intelligence features discoverable and fixes stale webmail shells. The reader now shows a visible **What did I commit to?** chip, each trust line exposes **Why is this safe? / Why is this junk?**, compose shows a **Send check** control, and Inbox exposes a manual **Catch me up** action that works even without a long absence. Tone suggestions now always provide a safe professional fallback for new recipients instead of disappearing. The webmail HTML entry point is revalidated instead of being cached, and the service worker fetches navigations with `no-store` so an older app shell cannot hide a new release. The service worker also adds any missing ambient controls when an older cached shell loads a newer app bundle.

The **0.9.0** release starts the ambient-intelligence work in webmail. Threads gain a user-triggered **What did I commit to?** action that extracts the mailbox owner's promises, deadlines and requests, quoting the exact sentence and jumping back to the source message. A send-time intent check warns when the draft says “attached” without a file or when a relative date such as “tomorrow” or “Friday” does not match the send time. Returning users see a dismissible catch-up card above Inbox with changed conversations, replies needed and unread counts. The composer offers a relationship-aware tone suggestion chip based on prior correspondence, and the security-details panel now explains SPF, DKIM, DMARC, trust and sender history in plain English. All model-backed additions respect the organization's AI assistant setting; the catch-up card, intent check and safety explanation remain available without AI.

The **0.8.2** patch fixes Web Net applies on servers upgraded from older releases. When the previous nginx site used wildcard listeners, the new per-address Web Net listeners could not be bound during a reload while the old wildcard sockets were still open. nginx kept serving the old mail certificate and `systemctl reload` still reported success, so Apply failed the certificate probe even though a valid certificate had been issued. Web Net now restarts nginx after writing the managed site, verifies with `nginx -T` that the managed configuration was actually loaded, retries the certificate probe after nginx settles, and reports the served certificate's names when a mismatch remains. Press **Apply edge config** once after upgrading to activate the already-issued certificates.

The **0.8.1** patch fixes control-plane details in the threat-intelligence flow: the Security reports Refresh button now retries the Rspamd sharing settings as well as the queue, audit and live-event feeds label threat-sharing changes, and reviewing a report that no longer exists returns a clear not-found response instead of a false success.

The **0.8.0** release adds confirmed threat-intelligence sharing with Rspamd. Reports now extract and store normalized URL hostnames at creation, the reviewer sees every extracted host before deciding, and shared destinations such as shorteners, cloud storage and email-service redirectors are excluded unless the reviewer explicitly opts in. When the new **Share confirmed threat indicators with Rspamd** setting is enabled, confirmed senders, sender domains and approved URL hosts are written to the Redis-backed `a8m_reported_*` hashes and consumed by three new multimap rules. Dismissing, reopening or undoing a report rebuilds the complete active set from all still-confirmed reports, so one dismissal cannot remove an indicator another report still confirms. The setting is off by default; turning it off clears the maps. The installer and updater add the Rspamd rules without overwriting existing administrator rules, and [`docs/THREAT_INTELLIGENCE.md`](docs/THREAT_INTELLIGENCE.md) documents the rules, Redis keys and inspection commands.

The **0.7.5** release fixes Web Push enrollment and key rotation, and widens the reading pane. Browser subscriptions now accept the standard `expirationTime` field in `PushSubscription.toJSON()`, so **Enable on this device** no longer fails with `json: unknown field "expirationTime"`. VAPID key rotation now updates the correct database parameter, replaces the key and clears subscriptions in one transaction, restores the previous private key if the database step fails, and reports the real cause instead of the generic control-service error. The message body now fills the reading pane while the reading column is capped at 2000px and centred on ultra-wide displays, replacing the narrow 90ch text measure.

The **0.7.4** release makes test notifications appear while Alt8Mail is open. A requested test now carries an explicit foreground hint, so the service worker shows it even when a webmail window is visible instead of deferring to the in-app notifier; ordinary new-mail pushes keep the duplicate-suppression behaviour. Both the webmail and System Control test senders include the hint.

The **0.7.3** release makes Web Push test delivery diagnose itself. Before sending a test, webmail now re-registers the browser’s current endpoint, so a subscription that was rotated or missing on the server is restored instead of producing a misleading “no subscribed browser” message. A subscription still owned by a previous account is replaced and retried once. The test response distinguishes no subscription, an unavailable VAPID key, an unreadable private key and a push-gateway rejection, and the notifications panel keeps the actual failure visible. Test results now report how many browsers received the notification and how many failed.

The **0.7.2** release hardens Web Net across reboots and updates. The control plane now restores panel-managed runtime IP addresses, regenerates its managed nginx site from the applied database state, starts nginx when a boot-time bind failure left it inactive, and probes every hostname over local TLS/SNI to confirm nginx serves the expected certificate. The mail host also serves a retirement service worker at `/sw.js`, so an old installed webmail PWA unregisters and clears its cached shell instead of bypassing Drop mode; normal mail-host HTTP requests still return 444. The stale “start it on port 8001” sign-in error is replaced with an accurate Web Net/a8m-server guidance message.

The **0.7.1** release lets Web Net request a certificate automatically when a hostname uses **Auto** and no valid covering certificate exists. **Verify / issue TLS** and **Apply edge config** both run certbot through the nginx ACME webroot, require DNS readiness and a Let’s Encrypt account email, add the issued certificate to the TLS inventory, pin it to that service, and surface the certbot output if issuance fails. The installer seeds the Let’s Encrypt account email, and this release includes the widened 90ch reading-pane width.

The **0.7.0** release adds **Web Net**, browser push notifications and enriched mail logs. Web Net gives webmail and administration separate hostnames from the mail/MX host, validates DNS against server addresses, supports shared or dedicated IP binding, selects issued certificates, and renders a managed nginx site with ACME, maintenance pages, allow/deny CIDRs and mail-host blocking. Applying is transactional: nginx is tested before reload and configuration, `app.env` and services roll back on failure. The page also reports request volume, 5xx count, unique clients, average/p95 latency and redacted top paths.

Browser Push uses VAPID keys owned by the control plane, with enable/disable, quiet hours, sender/subject privacy controls, key rotation and per-account test delivery. Webmail users opt in per browser under **Settings → Notifications**; the service worker shows OS notifications when the tab is hidden or closed and suppresses duplicates while an Alt8Mail window is visible. Mail-log event modals now show the real Subject plus Headers, Message body and Raw source tabs, enriched from the Postfix queue or Maildir when the delivered copy is still available. The installer preserves a Web Net-managed nginx site and its origin values on repair runs.

The **0.6.6** release adds complete paragraph alignment to the compose editor. The toolbar now exposes Left, Center, Right and Justify with active-state feedback, and Ctrl+L, Ctrl+E, Ctrl+R and Ctrl+J align the current paragraph while focus is inside the rich-text editor. Alignment is retained by the client and server HTML sanitizers when a message is sent.

The same release bundles the recent webmail upgrade set: DMARC-gated BIMI logos with authenticated favicon fallback, previous/next paging for multi-attachment previews, AI reply attachment context for PDF/Word/Excel/CSV/images with a 100 MB selection budget, a moving writing indicator while the AI provider works, automatic-signature restoration below AI-generated reply text and above the quote, attachment selection inside the regular AI writing assistant, Ctrl+Z/Ctrl+Y across compose and signature editors, and the revised **See Alt8Mail policy** retention label.

The **0.6.5** patch matches Outlook’s read/unread treatment in the conversation list. Read senders and subjects now use regular weight, while unread conversations stay bold; opening or marking a message read removes the bold styling immediately.

The **0.6.4** release tightens mailbox action feedback and routing. Optimistic archive, move, delete, spam and snooze actions stay hidden while their server write is in flight, even if a background refresh arrives, and selections originating from notifications or deep links are canonicalized to individual-view message rows. Emptying a folder now fades its visible rows out, clears the active list immediately and refreshes the folder that is actually open instead of silently loading Inbox. The tab/URL helpers also reuse the already-built view thread instead of rebuilding the conversation list for every title or hash update. Individual message view still renders one row per message, so large inboxes remain faster in Grouped conversation view.

The **0.6.3** patch keeps conversation addresses opaque in Individual message view. Selecting a message now writes its 22-character thread ID to the hash instead of the message row ID, so an address can no longer fall back to `msg_imap_…`. Pop-out windows and `?thread=`/`?message=` deep links are translated to the same opaque thread address while the selected individual message still opens in the reader.

The **0.6.2** release gives every webmail page a context-aware browser-tab title. Sign-in uses `Alt8Mail: Secure Email with AI built in - Sign-in`; mailbox folders show their unread count, folder name and account address; open conversations show the folder and subject; an IMAP sync failure or browser outage uses the offline format; authenticated non-mail workspaces use the account tagline; and non-auth pages use the general Alt8Mail tagline. Titles update when the folder changes, a conversation opens or closes, messages become read or unread, background sync refreshes the mailbox, or the connection state changes. The bootstrap payload now exposes the last IMAP sync error so an outage can be reflected without reloading the page.

The **0.6.1** patch hardens the opaque-ID migration for clients that still hold a legacy value. Draft saves, sends, restored drafts and task links normalize `t_` and `t_imap_` identifiers before they are written, so an already-open browser tab or an older API client cannot reintroduce a legacy URL after the migration has run.

The **0.6.0** release makes conversation URLs opaque and uniform. Thread identifiers now use one 22-character base64url format everywhere: new local and composed conversations use 16 bytes from `crypto/rand`, while first-time imported threads derive an opaque 128-bit SHA-256 identifier from their thread root. The `t_` and `t_imap_` origin prefixes are gone, so a URL such as `https://mail.example.com/#/Inbox/q8Zp2rLmXwN4tVbKc9FjA3` no longer reveals whether a conversation was composed locally or imported over IMAP. Existing message rows, queued outbox items, task links, ignored-thread lists and undo snapshots are migrated in place. Legacy bookmarks remain compatible: the backend resolves a migrated legacy ID to its canonical value and the webmail rewrites the address bar, including pop-out links and `?thread=` deep links.

The **0.5.3** release rebuilds second-factor setup in the webmail. The authenticator dialog now renders a scannable QR code from the server’s `otpauth` payload, offers guided Google Authenticator, Microsoft Authenticator and Authy choices with their icons, official download links and a clear non-affiliation notice, and exposes a masked, copyable manual setup key with provider-specific instructions. Six auto-advancing code boxes support paste and one-time-code autofill, verification runs automatically when the sixth digit arrives, and inline guidance covers expired codes, device clocks and starting over. Removing an authenticator now uses a password-confirmation dialog instead of a browser prompt, and a rejected code or password no longer signs the user out. Passkey enrollment gained a capability-aware panel that detects Windows Hello, Touch ID, Face ID, platform authenticators and HTTPS requirements, explains security-key and phone fallbacks, lets users name the passkey, maps WebAuthn failures to friendly retry guidance, and supports cancellable device prompts. The sign-in passkey prompt is redesigned with device context, privacy messaging, troubleshooting and a live waiting state.

The **0.5.2** release rebuilds mail folders as a persistent Outlook-style tree. Child folders render directly beneath their parent, each parent gets a disclosure arrow, and the expanded or collapsed state is stored with the folder so it survives sign-out and sign-in. Primary folders are fixed and cannot be moved; custom folders can be dragged between any valid parent with cycle and depth protection, and a new subfolder expands its parent automatically. Messages can also be dragged from the conversation list onto any folder, including folders nested under Inbox. Folder rows use a larger, full-width selection treatment with drop highlights and accessible disclosure controls.

The **0.5.1** patch keeps connected-drive paths intact when a Cabinet folder is renamed, moved or deleted, refreshes the shared-file badge immediately after a link is revoked, and keeps the Calendar mini-month in sync when a date outside the visible month is selected.

The **0.5.0** release turns the Cabinet into a full file workspace and rebuilds the Calendar around shared, enterprise scheduling. Cabinet now supports drag-and-drop uploads, nested folders with drag-to-move, list/tile/thumbnail layouts, sorting and grouping, and a complete right-click menu (preview, download, open in, copy link, share, permissions, rename, move, copy, save as, delete, file info and more). Files can be attached to a new message directly from that menu, external links now show the permission level on the public share page, and revoked links are managed in place. Cabinet storage follows one combined mailbox allowance by default; administrators can switch the organization, a domain or an individual user to a separate Cabinet allowance from the new **Storage** page in `/admin`, and a non-zero mail allowance is mirrored to Dovecot's mailbox quota.

The Calendar has a new Outlook-inspired ribbon with Day, Work week, Week, Month, Split and Agenda views, a mini month, team calendars with colour coding and visibility controls, drag-and-drop across days, event search, reminders and print. Events now support attendees, invitations with ICS email, accept/tentative/decline RSVPs, recurrence, busy/free, locations, meeting links and Cabinet attachments. Shared calendars can be viewed or edited by coworkers, and any calendar can publish a revocable iCalendar feed for Google Calendar, Apple Calendar and Outlook, or subscribe to an https/webcal feed from Google or iCloud with SSRF protection. The Add calendar panel covers blank calendars, web subscriptions, `.ics` import, 190+ country holiday calendars and contact birthdays. Address books are now multiple, contacts can store birthdays, and the attendee picker searches them. Both Compose and the Signature Editor gain a dark-theme-only light bulb that gives the writing surface a white background without changing the surrounding chrome.

Security and account fixes include a persistent admin sign-out (cookies are cleared even when the session or CSRF token is stale), self-service password changes in `/admin` that also rotate the mailbox password and revoke other sessions, and sign-in activity now reads “New sign-in detected on your email account” instead of exposing the backend stack name. External file sharing is available by default with view-only links; organizations can disable or restrict it under Policies → Sharing.

The **0.4.3** release groups the message list by Outlook-style periods: Pinned, Today, Yesterday, This week, Last week, This month, Last month, named months, Last Year and older years. Groups are collapsible, pinned rows carry a yellow gradient and always lead the list, and a snoozed message returns unread with a clock indicator in Today until it is opened, after which it settles back into its real date group. Label chips are larger Outlook-style outlined pills, and the All/Unread/Starred/Files filter strip is raised and squared so the list appears beneath it.

The **0.4.2** release refines the message list and reader. Row checkboxes stay out of the way until hover or selection, the bulk-action bar stays inside the list pane, folder context menus show total and unread counts, and pinned conversations rise to the top with a yellow pin. The list header gains Outlook-style filter choices (All, Unread, Flagged, To me, Has files, Mentions me and calendar invites), Ctrl+A selects the visible list, and right-clicking highlighted message text offers only Reply, Reply all, Forward, Delete and Copy Text. Custom labels can be created from the Categorize menu or Settings → Mail, with names and colours; dark-theme readers also gain a yellow light-bulb toggle for a white reading surface.

The **0.4.1** release fixes a mailbox-memory regression found on small hosts. The Go server no longer writes the whole `messages`, `attachments` or `outbox` collections back into `app_states` after a sync or message action, and migration `0027` removes copies left by earlier releases. IMAP sync, folder actions and undo now load message metadata without HTML or plain-text bodies, so reconciling thousands of messages no longer allocates several copies of the mailbox. Delegated inbox rows are read from the normalized mail table instead of the workspace document. A 2 GB host receives conservative automatic caps (two IMAP workers and eight database connections) and an automatic soft Go heap target; larger hosts keep their configured values. `ALT8MAIL_MEMORY_LIMIT_MB` and `GOMEMLIMIT` can override the heap target.

The **0.4.0** release brings the webmail closer to Outlook. Message rows show reply (←) and forward (→) activity, and the reader shows the weekday, date and time of each action. Right-clicking a conversation opens a full message menu with Reply, Reply all, Forward, Delete, Archive, Move, Copy, Mark as unread, Pin, Categorize, Flag, Snooze, Rules, quick-action setup, junk/phishing/concern reports, Block, Ignore, Download, Find related, View and Advanced actions; every entry has server support, including Snoozed and Ignored conversation state. Deletes are optimistic: the row disappears at once, the reader advances to the next conversation (and closes after the last), and actions are queued with retries, idempotent request receipts and offline recovery. Compose now proxies remote images, preserves pasted base64 images, keeps Outlook table layout without forced grid lines, applies font family and size correctly before typing, and offers an HTML-source dialog for both compose and the signature editor. Signature editing matches the compose editor with the full formatting toolbar, image handling, advanced styling and the same safe HTML conversion.

The **0.3.19** release makes the webmail open large mailboxes in pages. Bootstrap, folder changes and inbox polling fetch the newest 50 conversations first; scrolling the list requests older pages, and folder badges use server-side counts rather than the loaded page. Conversation siblings are loaded with each page, so threads stay complete, while drafts and delegated mailboxes remain available on first paint.

The **0.3.18** release makes mailbox migration idempotent when a destination account was already created by an interrupted attempt, fixes the destination account/password handoff that could show blank credentials, adds a durable activity log to the admin migration view, and makes self-update prefer the checksummed GitHub release asset instead of compiling on the mail host. Imports now record why a source, destination, folder, notification or scanner step failed; never-signed-in destinations recovered from an earlier interrupted job get a fresh one-time password pair. The updater also refuses to start without enough free disk and caps Go build parallelism on small hosts.

The **0.3.17** release fixes a disk-exhaustion bug in mailbox import and IMAP sync. Raw Windows-1252/Latin-1 and NUL bytes in subjects, sender names, message identifiers and attachment filenames are normalized to valid UTF-8 before PostgreSQL writes; attachment files written by a sync that fails to commit are removed immediately; a background sweep reclaims unreferenced local attachment files with a configurable grace period; repeated full-sync failures back off up to one hour instead of retrying every five minutes; and imported IMAP/POP3 messages up to the configured scan cap are scanned through ClamAV, with detections moved to the unmapped `Quarantine` mailbox.

The **0.3.16** release reconciles Alt8Mail update jobs in the background after the updater restarts the control plane, and refreshes the latest-version state when an update completes so the System page does not wait for a manual refresh.

The **0.3.15** release makes control-plane updates self-healing when systemd starts the updater without a login environment: the release builder and updater set stable `HOME`, `GOPATH`, `GOMODCACHE` and `GOCACHE` paths, and the transient update unit receives the same environment explicitly.

The **0.3.14** release repairs mailbox import validation: the admin UI and API now share one canonical connection/options contract, older cached admin bundles are still accepted, duplicate or invalid source mailboxes are rejected before a job starts, the configured message-size limit is enforced for IMAP and POP3, and mismatched TLS ports are caught before validating credentials.

The **0.3.13** release adds the System Control **System** page for transactional Alt8Mail release updates with live logs, release history, reboot tracking and audit entries; seeds hostname, mail hostname and webmail URL from the installer; lets reviewers undo a mistaken threat confirmation; and makes the updater try SSH and HTTPS repository transports in turn.

The **0.3.12** release ships the padlock markup for the six-second sign-out sequence so the envelope completes its close animation into a lock on every deployment.

The **0.3.11** release adds a six-second sign-out sequence with reverse envelope/lock, outward dot and grey draining progress animations, and resets the cached accent to the default on logout so the login card never inherits the previous account colour.

The **0.3.10** release ensures the purple pulsing AI compose action always renders its icon and AI label in white.

The **0.3.9** release fixes reply/forward draft autosave when a quoted line ends in a multibyte character, returns readable raw message source instead of base64, places the typing caret on a blank left-aligned line above the quote, and uses a solid purple pulsing AI action.

The **0.3.8** release honours numeric image widths from quoted email HTML even when the sender also supplied width:100%, preventing logos and banners from stretching to the composer width.

The **0.3.7** release shows quoted HTML automatically in reply and forward (no Quote click required), constrains it to an Outlook-style centered reading column with the original image dimensions, fixes raw message source loading for real IMAP mail, restores readable danger-button contrast, gives priority choices distinct color signals, and moves a pulsing AI assistant action directly after Quote.

The **0.3.6** release keeps the safe spacing, borders, backgrounds and table layout of quoted email HTML in outbound reply and forward messages, not only in the editor.

The **0.3.5** release renders the original HTML, layout and images inside reply and forward compose, using a compact Gmail-style quote with a show/hide control. Quoted images load through the privacy proxy in the editor and are restored to their original external URLs when sent; the composer stays a normal compose window instead of expanding into an email page.

The **0.3.4** release keeps delegated mailboxes out of move and rule destinations, stops the migration mapping form from refreshing while an administrator edits it, and clears source credentials after a migration is dismissed.

The **0.3.3** release preserves the branded HTML of account and migration notification emails (server-generated, escaped) and surfaces notification failures in migration results.

The **0.3.2** release finishes the admin detail-page polish: edits stay on the user page, vacation intervals match the Sieve limit, and validated mailboxes are auto-mapped to existing users with the same address.

The **0.3.1** release hardens the onboarding workspace: delegated-mailbox permission and signature-default fixes, faster batched IMAP migration with resumable per-folder checkpoints, IPv6-safe source connections, and clearer migration credentials when a notification email could not be delivered.

The **0.3.0** platform release turns the customer admin into an onboarding and migration workspace: CSV/XLSX user import with generated credentials and branded notification email, a resumable IMAP/POP3 mailbox migration engine with validation, mailbox mapping, live progress and dashboard visibility, a dedicated user page with alias editing, signature and per-address signature management, vacation responders and delegated mailbox access, a raw RFC 822 message source viewer in webmail, and an edge-to-edge webmail layout.

The **0.2.8** platform release lets users manage their own Junk by default: **Report Not Spam** and **Move to Inbox** work unless an organization turns on **Restrict Junk release to administrators** in the admin Mail policy. When enabled, members see an "Administrator release required" state in the Junk banner and the reader action is locked, while tenant administrators release mail from the Quarantine queue. The policy is returned to the webmail bootstrap so the UI and API agree.

The **0.2.7** platform release adds privacy-proxied remote and inline email images with a default-on display preference (Junk always blocks images and attachments), a Junk quarantine banner with **Report Not Spam** and **Move to Inbox**, automatic sender rules when mail is marked Junk, a cross-tenant **Security reports** review queue in System Control with immediate Postfix-policy sender blocking, custom folders with rich hover/right-click actions, a searchable **Move to** panel with recent folders and rule mode, and a slim reader subject bar with close and previous/next navigation.

The **0.2.4** platform release adds mandatory first-login onboarding (display
name, time zone, 12/24-hour time, language, theme and accent, temporary-password
rotation and optional two-factor guidance), an admin user flow that picks the
mailbox name and verified domain separately, optional admin-set passwords, a
named signature library with New/Reply/Forward defaults, per-identity rich/plain
signatures, a Compose **Insert signature** picker with an inline WYSIWYG creator,
Alt8Mail Intelligence English spell check before sending, and a working
Save-a-copy-in-Sent toggle. Completing onboarding writes the account name,
primary sender label and primary sending identity, so messages no longer leave
as `Primary <user@domain>`.

The staging webmail release (**0.9.0-staging**) additionally has normalized mail,
attachment, outbox and undo tables, PostgreSQL full-text search, IMAP IDLE
push, normalized Tasks/Files/Calendar/Contacts, public guest-share pages,
CalDAV/CardDAV with expiring app passwords, incremental large-mailbox sync,
MTA-STS policy publication, S3/MinIO storage and Google Drive/Dropbox/OneDrive
OAuth flows. The 0.7 release added a rich HTML composer with saved
size/position preferences, verified sending identities, dated mail forwarding,
a live notification center, a branded boot splash, and a tenant-configured AI
assistant for writing, summaries and translation. The 0.8 release makes reader
AI strictly user-triggered with a split **AI** dropdown (Summarise this email
/ Translate Email), adds **Reply with AI** and CC-aware **Reply all**, adds
font family/size controls to the HTML composer, makes minimized drafts a
high-contrast Outlook-blue dock tab with an arrival animation, adds the
server-backed Group by conversation / Individual view choice, holds the
opening-workspace splash for five seconds after interactive sign-in, and
accepts the staging demo password for re-authentication prompts on its mapped
account. The 0.9 release adds persistent pin/unpin, cleaner individual-message
reading controls, server-persisted compose minimization across reload and
sign-in on another computer, hash routes for folders/settings/messages, an
Alt8Mail About page with IMAP/POP3/SMTP client setup, and a System Control
release (`0.10.0-control`) with mailbox pagination and sorting, TLS certificate
import/assignment with validation and rollback, OS software updates with live
terminal output, an authenticated local SSH terminal with SSH-port detection,
and deliverability health for the Postfix sending IP. See
[`docs/PHASE_REVIEW.md`](docs/PHASE_REVIEW.md) for what is live versus
constrained.

The frontend is dependency-free: Node.js built-ins only for the server and a
plain ES-module frontend. It is a blueprint for wiring the UI to a real mail
stack (Dovecot, Sieve/managesieve, postfwd, DKIM/DMARC/X, object storage,
identity provider, audit log, internal directory).

```text
http://localhost:8001
demo: avery.chen@a8m.example / a8m-demo
second factor: use the simulated passkey or code 123456
```

## Cloud staging build (Go feature API + real mail)

A live staging environment is running on Debian 12 at:

```text
https://130-94-46-22.sslip.io   (Let's Encrypt certificate, valid)
https://130.94.46.22            (IP fallback, self-signed/warning)

Alt8Mail UI + full Go feature API (nginx /api → Go):
  staging alias: avery.chen@a8m.example / a8m-demo
  primary account: avery.chen@a8m.test
  password: /opt/a8m/secrets/staging_avery_password (server root only)
  second Go auth user: jordan.lee@a8m.test
  password: /opt/a8m/secrets/staging_jordan_password (server root only)

Go system API namespace (nginx /system/api → same Go service):
  https://130-94-46-22.sslip.io/system/api/

Admin console (separate Go admin backend on 127.0.0.1:8081):
  https://130-94-46-22.sslip.io/admin/

System Control (separate root-level Go service, TLS on 8808):
  https://130.94.46.22:8808
  admin: sysadmin@alt8mail.local
  initial password: /root/alt8mail-control-initial-password (root-only)
  browser warning: self-signed bootstrap certificate until replaced
```

> **Port note:** `88008` is not a valid TCP port (valid range 0–65535); Go,
> curl and UFW all reject it. The panel therefore listens on **8808**, and the
> address to open is `https://130.94.46.22:8808`. See
> [`docs/SYSTEM_CONTROL.md`](docs/SYSTEM_CONTROL.md) for installation,
> replication, security, API and recovery details.

System Control is distinct from both the webmail frontend and the customer admin
console. It has its own database schema, DB role, secrets, sessions, TOTP, audit
trail and API keys, and it manages real host resources: domains/DNS/DKIM,
mailboxes and aliases, TLS/certbot, Postfix queue and delivery, listeners and
systemd services, logs/journald, imports, backups, cron, network/UFW, PostgreSQL,
panel access, public API v1, webhooks and notifications. The dashboard reports
live mail-event traffic, host CPU/memory/disk, service health and config-sync
drift; `/healthz`, `/metrics`, `/healthz/stacks` and `/events` are available for
monitoring.

The root UI is served by the **Go backend** with **real Dovecot mailbox data**.
It does not read `seed/state.json` or the reference Node preview's demonstration
mailbox. A fresh Go mailbox state contains no messages, tasks, files or calendar
events; all mail is imported by IMAP from Dovecot.

What is live:

- Every active mailbox has its own Go worker. IMAP sync mirrors INBOX, Sent,
  Drafts, Archive, Junk and Trash from Dovecot on startup, every five minutes and
  on demand at `POST /api/mail/sync`. An IMAP IDLE connection pushes new mail,
  flag changes and expunges as they happen; when IDLE is unavailable, a
  UID-based INBOX check runs every 15 seconds per user. Work is serialized per
  mailbox and bounded by `ALT8MAIL_MAIL_WORKER_CONCURRENCY`.
- The open webmail polls the real inbox every 15 seconds as a safety net,
  updates the list, folder counts and reading pane in place, and shows a desktop
  notification or an in-app toast. No page reload is required; returning to the
  tab triggers an immediate check.
- Mail is stored in normalized PostgreSQL tables (`mail_messages`,
  `attachments`, `mail_message_attachments`, `outbox`, `undo_actions`), not in
  a per-user JSONB document. `/api/search` runs indexed PostgreSQL full-text
  search, and the frontend debounces queries and replaces the list with server
  results.
- A Message-ID is treated as unique only within a folder: sending to yourself
  correctly produces one Sent copy and one INBOX copy, sharing a conversation
  thread instead of one copy silently overwriting the other.
- Message read/star/flag, archive/move/spam/delete and undo are propagated back
  to Dovecot over IMAP. Permanent deletes are deferred until the undo window
  expires, then expunged.
- Compose drafts are written to Dovecot Drafts; sending removes the draft,
  delivers through Postfix submission (587, STARTTLS, Dovecot SASL), scans with
  Rspamd/ClamAV and appends the sent message to the Dovecot Sent folder.
- The 0.8 composer is a rich HTML editor with font family and size selectors,
  headings, lists, links, colors, alignment, a plain-text mode, radio priority
  control, real signed-in From identities, multiple drag-and-drop or Files
  attachments, an attachment picker, and saved window size/position (wide 2× and
  centered by default). A deliberately closed draft stays closed after reload;
  open/minimized windows are restored. Minimized drafts become a high-contrast
  Outlook-blue dock tab with a visible arrival animation and toast.
- Reply-all is shown only when the replied message carries additional To/Cc
  recipients; those addresses populate the compose Cc field automatically.
- Settings → Mail & reading lets each user choose **Group by conversation**
  (default) or **Individual view**, persisted through PostgreSQL preferences.
- The notification bell is a live center for real unread mail, scheduled sends,
  upcoming calendar events, sign-in/security events and quota warnings. It has
  read state, filters and a compose-style notification window instead of demo
  rows.
- Sending identities beyond the primary account and organization aliases can be
  added in Settings. External addresses receive a 48-hour verification email
  before they can send; internal addresses require an organization alias.
  A dated or permanent forwarding rule is compiled into Dovecot Sieve with an
  optional keep-a-copy behavior.
- App passwords now support permanent or 7/30/90/365-day/custom expiry, are
  enforced immediately by CalDAV/CardDAV authentication, and retain an
  active/expired/revoked history for the account owner.
- When an administrator enables the tenant AI assistant, compose gains an AI
  writing panel (8 styles, 3 lengths, creativity control, 36 languages) and the
  reading pane gains a split **AI** dropdown offering **Summarise this email**
  (recent conversation) and **Translate Email** (the user's language), plus a
  **Reply with AI** action that opens a prefilled compose window. Reply and
  compose assistance can use PDF, Word, Excel and image attachments from the
  conversation; the user explicitly selects up to 100 MB of files, PDF/DOCX/XLSX
  text is extracted with size limits, and images are sent visually only when the
  configured model supports vision. An animated writing card covers the editor
  while the provider is working, and Reply with AI restores the automatic
  signature below the generated text and above the quoted conversation. Reader
  AI is strictly manual: opening or hovering a message never generates a
  summary. Provider endpoint, model, key and generation parameters are managed
  in the admin console; keys are AES-GCM sealed and cached results are
  tenant-scoped.
- Sender avatars resolve `default._bimi` records for DMARC-enforced domains,
  proxy the published logo through the public-IP-only image fetcher, and fall
  back to the domain favicon. The endpoint is authenticated and cached per
  domain; messages fall back to the existing initials avatar.
- A branded, animated envelope boot splash covers session restoration on every
  reload so the login form never flashes before an existing session resolves.
  After an interactive sign-in, "Opening your workspace" remains visible for
  five seconds before the inbox is revealed.
- Tasks, Files, Calendar and Contacts start empty and only contain data the user
  creates or uploads through the Go API. They are stored in normalized
  PostgreSQL tables, not per-user JSONB. Storage connector definitions are
  configuration, not example file rows.
- Files can live in Alt8Mail Drive, S3/MinIO, Google Drive, Dropbox or OneDrive.
  S3 is live-tested with upload/sync/download/delete; OAuth connectors implement
  authorization, refresh, upload, download and sync but need real provider app
  credentials. Guest share pages at `/files/s/<token>` support expiry,
  revocation, view/edit access and file replacement.
- Calendar and contacts support full UI plus protocol writes. CalDAV
  (`/.well-known/caldav`) and CardDAV (`/.well-known/carddav`) expose default
  collections with ETags, sync tokens, PROPFIND/REPORT and PUT/DELETE. DAV
  clients use revocable app passwords; accounts with MFA cannot use the
  interactive password for DAV.
- Full sync is incremental and uncapped: it reconciles against a complete UID
  snapshot, downloads bodies only for unseen UIDs, imports newest-first within a
  per-pass time budget and records a `pending` backfill count. Flag-only
  reconciliation and UIDVALIDITY resets are handled without re-downloading
  existing bodies.
- Directory policies are applied to presentation: external-sender labels can be
  disabled, self-service profile editing can be locked, and recipients can be
  auto-added as personal contacts. Personal contacts always remain visible to
  their owner, independently of the organization directory policy. MTA-STS policy state, `_mta-sts` records and the
  public policy endpoint are implemented; public validation requires a
  customer-owned domain and `mta-sts.<domain>` TLS certificate.
- Login, aliases and directory are read from PostgreSQL users and
  `user_aliases`.
- Mail TLS uses the pinned `mail.a8m.test` certificate through
  `ALT8MAIL_MAIL_CA_FILE`; IMAP and SMTP verify the certificate.
- Settings from Appearance through Account are server-backed in PostgreSQL:
  theme, accent, density, reading behavior, default priority/receipt, undo
  window, signature, notification choices, language/date/time/time zone/work
  hours, sender-warning policy, 2FA policy and account sessions.
- Sieve rules and automatic replies are validated server-side, saved in
  PostgreSQL and pushed to Dovecot ManageSieve as the active `a8m` script.
- Real TOTP enrollment, hashed single-use recovery codes, trusted devices and
  WebAuthn passkey registration/verification are implemented in Go. Login
  issues a real second-factor challenge when any factor is enrolled.
- First-time enrollment is a per-user DB policy (`users.enrollment_required`).
  It is currently **false** for both staging users so normal password sessions
  can be tested. Turn it on from Security & privacy (or the DB/admin path) to
  show the enrollment screen at the next sign-in.
- A separate Go **admin console** provides tenant and user lifecycle, aliases,
  groups, org units, custom RBAC roles, domains with real DNS verification
  (including exact DKIM `p=` matching and per-domain signing keys),
  security/mail/sharing/directory policies, a tenant-wide quarantine queue with
  audited release, admin audit/CSV, admin TOTP, tenant switching, tenant
  suspension and system-administrator management.
  Admin sessions and CSRF cookies are separate from mail sessions; every
  mutation is audited. Staging exposes it at `/admin/`; it is ready for an
  `admin.example.com` subdomain.
- Tenant MFA/IP policy is enforced at mail sign-in, and tenant suspension
  invalidates mail sessions and blocks Postfix/Dovecot authentication. Outbound
  mail policy enforces SMTP access, external-recipient allowlists, blocked
  attachment extensions and size limits.
- Password history/expiry are enforced by the self-service password-change flow;
  a web password change also rotates the Dovecot mailbox credential. IMAP, POP3
  and SMTP switches are enforced by the Dovecot SQL passdb, forwarding rules are
  re-filtered on policy change, retention runs through an audited UID EXPUNGE
  worker, non-admins cannot release quarantined mail, and external link sharing
  respects the tenant sharing policy.
- Group addresses are real distribution lists: the Postfix SQL alias map expands
  active group membership to active mailboxes. A root-run
  `a8m-mailprovision` timer creates and verifies each Maildir explicitly,
  and the admin console shows provisioning state.
- A root-run `a8m-dkimprovision` timer creates per-domain 2048-bit RSA DKIM
  keys, publishes the real public key in the admin DNS records, updates
  Rspamd's selector map and verifies that the published `p=` matches the active
  signing key. Rspamd signs with the domain selector; the admin console never
  shows a placeholder DKIM value.
- The Go server authenticates its workers with Dovecot master auth
  (`ALT8MAIL_IMAP_MASTER_*`), so every mailbox is synced without storing each
  user's plaintext mailbox password.

The staging login alias is gated by `ALT8MAIL_DEMO_MODE` and is refused when
`ALT8MAIL_ENV=production`. It only maps the login, never restores example
mailbox content.

The reference Node `a8m-preview` service is stopped and disabled. Its demo
JSON remains in the repository only as the standalone local reference template;
it is not proxied and is not used by the staging frontend. See
[`docs/GO_API.md`](docs/GO_API.md) for the Go endpoint map, mail pipeline and
storage model, [`docs/ADMIN.md`](docs/ADMIN.md) for the admin console, RBAC,
policy matrix, API and subdomain deployment,
[`docs/ADDONS.md`](docs/ADDONS.md) for the curated add-on lifecycle, security
model and connector guide,
[`docs/OFFICE.md`](docs/OFFICE.md) for the OnlyOffice lifecycle agent, Cabinet
editor, guest and internal sharing, policies, DLP on save, legal hold,
eDiscovery, version comparison and review, PDF conversion and AI assistant,
[`docs/PLATFORM_BUILD_PLAN.md`](docs/PLATFORM_BUILD_PLAN.md),
[`docs/PHASE_REVIEW.md`](docs/PHASE_REVIEW.md),
[`docs/THREAT_INTELLIGENCE.md`](docs/THREAT_INTELLIGENCE.md) and
[`docs/OPERATIONS.md`](docs/OPERATIONS.md) for the phase gate, exact commands,
verification evidence and remaining gaps (public domain/MTA-STS validation,
live provider OAuth/AI credentials, DAV client/iTIP scope, identity-email
deliverability for external domains, bootstrap pagination for extreme mailboxes
and production scale/security gates).

## Run the local reference template

```bash
npm start          # production-style start on :8001
npm run dev        # restart on file changes
npm test           # API + backend behavior (24 tests)
npm run test:e2e   # real Chromium UI flows (screenshots in artifacts/)
npm run test:all   # both suites
```

Requirements: Node.js 22+ (tested on Node 24), Chrome or Microsoft Edge for the
browser suite. The app itself has no install step and no third-party packages.

Useful environment variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `8001` | HTTP port |
| `HOST` | `127.0.0.1` | Bind address |
| `DATA_FILE` | `./data/db.json` | JSON persistence file |
| `CHROME_PATH` | auto-detected | Browser used by `test:e2e` |

This section describes the standalone Node reference template only; it is not
what the live staging UI uses. The local reference seeds demo data on first run.
**About → Reset demo data** restores that local seed; it also resets drafts,
queued/scheduled sends, rules, automatic replies,
tasks, files, storage connections, preferences, 2FA recovery-code usage, trust
decisions and audit entries.

## What is implemented

> Unless marked otherwise, this section documents the standalone Node reference
> template and its seeded demo fixtures. The live Go staging UI deliberately
> starts with **no seeded mailbox, task, file or calendar data**; its mail comes
> from Dovecot and its workspaces fill only from real user actions.

### Must-haves

- **Threaded conversation view** — conversations group by `threadId`, the list
  shows message counts and participants, and the reader renders every message
  in a thread with per-message collapse/expand plus Expand all / Collapse all.
- **Attachment preview** — PDFs and images (including SVG) open in a branded
  preview dialog. Images get zoom/fit controls and dimensions; PDFs render in
  the browser viewer; other types fall back to a clear download panel. Uploaded
  attachments are stored server-side, MIME-sniffed and served with inline or
  attachment disposition as appropriate.
- **Outlook-style pop-out** — double-clicking a conversation row opens it in a
  dedicated pop-out window, and the reader toolbar has the same action. The
  popout route renders only the reading pane with the full action toolbar.
- **Reading-pane safety controls** — Archive, Flag/Unflag, Mark as spam / Not
  junk and Report email are available directly above the message. Reporting
  opens a reason picker (phishing, spam, malware, other), optional analyst
  note, and moves the conversation to Junk with a durable report record and
  audit entry. Flags appear in the message list; spam moves are undoable.
- **Server-side full-text search** — the Go API runs an indexed PostgreSQL
  `tsvector` query over subject, participants and body with
  `websearch_to_tsquery` (quoted phrases, OR and `-exclusion` work). The UI
  debounces input by 220 ms and swaps the list to server results. Operators:
  `from:`, `to:`, `cc:`, `subject:`, `body:`, `filename:` / `attachment:`,
  `has:attachment`, `has:pdf`, `before:`, `after:`,
  `is:unread|read|starred|unstarred`, `in:`, `larger:`, `smaller:` and free
  text. Search defaults to all mail; `in:` narrows it. Invalid operators are
  reported in the result summary. The standalone Node reference template keeps
  a local in-memory index for offline use.
- **Multi-select + bulk actions** — checkboxes, select-all with indeterminate
  state, shift-click range select, `X`/Shift+J/K keyboard selection, and bulk
  Archive, Move, Mark read, Mark unread and Delete with a server-issued undo
  token.
- **Undo send** — messages are queued server-side for a 5–10 second window
  (default 8s, configurable in Settings), with a countdown toast, status-bar
  queue chip, a queue panel, and canonical draft restore on undo.
- **Send later + Scheduled center** — compose exposes a Schedule send button
  with an Outlook-style scheduling workspace: next work slot, tomorrow
  morning/afternoon, Monday morning, or a custom date/time. The dialog previews
  the exact send moment in the user's time zone, flags sends outside working
  hours, shows a relative countdown, and queues the message server-side.
  Scheduled messages appear in a dedicated **Scheduled** folder and management
  view with summary tiles, day-grouped cards, and full **Send now**, **Edit**
  and **Cancel** actions. Edit changes recipients, Cc, subject, body and send
  time in place; Send now confirms, delivers immediately and offers a shortcut
  to Sent; Cancel restores an editable draft. The footer chip also exposes the
  same actions in a compact queue popover.
- **Undo move/delete** — folder moves, archives and deletes return an undo
  token valid for 30 seconds; the toast exposes Undo for 12 seconds. Permanent
  deletes from Trash can be undone as well.
- **Cabinet and connected storage** — the Files workspace is now **Cabinet**,
  with a filing-cabinet icon and a disk-with-cloud mark for Alt8Mail Drive. It
  stores uploads and mail attachments. A per-user default storage provider is
  used for **Save to Cabinet** and for the compose "Attach from" picker.
  Tenant owners and administrators can configure Google Drive, Dropbox,
  OneDrive and S3/MinIO credentials in the storage modal or Settings → Storage;
  OAuth client secrets and S3 secret keys are sealed in PostgreSQL.
- **Attachment save actions** — received attachments expose individual **Save**
  actions and **Save all to Cabinet**, with the destination following the
  user's default storage provider.
- **DNS-gated sending** — outbound send is refused until the sender domain has
  a matching `_a8m-verification` TXT value plus the required MX, SPF, DKIM and
  DMARC records. The webmail shows a structured missing-record dialog instead
  of sending in the background.
- **Server-backed compose sessions** — every compose window is a server draft,
  autosaved on a debounce and flushed on tab hide. Reload/navigate and the
  compose windows come back with recipient, subject, body, attachments and the
  selected From identity. Drafts persist across restarts.
- **Recipient chips and address-book autocomplete** — To, Cc and Bcc tokens are
  committed on Enter, comma or semicolon; autocomplete searches the internal
  directory and personal contacts as you type; external recipients are saved
  once as auto-saved contacts and appear in People under **Your contacts**.
- **Keyboard-first navigation** — `J/K`, `C`, `/`, `?`, `X`, `E` archive,
  `#` delete, `U` unread, `R` reply, `A` reply-all, `F` forward, `S` star.
  Shortcuts are suppressed while typing or in a visible compose window.
- **Send-as identities** — four aliases in the user record; three are sendable.
  The compose From selector and header badge are wired to them, the server
  validates the alias, and non-delegated shared mailboxes are read-only.
- **Sieve rules UI** — list, enable/disable, reorder, create, edit and delete
  rules plus a generated Sieve script preview. Validation and script rendering
  live server-side in `server/sieve.mjs`.
- **Automatic replies** — an Outlook-style out-of-office panel with an on/off
  switch, optional date range, reply interval, separate internal and external
  messages, live preview, and a policy note. The server stores both audiences
  in the Sieve `vacation` model and includes the external branch only when
  enabled.

### Enterprise differentiators

- **Sender trust panel** — SPF/DKIM/DMARC result chips, signing domain, sender
  IP, TLS version, first-contact state, authentication warnings, an
  external-sender banner (including a red failed-auth variant), security
  details and per-sender trust. The demo reads structured `auth` results from
  the stored message; `server/app.mjs` marks the adapter point for
  `internal/dkim` + live DNS.
- **Outbound caps** — per-user limit (200 / rolling 24h) shown in the status
  bar, the Security settings usage card and a status popover, with queued
  sends counted toward the cap. `POST /api/send` returns `429 OUTBOUND_CAP` when
  exceeded. The blueprint maps this to postfwd.
- **Quota and usage** — the server computes mailbox usage from the stored
  messages on top of a Dovecot baseline, exposes `/api/quota`, updates it after
  permanent deletes, and shows it in the folder pane, status bar, storage card
  and a breakdown popover.
- **Audit log** — login, failed login, logout, 2FA challenges, send
  queue/schedule/reschedule/send-now/cancel/commit,
  archive/move/delete/mark/star/flag/spam/report, drafts, tasks, files, storage
  connectors, preferences, rule changes, trust changes and demo reset.
  Filterable UI, server-side CSV export, seven-year retention note.
- **Two-factor sign-in** — the standalone Node template demonstrates the flow
  with a simulated passkey and a demo code `123456`; its TOTP and single-use
  recovery-code logic is real. The live Go staging backend is different: it
  verifies real TOTP and WebAuthn assertions server-side, stores hashed recovery
  codes, and stops un-enrolled users at a first-time enrollment screen when the
  per-user policy is enabled. Public payloads never include the TOTP secret or
  unused recovery codes.
- **Tasks workspace** — enterprise to-do lists with four dashboard tiles (all,
  due today, overdue, completed), filters (mine/team/today/overdue/completed),
  priorities, due dates and times, tags, assignee validation against the
  directory, linked email conversations, notes, and complete/reopen/delete
  flows. Stats and list ordering respect the user's time zone.
- **Files workspace** — a unified enterprise file shelf across Alt8Mail Drive,
  Microsoft OneDrive, Dropbox Business, Google Drive and AWS S3. Connectors
  have connected/disconnected states, account and usage details; connecting a
  provider indexes its mock files, file filters cover all/recent/shared/images/
  documents/starred, and detail views expose owner, version, path, sharing,
  preview, download, star and delete. Sharing creates expiring external links
  with view/edit access. Uploads are stored as real binary attachments before
  being added to the shelf.
- **Regional and sending settings** — interface locale, 12/24-hour time,
  date format, IANA time zone, calendar week start, working hours/days, default
  From identity, signature (inserted into new mail), and auto-advance after
  archive/delete/junk. These values drive list/reader dates, task due labels,
  schedule presets and the full date/time shown in the schedule dialog.
  *Language controls the JSON UI catalogs under `public/locales/` as well as
  `Intl` locale, date, number and time formatting. English, French, Spanish and
  Arabic catalogs ship with this release; missing keys fall back to English.*
- **PWA + mobile** — manifest with 192/512/maskable icons and shortcuts, a
  service worker app-shell cache (API responses are never cached), install
  prompt handling, offline banner, installability passing Chrome checks, a
  six-item mobile tab bar (Mail, Calendar, People, Tasks, Files, Settings),
  drawer, single-pane reader and full-screen compose.
- **Dark mode + accents** — the Alt8Mail token system with Light / Dark / System,
  four accent colors and comfortable/compact density, persisted per browser.
- **Calendar + directory** — the Alt8Mail month grid and agenda are wired to the
  Go API. People groups the internal directory and personal contacts, with
  search, source badges, filters and contact detail/editing. The live staging
  build stores calendar events, attendees and personal contacts in normalized
  tables, with create/edit/delete UI plus CalDAV/CardDAV access. The standalone Node template
  still uses its demo fixtures.

### Deliberately deferred

These are documented next steps, not hidden gaps. Items marked **Node
reference** apply to the standalone template; the live Go staging backend has
already replaced the passkey simulation with real WebAuthn.

- **OpenPGP / Enigma-style encryption** — not implemented.
- **Shared mailboxes and delegation** — aliases are read-only/ sendable as
  configured, but delegated mailbox management is not implemented.
- **Node reference passkey simulation** — the standalone template's passkey
  button demonstrates the product flow without a credential ceremony. The live
  Go backend implements real WebAuthn: `navigator.credentials.get()`,
  server-side signature/origin/RP-ID/counter verification and PostgreSQL
  credential storage.
- **Live provider OAuth credentials** — the Go backend implements Google,
  Dropbox and OneDrive authorization/refresh/upload/download/sync, but staging
  has no provider apps configured. S3/MinIO is the end-to-end validated
  connector.
- **AI provider credentials** — the tenant AI assistant, compose generation,
  summaries and translation are implemented and were exercised against a local
  OpenAI-compatible mock. Staging has no customer OpenAI/Claude/DeepSeek key
  configured, so the AI controls stay hidden until an administrator enables and
  tests a provider in the admin console.
- **External identity deliverability** — verification emails are sent through
  the normal Postfix submission path. An external identity can only send
  successfully when its domain publishes SPF/DKIM aligned for Alt8Mail's egress;
  otherwise recipients may reject or junk the mail even though the identity is
  verified.
- **Notification read state** is per-browser (localStorage) rather than synced
  across devices. Notification content is live; only "read/dismissed" is local.
- **Inline editor images and tables** — the WYSIWYG toolbar covers headings,
  emphasis, lists, links, quotes, code, alignment and color, but does not embed
  images inside the HTML body; files remain proper MIME attachments.
- **Bootstrap pagination** — `/api/bootstrap` and the message list still return
  the whole mailbox in one response. PostgreSQL FTS, IDLE and normalized rows
  remove the write/search bottlenecks, but a windowed bootstrap is the next
  step for extreme single-user mailboxes.
- **CalDAV/CardDAV extensions** — one default calendar and address book, no
  VTODO/VJOURNAL, multiple collections, sharing/ACLs or iTIP scheduling yet.
  Basic discovery and CRUD are live; test target clients before rollout.
- **Rules execution** — the UI edits structured rules and generates a Sieve
  script. Dovecot's Sieve interpreter runs it in production; the standalone
  Node demo does not deliver new mail through it.

### Recommended enterprise settings

The template already exposes sign-in protection, recovery codes, regional
formatting, working hours, default identity, signature, auto-advance, automatic
replies, external-sender warnings, retention, quota and outbound controls. The
next settings layer I would add for a business/enterprise customer, in priority
order:

1. **Tenant policy console** — enforce MFA and passkeys, session idle/absolute
   timeouts, trusted-device lifetime, IP allowlists, and per-role send
   restrictions. These are the settings an admin must own centrally, not the
   end user.
2. **DLP and retention** — classification labels, policy tips, blocked external
   recipients/domains, legal hold, eDiscovery export, retention by folder and
   message expiration. The existing audit log is the natural event source.
3. **Phishing triage loop** — turn the reader's Report action into a tenant
   queue with allow/block sender lists, campaign clustering, quarantine release
   and a feedback status visible to the reporter.
4. **Shared mailboxes and delegation** — mailbox ACLs, send-on-behalf, folder
   sharing, out-of-office for shared mailboxes, and audit attribution.
5. **Encryption policy** — S/MIME or OpenPGP key discovery/verification, TLS
   enforcement per recipient domain, and an optional expiring-message policy.
6. **Directory and lifecycle** — SCIM/HRIS sync, groups and distribution lists,
   delegated admin roles, offboarding rules (transfer mail/files/tasks) and
   directory visibility controls.
7. **Worker-facing productivity** — snooze, follow-up reminders, focused inbox,
   unsubscribe management, rich rule templates, saved searches, and
   calendar-aware automatic replies.
8. **Locale and accessibility** — translation packs per tenant, RTL layouts,
   per-user language/time zone defaults, screen-reader and reduced-motion
   audits, and localized date/number formats (the regional model is already in
   place).
9. **Branding and deployment** — tenant logo/accent/email footer, custom
   domains, data-residency choices, SIEM webhook for audit events, backup and
   restore verification, and mobile-device policy guidance for the PWA.

## Architecture

```text
server.js                 launcher / port / graceful shutdown
server/
  app.mjs                 HTTP router, sessions, CSRF, all API endpoints
  store.mjs               atomic JSON data store (temp file + rename)
  seed.mjs                demo user, aliases, mailbox, tasks, files, rules, audit
  sieve.mjs               rule validation + Sieve script generation
  twofactor.mjs           TOTP, base32, HOTP window and recovery-code helpers
  util.mjs                cookies, CSP, byte/format helpers, errors
public/
  index.html              Alt8Mail shell
  styles.css              Alt8Mail design system
  features.css            threaded list, bulk, rules, trust, popover layer
  workspace.css           Tasks, Files, 2FA, scheduling, preview and settings UI
  features-0.7.css        boot splash, notifications, AI, composer and 0.7 settings
  app.js                  application behavior (ES module)
  search.mjs              shared search parser + inverted index + threading
  manifest.webmanifest    PWA manifest
  sw.js                   app-shell service worker
  icons/                  generated 192/512/maskable/apple icons
  samples/                seeded PDF/SVG preview fixtures
tests/
  api.test.mjs            node:test API suite
  e2e.mjs                 CDP-driven Chromium flows + screenshots
tools/
  cdp.mjs                 tiny Chrome DevTools Protocol driver
  compose-notifications-smoke.mjs  0.7 composer/notification/identity UI smoke
  ai-reading-compose-smoke.mjs     0.8 AI/reading/font/minimize/view UI smoke
  app-password-demo-smoke.mjs      demo-alias re-authentication/app-password smoke
  make-icons.mjs          dependency-free PNG renderer for PWA icons
  make-samples.mjs        generates the seed PDF/SVG fixtures
artifacts/                E2E screenshots (generated)
data/db.json              runtime persistence (seeded on first run)
data/uploads/             uploaded attachment binaries (runtime)
```

The frontend is intentionally a plain ES module. The backend is intentionally
small and adapter-shaped: replace one function or endpoint group at a time with
your Go services without redesigning the UI.

### Data model (summary)

- `user` — identity, scrypt password, quota baseline, aliases, trusted senders,
  outbound baseline/events, regional/sending preferences, and `security.twoFactor`
  (enabled flag, TOTP secret, passkeys, one-time recovery codes). `publicUser()`
  strips the secret and unused codes before reaching the browser.
- **Node reference only:** `messages` — one demo document per message *and* one
  per draft, with folder, threadId, from/to/cc/bcc, subject, bodyHtml/bodyText,
  date, unread, starred, flagged, attachments metadata, categories,
  external/auth, sizeBytes, messageId, inReplyTo, draft/windowMode and optional
  `spamReport`. The live Go API does not read this document.
- **Go staging mail tables** — `mail_messages` (one row per mailbox copy:
  folder/IMAP UID, Message-ID/thread, envelope, body, flags, draft and trust
  fields, generated `search_vector`), `attachments` (SHA-256 metadata and
  storage path), `mail_message_attachments`, `outbox` (scheduled/delivery
  state) and `undo_actions`. Migrations `0011`–`0013` created these tables,
  backfilled legacy documents and removed the `messages`, `attachments`,
  `outbox` and `undo` keys from `app_states`.
- **Go staging workspace tables** — `tasks`, `files`, `file_shares`,
  `calendar_events`, `calendar_attendees`, `contacts`, `storage_accounts`,
  `storage_oauth_states` and `workspace_audit`; workspace arrays are removed
  from `app_states` by migration `0007_workspace.sql`.
- **Go 0.7 tables** (migration `0014_compose_identity_ai.sql`) —
  `mail_identities` holds self-service sending identities and hashed 48-hour
  verification tokens; `tenant_ai_settings` stores the encrypted tenant AI
  provider configuration; `mail_ai_cache` caches summaries/translations per
  message, user and language. `app_passwords` gains `revoked_at` for history and
  `outbox` gains `body_html` for rich messages.
- **Go DAV and credential tables** — `app_passwords` stores SHA-256 hashes of
  high-entropy client tokens plus creation, last-use, expiry and revocation
  timestamps; calendar/contact rows carry UID, resource name and ETag fields
  used by CalDAV/CardDAV.
- **Notification read state** is per-browser/per-account in `localStorage`;
  notification content itself is derived live from mailbox, calendar, pending
  send, quota and security-audit data, so no separate notification table exists.
- The Node reference template (not the live Go API) stores `tasks`, `files`,
  `storageProviders` and `calendarEvents` as demo documents.
- **Node reference only:** `attachments` metadata keyed by attachment id with
  bytes under `data/uploads/`; `sieve`; `audit` (capped at 1000); `outbox`; and
  `undo` snapshots. The Go staging backend uses the normalized tables above and
  the `sieve`/`audit` sections of `app_states`, with attachment bytes under the
  configured upload directory and S3 keys for external files.
- `calendarEvents`, `folders`, `people`.

## Search syntax

| Example | Meaning |
| --- | --- |
| `from:elena` | sender name or address contains `elena` |
| `to:priya`, `cc:marcus` | recipient filters |
| `subject:invoice` | subject contains |
| `body:"launch blocker"` | body contains phrase |
| `has:attachment`, `has:pdf` | has any file / extension |
| `filename:roadmap` | attachment filename contains |
| `before:2026-11-01` | sent before that date |
| `after:2026-10-01` | sent on/after that date |
| `is:unread`, `is:starred` | state filters |
| `in:archive`, `in:inbox` | restrict folder |
| `larger:1M`, `smaller:500k` | size filters |
| `from:elena has:attachment before:2026-11-01` | operators AND together |

Search defaults to **all mail**; the scope row shows the folder and parsed
tokens. Clearing the query returns to the current folder.

## Keyboard shortcuts

`J` / `K` next/previous conversation · `Shift+J/K` extend selection · `X` select
· `C` compose · `R` reply · `A` reply all · `F` forward · `E` archive · `#`
delete · `U` mark unread · `S` star · `/` or `Ctrl/Cmd+K` search · `?` shortcut
reference · `Esc` close panel/menu/reader/search.

## API overview

All endpoints are same-origin JSON. Mutations require the CSRF header obtained
from sign-in (`X-CSRF-Token`); the session is an HttpOnly cookie. Responses are
`no-store`.

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/session` | password or demo SSO sign-in; returns a 2FA challenge when enabled |
| POST | `/api/session/2fa` | verify TOTP or a recovery code and create the session |
| GET/DELETE | `/api/session` | current session / sign out |
| GET | `/api/bootstrap` | user, aliases, folders, people, messages, calendar, tasks, files, providers, sieve, quota, outbound, pending sends, capability list |
| GET/PATCH | `/api/preferences` | read/update all server-backed personal settings |
| POST | `/api/preferences/reset` | reset personal settings to DB defaults |
| GET | `/api/search?q=` | indexed search with parsed operators |
| GET | `/api/messages?folder=&q=&filter=` | message list + thread summaries |
| PATCH | `/api/messages/:id` | read/star/flag flags |
| POST | `/api/messages/read` | mark several messages read |
| POST | `/api/messages/actions` | bulk archive/move/delete/mark/star/flag/spam/notSpam/report; returns undo token and report metadata |
| POST | `/api/undo/:token` | restore a bulk action |
| GET/DELETE | `/api/drafts`, `/api/drafts/:id` | list / delete drafts |
| PUT/POST | `/api/drafts/:id` | create or autosave a compose draft |
| POST | `/api/send` | queue a send (with optional `scheduledFor`) ; validates alias, recipients and caps |
| GET | `/api/outbox` | pending and scheduled sends (includes editable recipients/body for scheduled mail) |
| PATCH | `/api/outbox/:id` | edit a scheduled message's recipients, Cc, subject, body, attachments or send time |
| POST | `/api/outbox/:id/send-now` | deliver a scheduled message immediately and record the original schedule |
| POST | `/api/outbox/:id/cancel` | undo a queued/scheduled send, restoring its draft |
| GET/POST | `/api/tasks`, `/api/tasks/:id` | list/create/update/delete enterprise tasks |
| GET | `/api/storage` | storage connector states |
| POST | `/api/storage/:id/connect`, `/disconnect` | connect or disconnect a provider |
| GET/POST | `/api/files` | list or add files to the shelf |
| PATCH/DELETE | `/api/files/:id` | update metadata / remove a file |
| POST | `/api/files/:id/share` | create an expiring external share link |
| POST | `/api/attachments` | upload base64 attachment content (8 MB limit, MIME sniffing) |
| GET | `/api/attachments/:id/content` | inline preview or attachment download |
| GET/POST | `/api/security/recovery-codes` | recovery-code status / regenerate hashed one-time codes |
| POST | `/api/security/2fa/totp/begin`, `/finish`, `/disable` | TOTP enrollment, verification and removal |
| POST | `/api/security/webauthn/register/begin`, `/finish` | WebAuthn passkey registration |
| DELETE | `/api/security/webauthn/:id` | remove a registered passkey |
| GET/PATCH | `/api/security/policy` | first-time enrollment policy and live security summary |
| GET | `/api/security/sessions` | active browser sessions |
| POST | `/api/security/sessions/revoke-others`, `/:id/revoke` | sign out other/selected sessions |
| POST | `/api/session/2fa/webauthn/begin`, `/finish` | real WebAuthn sign-in assertion |
| POST | `/api/enrollment/totp/begin`, `/finish` | first-time TOTP enrollment |
| POST | `/api/enrollment/webauthn/begin`, `/finish` | first-time passkey enrollment |
| GET | `/api/messages/:id/trust` | sender auth panel |
| POST | `/api/messages/:id/trust` | mark/unmark sender trusted |
| GET/PUT | `/api/sieve` | read/save rules + internal/external vacation; returns generated script |
| GET | `/api/sieve/script` | plain-text Sieve script |
| GET/PUT | `/api/forwarding` | read/save dated or permanent mail forwarding |
| GET/POST | `/api/identities` | list/add verified sending identities |
| POST | `/api/identities/:id/resend` | resend an external verification email |
| DELETE | `/api/identities/:id` | remove a custom sending identity |
| GET/POST | `/api/identities/verify` | public confirmation page and token submission |
| GET/POST/DELETE | `/api/security/app-passwords` | expiring/revocable client credentials with history |
| POST | `/api/ai/compose` | generate an email draft from notes/style/length/creativity/language |
| POST | `/api/ai/messages/:id/summary` | cached summary in the user's language |
| POST | `/api/ai/messages/:id/translate` | cached translation in the user's language |
| GET | `/api/audit` | filtered audit entries (`login`, `send`, `message`, `rule`, `draft`, `task`, `files`, `preferences`) |
| GET | `/api/audit.csv` | CSV export |
| GET | `/api/quota` | live quota snapshot |
| GET | `/api/outbound` | outbound usage |
| POST | `/api/mail/sync` | reconcile all standard IMAP folders for the signed-in mailbox |
| GET | `/api/mail/sync` | IMAP sync status, last error, folder checkpoints and pending backfill |
| GET/PATCH | `/api/profile` | read/update self-service profile fields (policy-gated) |
| GET/POST | `/api/security/app-passwords` | list/create revocable DAV client tokens; token returned once |
| DELETE | `/api/security/app-passwords/:id` | revoke an app password |
| GET/POST | `/api/calendar/events` | list/create normalized calendar events |
| PATCH/DELETE | `/api/calendar/events/:id` | update/delete a calendar event |
| GET/POST | `/api/contacts` | list/create personal contacts |
| PATCH/DELETE | `/api/contacts/:id` | update/delete a personal contact |
| GET | `/api/directory` | internal users + policy-filtered personal contacts |
| GET | `/api/files/:id/content` | authorized file content (internal/S3/OAuth) |
| GET | `/api/public/shares/:token` | guest share metadata; content and replace subpaths |
| GET | `/files/s/:token` | public guest share HTML page |
| PROPFIND/REPORT/GET/PUT/DELETE | `/dav/` | CalDAV/CardDAV discovery, collections and resources |
| GET | `/.well-known/mta-sts.txt` | MTA-STS policy selected by `mta-sts.<domain>` Host |
| GET | `/.well-known/caldav`, `/.well-known/carddav` | DAV discovery redirects |

The deployed Go API uses the same paths. Its session cookies are
`a8m_system_sid` / `a8m_system_csrf`, and the staging login alias maps
to the canonical `.test` account. The reference demo-reset endpoint has been
removed from the Go API; initialize a clean real mailbox with
`a8m-inituser`.

The separate administration API (`/admin/api` on staging, or `/api` on the admin
subdomain), permission catalog, tenant policy model and RBAC are documented in
[`docs/ADMIN.md`](docs/ADMIN.md). Admin CSRF uses `a8m_admin_csrf` and
sessions use `a8m_admin_sid`.

## Go backend build and verification

```bash
cd backend
go test ./...
go vet ./...
go build ./...
go build -o bin/a8m-inituser ./cmd/inituser
go build -o bin/a8m-admin-server ./cmd/adminserver
go build -o bin/a8m-mailprovision ./cmd/mailprovision
go build -o bin/a8m-dkimprovision ./cmd/dkimprovision

# migrations 0003_real_auth through 0014_compose_identity_ai must be applied first on
# staging, after loading the service environment:
# bin/a8m-inituser -email avery.chen@a8m.test
# systemctl restart a8m-server
# systemctl enable --now a8m-mailprovision.timer a8m-dkimprovision.timer
```

`a8m-inituser` replaces any demo/legacy state with a clean identity,
aliases and tenant directory. It writes **zero** messages, tasks, files,
calendar events and audit rows; the IMAP worker imports the real mailbox.

The staging smoke tests are kept in the repository:

```bash
# API: login, bootstrap, actions/undo, tasks, drafts, scheduled send, SMTP,
# attachments, storage, Sieve, preferences, search, audit and trust
GO_API=https://130-94-46-22.sslip.io/system/api \
GO_PASSWORD_FILE=./staging_avery_password \
python3 tools/go-api-smoke.py

# Browser: sign in, threads, search, Scheduled, Tasks, Files, People, Calendar
ALT8MAIL_UI_PASSWORD="$(cat ./staging_avery_password)" \
node tools/go-ui-smoke.mjs

# Every settings tab, with /api/preferences DB readback
ALT8MAIL_UI_PASSWORD="$(cat ./staging_avery_password)" \
node tools/go-settings-smoke.mjs

# Real TOTP, recovery codes, WebAuthn passkeys and first-time enrollment
ALT8MAIL_UI_PASSWORD="$(cat ./staging_avery_password)" \
node tools/go-auth-smoke.mjs

# Self-delivery appears in the open INBOX without a reload, with a notification
ALT8MAIL_UI_PASSWORD="$(cat ./staging_avery_password)" \
node tools/go-live-mail-smoke.mjs

# 0.7 composer, live notifications, identities, app-password controls and
# closed-draft reload behavior
ALT8MAIL_UI_PASSWORD="$(cat ./staging_avery_password)" \
node tools/compose-notifications-smoke.mjs

# 0.8 five-second splash, manual AI summary/translation, Reply with AI,
# font controls, blue minimized dock tab and grouped/individual inbox view
ALT8MAIL_UI_PASSWORD="$(cat ./staging_avery_password)" \
node tools/ai-reading-compose-smoke.mjs

# App-password re-authentication through the staging demo alias.
# Supply the gated demo password without putting it in shell history.
ALT8MAIL_UI_DEMO_PASSWORD=... \
node tools/app-password-demo-smoke.mjs

# Calendar/contact/profile edits and app-password create/revoke through the UI
ALT8MAIL_UI_PASSWORD="$(cat ./staging_avery_password)" \
node tools/workspace-ui-smoke.mjs

# Customer/system administration API, RBAC, policy, tenant lifecycle, admin MFA
ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password python3 tools/admin-api-smoke.py
ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password python3 tools/admin-auth-smoke.py
ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password python3 tools/admin-policy-smoke.py
ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password python3 tools/admin-tenant-smoke.py
ALT8MAIL_ADMIN_PASSWORD="$(cat ./staging_avery_password)" node tools/admin-ui-smoke.mjs

# Password flow, mail-client policy gates, forwarding, sharing, quarantine,
# retention and explicit Maildir provisioning. Port 587 is often blocked from
# operator networks; open an SSH tunnel and point the test at localhost.
ALT8MAIL_SMTP_HOST=127.0.0.1 ALT8MAIL_SMTP_PORT=1587 \
  python tools/mail-policy-smoke.py

# DKIM public key in the admin API, exact p= matching and readiness state
ALT8MAIL_ADMIN_PASSWORD_FILE=./staging_avery_password \
  python tools/dkim-smoke.py

# CalDAV/CardDAV discovery and event/contact CRUD. Use an app password when
# the account has MFA; DAV_INSECURE=1 only for a self-signed staging cert.
GO_API=https://130-94-46-22.sslip.io \
DAV_EMAIL=avery.chen@a8m.test \
DAV_PASSWORD="$(cat ./staging_avery_password)" \
  python3 tools/dav-smoke.py

# Calendar/contact/profile/app-password UI flows
ALT8MAIL_UI_PASSWORD="$(cat ./staging_avery_password)" \
  node tools/workspace-ui-smoke.mjs
```

`tools/go-api-smoke.py` needs Python 3 and a password file readable by the
operator; do not put the secret in shell history. The UI smoke uses the
prefilled staging demo login if `ALT8MAIL_UI_PASSWORD` is omitted.

## Security notes

Responsible disclosure and the production hardening checklist are in
[`SECURITY.md`](SECURITY.md).

- **Standalone Node reference template:** the demo password is hashed with
  scrypt, while sessions and 2FA challenges are in memory, so a restart requires
  a fresh sign-in (mailbox data persists).
- **Live Go staging backend:** Argon2id web passwords, opaque PostgreSQL
  sessions, PostgreSQL `auth_challenges`, CSRF header validation,
  `SameSite=Lax` HttpOnly cookies, login/2FA rate limits, JSON content-type and
  size limits. Sessions are revocable from Account.
- **Two-factor boundary:** the Go backend verifies real TOTP and WebAuthn,
  seals TOTP secrets with AES-256-GCM and stores recovery codes and challenges
  hashed and single-use. The standalone Node template instead simulates the
  passkey branch and keeps seed recovery codes in clear JSON. Remove the Node
  `demoCode` and demo seed before deploying that template. Production should add
  step-up authentication for sensitive changes and KMS/HSM-backed secret
  wrapping.
- **DAV credentials:** CalDAV/CardDAV uses HTTP Basic over TLS. Accounts with
  TOTP/passkeys must use a revocable app password, so DAV cannot bypass the
  second factor. App-password tokens are 192-bit random values stored only as
  SHA-256 hashes; the plaintext token is shown once. Password changes clear the
  in-memory DAV password-verification cache.
- **MTA-STS verifier hardening:** policy fetches require HTTPS, do not follow
  redirects, ignore environment proxies and dial only resolved public IPs.
  Loopback, private, link-local, CGNAT and unspecified addresses are refused.
- **MTA-STS publishing:** the `id` in the `_mta-sts` TXT record is derived from
  the exact policy body, so mode/max-age/MX changes force a DNS update. The
  policy host requires its own publicly trusted certificate.
- **Guest shares:** tokens are random bearer values stored as SHA-256 hashes;
  expiry and revocation are enforced server-side, edit links are audited, and
  public pages set a no-store/no-index CSP. Treat a share link as a bearer
  credential; do not send it to unintended recipients.
- **Storage tokens:** OAuth refresh/access tokens are AES-GCM sealed with a key
  derived from the session secret. Rotating the session secret invalidates
  connector tokens. S3 credentials are server-side only and never returned to
  the browser.
- CSP, `X-Content-Type-Options`, `Referrer-Policy`, permissions policy and
  same-origin resource policy are set server-side. The CSP allows same-origin
  frames only so the PDF preview can render inside the app.
- Uploaded content is MIME-sniffed, size-limited and served with `nosniff`.
  Web attachment uploads and guest-share replacements are scanned with ClamAV
  INSTREAM when `ALT8MAIL_CLAMAV_ADDR` is configured; scanner errors fail closed.
  Uploaded SVG is not registered as previewable, and any SVG attachment response
  is sandboxed so it cannot execute script when opened directly. The live mail
  path also runs Postfix/Rspamd/ClamAV before delivery. Production should add
  content disarm and archive/decompression limits.
- Email HTML is sanitized client-side (scripts, event handlers, styles,
  dangerous URLs and frames removed) before rendering. In production, also
  sanitize on ingest and render in a sandboxed view if you allow remote images.
- Path traversal is blocked for static files; API data is outside the public
  root. Uploaded bytes are stored under `data/uploads/`, not in the web root.
- The staging login card prefills the gated demo alias by design. The alias is
  refused when `ALT8MAIL_DEMO_MODE=false` or `ALT8MAIL_ENV=production`. Remove
  the prefilled values, demo seed and demo reset endpoint before exposing the
  reference template publicly. The Go API has no demo reset endpoint; use
  `a8m-inituser` for a clean real mailbox.

## Testing

The Go staging checks are in **Go backend build and verification** above
(`go test ./...`, `go vet ./...`, `go-api-smoke`, `go-ui-smoke`,
`go-settings-smoke`, `go-auth-smoke`, `workspace-ui-smoke`,
`compose-notifications-smoke`, `dav-smoke`, `dkim-smoke`,
mail/policy/group/provisioning smokes). Artifacts for the workspace,
DAV, large-mailbox, MTA-STS, S3, ClamAV and policy checks are under
`artifacts/`. The Node suites below cover the standalone reference template.

System Control has its own integration and browser suites under `tools/`
(`control-api-smoke.mjs`, `control-ui-smoke.mjs`,
`control-ui-interactions.mjs`, `control-ui-writes.mjs`, `control-sync-smoke.mjs`,
`control-delivery-smoke.mjs`, `control-tls-smoke.mjs`, `control-caps-smoke.mjs`,
`control-journald-smoke.mjs`, `control-import-smoke.mjs`,
`control-backup-restore.mjs`, `control-outbound-smoke.mjs`,
`control-ports-save.mjs` and focused TLS/self-signed checks). They require
`CONTROL_PASSWORD_FILE` pointing at the root-only bootstrap password file on
the operator workstation. Artifacts are written under `artifacts/control-ui/`.
See [`docs/SYSTEM_CONTROL.md`](docs/SYSTEM_CONTROL.md) for the current 21/21 API,
32/32 route, 43/43 feature, 18/18 terminal and 28/28 interaction results.

The external security assessment, remediation retest and 1,000-user soak are
documented in [`docs/PENTEST_REPORT_2026-10-29.md`](docs/PENTEST_REPORT_2026-10-29.md).
The sustained run completed with 1,000/1,000 logins, 0 HTTP 5xx and p95 47.6 ms;
the mid-run fan-out test also found and closed a critical Message-ID isolation
bug in the normalized mail table.
The assessment scope and rules of engagement are in
[`docs/PENTEST_SCOPE.md`](docs/PENTEST_SCOPE.md). Soak evidence is written under
`artifacts/soak/`, including the host monitor, load summary and admin/control
plane probes.

The Node API suite (24 tests) covers auth/CSRF, 2FA passkey/TOTP/recovery and
one-time codes, bootstrap shape, threading, all search operators, read/star/flag
updates, bulk archive/move/delete + undo, live quota, draft
create/autosave/restore/discard, undo-send cancel and commit, scheduled-send
validation, editing, send-now and cancellation, outbound caps, regional preference validation,
tasks, storage connectors and file sharing, attachment upload/preview
disposition, Sieve validation/quoting/vacation (internal+external), sender
trust, spam/report controls and audit metadata, audit filtering/CSV,
persistence across restart, demo reset, static files, manifest/service worker
and security headers.

The E2E suite drives Chromium through two-factor sign-in, thread
collapse/expand, search with operators, multi-select archive/mark/move + undo,
keyboard shortcuts, compose autosave + reload restore, From alias wiring, undo
send, committed Sent delivery, Sieve editing, automatic replies, regional
settings, audit/account/security panels including recovery codes, sender trust,
calendar/directory, PDF and image attachment preview, pop-out windows,
flag/spam/report controls, task creation/completion, storage connection, file
sharing, send-later scheduling/cancellation, the Scheduled center (edit,
send now and View sent), PWA/installability, dark mode and a mobile viewport.
Screenshots are written to `artifacts/`.

## Integration blueprint

See [docs/INTEGRATION.md](docs/INTEGRATION.md) for the adapter-by-adapter plan:
`internal/store` identities, WebAuthn/TOTP, `internal/dkim` + DNS, postfwd caps,
Dovecot quota, managesieve, object storage and file connectors, auditlog,
CalDAV, directory, and the path to Bleve/FTS5, OpenPGP, delegation and
production attachment scanning.
