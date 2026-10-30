# Alt8Mail First Install Guide

**Applies to:** Alt8Mail **0.3.12** on **Debian 12 (bookworm)**
**Audience:** the administrator who will install, own and operate the deployment.

This is the practical path from a fresh server to a working mail domain. It covers the decisions you must make before installation, what the installer changes, how to reach the three administration planes, how to complete DNS and TLS, how to prove mail flow, how to add the first users, and the operating habits that keep the platform recoverable.

> **Use a fresh host.** The installer takes ownership of nginx, Postfix, Dovecot, PostgreSQL, Redis, UFW and the hostname. Do not run it on a server that already hosts another mail or web stack.

---

## 0. What you are installing

Alt8Mail is a self-hosted mail platform, not a hosted mailbox add-on. The installer creates a complete Debian mail stack:

| Component | Role | Default exposure |
| --- | --- | --- |
| nginx | TLS termination, webmail, customer admin proxy, Let's Encrypt webroot | 80, 443 |
| Go web API | Webmail API, mailbox sync, sending, workspaces | `127.0.0.1:8080` |
| Go admin API | Customer and tenant administration | `127.0.0.1:8081` |
| PostgreSQL 15 | Identity, tenants, policy, state, audit, mail index | `127.0.0.1:5432` |
| Redis 7 | Sessions, rate limits and cache | `127.0.0.1:6379` |
| Postfix | Inbound SMTP and authenticated submission | 25, 587 |
| Dovecot | IMAPS, POP3S, LMTP, Sieve and ManageSieve | 993, 995, 4190 |
| Rspamd + ClamAV | Spam scoring, DKIM signing and malware scanning | local services |
| System Control | Host and mail-stack operations panel | `0.0.0.0:8808` |
| Maildir | Actual message storage | `/var/vmail` |

The result has three separate front doors:

| Plane | URL | Signs in with | Purpose |
| --- | --- | --- | --- |
| Webmail | `https://mail.example.com/` | Mailbox web password | Reading and sending mail |
| Customer admin | `https://mail.example.com/admin/` | Same Alt8Mail identity | Tenants, users, domains, policy, audit |
| System Control | `https://mail.example.com:8808/` | Separate control account | Host, TLS, services, firewall, backups, logs |

Keep those planes separate. A webmail session cannot become an admin session, and the customer admin cannot manage the host. System Control is the root-level plane and must be treated accordingly.

---

## 1. Preflight

### 1.1 Server baseline

| Item | Requirement |
| --- | --- |
| Operating system | Debian 12 (bookworm), 64-bit |
| Access | `root`, or a sudo account that can become root |
| CPU / RAM | 2 vCPU / 4 GB as a practical floor; 4 vCPU / 8 GB or more for production |
| Disk | 50 GB minimum for an evaluation host; size production for mailbox data, attachments, database growth and backups |
| Network | Public IPv4 address, with provider firewall and reverse DNS under your control |
| Host state | Fresh Debian install, no other mail/web stack |
| Protection | A provider snapshot taken immediately before installation |

The installer adds a 2 GiB swap file when the host has less than 2 GiB of swap, sets the time zone to UTC and sets the mail hostname. Keep UTC on the host; user-facing time zones are per-user preferences.

The commands in this guide assume a root shell. If you sign in with a sudo account, prefix host-level commands with `sudo`.

### 1.2 Choose the names

You need five values before you begin:

| Value | Example | Rules and meaning |
| --- | --- | --- |
| Mail domain | `example.com` | The domain in users' addresses. The owner address must end in `@example.com`. |
| Mail hostname | `mail.example.com` | The MX target and the name on the TLS certificate. |
| Tenant name | `Example Corp` | Display name for the organization. |
| Tenant slug | `example` | Lowercase letters, digits and hyphens; no spaces. |
| System Control email | `sysadmin@example.com` | A separate login for the host control plane. It does not create a mailbox. |

The installer also asks for an owner email, owner display name and a Let's Encrypt account email. The default owner is `owner@example.com`.

### 1.3 DNS before installation

Add these records **before** running the installer if you want an automatic Let's Encrypt certificate:

| Type | Name | Value | Why |
| --- | --- | --- | --- |
| A | `mail.example.com` | Your server's public IPv4 | Webmail, TLS and MX target |
| A | `example.com` | Your server's public IPv4 | The installer requests one certificate covering both names |
| PTR / rDNS | Your server's IPv4 | `mail.example.com` | Set at the VPS provider; improves outbound reputation |
| CAA (if present) | `example.com` | Include `letsencrypt.org` | A restrictive CAA record blocks issuance |

Important details:

- If `mail.example.com` is behind Cloudflare or another HTTP proxy, set it to **DNS only**. A proxy cannot carry SMTP or IMAP.
- The installer's Let's Encrypt request covers both the bare domain and the mail hostname. If either A record is missing or points elsewhere, it falls back to a self-signed certificate.
- Use a low TTL (300–3600 seconds) while you are setting this up.
- If the bare domain must remain pointed at a different website, plan a dedicated mail domain instead of forcing the standard installer path. Do not start production with a certificate warning.

