# Alt8Mail Add-ons

Alt8Mail add-ons are a **curated, first-party-reviewed directory**. They are not an open marketplace and they do not execute third-party code inside the webmail page. Each add-on is a small connector owned by Alt8Mail that talks to a provider's public API using the credentials a workspace or user explicitly connects.

## Current phase

**Wave 4 — Notes and mail-merge handoff.**

Wave 4 includes:

- everything delivered in Waves 1–3
- two additional first-party connectors:
  - **Evernote** — personal private notes address, save the selected email to Evernote
  - **Mergo** — Google Sheets campaign handoff, add the sender to a Mergo mail-merge sheet
- provider identity on all 20 connectors: exact local logos, vendor, category,
  tagline, connector version, website, support, documentation and privacy links
- Evernote uses its documented **Email notes to** address; Alt8Mail sends the
  selected email through the user's own submission service, so no deprecated
  Evernote API or token is required
- Mergo has no native public API. Its connector uses Mergo's documented
  Google Sheets/Zapier workflow: a small Apps Script web app appends the sender
  to the campaign sheet, and the connector posts to that user-hosted endpoint

### Phase history

| Phase / wave | Scope | Connectors |
|---|---|---|
| Phase 1 | Curated directory, catalog, encrypted connections, installs, requests, reader actions | DeepL, Todoist, Slack |
| Phase 2 / Wave 1 | Managed OAuth relay, Slack OAuth and events, install approval policy, rate gate, System Control policy | + HubSpot, Asana, PagerDuty, Zoom (**7**) |
| Phase 3 / Wave 2 | Provider identity, support/docs links, exact logos, CRM/project/support wave | + Pipedrive, Zoho CRM, ClickUp, Zendesk (**11**) |
| Phase 4 / Wave 3 | Enterprise work connectors: CRM, support, tasks, scheduling and engineering | + Salesforce, Freshdesk, Intercom, TickTick, Calendly, Jira, Linear (**18**) |
| Wave 4 | Notes and mail-merge handoff | + Evernote, Mergo (**20**) |
| Wave 5 (planned) | Design, telephony and e-signature deep integrations | Canva, Dialpad, DocuSign, plus community/security connectors |

Canva, Dialpad and DocuSign are intentionally deferred rather than shipped as
token stubs: Canva needs a design/asset pipeline, Dialpad outbound messaging
needs explicit telephony consent, and DocuSign needs an envelope and attachment
pipeline. They will be implemented with those workflows in Wave 5.

Migrations: `0046_addons`, `0047_addon_oauth_policy`,
`0048_addon_catalog_seed`, `0049_addon_wave2_branding`,
`0050_addon_wave3_connectors`, `0051_addon_notes_mailmerge`.

## Connector directory

| Add-on | Category | Scope | Authentication | Support |
|---|---|---|---|---|
| DeepL | Translation | Personal | API key | https://support.deepl.com |
| Todoist | Tasks | Personal | API token | https://todoist.com/help |
| Slack | Communication | Workspace | OAuth or webhook | https://slack.com/help |
| HubSpot | CRM | Workspace | Private-app token | https://help.hubspot.com |
| Asana | Tasks | Personal | Personal access token | https://asana.com/support |
| PagerDuty | Operations | Workspace | Events API v2 routing key | https://support.pagerduty.com |
| Zoom | Meetings | Workspace | Server-to-Server OAuth | https://support.zoom.us |
| Pipedrive | CRM | Workspace | API token | https://support.pipedrive.com |
| Zoho CRM | CRM | Workspace | OAuth refresh token | https://help.zoho.com/portal/en/kb/crm |
| ClickUp | Tasks | Personal | Personal API token | https://help.clickup.com |
| Zendesk | Support | Workspace | Subdomain + API token | https://support.zendesk.com |
| Salesforce | CRM | Workspace | OAuth refresh token | https://help.salesforce.com |
| Freshdesk | Support | Workspace | Domain + API key | https://support.freshdesk.com |
| Intercom | Support | Workspace | Access token | https://help.intercom.com |
| TickTick | Tasks | Personal | OAuth access token | https://help.ticktick.com |
| Calendly | Scheduling | Personal | Personal access token | https://help.calendly.com |
| Jira | Engineering | Workspace | Atlassian API token | https://support.atlassian.com/jira-software-cloud/ |
| Linear | Engineering | Workspace | API key | https://linear.app/contact/support |
| Evernote | Notes | Personal | Private notes email address | https://help.evernote.com |
| Mergo | Marketing | Personal | Google Apps Script sheet webhook | https://help.mergo.app |

