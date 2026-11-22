# Alt8Mail 3.11.7

Release date: 2026-11-23. Enterprise editor shell and branding cleanup.

## 3.11.7 Office editor shell

- **The editor opens maximized by default.** Office documents are a workspace, not a popup. A windowed layout is honored only when the user has explicitly resized or moved the editor, and the maximize/restore button now reflects the active state.
- **The Alt8Mail 8 icon replaces the duplicated full wordmark in the editor.** The 8 icon is placed in the editor window header; the Collabora toolbar wordmark is hidden. The About dialog keeps the full wordmark and mark, and the webmail app header is unchanged.
- **Branding fixes no longer require rebuilding the engine image.** The Office agent writes `branding.css` into `/var/lib/a8m-office/branding/` and mounts it over the container's `/usr/share/coolwsd/browser/dist/branding.css`.
- Added agent and Compose tests for the branding file and mount.

# Alt8Mail 3.11.6

Release date: 2026-11-23. Complete the webmail-origin fix for the Collabora editor postMessage bridge. Includes all 3.11.5 changes.

## 3.11.6 postMessage origin

- The WOPI `CheckFileInfo` `PostMessageOrigin` now follows the validated webmail origin of the WOPI request, matching the editor iframe parent. This keeps the AI copy/paste bridge and editor-to-parent messages working when webmail is opened from the primary mail domain.

# Alt8Mail 3.11.5

Release date: 2026-11-23. Fix the Office editor iframe when webmail is opened from the primary mail domain. Includes all 3.11.4 changes.

## 3.11.5 webmail origins

- **The editor iframe follows the webmail origin.** Alt8Mail serves webmail on the mail hostname and the primary mail domain (for example `mail.example.com` and `example.com`). Collabora only allowed `frame-ancestors` for the configured webmail URL, so opening the editor from the primary domain showed `office.example.com refused to connect`.
- The Collabora WOPI base now uses the validated webmail origin of the open request. Collabora automatically includes the WOPI host in `frame-ancestors`, so the iframe parent is allowed from either webmail hostname.
- The Control Plane adds every configured webmail origin (webmail URL, mail hostname and primary mail domain) to Collabora's `aliasgroup1` WOPI allowlist. The agent stores and reuses that list for updates, font mounts and credential rotation.
- Unknown request hosts fall back to the configured public webmail URL. Alias groups accept HTTP only for local development; production origins are HTTPS.
- Added tests for origin normalization, WOPI base selection and alias-group validation.

# Alt8Mail 3.11.4

Release date: 2026-11-23. Fix switching an existing Office deployment between engines. Includes all 3.11.3 changes.

## 3.11.4 Office engine switch

- **Switching ONLYOFFICE ↔ Collabora no longer fails on the reused container name.** Both engines use the `a8m-office` container name but different Compose service names (`documentserver` and `office`). Compose treated the previous container as an orphan and then refused to create the new one with `container name "/a8m-office" is already in use`.
- The agent now inspects the existing container's `com.docker.compose.service` label and removes it only when it belongs to the other engine's service, before starting the new Compose project. The guard applies to install, start, update, font mount and credential rotation; same-engine operations are unchanged.
- Added a unit test for the container-removal decision.

# Alt8Mail 3.11.3

Release date: 2026-11-23. Supersedes 3.11.2, whose release workflow failed on a pre-existing storage-move verification flake. Includes all 3.11.2 changes.

## 3.11.3 storage move verification

- **Target-only deliveries no longer fail verification.** The rsync verification used `--itemize-changes` with directory times, so a message that arrived only on the target after the switch changed the target Maildir directory mtime and produced `.d..t...... Maildir/cur/`, failing the move even though every source message was present. Verification now passes `--omit-dir-times`; target-only files were already allowed and remain allowed.
- **Regression coverage:** `TestVerifyMailHomeFallback` now forces the target directory mtime to differ and is exercised against the real rsync path on Linux.

# Alt8Mail 3.11.2

Release date: 2026-11-22. Hardening for the Collabora Office engine.

## 3.11.2 engine/image safety

- **Engine and image must match.** The Control Plane and `a8m-office-agent` now reject `engine=onlyoffice` with a Collabora image (and the reverse). This prevents the agent from generating an ONLYOFFICE Compose around a Collabora image, which left the container running but unreachable on port 80 instead of Collabora's port 9980.
- **Failed installs no longer pre-record the requested engine/image.** The remote transport is recorded before the agent call, but `public_url`, `image` and `engine` are saved only after the agent runs. The status refresh follows what the agent actually deployed; if the request fails before deployment, the previous values remain.
- **Unhealthy is a first-class state.** A running container whose health endpoint fails is stored as `unhealthy`; webmail refuses to open documents until health is OK. `last_error` is preserved across status polls until a healthy check or successful install, and the agent's actual engine/public URL are persisted so the Control Plane cannot drift from the Office host.
- **Control UI:** the status card shows **Unhealthy** with a health row and a log hint instead of "Stopped"; entering a known image family selects the matching engine; install rejects a mismatched engine/image before sending; and lifecycle buttons are disabled while an install, start, stop or rotate operation runs, preventing overlapping agent calls.
- **Preflight memory guidance** now calls out the 8 GB recommendation for Collabora with concurrent documents; the hard minimum remains 2 GB.

# Alt8Mail 3.11.1

Release date: 2026-11-22. Supersedes 3.11.0 with the Office agent install fix.

## 3.11.1 office agent

- **Fixed the Office agent install on non-root hosts and Linux CI.** Preparing the
  Collabora data directories always created `/var/lib/a8m-office/cache` and
  `/var/lib/a8m-office/logs`, even when the agent state file was configured
  elsewhere, so a non-root runner failed with `permission denied`. The data root
  now defaults to the parent of an explicitly configured state file (and
  `/var/lib/a8m-office` otherwise), and the same root feeds the cache, logs and
  ONLYOFFICE data volume mounts. The backend test suite passes on Linux CI again.

# Alt8Mail 3.11.0

Release date: 2026-11-22. Selectable Collabora Office engine and the Debian/Ubuntu remote prerequisite fix.

## 3.11.0 office engine

- **Collabora Online is now a selectable Office engine** alongside ONLYOFFICE. System Control → Office gains a Document engine selector, and `office_settings.engine` stores `onlyoffice` or `collabora`.
- **Same Cabinet model.** Collabora saves through the WOPI `PutFile` endpoint into the existing immutable-version pipeline: Cabinet storage, `files.version`, DLP on save, quota, legal hold, eDiscovery, audit, version compare/review/restore and share links are unchanged.
- **Same deployment model.** `a8m-office-agent` generates the engine-specific Compose project, nginx gateway, health check (`/hosting/discovery`), font refresh and credential rotation. Remote deployment over SSH works for both engines.
- **AI panel preserved.** The in-editor assistant uses Collabora's documented `Action_Copy`/`Action_Paste` postMessage API; no browser fork is required.
- **Image:** the white-labelled Collabora image is published as `altgence/office:25.04` (linux/amd64). Remote Office hosts pull it; a private Docker Hub repository requires `docker login` on the Office host first.
- **Migration:** `0057_office_collabora.sql` adds `office_settings.engine`. Existing installations remain on `onlyoffice` until the engine is switched.

## 3.11.0 Debian/Ubuntu remote prerequisites

- **Fixed the remote prerequisite installer on distributions that do not package Docker Compose v2.** Debian 12/13 and Ubuntu 22.04 provide `docker.io` and Compose v1, not `docker-compose-v2` or `docker-compose-plugin`; the previous script exited with `No Docker Compose v2 package is available in apt.` before installing anything.
- The installer now installs Docker, curl, CA certificates, nginx and certbot from the distribution, then downloads the official Compose v2 CLI plugin from Docker's GitHub release into `/usr/local/lib/docker/cli-plugins` using the host architecture (`amd64`, `arm64`, `armhf`, `ppc64el`, `s390x`, `riscv64`).
- The Office host must be able to reach `github.com` for the plugin download. A host with Docker and Compose v2 already installed skips the download.

# Alt8Mail 3.10.1

Release date: 2026-11-20. Admin portal hostname and mail/admin DNS preflight.

## 3.10.1 installer

- **Admin portal hostname field.** Interactive installs ask for an admin
  hostname (default `admin.<domain>`); `--admin-hostname` and
  `A8M_ADMIN_HOSTNAME` are supported. It must differ from the mail hostname
  and primary domain.