Check from your workstation:

```bash
dig +short A mail.example.com
dig +short A example.com
dig -x YOUR_SERVER_IPV4 +short
dig +short CAA example.com
```

All three names should point where you expect. The `dig -x` result should be `mail.example.com.` once the provider has set reverse DNS.

### 1.4 Ports and provider filtering

The installer configures UFW, but it cannot override a provider firewall or a blocked port.

| Port | Purpose | Who needs it |
| --- | --- | --- |
| 22/tcp (or your SSH port) | Administration | You only |
| 80/tcp | ACME challenge and HTTP redirect | Public |
| 443/tcp | Webmail, customer admin and TLS | Public |
| 25/tcp | Inbound SMTP | Public; **provider must allow it** |
| 587/tcp | Authenticated submission | Mail clients |
| 993/tcp | IMAPS | Mail clients |
| 995/tcp | POP3S | Mail clients that use POP |
| 4190/tcp | ManageSieve | Optional; close it if you do not use Sieve management |
| 8808/tcp | System Control | Administrators only; root-equivalent |

Open the same ports in the provider's firewall/security group. Never expose PostgreSQL `5432`, Redis `6379`, the Go backends `8080`/`8081`, or System Control without an access restriction.

**The most common installation-blocking problem is port 25.** Ask the provider, before you install, to confirm that both **inbound and outbound TCP 25** are allowed. UFW cannot fix an upstream block. If outbound 25 is blocked, configure a smarthost in System Control after installation. If inbound 25 is blocked, direct MX delivery is impossible and you need a different host or a mail relay in front.

### 1.5 Ready to install

- [ ] Debian 12 host with a public IPv4
- [ ] Provider snapshot taken
- [ ] `mail.example.com` A record points at the host
- [ ] `example.com` A record points at the host (for the standard TLS path)
- [ ] Provider confirms port 25 is open in both directions
- [ ] Provider firewall mirrors the port table
- [ ] Reverse DNS points to `mail.example.com`
- [ ] You have a password manager ready for the generated credentials

---

## 2. Install

### 2.1 Upload the release

The preferred method is the prebuilt release bundle from the repository's GitHub Releases page. It contains the Linux binaries, web assets, admin UI, migrations and deployment templates. The installer does not need to build Go on the server.

On your workstation:

```bash
scp alt8mail-0.3.12.tar.gz alt8mail-0.3.12.tar.gz.sha256 root@YOUR_SERVER_IP:/root/
```

On the server:

```bash
cd /root
sha256sum -c alt8mail-0.3.12.tar.gz.sha256
tar -xzf alt8mail-0.3.12.tar.gz
cd alt8mail-0.3.12
```

If you install from a source checkout instead, point the installer at the checkout or run it from the repository root. A source install downloads Go and builds the three Go binaries, so it takes longer and needs access to `go.dev` (or its configured mirror).

The repository is private. Do not clone it over HTTPS with an account password, and do not copy the development working tree to the server. Use one of these two private paths.

#### Option A: the server pulls the tag from GitHub (recommended)

This gives the server a repository-scoped, read-only credential and lets the same key drive future updates. Run this on the server before the installer.

```bash
apt-get update && apt-get install -y git openssh-client
install -d -m 0755 /etc/a8m
ssh-keygen -t ed25519 -N '' -C "alt8mail-$(hostname)" -f /etc/a8m/github_readonly
chmod 0600 /etc/a8m/github_readonly
cat /etc/a8m/github_readonly.pub
```

Add the public key in GitHub under **Repository → Settings → Deploy keys → Add deploy key**. Give it a name such as `alt8mail-server`, and leave **Allow write access** off. The installer will find this key and reuse it for `alt8mail-update`, so you do not need a second deploy key.

Test the key and clone the immutable release tag:

```bash
GIT_SSH_COMMAND='ssh -i /etc/a8m/github_readonly -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new' ssh -T git@github.com
GIT_SSH_COMMAND='ssh -i /etc/a8m/github_readonly -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new' \
  git clone --branch v0.3.12 git@github.com:CryptRoute/Alt8Mail.git /root/Alt8Mail
cd /root/Alt8Mail
git describe --tags
```

GitHub will show a success message instead of a shell, so a nonzero exit from `ssh -T` is normal. If it reports `Permission denied`, the key was not added to this repository, or organization SSO needs the deploy key authorized.

#### Option B: copy a tagged source archive over SSH

If the cloud server must not connect to GitHub, create an archive from the tag on this workstation and copy it directly over SSH. This avoids the private repository entirely for the initial transfer.

```bash
git archive --format=tar.gz --prefix=alt8mail-0.3.12/ -o dist/alt8mail-0.3.12-source.tar.gz v0.3.12
scp -P 22 -i "$HOME/.ssh/alt8mail_server" dist/alt8mail-0.3.12-source.tar.gz root@YOUR_SERVER_IP:/root/
```