Provider logos are stored locally under `public/addon-logos/` (and mirrored for
System Control under `backend/internal/control/web/addon-logos/`). They are used
only to identify the provider integration; all marks remain the property of
their owners. The Details dialog repeats that attribution. The vector marks are
drawn from Simple Icons (CC0) in the provider's official brand colour; Slack,
Pipedrive, Salesforce, Freshdesk and Mergo use their product/app icons.

## Admin surfaces

- **Webmail** — Settings → Add-ons, plus the **Add-on** shortcut under Cabinet
  in the workspace rail. Users install personal add-ons here; workspace
  administrators configure provider credentials and see the request queue.
- **Customer Admin** — Governance → Add-ons. Shows the reviewed catalog,
  provider logos, support/docs links, workspace enablement, install policy,
  active install counts and pending requests. Approving an install request
  installs the add-on for that user when a connection exists.
- **System Control** — Infrastructure → Add-ons. Enables or disables each
  reviewed connector system-wide and shows workspace/install counts.

Credentials and provider secrets are never exposed in Customer Admin or System
Control. Setup stays in Webmail, where the connection is encrypted.

## Lifecycle

Every add-on moves through four states:

```text
system-available
    Control Plane makes the add-on available on this Alt8Mail system.

tenant-configured
    A workspace administrator connects provider credentials or enables
    the workspace policy.

install-ready
    The add-on is configured and can be installed by a user.

installed
    A user has enabled the add-on and its reader actions.
```

A user can see a system-available add-on that the workspace has not configured yet and send a setup request to an administrator. Requests are deduplicated while pending.

When a workspace administrator selects **Approval required** for an add-on,
members can no longer install it directly. They send an install request, an
administrator approves it from the Add-ons admin queue, and only then does the
install become available. Workspace administrators can always install directly.

## Database model

- `addon_system_catalog` — reviewed add-on definitions and the system-level enabled flag.
- `tenant_addons` — workspace policy, install policy, non-secret configuration
  and sealed Slack app/signing secrets.
- `addon_connections` — encrypted credentials for one user or the workspace,
  plus the provider account ID used to route webhooks.
- `addon_installs` — per-user installation state.
- `addon_requests` — setup/install requests for the administrator queue.
- `addon_events` — verified provider events, deduplicated by
  `(addon_id, provider_event_id)`.

Provider credentials are encrypted with the existing workspace-key sealing helper (`sealWorkspaceToken`). Secrets are never returned by the API.

## API

```text
GET    /api/addons
POST   /api/addons/{id}/connect
POST   /api/addons/{id}/oauth/start
POST   /api/addons/{id}/install
DELETE /api/addons/{id}/install
PUT    /api/addons/{id}/tenant
POST   /api/addons/{id}/request
POST   /api/addon-requests/{id}/resolve
POST   /api/addons/{id}/actions/{action}
GET    /oauth/slack/callback
GET    /oauth/relay/complete
POST   /api/addons/webhooks/slack
```

`GET /api/addons` returns the catalog, connection state, install state, pending requests and, for workspace administrators, the request queue. Each Slack view also reports `oauthAvailable` and `oauthMode` (`managed`, `byo` or `disabled`). Add-ons are disabled for delegated mailbox windows.

### Slack OAuth

`POST /api/addons/slack/oauth/start` accepts an optional `mode` of `managed`
or `byo`, plus the Slack app credentials when bringing your own app:

```json
{
  "mode": "byo",
  "clientId": "1234567890.1234567890",
  "clientSecret": "...",
  "signingSecret": "..."
}
```

The response is `{ "oauthUrl": "...", "mode": "managed|byo" }`. The browser
navigates to that URL. Slack returns to `/oauth/slack/callback` (BYO) or to the
managed relay, which returns to `/oauth/relay/complete`. Both callbacks store an
encrypted tenant connection and redirect to:

```text
/?addon=slack&addon_status=connected#/Settings/addons
```

Failures redirect with `addon_status=error&addon_message=...`.

