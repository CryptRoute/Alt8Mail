import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const outDir = path.resolve(__dirname, "..", "public", "samples");
fs.mkdirSync(outDir, { recursive: true });

function escapePdfText(value) {
  return String(value).replace(/\\/g, "\\\\").replace(/\(/g, "\\(").replace(/\)/g, "\\)");
}

function buildPdf({ title, subtitle, lines, accent = [15, 108, 189] }) {
  const content = [];
  content.push("q");
  content.push(`${accent[0] / 255} ${accent[1] / 255} ${accent[2] / 255} rg`);
  content.push("0 742 612 50 re f");
  content.push("Q");
  content.push("BT /F1 24 Tf 48 714 Td (" + escapePdfText(title) + ") Tj ET");
  content.push("BT /F1 11 Tf 48 688 Td 0.42 0.47 0.55 rg (" + escapePdfText(subtitle) + ") Tj 0 0 0 rg ET");
  let y = 636;
  for (const line of lines) {
    if (line === "") {
      y -= 12;
      continue;
    }
    const bold = line.startsWith("## ");
    const text = bold ? line.slice(3) : line;
    content.push(`BT /F1 ${bold ? 13 : 11} Tf 48 ${y} Td (${escapePdfText(text)}) Tj ET`);
    y -= bold ? 24 : 18;
  }
  content.push("q 0.82 0.85 0.89 rg 48 64 516 1 re f Q");
  content.push("BT /F1 8 Tf 48 48 Td 0.48 0.52 0.58 rg (Alt8Mail - demo attachment - generated for the Aurora template) Tj ET");

  const objects = [
    "<< /Type /Catalog /Pages 2 0 R >>",
    "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
    "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
    "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
    `<< /Length ${Buffer.byteLength(content.join("\n"))} >>\nstream\n${content.join("\n")}\nendstream`
  ];
  let pdf = "%PDF-1.4\n";
  const offsets = [];
  objects.forEach((body, index) => {
    offsets.push(Buffer.byteLength(pdf));
    pdf += `${index + 1} 0 obj\n${body}\nendobj\n`;
  });
  const xref = Buffer.byteLength(pdf);
  pdf += `xref\n0 ${objects.length + 1}\n0000000000 65535 f \n`;
  for (const offset of offsets) pdf += `${String(offset).padStart(10, "0")} 00000 n \n`;
  pdf += `trailer\n<< /Size ${objects.length + 1} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`;
  return Buffer.from(pdf, "binary");
}

const pdfs = [
  {
    file: "Q4-roadmap-v3.pdf",
    title: "Q4 Roadmap Review",
    subtitle: "Alt8Mail Product · Thursday 10:00 · Conference Room B",
    accent: [15, 108, 189],
    lines: [
      "## Overview",
      "The revised scope trims the wish list to what we can ship before the holidays.",
      "",
      "## Open questions",
      "1. Keep the shared-inbox beta in Q4, or move it to January?",
      "2. Is the analytics export a launch blocker, or can it follow a week later?",
      "",
      "## Launch scope",
      "Shared inbox beta       Keep in Q4",
      "Analytics export         Week 1, January",
      "Mobile reader polish     In scope",
      "Admin audit export       Follow-up",
      "",
      "Owner: Elena Vasquez · Product",
      "Reviewers: Avery Chen, Nora Patel, Aisha Bello"
    ]
  },
  {
    file: "Brightline-order-form.pdf",
    title: "Brightline Annual Order Form",
    subtitle: "Countersigned · Procurement approved",
    accent: [46, 125, 91],
    lines: [
      "## Customer",
      "Brightline, Inc. · jordan.blake@brightline.example",
      "Annual term · 24 seats · Production workspace",
      "",
      "## Commercials",
      "Platform subscription        $48,000 / year",
      "Implementation services      $6,500 one-time",
      "Billing contact              Priya Shah, Finance",
      "PO reference                 PO-2026-0418",
      "",
      "## Signature",
      "Jordan Blake, Brightline                         Date: 2026-10-27",
      "Dana Whitfield, Alt8Mail                         Date: 2026-10-27",
      "",
      "Security review complete. Onboarding begins Monday."
    ]
  },
  {
    file: "first-run-flows.pdf",
    title: "First-run Flows",
    subtitle: "Design review notes · Nora Patel",
    accent: [99, 70, 216],
    lines: [
      "## Goals",
      "Make the first five minutes feel calm, short and reversible.",
      "",
      "## Flow",
      "1. Welcome and workspace identity",
      "2. Permissions merged into step three",
      "3. Inbox preview with three sample conversations",
      "4. Keyboard shortcut card and directory handoff",
      "",
      "## Design notes",
      "Empty states carry a single action.",
      "Copy stays under 14 words per line.",
      "Mobile keeps one decision per screen.",
      "",
      "Open question: should the mobile empty state mention search?"
    ]
  },
  {
    file: "invoice-4821.pdf",
    title: "Invoice 4821",
    subtitle: "Alt8Mail Finance · due Friday",
    accent: [122, 74, 92],
    lines: [
      "## Vendor",
      "Northstar Research · 244 Market Street, San Francisco",
      "Invoice date       2026-10-21",
      "Due date           2026-10-30",
      "PO reference       PO-2026-0392",
      "",
      "## Lines",
      "Customer interviews            12 x $450       $5,400.00",
      "Synthesis workshop              1 x $1,800     $1,800.00",
      "Travel and materials                           $418.60",
      "Subtotal                                       $7,618.60",
      "Tax                                            $0.00",
      "Total due                                      $7,618.60",
      "",
      "Route to AP after one-line approval."
    ]
  },
  {
    file: "q3-retro.pdf",
    title: "Q3 Retrospective",
    subtitle: "Product and Engineering · actions and owners",
    accent: [71, 107, 79],
    lines: [
      "## What worked",
      "Weekly scope reviews reduced late surprises.",
      "Design handoffs started earlier in the cycle.",
      "",
      "## What to change",
      "Too many handoffs late in the quarter.",
      "Analytics requests lacked a single intake path.",
      "Incident follow-ups need a visible owner.",
      "",
      "## Actions",
      "Create a single intake form                 Elena",
      "Move design review to week two             Nora",
      "Add incident owner to weekly metrics       Daniel",
      "",
      "Revisit at the start of Q4."
    ]
  }
];

