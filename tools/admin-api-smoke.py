#!/usr/bin/env python3
"""Admin API smoke test. Requires MERIDIAN_ADMIN_PASSWORD or MERIDIAN_ADMIN_PASSWORD_FILE."""
import http.cookiejar
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

BASE = os.environ.get("MERIDIAN_ADMIN_API", "https://130-94-46-22.sslip.io/admin/api")
EMAIL = os.environ.get("MERIDIAN_ADMIN_EMAIL", "avery.chen@meridian.test")
PASSWORD = os.environ.get("MERIDIAN_ADMIN_PASSWORD", "")
if not PASSWORD and os.environ.get("MERIDIAN_ADMIN_PASSWORD_FILE"):
    with open(os.environ["MERIDIAN_ADMIN_PASSWORD_FILE"], "r", encoding="utf-8") as handle:
        PASSWORD = handle.read().strip()
if not PASSWORD:
    raise SystemExit("MERIDIAN_ADMIN_PASSWORD or MERIDIAN_ADMIN_PASSWORD_FILE is required")

jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(
    urllib.request.HTTPCookieProcessor(jar),
    urllib.request.HTTPSHandler(context=None),
)
csrf = ""
failures = []


def request(method, path, body=None, raw=False):
    url = BASE + path
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Accept", "application/json")
    if body is not None:
        req.add_header("Content-Type", "application/json")
    if csrf:
        req.add_header("X-CSRF-Token", csrf)
    try:
        with opener.open(req, timeout=30) as response:
            payload = response.read()
            if raw:
                return response.status, payload, dict(response.headers)
            return response.status, json.loads(payload or b"{}"), dict(response.headers)
    except urllib.error.HTTPError as error:
        payload = error.read()
        try:
            parsed = json.loads(payload or b"{}")
        except json.JSONDecodeError:
            parsed = {"raw": payload.decode(errors="replace")}
        return error.code, parsed, dict(error.headers)


def check(condition, message, detail=None):
    if condition:
        print("  PASS ", message)
    else:
        print("  FAIL ", message, "" if detail is None else detail)
        failures.append(message)


def ok(method, path, body=None):
    status, payload, _ = request(method, path, body)
    if status >= 400:
        raise RuntimeError(f"{method} {path} -> {status}: {payload}")
    return payload


print("\n[1] Admin sign-in and bootstrap")
status, payload, headers = request("POST", "/session", {"email": EMAIL, "password": PASSWORD})
check(status == 200 and payload.get("ok"), "admin login succeeds", (status, payload))
check(bool(payload.get("twoFactorRequired")) is False, "staging admin has no blocking second factor")
check(bool(payload.get("csrf")), "CSRF token supplied")
csrf = payload.get("csrf", "")
boot = ok("GET", "/bootstrap")
check(boot["admin"]["isSystemAdmin"], "account has system administrator access")
check(len(boot.get("tenants", [])) >= 1, "tenant list is available")
check(len(boot.get("catalog", [])) > 10, "permission catalog is available")
tenant_id = boot["activeTenant"]["id"]

print("\n[2] Users")
users = ok("GET", "/users?limit=100")
check(users.get("total", 0) >= 5, "users list is populated", users.get("total"))
check(any(u["email"] == EMAIL for u in users["users"]), "admin identity is in the user list")
stamp = int(time.time())
smoke_email = f"admin.smoke.{stamp}@meridian.test"
created = ok("POST", "/users", {
    "name": "Admin Smoke",
    "email": smoke_email,
    "role": "member",
    "quotaBytes": 5 * 1024 * 1024 * 1024,
    "title": "Smoke account",
})
check(created["user"]["email"] == smoke_email, "user creation returns the account")
check(bool(created.get("temporaryPassword")), "temporary web password is issued once")
check(bool(created.get("mailPassword")), "mailbox password is issued once")
user_id = created["user"]["id"]
detail = ok("GET", f"/users/{urllib.parse.quote(user_id)}")
check(len(detail["user"]["aliases"]) == 1, "primary alias is provisioned")
ok("PATCH", f"/users/{urllib.parse.quote(user_id)}", {"title": "Senior Smoke", "status": "suspended"})
detail = ok("GET", f"/users/{urllib.parse.quote(user_id)}")
check(detail["user"]["title"] == "Senior Smoke" and detail["user"]["status"] == "suspended", "user profile and status update")
alias_email = f"smoke.alias.{stamp}@meridian.test"
ok("POST", f"/users/{urllib.parse.quote(user_id)}/aliases", {"email": alias_email, "label": "Smoke alias"})
detail = ok("GET", f"/users/{urllib.parse.quote(user_id)}")
check(len(detail["user"]["aliases"]) == 2, "alias can be added")
alias_id = next(a["id"] for a in detail["user"]["aliases"] if a["kind"] == "alias")
ok("DELETE", f"/users/{urllib.parse.quote(user_id)}/aliases/{urllib.parse.quote(alias_id)}")
ok("POST", f"/users/{urllib.parse.quote(user_id)}/reset-password", {})
ok("POST", f"/users/{urllib.parse.quote(user_id)}/reset-mfa", {})
ok("POST", f"/users/{urllib.parse.quote(user_id)}/revoke-sessions", {})
ok("DELETE", f"/users/{urllib.parse.quote(user_id)}")
closed = ok("GET", f"/users/{urllib.parse.quote(user_id)}")
check(closed["user"].get("deletedAt") is not None, "account is soft-closed")
ok("POST", f"/users/{urllib.parse.quote(user_id)}/restore", {})
ok("POST", "/users/bulk", {"action": "suspend", "ids": [user_id]})
ok("POST", "/users/bulk", {"action": "activate", "ids": [user_id]})