On the server:

```bash
cd /root
tar -xzf alt8mail-0.3.12-source.tar.gz
cd alt8mail-0.3.12
```

This archive contains only tracked files from the tag. It does not include ignored local secrets, the `data/` workspace, or uncommitted changes. It is source-only, so the installer downloads Go and builds the binaries. The installer generates `/etc/a8m/github_readonly`; add its `.pub` to GitHub after installation. If the server can never reach GitHub, application updates remain manual: repeat this archive and copy step for each release.

On PowerShell, use `$HOME\.ssh\alt8mail_server` for the key path, or omit `-i` if your default key is configured. Use `ssh -p` and `scp -P` for a nonstandard SSH port.

### 2.2 Run the installer

Run it as root. If you normally use sudo, use `sudo bash install.sh`.

```bash
bash install.sh \
  --domain example.com \
  --hostname mail.example.com \
  --admin-name "Jane Doe" \
  --admin-email owner@example.com \
  --tenant-name "Example Corp" \
  --tenant-slug example \
  --control-email sysadmin@example.com \
  --tls letsencrypt \
  --tls-email admin@example.com
```

| Flag | Meaning |
| --- | --- |
| `--domain` | Bare mail domain used in addresses |
| `--hostname` | MX target and TLS name; defaults to `mail.<domain>` |
| `--admin-email` | First tenant owner; must be an `@<domain>` address |
| `--admin-name` | Display name for the first owner |
| `--tenant-name` | Organization display name |
| `--tenant-slug` | Lowercase tenant identifier |
| `--control-email` | First System Control superadmin; separate from webmail |
| `--tls letsencrypt` | Request a trusted certificate; the default |
| `--tls-email` | Let's Encrypt account email |

Add `--non-interactive` only when every required value is present; it makes the installer fail instead of prompting. Do not use `--skip-packages` on a real host. Do not use `--skip-certbot` or `--tls selfsigned` for production unless you are deliberately working in a lab and will replace the certificate later.

The installer is safe to re-run for repair. It preserves generated secrets and existing accounts, but it reapplies its own nginx, Postfix and UFW configuration. Review custom changes before re-running.

### 2.3 What the installer does

- Installs PostgreSQL, Redis, Dovecot, Postfix, Rspamd, ClamAV, nginx, certbot, fail2ban and supporting tools.
- Sets the hostname, UTC and, when needed, a 2 GiB swap file.
- Creates the `a8m` database and the application, mail and control database roles.
- Applies the migrations and provisions the first tenant, mail domain, owner user and mailbox.
- Installs the Go binaries under `/opt/a8m/current`, with config in `/etc/a8m` and secrets in `/opt/a8m/secrets`.
- Configures Dovecot SQL authentication, Postfix virtual domains, Rspamd DKIM signing and ClamAV scanning.
- Requests a certificate for both names and installs the renewal hook.
- Installs systemd units, starts the services and runs the first DKIM and mailbox provisioning passes.
- Resets UFW, opens the mail/web/control ports and leaves SSH open on the port you connected through.
- Writes `/root/alt8mail-initial-credentials.txt` and `/root/alt8mail-dns-records.txt`, both root-only.
- Creates an updater SSH key at `/etc/a8m/github_readonly`; register its public half as a read-only deploy key for application updates.

It does **not** configure DNS at your registrar, set reverse DNS, open your provider's firewall, harden SSH, create offsite backups, or create your staff accounts.

### 2.4 Verify the installation

The installer ends with `installation complete` and prints the webmail and control URLs. If it reports a failed health check, stop and read the log before changing anything.

```bash
systemctl is-active postgresql redis-server dovecot postfix rspamd clamav-daemon nginx a8m-server a8m-admin alt8mail-control
curl -fsS https://mail.example.com/api/health
curl -fsS http://127.0.0.1:8081/api/health
curl -kfsS https://127.0.0.1:8808/healthz
```

Expected: every service is `active`, the public health endpoint returns JSON with `"ok":true`, and System Control returns a healthy response. The `-k` on the control check is only for the loopback self-check; browse to the control panel by hostname.

If the installer exited at the service-check stage:

```bash
sudo tail -n 100 /var/log/a8m-install.log
systemctl --failed
systemctl status clamav-daemon clamav-freshclam --no-pager
```

ClamAV's first signature download can take several minutes on a new host. Wait for `clamav-daemon` to become active, then re-run the installer.

### 2.5 Record the credentials

```bash
sudo less /root/alt8mail-initial-credentials.txt
```

Copy the values into your password manager over an SSH session you control. Do not paste them into chat, email, tickets or screenshots. The file contains:

- The owner web password (webmail and customer admin).
- The owner mailbox password (IMAP/POP/SMTP before the first web password change).
- The System Control email and password.
- Paths to the secrets directory and database roles.

