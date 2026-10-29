#!/usr/bin/env python3
"""End-to-end staging checks for password, mail policy, sharing and retention.

Creates an isolated test user, exercises the real Go API and Dovecot/Postfix
paths, and removes the test account. Requires the staging admin password file.
"""
import base64
import datetime
import http.cookiejar
import imaplib
import json
import os
import poplib
import smtplib
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

ADMIN_BASE = os.environ.get("ALT8MAIL_ADMIN_API", "https://130-94-46-22.sslip.io/admin/api")
USER_BASE = os.environ.get("ALT8MAIL_USER_API", "https://130-94-46-22.sslip.io/system/api")
MAIL_HOST = os.environ.get("ALT8MAIL_MAIL_HOST", "130.94.46.22")
SMTP_HOST = os.environ.get("ALT8MAIL_SMTP_HOST", MAIL_HOST)
SMTP_PORT = int(os.environ.get("ALT8MAIL_SMTP_PORT", "587"))
ADMIN_EMAIL = os.environ.get("ALT8MAIL_ADMIN_EMAIL", "avery.chen@a8m.test")
ADMIN_PASSWORD_FILE = os.environ.get("ALT8MAIL_ADMIN_PASSWORD_FILE", "staging_avery_password")
ADMIN_PASSWORD = os.environ.get("ALT8MAIL_ADMIN_PASSWORD", "")
if not ADMIN_PASSWORD and ADMIN_PASSWORD_FILE:
    with open(ADMIN_PASSWORD_FILE, "r", encoding="utf-8") as handle:
        ADMIN_PASSWORD = handle.read().strip()
if not ADMIN_PASSWORD:
    raise SystemExit("admin password is required")

SUFFIX = str(int(time.time()))
TEST_EMAIL = f"policy-test-{SUFFIX}@a8m.test"
TEST_NAME = "Policy Test"
TEMP_TO_NEW = "Policy-Test-2026!A"
NEW_TO_THIRD = "Policy-Test-2026!B"
failures = []


def check(condition, message, detail=None):
    if condition:
        print("  PASS ", message)
    else:
        print("  FAIL ", message, "" if detail is None else detail)
        failures.append(message)


def redact(payload):
    if isinstance(payload, dict):
        return {key: ("<redacted>" if "password" in key.lower() else redact(value)) for key, value in payload.items()}
    if isinstance(payload, list):
        return [redact(value) for value in payload]
    return payload


class Client:
    def __init__(self, base):
        self.base = base
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))
        self.csrf = ""

    def request(self, method, path, body=None):
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(self.base + path, data=data, method=method)
        request.add_header("Accept", "application/json")
        if body is not None:
            request.add_header("Content-Type", "application/json")
        csrf = self.csrf
        if not csrf:
            for cookie in self.jar:
                if cookie.name in ("a8m_admin_csrf", "a8m_system_csrf"):
                    csrf = cookie.value
        if csrf:
            request.add_header("X-CSRF-Token", csrf)
        try:
            with self.opener.open(request, timeout=60) as response:
                return response.status, json.loads(response.read() or b"{}")
        except urllib.error.HTTPError as error:
            return error.code, json.loads(error.read() or b"{}")

    def ok(self, method, path, body=None):
        status, payload = self.request(method, path, body)
        if status >= 400:
            raise RuntimeError(f"{method} {path} -> {status}: {redact(payload)}")
        return payload


def login(client, email, password):
    status, payload = client.request("POST", "/session", {"email": email, "password": password})
    if status == 200:
        client.csrf = payload.get("csrf", "")
    return status, payload


def imap_login(email, password):
    context = ssl._create_unverified_context()
    conn = imaplib.IMAP4_SSL(MAIL_HOST, 993, ssl_context=context)
    conn.login(email, password)
    conn.select("INBOX")
    conn.logout()


def pop_login(email, password):
    context = ssl._create_unverified_context()
    conn = poplib.POP3_SSL(MAIL_HOST, 995, context=context)
    conn.user(email)
    conn.pass_(password)
    conn.quit()


def smtp_login(email, password):
    context = ssl._create_unverified_context()
    conn = smtplib.SMTP(SMTP_HOST, SMTP_PORT, timeout=20)
    conn.ehlo()
    conn.starttls(context=context)
    conn.ehlo()
    conn.login(email, password)
    conn.quit()


