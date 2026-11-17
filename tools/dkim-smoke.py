#!/usr/bin/env python3
"""Verify that the admin domain API serves real DKIM key material and that
verification distinguishes a missing key from a DNS mismatch."""
import http.cookiejar
import json
import os
import sys
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


def check(condition, message, detail=None):
    print("  PASS ", message) if condition else print("  FAIL ", message, "" if detail is None else detail)
    if not condition:
        failures.append(message)


class Client:
    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))
        self.csrf = ""

    def request(self, method, path, body=None):
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(BASE + path, data=data, method=method)
        request.add_header("Accept", "application/json")
        if body is not None:
            request.add_header("Content-Type", "application/json")
        if self.csrf:
            request.add_header("X-CSRF-Token", self.csrf)
        try:
            with self.opener.open(request, timeout=30) as response:
                return response.status, json.loads(response.read() or b"{}")
        except urllib.error.HTTPError as error:
            return error.code, json.loads(error.read() or b"{}")

    def ok(self, method, path, body=None):
        status, payload = self.request(method, path, body)
        if status >= 400:
            raise RuntimeError(f"{method} {path} -> {status}: {payload}")
        return payload


client = Client()
status, payload = client.request("POST", "/session", {"email": EMAIL, "password": PASSWORD})
if status != 200 or not payload.get("ok"):
    raise SystemExit(f"admin login failed: {status} {payload}")
client.csrf = payload.get("csrf", "")

domains = client.ok("GET", "/domains").get("domains", [])
check(bool(domains), "domain list is available")
for domain in domains:
    dkim = next((record for record in domain.get("records", []) if "_domainkey" in record.get("name", "")), None)
    check(dkim is not None, f"{domain['domain']}: DKIM record is present")
    if not dkim:
        continue
    value = dkim.get("value", "")
    check(value.startswith("v=DKIM1; k=rsa; p="), f"{domain['domain']}: DKIM record is a real v=DKIM1 record", value[:80])
    check(len(value) > 160, f"{domain['domain']}: DKIM public key is populated", len(value))
    check("<public key" not in value.lower(), f"{domain['domain']}: DKIM placeholder is absent")
    check(domain.get("dkimKeyReady") is True, f"{domain['domain']}: admin reports key ready", domain.get("dkimKeyError"))
    status, result = client.request("POST", f"/domains/{domain['id']}/verify", {})
    check(status == 200, f"{domain['domain']}: DNS verification endpoint responds", status)
    detail = (result.get("details") or {}).get("dkim", "")
    check("not been provisioned" not in detail, f"{domain['domain']}: verifier sees the provisioned signing key", detail)
    check("ready" in result, f"{domain['domain']}: response distinguishes ownership from mail readiness")

print("\nResult:", "PASS" if not failures else f"FAIL ({len(failures)})")
sys.exit(0 if not failures else 1)