The managed relay receives an `incoming-webhook` URL from Slack, so sending
works without per-tenant Slack app configuration. Slack **Events API** webhooks
are verified with the workspace's own signing secret, so inbound events need a
bring-your-own Slack app. Configure:

- OAuth redirect URL: `https://<mail-host>/oauth/slack/callback`
- Events request URL: `https://<mail-host>/api/addons/webhooks/slack`

The webhook endpoint verifies `X-Slack-Signature`, rejects stale timestamps,
answers `url_verification`, and stores each event once in `addon_events`.

## Connection scopes

- **Personal** add-ons use a connection owned by the signed-in user. DeepL,
  Todoist, Asana and ClickUp use this model.
- **Workspace** add-ons use one connection configured by an administrator.
  Slack, HubSpot, PagerDuty, Zoom, Pipedrive, Zoho CRM and Zendesk use this
  model.

A workspace administrator can disable a personal add-on for the whole workspace without deleting a user's existing encrypted connection.

### Provider setup notes

- **Pipedrive** — create a personal API token and paste it as the workspace
  connection. The connector searches for the sender by email and creates a note
  on the matching person.
- **Zoho CRM** — choose the data centre, then supply the client ID, client
  secret and refresh token from a Zoho Self Client. The refresh token needs
  contact read and note create scopes.
- **ClickUp** — use a personal API token and the target list ID. The connector
  creates a task in that list with the email content as its description.
- **Zendesk** — use the help-centre subdomain, the agent email that owns the API
  token and the token itself. The connector opens a ticket from the selected
  email.
- **Salesforce** — authorize a connected app with the `api` and `refresh_token`
  scopes, then connect its consumer key, consumer secret and refresh token. The
  connector logs the email as a completed task on the matching contact.
- **Freshdesk** — use the help-desk domain and an agent API key. The connector
  opens a ticket with the sender as requester.
- **Intercom** — choose the workspace region and an access token from a private
  app. The connector finds the sender as a contact and adds an internal note.
- **TickTick** — create a developer app, paste its OAuth access token and the
  target project ID. The connector creates a task with the email as content.
- **Calendly** — paste a personal access token plus the event type and owner
  URIs from the Calendly API. The connector creates a single-use scheduling
  link.
- **Jira** — use an Atlassian Cloud site, account email, API token, project key
  and issue type. Only `*.atlassian.net` sites are accepted.
- **Linear** — paste a personal API key and the target team UUID. The connector
  creates an issue through Linear's GraphQL API.
- **Evernote** — find the private notes address under Evernote Account settings
  → Email notes to and paste it into the connection. Alt8Mail sends the selected
  email to that address through the user's mailbox. Only `@m.evernote.com`
  addresses are accepted.
- **Mergo** — Mergo has no public REST API; it reads campaign recipients from
  Google Sheets. Deploy a Google Apps Script web app for the campaign sheet and
  paste its `/exec` URL into the connection. Only `script.google.com` Apps Script
  URLs are accepted.

Example Apps Script for the Mergo handoff:

```javascript
function doPost(e) {
  const body = JSON.parse(e.postData.contents || "{}");
  const sheet = SpreadsheetApp.openById("YOUR_MERGO_CAMPAIGN_SHEET_ID")
    .getSheetByName("Campaign");
  sheet.appendRow([new Date(), body.name || "", body.email || "",
                   body.subject || "", body.source || "Alt8Mail"]);
  return ContentService.createTextOutput(JSON.stringify({ ok: true }))
    .setMimeType(ContentService.MimeType.JSON);
}
```

Deploy it as a web app that executes as the sheet owner and allows access to
anyone with the link, then paste the `/exec` URL into the Mergo add-on setup.

## Adding a connector

1. Add an `addonDefinition` in `backend/internal/httpapi/addons.go`.
2. Declare the connection scope, auth type, fields, permissions, actions and reader surfaces.
3. Add credential validation for the provider fields.
4. Add the provider API call in the add-on action dispatcher through
   `addonDo` so it inherits the provider rate gate.
5. Update the catalog seed migrations (`0048_addon_catalog_seed.sql` through
   `0051_addon_notes_mailmerge.sql`) and the mock definition in
   `server/app.mjs`, including the logo, support, website, docs and privacy
   metadata.
