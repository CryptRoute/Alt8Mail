#!/usr/bin/env python3
"""End-to-end CalDAV/CardDAV smoke test.

Environment:
  GO_API            base URL, e.g. https://mail.example.com
  DAV_EMAIL         account email
  DAV_PASSWORD      account password or app password
  DAV_INSECURE=1    skip TLS verification for a staging self-signed cert

The script discovers the principal, creates/updates/deletes one event and one
contact, and verifies PROPFIND, GET and REPORT responses. It prints no
credentials and removes the resources it creates.
"""
import base64
import os
import re
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

BASE = os.environ.get("GO_API", "").rstrip("/")
EMAIL = os.environ.get("DAV_EMAIL", "")
PASSWORD = os.environ.get("DAV_PASSWORD", "")
INSECURE = os.environ.get("DAV_INSECURE", "") == "1"

if not BASE or not EMAIL or not PASSWORD:
    print("Set GO_API, DAV_EMAIL and DAV_PASSWORD.", file=sys.stderr)
    sys.exit(2)

CONTEXT = ssl._create_unverified_context() if INSECURE else ssl.create_default_context()
AUTH = "Basic " + base64.b64encode(f"{EMAIL}:{PASSWORD}".encode()).decode()
PROPFIND_BODY = b'<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:current-user-principal/><D:resourcetype/><D:displayname/></D:prop></D:propfind>'


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def request(method, path, body=None, content_type=None, expected=None, follow=True):
    url = BASE + path
    req = urllib.request.Request(url, data=body, method=method)
    req.add_header("Authorization", AUTH)
    req.add_header("User-Agent", "Meridian-DAV-Smoke/1.0")
    if content_type:
        req.add_header("Content-Type", content_type)
    handlers = [urllib.request.HTTPSHandler(context=CONTEXT)]
    if not follow:
        handlers.insert(0, NoRedirect())
    opener = urllib.request.build_opener(*handlers)
    try:
        with opener.open(req, timeout=30) as response:
            data = response.read()
            status = response.status
            headers = dict(response.headers)
    except urllib.error.HTTPError as error:
        data = error.read()
        status = error.code
        headers = dict(error.headers)
    if expected is not None and status != expected:
        raise AssertionError(f"{method} {path} -> {status}, expected {expected}: {data[:300]!r}")
    return status, data, headers


def propfind(path, depth, body=PROPFIND_BODY, expected=207):
    return request("PROPFIND", path, body, "application/xml", expected)


