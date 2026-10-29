import assert from "node:assert/strict";
import crypto from "node:crypto";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { launchBrowser } from "./cdp.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const artifacts = path.resolve(__dirname, "..", "artifacts");
const base = process.env.MERIDIAN_UI_URL || "https://130-94-46-22.sslip.io/";
const email = process.env.MERIDIAN_UI_EMAIL || "avery.chen@meridian.test";
const password = process.env.MERIDIAN_UI_PASSWORD || "";
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
if (!password) throw new Error("MERIDIAN_UI_PASSWORD is required");

function decodeBase32(value) {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  const clean = value.toUpperCase().replace(/=+$/, "");
  let bits = 0;
  let buffer = 0;
  const bytes = [];
  for (const char of clean) {
    const index = alphabet.indexOf(char);
    if (index < 0) continue;
    buffer = (buffer << 5) | index;
    bits += 5;
    if (bits >= 8) {
      bits -= 8;
      bytes.push((buffer >> bits) & 0xff);
    }
  }
  return Buffer.from(bytes);
}

function totp(secret) {
  const counter = Math.floor(Date.now() / 30000);
  const message = Buffer.alloc(8);
  message.writeBigUInt64BE(BigInt(counter));
  const digest = crypto.createHmac("sha1", decodeBase32(secret)).update(message).digest();
  const offset = digest[digest.length - 1] & 0x0f;
  const value = digest.readUInt32BE(offset) & 0x7fffffff;
  return String(value % 1000000).padStart(6, "0");
}

const browser = await launchBrowser({ headless: true, width: 1440, height: 1000, ignoreCertificateErrors: false });
const page = await browser.newPage(base);

const apiFetch = (method, url, body) => page.eval(`(async () => {
  const match = document.cookie.match(/(?:^|; )meridian_system_csrf=([^;]+)/);
  const headers = { "Content-Type": "application/json" };
  if (match) headers["X-CSRF-Token"] = decodeURIComponent(match[1]);
  const response = await fetch(${JSON.stringify(url)}, {
    method: ${JSON.stringify(method)}, credentials: "same-origin", headers,
    body: ${body === undefined ? "undefined" : JSON.stringify(JSON.stringify(body))}
  });
  const payload = await response.json().catch(() => null);
  return { status: response.status, body: payload };
})()`);

const fillChannel = async (selector, value) => {
  await page.setValue(selector, value);
};

async function closeComposeWindows() {
  while (await page.eval("document.querySelectorAll('.compose [data-compose-action=\"close\"]').length")) {
    await page.eval("document.querySelector('.compose [data-compose-action=\"close\"]').click()");
    await sleep(250);
  }
}

async function waitForWorkspace() {
  await page.waitFor(() => document.getElementById("bootSplash").hidden === true, 15000);
}

async function login() {
  await page.waitFor(() => !document.getElementById("loginScreen").hidden, 12000);
  await fillChannel("#loginEmail", email);
  await fillChannel("#loginPassword", password);
  await page.click("#loginSubmit");
  await page.waitFor(() => !document.getElementById("app").hidden || !document.getElementById("twoFactorForm").hidden || !document.getElementById("enrollmentForm").hidden, 15000);
  await sleep(300);
  const result = await page.eval("!document.getElementById('app').hidden ? 'app' : (!document.getElementById('twoFactorForm').hidden ? 'twofactor' : 'enrollment')");
  if (result === "app") {
    await waitForWorkspace();
    await closeComposeWindows();
  }
  return result;
}

async function openSecurity() {
  await closeComposeWindows();
  await page.click(".rail-btn[data-task='settings']");
  await page.waitFor(() => !document.getElementById("settingsView").hidden, 5000);
  await page.click("[data-settings-tab='security']");
  await sleep(500);
}

async function logout() {
  await page.click("#railLogout");
  await page.waitFor(() => !document.getElementById("loginScreen").hidden, 10000);
  await sleep(400);
}

async function closeRecovery() {
  if (await page.eval("!document.getElementById('recoveryOverlay').hidden")) {
    await page.click("#recoveryClose");
    await sleep(150);
  }
}