6. Add API and browser coverage in `tests/api.test.mjs` and `tests/e2e.mjs`.
7. Keep provider calls server-side. Do not expose credentials or call providers directly from the browser.

A connector should use fixed provider endpoints or an allowlisted host. Slack webhook URLs, for example, are restricted to `hooks.slack.com` and `hooks.slack-gov.com` to avoid SSRF.

## Deployment modes

The connector layer supports three modes without changing connector code:

1. **Managed relay** — one Alt8Mail-owned OAuth app and a canonical relay
   domain, for example `connect.alt8mail.com`.
2. **Self-hosted gateway** — a `connect.<customer-domain>` endpoint with
   bring-your-own OAuth credentials.
3. **Managed-only** — a single hosted deployment with a known domain.

For the managed relay, point the `connect.<domain>` A/AAAA record at the relay
host, issue a certificate for it, deploy `bin/a8m-addon-relay`, install
`deploy/systemd/a8m-addon-relay.service`, copy
`deploy/addonrelay/addon-relay.env.example` to `/etc/a8m/addon-relay.env`
(mode `0640`, owner `root:a8m`), and set the webmail environment:

```text
ALT8MAIL_ADDON_RELAY_URL=https://connect.example.com
ALT8MAIL_ADDON_RELAY_SECRET=<shared secret>
ALT8MAIL_ADDON_GATEWAY_URL=https://mail.example.com
```

Then run `systemctl enable --now a8m-addon-relay` and render the relay nginx site.

`ALT8MAIL_ADDON_GATEWAY_URL` is the public origin browsers use for OAuth
callbacks. It must proxy `/oauth/` to the webmail API. If it is unset, Alt8Mail
uses `ALT8MAIL_PUBLIC_URL`, then the request host. A dedicated
`connect.<customer-domain>` gateway is optional; it is separate from
`ALT8MAIL_ADDON_RELAY_URL`, which points at the Alt8Mail relay.

The relay holds no mailbox data and no long-lived tenant tokens. It exchanges
the Slack authorization code and hands the result back over a one-time code
that expires after five minutes. Use the same `ALT8MAIL_ADDON_RELAY_SECRET` on
the relay and the webmail API, and keep it out of public output.

In the relay's Slack app, set the OAuth redirect URL to
`https://connect.example.com/oauth/slack/callback`. Tenant webmail hosts only
need the `/oauth/relay/complete` proxy route. A ready-to-render nginx site is in
`deploy/nginx/a8m-addon-relay.conf`.

### Reverse proxy

The webmail nginx site must proxy `/oauth/` to the Go API, because Slack and the
relay redirect the browser there:

```nginx
location ^~ /oauth/ {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

Current nginx templates, the System Control Web Net generator and
`scripts/alt8mail-ensure-nginx-routes.sh` all add this route. Updating an older
installation repairs the managed include automatically on the next boot.

## Security rules

- No third-party JavaScript runs in the webmail origin.
- Add-ons are disabled in delegated mailbox windows.
- Only the selected message is sent to a provider, and only when the user invokes an action.
- Slack webhook URLs are host-allowlisted.
- Provider calls use fixed API origins. Zendesk and Freshdesk subdomains are
  validated before they are placed in a URL; Zoho data-centre hosts come from a
  fixed allowlist; Salesforce instance URLs must end in `.salesforce.com`;
  Jira accepts only `*.atlassian.net`; Intercom regions map to fixed hosts;
  Calendly URIs must belong to `api.calendly.com`; Evernote destinations must
  end in `@m.evernote.com`; Mergo webhooks must be
  `https://script.google.com/macros/s/*/exec`; ClickUp list IDs, Asana project
  GIDs and Linear team IDs are validated before use.
- Provider logos are local assets. No logo, font or script is loaded from a
  third-party origin at runtime, so the webmail CSP and user privacy are kept.
- OAuth state is HMAC-signed, expires after ten minutes and is bound to the
  tenant, add-on, user and callback mode.
- Relay exchange codes are random, single-use and expire after five minutes.
- Credentials are encrypted at rest and omitted from API responses and audit details.
- Workspace policy can disable all add-on actions immediately.
- Every connection, install, request, OAuth start and action is written to the existing audit trail.
- Sensitive or external-sharing endpoints remain unavailable to delegated sessions.
