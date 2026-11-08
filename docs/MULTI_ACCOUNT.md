# Multi-account mail

Alt8Mail is moving from one native Dovecot mailbox per user to a mailbox-source
model: the native Alt8Mail mailbox plus optional connected IMAP/SMTP accounts.

## Phase 0 — foundations

Phase 0 adds the account and folder model and keeps the webmail UI unchanged.
Only the native account exists until Phase 1 adds the connected-account setup.

### Schema

`public.mail_accounts` stores one row per mailbox source:

- native Alt8Mail accounts (`kind='native'`, `provider='alt8mail'`)
- future connected accounts (`kind='external'`, provider `gmail`, `microsoft`
  or `imap`)
- IMAP/SMTP endpoints, status, sync timestamps and sealed credential columns
- `UNIQUE (user_id, provider, email)` and one native account per user

`public.mail_folders` stores the per-account folder tree, remote name,
display name, special-use role, UID state and last sync time.

The following tables now carry `account_id`:

- `mail_messages`
- `outbox`
- `mail_identities`

`mail_messages` also gets account-first indexes and a replacement for the old
IMAP UID index. UIDs are unique per IMAP account, not per Alt8Mail user, so the
unique index is now `(account_id, imap_folder, uid_validity, uid)`.

### Native compatibility

Migration `0057_mail_accounts` backfills one native account for every existing
user and seeds the six standard native folders (Inbox, Sent, Drafts, Archive,
Junk, Trash). New users get the same rows through database triggers.

Older code paths do not pass an account id. A `BEFORE INSERT OR UPDATE` trigger
fills the user's native account automatically. The trigger also rejects an
account that belongs to a different user, so a broken or malicious insert cannot
mix two people's mail.

Changing a user's email, display name or color keeps the native account in sync.

Delegated mailbox windows only see the native account. Connected personal
accounts will never be exposed through Alt8Mail mailbox delegation.

### API and policy

- `GET /api/accounts` returns the signed-in user's non-secret account list.
- `GET /api/bootstrap` now includes the same list under `accounts`.
- `mail.externalAccounts` is a tenant policy flag, enabled by default for
  tenants that have not explicitly turned it off. Phase 1
  enforces it when the connected-account routes are added.
- Server helpers resolve a request scope from the `account` query parameter or
  the `X-A8M-Account` header. An empty or `native` scope means the native
  account; `all` is reserved for future unified views. Every individual account
  id is checked against the signed-in user.

### Verification

The migration was applied to a clean PostgreSQL 15 database and to a database
that already contained a user, a message, an outbox item and a sending identity.
Both paths produced one native account, six native folders and matching
`account_id` values.

The integration test is opt-in:

```text
A8M_MAIL_ACCOUNTS_TEST_DSN='postgres://...' \
  go test ./backend/internal/httpapi -run MailAccountsPhase0 -count=1
```

It was also run as the restricted `a8m_app` database role after applying
`deploy/postgresql/grants.sql`.

## Phase 1 — connected accounts (implemented)

The webmail now supports external IMAP/SMTP accounts alongside the native
Alt8Mail mailbox:

### Where to connect an account

1. Open **Settings → Connected accounts**.
2. Choose **Add mail account**.
3. Pick a provider from the gallery — Gmail, Outlook, Hotmail, Yahoo Mail,
   iCloud Mail, AOL, Zoho, GMX, Yandex, Fastmail, Mail.com or Other IMAP/SMTP —
   then use Google/Microsoft sign-in or the provider's app password. Server
   settings are prefilled and can be adjusted under **Advanced server settings**.
4. Run **Test connection**, then choose **Connect account**. The account appears
   in the account switcher above the folder list.

The folder pane shows a mailbox card with the active account's avatar or
provider logo, name and address. Opening it lists the primary Alt8Mail mailbox,
every connected mailbox and the unified inbox when more than one mailbox
exists, with a Manage action and **Add mail account** in the menu. A compact
plus button stays next to the card for quick account setup. Delegated windows
show the delegated identity as a read-only card and cannot switch mailboxes.
An administrator can turn the feature off under **Mail access → Connected
accounts**; the Settings tab then explains that the organization disabled it.