- **Mandatory A/AAAA preflight for the mail and admin hostnames only.** The
  primary domain is not required to point at the mail server because it may
  host an unrelated website. The installer requires each of the mail and
  admin names to resolve to the server's public IPv4 or IPv6, prints actual
  and expected records, and stops if they do not. `dnsutils` is installed
  automatically when needed.
- **The admin hostname is used end to end.** It is added to the TLS
  certificate request and SAN, nginx `server_name`, the webmail admin URL and
  WebAuthn origins. `https://admin.<domain>/` redirects to the administration
  panel; `/admin/` on the mail hostname continues to work.
- The primary domain is added to the certificate only when its own DNS points
  at this server. Existing certificates are reused when they cover the mail
  and admin names; certbot uses `--expand` otherwise.
- Includes the 3.9.9 license trust-anchor fix and all preceding installer
  fixes.

# Alt8Mail 3.9.9

Release date: 2026-11-20. License trust anchor and enterprise bundle fix.

## 3.9.9 licensing

- **The enterprise-capable binaries now compile the vendor license trust
  anchor.** The private build previously omitted the public key, so after a
  community-to-enterprise upgrade Control reported “Trust anchor: Not
  configured” and refused license uploads even though the token was valid.
- **Configured keys are accepted as a fallback on fresh installs.** The
  installer writes `CONTROL_LICENSE_ALLOW_CONFIG_KEY=true` and
  `ALT8MAIL_LICENSE_ALLOW_CONFIG_KEY=true` when no previous explicit value is
  preserved, so a root-owned configured vendor key can verify a token if a
  binary was built without the embedded anchor.
- Existing deployments can recover without reinstalling by adding the public
  key and `ALLOW_CONFIG_KEY=true` to `control.env`/`app.env`, then restarting
  Control and the webmail.

# Alt8Mail 3.9.8.9

Release date: 2026-11-20. Hostname and PostgreSQL grants fixes.

## 3.9.8.9 installer

- **The requested hostname is applied before preflight.** The installer updates
  the system hostname, `/etc/hostname` and `/etc/hosts`, and refreshes the
  whiptail backtitle as soon as the configuration is accepted, so subsequent
  screens show the host being installed instead of the provider's old name.
- **PostgreSQL grants are copied to `/tmp` before `psql -f` runs.** The
  `postgres` account cannot traverse `/root`, which caused
  `grants.sql: Permission denied` after all migrations had applied.
- Expected migration notices remain suppressed and the migration client runs
  outside `/root`.
- Includes the 3.9.8.8 PostgreSQL output cleanup and all earlier installer
  fixes.

# Alt8Mail 3.9.8.8

Release date: 2026-11-20. Clean PostgreSQL migration output.

## 3.9.8.8 installer

- The installer changes to `/tmp` before running PostgreSQL client commands.
  The `postgres` account can no longer emit “could not change directory to
  …: Permission denied” for every migration.
- PostgreSQL client messages are lowered to warnings for installer commands,
  suppressing expected `NOTICE` lines such as “extension already exists” and
  “constraint does not exist, skipping”. Real errors still stop the phase.
- Includes the 3.9.8.7 prebuilt binary mode fix and all 3.9.8.6 progress,
  welcome and branding changes.

# Alt8Mail 3.9.8.7

Release date: 2026-11-20. Prebuilt release mode fix.

## 3.9.8.7 installer

- Release tarballs assembled on Windows can carry `bin/` files with mode
  `0644`. The installer now restores `0755` on the release binaries before
  deciding whether to use the prebuilt release, so a release install never
  falls through to a source build in a tree that has no `backend/` directory.
- The same normalization is applied when resuming a saved build and when
  installing the release tree.
- No package-manager output is shown on the terminal; package, build and
  certbot work stays behind the compact spinner from 3.9.8.6.

# Alt8Mail 3.9.8.6

Release date: 2026-11-20. Clean installer progress display.

## 3.9.8.6 installer display

- Package installation, repository refreshes, the release build and certbot
  now run behind a single compact spinner with elapsed time. Their verbose
  output goes to the phase log instead of flooding the terminal.
- The screen keeps the phase headers, task lines and green completion lines;
  there is no background package-manager noise during a normal install.
- On failure, only the last 25 log lines are printed, followed by the phase
  error and log path.
- Welcome screen, append-only phase transcript and non-Enterprise branding
  remain from 3.9.8.5.

# Alt8Mail 3.9.8.5

Release date: 2026-11-20. Installer experience and progress reporting.

## 3.9.8.5 installer experience

- **A welcome screen explains the install and asks for explicit permission**
  before any system change. It lists what will be installed, supported
  operating systems, resource requirements, DNS/ports, log and state paths.
- **The installer is no longer branded “Enterprise.”** It is the Alt8Mail
  Installer, used by community and paid deployments alike.
- **Phases no longer clear the screen.** Every phase prints a numbered header,
  live task lines and a green completion line with elapsed time; completed
  phases remain in the transcript, and a final summary lists every phase.
- **Long operations stream their output** to the terminal and the phase log
  instead of looking frozen. Package installation uses a five-minute dpkg lock
  timeout and non-interactive dpkg options so it fails clearly instead of
  waiting forever.
- ClamAV signature startup is reported as a warning rather than failing an
  otherwise healthy install, and `enabled-runtime` template units count as
  enabled for the final health check.

# Alt8Mail 3.9.8.4

Release date: 2026-11-20. Installer rewrite, release packaging and uninstaller
fixes.

## 3.9.8.4 installer

- **The modular installer is now complete and packaged in the release.**
  `install.sh` invokes `installer/main.sh`, and release tarballs now include
  the whole `installer/` tree plus `uninstall.sh`.
- **Release layout is correct.** The installer builds through
  `scripts/build-release.sh`, installs into `/opt/a8m/releases/<version>`,
  maintains `current`/`bin`/`admin`/`app` links, installs the real
  `deploy/systemd` units and starts the correct `a8m-admin-server` binary as
  the unprivileged `a8m` user.
- **Full provisioning is restored.** The phases create the `a8m` and `vmail`
  users, secrets, `app.env`, `control.env`, PostgreSQL roles and grants,
  migrations, Dovecot/Postfix/Rspamd/ClamAV/Redis configuration, nginx and
  TLS, the first tenant/owner/Control account, Fail2Ban and UFW.
- **PostgreSQL is detected, not assumed.** Debian 12 (15), Ubuntu 22.04 (14)
  and Ubuntu 24.04 (16) are supported, and every monitored unit is enabled at
  boot.
- **The uninstaller mirrors the installed footprint.** It stops the timers and
  office agent, drops the database and roles, removes Alt8Mail units, nginx
  sites, Fail2Ban/sysctl/Redis fragments and application directories, while
  preserving `/var/vmail`, `/srv/a8m-storage`, PostgreSQL data, logs and
  backups.
- `--non-interactive`, `--yes`, `--dry-run`, `--force` and the resume journal
  now behave consistently without opening whiptail prompts.
- Phrase translation now prefers the most specific catalog pattern, so combined
  audit phrases such as “Ruleset saved · revision 4 · vacation responder
  enabled” translate in full. The seat-policy rollback also tolerates the
  soft-licensing migration that removes its table.

# Alt8Mail 3.9.8.3

Release date: 2026-11-20. Licensing, service supervision and community
feature discovery.

## 3.9.8.3 licensing and operations

- **Community edition has unlimited users.** The 25-active-user cap and its
  database trigger are removed. A paid license overage is a soft true-up
  warning in Control and the administration overview; mail delivery and user
  creation are never refused.
- **Seat language is now user language.** The licence page reports active
  users and licensed users, not seats. The administrative details paragraph
  and the token-ownership paragraph were removed, and the verification note is
  now accurate for the whole system.
- **Enterprise feature discovery in the community admin.** Paid menu entries
  appear with a lock and open a polished preview page with a trial/upgrade
  call to action. Set `HIDE_ENTERPRISE_PROMPTS=true` to hide the previews
  entirely. The webmail never shows upgrade prompts.
- **Postfix supervision is correct.** Control monitors `postfix.service`
  instead of the unused `postfix@-.service` template, reloads through systemd,
  validates `postfix check` before reloading and keeps the old configuration
  live if validation fails.
- **All monitored units are enabled by default.** The installer enables
  `clamav-freshclam.service` and the PostgreSQL 15 instance unit alongside the
  existing services.
- New strings and the rewritten verification sentence have locale entries,
  including translations for all shipped admin languages.

# Alt8Mail 3.9.8.1

