import { test, before, after, beforeEach } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { createApp } from "../server/app.mjs";
import { buildThreads } from "../public/search.mjs";
import { DEMO_EMAIL, DEMO_PASSWORD } from "../server/seed.mjs";

let tempDir;
let app;
let base;
let client;
let extraApps = [];

class ApiClient {
  constructor(root) {
    this.root = root;
    this.cookies = new Map();
    this.csrf = null;
  }

  cookieHeader() {
    return Array.from(this.cookies.entries()).map(([key, value]) => `${key}=${value}`).join("; ");
  }

  storeCookies(response) {
    const values = typeof response.headers.getSetCookie === "function"
      ? response.headers.getSetCookie()
      : [response.headers.get("set-cookie")].filter(Boolean);
    for (const header of values) {
      for (const part of String(header).split(/,(?=[^;]+?=)/)) {
        const pair = part.split(";")[0].trim();
        const index = pair.indexOf("=");
        if (index === -1) continue;
        const key = pair.slice(0, index).trim();
        const value = pair.slice(index + 1).trim();
        if (value === "") this.cookies.delete(key);
        else this.cookies.set(key, value);
      }
    }
  }

  async request(pathname, { method = "GET", body, csrf = true, headers = {}, raw = false } = {}) {
    const finalHeaders = { Accept: "application/json", ...headers };
    if (this.cookies.size) finalHeaders.Cookie = this.cookieHeader();
    if (body !== undefined) finalHeaders["Content-Type"] = "application/json";
    if (csrf && method !== "GET" && this.csrf) finalHeaders["X-CSRF-Token"] = this.csrf;
    const response = await fetch(this.root + pathname, {
      method,
      headers: finalHeaders,
      body: body === undefined ? undefined : JSON.stringify(body),
      redirect: "manual"
    });
    this.storeCookies(response);
    if (raw) return response;
    const type = response.headers.get("content-type") || "";
    const payload = type.includes("application/json") ? await response.json() : await response.text();
    return { status: response.status, headers: response.headers, body: payload };
  }

  async login(email = DEMO_EMAIL, password = DEMO_PASSWORD) {
    const result = await this.request("/api/session", {
      method: "POST",
      body: { email, password },
      csrf: false
    });
    assert.equal(result.status, 200);
    if (result.body.twoFactorRequired) {
      const verified = await this.request("/api/session/2fa", {
        method: "POST",
        body: { challengeId: result.body.challengeId, method: "totp", code: "123456" },
        csrf: false
      });
      assert.equal(verified.status, 200);
      this.csrf = verified.body.csrf;
      return verified.body;
    }
    this.csrf = result.body.csrf;
    return result.body;
  }
}

async function startExtraApp(options = {}) {
  const dataFile = path.join(tempDir, `extra-${extraApps.length}.json`);
  const instance = await createApp({ dataFile, holdMs: 400, ...options });
  const address = await instance.listen(0);
  extraApps.push(instance);
  return { app: instance, base: `http://127.0.0.1:${address.port}` };
}

before(async () => {
  tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "a8m-test-"));
  app = await createApp({
    dataFile: path.join(tempDir, "db.json"),
    holdMs: 450,
    undoTtlMs: 5000
  });
  const address = await app.listen(0);
  base = `http://127.0.0.1:${address.port}`;
  client = new ApiClient(base);
  await client.login();
});

after(async () => {
  for (const instance of extraApps) await instance.close();
  await app.close();
  await fs.rm(tempDir, { recursive: true, force: true });
});

beforeEach(async () => {
  // Keep tests independent while keeping the session valid.
  const result = await client.request("/api/dev/reset", { method: "POST", body: {} });
  assert.equal(result.status, 200);
});

function messageBySubject(bootstrap, subjectFragment) {
  return bootstrap.messages.find((message) => message.subject.includes(subjectFragment));
}

test("health, static assets, manifest, service worker and security headers", async () => {
  const health = await fetch(base + "/api/health");
  assert.equal(health.status, 200);
  assert.equal((await health.json()).ok, true);

  const index = await fetch(base + "/");
  assert.equal(index.status, 200);
  const csp = index.headers.get("content-security-policy");
  assert.match(csp, /default-src 'self'/);
  assert.match(csp, /script-src 'self'/);
  assert.equal(index.headers.get("x-content-type-options"), "nosniff");
  assert.match(index.headers.get("content-type"), /text\/html/);

  const manifest = await fetch(base + "/manifest.webmanifest");
  assert.equal(manifest.status, 200);
  assert.match(manifest.headers.get("content-type"), /application\/manifest\+json/);
  const manifestBody = await manifest.json();
  assert.equal(manifestBody.name, "Alt8Mail");
  assert.equal(manifestBody.icons.length, 3);
  assert.equal(manifestBody.shortcuts.length, 3);
  assert.equal(manifestBody.screenshots.length, 4);
  const shot = await fetch(base + manifestBody.screenshots[0].src);
  assert.equal(shot.status, 200);
  assert.match(shot.headers.get("content-type"), /image\/png/);

  const sw = await fetch(base + "/sw.js");
  assert.equal(sw.status, 200);
  assert.equal(sw.headers.get("service-worker-allowed"), "/");
  assert.match(await sw.text(), /a8m-shell/);

  const icon = await fetch(base + "/icons/icon-192.png");
  assert.equal(icon.status, 200);
  assert.match(icon.headers.get("content-type"), /image\/png/);

  const missing = await fetch(base + "/does-not-exist.txt");
  assert.equal(missing.status, 404);

  const traversal = await fetch(base + "/..%5cserver%5capp.mjs");
  assert.ok([403, 404].includes(traversal.status));
});