The file is written only when it does not already exist, so a repair run does not overwrite your copy. `/opt/a8m/secrets` is the authoritative secret store; back it up separately.

### 2.6 Register the update key

Application updates fetch release tags over SSH. Register the installer's public key as a **read-only deploy key** on the repository:

```bash
sudo cat /etc/a8m/github_readonly.pub
```

Add it in GitHub under **Repository → Settings → Deploy keys → Add deploy key**, with write access **off**. If you installed from your own fork or mirror, edit `/etc/a8m/update.conf` and set `A8M_REPO` to that repository before relying on updates.

---

## 3. First sign-in

### 3.1 Webmail as the owner

Open `https://mail.example.com/` and sign in with the owner email and owner web password.

The installer-created owner account has a generated password and is not forced through the onboarding password-change screen. Protect it as a break-glass credential:

1. Open **Settings → Account** and set the display name.
2. Open **Settings → Appearance** and **Settings → Regional** and set the theme, time zone and formats.
3. Open **Settings → Security** and enroll an authenticator or passkey. Save the recovery codes offline.
4. Store the generated owner password in your vault.
5. Create a named admin account for daily administration instead of sharing the owner.

To rotate the owner's own password, use **Customer admin → Users → [owner] → Reset password**. The reset signs out existing sessions. Sign back in with the temporary password, then complete the forced change at the next webmail sign-in. The new web password also rotates the Dovecot mailbox password, so mail clients use the new credential afterward.

### 3.2 Customer admin

Open `https://mail.example.com/admin/` and sign in with the same owner identity and password. This is a separate session and cookie from webmail.

The installer owner has system-administrator entitlement, so the console can switch tenants. For day-to-day work, create a named administrator with only the permissions they need:

- **Owner / admin** — full tenant administration.
- **Custom role** — a tenant-scoped set of permissions.
- **Member / readonly** — no administrative access unless a custom role is assigned.

### 3.3 System Control

Open `https://mail.example.com:8808/` and sign in with the control email and password from the credentials file. Use the hostname, not the bare IP; the certificate is issued for the mail hostname, so the IP will produce a browser warning.

On first sign-in:

1. Change the control password.
2. Enable TOTP and store the recovery codes offline.
3. Create a named operator account instead of sharing the superadmin.
4. Restrict port 8808 to your administration network.
5. Review **Server & network** and **TLS** settings before making changes.

System Control can start and stop services, edit Postfix configuration, manage certificates, read logs, run a terminal and restore backups. Treat a superadmin session as equivalent to root SSH access.

If the browser shows a certificate warning, do not click through on a production host. Check DNS, the certificate issuer and the Let's Encrypt log first.

---

## 4. Secure the deployment

Do these before inviting users.

### 4.1 Put credentials and MFA in order

- Store the initial credentials file in a password manager, not in a shared drive.
- Enable TOTP for the owner, every customer admin and every System Control operator.
- Save recovery codes somewhere offline and separate from the authenticator device.
- In **Customer admin → Policies → Security**, turn on MFA for administrators first, then for all users once your pilot users have enrolled.
- Set an admin idle timeout and session lifetime that match your organization's policy.

Do not turn on a restrictive IP allowlist until you have confirmed your own egress address and tested a second admin account. A bad allowlist can lock out the console.

### 4.2 Harden SSH

The installer opens your current SSH port; it does not change `sshd_config`. Before locking down SSH, add a key you have tested:

```bash
# On your workstation
ssh-keygen -t ed25519 -C "you@workstation"
ssh-copy-id -i ~/.ssh/id_ed25519.pub root@YOUR_SERVER_IP

# Confirm the key works in a second terminal, then apply the shipped policy
ssh root@YOUR_SERVER_IP '
  install -m 0644 /opt/a8m/current/deploy/ssh/99-alt8mail-hardening.conf /etc/ssh/sshd_config.d/ &&
  sshd -t &&
  systemctl reload ssh
'
```

The drop-in disables external password and keyboard-interactive authentication, keeps key-based root access for break-glass use, and allows password authentication only on loopback so the System Control terminal can work. Keep an existing root session open until you have verified a new key-based session. If you use a non-root sudo account for daily work, create it and test it before disabling root key access.

### 4.3 Restrict System Control

The installer opens `8808/tcp` to every source. Restrict it to your administration network at both the host firewall and the provider firewall:

```bash
sudo ufw delete allow 8808/tcp
sudo ufw allow from YOUR_ADMIN_IP to any port 8808 proto tcp
sudo ufw status verbose
```

Use a single trusted IP or a VPN egress range. Keep SSH access open while you test. If you lose access to System Control, SSH remains the recovery path.

### 4.4 Treat the customer admin as an administrative surface

`/admin/` is served on the same public hostname as webmail. Do not leave it publicly reachable without MFA and a network policy. Choose one of these:

- Put `/admin/` behind your VPN or zero-trust proxy.
- Restrict access with an nginx allowlist at the edge.
- Set a tested admin IP policy in **Policies → Security**.
- Publish the admin console on a separate `admin.example.com` hostname with its own certificate and network controls.

The project's own guidance is explicit: do not expose `/admin/` publicly on a production mail host without admin policy and network controls.

### 4.5 Mirror the firewall at the provider

UFW is the host firewall, not the cloud firewall. Make the provider security group match the port table: public 25/80/443/587/993/995, optional 4190, restricted 22 and 8808, and nothing else.

### 4.6 Keep the service account and secrets private

- `/opt/a8m/secrets` is mode 0700 and owned by the `a8m` service account. Do not loosen it.
- `/etc/a8m/app.env` and `/etc/a8m/control.env` contain database and signing secrets. They must stay root-owned and mode 0640/0600.
- Do not run the applications as root. Only System Control runs as root because it manages host services; that is why its port must be restricted.
- Do not expose PostgreSQL, Redis, the Go backends or the mail spool directly.

---

## 5. Make mail flow

### 5.1 Add the mail DNS records

Use the live domain drawer in **Customer admin → Domains** as the source of truth. `/root/alt8mail-dns-records.txt` contains the initial set, but the admin drawer also shows the ownership record and any later changes.

| Type | Name | Value | Notes |
| --- | --- | --- | --- |
| MX | `example.com` | `10 mail.example.com.` | One MX is enough for a single host |
| TXT | `example.com` | `v=spf1 mx -all` | Exactly one SPF record at the name |
| TXT | `a8m._domainkey.example.com` | `v=DKIM1; k=rsa; p=...` | Copy the full value from the admin drawer |
| TXT | `_dmarc.example.com` | `v=DMARC1; p=none; rua=mailto:dmarc@example.com` | Start at `p=none`, then tighten |
| TXT | `_a8m-verification.example.com` | `a8m-verify=<token>` | Proves ownership; add it for the primary domain too |

The DKIM key is generated after the domain exists. If the admin drawer still says **Provisioning**, give the timer a minute or run:

```bash
sudo systemctl start a8m-dkimprovision.service
sudo systemctl status a8m-dkimprovision.service --no-pager
```

The timer runs every minute and retries failures. After the key appears, copy the record exactly; a truncated or reformatted TXT value will fail verification.

Optional later: MTA-STS. Enable it only after basic mail and HTTPS work. It needs an `_mta-sts` TXT record and an HTTPS policy served for `mta-sts.<domain>` with a trusted certificate. The admin drawer shows the exact expected values.

### 5.2 Verify DNS and TLS

```bash
dig +short MX example.com
dig +short TXT example.com
dig +short TXT a8m._domainkey.example.com
dig +short TXT _dmarc.example.com
dig +short TXT _a8m-verification.example.com
```

Then open the domain in the customer admin and choose **Run DNS verification**. The goal is green checks for ownership, MX, SPF, DKIM and DMARC. MTA-STS can remain disabled until you are ready to operate it.

Check the certificate:

```bash
curl -I https://mail.example.com/
echo | openssl s_client -connect mail.example.com:443 -servername mail.example.com 2>/dev/null \
  | openssl x509 -noout -issuer -subject -dates
```

The issuer should be a public CA, the subject should cover `mail.example.com`, and the expiry should be in the future. Let's Encrypt renewal is handled by `certbot.timer`; keep port 80 open for the webroot challenge. You can inspect and dry-run renewal in **System Control → TLS**.

### 5.3 Test mail from outside

Do not test only from the same host; loopback delivery can succeed while public DNS or port 25 is broken.

1. Send a message from an external mailbox (Gmail, Outlook, another provider) to `owner@example.com`.
2. Confirm it arrives in webmail.
3. Reply from webmail to the external address.
4. In the received message's original headers, confirm `spf=pass`, `dkim=pass` and `dmarc=pass` for your domain.
5. Check the queue and logs if anything is delayed.

```bash
mailq
sudo journalctl -u postfix -u dovecot -u rspamd -n 100 --no-pager
sudo tail -n 100 /var/log/mail.log
```

If outbound mail is queued with connection timeouts, port 25 outbound is the first suspect. If inbound mail never appears in the logs, MX, inbound port 25 or provider filtering is the first suspect.

### 5.4 Mail client settings

| Setting | Value |
| --- | --- |
| IMAP server | `mail.example.com` |
| IMAP port | `993`, SSL/TLS |
| SMTP submission | `mail.example.com` |
| SMTP port | `587`, STARTTLS, authentication required |
| POP3S (optional) | `mail.example.com`, port `995`, SSL/TLS |
| ManageSieve (optional) | `mail.example.com`, port `4190`, STARTTLS |
| Username | Full email address |
| Password | Mailbox password; after a self-service web password change, the new web password |

Ports 143, 110 and 465 are not part of the default installation. Do not configure clients to use them unless you deliberately add those listeners.

