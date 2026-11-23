# Alt8Mail Office — Collabora engine

Alt8Mail can run its document editor on either ONLYOFFICE (the existing
integration) or a white-labelled Collabora Online build. The engine is selected
per installation in `office_settings.engine` (`onlyoffice` or `collabora`).

## Data flow

1. `POST /api/office/documents/{id}/open` (or the guest share endpoint) resolves
   the file and the user's permission exactly as before.
2. With `engine = 'collabora'`, the API returns an `editorUrl` instead of a
   DocsAPI configuration:

   ```text
   https://office.example.com/browser/dist/cool.html?WOPISrc=<encoded>&access_token=<token>
   ```

3. `/office-editor` embeds that URL in an iframe (the isolated host page is
   unchanged; the iframe uses Collabora's own origin).
4. Collabora calls the WOPI host:

   | Method | Path | Purpose |
   |---|---|---|
   | `GET` | `/api/office/wopi/files/{id}` | `CheckFileInfo` |
   | `GET` | `/api/office/wopi/files/{id}/contents` | `GetFile` (Range supported) |
   | `POST` | `/api/office/wopi/files/{id}/contents` | `PutFile` |
   | `POST` | `/api/office/wopi/files/{id}` | lock family; returns 501 (timestamp mode) |

5. `PutFile` re-checks the live share/grant/policy, DLP, quota and legal-hold
   rules, then writes an immutable version through the existing
   `officeSaveCallbackVersion` pipeline.

## Conflict and version model

- `CheckFileInfo` returns `LastModifiedTime` (`files.updated_at`) and `Version`
  (`files.checksum`).
- `PutFile` compares `X-COOL-WOPI-Timestamp` with the current
  `files.updated_at`. A stale base returns `409 {"COOLStatusCode":1010}`;
  Collabora retries without the header on explicit user overwrite.
- Identical saves are deduplicated with the existing
  `office_callback_events` table: `document_key = 'wopi:<document id>'`,
  `status = 2`, and an event key built from the content SHA-256 plus the base
  timestamp. No new save table is required.
- Every distinct save creates a new `office_document_versions` row. Tune the
  editor's save interval if a tenant wants fewer intermediate versions.

## Permissions

The WOPI token is HMAC-signed with the existing office secret and carries:
tenant, file, owner, editor (internal share), share id and share token (guest),
display name, can-write and expiry. Every request re-resolves the file:

- **Owner:** `loadFileContent`.
- **Internal editor:** `loadAccessibleFileContent` plus a live
  `file_permissions` check.
- **Guest:** `loadPublicShare` plus lock, expiry, revocation and guest policy.

DLP, quota, legal hold, eDiscovery, audit, version compare/review and restore
are unchanged because `PutFile` calls the same save pipeline.

## AI assistant

The floating Alt8Mail AI panel is part of the `/office-editor` host page and is
available for authenticated documents (not public share links). In Collabora it
uses the documented postMessage API:

- `Action_Copy` with `Mimetype: text/plain;charset=utf-8` returns the selected
  text in `Action_Copy_Resp.Values.content`.
- `Action_Paste` with `Mimetype` and `Data` inserts the generated text.

Generation is served by `POST /api/office/ai`. The handler deliberately reuses
the webmail AI configuration: `tenant_ai_settings`, the sealed provider key, the
provider client, the monthly budget hooks and DLP redaction. No second API key
or Office-specific AI setting is required.

| Action | Context |
|---|---|
| Summarize, Rewrite, Translate, Continue, Ask AI | selected text via `Action_Copy` |
| Ask document, Action items, Draft reply | latest saved version via `office.ExtractText` after a live file-access check |

Document text is capped and treated as untrusted data in the prompt. The
`aiEnabled` flag returned by the open endpoint is true when the paid Office AI
hook allows the user or when the tenant webmail AI is enabled with a model.

No browser patch is required for the AI bridge.

## Configuration

- `office_settings.engine = 'collabora'`
- `office_settings.public_url` = the Collabora public origin
  (`https://office.example.com`).
- The Alt8Mail public URL must be reachable by Collabora; it is the WOPI host
  origin.
- The office secret file (`OFFICE_JWT_SECRET_FILE`) still signs the WOPI token;
  it is no longer shared with the office engine. If it does not exist, create it
  with `openssl rand -base64 48 > /etc/a8m/office-jwt-secret`, then set
  `root:a8m` and `0640`.
- The engine and image must name the same product. The Control Plane and the
  agent reject `engine=onlyoffice` with `altgence/office` (or another Collabora
  image) and `engine=collabora` with `onlyoffice/documentserver`. Custom images
  whose names are not recognized are accepted for either engine.

## Health and failure states

- A running container whose health endpoint fails is stored as `unhealthy`, not
  `running`. Webmail refuses to open documents until health is OK again.
- `last_error` is preserved across status polls until a healthy check or a
  successful install, so a transient poll does not hide the original failure.
- The requested engine and image are not pre-recorded before the agent runs.
  The status refresh follows the engine and image the agent actually deployed;
  if the request fails before deployment, the previous values remain.

## Migration

`0057_office_collabora.sql` adds `office_settings.engine`.

## Tests

```sh
go test ./internal/officecollabora ./internal/office
go test ./internal/httpapi -run 'Office|WOPI|Collabora'
```

## Deployment

The Control Plane and `a8m-office-agent` now support both engines. Set
`office_settings.engine` (or choose the engine in System Control → Office), and
the agent generates the matching Compose project, nginx gateway, health check,
font refresh and credential-rotation path. `install/install.sh` in the
Alt8Office workspace remains a standalone fallback for a hand-managed host.

For remote deployments, the Collabora image is pulled as
`altgence/office:25.04`. If the Docker Hub repository is private, run
`docker login` on the Office host first (prefer a read-only token); the agent's
`docker compose pull` uses the host's Docker credentials. The agent skips the
pull when the image already exists locally.

The agent's data root defaults to the parent of `OFFICE_AGENT_STATE_FILE`
(`/var/lib/a8m-office` in the generated environment). It prepares `cache/` and
`logs/` there and uses the same root for the Collabora cache/log mounts and the
ONLYOFFICE data/log mounts, so custom state paths and non-root hosts work.

Switching engines reuses the `a8m-office` container name but changes the Compose
service (`documentserver` for ONLYOFFICE, `office` for Collabora). The agent
inspects the existing container's `com.docker.compose.service` label and removes
it when it belongs to the other engine before starting the new Compose project.
This avoids the Compose orphan/name-conflict error on a live engine switch.

## Webmail origins and frame ancestors

Alt8Mail serves webmail on both the mail hostname (`mail.example.com`) and the
primary mail domain (`example.com`). The Collabora editor must be allowed to
frame inside either origin:

- The `open` endpoint uses the validated request host as the WOPI base, so
  Collabora sees the same origin that is embedding the editor and automatically
  adds it to `frame-ancestors` via the `WOPISrc` host.
- The Control Plane sends the webmail URL, mail hostname and primary mail
  domain to the agent as the comma-separated `aliasgroup1` WOPI allowlist. The
  agent persists that list and reuses it for updates, font mounts and credential
  rotation.
- An unrecognized request host falls back to the configured public webmail URL
  rather than becoming a WOPI host.
- `CheckFileInfo` returns the same validated origin as `PostMessageOrigin`, so
  the editor's postMessage bridge targets the iframe parent.

## Editor shell and branding

The webmail Office window opens maximized by default; an explicitly resized or
moved window is preserved. The Alt8Mail 8 icon sits in the editor window header.
The Collabora toolbar wordmark is hidden because it duplicated the product mark
and overflowed the notebookbar. The About dialog keeps the full wordmark and
mark.

The agent writes `/var/lib/a8m-office/branding/branding.css` and
`branding.js`, then mounts them over the container's `browser/dist` files, so
branding changes ship with the agent without rebuilding the Collabora image.
The gateway appends a content-hash query to both URLs; this prevents a browser
from reusing an old cached wordmark CSS.

The host page passes `ui_defaults=UITheme=dark|light;SavedUIState=false` to
Collabora, so the editor follows the webmail light/dark theme even when the user
previously chose another theme inside Collabora. The `theme` and `ui_theme`
parameters are also passed for branding/welcome resources. Collabora's
`ui_defaults` cache reuses the parsed JSON for an identical string and does not
restore `SavedUIState` on a cache hit, so each open includes a harmless unique
`TextStatusbar` value. This keeps the host theme authoritative without an image
rebuild. Changing the webmail theme while a document is open asks Collabora to
save, then reloads the host page with the new theme. The
`branding.js` applies the desktop-suite skin after the document type is known:
it closes the wide Styles sidebar by default, enables the ruler, applies 100%
zoom, replaces the image-based Collabora style gallery with a horizontal text
style strip that drives the native style commands, and adds narrow left/right
icon rails. The right rail is the far-right flex item, so an opened sidebar sits
to its left exactly like a desktop suite; the Style and Property decks open from
the rail. The strip hides lower-priority styles at narrower editor widths instead
of clipping. The skin also hides the duplicate document title when the editor is
embedded in the webmail Office window, where the window header already carries
the name.

The skin is CSS/DOM-only and ships in the agent's mounted `branding.css` and
`branding.js`; it does not rebuild the Collabora image. The webmail
`workspace.css` keeps the outer Office window header to a compact title bar so
the shell and editor read as one application. The widescreen style strip steps
down from eight to six, five, three and two styles as the iframe narrows; the
full style list remains available in the Styles sidebar.

On Debian/Ubuntu, the remote prerequisite step installs Docker from the
distribution and downloads the Docker Compose v2 CLI plugin from Docker's
GitHub release into `/usr/local/lib/docker/cli-plugins`. Debian 12 does not
package Compose v2 under any apt name, so this download replaces the old
`apt-cache` lookup. The Office host must be able to reach `github.com`.

## Not yet wired

- The `/office-editor` CSP still allows broad `https:` frame sources. Tighten
  it to the configured office origin as a follow-up.
- The ribbon is a CSS/DOM skin over Collabora's command model. A source-level
  rewrite of the notebookbar definitions is not required for the shipped look,
  but the native dialogs and secondary panels keep their engine styling.
- WOPI proof-key validation (`X-WOPI-Proof` from `/hosting/discovery`) is not
  enabled; add it at GA.
- The browser end-to-end edit/save flow still needs a real-browser test.
