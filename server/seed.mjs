import { randomUUID, scryptSync, randomBytes } from "node:crypto";
import { stripHtml } from "../public/search.mjs";
import { renderSieve } from "./sieve.mjs";

export const DEMO_EMAIL = "avery.chen@a8m.example";
export const DEMO_PASSWORD = "a8m-demo";
export const GIB = 1024 ** 3;
export const MIB = 1024 ** 2;
export const KIB = 1024;

const NOW = Date.now();
const MINUTE = 60 * 1000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

const iso = (msAgo) => new Date(NOW - msAgo).toISOString();

export function hashPassword(password, salt = randomBytes(16).toString("hex")) {
  const hash = scryptSync(password, salt, 64).toString("hex");
  return { salt, hash };
}

function bytesFrom(label) {
  const match = String(label).trim().match(/^([\d.]+)\s*(KB|MB|GB|B)$/i);
  if (!match) return Number(label) || 0;
  const value = Number(match[1]);
  const unit = match[2].toUpperCase();
  if (unit === "GB") return Math.round(value * GIB);
  if (unit === "MB") return Math.round(value * MIB);
  if (unit === "KB") return Math.round(value * KIB);
  return Math.round(value);
}

const SAMPLE_FILES = {
  "Q4-roadmap-v3.pdf": { url: "/samples/Q4-roadmap-v3.pdf", mime: "application/pdf" },
  "Brightline-order-form.pdf": { url: "/samples/Brightline-order-form.pdf", mime: "application/pdf" },
  "first-run-flows.pdf": { url: "/samples/first-run-flows.pdf", mime: "application/pdf" },
  "invoice-4821.pdf": { url: "/samples/invoice-4821.pdf", mime: "application/pdf" },
  "q3-retro.pdf": { url: "/samples/q3-retro.pdf", mime: "application/pdf" },
  "offsite-team.svg": { url: "/samples/offsite-team.svg", mime: "image/svg+xml" }
};

const MIME_BY_TYPE = {
  PDF: "application/pdf",
  XLS: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
  DOC: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  PPT: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
  ZIP: "application/zip",
  IMG: "image/png",
  SVG: "image/svg+xml",
  TXT: "text/plain"
};

function file(name, size, type, extra = {}) {
  const sample = SAMPLE_FILES[name] || null;
  return {
    id: "att_" + randomUUID().slice(0, 8),
    name,
    size,
    sizeBytes: bytesFrom(size),
    type,
    mime: sample ? sample.mime : (MIME_BY_TYPE[type] || "application/octet-stream"),
    contentUrl: sample ? sample.url : null,
    previewable: Boolean(sample),
    ...extra
  };
}

const ME = {
  name: "Avery Chen",
  email: DEMO_EMAIL,
  initials: "AC",
  color: "#34557a",
  role: "VP Product",
  company: "Alt8Mail",
  team: "Product",
  phone: "+1 (415) 555-0101",
  location: "San Francisco",
  reportsTo: "Dana Whitfield, CEO"
};

const ELENA = { name: "Elena Vasquez", email: "elena.vasquez@a8m.example", initials: "EV", color: "#34557a" };
const MARCUS = { name: "Marcus Webb", email: "marcus.webb@a8m.example", initials: "MW", color: "#3d6b62" };
const NORA = { name: "Nora Patel", email: "nora.patel@a8m.example", initials: "NP", color: "#5b4a7e" };
const PRIYA = { name: "Priya Shah", email: "priya.shah@a8m.example", initials: "PS", color: "#7a4a5c" };
const TOMAS = { name: "Tomás Ribeiro", email: "tomas.ribeiro@a8m.example", initials: "TR", color: "#7a5a3a" };
const LENA = { name: "Lena Fischer", email: "lena.fischer@a8m.example", initials: "LF", color: "#3f6570" };
const DANIEL = { name: "Daniel Kim", email: "daniel.kim@a8m.example", initials: "DK", color: "#476b4f" };
const AISHA = { name: "Aisha Bello", email: "aisha.bello@a8m.example", initials: "AB", color: "#4f5673" };
const JORDAN = { name: "Jordan Blake", email: "jordan.blake@brightline.example", initials: "JB", color: "#8a6a3a", company: "Brightline" };
const NADIA = { name: "Nadia Haddad", email: "nadia.haddad@a8m.example", initials: "NH", color: "#b8336a" };
const ANDREW = { name: "Andrew Lowe", email: "andrew.lowe@a8m.example", initials: "AL", color: "#0e7490" };
const IT = { name: "Alt8Mail IT", email: "it@a8m.example", initials: "IT", color: "#64748b" };
const FINANCE = { name: "Alt8Mail Finance", email: "finance@a8m.example", initials: "MF", color: "#15803d" };
const PARCEL = { name: "Parcel Notice", email: "no-reply@parcel-alert.example", initials: "PN", color: "#64748b" };
const OFFERS = { name: "Offers Daily", email: "hello@offers-daily.example", initials: "OD", color: "#b45309" };
const PHISH = { name: "Alt8Mail Account Team", email: "account-verify@a8m-security.example", initials: "MA", color: "#991b1b" };
const DESIGNFILE = { name: "Design Weekly", email: "news@designweekly.example", initials: "DW", color: "#6346d8" };

