import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";

const DEFAULT_PORTS = [9333, 9334, 9335];

async function waitForJson(url, timeout = 12000) {
  const started = Date.now();
  let lastError;
  while (Date.now() - started < timeout) {
    try {
      const response = await fetch(url);
      if (response.ok) return await response.json();
    } catch (error) {
      lastError = error;
    }
    await new Promise((resolve) => setTimeout(resolve, 150));
  }
  throw lastError || new Error("Timed out waiting for browser endpoint");
}

export async function launchBrowser({ chromePath, port, headless = true, width = 1440, height = 1000, ignoreCertificateErrors = false } = {}) {
  const candidates = [
    chromePath,
    process.env.CHROME_PATH,
    "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",
    "C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe",
    "C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe",
    "/usr/bin/google-chrome",
    "/usr/bin/chromium",
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
  ].filter(Boolean);
  const executable = candidates.find((candidate) => existsSync(candidate)) || candidates[0];
  const chosenPort = port || DEFAULT_PORTS.find((value) => value > 0) || 9333;
  const userDataDir = await fs.mkdtemp(path.join(os.tmpdir(), "meridian-chrome-"));
  const args = [
    `--remote-debugging-port=${chosenPort}`,
    `--user-data-dir=${userDataDir}`,
    "--no-first-run",
    "--no-default-browser-check",
    "--disable-background-networking",
    "--disable-component-update",
    "--disable-default-apps",
    "--disable-dev-shm-usage",
    "--disable-extensions",
    "--disable-sync",
    "--metrics-recording-only",
    "--mute-audio",
    "--no-sandbox",
    "--disable-gpu",
    `--window-size=${width},${height}`,
    "about:blank"
  ];
  if (ignoreCertificateErrors) args.push("--ignore-certificate-errors");
  if (headless) args.unshift("--headless=new");
  const processHandle = spawn(executable, args, { stdio: "ignore", windowsHide: true });
  let version;
  try {
    version = await waitForJson(`http://127.0.0.1:${chosenPort}/json/version`);
  } catch (error) {
    processHandle.kill();
    throw new Error(`Could not start Chrome at ${executable}: ${error.message}`);
  }
  const browser = new CdpBrowser({ port: chosenPort, processHandle, userDataDir, version, width, height });
  return browser;
}

class CdpBrowser {
  constructor({ port, processHandle, userDataDir, version, width, height }) {
    this.port = port;
    this.processHandle = processHandle;
    this.userDataDir = userDataDir;
    this.version = version;
    this.width = width;
    this.height = height;
    this.pages = [];
  }

  async newPage(url = "about:blank") {
    const response = await fetch(`http://127.0.0.1:${this.port}/json/new?${encodeURIComponent(url)}`, { method: "PUT" });
    if (!response.ok) throw new Error(`Could not create page: ${response.status}`);
    const target = await response.json();
    const page = new CdpPage(this, target);
    await page.connect();
    this.pages.push(page);
    return page;
  }

  async close() {
    for (const page of this.pages) {
      try { await page.close(); } catch {}
    }
    try { this.processHandle.kill(); } catch {}
    await new Promise((resolve) => setTimeout(resolve, 250));
    try { await fs.rm(this.userDataDir, { recursive: true, force: true }); } catch {}
  }
}

class CdpPage {
  constructor(browser, target) {
    this.browser = browser;
    this.target = target;
    this.id = 0;
    this.pending = new Map();
    this.listeners = new Map();
    this.errors = [];
    this.console = [];
    this.ws = null;
    this.closed = false;
  }