- **Add account** from the folder-pane account switcher, or from Settings →
  Connected accounts. The three-step wizard presents provider logo tiles for
  Gmail, Outlook, Hotmail, Yahoo, iCloud, AOL, Zoho, GMX, Yandex, Fastmail,
  Mail.com and Other IMAP/SMTP, prefills each provider's servers, supports
  Google/Microsoft OAuth and app passwords, and tests IMAP and SMTP before
  saving. **Connect account** stays disabled until the test succeeds and is
  invalidated whenever a credential or server changes. Passwords are stored
  with the same AES-256-GCM workspace key used by the other connected services.
- **Server-side safety**: hostnames are resolved before dialing, private,
  loopback, link-local and carrier-grade NAT addresses are rejected, DNS is
  re-resolved at connection time, TLS is mandatory, and port 25 is blocked.
- **Per-account sync workers** discover the standard IMAP folders (Inbox, Sent,
  Drafts, Archive, Junk, Trash), read the special-use flags, and import recent
  headers and bodies. Existing code paths fill the native account automatically.
- **Account switching** reloads the mailbox with an `X-A8M-Account` scope.
  Messages, folder counts, search and Pulse use the active account. Delegated
  Alt8Mail windows never see connected personal accounts.
- **Unread at a glance** every account row shows an inbox envelope counter and
  the selector shows the total unread across all mailboxes. `GET
  /api/accounts/summary` returns per-account counts and the newest unread
  message; the realtime `mail` event and the 15-second monitor refresh it. When
  unread mail arrives in an account that is not active, the selector raises an
  in-place alert with the provider logo, sender, subject and unread total,
  pulses the account card, and switches to that mailbox when opened. Unified
  view suppresses these alerts because every mailbox is already visible.
- **Sending** uses the account's own SMTP server and From address. Gmail does
  not need a Sent copy because SMTP saves it; other providers receive an IMAP
  append to their Sent folder. Scheduled send, undo send, drafts and the outbox
  carry the account id.
- **Read/star/archive/move/delete** actions are applied to the provider over
  IMAP. Gmail's All Mail folder is used as the archive target without importing
  its duplicate message set. A later sync reuses an existing local row by
  account, message id and app folder, so a moved message or a locally recorded
  Sent copy is updated in place instead of appearing twice.
- **Reauthentication** is a first-class state: failed logins mark the account
  `reauth`, the settings list shows it, and entering a new password tests the
  connection and schedules a fresh sync.
- **Removal** deletes the cached messages, folders, outbox rows and identities
  for that account. Removing an account never changes the provider mailbox.
- **Policy**: `mail.externalAccounts` defaults to on. The webmail admin
  console exposes it under Mail access → Connected accounts, where an
  organization can turn it off.

Phase 2 completes the thin Phase 1 edges and adds OAuth2 and a unified view.

### Phase 1 completions in Phase 2

- **Custom folders and Gmail labels**: every selectable custom IMAP folder is
  mirrored as its own connected folder in the sidebar. Gmail labels appear as
  label folders; a message can therefore have one local row in Inbox and one in
  each label, matching Gmail's label model. Gmail's virtual All Mail is used as
  an archive target but is not mirrored as a second copy of every message. The
  sync caps a single account at 40 folders and imports the same recent window
  per folder.
- **External drafts**: compose autosave now replaces the draft in the provider's
  Drafts folder using a stable Message-ID, and discarding a draft expunges the
  remote copy. Failures are logged without losing the local draft.
- **Remote deletions**: each sync checks the cached UIDs against `UID SEARCH`.
  A message deleted or moved by another mail client leaves the local cache; a
  message outside the sync window is never touched.
- **Cache retention**: the local connected-account cache is trimmed to the newest
  3,000 non-starred, non-pinned messages and to 365 days. Starred, pinned and
  draft messages are kept. This never deletes anything on the provider.
- **Server changes**: Settings → Connected accounts → Servers can change the
  username, IMAP/SMTP hosts, ports and TLS mode. The new settings are tested
  with the existing credential before they are saved.
- **Reauthentication**: OAuth accounts show a Reconnect action that runs the
  provider sign-in again; password accounts keep the password/app-password
  dialog. Switching an account to an app password clears its stored OAuth
  tokens.

