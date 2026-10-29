/* ============================================================
   Sieve rule validation + script generation
   The UI edits structured rules; this module is the server-side
   source of truth for the script handed to Dovecot's managesieve.
   ============================================================ */

export const SIEVE_FIELDS = ["from", "to", "subject", "body", "size"];
export const SIEVE_OPERATORS = ["contains", "is", "matches", "notcontains", "over", "under"];
export const SIEVE_ACTIONS = ["move", "markRead", "star", "discard", "redirect", "keep"];
export const MOVE_FOLDERS = ["inbox", "archive", "spam", "trash"];

function quote(value) {
  return `"${String(value).replace(/\\/g, "\\\\").replace(/"/g, '\\"').replace(/\r/g, "").replace(/\n/g, "\\n")}"`;
}

function validDate(value) {
  return /^\d{4}-\d{2}-\d{2}$/.test(String(value || ""));
}

function conditionTest(condition) {
  const { field, operator, value } = condition;
  if (field === "size") {
    const test = operator === "under" ? "under" : "over";
    return `size :${test} ${Number(value)}K`;
  }
  const op = operator === "is" ? ":is" : operator === "matches" ? ":matches" : ":contains";
  let test;
  if (field === "from" || field === "to") {
    test = `address ${op} ${quote(field)} ${quote(value)}`;
  } else if (field === "body") {
    test = `body ${op} ${quote(value)}`;
  } else {
    test = `header ${op} ${quote(field === "subject" ? "subject" : field)} ${quote(value)}`;
  }
  return operator === "notcontains" ? `not ${test}` : test;
}

function renderAction(action) {
  switch (action.type) {
    case "move":
      return [`fileinto ${quote(action.value)};`, "stop;"];
    case "markRead":
      return ['addflag "\\\\Seen";'];
    case "star":
      return ['addflag "\\\\Flagged";'];
    case "discard":
      return ["discard;", "stop;"];
    case "redirect":
      return [`redirect ${quote(action.value)};`, "stop;"];
    case "keep":
    default:
      return ["keep;", "stop;"];
  }
}

export function normalizeSieve(input, options = {}) {
  const errors = [];
  const folders = options.folders || MOVE_FOLDERS;
  if (!input || typeof input !== "object") {
    return { errors: ["Rules payload is required"], rules: [], vacation: null };
  }
  const sourceRules = Array.isArray(input.rules) ? input.rules : [];
  if (sourceRules.length > 50) errors.push("A maximum of 50 rules is allowed");
  const names = new Set();
  const rules = [];
  sourceRules.forEach((raw, index) => {
    const label = `Rule ${index + 1}`;
    if (!raw || typeof raw !== "object") {
      errors.push(`${label}: invalid rule`);
      return;
    }
    const name = String(raw.name || "").trim();
    if (!name) errors.push(`${label}: a name is required`);
    else if (name.length > 80) errors.push(`${label}: name is too long`);
    else if (names.has(name.toLowerCase())) errors.push(`${label}: duplicate name “${name}”`);
    else names.add(name.toLowerCase());

    const match = raw.match === "any" ? "any" : "all";
    const conditions = Array.isArray(raw.conditions) ? raw.conditions : [];
    if (!conditions.length) errors.push(`${label}: at least one condition is required`);
    if (conditions.length > 10) errors.push(`${label}: a maximum of 10 conditions is allowed`);
    const normalizedConditions = conditions.map((condition, conditionIndex) => {
      const field = SIEVE_FIELDS.includes(condition && condition.field) ? condition.field : null;
      const operator = SIEVE_OPERATORS.includes(condition && condition.operator) ? condition.operator : null;
      let value = condition && condition.value != null ? String(condition.value).trim() : "";
      if (!field) errors.push(`${label}: condition ${conditionIndex + 1} has an unknown field`);
      if (!operator) errors.push(`${label}: condition ${conditionIndex + 1} has an unknown operator`);
      if (field === "size") {
        if (operator !== "over" && operator !== "under") errors.push(`${label}: size conditions use over or under`);
        const numeric = Number(value);
        if (!Number.isFinite(numeric) || numeric <= 0) errors.push(`${label}: size must be a positive number of KB`);
        else value = numeric;
      } else if (!value) {
        errors.push(`${label}: condition ${conditionIndex + 1} needs a value`);
      } else if (value.length > 200) {
        errors.push(`${label}: condition value is too long`);
      }
      return { field, operator, value };
    });

    const rawAction = raw.action || {};
    const type = SIEVE_ACTIONS.includes(rawAction.type) ? rawAction.type : null;
    if (!type) errors.push(`${label}: choose an action`);
    let actionValue = rawAction.value != null ? String(rawAction.value).trim() : "";
    if (type === "move") {
      if (!folders.includes(actionValue)) errors.push(`${label}: choose a valid destination folder`);
    } else if (type === "redirect") {
      if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(actionValue)) errors.push(`${label}: redirect needs a valid email address`);
    } else {
      actionValue = "";
    }

    rules.push({
      id: typeof raw.id === "string" && /^[A-Za-z0-9_-]{3,64}$/.test(raw.id) ? raw.id : `rule_${index + 1}`,
      name,
      enabled: raw.enabled !== false,
      priority: Number.isFinite(Number(raw.priority)) ? Number(raw.priority) : index + 1,
      match,
      conditions: normalizedConditions,
      action: { type, value: actionValue }
    });
  });

  let vacation = null;
  if (input.vacation && typeof input.vacation === "object") {
    const v = input.vacation;
    const days = Math.max(1, Math.min(30, Number(v.days) || 7));
    const start = String(v.start || "").trim();
    const end = String(v.end || "").trim();
    const internalRaw = v.internal && typeof v.internal === "object" ? v.internal : { subject: v.subject, message: v.message };
    const externalRaw = v.external && typeof v.external === "object" ? v.external : { enabled: false };
    const internal = {
      subject: String(internalRaw.subject || "Out of office").trim().slice(0, 200),
      message: String(internalRaw.message || "").trim()
    };
    const external = {
      enabled: !!externalRaw.enabled,
      subject: String(externalRaw.subject || "Away from the office").trim().slice(0, 200),
      message: String(externalRaw.message || "").trim()
    };
    if (v.enabled && !internal.message) errors.push("Automatic replies: an inside-organization message is required");
    if (v.enabled && external.enabled && !external.message) errors.push("Automatic replies: an outside-organization message is required");
    if (internal.message.length > 4000 || external.message.length > 4000) errors.push("Automatic replies: message is too long");
    if (start && !validDate(start)) errors.push("Automatic replies: start date is invalid");
    if (end && !validDate(end)) errors.push("Automatic replies: end date is invalid");
    if (start && end && end < start) errors.push("Automatic replies: end date must be after the start date");
    vacation = { enabled: !!v.enabled, start, end, days, internal, external };
  }

  return { errors, rules, vacation };
}