test("auth: bad credentials, CSRF, session, logout", async () => {
  const fresh = new ApiClient(base);
  const bad = await fresh.request("/api/session", {
    method: "POST",
    body: { email: DEMO_EMAIL, password: "wrong" },
    csrf: false
  });
  assert.equal(bad.status, 401);
  assert.equal(bad.body.error.code, "BAD_CREDENTIALS");

  const unauth = await fresh.request("/api/bootstrap");
  assert.equal(unauth.status, 401);

  await fresh.login();
  const session = await fresh.request("/api/session");
  assert.equal(session.status, 200);
  assert.equal(session.body.user.email, DEMO_EMAIL);
  assert.equal(session.body.user.password, undefined);

  const noCsrf = await fresh.request("/api/messages/actions", {
    method: "POST",
    body: { ids: ["m1"], action: "archive" },
    csrf: false
  });
  assert.equal(noCsrf.status, 403);
  assert.equal(noCsrf.body.error.code, "CSRF");

  const logout = await fresh.request("/api/session", { method: "DELETE" });
  assert.equal(logout.status, 200);
  const afterLogout = await fresh.request("/api/session");
  assert.equal(afterLogout.status, 401);
});

test("two-factor: password challenge, passkey, recovery and one-time codes", async () => {
  const passkeyClient = new ApiClient(base);
  const challenge = await passkeyClient.request("/api/session", {
    method: "POST",
    body: { email: DEMO_EMAIL, password: DEMO_PASSWORD },
    csrf: false
  });
  assert.equal(challenge.status, 200);
  assert.equal(challenge.body.twoFactorRequired, true);
  assert.ok(challenge.body.challengeId);
  assert.equal(challenge.body.methods.passkey, true);
  assert.equal(challenge.body.methods.authenticator, true);
  assert.equal(challenge.body.methods.recovery, true);
  assert.equal(challenge.body.passkeys.length, 2);
  assert.equal(challenge.body.demoCode, "123456");
  assert.equal(passkeyClient.cookies.size, 0);
  assert.equal((await passkeyClient.request("/api/bootstrap")).status, 401);

  const wrong = await passkeyClient.request("/api/session/2fa", {
    method: "POST",
    body: { challengeId: challenge.body.challengeId, method: "totp", code: "000000" },
    csrf: false
  });
  assert.equal(wrong.status, 401);
  assert.equal(wrong.body.error.code, "TWO_FACTOR_INVALID");
  assert.equal(wrong.body.error.details.attemptsRemaining, 4);

  const passkey = await passkeyClient.request("/api/session/2fa", {
    method: "POST",
    body: {
      challengeId: challenge.body.challengeId,
      method: "passkey",
      passkeyId: "pk_windows_hello",
      trustDevice: true
    },
    csrf: false
  });
  assert.equal(passkey.status, 200);
  assert.equal(passkey.body.twoFactorMethod, "passkey");
  assert.equal(passkey.body.trustedDevice, true);
  assert.ok(passkey.body.csrf);
  passkeyClient.csrf = passkey.body.csrf;
  const session = await passkeyClient.request("/api/bootstrap");
  assert.equal(session.status, 200);
  assert.deepEqual(session.body.user.security.twoFactor.methods, ["passkey", "authenticator", "recovery"]);
  assert.equal(session.body.user.security.twoFactor.recoveryCodesRemaining, 4);

  const recoveryClient = new ApiClient(base);
  const recoveryChallenge = await recoveryClient.request("/api/session", {
    method: "POST",
    body: { email: DEMO_EMAIL, password: DEMO_PASSWORD },
    csrf: false
  });
  const recovery = await recoveryClient.request("/api/session/2fa", {
    method: "POST",
    body: { challengeId: recoveryChallenge.body.challengeId, method: "recovery", code: "a8m-7f2k-9q4a" },
    csrf: false
  });
  assert.equal(recovery.status, 200);
  assert.equal(recovery.body.twoFactorMethod, "recovery");
  recoveryClient.csrf = recovery.body.csrf;
  const afterRecovery = await recoveryClient.request("/api/bootstrap");
  assert.equal(afterRecovery.body.user.security.twoFactor.recoveryCodesRemaining, 3);

  const reuseClient = new ApiClient(base);
  const reuseChallenge = await reuseClient.request("/api/session", {
    method: "POST",
    body: { email: DEMO_EMAIL, password: DEMO_PASSWORD },
    csrf: false
  });
  const reused = await reuseClient.request("/api/session/2fa", {
    method: "POST",
    body: { challengeId: reuseChallenge.body.challengeId, method: "recovery", code: "A8M-7F2K-9Q4A" },
    csrf: false
  });
  assert.equal(reused.status, 401);

  const audit = (await client.request("/api/audit?category=login")).body;
  assert.ok(audit.entries.some((entry) => entry.action === "auth.challenge"));
  assert.ok(audit.entries.some((entry) => entry.action === "auth.2fa" && entry.detail.includes("passkey")));
  const bootstrap = (await client.request("/api/bootstrap")).body;
  assert.equal(bootstrap.user.security.twoFactor.totpSecret, undefined);
  assert.ok(bootstrap.user.security.twoFactor.passkeys.every((item) => item.id && item.name && item.lastUsedAt));
});

test("regional preferences persist, validate and drive the account payload", async () => {
  const initial = await client.request("/api/preferences");
  assert.equal(initial.status, 200);
  assert.equal(initial.body.preferences.language, "en-US");
  assert.equal(initial.body.preferences.timeZone, "America/Los_Angeles");

  const patch = await client.request("/api/preferences", {
    method: "PATCH",
    body: {
      language: "de-DE",
      timeFormat: "24h",
      dateFormat: "dmy",
      timeZone: "Europe/Berlin",
      weekStart: "sunday",
      workStart: "08:30",
      workEnd: "18:00",
      workDays: [1, 2, 3, 4, 5],
      autoAdvance: true,
      signature: "Avery Chen\nVP Product · Alt8Mail Berlin"
    }
  });
  assert.equal(patch.status, 200);
  assert.equal(patch.body.preferences.timeZone, "Europe/Berlin");
  assert.equal(patch.body.preferences.timeFormat, "24h");
  assert.ok(patch.body.changed.includes("timeZone"));
  const bootstrap = (await client.request("/api/bootstrap")).body;
  assert.equal(bootstrap.user.preferences.language, "de-DE");
  assert.equal(bootstrap.user.preferences.autoAdvance, true);

  const badZone = await client.request("/api/preferences", {
    method: "PATCH",
    body: { timeZone: "Mars/Olympus" }
  });
  assert.equal(badZone.status, 400);
  assert.equal(badZone.body.error.code, "PREFERENCES");
  const badHours = await client.request("/api/preferences", {
    method: "PATCH",
    body: { workStart: "18:00", workEnd: "09:00" }
  });
  assert.equal(badHours.status, 400);
});

