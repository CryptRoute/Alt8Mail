#!/usr/bin/env python3
"""Tenant lifecycle smoke: create, active login, suspend, blocked login, reactivate."""
import http.cookiejar
import json
import os
import sys
import time
import urllib.error
import urllib.request

ADMIN_BASE = os.environ.get("ALT8MAIL_ADMIN_API", "https://130-94-46-22.sslip.io/admin/api")
USER_BASE = os.environ.get("ALT8MAIL_USER_API", "https://130-94-46-22.sslip.io/system/api")
EMAIL = os.environ.get("ALT8MAIL_ADMIN_EMAIL", "avery.chen@a8m.test")
PASSWORD = os.environ.get("ALT8MAIL_ADMIN_PASSWORD", "")
if not PASSWORD and os.environ.get("ALT8MAIL_ADMIN_PASSWORD_FILE"):
    with open(os.environ["ALT8MAIL_ADMIN_PASSWORD_FILE"], "r", encoding="utf-8") as handle:
        PASSWORD = handle.read().strip()
if not PASSWORD:
    raise SystemExit("admin password is required")
failures = []


def check(condition, message, detail=None):
    if condition:
        print("  PASS ", message)
    else:
        print("  FAIL ", message, "" if detail is None else detail)
        failures.append(message)


class Client:
    def __init__(self, base):
        self.base = base
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))
        self.csrf = ""

    def request(self, method, path, body=None):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header("Accept", "application/json")
        if body is not None:
            req.add_header("Content-Type", "application/json")
        csrf = self.csrf
        if not csrf:
            for cookie in self.jar:
                if cookie.name in ("a8m_admin_csrf", "a8m_system_csrf"):
                    csrf = cookie.value
        if csrf:
            req.add_header("X-CSRF-Token", csrf)
        try:
            with self.opener.open(req, timeout=30) as response:
                return response.status, json.loads(response.read() or b"{}")
        except urllib.error.HTTPError as error:
            return error.code, json.loads(error.read() or b"{}")

    def ok(self, method, path, body=None):
        status, payload = self.request(method, path, body)
        if status >= 400:
            raise RuntimeError(f"{method} {path} -> {status}: {payload}")
        return payload


stamp = int(time.time())
slug = f"lifecycle-{stamp}"
admin = Client(ADMIN_BASE)
status, payload = admin.request("POST", "/session", {"email": EMAIL, "password": PASSWORD})
if status != 200 or not payload.get("ok"):
    raise SystemExit(f"admin login failed: {status} {payload}")
admin.csrf = payload.get("csrf", "")
tenant_id = ""
try:
    print("\n[1] Provision a tenant and owner")
    tenant = admin.ok("POST", "/tenants", {
        "name": f"Lifecycle Org {stamp}",
        "slug": slug,
        "primaryDomain": f"{slug}.test",
        "plan": "business",
        "region": "us-east-1",
        "ownerName": "Lifecycle Owner",
        "ownerEmail": f"owner@{slug}.test",
    })
    tenant_id = tenant["tenant"]["id"]
    owner_email = tenant["owner"]["email"]
    owner_password = tenant["owner"]["temporaryPassword"]
    check(tenant["tenant"]["status"] == "active", "tenant is active")

    print("\n[2] Active tenant owner can sign in")
    user = Client(USER_BASE)
    status, payload = user.request("POST", "/session", {"email": owner_email, "password": owner_password})
    check(status == 200 and payload.get("user", {}).get("email") == owner_email, "owner webmail sign-in succeeds", (status, payload))
    if status == 200:
        user.csrf = payload.get("csrf", "")
        check(user.request("GET", "/session")[0] == 200, "owner session validates")

    print("\n[3] Suspension invalidates access")
    admin.ok("PATCH", f"/tenants/{tenant_id}", {"status": "suspended"})
    status, payload = user.request("GET", "/session")
    check(status == 401, "existing owner session is invalidated", (status, payload))
    fresh = Client(USER_BASE)
    status, payload = fresh.request("POST", "/session", {"email": owner_email, "password": owner_password})
    check(status == 401, "new owner sign-in is blocked", (status, payload))

    print("\n[4] Reactivation restores access")
    admin.ok("PATCH", f"/tenants/{tenant_id}", {"status": "active"})
    user = Client(USER_BASE)
    status, payload = user.request("POST", "/session", {"email": owner_email, "password": owner_password})
    check(status == 200 and payload.get("user", {}).get("email") == owner_email, "owner sign-in works after reactivation", (status, payload))
    if status == 200:
        user.request("DELETE", "/session")
finally:
    admin.request("DELETE", "/session")

print("\nCleanup tenant:", tenant_id, slug)
print("\nResult:", "PASS" if not failures else f"FAIL ({len(failures)})")
sys.exit(0 if not failures else 1)