  async connect() {
    this.ws = new WebSocket(this.target.webSocketDebuggerUrl);
    await new Promise((resolve, reject) => {
      this.ws.addEventListener("open", resolve, { once: true });
      this.ws.addEventListener("error", reject, { once: true });
    });
    this.ws.addEventListener("message", (event) => this.onMessage(event));
    this.ws.addEventListener("close", () => { this.closed = true; });
    await this.send("Page.enable");
    await this.send("Runtime.enable");
    await this.send("Log.enable");
    await this.send("Network.enable");
    await this.send("Emulation.setDeviceMetricsOverride", {
      width: this.browser.width,
      height: this.browser.height,
      deviceScaleFactor: 1,
      mobile: false
    });
    this.on("Runtime.exceptionThrown", (params) => {
      const detail = params.exceptionDetails || {};
      this.errors.push(detail.exception?.description || detail.text || "Runtime exception");
    });
    this.on("Log.entryAdded", (params) => {
      const entry = params.entry || {};
      if (entry.level === "error") this.errors.push(entry.text || "Log error");
    });
    this.on("Runtime.consoleAPICalled", (params) => {
      const text = (params.args || []).map((arg) => arg.value ?? arg.description ?? "").join(" ");
      this.console.push({ type: params.type, text });
      if (params.type === "error") this.errors.push(text);
    });
    this.on("Network.loadingFailed", (params) => {
      if (params.canceled) return;
      this.errors.push(`Network failed: ${params.errorText || "unknown"} ${params.type || ""}`);
    });
  }

  on(eventName, handler) {
    if (!this.listeners.has(eventName)) this.listeners.set(eventName, []);
    this.listeners.get(eventName).push(handler);
  }

  onMessage(event) {
    let message;
    try { message = JSON.parse(event.data); } catch { return; }
    if (message.id) {
      const pending = this.pending.get(message.id);
      if (!pending) return;
      this.pending.delete(message.id);
      if (message.error) pending.reject(new Error(`${message.error.message || JSON.stringify(message.error)} (${pending.method})`));
      else pending.resolve(message.result);
      return;
    }
    const handlers = this.listeners.get(message.method) || [];
    for (const handler of handlers) {
      try { handler(message.params || {}); } catch (error) { this.errors.push(error.message); }
    }
  }