export function renderSieve(sieve, user) {
  const rules = Array.isArray(sieve && sieve.rules) ? sieve.rules : [];
  const vacation = sieve && sieve.vacation;
  const requires = new Set();
  for (const rule of rules) {
    if (!rule.enabled) continue;
    if (rule.action.type === "move") requires.add("fileinto");
    if (rule.action.type === "markRead" || rule.action.type === "star") requires.add("imap4flags");
    if (rule.conditions.some((condition) => condition.field === "body")) requires.add("body");
  }
  if (vacation && vacation.enabled) {
    requires.add("vacation");
    if (vacation.start || vacation.end) requires.add("date");
  }
  const lines = [];
  lines.push("# Generated by Alt8Mail — manage this ruleset in the webmail.");
  lines.push(`# Owner: ${user && user.email ? user.email : "unknown"} · revision ${sieve && sieve.revision ? sieve.revision : 1}`);
  lines.push(`# Updated: ${sieve && sieve.updatedAt ? sieve.updatedAt : new Date().toISOString()}`);
  lines.push("");
  if (requires.size) lines.push(`require ${Array.from(requires).sort().map(quote).join(", ")};`);
  lines.push("");

  const ordered = rules.slice().sort((a, b) => (a.priority || 0) - (b.priority || 0));
  for (const rule of ordered) {
    const comment = rule.enabled ? `# ${rule.name}` : `# [disabled] ${rule.name}`;
    lines.push(comment);
    if (!rule.enabled) continue;
    const tests = rule.conditions.map(conditionTest);
    let test;
    if (tests.length === 1) test = tests[0];
    else test = `${rule.match === "any" ? "anyof" : "allof"} (${tests.join(", ")})`;
    lines.push(`if ${test} {`);
    for (const statement of renderAction(rule.action)) lines.push("  " + statement);
    lines.push("}");
    lines.push("");
  }

  if (vacation && vacation.enabled) {
    const internal = vacation.internal || { subject: vacation.subject, message: vacation.message };
    const external = vacation.external || { enabled: false };
    const dateTests = [];
    if (vacation.start) dateTests.push(`currentdate :value "ge" "date" ${quote(vacation.start)}`);
    if (vacation.end) dateTests.push(`currentdate :value "le" "date" ${quote(vacation.end)}`);
    const vacationAction = (subject, message) =>
      `vacation :days ${vacation.days} :subject ${quote(subject)} ${quote(message)};`;
    const domain = user && user.email ? String(user.email).split("@").pop() : "";
    if (external.enabled && domain) {
      lines.push("# Automatic replies · inside the organization");
      const insideTests = [...dateTests, `address :domain :is "from" ${quote(domain)}`];
      lines.push(`if ${insideTests.length === 1 ? insideTests[0] : `allof (${insideTests.join(", ")})`} {`);
      lines.push("  " + vacationAction(internal.subject || "Out of office", internal.message));
      lines.push("}");
      lines.push("");
      lines.push("# Automatic replies · outside the organization");
      const outsideTests = [...dateTests, `not address :domain :is "from" ${quote(domain)}`];
      lines.push(`if ${outsideTests.length === 1 ? outsideTests[0] : `allof (${outsideTests.join(", ")})`} {`);
      lines.push("  " + vacationAction(external.subject || "Away from the office", external.message));
      lines.push("}");
      lines.push("");
    } else {
      lines.push("# Automatic reply");
      const prefix = dateTests.length ? `if ${dateTests.length === 1 ? dateTests[0] : `allof (${dateTests.join(", ")})`} {` : "";
      if (prefix) lines.push(prefix);
      lines.push(`${prefix ? "  " : ""}${vacationAction(internal.subject || "Out of office", internal.message)}`);
      if (prefix) lines.push("}");
      lines.push("");
    }
  }

  lines.push("# End of ruleset");
  return lines.join("\n");
}