Release date: 2026-11-19. Fresh-install fix for source checkouts.

## 3.9.8.1 installer

- A fresh `git clone` install no longer fails at “applying database
  migrations” with `cp: cannot stat '.../migrations/.'`.
- The installer resolves migrations from `migrations/` in a release tarball or
  `backend/migrations/` in a source checkout.
- No application binaries or database schema changed in this patch.

# Alt8Mail 3.9.8

Release date: 2026-11-19. Every enterprise catalogue entry now has a real
management screen.

## 3.9.8 enterprise operations

- **Usage analytics** shows users, storage, mail, files, tasks, calendar and
  AI adoption with a 30-day outbound send chart and an accessible daily table.
- **CRM sync** lists connector state and cached contacts, companies and deals,
  and runs a provider sync from the admin console.
- **Support operations** generates a downloadable support bundle and manages
  the optional telemetry heartbeat.
- **Identity and SSO** manages OIDC and SAML settings, including the SP key
  pair, from the admin console. Changes apply without a restart and are
  audited.
- **Workspace features** gives administrators real tenant-wide inventories and
  safe actions for shared drafts, team templates, the template gallery,
  automation rules, booking pages, mail merge campaigns, campaign tracking and
  follow-ups.
- **MCP governance** lists connected clients, granted scopes and write-action
  requests, with immediate connection revocation.
- **Backup and restore** links to the audited System Control backup surface and
  states exactly what each archive covers.
- Tenant administrators now receive the paid operation permissions for
  analytics, CRM, telemetry, encryption, eDiscovery review, phishing
  simulations and developer integrations by default.

# Alt8Mail 3.9.7

Release date: 2026-11-19. Trial and infrastructure clarity on the licence page.

## 3.9.7 licence display

- **Trial licences are visibly trials.** The licence page shows a Trial badge,
  a Trial licence type row and a trial-specific notice with the exact expiry,
  instead of the generic full-licence message.
- **Infrastructure type is carried in the signed token.** On-premise or cloud
  is shown as a licence claim and on the portal licence record, so an offline
  token and the online audit trail agree.
- **Verification wording is accurate.** The page now says local Ed25519 token
  verification with the portal used only for activation, enterprise bundle
  delivery and update checks. It no longer claims that no network call exists.
- Infrastructure changes are audited in the licensing portal and copied into
  subsequently issued or reissued licence tokens.

# Alt8Mail 3.9.6

Release date: 2026-11-19. Enterprise feature discoverability in the admin console.

## 3.9.6 enterprise catalog

- **The admin console now has an Enterprise → Enterprise features page.**
  It lists the complete paid feature catalog registered by the installed
  enterprise module, not just the six static admin pages.
- Each capability shows whether the signed license includes it, and where it is
  managed: an admin console page, the webmail workspace, deployment
  configuration, or a backend API with no admin screen yet.
- The catalog separates "not in this license" from "not yet exposed in the
  admin UI", so a plan limitation is no longer mistaken for a missing product.
- Counts summarise registered capabilities, licensed capabilities, admin
  surfaces and capabilities that still need an admin surface.

# Alt8Mail 3.9.5

Release date: 2026-11-18. Enterprise activation recovery and installer UX.

## 3.9.5 activation fixes

- **Executable bits are normalized after the enterprise overlay.** An
  enterprise bundle produced on a non-POSIX build host could extract with mode
  `0644`; systemd then failed the webmail and admin services with
  `203/EXEC`. Control now makes every installed binary executable before it
  switches the release, and the bundle build normalizes the same mode.
- **No duplicate upgrade prompts after activation.** The portal upgrade card is
  hidden once a valid enterprise license is activating or the binary is
  already enterprise. The remaining paid-capability action is labelled
  **Upgrade plan**, not another binary install.
- **Installer-style activation modal.** The activation flow now shows a
  segmented progress meter, numbered steps with completed/active/pending
  markers, a live status panel and retry-safe progress across the Control
  restart.

# Alt8Mail 3.9.4

Release date: 2026-11-18. Honest license state and in-place enterprise activation.

## 3.9.4 enterprise activation

- **The license page no longer pretends a community binary is enterprise.**
  Status now reports `binaryEdition` separately from the licensed edition. A
  valid enterprise token on a community build shows a warning and an
  **Install enterprise release** action instead of silently claiming the paid
  features are active.
- **License upload offers immediate activation.** After a valid enterprise
  token is uploaded, Control opens an activation modal, authorizes the signed
  release with the licensing portal, downloads it, verifies the checksum and
  Ed25519 signature, installs it beside the running release and restarts the
  services. Failure leaves the community release running.
- **Enterprise updates come from the licensing portal.** Once the enterprise
  binary is installed, Software updates checks the portal's enterprise channel
  and installs the newest signed bundle through the same transactional flow;
  it no longer queries the public community repository.
- **New portal endpoints.** `GET /api/v1/enterprise/version` publishes the
  available enterprise version and `POST /api/v1/enterprise/install` exchanges
  a valid license token for a one-time, deployment-bound bundle download.

# Alt8Mail 3.9.3

Release date: 2026-11-18. One-click community-to-enterprise upgrades and a
cleaner community admin experience.

## 3.9.3 upgrade and operations

- **One-click enterprise upgrade.** System Control now starts an upgrade in
  the licensing portal, waits for the signed licence and private enterprise
  bundle, verifies the SHA-256 checksum and Ed25519 release signature, installs
  the bundle beside the running release and restarts the services. The previous
  release stays available if any step fails.
- **Installer carries the vendor defaults.** A release built with the licensing
  portal URL and trust anchors configures the licence path, public key, key id
  and portal URL during installation. Operators no longer edit license
  variables over SSH before the first upgrade.
- **Operator console download and handoff.** The licensing console exposes a
  Download action for issued licences and a Send to Alt8Mail action that returns
  an existing licence to a deployment waiting for an upgrade.
- **Community admin gating.** Paid administration pages are hidden in community
  builds and direct links show an enterprise-required panel instead of an API
  error. Community builds continue to serve all core mail, admin and control
  features.
- **Localization.** New admin and upgrade strings, including sidebar category
  labels, are present in every console locale catalog.

# Alt8Mail 3.9.2

Release date: 2026-11-17. First clean community-edition baseline after the
enterprise split.

## 3.9.2 community baseline

- Paid enterprise implementations now ship only in the private enterprise
  module; the public tree contains the community application, the extension
  host, entitlement gates and community defaults.
- Old community 3.9.1 installations can update through the normal release
  updater to this community build. The update removes paid features from that
  host; enterprise hosts must use the private signed enterprise release.
- The admin console, System Control plane, mail stack, migrations and community
  features remain in the public release.

# Alt8Mail 3.9.1

Release date: 2026-11-16. Reliable capacity forecasts and SSH-safe intrusion bans.

## 3.9.1 operations fixes

- **Capacity forecasts measure real growth.** A projection now requires seven
  daily samples and uses the median of consecutive daily changes. One-time
  events such as a swapfile, restore, backup or package update no longer become
  a permanent “disk will fill” trend. A flat or shrinking filesystem is reported
  as stable rather than projected forward. Stale projected-full alerts resolve
  when the data no longer supports them.
- **Filesystem and mailbox data are separated.** The storage view and alerts now
  report scanned mailbox bytes, mailbox count and the bytes outside scanned
  mail. The forecast is explicitly labelled as a whole-filesystem forecast,
  because mail shares that filesystem with logs, PostgreSQL, backups and
  releases.
- **SSH bans affect only the SSH port.** The managed `a8m-ufw-port` Fail2Ban
  action adds a UFW deny rule for the configured sshd port, not for every
  destination port. The default is a 60-minute ban, a 10-minute find window and
  five failed attempts. A failed SSH login can no longer take webmail or System
  Control offline for that address.
- **Fail2Ban controls in System Control.** The Firewall page now exposes the
  jail status, banned addresses with unban actions, ban duration, find window,
  retry threshold and always-allowed addresses. The UFW page warns that
  host-wide rules can block every service, marks existing host-wide deny rules,
  and explains when the firewall is inactive.
- **Legacy lockout cleanup.** On startup, Control removes only host-wide
  REJECT/DENY rules that carry Fail2Ban's own comment, leaving port-scoped and
  administrator rules untouched. It also migrates a legacy
  `banaction = ufw` jail to the port-scoped action while preserving configured
  durations, retries and ignore addresses, then restarts Fail2Ban so the action
  is actually loaded.
