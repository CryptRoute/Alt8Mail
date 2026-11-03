# Threat intelligence sharing with Rspamd

Alt8Mail can feed confirmed security reports into Rspamd through Redis-backed
multimaps. A confirmed report contributes:

- the sender address;
- the sender domain;
- normalized URL hostnames extracted from the reported message body.

Full URLs, subjects, snippets and message content are never published. Pending
reports continue to block at the Postfix policy layer only.

## Enablement

Open **System Control → Security reports**. The **Threat intelligence** card
contains the setting:

> Share confirmed threat indicators with Rspamd

It is **off by default**. While off, normal review actions perform no Redis
writes. Turning it off clears the three Redis hashes.

When enabled, the control plane rebuilds the complete active set from every
confirmed report after each review, undo or reopen. A five-minute reconciler
repairs drift, including Redis data loss or a confirmed report being reset to
pending by a new user report.

The control plane connects to Redis at `CONTROL_REDIS_ADDR`
(default `127.0.0.1:6379`) using the password in
`CONTROL_REDIS_PASSWORD_FILE`.

## Rspamd rules

The rules live in `deploy/rspamd/multimap.conf` and are installed as
`/etc/rspamd/local.d/multimap.conf`:

| Symbol | Match | Redis hash | Score |
| --- | --- | --- | --- |
| `A8M_REPORTED_SENDER` | Full sender address | `a8m_reported_senders` | 15 |
| `A8M_REPORTED_SENDER_DOMAIN` | Sender domain | `a8m_reported_domains` | 8 |
| `A8M_REPORTED_PHISHING_URL` | URL hostname | `a8m_reported_urls` | 8 |

Rspamd reads the hashes directly. Adding or removing indicators requires no
file edit and no Rspamd restart. Rspamd refreshes maps on its normal cache
interval; structural changes to `multimap.conf` do require a reload.

## URL-host guardrail

Shorteners, cloud storage/CDN hosts and email-service redirectors are marked as
shared destinations. They are shown to the reviewer but are **not** approved by
default. The reviewer can explicitly check one before confirming the threat.
This prevents a single report from blocking a host used by many unrelated
senders.

Only normalized hostnames are stored in
`public.security_report_url_hosts`; the full URL is discarded. Private,
single-label and IP-literal URLs are not published.

## Rebuild semantics

The control plane never deletes a single indicator from Redis. It reads every
report with `status='confirmed'`, builds the complete sender/domain/host sets,
then atomically replaces all three hashes with a Redis transaction pipeline.
Dismissing or undoing one report therefore cannot remove an indicator that
another confirmed report still contributes.

## Inspecting Redis

Run on the mail host. The password is read from the root-only secret file and is
not printed:

```bash
sudo REDISCLI_AUTH="$(cat /opt/a8m/secrets/redis_password)" \
  redis-cli HGETALL a8m_reported_senders
sudo REDISCLI_AUTH="$(cat /opt/a8m/secrets/redis_password)" \
  redis-cli HGETALL a8m_reported_domains
sudo REDISCLI_AUTH="$(cat /opt/a8m/secrets/redis_password)" \
  redis-cli HGETALL a8m_reported_urls
```

Each field is an active indicator; the value is `1`.

Useful checks:

```bash
rspamadm configtest
sudo REDISCLI_AUTH="$(cat /opt/a8m/secrets/redis_password)" \
  redis-cli HLEN a8m_reported_urls
journalctl -u alt8mail-control -n 100 --no-pager
journalctl -u rspamd -n 100 --no-pager
```

## API

- `GET /control/api/security/threat-intel` — setting state and active counts.
- `PUT /control/api/security/threat-intel` — `{ "enabled": true|false }`.
- `GET /control/api/security/reports/{id}` — includes
  `report.urlHosts: [{ host, shared, approved }]`.
- `PATCH /control/api/security/reports/{id}` — accepts `urlHosts` when
  confirming, containing the hosts the reviewer approved.

## Out of scope

Bayes learning, Rspamd fuzzy hashing and rules based on subjects or snippets are
not part of this phase.
