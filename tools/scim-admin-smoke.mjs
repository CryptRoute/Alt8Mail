#!/usr/bin/env node
// SCIM token lifecycle smoke for a running Alt8Mail admin instance.
//
//   ALT8MAIL_ADMIN_URL      Admin console URL, e.g. https://mail.example.com/admin/
//                           (or set ALT8MAIL_ADMIN_API to the .../admin/api URL directly)
//   ALT8MAIL_ADMIN_EMAIL    Administrator email (default: avery.chen@a8m.test)
//   ALT8MAIL_ADMIN_PASSWORD Admin password, or ALT8MAIL_ADMIN_PASSWORD_FILE
//
// The tool skips cleanly with exit code 0 when no credentials are configured,
// so it is safe to run in CI. It exercises create, rotate and revoke against the
// admin token API and never prints a raw token value.

import fs from "node:fs";

const adminUrl = (process.env.ALT8MAIL_ADMIN_URL || "").trim();
const explicitApi = (process.env.ALT8MAIL_ADMIN_API || "").trim();
const email = (process.env.ALT8MAIL_ADMIN_EMAIL || "avery.chen@a8m.test").trim();
const passwordFile = (process.env.ALT8MAIL_ADMIN_PASSWORD_FILE || "").trim();
let password = process.env.ALT8MAIL_ADMIN_PASSWORD || "";
if (!password && passwordFile) {
  try {
    password = fs.readFileSync(passwordFile, "utf8").trim();
  } catch {
    password = "";
  }
}

function adminApiBase() {
  if (explicitApi) return explicitApi.replace(/\/+$/, "") + "/";
  if (!adminUrl) return "";
  try {
    const consoleUrl = adminUrl.endsWith("/") ? adminUrl : adminUrl + "/";
    return new URL("api/", consoleUrl).href.replace(/\/+$/, "") + "/";
  } catch {
    return "";
  }
}

const apiBase = adminApiBase();
if (!apiBase || !password) {
  console.log("SCIM admin smoke skipped: set ALT8MAIL_ADMIN_URL (or ALT8MAIL_ADMIN_API) and ALT8MAIL_ADMIN_PASSWORD (or ALT8MAIL_ADMIN_PASSWORD_FILE) to run it against a test admin instance.");
  process.exit(0);
}

let csrf = "";
let failures = 0;
const cookies = new Map();
const cleanupIds = new Set();

function redact(value) {
  return String(value == null ? "" : value).replace(/a8m_scim_[A-Za-z0-9_-]+/g, "a8m_scim_<redacted>");
}

function describe(payload) {
  return redact(JSON.stringify(payload));
}

function check(condition, message, detail) {
  if (condition) {
    console.log("  PASS ", message);
  } else {
    failures += 1;
    console.error("  FAIL ", message, detail === undefined ? "" : detail);
  }
}

function cookieHeader() {
  return Array.from(cookies, ([name, value]) => `${name}=${value}`).join("; ");
}

function rememberCookies(response) {
  const values = typeof response.headers.getSetCookie === "function"
    ? response.headers.getSetCookie()
    : (response.headers.get("set-cookie") ? [response.headers.get("set-cookie")] : []);
  for (const value of values) {
    const pair = String(value).split(";")[0];
    const separator = pair.indexOf("=");
    if (separator > 0) cookies.set(pair.slice(0, separator).trim(), pair.slice(separator + 1).trim());
  }
}