print("\n[3] Groups and org units")
group = ok("POST", "/groups", {
    "name": f"Smoke group {stamp}",
    "email": f"smoke-group-{stamp}@meridian.test",
    "type": "distribution",
    "description": "admin smoke",
})["group"]
check(group["memberCount"] == 0, "group starts empty")
ok("POST", f"/groups/{urllib.parse.quote(group['id'])}/members", {"userId": user_id, "memberRole": "member"})
group = ok("GET", f"/groups/{urllib.parse.quote(group['id'])}")["group"]
check(group["memberCount"] == 1, "group membership can be added")
ok("DELETE", f"/groups/{urllib.parse.quote(group['id'])}/members/{urllib.parse.quote(user_id)}")
ok("DELETE", f"/groups/{urllib.parse.quote(group['id'])}")
unit = ok("POST", "/org-units", {"name": f"Smoke Unit {stamp}", "description": "temporary"})["orgUnit"]
ok("PATCH", f"/org-units/{urllib.parse.quote(unit['id'])}", {"name": f"Smoke Unit Renamed {stamp}"})
bundle = ok("GET", "/org-units")
check(any(u["id"] == unit["id"] for u in bundle["orgUnits"]), "org unit is listed")
ok("DELETE", f"/org-units/{urllib.parse.quote(unit['id'])}")

print("\n[4] Roles and assignments")
role = ok("POST", "/roles", {
    "name": f"Smoke auditor {stamp}",
    "description": "temporary",
    "permissions": ["users.read", "audit.read"],
})["role"]
check(sorted(role["permissions"]) == ["audit.read", "users.read"], "custom role permissions are normalized")
ok("PATCH", f"/roles/{urllib.parse.quote(role['id'])}", {"description": "updated", "permissions": ["users.read"]})
ok("POST", f"/roles/{urllib.parse.quote(role['id'])}/assign", {"userId": user_id})
ok("DELETE", f"/roles/{urllib.parse.quote(role['id'])}/assign/{urllib.parse.quote(user_id)}")
ok("DELETE", f"/roles/{urllib.parse.quote(role['id'])}")

print("\n[5] Domains")
domain = ok("POST", "/domains", {"domain": f"smoke-{stamp}.test", "dkimSelector": "meridian", "primary": False})["domain"]
check(domain["status"] == "pending", "new domain starts pending")
verified = ok("POST", f"/domains/{urllib.parse.quote(domain['id'])}/verify", {})
check(verified.get("ok") is False, "unconfigured DNS does not verify")
ok("PATCH", f"/domains/{urllib.parse.quote(domain['id'])}", {"dmarcPolicy": "quarantine"})
ok("DELETE", f"/domains/{urllib.parse.quote(domain['id'])}")

print("\n[6] Policies and settings")
policy = ok("GET", "/policies")["policy"]
policy["security"]["passwordMinLength"] = 14
ok("PATCH", "/policies", policy)
updated = ok("GET", "/policies")["policy"]
check(updated["security"]["passwordMinLength"] == 14, "policy persists")
policy["security"]["passwordMinLength"] = 12
ok("PATCH", "/policies", policy)
settings = ok("GET", "/settings")
ok("PATCH", "/settings", {"name": settings["tenant"]["name"], "branding": {"productName": "Meridian Mail", "primaryColor": "#0f6cbd"}})

print("\n[7] Audit and admin sessions")
audit = ok("GET", "/audit?limit=100")
check(audit.get("total", 0) >= 5, "admin audit contains actions", audit.get("total"))
status, csv_payload, headers = request("GET", "/audit.csv", raw=True)
check(status == 200 and b"created_at" in csv_payload, "audit CSV exports")
sessions = ok("GET", "/security/sessions")
check(sessions.get("count", 0) >= 1, "admin sessions list works")

print("\n[8] Tenant creation and context")
tenant = ok("POST", "/tenants", {
    "name": f"Smoke Org {stamp}",
    "slug": f"smoke-{stamp}",
    "primaryDomain": f"smoke-{stamp}.test",
    "plan": "business",
    "region": "us-east-1",
    "ownerName": "Smoke Owner",
    "ownerEmail": f"owner@smoke-{stamp}.test",
})
check(tenant["tenant"]["slug"] == f"smoke-{stamp}", "tenant created")
check(bool(tenant.get("owner", {}).get("temporaryPassword")), "owner credentials issued")
ok("POST", "/context", {"tenantId": tenant["tenant"]["id"]})
back = ok("POST", "/context", {"tenantId": tenant_id})
check(back["activeTenant"]["id"] == tenant_id, "context restored")

print("\n[9] System administrators")
ok("POST", "/system/admins", {"email": "jordan.lee@meridian.test"})
admins = ok("GET", "/system/admins")
check(any(a["email"] == "jordan.lee@meridian.test" for a in admins["admins"]), "system admin granted")
jordan_id = next(a["id"] for a in admins["admins"] if a["email"] == "jordan.lee@meridian.test")
ok("DELETE", f"/system/admins/{urllib.parse.quote(jordan_id)}")

print("\n[10] Cleanup targets")
print("  INFO  test user:", smoke_email)
print("  INFO  test tenant:", f"smoke-{stamp}")

print("\nResult:", "PASS" if not failures else f"FAIL ({len(failures)})")
sys.exit(0 if not failures else 1)