const PEOPLE = [
  { name: "Elena Vasquez", role: "VP Product", company: "Alt8Mail", team: "Product", email: "elena.vasquez@a8m.example", phone: "+1 (415) 555-0138", location: "San Francisco", reportsTo: "Dana Whitfield, CEO", initials: "EV", color: "#34557a" },
  { name: "Marcus Webb", role: "Legal Counsel", company: "Alt8Mail", team: "Legal", email: "marcus.webb@a8m.example", phone: "+1 (415) 555-0112", location: "New York", reportsTo: "Dana Whitfield, CEO", initials: "MW", color: "#3d6b62" },
  { name: "Nora Patel", role: "Design Lead", company: "Alt8Mail", team: "Design", email: "nora.patel@a8m.example", phone: "+1 (415) 555-0164", location: "San Francisco", reportsTo: "Elena Vasquez, VP Product", initials: "NP", color: "#5b4a7e" },
  { name: "Priya Shah", role: "Finance Manager", company: "Alt8Mail", team: "Finance", email: "priya.shah@a8m.example", phone: "+1 (415) 555-0179", location: "London", reportsTo: "Dana Whitfield, CEO", initials: "PS", color: "#7a4a5c" },
  { name: "Tomás Ribeiro", role: "Customer Success Lead", company: "Alt8Mail", team: "Customer Success", email: "tomas.ribeiro@a8m.example", phone: "+1 (415) 555-0193", location: "Lisbon", reportsTo: "Elena Vasquez, VP Product", initials: "TR", color: "#7a5a3a" },
  { name: "Lena Fischer", role: "People Operations", company: "Alt8Mail", team: "People", email: "lena.fischer@a8m.example", phone: "+1 (415) 555-0147", location: "Berlin", reportsTo: "Dana Whitfield, CEO", initials: "LF", color: "#3f6570" },
  { name: "Daniel Kim", role: "Data & Analytics", company: "Alt8Mail", team: "Data", email: "daniel.kim@a8m.example", phone: "+1 (415) 555-0125", location: "San Francisco", reportsTo: "Elena Vasquez, VP Product", initials: "DK", color: "#476b4f" },
  { name: "Aisha Bello", role: "Engineering Manager", company: "Alt8Mail", team: "Engineering", email: "aisha.bello@a8m.example", phone: "+1 (415) 555-0186", location: "London", reportsTo: "Dana Whitfield, CEO", initials: "AB", color: "#4f5673" }
];

const FOLDERS = [
  { id: "inbox", label: "Inbox", icon: "i-inbox", section: "favorites" },
  { id: "starred", label: "Starred", icon: "i-star", section: "favorites", virtual: true },
  { id: "sent", label: "Sent", icon: "i-send", section: "folders" },
  { id: "drafts", label: "Drafts", icon: "i-file", section: "folders" },
  { id: "archive", label: "Archive", icon: "i-archive", section: "folders" },
  { id: "spam", label: "Spam", icon: "i-alert", section: "folders" },
  { id: "trash", label: "Trash", icon: "i-trash", section: "folders" }
];

function authInternal(overrides = {}) {
  return {
    spf: "pass",
    dkim: "pass",
    dmarc: "pass",
    tls: "TLS 1.3",
    ip: "10.20.4.18",
    signer: "a8m.example",
    firstContact: false,
    warnings: [],
    ...overrides
  };
}

function authExternal(overrides = {}) {
  return {
    spf: "pass",
    dkim: "pass",
    dmarc: "pass",
    tls: "TLS 1.3",
    ip: "198.51.100.24",
    signer: "brightline.example",
    firstContact: false,
    warnings: [],
    ...overrides
  };
}

let messageCounter = 0;
function message(input) {
  messageCounter += 1;
  const bodyHtml = input.bodyHtml || "";
  const bodyText = input.bodyText || stripHtml(bodyHtml);
  const attachments = input.attachments || [];
  const sizeBytes = Number(input.sizeBytes) || (8 * KIB + attachments.reduce((sum, item) => sum + (item.sizeBytes || 0), 0));
  const external = input.external != null ? input.external : (input.from && !String(input.from.email).endsWith("@a8m.example"));
  return {
    id: input.id || `m${messageCounter}`,
    threadId: input.threadId || input.id || `m${messageCounter}`,
    folder: input.folder || "inbox",
    from: input.from || ME,
    to: input.to ? (Array.isArray(input.to) ? input.to : [input.to]) : [ME],
    cc: input.cc || [],
    bcc: input.bcc || [],
    subject: input.subject || "(no subject)",
    snippet: input.snippet || bodyText.slice(0, 150),
    bodyHtml,
    bodyText,
    date: input.date || iso(0),
    unread: !!input.unread,
    starred: !!input.starred,
    draft: !!input.draft,
    attachments,
    categories: input.categories || [],
    classification: input.classification || "Internal",
    external,
    auth: input.auth || (external ? authExternal() : authInternal()),
    event: input.event || null,
    sizeBytes,
    messageId: `<${input.id || "m" + messageCounter}@a8m.example>`,
    inReplyTo: input.inReplyTo || null,
    priority: input.priority || "normal",
    receipt: !!input.receipt,
    delivery: !!input.delivery,
    saveCopy: input.saveCopy !== false,
    viaApi: !!input.viaApi,
    windowMode: input.windowMode || "floating",
    threadIdExplicit: !!input.threadId
  };
}

function bodyParagraphs(lines) {
  return lines.map((line) => `<p>${line.replace(/[&<>]/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" }[ch]))}</p>`).join("");
}

function signature(name, role) {
  return `<p class="sig">${name}<br><span>${role} · Alt8Mail</span></p>`;
}