test("tasks: create, filter, complete, link and delete an enterprise task", async () => {
  const initial = await client.request("/api/tasks");
  assert.equal(initial.status, 200);
  assert.equal(initial.body.tasks.length, 8);

  const created = await client.request("/api/tasks", {
    method: "POST",
    body: {
      title: "Prepare board follow-up",
      notes: "Summarise the Q4 roadmap decisions for the board packet.",
      due: "2026-11-05",
      dueTime: "16:30",
      priority: "high",
      tags: ["Board", "Product"],
      assigneeEmail: "elena.vasquez@a8m.example",
      linkedThreadId: "t-q4-roadmap",
      linkedSubject: "Q4 roadmap review"
    }
  });
  assert.equal(created.status, 201);
  assert.equal(created.body.task.status, "open");
  assert.equal(created.body.task.assignee.email, "elena.vasquez@a8m.example");
  assert.deepEqual(created.body.task.tags, ["Board", "Product"]);

  const completed = await client.request(`/api/tasks/${created.body.task.id}`, {
    method: "PATCH",
    body: { status: "done" }
  });
  assert.equal(completed.status, 200);
  assert.equal(completed.body.task.status, "done");
  assert.ok(completed.body.task.completedAt);

  const tasks = (await client.request("/api/tasks")).body;
  assert.equal(tasks.tasks.length, 9);
  assert.equal(tasks.tasks.find((task) => task.id === created.body.task.id).status, "done");

  const invalid = await client.request("/api/tasks", { method: "POST", body: { title: "" } });
  assert.equal(invalid.status, 400);
  assert.equal(invalid.body.error.code, "TASK_TITLE");
  const badAssignee = await client.request("/api/tasks", {
    method: "POST",
    body: { title: "Outside assignee", assigneeEmail: "outsider@example.net" }
  });
  assert.equal(badAssignee.status, 400);

  const removed = await client.request(`/api/tasks/${created.body.task.id}`, { method: "DELETE" });
  assert.equal(removed.status, 200);
  const audit = (await client.request("/api/audit?category=task")).body;
  assert.ok(audit.entries.some((entry) => entry.action === "task.completed"));
});