- **Release retention.** Updates keep the current and previous release plus one
  additional release, and the ten newest update backups. The staging host had
  accumulated 87 releases and 12 GB before this fix.
- **Verification.** Live staging validation covered the real sample history,
  UFW activation, a real Fail2Ban ban/unban cycle, legacy action migration and
  host-wide rule cleanup. Go tests, `go vet`, the Node suite, version checks,
  JavaScript syntax checks and shell syntax checks pass.

Known limits: Fail2Ban enforcement requires UFW to be active; the managed action
protects the primary sshd port. Keep provider console access available when
changing firewall settings.

# Alt8Mail 3.9.0

Release date: 2026-11-16. Browser-held E2EE, client S/MIME and guarded migration.

## 3.9.0 encryption hardening

- **Client-side attachments.** OpenPGP and S/MIME messages encrypt attachments
  inline, with a 12-file, 8 MiB pre-encoding and 24 MiB encrypted-MIME limit.
  The reader decrypts and opens them without a server-side plaintext copy.
- **Browser-local encrypted drafts.** OpenPGP and CMS-encrypted S/MIME drafts
  are stored only in IndexedDB with a combined 25-draft cap. Locking the key
  flushes and closes client-side composers.
- **Browser-held S/MIME.** PKCS#12 import, passphrase-protected storage, CMS
  signing/encryption/decryption, inner signature verification (including
  attached CMS SignedData), S/MIME composer selection and per-address
  certificate publication.
- **Alias-aware publication.** One browser key or certificate can publish
  active rows for every owned send-as address it covers. Migration 0104 changes
  the identity uniqueness rule to include the address.
- **WKD and directory trust.** Advanced WKD is served for browser OpenPGP keys,
  with a direct-host handler; directory fingerprints are pinned in the browser
  and a changed key or certificate requires explicit review.
- **Account key recovery.** Passphrase-protected OpenPGP key backup/restore in
  the account, plus S/MIME PKCS#12 export/import. This is explicit recovery,
  not live multi-device synchronization.
- **Server-managed to client-side migration.** Admin preflight reports every
  server-held key, users export each active or retired key, and the migration
  requires a password, exact confirmation and legal-hold acknowledgement before
  deleting the wrapped keys and switching the policy. Migration 0105 records the
  report and installs a trigger that rejects new server-held private keys in
  client-side tenants.
- **OCSP and CRL revocation.** S/MIME validation supports `off`, `soft` and
  `require` modes with caching, timeouts, private-target guards and an 8 MiB
  response cap.
- **PKCS#11-backed S/MIME.** Server-managed S/MIME can sign and decrypt through
  a token using the new `smimehsm` package. Build with `-tags pkcs11` and
  `CGO_ENABLED=1`; default builds keep a clear stub. The server verifies that
  the token key matches the configured certificate at startup.
- **Protected MIME delivery.** Encrypted MIME entities now replace the message
  content headers instead of being appended after a blank line, so delivered
  PGP/MIME and S/MIME messages have one valid header block.
- **Locale coverage.** The new encryption strings are translated across all 33
  released catalogs, bringing the canonical catalog to 4,970 keys.
- **Verification.** PostgreSQL 18 migrations 0100-0105 were applied from a
  fresh database and 0104/0105 rollback/reapply was validated on a clone. Go
  tests, vet, the PostgreSQL encryption integration suite, the Node suite,
  browser E2E, i18n E2E and `npm run test:all` pass.

Known limits: browser S/MIME is RSA-only; native smart-card access still needs
a PCSC/PKCS#11 helper; server-managed decrypted attachments are not yet shown in
the webmail reader; the direct WKD host needs its own DNS and certificate; and
client-side mode does not hide metadata or protect plaintext mail sent by
others.

# Alt8Mail 3.8.0

Release date: 2026-11-15. System-wide languages, stable URLs and complete surface translation.

## 3.8.0 system-wide interface languages

- **One system default.** The Control Plane setting formerly labelled
  “Default webmail language” is now **Default system language**. It applies to
  webmail, the customer administration console and System Control whenever a
  user or administrator has not chosen a language explicitly.
- **Control Plane and admin localization.** Both consoles now load their own
  locale catalogs, translate rendered text and server responses, expose a
  language selector, and persist the operator’s explicit choice (including one
  made on the sign-in screen). Switching language reloads the console so no
  text is left in the previous language. A new `control.admin_users.language`
  column stores the Control Plane preference; the administration console uses
  the account’s existing language preference.
- **Stable URLs.** Hash routes use canonical ASCII folder and task slugs
  (`#/Inbox`, `#/Calendar`) instead of localized labels. Switching language no
  longer changes a bookmark into `#/Buzón de entrada`; old localized links
  still resolve.
- **People and add-ons.** Remaining hardcoded People and add-on labels are now
  catalogued. Add-on descriptions, taglines, categories, permissions, actions
  and field copy from the server are translated through the new phrase
  catalog.
- **Audit and errors.** Audit detail and target text is translated for the
  reader, including the administration audit table; server error text is
  translated through the phrase catalog with `errors.<CODE>` fallbacks. Nested
  detail values (folder names, browser names, audit list targets) and counted
  units (“1 recipient”, “3 conversations”) are translated recursively, and the
  sign-in credential error is localized.
- **Calendar dates.** Month, week, agenda, mini-calendar and upcoming-event
  dates format with the active locale, the mini calendar exposes localized
  narrow weekday initials in the same Monday-first order as its grid, and the
  agenda count uses plural rules.
- **Phrase catalogs for every surface.** Webmail, the administration console
  and System Control share the console phrase catalog. Every enabled locale
  ships a catalog; Spanish is complete, the major catalogs received a machine
  pass for the new control and audit strings, and any phrase without a
  translation falls back to English. Count-aware patterns use a `one|other`
  template selected from the numeric `{count}` value.
- **Default-language precedence.** Language selection now honors an explicit
  account preference first, then the Control Plane default. A browser language
  no longer overrides the configured system default; saving unrelated
  preferences no longer turns that default into an explicit user choice. A
  stale default that was also disabled falls back to English.
- **Verification.** Go tests cover the policy endpoints, admin default parsing,
  effective-language precedence and console assets; catalog tests cover phrase
  placeholder integrity and the per-locale administration catalogs; the web
  suite, 33-locale matrix, accessibility gate, `go test ./...` and `go vet ./...`
  pass.

# Alt8Mail 3.7.2

Release date: 2026-11-15. Language availability and boot reliability.

## 3.7.2 language policy and boot recovery

- **Stale-asset root cause.** Web Net cached every `.js`, `.mjs` and `.json`
  response for an hour. Module specifiers such as `i18n.mjs` and the locale
  catalogs are not versioned, so an upgraded shell could pair with the previous
  release's runtime or nine-locale manifest. JavaScript, ES modules and JSON now
  revalidate; only `?v=`-versioned CSS and images keep the immutable cache.
- **Authoritative locale policy.** `GET /api/locales` returns the shipped
  manifest with the Control Plane's enabled/disabled flags applied, and is
  `no-store`. The webmail no longer trusts a potentially cached static manifest
  for the picker, and a stale device cannot save a disabled language.
- **Control Plane language management.** A new **Languages** page enables or
  disables any webmail locale, selects the system default, records every change
  in the control audit log, and stores the policy in
  `control.webmail_locales`. English (United States) is always available as the
  fallback.
- **Dynamic selectors.** Webmail settings and onboarding, the customer admin
  default-language selector and the Control Plane default selector all read
  the enabled list. Admin-created users inherit the organization's default
  language, and existing accounts whose language was disabled are migrated to
  the system default on their next bootstrap.
- **No infinite splash.** A small boot watchdog runs before the module graph.
  A parse failure, missing import or hung boot now shows an actionable retry
  panel after 20 seconds instead of leaving “Opening your workspace” spinning
  forever. Top-level boot failures return to the sign-in screen with an error.
- **Verification.** New Go tests cover policy validation, fallback enforcement,
  handler persistence, embedded Control assets and the nginx cache contract;
  the webmail end-to-end suite covers disabling a locale, selector filtering and
  fallback on reload. `test:all`, the 33-locale matrix, the accessibility gate,
  `go test ./...` and `go vet ./...` pass.

# Alt8Mail 3.7.0

Release date: 2026-11-14. Full-core localization across 33 languages.

## 3.7.0 full-core localization

