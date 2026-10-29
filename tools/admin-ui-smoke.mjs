import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { launchBrowser } from "./cdp.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const artifacts = path.resolve(__dirname, "..", "artifacts");
const base = process.env.MERIDIAN_ADMIN_URL || "https://130-94-46-22.sslip.io/admin/";
const email = process.env.MERIDIAN_ADMIN_EMAIL || "avery.chen@meridian.test";
const password = process.env.MERIDIAN_ADMIN_PASSWORD || "";
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
if (!password) throw new Error("MERIDIAN_ADMIN_PASSWORD is required");

const browser = await launchBrowser({ headless: true, width: 1440, height: 1000, ignoreCertificateErrors: false });
const page = await browser.newPage(base);
const stamp = Date.now();
const smokeUser = `ui.smoke.${stamp}@meridian.test`;
const smokeGroup = `ui-smoke-${stamp}@meridian.test`;
const smokeRole = `UI smoke role ${stamp}`;
const smokeUnit = `UI Smoke Unit ${stamp}`;

const api = (method, url, body) => page.eval(`(async () => {
  const match = document.cookie.match(/(?:^|; )meridian_admin_csrf=([^;]+)/);
  const headers = { "Content-Type": "application/json" };
  if (match) headers["X-CSRF-Token"] = decodeURIComponent(match[1]);
  const response = await fetch(${JSON.stringify(url)}, {
    method: ${JSON.stringify(method)}, credentials: "same-origin", headers,
    body: ${body === undefined ? "undefined" : JSON.stringify(JSON.stringify(body))}
  });
  const payload = await response.json().catch(() => null);
  return { status: response.status, body: payload };
})()`);

const click = async (selector) => {
  await page.waitFor(`!!document.querySelector(${JSON.stringify(selector)})`, 8000);
  await page.click(selector);
};
const waitText = (text, timeout = 10000) => page.waitFor(`document.body.textContent.includes(${JSON.stringify(text)})`, timeout, 200);