App passwords are for CalDAV/CardDAV clients, not IMAP/SMTP. IMAP and SMTP use the Dovecot mailbox password; the web password change flow keeps that mailbox password in sync.

---

## 6. Add users and set policy

### 6.1 Create a named administrator

In **Customer admin → Users → New user**, create an administrator with a real name and address. Give the account only the role it needs. Admin-created users go through the guided onboarding flow on first sign-in: display name, time zone, language, theme and password change. Enroll MFA before adding more administrators.

### 6.2 Import users from CSV or XLSX

Use **Customer admin → Users → Import users**. Full name and email are required.

```csv
name,email,role,quota_gb,notify_email
Jane Doe,jane@example.com,member,16,jane.doe@example.net
Sam Rivera,sam@example.com,admin,16,
```

Accepted columns:

| Column | Required | Notes |
| --- | --- | --- |
| `name` or `full_name` | Yes | Display name |
| `email` | Yes | Must use a verified domain |
| `role` | No | `owner`, `admin`, `member` or `readonly`; unknown values become `member` |
| `quota_gb` | No | Defaults to 16 GB |
| `notify_email` | No | Alternate address for the branded credentials notification |

Validate the preview before starting the job. The generated web and mailbox passwords are shown only in the result view, so download the results CSV and deliver credentials through a secure channel. The optional notification email only works after outbound mail is healthy.

### 6.3 Onboarding expectations

- New users are required to complete onboarding before they can use the mailbox.
- They set their display name, time zone, language, theme and a new password.
- The new web password is synchronized to Dovecot, so the same credential works for webmail and mail clients.
- MFA enrollment can be encouraged during onboarding and required by policy afterward.
- Recovery codes are shown once; users must store them.

### 6.4 Add the organization's structure

- **Aliases** — add send-as or receive-only aliases from the user detail page.
- **Groups** — create distribution or security groups; Postfix expands active group members at delivery.
- **Org units** — model the reporting structure for administration and directory views.
- **Delegates** — grant another user access to a mailbox from the user detail page.
- **Vacation / automatic replies** — configure per user with internal and external copy.
- **Mailbox migration** — use **Mailboxes → Import/migrate** to connect to an old IMAP or POP3 server, validate credentials, map source mailboxes to existing or new users, and run a resumable background migration. Do this after basic mail flow works, not before.

### 6.5 Policy checklist

Start with a small set and record every change:

- **Security:** MFA for admins, then all users; password length and history; session limits; admin IP ranges only after testing.
- **Mail:** IMAP/POP access, SMTP access, forwarding, retention, attachment limits, Junk release policy.
- **Sharing and directory:** external sharing, directory visibility, profile editing.
- **Branding:** organization name, logo and colors.
- **AI assistant:** optional. Configure an OpenAI-compatible endpoint or Anthropic, save, and use **Save & test**. The API key is sealed and never returned to the browser; keep `policies.manage` restricted to trusted administrators.

The default Junk behavior lets users release their own Junk. If you want administrator-only release, turn on **Restrict Junk release to administrators**; users then see the administrator-release state and an authorized administrator handles the quarantine queue.

### 6.6 Go-live gate for people

Do not invite the whole company until one pilot user has completed onboarding, signed in with a mail client, sent to an external address and received a reply with SPF, DKIM and DMARC passing.

---

## 7. Operate and recover

### 7.1 Application updates

Application updates are tag-based and transactional:

```bash
sudo alt8mail-update --check
sudo alt8mail-update
```

The updater fetches the latest stable tag from the configured repository, builds it, backs up the database and configuration under `/var/backups/alt8mail/update-<timestamp>`, applies migrations, switches the release symlink and checks all three services. If readiness fails, it restores the previous release and database.

Logs and rollback evidence:

```bash
sudo tail -n 100 /var/log/alt8mail-update.log
ls -lt /var/backups/alt8mail | head
cat /opt/a8m/current/VERSION
```

Run updates in a maintenance window. Mail queues, not messages, are affected during the short service restart. Do not edit files under `/opt/a8m/current`; it is a release symlink and will be replaced.

Operating-system packages are separate: use **System Control → Infrastructure → Software updates** for apt packages and reboot status.

### 7.2 Backups

There are two layers to protect:

1. **System Control → Infrastructure → Backups** creates a `.tar.gz` archive of selectable contents: PostgreSQL, Postfix, Dovecot, DKIM, Let's Encrypt and Maildirs. The default selection does **not** include Maildirs; select **Maildir message stores** when you need a full mail backup.
2. The critical configuration and secrets archive, which the control-plane backup does not include: `/opt/a8m/secrets`, `/etc/a8m`, nginx, Redis configuration and SSL material.

Create the first backup before you add users, download it over SSH, and store it off the server in an encrypted vault. The panel archive is a plain `.tar.gz`; encrypt it before storing it anywhere you do not fully control.

A minimal critical-config archive:

```bash
sudo tar -C / -czf /root/alt8mail-critical-$(date -u +%F).tar.gz \
  opt/a8m/secrets \
  etc/a8m \
  etc/nginx \
  etc/redis/a8m.conf \
  etc/ssl/alt8mail
sudo chmod 600 /root/alt8mail-critical-$(date -u +%F).tar.gz
```

Also copy the archive off-host:

```bash
scp root@YOUR_SERVER_IP:/root/alt8mail-critical-*.tar.gz ./
```

Test a restore on a separate Debian host before you need one. A backup that has never been restored is a hope, not a recovery plan. At minimum, verify that the database dump restores, the Maildir tree is readable, and the restored host can start the three services with the same secrets.

### 7.3 Monitor the things that fail first

- **System Control dashboard** — service health, CPU/memory/disk, mail volume, queue, certificate expiry and update state.
- **Notifications** — configure at least one handle (email, Slack, Discord, Telegram or webhook) and send a test.
- **Queue** — investigate deferred mail before it becomes a delivery incident.
- **Disk** — Maildirs and backup archives grow. Watch `/var/vmail` and `/var/backups/alt8mail`.
- **TLS** — keep port 80 open and check the certificate inventory and renewal state.
- **Logs** — Postfix, Dovecot, Rspamd and the three Alt8Mail services are in journald and `/var/log/mail.log`.

Useful health checks:

```bash
systemctl is-active postgresql redis-server dovecot postfix rspamd clamav-daemon nginx a8m-server a8m-admin alt8mail-control
curl -fsS https://mail.example.com/api/health
curl -fsS http://127.0.0.1:8081/api/health
curl -kfsS https://127.0.0.1:8808/healthz
mailq
df -h /var/vmail /var/backups/alt8mail
```

Set disk and certificate alerts before the host is full or the certificate is close to expiry.

### 7.4 TLS renewal

When installed with Let's Encrypt, `certbot.timer` is enabled and a deploy hook copies the renewed certificate to nginx, Postfix, Dovecot and System Control and reloads the affected services. Keep port 80 reachable. Verify:

```bash
systemctl list-timers 'certbot*' --no-pager
sudo certbot renew --dry-run
```

The dry run should complete without changing the live certificate. If you replace certificates with a CA-issued pair, use **System Control → TLS** to import and assign them rather than editing certificate paths by hand.

### 7.5 Disaster recovery outline

Rehearse this on a scratch host:

1. Build a new Debian 12 host with the same hostname and domain plan.
2. Install the same Alt8Mail release.
3. Stop the application and mail services.
4. Restore the PostgreSQL dump, `/var/vmail`, the mail configuration and `/opt/a8m/secrets`.
5. Restore `/etc/a8m`, TLS material, nginx and systemd units.
6. Start the stack and verify health, login, inbound mail, outbound mail and DKIM.
7. Repoint DNS and reverse DNS only after the restored host is healthy.

Do not make the first restore attempt during an outage.

---

## 8. Quick reference

### URLs

| Plane | URL |
| --- | --- |
| Webmail | `https://mail.example.com/` |
| Customer admin | `https://mail.example.com/admin/` |
| System Control | `https://mail.example.com:8808/` |

### Paths

| Path | Contents |
| --- | --- |
| `/opt/a8m/current` | Active release symlink |
| `/opt/a8m/secrets` | Root-only generated secrets |
| `/etc/a8m/app.env` | Mail application configuration |
| `/etc/a8m/control.env` | System Control configuration |
| `/var/vmail` | Maildir message stores |
| `/var/backups/alt8mail` | Control-plane archives and updater snapshots |
| `/var/log/a8m-install.log` | Installer log |
| `/var/log/alt8mail-update.log` | Updater log |
| `/var/log/mail.log` | Postfix, Dovecot and Rspamd mail events |

### Services

`postgresql`, `redis-server`, `dovecot`, `postfix`, `rspamd`, `clamav-daemon`, `nginx`, `a8m-server`, `a8m-admin`, `alt8mail-control`.

The background provisioners are `a8m-mailprovision.timer` and `a8m-dkimprovision.timer`.

### Credential map

| Credential | Used for |
| --- | --- |
| Owner web password | Webmail and customer admin |
| Owner mailbox password | IMAP/POP/SMTP until the first web password change; then the web password is synced |
| Control password | System Control only |

### Important defaults

- Database name: `a8m`
- Go API: `127.0.0.1:8080`
- Customer admin API: `127.0.0.1:8081`
- System Control: `0.0.0.0:8808`
- Mail domain storage: `/var/vmail/<domain>/<local-part>/Maildir`
- Host time zone: UTC
- DKIM selector: `a8m`

---

## 9. Troubleshooting

### Let's Encrypt did not issue a certificate

Check both A records, the proxy status for `mail`, port 80 and CAA. Inspect `/var/log/letsencrypt/letsencrypt.log`. If the host fell back to self-signed, fix DNS and port 80, then re-run the installer with `--tls letsencrypt` or issue a certificate through **System Control → TLS**.