- **Every static product string is catalogued.** The webmail, login, onboarding,
  settings, compose, reader, calendar, Tasks, Cabinet and Office surfaces no
  longer carry hardcoded English UI copy; `i18n-lint --fail` reports zero
  candidates.
- **One canonical catalog.** `locales/en.json` now carries 4,661 keys. All 33
  target locale catalogs are complete, synced, enabled and certified, and the
  language selector offers every one.
- **Static HTML.** Every visible text node and visible `placeholder`, `title`,
  `aria-label` and `alt` attribute in `public/index.html` carries an i18n
  marker; `data-i18n-alt` support was added to the runtime.
- **Extracted modules.** Search, people, add-ons, Web Push, toasts, formatting,
  API transport, service-worker fallbacks and the mobile upload flow use the
  same catalogs.
- **Server messages.** The embedded Go catalog bundle now covers all 33
  webmail locales, including push notifications, notification-center titles and
  the public mobile upload page. The upload page negotiates the viewer's
  language and renders its expiry, session and upload states through the
  translator.
- **Structural checks.** Plural categories, placeholders, HTML tags and URLs
  are validated before promotion. Locale catalogs use CLDR plural rules and
  RTL markup uses logical properties.
- **Verification.** `i18n-certify --strict` reports 33 certified catalogs;
  `i18n-perf --fail`, `test:all`, the accessibility gate, the 33-locale
  screenshot matrix at 1440px/360px and the Go suite all pass.

The expanded catalog was machine-translated with the local Ollama pipeline and
promoted under the reviewer entry “Full-core machine translation pass”. Native
speaker spot-checks of the new strings remain the recommended quality pass.

# Alt8Mail 3.6.0

Release date: 2026-11-13. Forwarding, connected accounts, Office recovery and
workspace reminders.

## 3.6.0 forwarding, connected accounts and Office recovery

- **Mail forwarding.** The forwarding panel now leaves its faded state as soon
  as the switch is enabled, and the target field receives focus. Policy
  refusals such as an external recipient stay visible under the address field
  instead of disappearing with a toast. The generated ruleset is covered by a
  regression test that asserts the active redirect and optional stop
  instruction.
- **Connected accounts.** A dropped IMAP connection during login was being
  treated as a credential failure, which parked the account in `reauth` and
  stopped the sync worker from retrying. Connection drops, closed sockets and
  unexpected EOFs are now transient, IMAP login gets one fresh retry, transient
  OAuth refresh failures do not require reconnection, and OAuth accounts already
  in `reauth` return to the retry pool. Migration
  `0095_mail_account_transient_reauth` moves previously stranded accounts back
  into that pool.
- **Office after reboot.** The Control Plane database role was missing grants on
  `public.office_settings`, so the Office page failed to load its settings and
  the background monitor stopped refreshing `last_health_at`; webmail then
  reported Office as not enabled, healthy and reachable. The authoritative
  grants file now covers Office, add-on and web-push tables, and a missing
  singleton Office settings row is recreated automatically.
- **No mail-stack names in webmail.** User-facing Dovecot/Postfix wording was
  replaced with neutral “mail server”, “mailbox policy” and “applied
  automatically” copy. System Control and administrator surfaces keep their
  operational wording.
- **Task badge and reminders.** The Tasks rail icon and mobile tab show a
  pulsing count of open tasks. Clicking the badge opens the open-task filter.
  Due and overdue tasks produce persistent reminder cards with Done, Postpone
  and Open task actions.
- **Calendar reminders.** Events on the current day produce persistent,
  interactive cards that stay until Noted, Postpone or Reschedule. Postpone
  opens an in-app time chooser; Reschedule opens the existing event editor.
  Multiple events stack independently, and acknowledgements and snoozes survive
  a reload.
- **Cabinet view preference.** List, Tiles and Thumbnails are now saved in the
  user's database preferences as `cabinetView` as well as local storage, so the
  chosen layout follows the user across devices and sign-ins.

# Alt8Mail 3.5.1

Release date: 2026-11-13. Swap-file fix for the legacy mail root.

## 3.5.1 swap files work on /var/vmail

- The legacy mail root `/var/vmail` is normally a directory on the root
  filesystem, not a separate mountpoint. The swap-file runner previously
  required an active mount and failed after a valid plan was created.
- The swap planner, candidate list and runner now accept the legacy root when
  its directory and filesystem are readable. Managed volumes still require an
  active mount, so a missing mount cannot silently place a swap file on the
  parent filesystem.
- Existing failed plans can be retried by creating a new swap plan after
  installing 3.5.1.

# Alt8Mail 3.5.0

Release date: 2026-11-13. Swap visibility and safe swap creation in Disk
Management.

## 3.5.0 host swap without leaving the panel

- **Swap card.** Disk Management now reports total, used and free swap, the
  active swap areas, their type, size, usage, priority and whether they persist
  at boot. Host memory, available memory and `vm.swappiness` are shown alongside
  the current swap state.
- **Swap file creation.** An administrator can allocate a hidden
  `.a8m-swapfile` on an online ext4 or XFS mail volume. The wizard recommends a
  size from host memory, enforces at least 64 MiB and protects the existing
  capacity reserve. The file is formatted, activated, restricted to mode 0600
  and persisted with a managed fstab block.
- **Dedicated disk creation.** A blank whole disk can be dedicated to swap.
  Disks with partitions, a mountpoint, system protection or active swap are not
  eligible. The operation requires the `storage.destroy` permission, a typed
  erase confirmation, and revalidates the disk identity and blank state in the
  root runner before the first write.
- **Reviewed operations.** Both modes use the existing storage plan/execute
  token flow and appear in the live Storage operations console. Failure cleanup
  removes a partial swap file, deactivates swap, restores fstab and removes a
  partial swap signature.
- NFS volumes are excluded from swap-file creation. Host disk provisioning must
  be enabled, and the existing runner and operation gates still apply.
- This release adds swap visibility and creation; turning off or removing an
  existing swap area is not included.

# Alt8Mail 3.4.2

Release date: 2026-11-13. Completes the 3.4.1 upgrade recovery.

## 3.4.2 create the storage root even on the first hop from 3.3.1

- The updater that runs during a 3.3.1 → 3.4.x update is the one installed in
  3.3.1, so it cannot run the 3.4.1 directory-creation change. The control unit
  now runs `/opt/a8m/current/scripts/alt8mail-ensure-storage-root.sh` as an
  `ExecStartPre`, creating `CONTROL_STORAGE_ROOT` (default
  `/srv/a8m-storage`) as `root:vmail` mode `0750` inside the service sandbox.
  This makes the first upgrade create the directory even when the old updater
  performed the restart.
- The helper is idempotent and falls back to creating the directory without the
  `vmail` group when that group is unavailable. The unit still marks the path
  optional, so a missing root cannot block the control plane from starting.
- This completes the 3.4.1 fix. Use 3.4.2 for upgrades; 3.4.0 should not be
  installed over an existing 3.3.1 host.

# Alt8Mail 3.4.1

Release date: 2026-11-13. Upgrade hotfix for existing 3.3.1 hosts.

## 3.4.1 upgrades start on hosts that do not yet have a storage root

- The 3.4.0 service units added `/srv/a8m-storage` to `ReadWritePaths`. systemd
  refuses to start a unit when a `ReadWritePaths` entry does not exist, and an
  existing 3.3.1 host did not have that directory yet because it was created
  only by a fresh install. After migrations, `alt8mail-control` therefore failed
  to start and the updater rolled back to 3.3.1.
- The control and mail-provisioning units now mark the storage root optional
  with the systemd `-` prefix. The updater also resolves
  `CONTROL_STORAGE_ROOT` (defaulting to `/srv/a8m-storage`) and creates it as
  `root:vmail` mode `0750` before installing the new units and restarting
  services. A missing directory can no longer block the control plane from
  booting; storage can still be repaired from Disk Management.
- Regression tests assert that both units use the optional path and that the
  updater creates the storage root before its restart loop.
- Use 3.4.1 for upgrades. The 3.4.0 release asset remains available for audit
  history but should not be installed over an existing host.

# Alt8Mail 3.4.0

Release date: 2026-11-13. Managed mail storage, capacity protection and a rebuilt
Disk Management console.

## 3.4.0 mail storage you can see, place and change safely

- **Storage-aware Dovecot homes.** The managed userdb query now returns
  `mail_home` when a mailbox has one and falls back to the legacy
  `/var/vmail/<domain>/<local-part>` path for existing mail. The storage doctor
  backs up, validates and reloads the query and verifies a mailbox before
  placement is unlocked. Upgrading an existing host does not move mail.