async function request(method, path, body) {
  const headers = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (cookies.size) headers.Cookie = cookieHeader();
  if (csrf && method !== "GET") headers["X-CSRF-Token"] = csrf;
  const response = await fetch(apiBase + path, {
    method,
    headers,
    redirect: "manual",
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  rememberCookies(response);
  const text = await response.text();
  let payload = null;
  try { payload = text ? JSON.parse(text) : null; } catch { payload = { raw: text }; }
  return { status: response.status, body: payload };
}

const stamp = Date.now();
const tokenName = `SCIM admin smoke ${stamp}`;
let createdId = "";
let rotatedId = "";

try {
  console.log("\n[1] Admin sign-in");
  const login = await request("POST", "session", { email, password });
  if (login.body && login.body.twoFactorRequired) {
    console.log("SCIM admin smoke skipped: the administrator account requires a second factor that this tool cannot supply.");
    process.exitCode = 0;
    throw new Error("__skip__");
  }
  check(login.status === 200 && login.body && login.body.ok, "admin sign-in succeeds", `${login.status} ${describe(login.body)}`);
  if (login.status !== 200 || !login.body || !login.body.ok) throw new Error("admin sign-in failed");
  csrf = login.body.csrf || "";
  check(Boolean(csrf), "CSRF token returned");

  console.log("\n[2] List SCIM tokens");
  const before = await request("GET", "scim/tokens");
  check(before.status === 200 && Array.isArray(before.body && before.body.tokens), "token list is readable", `${before.status} ${describe(before.body)}`);
  const beforeTokens = (before.body && before.body.tokens) || [];
  check(beforeTokens.every((token) => !("token" in token) && !("tokenHash" in token) && !("hash" in token)), "token list never exposes a raw token or hash");

  console.log("\n[3] Create SCIM token");
  const created = await request("POST", "scim/tokens", { name: tokenName });
  check(created.status === 201, "create returns 201", `${created.status} ${describe(created.body)}`);
  const rawToken = (created.body && created.body.token) || "";
  check(/^a8m_scim_[A-Za-z0-9_-]{20,}$/.test(rawToken), "create returns a one-time bearer token");
  createdId = (created.body && created.body.id) || "";
  check(Boolean(createdId), "created token has an id");
  if (createdId) cleanupIds.add(createdId);
  const afterCreate = await request("GET", "scim/tokens");
  const createdRow = ((afterCreate.body && afterCreate.body.tokens) || []).find((token) => token.id === createdId);
  check(Boolean(createdRow), "created token appears in the list");
  check(!JSON.stringify(afterCreate.body || {}).includes(rawToken), "raw token never appears in a list response");

  console.log("\n[4] Rotate SCIM token");
  const rotated = createdId ? await request("POST", `scim/tokens/${encodeURIComponent(createdId)}/rotate`, {}) : { status: 0, body: null };
  check(rotated.status === 200, "rotate returns 200", `${rotated.status} ${describe(rotated.body)}`);
  const rotatedRaw = (rotated.body && rotated.body.token) || "";
  check(/^a8m_scim_[A-Za-z0-9_-]{20,}$/.test(rotatedRaw) && rotatedRaw !== rawToken, "rotate returns a new one-time token");
  rotatedId = (rotated.body && rotated.body.id) || "";
  check(Boolean(rotatedId) && rotatedId !== createdId, "rotation creates a replacement token row");
  if (rotatedId) cleanupIds.add(rotatedId);
  const afterRotate = await request("GET", "scim/tokens");
  const oldRow = ((afterRotate.body && afterRotate.body.tokens) || []).find((token) => token.id === createdId);
  const newRow = ((afterRotate.body && afterRotate.body.tokens) || []).find((token) => token.id === rotatedId);
  check(oldRow && oldRow.status === "revoked", "rotated token is revoked");
  check(newRow && newRow.status === "active", "replacement token is active");
  check(!JSON.stringify(afterRotate.body || {}).includes(rawToken) && !JSON.stringify(afterRotate.body || {}).includes(rotatedRaw), "rotated list never contains a raw token");

  console.log("\n[5] Revoke SCIM token");
  const revoked = rotatedId ? await request("DELETE", `scim/tokens/${encodeURIComponent(rotatedId)}`) : { status: 0, body: null };
  check(revoked.status === 200 && revoked.body && revoked.body.ok === true, "revoke returns 200", `${revoked.status} ${describe(revoked.body)}`);
  cleanupIds.delete(rotatedId);
  const afterRevoke = await request("GET", "scim/tokens");
  const revokedRow = ((afterRevoke.body && afterRevoke.body.tokens) || []).find((token) => token.id === rotatedId);
  check(revokedRow && revokedRow.status === "revoked", "replacement token is revoked in the list");
  const secondRevoke = rotatedId ? await request("DELETE", `scim/tokens/${encodeURIComponent(rotatedId)}`) : { status: 0, body: null };
  check(secondRevoke.status === 404, "second revoke returns 404", `${secondRevoke.status} ${describe(secondRevoke.body)}`);

  console.log(`\nSCIM admin smoke passed: created, rotated and revoked "${tokenName}". Only prefixes were displayed.`);
} catch (error) {
  if (error.message !== "__skip__") {
    console.error("SCIM ADMIN SMOKE FAILED:", redact(error.message));
    failures += 1;
  }
} finally {
  for (const id of cleanupIds) {
    try { await request("DELETE", `scim/tokens/${encodeURIComponent(id)}`); } catch {}
  }
  if (failures > 0) {
    console.error(`\nSCIM admin smoke failed with ${failures} failed check(s).`);
    process.exitCode = 1;
  }
}