### The installer stopped at a health check

Read `/var/log/a8m-install.log` and run `systemctl --failed`. The usual cause on a fresh host is ClamAV still loading its first signature database. Wait for `clamav-daemon` to become active, then re-run the installer. Do not delete the release or the secrets directory to "start over".

### Webmail loads but the owner cannot sign in

Use the exact owner email and the web password, not the mailbox password. Check `systemctl status a8m-server` and `journalctl -u a8m-server -n 100`. Confirm the account is active in the customer admin or in the database. If the owner password is lost, the control-plane reset flow is the supported recovery path.

### `/admin/` returns 404 or cannot be reached

Include the trailing slash: `https://mail.example.com/admin/`. Check `systemctl status a8m-admin`, `curl http://127.0.0.1:8081/api/health`, and the nginx configuration. The admin UI is served by the Go admin binary, not as a static file.

### System Control will not open

Use `https://mail.example.com:8808/`, not port 88008 and not the plain `https` port. Check the provider firewall, `sudo ufw status`, and:

```bash
curl -kfsS https://127.0.0.1:8808/healthz
systemctl status alt8mail-control --no-pager
```

A warning on the IP address is expected if the certificate is issued only for the hostname. Restrict 8808 rather than disabling TLS verification.

### Domain verification fails

Open the domain drawer and read the per-check details. Common causes: the ownership TXT is missing or has the wrong token, the MX does not point to the mail hostname, there are multiple SPF records, the DKIM `p=` value was truncated, or the DMARC value uses an invalid policy. Wait for propagation and run verification again. The initial domain can be created as `manual` by the installer; adding the real records is still required for reliable mail.

### Inbound mail does not arrive

Check MX, inbound port 25 at the provider, `journalctl -u postfix`, `/var/log/mail.log` and `mailq`. A missing MX or a provider-level port-25 block is more likely than a Dovecot problem on a new installation.

### Outbound mail stays queued

Check outbound port 25 first. Then check PTR, SPF, DKIM and DMARC, and inspect the queue reason with `mailq`. A provider that blocks outbound 25 can be handled with a smarthost in **System Control → Delivery**; a provider that blocks inbound 25 cannot receive direct MX mail and needs a different host or a front relay.

### A mail client rejects the password

Use the full email address as the username and the current mailbox password. Use IMAPS 993 and submission 587 with STARTTLS. Confirm IMAP/POP access is enabled in policy. After a web password change, use the new password; the mailbox hash is synchronized. App passwords are for CalDAV/CardDAV, not IMAP/SMTP.

### An application update fails

Confirm `/etc/a8m/github_readonly.pub` is registered as a read-only deploy key on the configured repository, then read `/var/log/alt8mail-update.log`. The updater restores the previous release and database when readiness fails; the backup path is printed in the failure message and in the log.

---

## 10. Go-live checklist

### Access and identity

- [ ] Owner credentials stored in a password manager
- [ ] Owner TOTP enrolled and recovery codes stored offline
- [ ] Named customer administrator created
- [ ] Named System Control operator created
- [ ] MFA required for administrators
- [ ] SSH key login tested; password login disabled externally
- [ ] Port 8808 restricted to the administration network
- [ ] Provider firewall matches the host firewall
- [ ] `/admin/` protected by network policy, VPN or an admin IP policy

### DNS and TLS

- [ ] `mail.example.com` A record points to the host
- [ ] Bare-domain A record points to the host, or a dedicated mail domain is used
- [ ] Reverse DNS points to `mail.example.com`
- [ ] MX, SPF, DKIM, DMARC and ownership TXT records are published
- [ ] Domain verification is green in the customer admin
- [ ] Certificate is publicly trusted and covers the mail hostname
- [ ] Certbot renewal dry run passes
- [ ] Port 80 remains open for ACME renewals

### Mail flow

- [ ] External inbound message arrives
- [ ] Outbound reply is accepted by an external provider
- [ ] SPF, DKIM and DMARC pass on the delivered message
- [ ] Queue is empty after the test
- [ ] Mail client works over IMAPS 993 and submission 587
- [ ] Port 25 is confirmed open in both directions at the provider

### People

- [ ] A pilot user completes onboarding
- [ ] Pilot user sends and receives external mail
- [ ] User import template is prepared with the correct columns
- [ ] Credential delivery process is defined
- [ ] Support contact and recovery process communicated

### Recovery and operations

- [ ] First full backup created, including Maildirs
- [ ] Critical secrets/configuration archive created
- [ ] Both archives downloaded off-host and encrypted
- [ ] Restore tested on a separate host
- [ ] System Control notification handle configured and tested
- [ ] Disk, certificate and queue alerts enabled
- [ ] Update deploy key registered; `sudo alt8mail-update --check` works
- [ ] Runbook owner and maintenance window agreed