def auth_fails(function, email, password):
    try:
        function(email, password)
    except Exception:  # noqa: BLE001 - protocol libraries raise different classes
        return True
    return False


def wait_for(predicate, timeout, interval=2):
    deadline = time.time() + timeout
    while time.time() < deadline:
        result = predicate()
        if result:
            return result
        time.sleep(interval)
    return None


admin = Client(ADMIN_BASE)
status, payload = login(admin, ADMIN_EMAIL, ADMIN_PASSWORD)
if status != 200 or not payload.get("ok"):
    raise SystemExit(f"admin login failed: {status} {redact(payload)}")

original_policy = admin.ok("GET", "/policies")["policy"]
policy = json.loads(json.dumps(original_policy))
user = None
user_id = ""
try:
    print("\n[1] Create isolated mailbox user")
    policy["security"]["mfaEnforcement"] = "off"
    admin.ok("PATCH", "/policies", policy)
    created = admin.ok("POST", "/users", {
        "name": TEST_NAME, "email": TEST_EMAIL, "role": "member", "status": "active",
    })
    user_id = created["user"]["id"]
    temporary_password = created["temporaryPassword"]
    check(bool(temporary_password), "temporary password issued")
    check(bool(created.get("mailPassword")), "separate mailbox password issued")

    print("\n[2] Explicit Maildir provisioning")
    provisioned = wait_for(
        lambda: (admin.ok("GET", f"/users/{user_id}").get("user", {}) or {}).get("mailboxStatus") == "ready",
        120, 5,
    )
    detail = admin.ok("GET", f"/users/{user_id}").get("user", {})
    check(provisioned is True, "mailbox provisioned by the root worker", detail.get("mailboxStatus"))
    check(bool(detail.get("maildirPath", "").endswith("/Maildir")), "admin exposes the provisioned Maildir path", detail.get("maildirPath"))

    print("\n[3] Forced password change and password history")
    member = Client(USER_BASE)
    status, payload = login(member, TEST_EMAIL, temporary_password)
    check(status == 200 and payload.get("user", {}).get("passwordChangeRequired") is True, "temporary password forces a change", (status, payload))
    reused = member.request("POST", "/password/change", {"currentPassword": temporary_password, "newPassword": temporary_password})
    check(reused[0] == 400, "current password cannot be reused", reused)
    changed = member.request("POST", "/password/change", {"currentPassword": temporary_password, "newPassword": TEMP_TO_NEW})
    check(changed[0] == 200 and changed[1].get("passwordChangeRequired") is False, "password change succeeds", changed)
    old_login = Client(USER_BASE)
    status, _ = login(old_login, TEST_EMAIL, temporary_password)
    check(status == 401, "old password no longer signs in", status)
    status, _ = login(member, TEST_EMAIL, TEMP_TO_NEW)
    check(status == 200, "new password signs in", status)
    history = member.request("POST", "/password/change", {"currentPassword": TEMP_TO_NEW, "newPassword": temporary_password})
    check(history[0] == 400 and history[1].get("error", {}).get("code") == "PASSWORD_HISTORY", "password history blocks reuse", history)
    check(imap_login(TEST_EMAIL, TEMP_TO_NEW) is None, "new password authenticates to IMAP")

    print("\n[4] IMAP / POP3 / SMTP policy switches")
    policy["mail"]["imapAccess"] = False
    admin.ok("PATCH", "/policies", policy)
    check(auth_fails(imap_login, TEST_EMAIL, TEMP_TO_NEW), "IMAP login blocked when disabled")
    policy["mail"]["imapAccess"] = True
    policy["mail"]["popAccess"] = True
    admin.ok("PATCH", "/policies", policy)
    check(pop_login(TEST_EMAIL, TEMP_TO_NEW) is None, "POP3 login allowed when enabled")
    policy["mail"]["popAccess"] = False
    admin.ok("PATCH", "/policies", policy)
    check(auth_fails(pop_login, TEST_EMAIL, TEMP_TO_NEW), "POP3 login blocked when disabled")
    check(smtp_login(TEST_EMAIL, TEMP_TO_NEW) is None, "SMTP submission allowed when enabled")
    policy["mail"]["smtpAccess"] = False
    admin.ok("PATCH", "/policies", policy)
    check(auth_fails(smtp_login, TEST_EMAIL, TEMP_TO_NEW), "SMTP submission blocked when disabled")
    policy["mail"]["smtpAccess"] = True
    admin.ok("PATCH", "/policies", policy)

    print("\n[5] Forwarding policy")
    policy["mail"]["autoForwarding"] = False
    policy["mail"]["externalForwarding"] = True
    admin.ok("PATCH", "/policies", policy)
    sieve = member.ok("GET", "/sieve")
    rule_body = {
        "rules": [{
            "name": "Policy forward", "enabled": True, "match": "all",
            "conditions": [{"field": "subject", "operator": "contains", "value": "policy"}],
            "action": {"type": "redirect", "value": "avery.chen@a8m.test"},
        }],
        "vacation": sieve["vacation"],
    }
    status, payload = member.request("PUT", "/sieve", rule_body)
    check(status == 403 and payload.get("error", {}).get("code") == "FORWARDING_DISABLED", "internal forwarding blocked", (status, payload))
    rule_body["rules"][0]["action"]["value"] = "outside@example.test"
    policy["mail"]["autoForwarding"] = True
    policy["mail"]["externalForwarding"] = False
    admin.ok("PATCH", "/policies", policy)
    status, payload = member.request("PUT", "/sieve", rule_body)
    check(status == 403 and payload.get("error", {}).get("code") == "FORWARDING_DISABLED", "external forwarding blocked", (status, payload))
    policy["mail"]["externalForwarding"] = True
    admin.ok("PATCH", "/policies", policy)
    status, payload = member.request("PUT", "/sieve", rule_body)
    check(status == 200, "forwarding allowed when policy permits", (status, payload))
    policy["mail"]["autoForwarding"] = False
    policy["mail"]["externalForwarding"] = False
    admin.ok("PATCH", "/policies", policy)
    filtered = wait_for(
        lambda: "redirect" not in (member.ok("GET", "/sieve").get("script") or ""),
        20, 2,
    )
    check(filtered is True, "existing generated script is filtered after a policy change")
    stored = member.ok("GET", "/sieve")
    check(any(rule.get("action", {}).get("type") == "redirect" for rule in stored.get("rules", [])), "stored rule is preserved for later editing")
    policy["mail"]["autoForwarding"] = True
    policy["mail"]["externalForwarding"] = True
    admin.ok("PATCH", "/policies", policy)
    member.ok("PUT", "/sieve", {"rules": [], "vacation": stored["vacation"]})

    print("\n[6] File sharing policy")
    member.ok("POST", "/storage/a8m/connect", {})
    uploaded = member.ok("POST", "/attachments", {"name": "policy.txt", "mime": "text/plain", "dataBase64": base64.b64encode(b"policy test").decode()})
    file_record = member.ok("POST", "/files", {"providerId": "a8m", "attachmentId": uploaded["attachment"]["id"], "name": "policy.txt"})
    file_id = file_record["file"]["id"]
    policy["sharing"]["externalSharing"] = False
    admin.ok("PATCH", "/policies", policy)
    status, payload = member.request("POST", f"/files/{file_id}/share", {"access": "view", "expiresDays": 1})
    check(status == 403 and payload.get("error", {}).get("code") == "SHARING_DISABLED", "external sharing blocked", (status, payload))
    policy["sharing"]["externalSharing"] = True
    policy["sharing"]["linkDefault"] = "view"
    admin.ok("PATCH", "/policies", policy)
    status, payload = member.request("POST", f"/files/{file_id}/share", {"access": "edit", "expiresDays": 1})
    check(status == 403 and payload.get("error", {}).get("code") == "SHARING_ACCESS_LIMIT", "edit links capped by view default", (status, payload))
    status, payload = member.request("POST", f"/files/{file_id}/share", {"access": "view", "expiresDays": 1})
    check(status == 200 and bool(payload.get("share", {}).get("url")), "view-only link allowed", (status, payload))
    policy["sharing"]["allowedDomains"] = ["partner.test"]
    admin.ok("PATCH", "/policies", policy)
    status, payload = member.request("POST", f"/files/{file_id}/share", {"access": "view", "expiresDays": 1})
    check(status == 403 and payload.get("error", {}).get("code") == "SHARING_DOMAIN_RESTRICTED", "domain-scoped policy blocks bearer links", (status, payload))
    policy["sharing"]["allowedDomains"] = []
    admin.ok("PATCH", "/policies", policy)

    print("\n[7] Quarantine release policy")
    policy["mail"]["quarantine"] = True
    admin.ok("PATCH", "/policies", policy)
    subject = f"Quarantine policy {SUFFIX}"
    member.ok("POST", "/send", {"to": TEST_EMAIL, "subject": subject, "bodyText": "quarantine test"})
    inbox_message = wait_for(lambda: next((item for item in member.ok("GET", "/messages?folder=inbox").get("messages", []) if item.get("subject") == subject), None), 90, 3)
    check(bool(inbox_message), "self-sent message reaches INBOX", subject)
    if inbox_message:
        acted = member.request("POST", "/messages/actions", {"action": "spam", "ids": [inbox_message["id"]]})
        check(acted[0] == 200, "message moved to quarantine", acted)
        denied = member.request("POST", "/messages/actions", {"action": "notSpam", "ids": [inbox_message["id"]]})
        check(denied[0] == 403 and denied[1].get("error", {}).get("code") == "QUARANTINE_ADMIN_ONLY", "member cannot release quarantine", denied)
        queue = admin.ok("GET", "/quarantine")
        queued = next((item for item in queue.get("items", []) if item.get("userId") == user_id and item.get("id") == inbox_message["id"]), None)
        check(bool(queued), "admin quarantine queue shows the member message", queue.get("count"))
        released = admin.request("POST", f"/quarantine/{user_id}/{urllib.parse.quote(inbox_message['id'])}/release", {})
        check(released[0] == 200 and released[1].get("ok") is True, "administrator releases quarantine through the admin API", released)
        restored = member.ok("GET", "/messages?folder=inbox")
        check(any(item.get("id") == inbox_message["id"] for item in restored.get("messages", [])), "released message is back in the member inbox")

    print("\n[8] Retention worker")
    policy["mail"]["retentionDays"] = 1
    admin.ok("PATCH", "/policies", policy)
    context = ssl._create_unverified_context()
    imap = imaplib.IMAP4_SSL(MAIL_HOST, 993, ssl_context=context)
    imap.login(TEST_EMAIL, TEMP_TO_NEW)
    old_message = b"From: old@example.test\r\nTo: " + TEST_EMAIL.encode() + b"\r\nSubject: Expired retention test\r\nMessage-ID: <retention-" + SUFFIX.encode() + b"@example.test>\r\n\r\nold\r\n"
    old_date = imaplib.Time2Internaldate(datetime.datetime(2020, 1, 2, 3, 4, 5, tzinfo=datetime.timezone.utc))
    imap.append("Archive", "(\\Seen)", old_date, old_message)
    imap.select("Archive")
    before = imap.search(None, "BEFORE", "01-Jan-2021")[1][0].split()
    check(bool(before), "old message appended to Archive", before)
    imap.logout()
    stats = admin.ok("POST", "/maintenance/retention", {})["stats"]
    check(stats.get("messages", 0) >= 1, "retention removed the expired message", stats)
    imap = imaplib.IMAP4_SSL(MAIL_HOST, 993, ssl_context=context)
    imap.login(TEST_EMAIL, TEMP_TO_NEW)
    imap.select("Archive")
    after = imap.search(None, "BEFORE", "01-Jan-2021")[1][0].split()
    imap.logout()
    check(not after, "expired message is gone from Dovecot", after)
    policy["mail"]["retentionDays"] = original_policy["mail"]["retentionDays"]
    admin.ok("PATCH", "/policies", policy)

    print("\n[9] Password expiry flag")
    # The forced-change path is covered by [3]; expiry parsing and enforcement
    # are covered by unit tests. Here we verify the account reports no pending
    # change after the successful rotation.
    current = member.ok("GET", "/bootstrap").get("user", {})
    check(current.get("passwordChangeRequired") is False, "password change clears the required flag")
finally:
    try:
        if user_id:
            admin.request("DELETE", f"/users/{user_id}")
    except Exception as error:  # noqa: BLE001
        print("  WARN  test user cleanup failed:", error)
    try:
        admin.ok("PATCH", "/policies", original_policy)
    except Exception as error:  # noqa: BLE001
        print("  WARN  policy restore failed:", error)
    admin.request("DELETE", "/session")

print("\nResult:", "PASS" if not failures else f"FAIL ({len(failures)})")
sys.exit(0 if not failures else 1)