  send(method, params = {}, timeout = 20000) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      return Promise.reject(new Error("Browser page is not connected"));
    }
    const id = ++this.id;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`CDP timeout: ${method}`));
      }, timeout);
      this.pending.set(id, {
        method,
        resolve: (value) => { clearTimeout(timer); resolve(value); },
        reject: (error) => { clearTimeout(timer); reject(error); }
      });
      this.ws.send(JSON.stringify({ id, method, params }));
    });
  }

  async goto(url, { waitForLoad = true } = {}) {
    const loaded = new Promise((resolve) => {
      const handler = () => { resolve(); };
      this.on("Page.loadEventFired", handler);
    });
    await this.send("Page.navigate", { url });
    if (waitForLoad) {
      await Promise.race([loaded, new Promise((resolve) => setTimeout(resolve, 8000))]);
    }
    await this.waitFor(() => document.readyState === "complete", 8000).catch(() => {});
  }

  async eval(expression, { awaitPromise = true, timeout = 15000 } = {}) {
    if (typeof expression === "function") expression = `(${expression.toString()})()`;
    const result = await this.send("Runtime.evaluate", {
      expression,
      awaitPromise,
      returnByValue: true,
      userGesture: true,
      timeout
    });
    if (result.exceptionDetails) {
      const detail = result.exceptionDetails;
      throw new Error(detail.exception?.description || detail.text || "Evaluation failed");
    }
    return result.result ? result.result.value : undefined;
  }

  async waitFor(expression, timeout = 8000, interval = 100) {
    const started = Date.now();
    const source = typeof expression === "function" ? `(${expression.toString()})()` : expression;
    let last;
    while (Date.now() - started < timeout) {
      try {
        const value = await this.eval(`Boolean(${source})`);
        if (value) return true;
      } catch (error) {
        last = error;
      }
      await new Promise((resolve) => setTimeout(resolve, interval));
    }
    throw new Error(`Timed out waiting for ${source}${last ? `: ${last.message}` : ""}`);
  }

  async box(selector) {
    const box = await this.eval(`(() => {
      const el = document.querySelector(${JSON.stringify(selector)});
      if (!el) return null;
      const r = el.getBoundingClientRect();
      return { x: r.x, y: r.y, width: r.width, height: r.height };
    })()`);
    if (!box) throw new Error(`Element not found: ${selector}`);
    return box;
  }

  async click(selector) {
    await this.eval(`(() => {
      const el = document.querySelector(${JSON.stringify(selector)});
      if (el) el.scrollIntoView({ block: "center", inline: "center" });
    })()`);
    await new Promise((resolve) => setTimeout(resolve, 60));
    const box = await this.box(selector);
    const x = box.x + box.width / 2;
    const y = box.y + box.height / 2;
    await this.send("Input.dispatchMouseEvent", { type: "mouseMoved", x, y });
    await this.send("Input.dispatchMouseEvent", { type: "mousePressed", x, y, button: "left", clickCount: 1 });
    await this.send("Input.dispatchMouseEvent", { type: "mouseReleased", x, y, button: "left", clickCount: 1 });
    await new Promise((resolve) => setTimeout(resolve, 160));
  }

  async clickAt(x, y) {
    await this.send("Input.dispatchMouseEvent", { type: "mouseMoved", x, y });
    await this.send("Input.dispatchMouseEvent", { type: "mousePressed", x, y, button: "left", clickCount: 1 });
    await this.send("Input.dispatchMouseEvent", { type: "mouseReleased", x, y, button: "left", clickCount: 1 });
    await new Promise((resolve) => setTimeout(resolve, 120));
  }

  async setValue(selector, value) {
    await this.eval(`(() => {
      const el = document.querySelector(${JSON.stringify(selector)});
      if (!el) throw new Error(${JSON.stringify("Missing " + selector)});
      const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : el instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
      const setter = Object.getOwnPropertyDescriptor(proto, "value").set;
      setter.call(el, ${JSON.stringify(value)});
      el.dispatchEvent(new Event("input", { bubbles: true }));
      el.dispatchEvent(new Event("change", { bubbles: true }));
    })()`);
  }

  async pressKey(key, { shift = false, ctrl = false, meta = false, alt = false } = {}) {
    const codeMap = {
      Enter: "Enter", Escape: "Escape", Tab: "Tab", Backspace: "Backspace", Delete: "Delete",
      " ": "Space", "/": "Slash", "?": "Slash", "#": "Digit3", "3": "Digit3"
    };
    const code = codeMap[key] || (/^[a-z]$/i.test(key) ? `Key${key.toUpperCase()}` : key);
    const modifiers = (shift ? 8 : 0) | (ctrl ? 2 : 0) | (alt ? 1 : 0) | (meta ? 4 : 0);
    const text = shift && key.length === 1 ? key : key.length === 1 ? key : key === "Enter" ? "\r" : undefined;
    await this.send("Input.dispatchKeyEvent", {
      type: text ? "keyDown" : "rawKeyDown",
      key,
      code,
      modifiers,
      windowsVirtualKeyCode: key === "Enter" ? 13 : key === "Escape" ? 27 : key.length === 1 ? key.toUpperCase().charCodeAt(0) : 0,
      text
    });
    await this.send("Input.dispatchKeyEvent", { type: "keyUp", key, code, modifiers });
    await new Promise((resolve) => setTimeout(resolve, 80));
  }

  async typeText(text) {
    await this.send("Input.insertText", { text });
    await new Promise((resolve) => setTimeout(resolve, 120));
  }

  async screenshot(filePath, { fullPage = false } = {}) {
    const result = await this.send("Page.captureScreenshot", {
      format: "png",
      fromSurface: true,
      captureBeyondViewport: fullPage
    });
    await fs.mkdir(path.dirname(filePath), { recursive: true });
    await fs.writeFile(filePath, Buffer.from(result.data, "base64"));
    return filePath;
  }

  clearErrors() {
    this.errors = [];
  }

  async close() {
    try { await this.send("Page.close"); } catch {}
    try { this.ws.close(); } catch {}
  }
}

export { CdpPage };