test("storage connectors and enterprise files: connect, share, star and remove", async () => {
  const storage = await client.request("/api/storage");
  assert.equal(storage.status, 200);
  assert.equal(storage.body.providers.length, 5);
  assert.equal(storage.body.providers.filter((provider) => provider.connected).length, 2);
  assert.ok(storage.body.providers.some((provider) => provider.id === "s3" && provider.kind === "s3"));

  const connected = await client.request("/api/storage/dropbox/connect", { method: "POST", body: {} });
  assert.equal(connected.status, 200);
  assert.equal(connected.body.provider.connected, true);
  assert.equal(connected.body.files.length, 2);
  const again = await client.request("/api/storage/dropbox/connect", { method: "POST", body: {} });
  assert.equal(again.body.files.length, 2);

  const files = await client.request("/api/files");
  assert.equal(files.status, 200);
  assert.equal(files.body.files.length, 14);
  assert.ok(files.body.files.every((file) => file.id && file.providerId && file.sizeBytes > 0));

  const patched = await client.request("/api/files/file_roadmap", {
    method: "PATCH",
    body: { starred: false, description: "Board-ready roadmap export." }
  });
  assert.equal(patched.status, 200);
  assert.equal(patched.body.file.starred, false);
  assert.equal(patched.body.file.description, "Board-ready roadmap export.");

  const shared = await client.request("/api/files/file_roadmap/share", {
    method: "POST",
    body: { access: "edit", expiresDays: 14 }
  });
  assert.equal(shared.status, 200);
  assert.match(shared.body.share.url, /^https:\/\/files\.a8m\.example\/s\//);
  assert.equal(shared.body.share.access, "edit");
  assert.equal(shared.body.share.expiresDays, 14);

  const removed = await client.request("/api/files/file_dropbox_recap", { method: "DELETE" });
  assert.equal(removed.status, 200);
  assert.equal((await client.request("/api/files")).body.files.length, 13);

  const disconnected = await client.request("/api/storage/dropbox/disconnect", { method: "POST", body: {} });
  assert.equal(disconnected.body.provider.connected, false);
  const audit = (await client.request("/api/audit?category=files")).body;
  assert.ok(audit.entries.some((entry) => entry.action === "file.shared"));
  assert.ok(audit.entries.some((entry) => entry.action === "storage.connected"));
});

test("attachments: upload PDF and text, inline preview, download disposition and file shelf", async () => {
  const pdfBytes = Buffer.from("%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\n%%EOF\n");
  const uploaded = await client.request("/api/attachments", {
    method: "POST",
    body: { name: "board-preview.pdf", mime: "application/pdf", dataBase64: pdfBytes.toString("base64") }
  });
  assert.equal(uploaded.status, 201);
  const attachment = uploaded.body.attachment;
  assert.equal(attachment.previewable, true);
  assert.equal(attachment.mime, "application/pdf");
  assert.equal(attachment.sizeBytes, pdfBytes.length);

  const content = await client.request(attachment.contentUrl, { raw: true });
  assert.equal(content.status, 200);
  assert.match(content.headers.get("content-type"), /application\/pdf/);
  assert.match(content.headers.get("content-disposition"), /^inline;/);
  const bytes = Buffer.from(await content.arrayBuffer());
  assert.equal(bytes.subarray(0, 5).toString("ascii"), "%PDF-");

  const text = await client.request("/api/attachments", {
    method: "POST",
    body: { name: "handoff-notes.txt", mime: "text/plain", dataBase64: Buffer.from("Handoff notes").toString("base64") }
  });
  assert.equal(text.status, 201);
  assert.equal(text.body.attachment.previewable, false);
  const download = await client.request(text.body.attachment.contentUrl, { raw: true });
  assert.match(download.headers.get("content-disposition"), /^attachment;/);

  const empty = await client.request("/api/attachments", { method: "POST", body: { name: "empty.pdf", dataBase64: "" } });
  assert.equal(empty.status, 400);

  const filed = await client.request("/api/files", {
    method: "POST",
    body: { providerId: "a8m", attachmentId: attachment.id, description: "Board preview file." }
  });
  assert.equal(filed.status, 201);
  assert.equal(filed.body.file.name, "board-preview.pdf");
  assert.equal(filed.body.file.providerId, "a8m");
  assert.equal(filed.body.file.attachmentId, attachment.id);
});

test("scheduled send: future-only validation, visible queue and cancel back to draft", async () => {
  const future = new Date(Date.now() + 60 * 60 * 1000).toISOString();
  const scheduled = await client.request("/api/send", {
    method: "POST",
    body: {
      fromEmail: DEMO_EMAIL,
      to: "elena.vasquez@a8m.example",
      subject: "Scheduled board note",
      bodyText: "This leaves tomorrow morning.",
      scheduledFor: future
    }
  });
  assert.equal(scheduled.status, 200);
  assert.equal(scheduled.body.scheduled, true);
  assert.equal(scheduled.body.queued.scheduled, true);
  assert.equal(scheduled.body.queued.canCancel, true);
  assert.equal(scheduled.body.undoUntil, null);

  const bootstrap = (await client.request("/api/bootstrap")).body;
  const pending = bootstrap.pendingSends.find((item) => item.subject === "Scheduled board note");
  assert.ok(pending);
  assert.equal(pending.scheduled, true);
  assert.ok(Date.parse(pending.sendAt) > Date.now());

  const past = await client.request("/api/send", {
    method: "POST",
    body: {
      fromEmail: DEMO_EMAIL,
      to: "elena.vasquez@a8m.example",
      subject: "Too late",
      bodyText: "Past date",
      scheduledFor: new Date(Date.now() - 60 * 1000).toISOString()
    }
  });
  assert.equal(past.status, 400);
  assert.equal(past.body.error.code, "SCHEDULE_INVALID");

  const cancelled = await client.request(`/api/outbox/${scheduled.body.queued.id}/cancel`, {
    method: "POST",
    body: {}
  });
  assert.equal(cancelled.status, 200);
  assert.equal(cancelled.body.draft.folder, "drafts");
  assert.equal(cancelled.body.draft.subject, "Scheduled board note");
  const after = (await client.request("/api/bootstrap")).body;
  assert.equal(after.pendingSends.some((item) => item.subject === "Scheduled board note"), false);
  const audit = (await client.request("/api/audit?category=send")).body;
  assert.ok(audit.entries.some((entry) => entry.action === "send.scheduled"));
  assert.ok(audit.entries.some((entry) => entry.action === "send.cancelled"));
});

test("scheduled messages can be edited and sent immediately from the outbox", async () => {
  const firstTime = new Date(Date.now() + 2 * 60 * 60 * 1000).toISOString();
  const created = await client.request("/api/send", {
    method: "POST",
    body: {
      fromEmail: DEMO_EMAIL,
      to: "elena.vasquez@a8m.example",
      subject: "Schedule manager test",
      bodyText: "First version of the scheduled message.",
      scheduledFor: firstTime
    }
  });
  assert.equal(created.status, 200);
  assert.equal(created.body.scheduled, true);
  const id = created.body.queued.id;

  const updatedTime = new Date(Date.now() + 4 * 60 * 60 * 1000).toISOString();
  const patch = await client.request(`/api/outbox/${id}`, {
    method: "PATCH",
    body: {
      to: "elena.vasquez@a8m.example, nora.patel@a8m.example",
      cc: "marcus.webb@a8m.example",
      subject: "Schedule manager test — revised",
      bodyText: "Second version, rescheduled with the manager.",
      sendAt: updatedTime
    }
  });
  assert.equal(patch.status, 200);
  assert.equal(patch.body.item.subject, "Schedule manager test — revised");
  assert.equal(patch.body.item.to.length, 2);
  assert.equal(patch.body.item.cc.length, 1);
  assert.equal(patch.body.item.sendAt, updatedTime);
  assert.ok(patch.body.changed.includes("schedule"));
  assert.equal(patch.body.pending.length, 1);

  const bootstrap = (await client.request("/api/bootstrap")).body;
  const pending = bootstrap.pendingSends.find((item) => item.id === id);
  assert.equal(pending.subject, "Schedule manager test — revised");

  const badTime = await client.request(`/api/outbox/${id}`, {
    method: "PATCH",
    body: { sendAt: new Date(Date.now() - 60 * 1000).toISOString() }
  });
  assert.equal(badTime.status, 400);
  assert.equal(badTime.body.error.code, "SCHEDULE_INVALID");
  const badRecipient = await client.request(`/api/outbox/${id}`, {
    method: "PATCH",
    body: { to: "not-an-address" }
  });
  assert.equal(badRecipient.status, 400);
  assert.equal(badRecipient.body.error.code, "BAD_RECIPIENT");

  const beforeSend = (await client.request("/api/outbound")).body;
  const sendNow = await client.request(`/api/outbox/${id}/send-now`, { method: "POST", body: {} });
  assert.equal(sendNow.status, 200);
  assert.ok(sendNow.body.sentMessageId);
  assert.equal(sendNow.body.pending.length, 0);
  assert.equal(sendNow.body.outbound.used, beforeSend.used + 1);
  assert.equal(sendNow.body.outbound.queued, 0);
  const after = (await client.request("/api/bootstrap")).body;
  assert.equal(after.pendingSends.length, 0);
  const sent = after.messages.find((message) => message.id === sendNow.body.sentMessageId);
  assert.ok(sent);
  assert.equal(sent.folder, "sent");
  assert.equal(sent.subject, "Schedule manager test — revised");
  assert.equal(sent.cc.length, 1);

  const again = await client.request(`/api/outbox/${id}/send-now`, { method: "POST", body: {} });
  assert.equal(again.status, 409);
  const audit = (await client.request("/api/audit?category=send")).body;
  assert.ok(audit.entries.some((entry) => entry.action === "send.rescheduled"));
  assert.ok(audit.entries.some((entry) => entry.action === "send.sent_now"));
  assert.ok(audit.entries.some((entry) => entry.action === "send.committed" && entry.target === "Schedule manager test — revised"));
});

test("reading-pane spam, report and follow-up flag controls are audited and undoable", async () => {
  const bootstrap = (await client.request("/api/bootstrap")).body;
  const target = bootstrap.messages.find((message) => message.folder === "inbox" && !message.draft);
  assert.ok(target);

  const flagged = await client.request("/api/messages/actions", {
    method: "POST",
    body: { ids: [target.id], action: "flag" }
  });
  assert.equal(flagged.status, 200);
  let after = (await client.request("/api/bootstrap")).body;
  assert.equal(after.messages.find((message) => message.id === target.id).flagged, true);
  const unflagged = await client.request(`/api/messages/${target.id}`, { method: "PATCH", body: { flagged: false } });
  assert.equal(unflagged.status, 200);
  assert.equal(unflagged.body.message.flagged, false);

  const spam = await client.request("/api/messages/actions", {
    method: "POST",
    body: { ids: [target.id], action: "spam" }
  });
  assert.equal(spam.status, 200);
  after = (await client.request("/api/bootstrap")).body;
  assert.equal(after.messages.find((message) => message.id === target.id).folder, "spam");
  assert.equal(after.messages.find((message) => message.id === target.id).unread, false);
  const undoSpam = await client.request(`/api/undo/${spam.body.undoToken}`, { method: "POST", body: {} });
  assert.equal(undoSpam.status, 200);
  after = (await client.request("/api/bootstrap")).body;
  assert.equal(after.messages.find((message) => message.id === target.id).folder, "inbox");

  const notSpam = await client.request("/api/messages/actions", {
    method: "POST",
    body: { ids: ["sp1"], action: "notSpam" }
  });
  assert.equal(notSpam.status, 200);
  after = (await client.request("/api/bootstrap")).body;
  assert.equal(after.messages.find((message) => message.id === "sp1").folder, "inbox");

  const reported = await client.request("/api/messages/actions", {
    method: "POST",
    body: { ids: [target.id], action: "report", reason: "phishing", note: "Credential harvest with a lookalike domain." }
  });
  assert.equal(reported.status, 200);
  after = (await client.request("/api/bootstrap")).body;
  const reportedMessage = after.messages.find((message) => message.id === target.id);
  assert.equal(reportedMessage.folder, "spam");
  assert.equal(reportedMessage.flagged, true);
  assert.equal(reportedMessage.spamReport.reason, "phishing");
  assert.match(reportedMessage.spamReport.at, /^\d{4}-\d{2}-\d{2}T/);

  const audit = (await client.request("/api/audit?category=message")).body;
  assert.ok(audit.entries.some((entry) => entry.action === "message.flag"));
  assert.ok(audit.entries.some((entry) => entry.action === "message.spam"));
  assert.ok(audit.entries.some((entry) => entry.action === "message.notSpam"));
  const reportAudit = audit.entries.find((entry) => entry.action === "message.report");
  assert.ok(reportAudit);
  assert.match(reportAudit.detail, /phishing/);
  assert.match(reportAudit.detail, /analyst note/);
});

test("bootstrap contains mailbox, aliases, quota, senders and no secret", async () => {
  const result = await client.request("/api/bootstrap");
  assert.equal(result.status, 200);
  const data = result.body;
  assert.equal(data.user.email, DEMO_EMAIL);
  assert.equal(data.aliases.length, 4);
  assert.equal(data.aliases.filter((alias) => alias.canSend).length, 3);
  assert.equal(data.folders.length, 7);
  assert.ok(data.messages.length >= 29);
  assert.ok(data.quota.limitBytes > data.quota.usedBytes);
  assert.equal(data.outbound.limit, 200);
  assert.equal(data.outbound.used, 37);
  assert.equal(data.sieve.rules.length, 3);
  assert.match(data.sieve.script, /fileinto/);
  assert.ok(!JSON.stringify(data).includes("scrypt") && !JSON.stringify(data).includes("\"password\""));
  assert.ok(data.messages.every((message) => typeof message.trusted === "boolean"));
  const external = data.messages.find((message) => message.external);
  assert.ok(external);
  assert.ok(external.auth);
  assert.ok(["pass", "fail", "softfail", "none"].includes(external.auth.spf));
});

test("recipients auto-save to contacts exactly once", async () => {
  const first = await client.request("/api/contacts/auto", {
    method: "POST",
    body: { email: "external.person@example.com", name: "External Person" }
  });
  assert.equal(first.status, 200);
  assert.equal(first.body.contact.email, "external.person@example.com");
  assert.equal(first.body.contact.name, "External Person");
  assert.equal(first.body.contact.source, "auto");
  const second = await client.request("/api/contacts/auto", {
    method: "POST",
    body: { email: "EXTERNAL.PERSON@example.com", name: "Ignored replacement" }
  });
  assert.equal(second.status, 200);
  assert.equal(second.body.contact.name, "External Person");
  const bootstrap = await client.request("/api/bootstrap");
  const matches = bootstrap.body.people.filter((person) => person.email === "external.person@example.com");
  assert.equal(matches.length, 1);
  assert.equal(matches[0].source, "auto");
});

test("thread grouping: inbox conversations have full message histories", async () => {
  const bootstrap = (await client.request("/api/bootstrap")).body;
  const inbox = bootstrap.messages.filter((message) => message.folder === "inbox");
  const threads = buildThreads(inbox, bootstrap.messages);
  assert.equal(threads.length, 12);
  const roadmap = threads.find((thread) => thread.subject.includes("Q4 roadmap"));
  assert.equal(roadmap.count, 3);
  assert.equal(roadmap.messages.length, 3);
  assert.ok(roadmap.messages.every((message) => message.folder === "inbox" || message.folder === "sent"));
  const brightline = threads.find((thread) => thread.subject.includes("Brightline"));
  assert.equal(brightline.count, 3);
  assert.ok(brightline.messages.some((message) => message.folder === "sent"));
});

test("search operators are instant and match indexed bodies and attachment names", async () => {
  const cases = [
    ["from:elena", 2],
    ["has:attachment", 8],
    ["filename:invoice", 1],
    ["attachment:first-run-flows", 1],
    ["subject:review", 6],
    ["is:unread", 11],
    ["in:archive has:attachment", 2],
    ["from:elena has:attachment", 1],
    ["before:2000-01-01", 0],
    ["after:2000-01-01", 29],
    ["first-run-flows", 1],
    ["\"Q4 roadmap\"", 3]
  ];
  for (const [query, expected] of cases) {
    const result = await client.request(`/api/search?q=${encodeURIComponent(query)}`);
    assert.equal(result.status, 200, query);
    assert.equal(result.body.count, expected, `${query} expected ${expected}, got ${result.body.count}`);
  }
  const parsed = (await client.request("/api/search?q=" + encodeURIComponent("from:elena is:unread before:2999-01-01"))).body;
  assert.ok(parsed.messages.every((message) => message.unread));
  assert.deepEqual(parsed.parsed.from, ["elena"]);

  const badDate = (await client.request("/api/search?q=" + encodeURIComponent("before:not-a-date"))).body;
  assert.ok(badDate.parsed.errors.length > 0);

  const tooLong = await client.request("/api/search?q=" + "x".repeat(501));
  assert.equal(tooLong.status, 400);
});

test("read and star mutations update the index and persist", async () => {
  const before = (await client.request("/api/bootstrap")).body;
  const message = before.messages.find((item) => item.folder === "inbox" && item.unread);
  const patch = await client.request(`/api/messages/${message.id}`, {
    method: "PATCH",
    body: { unread: false, starred: true }
  });
  assert.equal(patch.status, 200);
  assert.equal(patch.body.message.unread, false);
  assert.equal(patch.body.message.starred, true);

  const after = (await client.request("/api/bootstrap")).body;
  const updated = after.messages.find((item) => item.id === message.id);
  assert.equal(updated.unread, false);
  assert.equal(updated.starred, true);
  const unreadSearch = await client.request(`/api/search?q=${encodeURIComponent("subject:" + message.subject.slice(0, 12) + " is:unread")}`);
  assert.ok(unreadSearch.body.messages.every((item) => item.id !== message.id));
});

test("bulk archive/move/delete support undo and write audit entries", async () => {
  const bootstrap = (await client.request("/api/bootstrap")).body;
  const inbox = bootstrap.messages.filter((message) => message.folder === "inbox");
  const first = inbox[0];
  const second = inbox[1];
  const archive = await client.request("/api/messages/actions", {
    method: "POST",
    body: { ids: [first.id, second.id], action: "archive" }
  });
  assert.equal(archive.status, 200);
  assert.equal(archive.body.affected, 2);
  assert.ok(archive.body.undoToken);

  let after = (await client.request("/api/bootstrap")).body;
  assert.equal(after.messages.find((message) => message.id === first.id).folder, "archive");
  assert.equal(after.messages.find((message) => message.id === second.id).folder, "archive");

  const undo = await client.request(`/api/undo/${archive.body.undoToken}`, { method: "POST", body: {} });
  assert.equal(undo.status, 200);
  assert.equal(undo.body.restored, 2);
  after = (await client.request("/api/bootstrap")).body;
  assert.equal(after.messages.find((message) => message.id === first.id).folder, "inbox");
  assert.equal(after.messages.find((message) => message.id === second.id).folder, "inbox");

  const move = await client.request("/api/messages/actions", {
    method: "POST",
    body: { ids: [first.id], action: "move", folder: "spam" }
  });
  assert.equal(move.status, 200);
  after = (await client.request("/api/bootstrap")).body;
  assert.equal(after.messages.find((message) => message.id === first.id).folder, "spam");

  const del = await client.request("/api/messages/actions", {
    method: "POST",
    body: { ids: [first.id], action: "delete" }
  });
  assert.equal(del.status, 200);
  after = (await client.request("/api/bootstrap")).body;
  assert.equal(after.messages.find((message) => message.id === first.id).folder, "trash");

  const permanent = await client.request("/api/messages/actions", {
    method: "POST",
    body: { ids: [first.id], action: "delete" }
  });
  assert.equal(permanent.status, 200);
  assert.equal(permanent.body.permanentlyDeleted, 1);
  after = (await client.request("/api/bootstrap")).body;
  assert.equal(after.messages.some((message) => message.id === first.id), false);

  const restore = await client.request(`/api/undo/${permanent.body.undoToken}`, { method: "POST", body: {} });
  assert.equal(restore.status, 200);
  after = (await client.request("/api/bootstrap")).body;
  assert.equal(after.messages.find((message) => message.id === first.id).folder, "trash");

  const audit = (await client.request("/api/audit?category=message")).body;
  assert.ok(audit.entries.some((entry) => entry.action === "message.archive"));
  assert.ok(audit.entries.some((entry) => entry.action === "message.delete"));
  assert.ok(audit.entries.some((entry) => entry.action === "message.undo"));
});

test("quota is live: permanent delete reduces usage, move does not", async () => {
  const before = (await client.request("/api/quota")).body;
  const bootstrap = (await client.request("/api/bootstrap")).body;
  const inboxMessage = bootstrap.messages.find((message) => message.folder === "inbox");
  await client.request("/api/messages/actions", {
    method: "POST",
    body: { ids: [inboxMessage.id], action: "move", folder: "trash" }
  });
  const afterMove = (await client.request("/api/quota")).body;
  assert.equal(afterMove.usedBytes, before.usedBytes);

  const trash = (await client.request("/api/bootstrap")).body.messages.find((message) => message.folder === "trash");
  const dynamicBefore = before.dynamicBytes;
  const deletion = await client.request("/api/messages/actions", {
    method: "POST",
    body: { ids: [trash.id], action: "delete" }
  });
  assert.equal(deletion.status, 200);
  assert.equal(deletion.body.permanentlyDeleted, 1);
  const afterDelete = (await client.request("/api/quota")).body;
  assert.ok(afterDelete.usedBytes < before.usedBytes || afterDelete.dynamicBytes < dynamicBefore);
  await client.request(`/api/undo/${deletion.body.undoToken}`, { method: "POST", body: {} });
  const restored = (await client.request("/api/quota")).body;
  assert.ok(restored.usedBytes > afterDelete.usedBytes);
});

test("server-backed drafts: create, autosave, restore and discard", async () => {
  const draftId = "draft_test_survives";
  const put = await client.request(`/api/drafts/${draftId}`, {
    method: "PUT",
    body: {
      toText: "elena.vasquez@a8m.example",
      subject: "Draft that survives",
      bodyText: "First version",
      fromEmail: DEMO_EMAIL,
      attachments: [{ name: "notes.txt", size: "12 B", sizeBytes: 12, type: "TXT" }],
      windowMode: "floating"
    }
  });
  assert.equal(put.status, 200);
  assert.equal(put.body.draft.folder, "drafts");
  assert.equal(put.body.draft.to[0].email, "elena.vasquez@a8m.example");

  const update = await client.request(`/api/drafts/${draftId}`, {
    method: "PUT",
    body: { toText: "elena.vasquez@a8m.example", subject: "Draft that survives", bodyText: "Second version", fromEmail: DEMO_EMAIL }
  });
  assert.equal(update.status, 200);
  assert.equal(update.body.draft.bodyText, "Second version");
  assert.equal(update.body.draft.id, draftId);

  const bootstrap = (await client.request("/api/bootstrap")).body;
  const draft = bootstrap.messages.find((message) => message.id === draftId);
  assert.ok(draft);
  assert.equal(draft.folder, "drafts");
  const search = (await client.request("/api/search?q=" + encodeURIComponent("notes.txt"))).body;
  assert.ok(search.messages.some((message) => message.id === draftId));

  const wrongFrom = await client.request(`/api/drafts/${draftId}`, {
    method: "PUT",
    body: { toText: "a@b.c", fromEmail: "ceo@a8m.example" }
  });
  assert.equal(wrongFrom.status, 403);

  const discard = await client.request(`/api/drafts/${draftId}`, { method: "DELETE" });
  assert.equal(discard.status, 200);
  const after = (await client.request("/api/bootstrap")).body;
  assert.equal(after.messages.some((message) => message.id === draftId), false);
});

test("undo send queue: cancel restores draft, commit creates Sent and updates outbound", async () => {
  const draftId = "draft_send_test";
  await client.request(`/api/drafts/${draftId}`, {
    method: "PUT",
    body: { toText: "elena.vasquez@a8m.example", subject: "Queued message", bodyText: "Hello from the queue", fromEmail: DEMO_EMAIL }
  });
  const sent = await client.request("/api/send", {
    method: "POST",
    body: {
      draftId,
      fromEmail: DEMO_EMAIL,
      to: "elena.vasquez@a8m.example",
      subject: "Queued message",
      bodyText: "Hello from the queue",
      holdSeconds: 5
    }
  });
  assert.equal(sent.status, 200);
  assert.ok(sent.body.queued.canCancel);
  assert.equal(sent.body.holdMs, 450);
  assert.equal(sent.body.outbound.queued, 1);
  let bootstrap = (await client.request("/api/bootstrap")).body;
  assert.equal(bootstrap.messages.some((message) => message.id === draftId), false);
  assert.equal(bootstrap.pendingSends.length, 1);

  const cancel = await client.request(`/api/outbox/${sent.body.queued.id}/cancel`, { method: "POST", body: {} });
  assert.equal(cancel.status, 200);
  assert.ok(cancel.body.draft);
  bootstrap = (await client.request("/api/bootstrap")).body;
  assert.equal(bootstrap.pendingSends.length, 0);
  assert.ok(bootstrap.messages.some((message) => message.id === cancel.body.draft.id && message.folder === "drafts"));
  const audit = (await client.request("/api/audit?category=send")).body;
  assert.ok(audit.entries.some((entry) => entry.action === "send.cancelled"));

  const sent2 = await client.request("/api/send", {
    method: "POST",
    body: {
      fromEmail: "product@a8m.example",
      to: "elena.vasquez@a8m.example",
      subject: "Committed message",
      bodyText: "This one goes out",
      holdSeconds: 5
    }
  });
  assert.equal(sent2.status, 200);
  assert.equal(sent2.body.queued.from.email, "product@a8m.example");
  await new Promise((resolve) => setTimeout(resolve, 900));
  bootstrap = (await client.request("/api/bootstrap")).body;
  const committed = bootstrap.messages.find((message) => message.folder === "sent" && message.subject === "Committed message");
  assert.ok(committed);
  assert.equal(committed.snippet, "This one goes out");
  assert.equal(bootstrap.pendingSends.length, 0);
  assert.equal(bootstrap.outbound.used, 38);
  assert.equal(bootstrap.outbound.queued, 0);
  const lateCancel = await client.request(`/api/outbox/${sent2.body.queued.id}/cancel`, { method: "POST", body: {} });
  assert.equal(lateCancel.status, 409);
  const audit2 = (await client.request("/api/audit?category=send")).body;
  assert.ok(audit2.entries.some((entry) => entry.action === "send.committed" && entry.target === "Committed message"));
});

test("outbound caps are enforced including queued messages", async () => {
  const extra = await startExtraApp({ outboundLimit: 38, holdMs: 5000 });
  const cappedClient = new ApiClient(extra.base);
  await cappedClient.login();
  const first = await cappedClient.request("/api/send", {
    method: "POST",
    body: { fromEmail: DEMO_EMAIL, to: "elena.vasquez@a8m.example", subject: "Cap one", bodyText: "one" }
  });
  assert.equal(first.status, 200);
  assert.equal(first.body.outbound.usedWithQueued, 38);
  const second = await cappedClient.request("/api/send", {
    method: "POST",
    body: { fromEmail: DEMO_EMAIL, to: "elena.vasquez@a8m.example", subject: "Cap two", bodyText: "two" }
  });
  assert.equal(second.status, 429);
  assert.equal(second.body.error.code, "OUTBOUND_CAP");
  await cappedClient.request(`/api/outbox/${first.body.queued.id}/cancel`, { method: "POST", body: {} });
});

test("sieve: validation, generated script, quoting, vacation and audit", async () => {
  const current = (await client.request("/api/sieve")).body;
  assert.ok(current.rules.length >= 2);
  assert.match(current.script, /require/);

  const rules = [
    {
      id: "rule_api_test",
      name: "Quoted rule",
      enabled: true,
      match: "all",
      conditions: [
        { field: "from", operator: "contains", value: "jordan.blake" },
        { field: "subject", operator: "contains", value: "He said \"hi\"" }
      ],
      action: { type: "star", value: "" }
    },
    {
      id: "rule_api_move",
      name: "Move large invoices",
      enabled: true,
      match: "all",
      conditions: [{ field: "size", operator: "over", value: "500" }],
      action: { type: "move", value: "archive" }
    }
  ];
  const vacation = {
    enabled: true,
    subject: "Away",
    message: "Back soon",
    days: 3,
    start: "2026-11-01",
    end: "2026-11-10"
  };
  const save = await client.request("/api/sieve", { method: "PUT", body: { rules, vacation } });
  assert.equal(save.status, 200);
  assert.equal(save.body.revision, current.revision + 1);
  assert.equal(save.body.rules.length, 2);
  assert.match(save.body.script, /Quoted rule/);
  assert.match(save.body.script, /\\"hi\\"/);
  assert.match(save.body.script, /vacation :days 3/);
  assert.match(save.body.script, /currentdate/);
  assert.match(save.body.script, /size :over 500K/);
  assert.match(save.body.script, /require [^;]*"date"[^;]*"vacation"/);

  const invalid = await client.request("/api/sieve", {
    method: "PUT",
    body: {
      rules: [{
        id: "bad_rule",
        name: "",
        match: "all",
        conditions: [{ field: "unknown", operator: "contains", value: "" }],
        action: { type: "move", value: "../etc" }
      }],
      vacation
    }
  });
  assert.equal(invalid.status, 400);
  assert.ok(invalid.body.error.details.errors.length >= 3);

  const audit = (await client.request("/api/audit?category=rule")).body;
  assert.ok(audit.entries.some((entry) => entry.action === "rule.saved"));
});

test("sender trust: auth panel, mark trusted and unmark", async () => {
  const bootstrap = (await client.request("/api/bootstrap")).body;
  const external = bootstrap.messages.find((message) => message.external && message.subject.includes("Brightline"));
  assert.ok(external);
  const panel = await client.request(`/api/messages/${external.id}/trust`);
  assert.equal(panel.status, 200);
  assert.equal(panel.body.checks.length, 3);
  assert.equal(panel.body.trusted, false);
  assert.ok(panel.body.sender.email.endsWith("brightline.example"));

  const trust = await client.request(`/api/messages/${external.id}/trust`, {
    method: "POST",
    body: { trusted: true }
  });
  assert.equal(trust.status, 200);
  assert.equal(trust.body.trusted, true);
  const updated = (await client.request("/api/bootstrap")).body;
  assert.ok(updated.messages.filter((message) => message.from.email === external.from.email).every((message) => message.trusted));
  assert.ok(updated.user.trustedSenders.includes(external.from.email));

  const unmark = await client.request(`/api/messages/${external.id}/trust`, {
    method: "POST",
    body: { trusted: false }
  });
  assert.equal(unmark.status, 200);
  const audit = (await client.request("/api/audit?category=message")).body;
  assert.ok(audit.entries.some((entry) => entry.action === "trust.marked"));
});

test("audit log filters and CSV export", async () => {
  await client.request("/api/messages/actions", {
    method: "POST",
    body: { ids: ["m1"], action: "archive" }
  });
  const all = (await client.request("/api/audit?limit=20")).body;
  assert.ok(all.entries.length > 0);
  assert.ok(all.entries.every((entry) => entry.id && entry.ts && entry.action));
  const loginAudit = (await client.request("/api/audit?category=login")).body;
  assert.ok(loginAudit.entries.length >= 1);
  assert.ok(loginAudit.entries.every((entry) => entry.action.startsWith("auth.")));
  const messageAudit = (await client.request("/api/audit?category=message")).body;
  assert.ok(messageAudit.entries.every((entry) => entry.action.startsWith("message.") || entry.action.startsWith("trust.")));

  const csv = await client.request("/api/audit.csv?category=message", { raw: true });
  assert.equal(csv.status, 200);
  assert.match(csv.headers.get("content-type"), /text\/csv/);
  assert.match(csv.headers.get("content-disposition"), /attachment; filename="a8m-audit-/);
  const text = await csv.text();
  assert.match(text.split("\r\n")[0], /^time,action,target,actor,detail,ip$/);
  assert.match(text, /message\.archive/);
});

test("data persists across an application restart", async () => {
  const dataFile = path.join(tempDir, "persist.json");
  const first = await createApp({ dataFile });
  const firstAddress = await first.listen(0);
  const firstClient = new ApiClient(`http://127.0.0.1:${firstAddress.port}`);
  await firstClient.login();
  const patch = await firstClient.request("/api/messages/m1", { method: "PATCH", body: { starred: true, unread: false } });
  assert.equal(patch.status, 200);
  await first.close();

  const second = await createApp({ dataFile });
  const secondAddress = await second.listen(0);
  const secondClient = new ApiClient(`http://127.0.0.1:${secondAddress.port}`);
  await secondClient.login();
  const bootstrap = (await secondClient.request("/api/bootstrap")).body;
  const message = bootstrap.messages.find((item) => item.id === "m1");
  assert.equal(message.starred, true);
  assert.equal(message.unread, false);
  await second.close();
});

test("demo reset restores the seed and keeps the session usable", async () => {
  await client.request("/api/messages/actions", { method: "POST", body: { ids: ["m1"], action: "archive" } });
  const reset = await client.request("/api/dev/reset", { method: "POST", body: {} });
  assert.equal(reset.status, 200);
  const bootstrap = (await client.request("/api/bootstrap")).body;
  assert.equal(bootstrap.messages.find((message) => message.id === "m1").folder, "inbox");
  assert.equal(bootstrap.pendingSends.length, 0);
  assert.equal(bootstrap.outbound.used, 37);
});
