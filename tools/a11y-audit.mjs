import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(__dirname, "..");
const artifacts = path.join(root, "artifacts");

export const SEVERITY_ORDER = { Critical: 0, High: 1, Medium: 2, Low: 3 };

function auditDocument() {
  const findings = [];
  const seen = new Set();
  const add = (rule, severity, selector, message) => {
    const key = `${rule}\u0000${selector}\u0000${message}`;
    if (seen.has(key)) return;
    seen.add(key);
    findings.push({ rule, severity, selector, message });
  };
  const rendered = (el) => {
    if (!el || !el.isConnected) return false;
    if (el.closest("[hidden]")) return false;
    const style = getComputedStyle(el);
    if (style.display === "none" || style.visibility === "hidden" || style.opacity === "0") return false;
    return el.getClientRects().length > 0;
  };
  const text = (el) => String(el?.textContent || "").replace(/\s+/g, " ").trim();
  const uniqueId = (id) => document.querySelectorAll(`[id="${CSS.escape(id)}"]`).length === 1;
  const cssPath = (el) => {
    if (!el || el === document.documentElement) return "html";
    if (el.id && uniqueId(el.id)) return `#${CSS.escape(el.id)}`;
    const parts = [];
    let current = el;
    while (current && current !== document.documentElement && parts.length < 6) {
      if (current.id && uniqueId(current.id)) {
        parts.unshift(`#${CSS.escape(current.id)}`);
        break;
      }
      let part = current.tagName.toLowerCase();
      const parent = current.parentElement;
      if (parent) {
        const same = [...parent.children].filter((child) => child.tagName === current.tagName);
        if (same.length > 1) part += `:nth-of-type(${same.indexOf(current) + 1})`;
      }
      parts.unshift(part);
      current = parent;
    }
    return parts.join(" > ");
  };
  const labelledByText = (el) => {
    const ids = String(el.getAttribute("aria-labelledby") || "").trim().split(/\s+/).filter(Boolean);
    if (!ids.length) return "";
    return ids.map((id) => text(document.getElementById(id))).filter(Boolean).join(" ");
  };
  const labelText = (el) => {
    if (el.labels && el.labels.length) return [...el.labels].map(text).filter(Boolean).join(" ");
    return "";
  };
  const accessibleName = (el) => {
    const by = labelledByText(el);
    if (by) return by;
    const aria = String(el.getAttribute("aria-label") || "").trim();
    if (aria) return aria;
    const labels = labelText(el);
    if (labels) return labels;
    if (el.tagName === "IMG") return String(el.getAttribute("alt") || "").trim();
    if (el.tagName === "INPUT" && ["submit", "button", "reset"].includes(String(el.type).toLowerCase())) {
      return String(el.value || "").trim();
    }
    const content = text(el);
    if (content) return content;
    return String(el.getAttribute("title") || "").trim();
  };
  const focusable = (el) => {
    if (!el || el.disabled || el.getAttribute("aria-disabled") === "true") return false;
    if (el.getAttribute("tabindex") === "-1") return false;
    if (el.matches("a[href], button, select, textarea, input:not([type=hidden]), [contenteditable='true']")) return true;
    return el.hasAttribute("tabindex");
  };

  if (!String(document.documentElement.lang || "").trim()) {
    add("document-lang", "Critical", "html", "The document has no lang attribute.");
  }
  if (!String(document.title || "").trim()) {
    add("document-title", "Critical", "head > title", "The document has no title.");
  }

  const headings = [...document.querySelectorAll("h1,h2,h3,h4,h5,h6")].filter(rendered);
  const h1s = headings.filter((heading) => heading.tagName === "H1");
  if (h1s.length === 0) {
    add("page-h1", "High", "body", "The page has no visible h1.");
  } else if (h1s.length > 1) {
    add("page-h1", "High", cssPath(h1s[1]), `The page has ${h1s.length} visible h1 headings.`);
  }
  let previousLevel = 0;
  for (const heading of headings) {
    const level = Number(heading.tagName.substring(1));
    if (previousLevel && level > previousLevel + 1) {
      add("heading-order", "High", cssPath(heading), `Heading level jumps from h${previousLevel} to h${level}.`);
    }
    previousLevel = level;
  }

  const hasMain = [...document.querySelectorAll("main, [role='main']")].some(rendered);
  if (!hasMain) add("landmark-main", "High", "body", "The page has no visible main landmark.");

  const ids = new Map();
  document.querySelectorAll("[id]").forEach((el) => {
    const id = el.id;
    if (!id) return;
    if (!ids.has(id)) ids.set(id, []);
    ids.get(id).push(el);
  });
  for (const [id, elements] of ids) {
    if (elements.length < 2) continue;
    const referenced = document.querySelector(`[for="${CSS.escape(id)}"], [aria-labelledby~="${CSS.escape(id)}"], [aria-describedby~="${CSS.escape(id)}"], [headers~="${CSS.escape(id)}"]`);
    add("duplicate-id", referenced ? "Critical" : "High", `#${CSS.escape(id)}`,
      `The id "${id}" is used ${elements.length} times${referenced ? " and is referenced by a label or ARIA relationship" : ""}.`);
  }

  const controls = [...document.querySelectorAll("a[href], button, input:not([type=hidden]), select, textarea, [role='button'], [role='link'], [role='tab'], [role='menuitem'], [role='checkbox'], [role='switch'], [contenteditable='true'], [tabindex]")]
    .filter((el) => rendered(el) && focusable(el));
  for (const el of controls) {
    const name = accessibleName(el);
    if (!name) {
      const placeholder = String(el.getAttribute("placeholder") || "").trim();
      add("control-name", "Critical", cssPath(el),
        `${el.tagName.toLowerCase()}${el.getAttribute("role") ? `[role=${el.getAttribute("role")}]` : ""} has no accessible name${placeholder ? ` (placeholder only: "${placeholder}")` : ""}.`);
    }
  }

  const formFields = [...document.querySelectorAll("input:not([type=hidden]), select, textarea")].filter(rendered);
  for (const field of formFields) {
    if (String(field.getAttribute("aria-hidden") || "") === "true") continue;
    const type = String(field.type || "").toLowerCase();
    if (["submit", "button", "reset", "image"].includes(type)) continue;
    const explicit = field.labels && field.labels.length > 0;
    const aria = String(field.getAttribute("aria-label") || "").trim() || labelledByText(field);
    if (!explicit && !aria) {
      add("field-label", "Critical", cssPath(field),
        `${field.tagName.toLowerCase()}${type ? `[type=${type}]` : ""} has no associated label or accessible name.`);
    }
  }

  document.querySelectorAll("img").forEach((image) => {
    if (!rendered(image)) return;
    if (!image.hasAttribute("alt")) add("image-alt", "High", cssPath(image), "The image has no alt attribute.");
  });

  document.querySelectorAll("[aria-hidden='true']").forEach((hidden) => {
    if (!rendered(hidden)) return;
    const focusables = [hidden, ...hidden.querySelectorAll("*")].filter((el) => focusable(el) && rendered(el));
    for (const el of focusables.slice(0, 3)) {
      add("aria-hidden-focusable", "Critical", cssPath(el), "Focusable content is inside an aria-hidden subtree.");
    }
  });

  const dialogs = [...document.querySelectorAll("[role='dialog'], dialog[open]")].filter(rendered);
  for (const dialog of dialogs) {
    if (!accessibleName(dialog)) {
      add("dialog-name", "Critical", cssPath(dialog), "The visible dialog has no accessible name.");
    }
  }

  let hasFocusVisible = false;
  let hasReducedMotion = false;
  const visitRules = (rules) => {
    for (const rule of rules) {
      if (rule.selectorText && /:focus-visible/.test(rule.selectorText)) hasFocusVisible = true;
      if (rule.conditionText && /prefers-reduced-motion/i.test(rule.conditionText)) hasReducedMotion = true;
      if (rule.cssRules) {
        try { visitRules(rule.cssRules); } catch {}
      }
    }
  };
  for (const sheet of document.styleSheets) {
    try { visitRules(sheet.cssRules); } catch {}
  }
  if (!hasFocusVisible) add("focus-visible", "High", "html", "No :focus-visible styles were found.");
  if (!hasReducedMotion) add("reduced-motion", "High", "html", "No prefers-reduced-motion media query was found.");

  const rootWidth = Math.max(document.documentElement.scrollWidth, document.body?.scrollWidth || 0);
  const viewportWidth = document.documentElement.clientWidth;
  if (rootWidth > viewportWidth + 1) {
    add("horizontal-overflow", "Critical", "html", `The document is ${rootWidth - viewportWidth}px wider than the ${viewportWidth}px viewport.`);
  }

  return {
    url: location.href,
    title: document.title,
    findings,
    counts: {
      Critical: findings.filter((finding) => finding.severity === "Critical").length,
      High: findings.filter((finding) => finding.severity === "High").length,
      Medium: findings.filter((finding) => finding.severity === "Medium").length,
      Low: findings.filter((finding) => finding.severity === "Low").length
    }
  };
}