for (const pdf of pdfs) {
  const buffer = buildPdf(pdf);
  fs.writeFileSync(path.join(outDir, pdf.file), buffer);
  console.log(pdf.file, buffer.length, "bytes");
}

const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="800" viewBox="0 0 1200 800" role="img" aria-label="Alt8Mail offsite in Berlin">
  <defs>
    <linearGradient id="sky" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="#0b3260"/>
      <stop offset=".52" stop-color="#2d6fa8"/>
      <stop offset="1" stop-color="#f4b183"/>
    </linearGradient>
    <linearGradient id="hill" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="#17324c"/>
      <stop offset="1" stop-color="#0a1826"/>
    </linearGradient>
  </defs>
  <rect width="1200" height="800" fill="url(#sky)"/>
  <circle cx="910" cy="210" r="74" fill="#ffe1ad" opacity=".9"/>
  <path d="M0 590 180 430 320 560 470 390 660 590Z" fill="#28506f" opacity=".8"/>
  <path d="M380 620 610 430 770 580 930 450 1200 650 1200 800 380 800Z" fill="url(#hill)"/>
  <path d="M0 680 170 560 300 660 520 560 720 700 930 610 1200 720 1200 800 0 800Z" fill="#07121d"/>
  <g fill="#7fb6e8" opacity=".55">
    <circle cx="160" cy="170" r="3"/><circle cx="250" cy="120" r="2.2"/><circle cx="370" cy="200" r="2.6"/>
    <circle cx="520" cy="130" r="2"/><circle cx="690" cy="190" r="2.4"/><circle cx="1080" cy="160" r="2.8"/>
    <circle cx="800" cy="110" r="1.8"/><circle cx="1020" cy="90" r="2.2"/>
  </g>
  <text x="70" y="110" fill="#f6fbff" font-family="Segoe UI, Arial, sans-serif" font-size="54" font-weight="600" letter-spacing="-2">Alt8Mail offsite</text>
  <text x="72" y="158" fill="#c7ddf2" font-family="Segoe UI, Arial, sans-serif" font-size="22">Berlin · Product team · 2026</text>
  <g fill="#f6fbff" opacity=".82" font-family="Segoe UI, Arial, sans-serif" font-size="15">
    <text x="72" y="720">Photo placeholder for the Aurora template attachment preview.</text>
  </g>
</svg>`;
fs.writeFileSync(path.join(outDir, "offsite-team.svg"), svg);
console.log("offsite-team.svg", Buffer.byteLength(svg), "bytes");

const notes = `# Sample attachments

These files are generated by \`tools/make-samples.mjs\` for the attachment
preview demo. They are deterministic and contain no real customer data.
`;
fs.writeFileSync(path.join(outDir, "README.md"), notes);