- **Managed volume registry and placement.** The Control Plane registers
  ext4, XFS and NFS mail volumes and records their mount path, UUID, device,
  fstab block, reboot verification and health. New mailboxes resolve placement
  by mailbox override, then domain default, then global default, then the legacy
  root. Changing a default never moves existing mail.
- **Reviewed host operations.** Add, adopt, extend, retire and wipe volumes run
  through plan and execute steps, a typed confirmation, a root storage runner
  and a replay-resistant plan token. The runner revalidates the device, serial
  or WWN, mount state and filesystem before writing. fstab edits use marked
  blocks and restore the original when validation fails.
- **Verified mailbox moves.** Moves copy, verify and switch mailbox homes while
  retaining the source for a configurable grace window. Operators can roll back
  or finalize from the live operation console; progress, step state, logs and
  per-mailbox results are retained.
- **Capacity policy and protection.** Configurable advisory through emergency
  thresholds, reserve and minimum free space, a 14-day linear forecast,
  projected-full alerts, alert re-arming, low-I/O usage scans and mailbox-level
  rebalance recommendations. At critical or emergency capacity the storage guard
  holds new inbound mail with SMTP `4.3.1` and webmail/Cabinet uploads with HTTP
  `507` while existing mail stays readable.
- **Hardware awareness.** Physical disk inventory now includes SMART health,
  topology detection for LVM, RAID, LUKS and multipath, clear protection reasons
  and safe candidates. Mounted NFS filesystems can be adopted without changing
  the export. Growth that cannot be performed safely, such as thin-LVM or
  LVM-RAID extension, is refused with an explanation.
- **Rebuilt Disk Management UI.** OS-style drive cards show capacity, path, type
  and health; status filtering covers online, attention, offline, retired and
  wiped volumes; clicking a volume opens full identity, health, forecast and
  action details. Capacity outlook moves directly below the KPI cards, capacity
  policy lives in a confirmed modal with a before/after change review, and
  storage operations paginate ten per page with sortable Operation, Target,
  Mailboxes and Status columns.
- **Scalable move picker.** The move wizard searches and pages the mailbox
  directory on the server, shows user, quota, used bytes, usage and current
  volume, and keeps checkbox selection while paging. It is validated against a
  5,120-mailbox directory. A single individually selected move is limited to 500
  mailboxes by the planner; larger batches use a whole-domain move or multiple
  jobs.
- Migrations `0088_storage_volumes` through `0094_storage_forecast_alert` and
  the new `a8m-storage-runner` binary ship in this release.

# Alt8Mail 3.3.1

Release date: 2026-11-12. Office window polish and Reply-To delivery fix.

## 3.3.1 a calmer Office window and a Reply-To that reaches the recipient

- The Office minimize, maximize and close controls now sit at the far right edge
  of the header, matching the compose window. **Open** and **Versions** keep
  their place next to the centered search.
- The dark page scrim is gone. The Office window floats over the workspace like a
  compose window, so the underlying page stays visible and interactive while the
  editor is open.
- Window position and size are stored locally and in the user's server
  preferences (`officeWindow`). Last-write-wins timestamps keep both copies in
  step, so signing in on another system restores the remembered geometry for
  every Office type.
- A new Office window opens at a focused, centered 78% × 82% size rather than
  almost filling the viewport. All four edges and corners are draggable resize
  handles, alongside the visible bottom-right grip. Double-clicking the header
  still maximizes and restores.
- The recent-documents dropdown now closes on any click outside the search
  field: elsewhere in the header, another part of the Office chrome, the page
  behind the window, or inside the ONLYOFFICE editor.
- PWA install screenshots were refreshed at the manifest's exact sizes
  (1440x1000 wide, 390x844 narrow) with the current Alt8Mail icon and logo.
  The manifest and page description now read “AI-powered enterprise email with
  workspace for business email, calendar, files, tasks, office and AI.”
- The mobile footer's Settings tab is replaced by a four-square **Menu** icon
  that opens the mailbox and folder drawer, matching the mobile folder control.
- **Reply-To delivery fix**: the normalized `outbox` table did not carry
  `reply_to`. The send handler stored the value in the state document, but the
  delivery worker rebuilds the queue from the outbox table and therefore dropped
  it before building the MIME message. Migration `0087_outbox_reply_to.sql` adds
  the column, persistence and loading now include it, and delivery resolves the
  identity reply-to from the database as a fallback for older queued items. An
  integration test asserts the submitted message contains the Reply-To header.

# Alt8Mail 3.3.0

Release date: 2026-11-12. Office window, open dialog and email-to-task.

## 3.3.0 an Office window that behaves like the desktop app

- The Office editor header has a centered search field (also **Ctrl K**). It
  searches only the file type of the open editor — documents, spreadsheets or
  presentations — across Cabinet names and email attachments, including
  server-side attachment filename search beyond the loaded mailbox page. Results
  show in a grouped dropdown; choosing one opens it in the shared Office window
  and docks the previously active document.
- The Office window can be moved by dragging its header and resized from its
  bottom-right grip, with the size and position remembered per user. Double-click
  the header to maximize or restore. Mobile keeps the full-screen editor.
- A new **Open** button beside Versions opens a moveable Open window with three
  columns: matching Cabinet files, matching email attachments and a device
  column. The device column browses the local PC or shows a QR code for a phone
  upload. A file uploaded from the phone lands in Cabinet and opens in the
  Office window automatically; the upload session now reports the created file
  id so the desktop can open the exact file.
- Emails can now be turned into tasks. The reader toolbar has an **Add to task**
  action and the message menu has **Add to task** as well. The task editor shows
  the linked email, pre-fills the title, sender and snippet, and saves the task
  with `linkedThreadId` / `linkedSubject` so the conversation stays attached.

# Alt8Mail 3.2.2

Release date: 2026-11-12. Compose regression fix.

## 3.2.2 compose keeps its identity, layout and calm typing area

- A message is now always routed through the account that owns its From
  identity. Choosing a native identity inside a compose window while a
  connected Gmail/Outlook mailbox is open no longer fails drafts or sends with
  `Choose the connected account address as the From identity`. The compose
  account is derived from the selected From address everywhere: draft save,
  send, schedule, AI tone and team templates, and it is repaired when identities
  refresh.
- The forwarding card's recipient field styles are scoped to forwarding again.
  The compose To, Cc and Bcc rows keep their original flat layout with no
  rounded border or focus box.
- The compose writing area no longer draws a rectangle while typing. The
  keyboard focus ring is shown only when the editor is reached with Tab, so the
  keyboard accessibility gate still passes.

# Alt8Mail 3.2.1

Release date: 2026-11-12. CI reliability for the 3.2.0 line.

## 3.2.1 deterministic mail refresh in the HTML reply fixture test

- The end-to-end HTML reply fixture no longer waits on the webmail's
  15-second new-mail poll. Headless CI browsers throttle that timer, so the
  test now clicks the toolbar **Refresh** button and waits for the injected
  message to render. Application behavior is unchanged from 3.2.0.

# Alt8Mail 3.2.0

Release date: 2026-11-12. Identity signatures, defaults and forwarding polish.

## 3.2.0 sending identity settings that behave like they look

- **Edit sending identity** now edits the From display name, reply-to and the
  signature in one place. Display name and reply-to work for organization
  aliases, including the primary account, not just self-service identities.
  Outgoing mail uses the edited From name, and a configured reply-to is written
  as a `Reply-To` header when the message leaves the server.
- The identity editor no longer embeds its own rich-text signature pad. It shows
  the signature library from **Settings → Mail & reading → Signatures** as a
  selectable list with a preview and its automatic-use chips. **New signature**
  opens the existing signature editor on top of the identity dialog; a saved
  signature is selected immediately and available on every identity.
- A **Default sending identity** switch sets the identity used for new messages.
  Turning it on clears the previous default, so exactly one identity is the
  default; the identity list shows a `Default` badge. An identity must be
  verified before it can become the default, and the last default cannot be
  switched off.
- The identity list now shows the signature actually attached to each address
  (`Signature · Sales follow-up`) or `No signature`, plus the reply-to and the
  default badge. Compose inserts the selected identity signature when that From
  address is chosen, including when switching identity inside an open message.
- Identities now reference a named signature from the library, so editing a
  signature updates every identity that uses it. Deleting a signature clears it
  from those identities. Legacy per-identity signatures are preserved as a
  `Current signature` choice until they are replaced.