try {
  await page.waitFor(() => document.readyState === "complete", 15000);
  await sleep(600);

  console.log("\n[1] TOTP enrollment from Security settings");
  assert.equal(await login(), "app", "password sign-in succeeds with no second factor");
  await openSecurity();
  await page.click("#setupTotpBtn");
  await page.waitFor(() => !document.getElementById("totpSetupOverlay").hidden, 5000);
  const totpSecret = await page.eval("document.getElementById('totpSetupSecret').textContent");
  assert.equal(totpSecret.length >= 16, true, "authenticator secret is generated");
  await page.setValue("#totpSetupCode", totp(totpSecret));
  await page.click("#totpSetupVerify");
  await page.waitFor(() => document.getElementById("totpSetupOverlay").hidden, 8000);
  await closeRecovery();
  const enabledPolicy = await apiFetch("GET", "/api/security/policy");
  assert.equal(enabledPolicy.body.security.twoFactor.totpEnabled, true, "TOTP enabled in PostgreSQL");
  assert.equal(enabledPolicy.body.recoveryRemaining > 0, true, "hashed recovery codes generated");

  console.log("\n[2] TOTP login challenge");
  await logout();
  assert.equal(await login(), "twofactor", "password sign-in now requires a code");
  assert.equal(await page.eval("document.getElementById('twoFactorDemo').hidden"), true, "no demo bypass code is offered");
  const loginCode = totp(totpSecret);
  for (let index = 0; index < 6; index += 1) await page.setValue(`#otp${index}`, loginCode[index]);
  await page.click("#twoFactorSubmit");
  await page.waitFor(() => !document.getElementById("app").hidden, 15000);
  await waitForWorkspace();
  assert.equal(await page.eval("!document.getElementById('app').hidden"), true, "TOTP code opens the mailbox");

  console.log("\n[3] First-time enrollment policy and screen");
  const disable = await apiFetch("POST", "/api/security/2fa/totp/disable", { password });
  assert.equal(disable.status, 200, "authenticator can be removed");
  const setPolicy = await apiFetch("PATCH", "/api/security/policy", { enrollmentRequired: true });
  assert.equal(setPolicy.status, 200, "enrollment policy persists in PostgreSQL");
  await logout();
  assert.equal(await login(), "enrollment", "un-enrolled user is stopped at the enrollment screen");
  await page.screenshot(path.join(artifacts, "go-enrollment-required.png"));
  await page.click("#enrollmentTotpBtn");
  await page.waitFor(() => !document.getElementById("enrollmentTotpBlock").hidden, 5000);
  const enrollSecret = await page.eval("document.getElementById('enrollmentTotpSecret').textContent");
  await page.setValue("#enrollmentTotpCode", totp(enrollSecret));
  await page.click("#enrollmentTotpVerify");
  await page.waitFor(() => !document.getElementById("app").hidden, 15000);
  await waitForWorkspace();
  await closeRecovery();
  assert.equal(await page.eval("!document.getElementById('app').hidden"), true, "enrollment completes and opens the app");
  const afterEnroll = await apiFetch("GET", "/api/security/policy");
  assert.equal(afterEnroll.body.methods.includes("authenticator"), true, "enrollment factor is active");
  await apiFetch("POST", "/api/security/2fa/totp/disable", { password });
  await apiFetch("PATCH", "/api/security/policy", { enrollmentRequired: false });

  console.log("\n[4] WebAuthn passkey enrollment and login");
  const webauthn = await page.send("WebAuthn.enable");
  assert.ok(webauthn, "WebAuthn domain enabled");
  await page.send("WebAuthn.addVirtualAuthenticator", {
    options: { protocol: "ctap2", transport: "internal", hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true }
  });
  await openSecurity();
  await page.click("#addPasskeyBtn");
  await page.waitFor(() => document.querySelectorAll("#passkeySettingsList .session-row").length >= 1, 15000);
  await closeRecovery();
  const policyWithPasskey = await apiFetch("GET", "/api/security/policy");
  assert.equal(policyWithPasskey.body.passkeys.length >= 1, true, "passkey stored in PostgreSQL");
  await sleep(300);

  await logout();
  assert.equal(await login(), "twofactor", "passkey sign-in challenge is offered");
  await page.click("#usePasskeyBtn");
  await page.waitFor(() => !document.getElementById("app").hidden, 15000);
  await waitForWorkspace();
  assert.equal(await page.eval("!document.getElementById('app').hidden"), true, "WebAuthn assertion opens the mailbox");
  const passkeys = await apiFetch("GET", "/api/security/policy");
  for (const passkey of passkeys.body.passkeys) {
    await apiFetch("DELETE", `/api/security/webauthn/${encodeURIComponent(passkey.id)}`);
  }
  assert.equal((await apiFetch("GET", "/api/security/policy")).body.security.twoFactor.enabled, false, "2FA disabled after passkey removal");

  console.log("\n[5] First-time WebAuthn enrollment");
  await apiFetch("PATCH", "/api/security/policy", { enrollmentRequired: true });
  await logout();
  assert.equal(await login(), "enrollment", "passkey enrollment screen is shown for an un-enrolled user");
  await page.click("#enrollmentPasskeyBtn");
  await page.waitFor(() => !document.getElementById("app").hidden, 15000);
  await waitForWorkspace();
  await closeRecovery();
  const enrolledPasskeyPolicy = await apiFetch("GET", "/api/security/policy");
  assert.equal(enrolledPasskeyPolicy.body.passkeys.length >= 1, true, "first-time passkey enrollment stored a credential");
  for (const passkey of enrolledPasskeyPolicy.body.passkeys) {
    await apiFetch("DELETE", `/api/security/webauthn/${encodeURIComponent(passkey.id)}`);
  }
  await apiFetch("PATCH", "/api/security/policy", { enrollmentRequired: false });
  const finalPolicy = await apiFetch("GET", "/api/security/policy");
  assert.equal(finalPolicy.body.security.twoFactor.enabled, false, "2FA disabled again after test cleanup");
  assert.equal(finalPolicy.body.enrollmentRequired, false, "enrollment policy disabled in DB");

  const errors = page.errors.filter((error) => !/401/.test(error) && !/Failed to load resource/.test(error));
  assert.deepEqual(errors, [], `browser errors: ${errors.join(" | ")}`);
  console.log("Go auth smoke passed: TOTP, first-time enrollment and real WebAuthn passkey flow.");
} catch (error) {
  console.error("GO AUTH SMOKE FAILED:", error.message);
  console.error("page.errors", page.errors);
  try { await page.screenshot(path.join(artifacts, "go-auth-failure.png")); } catch {}
  throw error;
} finally {
  await browser.close();
}
