# Cabinet uploads and secure file sharing

Alt8Mail 0.17 turns Cabinet uploads into a workspace-level transfer queue and
closes the recipient-confirmation gap on external file links.

## Sticky upload manager

The Cabinet **Upload** button opens a draggable panel that stays available while
the user moves between Mail, Calendar, Tasks and Files.

### Computer

- Drag files onto the panel or choose them from the device.
- Each file gets its own progress bar, state, size and elapsed time.
- Uploads run in the background when the panel is minimized. Closing is
  deferred while transfers are active; the panel can be reopened with **Upload**.
- Files go to the current Cabinet folder and storage provider. Alt8Mail Drive
  and connected providers use the existing attachment/file pipeline.

### URL

- Paste an `http` or `https` URL and Alt8Mail fetches the file into Alt8Mail
  Drive.
- Redirects are limited, credentials in URLs are rejected, and private,
  loopback, link-local and metadata addresses are blocked at DNS/dial time.
- The same 8 MB attachment ceiling applies.

### Mobile QR

- Cabinet creates a signed upload session valid for **60 minutes** and renders
  a QR code with the session URL.
- The mobile page is responsive, needs no desktop session, and uploads directly
  from the phone with per-file progress. The desktop panel polls the session and
  shows the same files progressing in real time.
- The desktop panel can add **1 or 4 hours** to the session; the mobile page
  never exposes the extension control.
- You can generate a fresh QR code at any time. The previous session is revoked.

Server routes:

- `POST /api/files/upload-sessions`
- `GET /api/files/upload-sessions/{id}`
- `POST /api/files/upload-sessions/{id}/extend`
- `DELETE /api/files/upload-sessions/{id}`
- `POST /api/files/import-url`
- `GET /upload/{token}`
- `POST /api/public/upload/{token}`
- `POST /api/public/upload/{token}/progress`

## Recipient-confirmed links

When a share link includes a recipient email, the public page first asks the
visitor to **confirm that email on the device**. A successful confirmation sets
an HTTP-only, HMAC-signed cookie for 12 hours (or until the link expires). The
raw file content endpoint also checks this cookie, so forwarding the URL does
not expose the file to another person.

## Locked files and access requests

Cabinet file details now include **Lock link / Unlock link**. A locked file:

- never serves its content through a public share;
- shows a **This file is locked** page;
- accepts an email and optional note as an **access request**;
- records the request in `file_share_access_requests`; and
- creates an in-app owner notification (`file_access_request`).

The owner can unlock the file or share it again after reviewing the request.

## Migration and edge routing

Migration `0042_file_transfer_security` adds:

- `file_upload_sessions` with hashed tokens, expiry/extension and upload progress;
- `file_share_access_requests`; and
- indexes for owner sessions, expiry and pending requests.

Both nginx templates proxy `^~ /upload/` to the Go server so the mobile page is
reachable at `https://<mail-host>/upload/<token>`. Upload session tokens are
opaque and are redacted automatically by the Web Net path log.
