#!/usr/bin/env python3
"""Provision distinct webmail accounts for the 1,000-user soak.

Uses the real admin user-creation path so aliases, mailboxes and workspace state
are created exactly as for a normal account. The password is supplied through
SOAK_PASSWORD and is never written to the artifact. The resulting user list is
metadata only (email/name/index).
"""
from __future__ import annotations

import concurrent.futures
import json
import os
import pathlib
import sys
import time
import urllib3

import requests

urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
BASE = "https://130-94-46-22.sslip.io"
OUT = pathlib.Path("artifacts/soak")
OUT.mkdir(parents=True, exist_ok=True)


def new_session():
    s = requests.Session()
    s.trust_env = False
    s.verify = False
    s.headers.update({"User-Agent": "Alt8Mail-soak-provision/1.0"})
    return s


def admin_login(s, email, password):
    r = s.post(BASE + "/admin/api/session", json={"email": email, "password": password}, timeout=30)
    if r.status_code != 200:
        raise RuntimeError(f"admin login HTTP {r.status_code}")
    return r.json().get("csrf", "")


def create_one(session, email, name, password, csrf):
    response = session.post(BASE + "/admin/api/users",
                            headers={"X-CSRF-Token": csrf, "Content-Type": "application/json"},
                            json={"name": name, "email": email, "role": "member",
                                  "status": "active", "password": password,
                                  "quotaBytes": 16 * 1024 * 1024 * 1024},
                            timeout=60)
    data = response.json() if response.headers.get("content-type", "").startswith("application/json") else {}
    if response.status_code == 409 and "already in use" in json.dumps(data).lower():
        return {"email": email, "status": "exists"}
    if response.status_code != 201:
        return {"email": email, "status": "failed", "http": response.status_code,
                "code": (data.get("error") or {}).get("code", "")}
    return {"email": email, "status": "created", "stateReady": bool(data.get("stateReady")),
            "userId": (data.get("user") or {}).get("id", "")}


def main():
    count = int(sys.argv[1]) if len(sys.argv) > 1 else 1000
    prefix = sys.argv[2] if len(sys.argv) > 2 else "soak-" + time.strftime("%Y%m%d")
    email = os.environ.get("SOAK_ADMIN_EMAIL", "avery.chen@meridian.test")
    password = os.environ.get("SOAK_ADMIN_PASS", "")
    user_password = os.environ.get("SOAK_PASSWORD", "")
    if not password or not user_password:
        print("SOAK_ADMIN_PASS and SOAK_PASSWORD are required", file=sys.stderr)
        sys.exit(2)
    started = time.time()
    results = []
    workers = 4
    sessions = []
    for _ in range(workers):
        s = new_session()
        csrf = admin_login(s, email, password)
        sessions.append((s, csrf))

    def run_batch(batch_index, batch):
        s, csrf = sessions[batch_index % workers]
        local = []
        for index in batch:
            local_email = f"{prefix}-{index:04d}@meridian.test"
            result = create_one(s, local_email, f"Soak User {index:04d}", user_password, csrf)
            local.append(result)
        return local

    batches = [list(range(i + 1, min(i + 1 + 25, count + 1))) for i in range(0, count, 25)]
    completed = 0
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
        futures = [pool.submit(run_batch, i, batch) for i, batch in enumerate(batches)]
        for future in concurrent.futures.as_completed(futures):
            batch_result = future.result()
            results.extend(batch_result)
            completed += len(batch_result)
            if completed % 100 < 25 or completed == count:
                print(f"provisioned {completed}/{count} in {time.time()-started:.1f}s", flush=True)
    results.sort(key=lambda item: item["email"])
    (OUT / "users.json").write_text(json.dumps({
        "prefix": prefix, "count": count, "users": results,
        "createdAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(started)),
        "elapsedSeconds": round(time.time() - started, 1),
    }, indent=2), encoding="utf-8")
    failed = [r for r in results if r["status"] == "failed"]
    not_ready = [r for r in results if r["status"] == "created" and not r.get("stateReady")]
    print(json.dumps({"count": count, "created": sum(1 for r in results if r["status"] == "created"),
                      "existing": sum(1 for r in results if r["status"] == "exists"),
                      "failed": len(failed), "stateNotReady": len(not_ready),
                      "elapsedSeconds": round(time.time() - started, 1)}, indent=2))
    if failed:
        print(json.dumps(failed[:10], indent=2))
        sys.exit(1)


if __name__ == "__main__":
    main()
