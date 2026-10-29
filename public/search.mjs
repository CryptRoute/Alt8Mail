/* ============================================================
   Alt8Mail — shared search + thread index
   Runs unchanged in the browser and in Node (server).
   No DOM access; safe to import from both environments.
   ============================================================ */

const FIELD_OPERATORS = new Set([
  "from", "to", "cc", "subject", "body", "filename", "attachment",
  "has", "before", "after", "is", "in", "larger", "smaller"
]);

const SIZE_UNITS = { b: 1, k: 1024, kb: 1024, m: 1024 * 1024, mb: 1024 * 1024, g: 1024 * 1024 * 1024, gb: 1024 * 1024 * 1024 };

export function stripHtml(html) {
  return decodeEntities(
    String(html || "")
      .replace(/<style[\s\S]*?<\/style>/gi, " ")
      .replace(/<script[\s\S]*?<\/script>/gi, " ")
      .replace(/<br\s*\/?>/gi, " ")
      .replace(/<\/(p|div|li|tr|h[1-6])>/gi, " ")
      .replace(/<[^>]+>/g, " ")
  )
    .replace(/\s+/g, " ")
    .trim();
}

function decodeEntities(value) {
  return String(value)
    .replace(/&nbsp;/gi, " ")
    .replace(/&amp;/gi, "&")
    .replace(/&lt;/gi, "<")
    .replace(/&gt;/gi, ">")
    .replace(/&quot;/gi, '"')
    .replace(/&#39;/gi, "'")
    .replace(/&#x27;/gi, "'");
}

export function normalize(value) {
  return String(value == null ? "" : value).toLowerCase();
}

export function tokenize(value) {
  return normalize(value)
    .split(/[^\p{L}\p{N}@._-]+/u)
    .map((token) => token.replace(/^[._-]+|[._-]+$/g, ""))
    .filter(Boolean);
}

function parseDate(value) {
  const raw = String(value || "").trim();
  if (!/^\d{4}[-/]\d{1,2}[-/]\d{1,2}$/.test(raw)) return null;
  const [year, month, day] = raw.split(/[-/]/).map(Number);
  if (month < 1 || month > 12 || day < 1 || day > 31) return null;
  const stamp = Date.UTC(year, month - 1, day);
  if (!Number.isFinite(stamp)) return null;
  return stamp;
}

export function parseSize(value) {
  const match = String(value || "").trim().toLowerCase().match(/^(\d+(?:\.\d+)?)\s*(b|k|kb|m|mb|g|gb)?$/);
  if (!match) return null;
  return Math.round(Number(match[1]) * (SIZE_UNITS[match[2] || "b"] || 1));
}

/**
 * Parse a Gmail-style query. Recognised operators are stripped from free
 * text; unrecognised "word:value" pairs remain searchable text.
 */
export function parseQuery(raw) {
  const query = String(raw == null ? "" : raw).trim();
  const parsed = {
    raw: query,
    textTerms: [],
    from: [],
    to: [],
    cc: [],
    subject: [],
    body: [],
    filename: [],
    hasAttachment: false,
    before: null,
    after: null,
    unread: null,
    starred: null,
    folder: null,
    larger: null,
    smaller: null,
    errors: []
  };
  if (!query) return parsed;

  const re = /([\p{L}\p{N}_-]+):("([^"]*)"|(\S+))|"([^"]*)"|(\S+)/gu;
  let match;
  while ((match = re.exec(query)) !== null) {
    if (match[1]) {
      const operator = match[1].toLowerCase();
      const value = match[3] !== undefined ? match[3] : match[4];
      if (!FIELD_OPERATORS.has(operator)) {
        parsed.textTerms.push(...tokenize(match[0]));
        continue;
      }
      if (!value) {
        parsed.errors.push(`“${operator}:” needs a value`);
        continue;
      }
      switch (operator) {
        case "from":
          parsed.from.push(normalize(value));
          break;
        case "to":
          parsed.to.push(normalize(value));
          break;
        case "cc":
          parsed.cc.push(normalize(value));
          break;
        case "subject":
          parsed.subject.push(normalize(value));
          break;
        case "body":
          parsed.body.push(normalize(value));
          break;
        case "filename":
        case "attachment":
          parsed.filename.push(normalize(value));
          break;
        case "has": {
          const kind = normalize(value);
          if (kind === "attachment" || kind === "attachments" || kind === "file" || kind === "files") {
            parsed.hasAttachment = true;
          } else if (/^\.?[a-z0-9]{1,8}$/.test(kind)) {
            parsed.filename.push("." + kind.replace(/^\./, ""));
          } else {
            parsed.errors.push(`Unknown has: value “${value}”`);
          }
          break;
        }
        case "before": {
          const stamp = parseDate(value);
          if (stamp == null) parsed.errors.push(`Use a date like 2026-10-31 for before:`);
          else parsed.before = stamp;
          break;
        }
        case "after": {
          const stamp = parseDate(value);
          if (stamp == null) parsed.errors.push(`Use a date like 2026-10-01 for after:`);
          else parsed.after = stamp;
          break;
        }
        case "is": {
          const kind = normalize(value);
          if (kind === "unread") parsed.unread = true;
          else if (kind === "read") parsed.unread = false;
          else if (kind === "starred" || kind === "flagged") parsed.starred = true;
          else if (kind === "unstarred") parsed.starred = false;
          else parsed.errors.push(`Unknown is: value “${value}”`);
          break;
        }
        case "in":
          parsed.folder = normalize(value);
          break;
        case "larger": {
          const bytes = parseSize(value);
          if (bytes == null) parsed.errors.push(`Use a size like 1M for larger:`);
          else parsed.larger = bytes;
          break;
        }
        case "smaller": {
          const bytes = parseSize(value);
          if (bytes == null) parsed.errors.push(`Use a size like 500k for smaller:`);
          else parsed.smaller = bytes;
          break;
        }
        default:
          parsed.textTerms.push(...tokenize(match[0]));
      }
    } else if (match[5]) {
      parsed.textTerms.push(...tokenize(match[5]));
    } else if (match[6]) {
      parsed.textTerms.push(...tokenize(match[6]));
    }
  }
  return parsed;
}

function addressText(list) {
  const values = Array.isArray(list) ? list : list ? [list] : [];
  return values
    .map((person) => `${person && person.name ? person.name : ""} ${person && person.email ? person.email : ""}`)
    .join(" ")
    .toLowerCase();
}

function messageBodyText(message) {
  if (message.bodyText) return String(message.bodyText);
  return stripHtml(message.bodyHtml || message.body || "");
}

function attachmentText(message) {
  return (message.attachments || []).map((file) => `${file.name || ""} ${file.type || ""}`).join(" ").toLowerCase();
}

function docFromMessage(message) {
  const body = messageBodyText(message);
  const from = addressText(message.from);
  const to = addressText(message.to);
  const cc = addressText(message.cc);
  const subject = normalize(message.subject);
  const attachments = attachmentText(message);
  const haystack = [from, to, cc, subject, body, attachments].join(" \n ");
  const tokens = Array.from(new Set(tokenize(haystack)));
  return {
    message,
    from,
    to,
    cc,
    subject,
    body: normalize(body),
    attachments,
    haystack,
    tokens,
    tokenSet: new Set(tokens),
    dateMs: Date.parse(message.date) || 0,
    sizeBytes: Number(message.sizeBytes) || 0,
    unread: !!message.unread,
    starred: !!message.starred,
    hasAttachment: (message.attachments || []).length > 0,
    folder: normalize(message.folder)
  };
}

/**
 * In-memory inverted index over a mailbox. Built once, kept current by the
 * caller. Free-text terms use AND + prefix matching; operators narrow the set.
 */
export class MailIndex {
  constructor(messages) {
    this.messages = Array.isArray(messages) ? messages : [];
    this.docs = this.messages.map(docFromMessage);
    this.postings = new Map();
    this.docs.forEach((doc, index) => {
      for (const token of doc.tokenSet) {
        let bucket = this.postings.get(token);
        if (!bucket) {
          bucket = new Set();
          this.postings.set(token, bucket);
        }
        bucket.add(index);
      }
    });
  }

  _candidates(terms) {
    if (!terms.length) return this.docs.map((_, index) => index);
    let result = null;
    for (const term of terms) {
      const matches = new Set();
      for (const [token, bucket] of this.postings) {
        if (token.startsWith(term)) {
          for (const index of bucket) matches.add(index);
        }
      }
      if (result === null) result = matches;
      else {
        for (const index of Array.from(result)) {
          if (!matches.has(index)) result.delete(index);
        }
      }
      if (!result.size) break;
    }
    return Array.from(result || []);
  }

  search(raw) {
    const parsed = parseQuery(raw);
    const indexes = this._candidates(parsed.textTerms);
    const results = [];
    for (const index of indexes) {
      const doc = this.docs[index];
      if (parsed.from.length && !parsed.from.some((value) => doc.from.includes(value))) continue;
      if (parsed.to.length && !parsed.to.some((value) => doc.to.includes(value))) continue;
      if (parsed.cc.length && !parsed.cc.some((value) => doc.cc.includes(value))) continue;
      if (parsed.subject.length && !parsed.subject.some((value) => doc.subject.includes(value))) continue;
      if (parsed.body.length && !parsed.body.some((value) => doc.body.includes(value))) continue;
      if (parsed.filename.length && !parsed.filename.some((value) => doc.attachments.includes(value))) continue;
      if (parsed.hasAttachment && !doc.hasAttachment) continue;
      if (parsed.unread !== null && doc.unread !== parsed.unread) continue;
      if (parsed.starred !== null && doc.starred !== parsed.starred) continue;
      if (parsed.before !== null && doc.dateMs >= parsed.before) continue;
      if (parsed.after !== null && doc.dateMs < parsed.after) continue;
      if (parsed.larger !== null && doc.sizeBytes <= parsed.larger) continue;
      if (parsed.smaller !== null && doc.sizeBytes >= parsed.smaller) continue;
      if (parsed.folder && parsed.folder !== "all" && parsed.folder !== "anywhere" && doc.folder !== parsed.folder) continue;
      results.push(doc.message);
    }
    results.sort((a, b) => (Date.parse(b.date) || 0) - (Date.parse(a.date) || 0));
    return { parsed, messages: results };
  }
}

export function searchMessages(messages, raw) {
  return new MailIndex(messages).search(raw);
}

/**
 * Group messages into conversations. `scope` decides which threads are
 * listed; `allMessages` (defaulting to scope) supplies every message in those
 * threads so the reader can render the full conversation.
 */
export function buildThreads(scope, allMessages) {
  const all = Array.isArray(allMessages) ? allMessages : scope;
  const byThread = new Map();
  for (const message of all) {
    const threadId = message.threadId || message.id;
    let bucket = byThread.get(threadId);
    if (!bucket) {
      bucket = [];
      byThread.set(threadId, bucket);
    }
    bucket.push(message);
  }
  const listed = new Set();
  const threads = [];
  for (const seed of scope) {
    const threadId = seed.threadId || seed.id;
    if (listed.has(threadId)) continue;
    listed.add(threadId);
    const members = (byThread.get(threadId) || [seed]).slice();
    members.sort((a, b) => (Date.parse(a.date) || 0) - (Date.parse(b.date) || 0));
    const latest = members[members.length - 1];
    const participants = [];
    const seenPeople = new Set();
    for (const message of members) {
      const people = message.from && message.from.email !== "avery.chen@a8m.example"
        ? [message.from]
        : Array.isArray(message.to) ? message.to : message.to ? [message.to] : [];
      for (const person of people) {
        if (!person || !person.email || seenPeople.has(person.email)) continue;
        seenPeople.add(person.email);
        participants.push(person);
      }
    }
    const folders = Array.from(new Set(members.map((message) => message.folder)));
    threads.push({
      id: threadId,
      messages: members,
      latest,
      subject: latest.subject || "(no subject)",
      participants,
      unreadCount: members.filter((message) => message.unread).length,
      starred: members.some((message) => message.starred),
      flagged: members.some((message) => message.flagged),
      pinned: members.some((message) => message.pinned),
      hasAttachments: members.some((message) => (message.attachments || []).length),
      folderSet: folders,
      date: latest.date,
      count: members.length
    });
  }
  threads.sort((a, b) => (Date.parse(b.date) || 0) - (Date.parse(a.date) || 0));
  return threads;
}