- **Mail forwarding** is rebuilt as a focused card: a forwarding switch, a
  recipient-style **Forward to** field with a clear control and a live resolved
  address chip, and options that dim while forwarding is off. Invalid and
  self-forwarding addresses are explained inline before anything is saved.
- The **Attached to this message** side of the compose attachment manager is now
  a vertical list like the Cabinet column instead of a horizontal card strip, so
  long file names are readable and many attachments stay scannable.
- Migration `0086_identity_signature_choices.sql` adds the named-signature link
  and the per-address display-name and reply-to overrides.

# Alt8Mail 3.1.2

Release date: 2026-11-11. OAuth popup and transient IMAP recovery.

## 3.1.2 connected-account resilience and OAuth result page

- A temporary Gmail IMAP failure such as `Temporary system problem. Please try
  again later` no longer sets the mailbox to `Reconnect required`. It is stored
  as a retrying sync error, the account stays in the sync worker, and the worker
  retries with backoff (2, 4, 8, then every 10 minutes). A successful pass
  restores the `Connected` status automatically.
- The account summary endpoint now returns `status` and `lastError`; the webmail
  refreshes them with its existing 15-second summary poll. Connected accounts
  settings, the account menu warning and the status row update without a page
  reload. Transient errors read `Sync error · retrying`; only real credential
  failures ask for a reconnect.
- Migration `0085_mail_sync_transient_status.sql` moves accounts that the old
  build already marked `Reconnect required` because of Gmail's temporary
  system-problem error back into the retrying worker, so they recover without
  asking the user to reconnect.
- Gmail's XOAUTH2 challenge scope is no longer appended to transient errors,
  where it was misleading. It remains in genuine authentication failures.
- The OAuth result window is a designed success/error card instead of an
  auto-closing page. It shows the connected mailbox, has a real **Close window**
  button and a **Return to Alt8Mail** button that focuses the main window. It no
  longer closes itself after 900 ms.
- On success the result page posts to the opener, focuses it immediately and the
  main app reloads bootstrap, re-renders connected accounts and opens the new
  mailbox, so the main window shows the `Connected` state while the result card
  is still visible.

# Alt8Mail 3.1.1

Release date: 2026-11-11. Gmail IMAP OAuth diagnostics.

## 3.1.1 explain Gmail and Outlook IMAP sign-in failures

- A successful Google/Microsoft sign-in can still fail at IMAP when the token
  did not receive the mail scope or the mailbox has IMAP disabled. The callback
  now checks the scopes returned by the token endpoint before opening IMAP:
  - Google must grant `https://mail.google.com/`.
  - Microsoft must grant `https://outlook.office.com/IMAP.AccessAsUser.All`.
  A missing scope now names the exact console setting and how to force a fresh
  grant instead of showing a generic “Invalid credentials” message.
- When the token has the scope, the error explains that Gmail accepted the
  sign-in but IMAP was refused, with the Gmail/Workspace IMAP and Microsoft
  365 IMAP/permission checks to perform.
- The IMAP XOAUTH2 client now captures Gmail's JSON authentication challenge
  and includes the server-requested scope in the error detail, distinguishing a
  consent/scope problem from a mailbox policy problem.
- The Mail OAuth page notes that adding a scope or API permission after a
  mailbox already connected requires reconnecting that mailbox so the provider
  records the new consent.

# Alt8Mail 3.1.0

Release date: 2026-11-11. Gmail and Outlook OAuth from System Control.

## 3.1.0 mail OAuth applications

- System Control has a new **Mail OAuth** page under Mail for the Google and
  Microsoft OAuth applications used by the multi-mailbox inbox. It replaces
  hand-editing `ALT8MAIL_GOOGLE_MAIL_*` and `ALT8MAIL_MICROSOFT_MAIL_*` in
  `/etc/a8m/app.env`.
- Each provider has an enable switch, client ID, encrypted client secret,
  Microsoft tenant (common, organizations, consumers, directory ID or verified
  domain), the exact authorized redirect URI with copy, requested scopes and
  step-by-step registration guidance for Google Cloud Console and Microsoft
  Entra.
- Secrets are sealed with the control-plane key before being stored in
  `control.mail_oauth_providers`. The API returns only whether a secret exists
  and where it came from; it never returns a stored secret.
- **Apply to webmail** writes the complete provider configuration to `app.env`,
  restarts the webmail and admin services and waits for their health checks.
  A failed restart restores the previous environment and brings the old
  services back. Saving a draft alone does not affect users.
- **Test connection** saves the draft, verifies the HTTPS redirect URI, checks
  that each enabled provider's OIDC metadata is reachable, and reports the
  authorize and token endpoints. Disabled providers are reported as skipped.
- Existing installations keep working without a click: before the first save
  the page mirrors the credentials already in `app.env`, including the shared
  OIDC client fallback, and marks whether mail-specific or shared credentials
  are in use.
- Disabling a provider writes an explicit `ALT8MAIL_*_MAIL_OAUTH_ENABLED=false`
  so shared OIDC credentials can no longer silently enable mail sign-in.
- The webmail now receives provider availability at bootstrap. When an
  administrator has not configured an OAuth application, the Add mail account
  wizard explains the missing setup and disables the provider button instead
  of opening a popup that immediately fails. Gmail still offers the app-password
  path in that state.
- The old “OAuth is not configured for this provider” error now names the
  provider and points to **System Control → Mail OAuth**.
- Added migration `0084_mail_oauth_config.sql` and unit coverage for credential
  resolution, encrypted draft storage, disable semantics, environment import,
  redirect URI validation and provider discovery.

# Alt8Mail 3.0.6

Release date: 2026-11-11. Enterprise interaction polish.

## 3.0.6 compose, scheduling and dialog UX

- The compose editor no longer paints a focus rectangle on mouse click. A
  keyboard-only focus ring remains when tabbing into the editor.
- Booking pages use a redesigned scheduler modal: a title/slug URL preview,
  duration presets, a full searchable list of IANA timezones, real time
  pickers for the availability window, weekday selection, a round-robin
  switch and inline validation. The public booking page is rebuilt as a
  responsive two-column enterprise scheduling page with grouped time slots,
  selected-time summary, proper form fields and a booking confirmation state.
- Meeting polls inherit the same form-field system and the full timezone list;
  their date/time option rows remain proper pickers.
- All native `window.prompt` dialogs are replaced with an in-app prompt modal.
  Link insertion, template variables, payment links and AI category feedback
  now use styled dialogs with validation and focus return.
- Follow up / Remind me use a designed reminder dialog with day presets, a
  custom day count and a due-date summary. Active reminders show a day-count
  badge on the button, can be updated in place or removed.
- Marking a sender VIP now visibly switches the button to a gold active state;
  active mute/follow-up states use the accent state style.
- Fixed the booking list date formatter, which previously threw after opening
  a page's bookings.

# Alt8Mail 3.0.5

Release date: 2026-11-11. Large-file scanning, settings UI and license-gate UX.

## 3.0.5 large-file scans and settings polish

- Go is updated to `1.26.11`, fixing the `net/http` `MaxBytesReader`
  header-race crash (GO-2026-6859). The CI vulnerability gate now passes with
  only the reviewed `GO-2026-6909` SAML advisory allowlisted.
- ClamAV scan limits are raised automatically to 320 MB (`StreamMaxLength`,
  `MaxFileSize`, `MaxScanSize`) with a ten-minute `MaxScanTime` by the
  control-plane startup check, so a 169 MB Cabinet share no longer fails with
  “The file could not be scanned for malware.” The scanner connection timeout
  is now ten minutes, and a scanner that still rejects a file returns a specific
  `FILE_SCAN_LIMIT` message with the exact limits to raise.
  `scripts/alt8mail-ensure-clamav-limits.sh` applies the same change manually
  and restores the previous config if clamd fails to restart.
- Keyboard shortcuts settings now use the settings-row design system with
  styled key fields, duplicate-key highlighting and a proper reset action.
- The AI workspace settings page uses consistent cards, list rows and form
  fields for triage, obligations, mailbox search and sender intelligence.
  License-gated AI capabilities render a dedicated notice instead of a bare
  error string.
- High contrast mode is a real on/off switch with `role="switch"`; per-folder
  density is rendered and wired to folder changes (it previously had no render
  call, so overrides never appeared).
- The Rules page reaction-rule section is wrapped in a settings card with a
  designed empty state and footer.