The initial import remains 90 days and up to 200 of the newest messages per
folder; incremental sync drains later messages in batches. The local cache is
the working set, not a replacement for provider retention.

### OAuth2

- Google and Microsoft OAuth use the authorization-code flow with PKCE (S256)
  and a one-time, ten-minute state row. The PKCE verifier is sealed with the
  workspace key. Callback URLs are:

  ```text
  https://mail.example.com/api/mail/oauth/gmail/callback
  https://mail.example.com/api/mail/oauth/microsoft/callback
  ```

- Configure a Google OAuth app with the Gmail scope (`https://mail.google.com/`)
  and a Microsoft app registration with delegated `IMAP.AccessAsUser.All`,
  `SMTP.Send`, `openid`, `email`, `profile` and `offline_access`. Microsoft
  identity is read from the ID token so the Outlook-scoped access token is
  never sent to Microsoft Graph.
  Set `ALT8MAIL_GOOGLE_MAIL_CLIENT_ID` / `ALT8MAIL_GOOGLE_MAIL_CLIENT_SECRET`
  and `ALT8MAIL_MICROSOFT_MAIL_CLIENT_ID` /
  `ALT8MAIL_MICROSOFT_MAIL_CLIENT_SECRET`. Microsoft defaults to the `common`
  Entra tenant; set `ALT8MAIL_MICROSOFT_MAIL_TENANT` to `organizations`,
  `consumers` or a tenant id for a single-tenant app. When the mail-specific
  variables are empty, the existing storage connector IDs are used as a
  fallback; the callbacks above must still be registered on those apps. The
  Gmail scope is a restricted Google scope, so a public deployment must
  complete Google's verification or use a Workspace-internal app.
- Gmail uses IMAP `imap.gmail.com:993` (TLS) and SMTP `smtp.gmail.com:587`
  (STARTTLS). Microsoft uses `outlook.office365.com:993` and
  `smtp.office365.com:587`. Both authenticate with SASL XOAUTH2. A fresh token
  is checked against IMAP before a new account is stored.
- Access tokens are refreshed automatically before they expire. Refresh-token
  rotation is stored, and a rejected refresh marks the account `reauth` instead
  of retrying with a dead token.
- Microsoft 365 tenants can disable SMTP AUTH independently of IMAP. In that
  case the account still syncs and reads, but sending reports the SMTP
  authentication error. The tenant administrator must enable SMTP AUTH for the
  mailbox (or the account must use a provider that allows it).
- App passwords remain available for Gmail and generic IMAP servers.

### Unified inbox

- The account switcher gains **All accounts** when at least one connected
  account is present. The browser sends `X-A8M-Account: all`; reads, searches,
  folder counts, drafts and Pulse cover every account the organization allows.
- Rows in the unified list carry an account dot and label. Folder actions are
  resolved per message, so read/star/archive/move/delete still reach the correct
  provider. A move to a standard folder is translated separately for each
  account; a move to a folder that exists only in one account is skipped for
  the others.
- Compose in the unified view starts from the native identity and keeps the
  normal From selector, so a connected identity can still be chosen explicitly.
- Unified views show the native folder tree and standard connected folders.
  Custom connected folders remain account-specific. Conversations are not
  merged across accounts even when subjects match, because their opaque thread
  ids are account-scoped.

Gmail labels and Microsoft OAuth were the Phase 1 gaps; Microsoft 365 mailboxes
no longer need an app password.

### Phase 2 limits

- Gmail's All Mail is an archive target, not a mirrored folder. A Gmail message
  that has been archived and carries no label may therefore be absent from the
  local cache. Label folders are mirrored copies, so one message can appear in
  several folder views; Pulse de-duplicates by account and Message-ID, while
  folder counts remain per-view.
- Moving a message into a Gmail label uses IMAP folder/move semantics. Alt8Mail
  does not yet offer add-label/remove-label actions.
- The unified view shows the native folder tree and standard connected folders.
  Custom connected labels are available inside the account view.
