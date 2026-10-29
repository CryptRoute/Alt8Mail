#!/usr/bin/env python3
"""Admin MFA smoke: TOTP enrollment, challenge login, and removal."""
import base64
import hashlib
import hmac
import http.cookiejar
import json
import os
import struct
import sys
import time
import urllib.error
import urllib.request

BASE = os.environ.get("ALT8MAIL_ADMIN_API", "https://130-94-46-22.sslip.io/admin/api")
EMAIL = os.environ.get("ALT8MAIL_ADMIN_EMAIL", "avery.chen@a8m.test")
PASSWORD = os.environ.get("ALT8MAIL_ADMIN_PASSWORD", "")
if not PASSWORD and os.environ.get("ALT8MAIL_ADMIN_PASSWORD_FILE"):
    with open(os.environ["ALT8MAIL_ADMIN_PASSWORD_FILE"], "r", encoding="utf-8") as handle:
        PASSWORD = handle.read().strip()
if not PASSWORD:
    raise SystemExit("admin password is required")

failures = []


def totp(secret):
    secret = secret.upper().replace(" ", "").rstrip("=")
    key = base64.b32decode(secret + "=" * ((8 - len(secret) % 8) % 8))
    counter = int(time.time() // 30)
    digest = hmac.new(key, struct.pack(">Q", counter), hashlib.sha1).digest()
    offset = digest[-1] & 0x0F
    value = struct.unpack(">I", digest[offset:offset + 4])[0] & 0x7FFFFFFF
    return f"{value % 1000000:06d}"


def check(condition, message, detail=None):
    if condition:
        print("  PASS ", message)
    else:
        print("  FAIL ", message, "" if detail is None else detail)
        failures.append(message)


class Client:
    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))
        self.csrf = ""

    def request(self, method, path, body=None):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(BASE + path, data=data, method=method)
        req.add_header("Accept", "application/json")
        if body is not None:
            req.add_header("Content-Type", "application/json")
        if self.csrf:
            req.add_header("X-CSRF-Token", self.csrf)
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


client = Client()
print("\n[1] Enable an admin authenticator")
status, payload = client.request("POST", "/session", {"email": EMAIL, "password": PASSWORD})
check(status == 200 and payload.get("ok"), "password sign-in before MFA", (status, payload))
client.csrf = payload.get("csrf", "")
begin = client.ok("POST", "/security/totp/begin", {})
secret = begin.get("secret", "")
check(len(secret) >= 16, "authenticator secret generated")
finish = client.ok("POST", "/security/totp/finish", {"code": totp(secret)})
check(len(finish.get("codes", [])) > 0, "single-use recovery codes issued")

print("\n[2] Password plus authenticator challenge")
check(client.request("DELETE", "/session")[0] == 200, "admin session signed out")
status, payload = client.request("POST", "/session", {"email": EMAIL, "password": PASSWORD})
check(status == 200 and payload.get("twoFactorRequired") is True, "password now returns an admin MFA challenge", (status, payload))
challenge = payload.get("challengeId", "")
status, payload = client.request("POST", "/session/2fa", {"challengeId": challenge, "code": "000000", "method": "totp"})
check(status == 401, "wrong TOTP is rejected", (status, payload))
status, payload = client.request("POST", "/session/2fa", {"challengeId": challenge, "code": totp(secret), "method": "totp"})
check(status == 200 and payload.get("ok"), "correct TOTP opens the admin console", (status, payload))
client.csrf = payload.get("csrf", "")

print("\n[3] Remove the authenticator with password + code")
status, payload = client.request("POST", "/security/totp/disable", {"password": "wrong-password", "code": totp(secret)})
check(status == 401, "wrong password blocks authenticator removal", (status, payload))
removed = client.ok("POST", "/security/totp/disable", {"password": PASSWORD, "code": totp(secret)})
check(removed.get("ok") is True, "authenticator removed")
client.request("DELETE", "/session")

print("\n[4] Password-only sign-in restored")
status, payload = client.request("POST", "/session", {"email": EMAIL, "password": PASSWORD})
check(status == 200 and payload.get("ok") is True and not payload.get("twoFactorRequired"), "password-only admin login works again", (status, payload))
client.csrf = payload.get("csrf", "")
status, payload = client.request("GET", "/security")
check(status == 200 and payload.get("security", {}).get("twoFactor", {}).get("totpEnabled") is False, "security summary reports MFA disabled")
client.request("DELETE", "/session")

print("\nResult:", "PASS" if not failures else f"FAIL ({len(failures)})")
sys.exit(0 if not failures else 1)