def main():
    print("1. discovery")
    status, body, headers = request("GET", "/.well-known/caldav", expected=None, follow=False)
    if status not in (200, 301, 302, 307, 308):
        raise AssertionError(f"caldav discovery returned {status}")
    if status in (301, 302, 307, 308) and "/dav/" not in (headers.get("Location") or ""):
        raise AssertionError(f"caldav discovery redirected to {headers.get('Location')}")
    status, body, _ = propfind("/dav/", "0")
    match = re.search(rb"<D:current-user-principal><D:href>/dav/principals/([^/]+)/", body)
    if not match:
        raise AssertionError("principal is missing from /dav/ PROPFIND")
    user_id = urllib.parse.unquote(match.group(1).decode())
    print("   principal discovered")

    print("2. calendar collection")
    status, body, _ = propfind(f"/dav/calendars/{user_id}/", "1")
    if b"<C:calendar" not in body:
        raise AssertionError("calendar collection was not advertised")

    stamp = str(int(time.time())) + "-" + uuid.uuid4().hex[:8]
    event_name = f"dav-smoke-{stamp}.ics"
    event_path = f"/dav/calendars/{user_id}/default/{event_name}"
    event_uid = f"dav-smoke-{stamp}@example.test"
    event_ics = (
        "BEGIN:VCALENDAR\r\n"
        "VERSION:2.0\r\n"
        "PRODID:-//Meridian Smoke//EN\r\n"
        "BEGIN:VEVENT\r\n"
        f"UID:{event_uid}\r\n"
        "DTSTAMP:20260101T090000Z\r\n"
        "DTSTART:20260102T090000Z\r\n"
        "DTEND:20260102T100000Z\r\n"
        "SUMMARY:DAV smoke event\r\n"
        "DESCRIPTION:Created by tools/dav-smoke.py\r\n"
        "END:VEVENT\r\n"
        "END:VCALENDAR\r\n"
    ).encode()
    try:
        print("3. event create/read/update/report/delete")
        _, _, headers = request("PUT", event_path, event_ics, "text/calendar", expected=201)
        etag = headers.get("ETag") or headers.get("Etag")
        if not etag:
            raise AssertionError("PUT did not return an ETag")
        _, body, _ = request("GET", event_path, expected=200)
        if b"DAV smoke event" not in body:
            raise AssertionError("GET did not return the event")
        updated_ics = event_ics.replace(b"SUMMARY:DAV smoke event", b"SUMMARY:DAV smoke event updated")
        _, _, headers = request("PUT", event_path, updated_ics, "text/calendar", expected=204)
        if headers.get("ETag") == etag:
            raise AssertionError("event update did not rotate the ETag")
        report = (
            '<?xml version="1.0"?><C:calendar-query xmlns:D="DAV:" '
            'xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><D:getetag/>'
            '<C:calendar-data/></D:prop><C:filter><C:comp-filter name="VCALENDAR">'
            '<C:comp-filter name="VEVENT"/></C:comp-filter></C:filter></C:calendar-query>'
        ).encode()
        status, body, _ = request("REPORT", f"/dav/calendars/{user_id}/default/", report, "application/xml", 207)
        if b"DAV smoke event updated" not in body:
            raise AssertionError("calendar REPORT did not include the event")
        request("DELETE", event_path, expected=204)
        request("GET", event_path, expected=404)
        print("   event CRUD passed")

        print("4. contact create/read/update/report/delete")
        contact_name = f"dav-smoke-{stamp}.vcf"
        contact_path = f"/dav/addressbooks/{user_id}/default/{contact_name}"
        contact_email = f"dav-smoke-{stamp}@example.test"
        card = (
            "BEGIN:VCARD\r\n"
            "VERSION:3.0\r\n"
            f"UID:dav-contact-{stamp}@example.test\r\n"
            "FN:DAV Smoke Contact\r\n"
            "N:Contact;DAV Smoke;;;\r\n"
            f"EMAIL;TYPE=INTERNET:{contact_email}\r\n"
            "ORG:Smoke Co\r\n"
            "TITLE:QA\r\n"
            "END:VCARD\r\n"
        ).encode()
        try:
            request("PUT", contact_path, card, "text/vcard", expected=201)
            _, body, _ = request("GET", contact_path, expected=200)
            if b"DAV Smoke Contact" not in body:
                raise AssertionError("GET did not return the contact")
            updated_card = card.replace(b"TITLE:QA", b"TITLE:Senior QA")
            request("PUT", contact_path, updated_card, "text/vcard", expected=204)
            report = (
                '<?xml version="1.0"?><A:addressbook-query xmlns:D="DAV:" '
                'xmlns:A="urn:ietf:params:xml:ns:carddav"><D:prop><D:getetag/>'
                '<A:address-data/></D:prop></A:addressbook-query>'
            ).encode()
            status, body, _ = request("REPORT", f"/dav/addressbooks/{user_id}/default/", report, "application/xml", 207)
            if b"DAV Smoke Contact" not in body:
                raise AssertionError("addressbook REPORT did not include the contact")
            request("DELETE", contact_path, expected=204)
            request("GET", contact_path, expected=404)
            print("   contact CRUD passed")
        finally:
            try:
                request("DELETE", contact_path, expected=None)
            except Exception:
                pass
    finally:
        try:
            request("DELETE", event_path, expected=None)
        except Exception:
            pass

    print("DAV smoke passed.")


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("DAV SMOKE FAILED:", error, file=sys.stderr)
        sys.exit(1)
