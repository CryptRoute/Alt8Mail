#!/usr/bin/env python3
"""Exercise the Go feature API against a staging backend (no external deps)."""
import base64
import http.cookiejar
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timedelta, timezone

BASE = os.environ.get("GO_API", "http://127.0.0.1:8080/api")
EMAIL = os.environ.get("GO_EMAIL", "avery.chen@meridian.test")
PASSWORD = open(os.environ["GO_PASSWORD_FILE"], encoding="utf-8").read().strip() if os.environ.get("GO_PASSWORD_FILE") else os.environ.get("GO_PASSWORD", "")
JAR = http.cookiejar.CookieJar()
OPENER = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(JAR))
CSRF = ""
failures = 0


def check(condition, message, detail=""):
    global failures
    if condition:
        print(f"  PASS  {message}")
    else:
        failures += 1
        print(f"  FAIL  {message} {detail}")


def request(method, path, body=None, raw=False):
    data = None
    headers = {"Accept": "application/json"}
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        headers["Content-Type"] = "application/json"
    if CSRF and method not in ("GET", "HEAD"):
        headers["X-CSRF-Token"] = CSRF
    req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    try:
        with OPENER.open(req, timeout=30) as response:
            payload = response.read()
            if raw:
                return response.status, payload, dict(response.headers)
            return response.status, json.loads(payload) if payload else None, dict(response.headers)
    except urllib.error.HTTPError as error:
        payload = error.read()
        try:
            parsed = json.loads(payload)
        except Exception:
            parsed = {"raw": payload.decode("utf-8", "replace")}
        return error.code, parsed, dict(error.headers)


def ok(method, path, body=None, expected=(200, 201)):
    status, payload, headers = request(method, path, body)
    if status not in expected:
        raise AssertionError(f"{method} {path} -> {status}: {payload}")
    return payload