- OAuth requires a provider app and the callback URLs above. Gmail's restricted
  scope needs Google verification for a public app. Microsoft tenants that
  disable SMTP AUTH can read but not send through that account until the tenant
  enables it.
- OAuth was exercised against local HTTP fixtures, not a live Google or
  Microsoft tenant. The browser E2E suite covers the webmail; the provider
  consent screen is outside that fixture.
- The cache is bounded as described above and is not a complete provider
  archive.

### Phase 2 verification

`0058_mail_phase2` was applied on PostgreSQL 15 and the integration test ran as
the restricted `a8m_app` role, checking the OAuth state table, the custom folder
view and user/tenant cleanup. Unit tests cover PKCE, both XOAUTH2 SASL clients,
provider scopes, token and user-info error handling, the OAuth result page,
unified scope conditions, folder roles and UID caps. The Go suite, Node API
tests, i18n catalogs and browser E2E suite pass. OAuth cannot be exercised
end-to-end without a real provider app; the token exchange and user-info calls
are tested against local HTTP fixtures, while IMAP/SMTP XOAUTH2 is tested at the
protocol boundary.

## Phase 3 — cross-account workspace

- **AI and commitments** use the identity of the account that owns the
  conversation. Reply drafting, tone suggestions and commitment extraction no
  longer describe an external message as if it belonged to the native mailbox.
  Label copies are de-duplicated before the model sees a thread, and commitment
  results carry the source account id and email. Attachment context, message
  summary and translation continue to resolve through the same message ids.
- **Push notifications** include connected-account messages. The push payload
  names the account when the sender/subject preferences are enabled, carries the
  account id/email, and deep-links to `/?account=<id>#/Inbox`; the webmail
  switches to that account on load. The existing cursor means the initial
  connected-account backfill is not pushed as new mail.
- **Calendar and tasks**: calendar parts from connected messages are stored as
  regular `text/calendar` attachments, so the existing invitation card and
  import flow work. Importing an invitation uses the connected account address
  as the local attendee/organizer identity, and inbound iMIP replies and
  counters found by a connected sync are applied to the same calendar events.
  Tasks keep their linked thread id, which is already unique across accounts.
- Provider-native Gmail/Microsoft APIs are optional and remain disabled; the
  IMAP/SMTP path is the supported integration for self-hosted deployments.

## Phase 4 — hardening

- **SSRF**: connected mail endpoints require public unicast addresses. In
  addition to private, loopback, link-local, multicast and CGNAT ranges, the
  dialer rejects documentation, benchmarking, reserved, NAT64 and 6to4 ranges;
  DNS is resolved again at dial time and TLS is mandatory.
- **Secret rotation**: set `ALT8MAIL_SESSION_SECRET_PREVIOUS` to the old secret
  while changing `ALT8MAIL_SESSION_SECRET`. Sealed storage, AI, add-on and
  connected-account secrets remain readable through the fallback key while new
  writes use the new key. Re-save or reconnect credentials to re-seal them, then
  remove the previous value. Removing it earlier makes old sealed values
  unreadable and forces a reconnect.
- **Rate limits**: account create/test/update/delete/sync and OAuth starts are
  limited to 30 actions per user per ten minutes. The OAuth popup shows a
  readable message when the limit is reached.
- **Load checks**: `go test ./internal/httpapi -run '^$' -bench 'Unified|Dedupe' -benchmem`
  exercises the unified SQL condition and thread de-duplication; scenario load
  remains available through `cmd/soakload`.
- **Migration rollback**: `backend/migrations/rollback/0058_mail_phase2_down.sql`
  and `0057_mail_accounts_down.sql` reverse the two mail migrations in order.
  The 0057 rollback is destructive and restores the old per-user IMAP UID index;
  take a backup and run 0058 down first.
- **Accessibility and i18n**: account management controls have accessible labels,
  live status regions announce connection progress, and the new account strings
  are translated in all eight base catalogs.

### Phase 3/4 verification

The Phase 3/4 paths are covered by the existing Go, Node, i18n and browser
suites plus unit tests for account identity in AI, account-aware calendar
import/replies, push payload construction, SSRF ranges, rate limiting and
workspace-key rotation. Provider-native APIs are intentionally not claimed as
live integration.