try {
  await page.waitFor(() => document.readyState === "complete", 15000);
  await sleep(600);
  await page.setValue("#adminEmail", email);
  await page.setValue("#adminPassword", password);
  await click("#loginSubmit");
  await page.waitFor(() => !document.getElementById("adminApp").hidden, 20000);
  await sleep(800);
  assert.ok((await page.eval("document.querySelector('.view-head h1')?.textContent || ''")).length > 0, "overview renders");

  console.log("\n[1] User lifecycle UI");
  await page.eval('location.hash = "#/users"');
  await page.waitFor(() => document.querySelectorAll("tr[data-user-id]").length > 0, 10000);
  await click('[data-action="new-user"]');
  await page.waitFor(() => document.getElementById("newUserForm") !== null, 5000);
  await page.setValue('[data-user-field="name"]', "UI Smoke User");
  await page.setValue('[data-user-field="email"]', smokeUser);
  await page.setValue('[data-user-field="title"]', "Automated verification");
  await page.setValue('[data-user-field="quotaGb"]', "2");
  await click('[data-modal-submit="newUserForm"]');
  await page.waitFor(() => document.body.textContent.includes("Temporary web password"), 20000);
  assert.ok(await page.eval("document.body.textContent.includes('Mailbox password')"), "one-time mailbox credentials shown");
  await click("[data-modal-close]");
  await page.waitFor(`document.body.textContent.includes(${JSON.stringify(smokeUser)})`, 10000);
  await page.screenshot(path.join(artifacts, "admin-users-created.png"));

  console.log("\n[2] Group and org unit UI");
  await page.eval('location.hash = "#/groups"');
  await page.waitFor(() => document.querySelector('[data-action="new-group"]'), 8000);
  await click('[data-action="new-group"]');
  await page.setValue('[data-group-field="name"]', `UI Smoke Group ${stamp}`);
  await page.setValue('[data-group-field="email"]', smokeGroup);
  await page.setValue('[data-group-field="description"]', "Created by the admin UI smoke test");
  await click('[data-modal-submit="newGroupForm"]');
  await page.waitFor(`document.body.textContent.includes(${JSON.stringify(smokeGroup)})`, 10000);
  await page.eval('location.hash = "#/org-units"');
  await page.waitFor(() => document.querySelector('[data-action="new-org-unit"]'), 8000);
  await click('[data-action="new-org-unit"]');
  await page.setValue('[data-org-field="name"]', smokeUnit);
  await page.setValue('[data-org-field="description"]', "Temporary UI smoke unit");
  await click('[data-modal-submit="newOrgUnitForm"]');
  await page.waitFor(`document.body.textContent.includes(${JSON.stringify(smokeUnit)})`, 10000);

  console.log("\n[3] Role UI");
  await page.eval('location.hash = "#/roles"');
  await page.waitFor(() => document.querySelector('[data-action="new-role"]'), 8000);
  await click('[data-action="new-role"]');
  await page.setValue('[data-role-field="name"]', smokeRole);
  await page.setValue('[data-role-field="description"]', "Temporary UI smoke role");
  await page.eval(`(() => { for (const key of ["users.read","audit.read"]) { const box = document.querySelector('[data-role-permission="'+key+'"]'); if (box) box.checked = true; } })()`);
  await click('[data-modal-submit="newRoleForm"]');
  await page.waitFor(`document.body.textContent.includes(${JSON.stringify(smokeRole)})`, 10000);
  await page.screenshot(path.join(artifacts, "admin-roles-created.png"));

  console.log("\n[4] Policies UI round-trip");
  await page.eval('location.hash = "#/policies"');
  await page.waitFor(() => document.querySelector('[data-policy-input="security.passwordMinLength"]'), 8000);
  await page.setValue('[data-policy-input="security.passwordMinLength"]', "13");
  await page.eval(`document.querySelector('[data-action="save-policies"]').click()`);
  await sleep(1300);
  let prefs = await api("GET", "./api/policies");
  assert.equal(prefs.body.policy.security.passwordMinLength, 13, "policy saved through the UI");
  await page.setValue('[data-policy-input="security.passwordMinLength"]', "12");
  await page.eval(`document.querySelector('[data-action="save-policies"]').click()`);
  await sleep(1000);

  console.log("\n[5] Domains, audit, settings and organizations");
  await page.eval('location.hash = "#/domains"');
  await page.waitFor(() => document.body.textContent.includes("meridian.test"), 10000);
  await click(".domain-card");
  await page.waitFor(() => document.querySelector("#drawerRoot .drawer") !== null, 5000);
  const domainDrawer = await page.eval("document.getElementById('drawerRoot').textContent");
  assert.ok(domainDrawer.includes("._domainkey."), "domain drawer renders the DKIM record");
  assert.ok(domainDrawer.includes("v=DKIM1") && domainDrawer.includes("p="), "domain drawer includes the real DKIM public key");
  assert.ok(!domainDrawer.includes("<public key from Rspamd>"), "domain drawer contains the real DKIM key, not a placeholder");
  await click("[data-drawer-close]");
  await page.eval('location.hash = "#/quarantine"');
  await page.waitFor(() => document.querySelector(".view-head h1")?.textContent === "Quarantine", 10000);
  assert.ok((await page.eval("document.body.textContent")).includes("Quarantine"), "quarantine view renders");
  await page.eval('location.hash = "#/audit"');
  await page.waitFor(() => document.querySelectorAll(".audit-table tbody tr").length > 0, 10000);
  await page.eval('location.hash = "#/settings"');
  await page.waitFor(() => document.getElementById("tenantSettingsForm") !== null, 10000);
  await page.eval('location.hash = "#/tenants"');
  await page.waitFor(() => document.body.textContent.includes("System administrators"), 10000);
  await page.screenshot(path.join(artifacts, "admin-tenants.png"));

  console.log("\n[6] Responsive admin shell");
  await page.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
  await page.eval('location.hash = "#/overview"');
  await sleep(600);
  assert.equal(await page.eval("document.documentElement.scrollWidth <= window.innerWidth + 1"), true, "mobile layout has no horizontal page overflow");
  await page.screenshot(path.join(artifacts, "admin-mobile.png"));
  await page.click("#menuButton");
  await sleep(250);
  assert.equal(await page.eval("document.getElementById('sidebar').classList.contains('open')"), true, "mobile navigation drawer opens");
  await page.send("Emulation.setDeviceMetricsOverride", { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });

  console.log("\n[7] Cleanup through the API");
  const groupId = await page.eval("(async () => { const d = await fetch('./api/groups').then(r=>r.json()); return (d.groups||[]).find(g=>g.email==='" + smokeGroup + "')?.id || ''; })()");
  if (groupId) await api("DELETE", `./api/groups/${encodeURIComponent(groupId)}`);
  const unitId = await page.eval("(async () => { const d = await fetch('./api/org-units').then(r=>r.json()); return (d.orgUnits||[]).find(u=>u.name==='" + smokeUnit + "')?.id || ''; })()");
  if (unitId) await api("DELETE", `./api/org-units/${encodeURIComponent(unitId)}`);
  const roleId = await page.eval("(async () => { const d = await fetch('./api/roles').then(r=>r.json()); return (d.roles||[]).find(r=>r.name==='" + smokeRole + "')?.id || ''; })()");
  if (roleId) await api("DELETE", `./api/roles/${encodeURIComponent(roleId)}`);
  const userId = await page.eval("(async () => { const d = await fetch('./api/users?q='+encodeURIComponent('" + smokeUser + "')).then(r=>r.json()); return (d.users||[])[0]?.id || ''; })()");
  if (userId) await api("DELETE", `./api/users/${encodeURIComponent(userId)}`);

  const errors = page.errors.filter((error) => !/401/.test(error) && !/Failed to load resource/.test(error));
  assert.deepEqual(errors, [], `browser errors: ${errors.join(" | ")}`);
  console.log("\nAdmin UI smoke passed: users, groups, org units, roles, policies, quarantine, domains, audit, settings and organizations.");
  console.log("Cleanup user:", smokeUser);
} catch (error) {
  console.error("ADMIN UI SMOKE FAILED:", error.message);
  console.error("page.errors", page.errors);
  try { await page.screenshot(path.join(artifacts, "admin-ui-failure.png")); } catch {}
  throw error;
} finally {
  await browser.close();
}