export function seedDatabase() {
  const messages = [];

  // t1 — Q4 roadmap review (3 messages, one unread)
  messages.push(message({
    id: "m1", threadId: "t1", folder: "inbox", from: ELENA, to: ME,
    subject: "Q4 roadmap review — Thursday 10:00",
    date: iso(2 * HOUR + 5 * MINUTE), unread: false,
    categories: ["decision", "product"], classification: "Internal",
    snippet: "I pulled the revised scope into the deck and trimmed the wish list to what we can ship before the holidays.",
    attachments: [file("Q4-roadmap-v3.pdf", "2.4 MB", "PDF"), file("Launch-scope.xlsx", "480 KB", "XLS")],
    event: { title: "Q4 roadmap review", day: "Thursday, October 29", time: "10:00–11:00", where: "Conference Room B · Teams", attendees: 6, response: null },
    bodyHtml: `<p>Hi Avery,</p>
<p>I pulled the revised scope into the deck and trimmed the wish list to what we can realistically ship before the holidays. The two open questions are:</p>
<ol><li>Do we keep the shared-inbox beta in Q4, or move it to January?</li><li>Is the analytics export a launch blocker, or can it follow a week later?</li></ol>
<p>I'd like to settle both on Thursday so we can lock the plan. I'll send an agenda Wednesday morning.</p>
<p>Thanks,<br>Elena</p>${signature("Elena Vasquez", "VP Product")}`
  }));
  messages.push(message({
    id: "m2", threadId: "t1", folder: "sent", from: ME, to: ELENA,
    subject: "Re: Q4 roadmap review — Thursday 10:00",
    date: iso(1 * HOUR + 58 * MINUTE), unread: false, sizeBytes: 16 * KIB,
    snippet: "The deck looks tight. I'd keep the shared-inbox beta in Q4 and move the analytics export to early January.",
    inReplyTo: "<m1@a8m.example>",
    bodyHtml: bodyParagraphs(["Elena —", "The deck looks tight. I'd keep the shared-inbox beta in Q4 and move the analytics export to early January, as long as support has the macro library ready.", "I'll bring both questions to Thursday.", "Avery"])
  }));
  messages.push(message({
    id: "m3", threadId: "t1", folder: "inbox", from: ELENA, to: ME,
    subject: "Re: Q4 roadmap review — Thursday 10:00",
    date: iso(38 * MINUTE), unread: true, sizeBytes: 9 * KIB,
    categories: ["decision"],
    snippet: "One more thing for the agenda: support asked for a hard date on the macro library.",
    inReplyTo: "<m2@a8m.example>",
    bodyHtml: bodyParagraphs(["One more thing for the agenda: support asked for a hard date on the macro library. Could you bring the latest estimate?", "Thanks — Elena"])
  }));

  // t2 — Brightline order form (3 messages, external, one unread)
  messages.push(message({
    id: "m4", threadId: "t2", folder: "inbox", from: JORDAN, to: ME,
    subject: "Brightline — signed order form and next steps",
    date: iso(3 * HOUR + 5 * MINUTE), unread: true, external: true,
    categories: ["sales"], classification: "Internal", attachments: [file("Brightline-order-form.pdf", "1.8 MB", "PDF")],
    snippet: "Attaching the countersigned order form. Procurement approved the annual term; security is finishing its review.",
    auth: authExternal({ ip: "198.51.100.24", signer: "brightline.example", firstContact: false }),
    bodyHtml: `<p>Avery,</p><p>Attaching the countersigned order form. Procurement has approved the annual term; our security team is finishing its review and we expect to close by Friday.</p><p>Could you confirm the billing contact and the PO reference when you have a moment?</p><p>Jordan</p><p class="sig">Jordan Blake<br><span>Brightline</span></p>`
  }));
  messages.push(message({
    id: "m5", threadId: "t2", folder: "sent", from: ME, to: JORDAN,
    subject: "Re: Brightline — signed order form and next steps",
    date: iso(2 * HOUR + 40 * MINUTE), unread: false, sizeBytes: 18 * KIB,
    inReplyTo: "<m4@a8m.example>",
    bodyHtml: bodyParagraphs(["Jordan,", "Received — thank you. Billing contact is Priya Shah; the PO reference is PO-2026-0418.", "Avery"])
  }));
  messages.push(message({
    id: "m6", threadId: "t2", folder: "inbox", from: JORDAN, to: ME,
    subject: "Re: Brightline — signed order form and next steps",
    date: iso(50 * MINUTE), unread: true, external: true,
    categories: ["sales"],
    snippet: "Perfect. Security signed off this morning — we are cleared to start onboarding Monday.",
    inReplyTo: "<m5@a8m.example>",
    auth: authExternal({ ip: "198.51.100.24", signer: "brightline.example" }),
    bodyHtml: bodyParagraphs(["Perfect. Security signed off this morning — we're cleared to start onboarding Monday.", "I'll ask our team to send the kickoff invite.", "Jordan"])
  }));

  // t3 — Contract renewal (3 messages)
  messages.push(message({
    id: "m7", threadId: "t3", folder: "inbox", from: MARCUS, to: ME,
    subject: "Re: Contract renewal — legal has signed off",
    date: iso(4 * HOUR + 15 * MINUTE), unread: true,
    categories: ["legal"], classification: "Confidential", sizeBytes: 42 * KIB,
    snippet: "Good news: the redlines are cleared and the countersigned copy is on its way.",
    bodyHtml: bodyParagraphs(["Avery —", "Good news from legal: the redlines are cleared and the countersigned copy is on its way. Nothing further is needed from your side this week. I'll file the executed agreement once it lands and send you the reference number.", "Marcus"])
  }));
  messages.push(message({
    id: "m8", threadId: "t3", folder: "sent", from: ME, to: MARCUS,
    subject: "Re: Contract renewal — legal has signed off",
    date: iso(3 * HOUR + 55 * MINUTE), unread: false, sizeBytes: 12 * KIB,
    inReplyTo: "<m7@a8m.example>",
    bodyHtml: bodyParagraphs(["Marcus,", "Thanks for pushing this through. Let me know when the countersigned copy is filed and I'll update the vendor record.", "Avery"])
  }));
  messages.push(message({
    id: "m9", threadId: "t3", folder: "inbox", from: MARCUS, to: ME,
    subject: "Re: Contract renewal — legal has signed off",
    date: iso(2 * HOUR + 20 * MINUTE), unread: false,
    categories: ["legal"], classification: "Confidential",
    snippet: "Filed under REF-2026-1019. I've copied the executed copy into the vendor folder.",
    inReplyTo: "<m8@a8m.example>",
    bodyHtml: bodyParagraphs(["Filed under REF-2026-1019. I've copied the executed copy into the vendor folder.", "Marcus"])
  }));

  // t4 — Design review (3 messages)
  messages.push(message({
    id: "m10", threadId: "t4", folder: "inbox", from: NORA, to: ME,
    subject: "Design review notes + updated flows",
    date: iso(6 * HOUR + 20 * MINUTE), unread: false, starred: true,
    attachments: [file("first-run-flows.pdf", "1.1 MB", "PDF")],
    snippet: "Thanks for the sharp feedback yesterday. I folded the empty-state notes into the flows and tightened the first-run copy.",
    bodyHtml: `<p>Hi Avery,</p><p>Thanks for the sharp feedback yesterday. I folded the empty-state notes into the flows and tightened the first-run copy. The updated prototype is linked in the doc.</p><p>Could you take another pass before Thursday? I'm most unsure about the second step — it may be doing too much for one screen.</p><p>Nora</p>${signature("Nora Patel", "Design Lead")}`
  }));
  messages.push(message({
    id: "m11", threadId: "t4", folder: "sent", from: ME, to: NORA,
    subject: "Re: Design review notes + updated flows",
    date: iso(5 * HOUR + 10 * MINUTE), unread: false, sizeBytes: 14 * KIB,
    inReplyTo: "<m10@a8m.example>",
    bodyHtml: bodyParagraphs(["Nora —", "The second step is close. I'd merge the permission prompt into step three and keep the copy short. I left two comments in the doc.", "Avery"])
  }));
  messages.push(message({
    id: "m12", threadId: "t4", folder: "inbox", from: NORA, to: ME,
    subject: "Re: Design review notes + updated flows",
    date: iso(65 * MINUTE), unread: true,
    snippet: "Merged the permission prompt into step three. One question about the mobile empty state.",
    inReplyTo: "<m11@a8m.example>",
    bodyHtml: bodyParagraphs(["Merged the permission prompt into step three. One question about the mobile empty state — should it mention search?", "Nora"])
  }));

  // t5 — Invoice approval (2 messages)
  messages.push(message({
    id: "m13", threadId: "t5", folder: "inbox", from: PRIYA, to: ME,
    subject: "Invoice #4821 is ready for approval",
    date: iso(8 * HOUR), unread: true,
    categories: ["finance"], classification: "Confidential", attachments: [file("invoice-4821.pdf", "312 KB", "PDF")],
    snippet: "Please review before Friday; the PO number is on the second page.",
    bodyHtml: bodyParagraphs(["Hi Avery,", "Invoice #4821 is ready for approval. Please review before Friday; the PO number is on the second page. If everything looks right, a one-line reply is enough and I'll route it to AP.", "Thanks, Priya"])
  }));
  messages.push(message({
    id: "m14", threadId: "t5", folder: "inbox", from: PRIYA, to: ME,
    subject: "Re: Invoice #4821 is ready for approval",
    date: iso(7 * HOUR + 15 * MINUTE), unread: false,
    categories: ["finance"], classification: "Confidential",
    snippet: "Quick reminder: AP closes the batch at 16:00 today.",
    inReplyTo: "<m13@a8m.example>",
    bodyHtml: bodyParagraphs(["Quick reminder: AP closes the batch at 16:00 today.", "Priya"])
  }));

  // Single-message threads
  messages.push(message({
    id: "m15", threadId: "t6", folder: "inbox", from: TOMAS, to: ME,
    subject: "Customer call recap: Brightline",
    date: iso(26 * HOUR), unread: false,
    snippet: "Three themes came up: onboarding speed, reporting depth, and the export limits on the team plan.",
    bodyHtml: bodyParagraphs(["Avery,", "Three themes came up on the Brightline call: onboarding speed, reporting depth, and the export limits on the team plan. I've written them up with quotes in the shared doc.", "Want to fold the export issue into Thursday's agenda? It sounds like the most urgent one.", "Tomás"])
  }));
  messages.push(message({
    id: "m16", threadId: "t7", folder: "inbox", from: LENA, to: ME,
    subject: "You're invited: Product offsite — Nov 14",
    date: iso(28 * HOUR), unread: true,
    categories: ["people"], sizeBytes: 22 * KIB,
    snippet: "Location is still TBD; I'll confirm once we have the headcount. Please add dietary notes by Monday.",
    event: { title: "Product offsite", day: "Friday, November 14", time: "09:00–17:00", where: "Alt8Mail HQ", attendees: 24, response: null },
    bodyHtml: bodyParagraphs(["Hi everyone,", "You're invited to the product offsite on Friday, November 14. Location is still TBD; I'll confirm once we have the headcount. Please add one dietary note to the sheet by Monday.", "I'll share the travel guidance in the same thread.", "Lena"])
  }));
  messages.push(message({
    id: "m17", threadId: "t8", folder: "inbox", from: DANIEL, to: ME,
    subject: "Weekly metrics — week 43",
    date: iso(30 * HOUR), unread: false, starred: true,
    categories: ["analytics"], attachments: [file("week-43-metrics.xlsx", "744 KB", "XLS")],
    snippet: "Active workspaces are up 4.2% week over week; churn is flat. The retention bump is mostly from shared inbox.",
    bodyHtml: bodyParagraphs(["Hi Avery,", "Active workspaces are up 4.2% week over week; churn is flat. The retention bump is mostly from the new shared-inbox cohort, which is holding better than we expected.", "Full numbers are in the attached sheet. Happy to walk through the cohort view on Thursday if useful.", "Daniel"])
  }));
  messages.push(message({
    id: "m18", threadId: "t9", folder: "inbox", from: AISHA, to: ME,
    subject: "Re: Welcome to the team",
    date: iso(3 * DAY + 2 * HOUR), unread: false,
    snippet: "Thrilled to have you aboard! Your first-week schedule is in the calendar, and I've paired you with Nora.",
    bodyHtml: bodyParagraphs(["Thrilled to have you aboard!", "Your first-week schedule is in the calendar, and I've paired you with Nora for the design walkthrough. Ping me if anything is missing.", "Aisha"])
  }));
  messages.push(message({
    id: "m19", threadId: "t10", folder: "inbox", from: IT, to: ME,
    subject: "Scheduled maintenance: Saturday 02:00–04:00 UTC",
    date: iso(4 * DAY + 3 * HOUR), unread: true,
    snippet: "Mail delivery will continue; the admin console may be briefly unavailable.",
    bodyHtml: bodyParagraphs(["Hello,", "Scheduled maintenance will take place this Saturday, 02:00–04:00 UTC. Mail delivery will continue; the admin console may be briefly unavailable during the window.", "No action is needed from you.", "— Alt8Mail IT"])
  }));
  messages.push(message({
    id: "m20", threadId: "t11", folder: "inbox", from: NADIA, to: ME,
    subject: "Photos from the offsite",
    date: iso(5 * DAY + 1 * HOUR), unread: false,
    attachments: [file("offsite-picks.zip", "18.2 MB", "ZIP"), file("offsite-team.svg", "1.8 MB", "IMG")],
    snippet: "Finally sorted the good ones. The album link expires in 30 days.",
    bodyHtml: bodyParagraphs(["Hi Avery,", "Finally sorted the good ones from the offsite. The album link expires in 30 days, and I've attached a smaller set for the deck.", "Let me know which ones you want to use and I'll send high-resolution versions.", "Nadia"])
  }));
  messages.push(message({
    id: "m21", threadId: "t12", folder: "inbox", from: FINANCE, to: ME,
    subject: "Your expense report was approved",
    date: iso(6 * DAY), unread: false,
    categories: ["finance"],
    snippet: "€418.60 will be reimbursed in the next payroll run.",
    bodyHtml: bodyParagraphs(["Hello Avery,", "Your expense report for the Berlin trip has been approved. €418.60 will be reimbursed in the next payroll run. No further action is needed from you.", "— Alt8Mail Finance"])
  }));

  // Archive
  messages.push(message({
    id: "a1", threadId: "ta1", folder: "archive", from: ANDREW, to: ME,
    subject: "Q3 retrospective notes",
    date: iso(9 * DAY), unread: false, starred: true,
    attachments: [file("q3-retro.pdf", "920 KB", "PDF")],
    snippet: "The recurring theme was too many handoffs late in the quarter.",
    bodyHtml: bodyParagraphs(["All,", "The recurring theme was too many handoffs late in the quarter. I've captured the actions and owners in the attached notes. We'll revisit them at the start of Q4.", "Andrew"])
  }));
  messages.push(message({
    id: "a2", threadId: "ta2", folder: "archive", from: LENA, to: ME,
    subject: "Benefits enrollment closes Friday",
    date: iso(14 * DAY), unread: false,
    snippet: "A quick reminder to review your selections before the window closes.",
    bodyHtml: bodyParagraphs(["Hi Avery,", "A quick reminder to review your benefits selections before the enrollment window closes on Friday. Your current choices carry over if you don't change anything.", "Lena"])
  }));
  messages.push(message({
    id: "a3", threadId: "ta3", folder: "archive", from: PRIYA, to: ME,
    subject: "2027 budget template",
    date: iso(20 * DAY), unread: false,
    attachments: [file("budget-2027.xlsx", "388 KB", "XLS")],
    snippet: "The template is ready for departmental submissions.",
    bodyHtml: bodyParagraphs(["Hello,", "The 2027 budget template is ready for departmental submissions. Please use the tabs marked for your team and return it by the end of the month.", "Priya"])
  }));

  // Spam
  messages.push(message({
    id: "sp1", threadId: "tsp1", folder: "spam", from: PARCEL, to: ME,
    subject: "Your parcel is waiting",
    date: iso(7 * HOUR), unread: true, external: true,
    snippet: "Confirm your details within 24 hours to arrange delivery.",
    auth: authExternal({ spf: "softfail", dkim: "none", dmarc: "fail", signer: "", ip: "203.0.113.77", warnings: ["Sender domain does not match the visible name", "First contact from this sender"] }),
    bodyHtml: bodyParagraphs(["Your parcel is being held. Confirm your details within 24 hours to arrange delivery."])
  }));
  messages.push(message({
    id: "sp2", threadId: "tsp2", folder: "spam", from: OFFERS, to: ME,
    subject: "Exclusive offer: 70% off",
    date: iso(2 * DAY), unread: true, external: true,
    snippet: "Today only — a selection of deals chosen for you.",
    auth: authExternal({ ip: "203.0.113.19", dmarc: "pass" }),
    bodyHtml: bodyParagraphs(["Today only: a selection of deals chosen for you. Unsubscribe at any time."])
  }));
  messages.push(message({
    id: "sp3", threadId: "tsp3", folder: "spam", from: PHISH, to: ME,
    subject: "Action required: re-verify your Alt8Mail account",
    date: iso(40 * MINUTE), unread: true, external: true, classification: "Confidential",
    snippet: "Your mailbox will be suspended unless you confirm your password today.",
    auth: authExternal({ spf: "fail", dkim: "none", dmarc: "fail", signer: "", ip: "192.0.2.201", warnings: ["SPF and DMARC failed", "Lookalike domain: a8m-security.example", "Urgency and credential request detected"] }),
    bodyHtml: bodyParagraphs(["Your mailbox will be suspended unless you confirm your password today.", "Open the secure form to keep access."])
  }));

  // Trash
  messages.push(message({
    id: "tr1", threadId: "ttr1", folder: "trash", from: DESIGNFILE, to: ME,
    subject: "This week in product design",
    date: iso(4 * DAY + 5 * HOUR), unread: false, external: true,
    snippet: "Five reads on onboarding, pricing pages and design systems.",
    auth: authExternal({ signer: "designweekly.example", ip: "198.51.100.8", dmarc: "pass" }),
    bodyHtml: bodyParagraphs(["Five reads on onboarding, pricing pages and design systems, selected by the Design Weekly editors."])
  }));

  // A server-backed draft restored into the compose dock on load.
  const draftBody = ["Lena —", "A few questions about the offsite:", "1. Do we have a travel budget per person?", "2. Should the customer-success team join the afternoon session?"].join("\n");
  messages.push(message({
    id: "draft-offsite", threadId: "tdraft1", folder: "drafts", from: ME, to: LENA,
    subject: "Offsite logistics",
    date: iso(30 * MINUTE), unread: false, draft: true, bodyText: draftBody,
    bodyHtml: bodyParagraphs(draftBody.split("\n").filter(Boolean)),
    sizeBytes: 6 * KIB, windowMode: "minimized", cc: [], bcc: [],
    priority: "normal", saveCopy: true
  }));

  const dynamicBytes = messages.reduce((sum, item) => sum + item.sizeBytes, 0);
  const desiredUsed = Math.round(2.4 * GIB);
  const baselineUsedBytes = Math.max(0, desiredUsed - dynamicBytes);

  const password = hashPassword(DEMO_PASSWORD);

  const sieve = {
    revision: 1,
    updatedAt: iso(2 * DAY),
    rules: [
      {
        id: "rule_invoices",
        name: "Archive invoices",
        enabled: true,
        priority: 1,
        match: "any",
        conditions: [
          { field: "subject", operator: "contains", value: "invoice" },
          { field: "body", operator: "contains", value: "invoice" }
        ],
        action: { type: "move", value: "archive" }
      },
      {
        id: "rule_brightline",
        name: "Flag Brightline mail",
        enabled: true,
        priority: 2,
        match: "all",
        conditions: [{ field: "from", operator: "contains", value: "brightline.example" }],
        action: { type: "star" }
      },
      {
        id: "rule_offers",
        name: "Discard obvious offers",
        enabled: false,
        priority: 3,
        match: "all",
        conditions: [{ field: "subject", operator: "contains", value: "70% off" }],
        action: { type: "discard" }
      }
    ],
    vacation: {
      enabled: false,
      start: "",
      end: "",
      days: 7,
      internal: {
        subject: "Out of office",
        message: "I'm away from the office and will reply when I return. For urgent matters, contact product@a8m.example."
      },
      external: {
        enabled: true,
        subject: "Away from the office",
        message: "Thanks for your message. I'm away and will reply when I return. For urgent matters, please contact product@a8m.example."
      }
    }
  };
  sieve.script = renderSieve(sieve, ME);

  const audit = [
    {
      id: "aud_seed_1", ts: iso(2 * HOUR + 10 * MINUTE), action: "auth.login", target: DEMO_EMAIL,
      actor: ME.name, detail: "Single sign-on · Chrome on Windows", ip: "203.0.113.24", userAgent: "Mozilla/5.0"
    },
    {
      id: "aud_seed_2", ts: iso(3 * HOUR + 30 * MINUTE), action: "send.committed", target: "Brightline — signed order form and next steps",
      actor: ME.name, detail: "Delivered via MX · 1 recipient", ip: "203.0.113.24", userAgent: "Mozilla/5.0"
    },
    {
      id: "aud_seed_3", ts: iso(1 * DAY + 2 * HOUR), action: "rule.saved", target: "Archive invoices",
      actor: ME.name, detail: "Sieve ruleset saved · revision 1", ip: "203.0.113.24", userAgent: "Mozilla/5.0"
    },
    {
      id: "aud_seed_4", ts: iso(2 * DAY + 4 * HOUR), action: "message.archive", target: "3 conversations",
      actor: ME.name, detail: "Conversations archived from Inbox", ip: "203.0.113.24", userAgent: "Mozilla/5.0"
    }
  ];

  const today = new Date();
  const dayKey = (offset) => {
    const date = new Date(today.getFullYear(), today.getMonth(), today.getDate() + offset);
    return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
  };
  const calendarEvents = [
    { id: "cal1", date: dayKey(0), start: "09:00", end: "09:30", title: "Inbox triage", color: "var(--cat-analytics)", where: "Focus time", attendees: 1 },
    { id: "cal2", date: dayKey(0), start: "10:00", end: "11:00", title: "Roadmap prep", color: "var(--cat-decision)", where: "Conference Room B · Teams", attendees: 6 },
    { id: "cal3", date: dayKey(0), start: "14:30", end: "15:00", title: "1:1 with Nora", color: "var(--cat-people)", where: "Teams", attendees: 2 },
    { id: "cal4", date: dayKey(1), start: "09:30", end: "10:00", title: "Finance review", color: "var(--cat-finance)", where: "Room 12A", attendees: 4 },
    { id: "cal5", date: dayKey(1), start: "13:00", end: "14:00", title: "Brightline customer call", color: "var(--cat-sales)", where: "Teams", attendees: 5 },
    { id: "cal6", date: dayKey(2), start: "10:00", end: "11:00", title: "Q4 roadmap review", color: "var(--cat-decision)", where: "Conference Room B · Teams", attendees: 6 },
    { id: "cal7", date: dayKey(2), start: "11:30", end: "12:00", title: "Product all-hands", color: "var(--cat-product)", where: "Auditorium", attendees: 38 },
    { id: "cal8", date: dayKey(3), start: "15:00", end: "15:45", title: "Offsite planning", color: "var(--cat-people)", where: "Teams", attendees: 4 },
    { id: "cal9", date: dayKey(6), start: "10:00", end: "11:30", title: "Board metrics review", color: "var(--cat-finance)", where: "Boardroom", attendees: 9 },
    { id: "cal10", date: dayKey(14), start: "09:00", end: "17:00", title: "Product offsite", color: "var(--cat-product)", where: "Alt8Mail HQ", attendees: 24 },
    { id: "cal11", date: dayKey(-2), start: "10:00", end: "10:30", title: "Weekly product sync", color: "var(--cat-product)", where: "Teams", attendees: 8 },
    { id: "cal12", date: dayKey(-5), start: "14:00", end: "15:00", title: "Design review", color: "var(--cat-product)", where: "Studio 2", attendees: 5 },
    { id: "cal13", date: dayKey(-7), start: "11:00", end: "12:00", title: "Company all-hands", color: "var(--cat-people)", where: "Auditorium", attendees: 64 }
  ];

  const task = (input) => ({
    id: input.id,
    title: input.title,
    notes: input.notes || "",
    status: input.status || "open",
    priority: input.priority || "normal",
    due: input.due || "",
    dueTime: input.dueTime || "",
    assignee: input.assignee || ME,
    createdBy: input.createdBy || ME.email,
    tags: input.tags || [],
    linkedThreadId: input.linkedThreadId || null,
    linkedSubject: input.linkedSubject || null,
    source: input.source || "manual",
    createdAt: input.createdAt || iso(2 * DAY),
    updatedAt: input.updatedAt || iso(2 * DAY),
    completedAt: input.completedAt || null
  });

  const tasks = [
    task({
      id: "task_invoice", title: "Approve invoice #4821", priority: "high",
      notes: "Route to AP after approval. The PO reference is on page two of the PDF.",
      due: dayKey(0), dueTime: "16:00", tags: ["Finance", "AP"],
      linkedThreadId: "t5", linkedSubject: "Invoice #4821 is ready for approval",
      source: "email", createdAt: iso(8 * HOUR), updatedAt: iso(8 * HOUR)
    }),
    task({
      id: "task_board_deck", title: "Prepare the board metrics review", priority: "urgent",
      notes: "Pull the week 43 cohort numbers and add the shared-inbox retention view.",
      due: dayKey(2), dueTime: "09:00", tags: ["Board", "Analytics"],
      linkedThreadId: "t8", linkedSubject: "Weekly metrics — week 43",
      source: "email", createdAt: iso(30 * HOUR), updatedAt: iso(5 * HOUR)
    }),
    task({
      id: "task_vendor_security", title: "Finish the Brightline security review", priority: "high",
      notes: "Confirm the security contact and file the countersigned order form in the vendor folder.",
      due: dayKey(1), dueTime: "12:00", tags: ["Vendor", "Security"],
      linkedThreadId: "t2", linkedSubject: "Brightline — signed order form and next steps",
      source: "email", createdAt: iso(3 * HOUR), updatedAt: iso(50 * MINUTE)
    }),
    task({
      id: "task_offsite", title: "Confirm offsite dietary notes", priority: "normal",
      notes: "Collect responses from the product team and send the final headcount to Lena.",
      due: dayKey(-1), tags: ["People", "Offsite"], assignee: LENA,
      linkedThreadId: "t7", linkedSubject: "You're invited: Product offsite — Nov 14",
      source: "email", createdAt: iso(28 * HOUR), updatedAt: iso(26 * HOUR)
    }),
    task({
      id: "task_design", title: "Merge permission prompt into step three", priority: "normal",
      notes: "Nora to share the updated prototype after the design review.",
      due: dayKey(3), tags: ["Design", "Product"], assignee: NORA,
      linkedThreadId: "t4", linkedSubject: "Design review notes + updated flows",
      source: "email", createdAt: iso(6 * HOUR), updatedAt: iso(1 * HOUR)
    }),
    task({
      id: "task_contract", title: "Update the vendor record for the contract renewal", priority: "normal",
      notes: "File the executed agreement under REF-2026-1019.",
      due: dayKey(4), tags: ["Legal", "Vendor"],
      linkedThreadId: "t3", linkedSubject: "Contract renewal — legal has signed off",
      source: "email", createdAt: iso(4 * HOUR), updatedAt: iso(2 * HOUR)
    }),
    task({
      id: "task_done_agenda", title: "Send the Q4 roadmap agenda", priority: "normal", status: "done",
      notes: "Agenda sent to the product leads on Wednesday morning.",
      due: dayKey(-2), tags: ["Product"], linkedThreadId: "t1",
      linkedSubject: "Q4 roadmap review — Thursday 10:00", source: "email",
      createdAt: iso(2 * DAY), updatedAt: iso(1 * DAY), completedAt: iso(1 * DAY)
    }),
    task({
      id: "task_done_onboarding", title: "Complete the design walkthrough with Nora", priority: "low", status: "done",
      notes: "Walkthrough completed; follow-up notes added to the onboarding doc.",
      due: dayKey(-3), tags: ["Onboarding", "Design"], assignee: NORA,
      createdAt: iso(4 * DAY), updatedAt: iso(3 * DAY), completedAt: iso(3 * DAY)
    })
  ];

  const textFile = (input) => ({
    id: input.id,
    name: input.name,
    type: input.type,
    mime: input.mime || MIME_BY_TYPE[input.type] || "application/octet-stream",
    size: input.size,
    sizeBytes: bytesFrom(input.size),
    providerId: input.providerId,
    path: input.path,
    owner: input.owner || ME,
    modifiedAt: input.modifiedAt || iso(2 * HOUR),
    createdAt: input.createdAt || iso(5 * DAY),
    starred: !!input.starred,
    sharedWith: input.sharedWith || [],
    locked: !!input.locked,
    version: input.version || "1",
    previewable: !!input.contentUrl,
    contentUrl: input.contentUrl || null,
    description: input.description || ""
  });

  const files = [
    textFile({ id: "file_roadmap", name: "Q4-roadmap-v3.pdf", type: "PDF", size: "2.4 MB", providerId: "a8m", path: "Product / Q4 planning", modifiedAt: iso(2 * HOUR), starred: true, sharedWith: ["elena.vasquez@a8m.example", "nora.patel@a8m.example", "aisha.bello@a8m.example"], contentUrl: "/samples/Q4-roadmap-v3.pdf", description: "Revised scope, launch checklist and open decisions." }),
    textFile({ id: "file_invoice", name: "invoice-4821.pdf", type: "PDF", size: "312 KB", providerId: "a8m", path: "Finance / 2026 / AP", modifiedAt: iso(8 * HOUR), sharedWith: ["priya.shah@a8m.example"], locked: true, contentUrl: "/samples/invoice-4821.pdf", description: "Northstar Research invoice awaiting approval." }),
    textFile({ id: "file_flows", name: "first-run-flows.pdf", type: "PDF", size: "1.1 MB", providerId: "a8m", path: "Design / Onboarding", modifiedAt: iso(6 * HOUR), sharedWith: ["nora.patel@a8m.example"], contentUrl: "/samples/first-run-flows.pdf", description: "Updated first-run flows for the design review." }),
    textFile({ id: "file_brightline", name: "Brightline-order-form.pdf", type: "PDF", size: "1.8 MB", providerId: "a8m", path: "Legal / Vendors / Brightline", modifiedAt: iso(3 * HOUR), sharedWith: ["marcus.webb@a8m.example", "priya.shah@a8m.example"], contentUrl: "/samples/Brightline-order-form.pdf", description: "Countersigned annual order form." }),
    textFile({ id: "file_retro", name: "q3-retro.pdf", type: "PDF", size: "920 KB", providerId: "a8m", path: "Product / Retrospectives", modifiedAt: iso(9 * DAY), contentUrl: "/samples/q3-retro.pdf", description: "Q3 retrospective actions and owners." }),
    textFile({ id: "file_offsite_svg", name: "offsite-team.svg", type: "IMG", mime: "image/svg+xml", size: "1.8 MB", providerId: "a8m", path: "People / Offsite 2026", modifiedAt: iso(5 * DAY), sharedWith: ["lena.fischer@a8m.example"], contentUrl: "/samples/offsite-team.svg", description: "Offsite visual for the team recap." }),
    textFile({ id: "file_brand", name: "Alt8Mail-brand-library.zip", type: "ZIP", size: "248 MB", providerId: "a8m", path: "Brand / Library", modifiedAt: iso(12 * DAY), description: "Logos, fonts and brand guidelines." }),
    textFile({ id: "file_offsite_plan", name: "Product-offsite-plan.docx", type: "DOC", size: "2.2 MB", providerId: "a8m", path: "People / Offsite 2026", modifiedAt: iso(4 * DAY), sharedWith: ["lena.fischer@a8m.example"], description: "Travel, agenda and dietary notes." }),
    textFile({ id: "file_board_pptx", name: "Board-metrics-Q4.pptx", type: "PPT", size: "8.4 MB", providerId: "onedrive", path: "Board / 2026", owner: ELENA, modifiedAt: iso(20 * HOUR), sharedWith: ["avery.chen@a8m.example"], description: "Board deck outline with cohort metrics." }),
    textFile({ id: "file_model", name: "Finance-model-2027.xlsx", type: "XLS", size: "1.4 MB", providerId: "onedrive", path: "Finance / Planning", owner: PRIYA, modifiedAt: iso(2 * DAY), sharedWith: ["avery.chen@a8m.example", "elena.vasquez@a8m.example"], locked: true, description: "Departmental planning model." }),
    textFile({ id: "file_contract", name: "Contract-renewal-signed.pdf", type: "PDF", size: "640 KB", providerId: "onedrive", path: "Legal / Contracts", owner: MARCUS, modifiedAt: iso(2 * DAY), sharedWith: ["avery.chen@a8m.example"], description: "Executed renewal agreement, REF-2026-1019." }),
    textFile({ id: "file_customer_notes", name: "Brightline-call-notes.docx", type: "DOC", size: "840 KB", providerId: "onedrive", path: "Customer success / Brightline", owner: TOMAS, modifiedAt: iso(26 * HOUR), sharedWith: ["avery.chen@a8m.example"], description: "Recap and themes from the customer call." })
  ];

  const storageProviders = [
    { id: "a8m", name: "Alt8Mail Drive", kind: "internal", connected: true, account: DEMO_EMAIL, usedBytes: 12.4 * GIB, limitBytes: 100 * GIB, color: "#0f6cbd", status: "synced", lastSyncAt: iso(4 * MINUTE), description: "Company-managed, encrypted at rest." },
    { id: "onedrive", name: "Microsoft OneDrive", kind: "oauth", connected: true, account: "avery.chen@a8m.example", usedBytes: 286 * GIB, limitBytes: 1024 * GIB, color: "#0364b8", status: "synced", lastSyncAt: iso(2 * MINUTE), description: "Business account · Microsoft 365 tenant." },
    { id: "dropbox", name: "Dropbox Business", kind: "oauth", connected: false, account: "", usedBytes: 0, limitBytes: 2 * 1024 * GIB, color: "#0061ff", status: "disconnected", lastSyncAt: null, description: "Connect a Dropbox Business team folder." },
    { id: "gdrive", name: "Google Drive", kind: "oauth", connected: false, account: "", usedBytes: 0, limitBytes: 2 * 1024 * GIB, color: "#188038", status: "disconnected", lastSyncAt: null, description: "Connect Google Workspace shared drives." },
    { id: "s3", name: "AWS S3", kind: "s3", connected: false, account: "", usedBytes: 0, limitBytes: 0, color: "#d97706", status: "disconnected", lastSyncAt: null, description: "Bring your own bucket with IAM role access." }
  ];

  return {
    version: 2,
    createdAt: new Date(NOW).toISOString(),
    user: {
      ...ME,
      timezone: "America/Los_Angeles",
      password,
      quota: { limitBytes: 15 * GIB, baselineUsedBytes },
      aliases: [
        { id: "primary", email: DEMO_EMAIL, label: "Avery Chen", kind: "primary", canSend: true, reason: "" },
        { id: "short", email: "a.chen@a8m.example", label: "Avery Chen (short)", kind: "alias", canSend: true, reason: "" },
        { id: "product", email: "product@a8m.example", label: "Product Team", kind: "shared", canSend: true, reason: "" },
        { id: "support", email: "support@a8m.example", label: "Support queue", kind: "shared", canSend: false, reason: "Requires delegated access" }
      ],
      trustedSenders: [],
      outbound: { limit: 200, windowHours: 24, baselineUsed: 37, events: [] },
      preferences: {
        language: "en-US",
        timeFormat: "12h",
        dateFormat: "auto",
        timeZone: "America/Los_Angeles",
        weekStart: "monday",
        workStart: "09:00",
        workEnd: "17:00",
        workDays: [1, 2, 3, 4, 5],
        defaultFrom: DEMO_EMAIL,
        signatureEnabled: true,
        signature: "Avery Chen\nVP Product · Alt8Mail\n+1 (415) 555-0101",
        autoAdvance: false
      },
      security: {
        twoFactor: {
          enabled: true,
          enabledAt: iso(30 * DAY),
          totpSecret: "JBSWY3DPEHPK3PXP",
          passkeys: [
            { id: "pk_windows_hello", name: "Windows Hello · Surface Laptop", createdAt: iso(30 * DAY), lastUsedAt: iso(2 * HOUR) },
            { id: "pk_phone", name: "iPhone · Face ID", createdAt: iso(14 * DAY), lastUsedAt: iso(6 * DAY) }
          ],
          recoveryCodes: [
            { code: "A8M-7F2K-9Q4A", used: false },
            { code: "A8M-3N8P-2L6C", used: false },
            { code: "A8M-9T4X-1V5B", used: false },
            { code: "A8M-5R7H-8M2D", used: false }
          ]
        }
      }
    },
    folders: FOLDERS,
    people: PEOPLE,
    messages,
    sieve,
    audit,
    outbox: [],
    undo: [],
    calendarEvents,
    tasks,
    files,
    storageProviders,
    attachments: {}
  };
}
