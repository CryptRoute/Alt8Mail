#!/usr/bin/env python3
"""Verify that tenant MFA policy reaches the mail user login path."""
import http.cookiejar
import json
import os
import sys
import urllib.error
import urllib.request

ADMIN_BASE = os.environ.get("MERIDIAN_ADMIN_API", "https://130-94-46-22.sslip.io/admin/api")
USER_BASE = os.environ.get("MERIDIAN_USER_API", "https://130-94-46-22.sslip.io/system/api")
EMAIL = os.environ.get("MERIDIAN_ADMIN_EMAIL", "avery.chen@meridian.test")
PASSWORD = os.environ.get("MERIDIAN_ADMIN_PASSWORD", "")
if not PASSWORD and os.environ.get("MERIDIAN_ADMIN_PASSWORD_FILE"):
    with open(os.environ["MERIDIAN_ADMIN_PASSWORD_FILE"], "r", encoding="utf-8") as handle:
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
                if cookie.name in ("meridian_admin_csrf", "meridian_system_csrf"):
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


admin = Client(ADMIN_BASE)
status, payload = admin.request("POST", "/session", {"email": EMAIL, "password": PASSWORD})
if status != 200 or not payload.get("ok"):
    raise SystemExit(f"admin login failed: {status} {payload}")
admin.csrf = payload.get("csrf", "")
policy = admin.ok("GET", "/policies")["policy"]
original = policy["security"]["mfaEnforcement"]
try:
    print("\n[1] Enforce MFA for all users")
    policy["security"]["mfaEnforcement"] = "all"
    admin.ok("PATCH", "/policies", policy)
    updated = admin.ok("GET", "/policies")["policy"]
    check(updated["security"]["mfaEnforcement"] == "all", "tenant policy persisted")

    print("\n[2] Mail user is stopped at enrollment")
    user = Client(USER_BASE)
    status, payload = user.request("POST", "/session", {"email": EMAIL, "password": PASSWORD})
    check(status == 200 and payload.get("enrollmentRequired") is True, "un-enrolled user receives the enrollment challenge", (status, payload))
    check(payload.get("twoFactorRequired") is not True, "no session is created before enrollment")

    print("\n[3] Restore optional MFA")
    policy["security"]["mfaEnforcement"] = original or "off"
    admin.ok("PATCH", "/policies", policy)
    restored = admin.ok("GET", "/policies")["policy"]
    check(restored["security"]["mfaEnforcement"] == (original or "off"), "policy restored")

    print("\n[4] Password-only mail sign-in works again")
    user = Client(USER_BASE)
    status, payload = user.request("POST", "/session", {"email": EMAIL, "password": PASSWORD})
    check(status == 200 and payload.get("enrollmentRequired") is not True and payload.get("twoFactorRequired") is not True, "password-only sign-in is restored", (status, payload))
    if status == 200:
        user.request("DELETE", "/session")
finally:
    policy["security"]["mfaEnforcement"] = original or "off"
    try:
        admin.ok("PATCH", "/policies", policy)
    except Exception as error:  # noqa: BLE001
        print("  WARN  policy restore failed:", error)
    admin.request("DELETE", "/session")

print("\nResult:", "PASS" if not failures else f"FAIL ({len(failures)})")
sys.exit(0 if not failures else 1)