export async function runAudit(page, surface, { width = 1440, height = 1000 } = {}) {
  const original = await page.eval(`({ width: innerWidth, height: innerHeight })`);
  await page.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: false });
  await page.send("Emulation.setEmulatedMedia", {
    features: [
      { name: "prefers-color-scheme", value: "light" },
      { name: "prefers-reduced-motion", value: "reduce" }
    ]
  });
  await new Promise((resolve) => setTimeout(resolve, 220));
  const result = await page.eval(auditDocument);
  await page.send("Emulation.setDeviceMetricsOverride", {
    width: original.width, height: original.height, deviceScaleFactor: 1, mobile: false
  });
  const report = {
    surface,
    generatedAt: new Date().toISOString(),
    viewport: { width, height },
    reducedMotionEmulated: true,
    ...result
  };
  await fs.mkdir(artifacts, { recursive: true });
  const reportPath = path.join(artifacts, `a11y-${surface}.json`);
  await fs.writeFile(reportPath, JSON.stringify(report, null, 2));
  return { ...report, reportPath };
}

export function summarize(result) {
  const critical = result.findings.filter((finding) => finding.severity === "Critical");
  const high = result.findings.filter((finding) => finding.severity === "High");
  return {
    surface: result.surface,
    reportPath: result.reportPath,
    counts: result.counts,
    critical,
    high
  };
}

export function printSummary(summary, { verbose = true } = {}) {
  const counts = summary.counts;
  console.log(`${summary.surface}: ${counts.Critical} critical, ${counts.High} high, ${counts.Medium} medium, ${counts.Low} low (${summary.reportPath})`);
  if (!verbose) return;
  for (const finding of [...summary.critical, ...summary.high]) {
    console.log(`  [${finding.severity}] ${finding.rule} ${finding.selector} — ${finding.message}`);
  }
}

export async function writeIndex(results) {
  await fs.mkdir(artifacts, { recursive: true });
  const indexPath = path.join(artifacts, "a11y-report.json");
  await fs.writeFile(indexPath, JSON.stringify({
    generatedAt: new Date().toISOString(),
    surfaces: results.map((result) => ({
      surface: result.surface,
      url: result.url,
      report: result.reportPath,
      counts: result.counts
    }))
  }, null, 2));
  return indexPath;
}

export function hasBlockingFindings(results) {
  return results.some((result) => result.findings.some((finding) => finding.severity === "Critical"));
}