- The admin Integrations, Compliance and Team inboxes pages now show a proper
  “Upgrade to unlock …” license card with the feature key, copy action and
  organization-settings link instead of a generic “Something went wrong” page.
  Feature-gate errors now carry the missing feature key in the API payload.

# Alt8Mail 3.0.4

Release date: 2026-11-11. Large-file Cabinet uploads for the compose share flow.

## 3.0.4 chunked Cabinet uploads

- Oversized compose attachments now upload to the Cabinet in 8 MB chunks, so a
  169 MB file or larger no longer trips the reverse proxy's 25 MB request-body
  limit with `Request failed (413)`.
- The share dialog shows real upload progress (percentage, bar and button
  state) before creating and inserting the secure link.
- The Cabinet accepts uploads up to 300 MB through the resumable chunk API:
  start, append at a verified offset, then complete. Incomplete or abandoned
  uploads leave only a temp part file and can be cancelled.
- The chunk API works through the existing `/api/` proxy rules, so upgrading
  does not require an nginx body-size change.
- Added an integration test for start/offset/chunk/complete and an end-to-end
  compose share test that uploads an oversized file, validates a private link
  and inserts it into the message.

# Alt8Mail 3.0.3

Release date: 2026-11-11. Compose attachment-limit redesign.

## 3.0.3 compose share dialog

- Replaced the bare attachment-limit prompt with an interactive Cabinet-link
  flow: a clear oversized-file summary, one-tap expiry presets plus a custom
  day count, selectable public/private link cards, a conditional recipient
  email field, an optional message note, a live link summary and a busy state
  while the file is uploaded and shared.
- The share dialog now layers above the Compose attachments picker instead of
  behind it, returns focus when dismissed, validates private links inline, and
  closes the picker after the link is inserted so the message is visible.
- Fixed the share insertion using an undefined date formatter, which previously
  failed after the Cabinet link was created.

# Alt8Mail 3.0.2

Release date: 2026-11-11. Branding and shell-cache patch.

## 3.0.2 branding and cache fix

- The webmail service worker now versions its cache with the release, requests
  all shell assets with the current version, and serves CSS/JS network-first
  with an offline cache fallback. Previously the worker and every asset URL
  were unchanged across releases, so returning browsers kept the old
  stylesheet and script while loading the new HTML. That mix rendered the new
  logo images at intrinsic size and injected the old `AL` initials tile.
- Every brand image now declares its rendered width and height, so a stale or
  missing stylesheet can never let a 512 px logo cover the header.
- Generated initial tiles (`AL`/`A8`) are removed from the app shell, tenant
  row, passkey prompt and onboarding rail; those marks now use the supplied
  icon artwork. White-label tenants show their product name without a
  generated monogram.
- The customer admin app ships its own brand assets under `/brand/` and no
  longer requests webmail icon paths from the admin host. Brand images that
  fail to load hide cleanly instead of showing the browser's broken-image
  glyph.
- CI and `npm run test:all` verify that `VERSION`, `package.json`, the shell
  asset query string, the service-worker cache version and the control-plane
  version stay in sync.

# Alt8Mail 3.0.1

Release date: 2026-11-11. Security and release-gate patch for 3.0.0.

## 3.0.1 security patch

- Go is pinned to `1.26.10`; all standard-library vulnerabilities reported by
  `govulncheck` in the 3.0.0 build are fixed.
- The `crewjam/saml` advisory `GO-2026-6909` has no fixed upstream release.
  SAML assertion panics are contained and audited, and the CI vulnerability
  gate allowlists only that reviewed ID.
- CI now installs Chrome before web E2E tests, pins the vulnerability gate and
  separates the allowlisted advisory from unapproved findings.

## 3.0.0 highlights

- **Enterprise entry:** OIDC/SAML SSO, SCIM, seat/license enforcement, white-
  label and custom webmail hosts, per-folder restore/DR, accessibility gates.
- **Team operations:** shared inboxes with round-robin/SLA/presence, shared
  drafts with versions/comments, team templates, automation builder, team
  collaboration extensions and the MCP seed.
- **Scheduling and deliverability:** recipient-time scheduling, follow-ups,
  native mail merge, booking pages, template gallery/payment links, DMARC
  ingestion, opt-in campaign tracking.
- **AI/MCP moat:** governed MCP write tools with approvals and dry-run, tenant
  AI budget, AI triage, obligations ledger, natural-language search, meeting
  prep, Office document AI and DLP policy assist.
- **Compliance:** advanced DLP detectors and simulation, legal holds,
  classification retention, eDiscovery JSON/MBOX exports with manifests and
  review queue, BYOK envelope keys, file governance, OCR/file index, signed
  audit bundles and SIEM packs.
- **Platform:** `/api/v1` with scoped API keys, idempotency, OpenAPI and signed
  webhooks; HubSpot/Salesforce/Zoho/Pipedrive CRM cache; native Gmail API and
  Microsoft Graph mailbox import; Slack/Teams interactive actions; dual
  delivery journaling; external iCalendar free/busy federation; migration
  fidelity reports; tenant analytics, support bundle, opt-in telemetry,
  phishing-awareness simulation, command palette and offline compose/send
  queue.
- **Final scope completion:** embedded outbound/file watermarks, immutable
  byte-copy Cabinet versions, shared-draft anchored comment pins and version
  restore, high-contrast mode and per-folder density, and a compose attachment
  fallback that uploads oversized files to the Cabinet and inserts an
  expiring public or recipient-locked share link.
- **Phase scope completion:** recurring tasks and reminder notifications,
  policy-checked auto-sent auto-replies, booking edit/disable/cancel with
  booking lists, scheduled DMARC rua polling, per-tool MCP approvals and
  distributed per-connection rate limits, API cursor pagination, persistent
  offline mailbox/task mutation queues, customizable keyboard shortcuts,
  calendar resource approval workflows and external CalDAV free/busy.
- **Paid entitlements:** every Phase 3–6 paid capability is gated by canonical
  license feature keys. Licenses may use `all`/`*`; System Control and the
  webmail license snapshot report `availableFeatures`/`missingFeatures` for
  reissue.
- **Encrypted storage:** BYOK now seals Office binaries and Cabinet file blobs
  with versioned tenant DEKs (AES-256-GCM) and supports client-encrypted
  folders. Mail message rows remain protected by database/storage-at-rest
  encryption; see the known limits below.
- **Branding:** production icon/logo assets used across webmail, System Control,
  PWA manifest and install surfaces.

## Migrations

The release applies migrations `0061_scim` through
`0083_phase_scope_completion` on top of the `v0.27.6`/`0060` schema. The upgrade smoke test applies the
release tag, upgrades, rolls back new migrations in reverse order and reapplies
them. `0081_encrypted_storage` adds blob encryption metadata;
`0082_addon_action_tokens` adds single-use Slack/Teams action capabilities;
`0083_phase_scope_completion` adds task recurrence/reminder state, DMARC poller
state, distributed MCP rate events and resource-approval metadata.

## Upgrade notes

- Reissue or confirm customer licenses include the Phase 3–6 feature keys (or
  `all`). Legacy licenses keep their existing features but do not silently gain
  new paid capabilities.
- BYOK is opt-in. It encrypts tenant secrets, Office binaries and Cabinet file
  blobs; client-encrypted Cabinet folders are never decrypted server-side.
  `mail_messages` rows and their full-text index still rely on PostgreSQL
  storage encryption. See `docs/RELEASE_GAP_AUDIT.md`.
- Slack interactive buttons require the workspace signing secret in the Slack
  app's Interactivity settings. Teams action links use the configured public
  URL (`ALT8MAIL_PUBLIC_URL`) and expire after seven days or first use.
- Gmail/Graph migration needs an OAuth token with mail read access; use a JSON
  refresh-token bundle for migrations longer than the provider access-token
  lifetime.
- OCR requires an operator-provided `A8M_OCR_COMMAND` sidecar.
- Webhooks, telemetry, SIEM and external free/busy feeds must be public HTTPS
  endpoints; private and local addresses are rejected outside test seams.

## Verification

Local gates before the tag:

- `go test ./...`, `go vet ./...`, `go build ./...`
- PostgreSQL `internal/httpapi` suite via `A8M_SCIM_TEST_DSN`
- `npm run test:all`
- `npm run test:a11y`
- `tools/migration-upgrade-smoke.sh`
- Tag-triggered `.github/workflows/release.yml` builds the archive and
  SHA-256 checksum.

## Known release decisions

See `docs/RELEASE_GAP_AUDIT.md` for the remaining scope decisions and operator
drills required before publication.