def main():
    global CSRF
    print("\n[1] Sign in and mailbox state")
    login = ok("POST", "/session", {"email": EMAIL, "password": PASSWORD, "remember": True})
    CSRF = login.get("csrf") or ""
    check(bool(CSRF), "login returns a CSRF token")
    boot = ok("GET", "/bootstrap")
    check(boot.get("mailboxMode") == "imap", "mailbox is in real IMAP mode", str(boot.get("mailboxMode")))
    check(len(boot["messages"]) >= 1, "real Dovecot messages are present", str(len(boot["messages"])))
    check(any(message.get("imapUid") for message in boot["messages"]), "messages carry real IMAP UIDs")
    demo_subjects = {
        "Q4 roadmap review — Thursday 10:00",
        "Brightline — signed order form and next steps",
        "Design review notes + updated flows",
    }
    check(not any(message.get("subject") in demo_subjects for message in boot["messages"]), "reference JSON demo messages are absent")
    demo_tasks = {"Approve invoice #4821", "Finalize Q4 launch checklist", "Review vendor security questionnaire"}
    check(not any(task.get("title") in demo_tasks for task in boot["tasks"]), "tasks are not seeded with demo rows")
    check(len(boot["files"]) == 0, "files start empty until a real upload", str(len(boot["files"])))
    check(len(boot["calendarEvents"]) == 0, "calendar starts without demo events")
    check(len(boot["storageProviders"]) == 5, "five storage providers are configured")
    check(len(boot["aliases"]) >= 1, "database aliases are present")
    check(boot["user"]["email"] == EMAIL, "state user is returned")
    check(boot["quota"]["usedBytes"] > 0, "quota is computed from the real mailbox")
    check(boot["outbound"]["limit"] == 200, "outbound cap is exposed")

    print("\n[2] Message flags, bulk actions and undo")
    inbox = [message for message in boot["messages"] if message["folder"] == "inbox"]
    check(bool(inbox), "inbox messages are available")
    target = inbox[0]
    patched = ok("PATCH", f"/messages/{urllib.parse.quote(target['id'])}", {"starred": True, "unread": False})
    check(patched["message"]["starred"] is True, "message flag patch persists")
    action = ok("POST", "/messages/actions", {"ids": [target["id"]], "action": "archive"})
    check(action["affected"] == 1, "bulk archive affects one message")
    check(action["undoToken"], "bulk action returns an undo token")
    undo = ok("POST", f"/undo/{urllib.parse.quote(action['undoToken'])}", {})
    check(undo["restored"] == 1, "undo restores the archived message")
    after_undo = ok("GET", "/bootstrap")
    restored = next(message for message in after_undo["messages"] if message["id"] == target["id"])
    check(restored["folder"] == target["folder"], "restored message returns to its folder")
    marked = ok("POST", "/messages/read", {"ids": [target["id"]]})
    check(marked["ok"] is True, "mark-read endpoint acknowledges")

    print("\n[3] Tasks")
    task = ok("POST", "/tasks", {
        "title": "Go API smoke task",
        "notes": "Created by the feature API smoke test.",
        "due": "2026-11-01",
        "dueTime": "14:30",
        "priority": "high",
        "assigneeEmail": "elena.vasquez@meridian.test",
        "tags": ["Smoke", "API"],
    })["task"]
    check(task["title"] == "Go API smoke task", "task is created")
    check(task["assignee"]["email"] == "elena.vasquez@meridian.test", "task assignee is resolved from the directory")
    completed = ok("PATCH", f"/tasks/{urllib.parse.quote(task['id'])}", {"status": "done"})["task"]
    check(completed["status"] == "done" and completed["completedAt"], "task can be completed")
    tasks = ok("GET", "/tasks")
    check(any(entry["id"] == task["id"] for entry in tasks["tasks"]), "task appears in the task list")
    ok("DELETE", f"/tasks/{urllib.parse.quote(task['id'])}")

    print("\n[4] Server drafts")
    draft_id = "draft_smoke_api"
    saved = ok("PUT", f"/drafts/{draft_id}", {
        "toText": "nora.patel@meridian.test",
        "ccText": "",
        "bccText": "",
        "subject": "Go API smoke draft",
        "bodyText": "Draft body saved through the Go feature API.",
        "fromEmail": "avery.chen@meridian.test",
        "attachments": [],
        "windowMode": "floating",
        "priority": "normal",
        "receipt": False,
        "delivery": False,
        "saveCopy": True,
    })
    check(saved["draft"]["id"] == draft_id, "draft is saved")
    drafts = ok("GET", "/drafts")
    check(any(entry["id"] == draft_id for entry in drafts["drafts"]), "draft is listed")
    deleted = ok("DELETE", f"/drafts/{draft_id}")
    check(deleted["ok"] is True, "draft is deleted")

    print("\n[5] Send queue, scheduled edit, send-now and cancel")
    future = (datetime.now(timezone.utc) + timedelta(hours=2)).replace(microsecond=0).isoformat().replace("+00:00", "Z")
    scheduled = ok("POST", "/send", {
        "fromEmail": "avery.chen@meridian.test",
        "to": "nora.patel@meridian.test",
        "cc": "",
        "bcc": "",
        "subject": "Go scheduling smoke",
        "bodyText": "Scheduled through the Go API.",
        "attachments": [],
        "priority": "normal",
        "receipt": False,
        "delivery": False,
        "saveCopy": True,
        "scheduledFor": future,
    })
    queued = scheduled["queued"]
    check(scheduled["scheduled"] is True and queued["scheduled"] is True, "scheduled message is queued")
    later = (datetime.now(timezone.utc) + timedelta(hours=3)).replace(microsecond=0).isoformat().replace("+00:00", "Z")
    edited = ok("PATCH", f"/outbox/{urllib.parse.quote(queued['id'])}", {
        "subject": "Go scheduling smoke — edited",
        "to": "nora.patel@meridian.test",
        "cc": "",
        "bodyText": "Edited through the Go outbox API.",
        "sendAt": later,
    })
    check(edited["item"]["subject"].endswith("edited"), "scheduled message can be edited")
    outbox = ok("GET", "/outbox")
    check(any(item["id"] == queued["id"] for item in outbox["pending"]), "scheduled message is in the outbox")
    sent_now = ok("POST", f"/outbox/{urllib.parse.quote(queued['id'])}/send-now", {})
    check(bool(sent_now["sentMessageId"]), "send-now commits the message")
    outbox = ok("GET", "/outbox")
    check(not any(item["id"] == queued["id"] for item in outbox["pending"]), "sent item leaves the outbox")
    boot = ok("GET", "/bootstrap")
    check(any(m["folder"] == "sent" and m["subject"] == "Go scheduling smoke — edited" for m in boot["messages"]), "sent copy is in Sent")

    held = ok("POST", "/send", {
        "fromEmail": "avery.chen@meridian.test",
        "to": "nora.patel@meridian.test",
        "cc": "",
        "bcc": "",
        "subject": "Go undo-send smoke",
        "bodyText": "Cancelled inside the undo window.",
        "attachments": [],
        "priority": "normal",
        "receipt": False,
        "delivery": False,
        "saveCopy": True,
        "holdSeconds": 5,
    })
    check(held["scheduled"] is False and held["queued"]["kind"] == "undo", "hold message enters the undo window")
    cancelled = ok("POST", f"/outbox/{urllib.parse.quote(held['queued']['id'])}/cancel", {})
    check(cancelled["draft"]["subject"] == "Go undo-send smoke", "cancelled send is restored to Drafts")
    ok("DELETE", f"/drafts/{urllib.parse.quote(cancelled['draft']['id'])}")

    print("\n[6] Attachments and files")
    pdf = b"%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<<>>\n%%EOF\n"
    attachment = ok("POST", "/attachments", {
        "name": "go-smoke.pdf",
        "mime": "application/pdf",
        "dataBase64": base64.b64encode(pdf).decode("ascii"),
    })["attachment"]
    check(attachment["previewable"] is True, "PDF upload is detected and previewable")
    status, content, headers = request("GET", f"/attachments/{urllib.parse.quote(attachment['id'])}/content", raw=True)
    check(status == 200 and content == pdf, "attachment content round-trips")
    check("inline" in headers.get("Content-Disposition", ""), "previewable attachment is served inline")
    file_record = ok("POST", "/files", {
        "providerId": "meridian",
        "attachmentId": attachment["id"],
        "path": "Uploads",
    })["file"]
    check(file_record["name"] == "go-smoke.pdf", "uploaded file record is created")
    share_status, share_payload, _ = request("POST", f"/files/{urllib.parse.quote(file_record['id'])}/share", {"access": "view", "expiresDays": 7})
    if share_status == 200:
        check(share_payload["share"]["access"] == "view", "file share link is created")
    else:
        check(share_status == 403 and share_payload.get("error", {}).get("code") == "SHARING_DISABLED",
              "file sharing is blocked by tenant policy", (share_status, share_payload))
    updated = ok("PATCH", f"/files/{urllib.parse.quote(file_record['id'])}", {"starred": True})
    check(updated["file"]["starred"] is True, "file metadata patch persists")
    ok("DELETE", f"/files/{urllib.parse.quote(file_record['id'])}")

    print("\n[7] Storage connectors")
    providers = {provider["id"]: provider for provider in ok("GET", "/storage")["providers"]}
    check(providers["meridian"]["connected"] is True, "Meridian Drive is connected")
    dropbox = providers["dropbox"]
    status, payload, _ = request("POST", "/storage/dropbox/connect", {})
    if dropbox.get("configured"):
        check(status in (200, 409), "configured Dropbox connector responds", (status, payload))
    else:
        check(status == 503 and payload.get("error", {}).get("code") == "OAUTH_NOT_CONFIGURED",
              "unconfigured OAuth provider is refused cleanly", (status, payload))
    s3 = providers.get("s3", {})
    if s3.get("configured"):
        check(s3.get("connected") is True, "configured S3 connector is connected")
        status, payload, _ = request("POST", "/storage/s3/sync", {})
        check(status == 200, "configured S3 connector syncs", (status, payload))

    print("\n[8] Sieve rules")
    sieve = ok("GET", "/sieve")
    original_rules = sieve["rules"]
    original_vacation = sieve["vacation"]
    test_rules = list(original_rules) + [{
        "id": "rule_smoke",
        "name": "Go API smoke rule",
        "enabled": True,
        "match": "all",
        "conditions": [{"field": "subject", "operator": "contains", "value": "smoke-test"}],
        "action": {"type": "move", "value": "archive"},
    }]
    updated_sieve = ok("PUT", "/sieve", {"rules": test_rules, "vacation": original_vacation})
    check(any(rule["id"] == "rule_smoke" for rule in updated_sieve["rules"]), "Sieve rule is saved")
    check(updated_sieve["revision"] > sieve["revision"], "Sieve revision increments")
    status, script, _ = request("GET", "/sieve/script", raw=True)
    check(status == 200 and b"fileinto" in script, "Sieve script is rendered")
    restored_sieve = ok("PUT", "/sieve", {"rules": original_rules, "vacation": original_vacation})
    check(not any(rule["id"] == "rule_smoke" for rule in restored_sieve["rules"]), "Sieve ruleset is restored")

    print("\n[9] Preferences, search, quota and audit")
    prefs = ok("GET", "/preferences")["preferences"]
    changed = ok("PATCH", "/preferences", {"timeFormat": "24h", "timeZone": "UTC"})
    check(changed["preferences"]["timeFormat"] == "24h", "preferences patch persists")
    inbox_view = prefs.get("inboxView", "grouped")
    individual = ok("PATCH", "/preferences", {"inboxView": "individual"})
    check(individual["preferences"]["inboxView"] == "individual", "individual inbox view persists")
    ok("PATCH", "/preferences", {"timeFormat": prefs.get("timeFormat", "12h"),
                                 "timeZone": prefs.get("timeZone", "UTC"),
                                 "inboxView": inbox_view})
    created_app_password = ok("POST", "/security/app-passwords", {
        "label": "API smoke verification", "password": PASSWORD, "expiresInDays": 1,
    })
    check(bool(created_app_password.get("token")), "app password creation accepts the current account password")
    ok("DELETE", "/security/app-passwords/" + urllib.parse.quote(created_app_password["id"]))
    search_sender = boot["messages"][0]["from"]["email"]
    search = ok("GET", "/search?q=" + urllib.parse.quote("from:" + search_sender))
    check(search["count"] >= 1, "search operators return real mailbox matches", str(search["count"]))
    messages = ok("GET", "/messages?folder=inbox")
    check(messages["count"] >= 1 and messages["threads"], "folder listing includes thread summaries")
    quota = ok("GET", "/quota")
    check(quota["usedBytes"] > 0, "quota endpoint reports usage")
    outbound = ok("GET", "/outbound")
    check(outbound["committed"] >= 1, "outbound events are counted")
    audit = ok("GET", "/audit?limit=200")
    check(audit["total"] >= 5, "audit log returns entries", str(audit["total"]))
    status, csv_payload, _ = request("GET", "/audit.csv", raw=True)
    check(status == 200 and b"action,target" in csv_payload, "audit CSV downloads")

    print("\n[10] Trust details")
    external = next((message for message in boot["messages"] if message.get("external") and message.get("auth", {}).get("spf")), None)
    if external:
        trust = ok("GET", f"/messages/{urllib.parse.quote(external['id'])}/trust")
        check(len(trust["checks"]) == 3, "trust profile includes SPF, DKIM and DMARC")
        toggled = ok("POST", f"/messages/{urllib.parse.quote(external['id'])}/trust", {"trusted": True})
        check(toggled["trusted"] is True, "sender can be trusted")

    print("\nResult:", "PASS" if failures == 0 else f"FAIL ({failures})")
    return 0 if failures == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
