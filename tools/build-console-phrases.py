import re, glob, html, json

files = (
    ["admin/index.html", "admin/admin.js", "admin/import-views.js", "admin/users-page.js", "admin/import.js"]
    + glob.glob("backend/internal/control/web/*.js")
    + ["backend/internal/control/web/index.html"]
    + glob.glob("public/src/**/*.js", recursive=True)
)
phrases = set()


def add(t):
    t = html.unescape(t).replace("’", "'").strip()
    if not (2 <= len(t) <= 300):
        return
    if not re.search(r"[A-Za-z]{2,}", t):
        return
    if any(c in t for c in ['"', "`", chr(92), "\n", "\r", "\t"]):
        return
    if any(c in t for c in ["<", ">", "=", ";", "{", "}", "[", "]", "(", ")"]):
        return
    if re.match(r"^(?:https?|www|i-|icon-|data-|aria-)", t, re.I):
        return
    if re.search(r"\.(js|mjs|css|html|json|png|svg|webp|woff2?)\b", t):
        return
    if re.search(
        r"\b(?:const|let|var|return|function|class|document|window|querySelector|addEventListener|innerHTML|textContent|setAttribute|getAttribute|localStorage|sessionStorage|JSON|undefined|null|true|false|fetch|api)\b",
        t,
    ):
        return
    if re.search(r"[a-z][A-Z]", t) and " " not in t:
        return
    if re.match(r"^[a-z0-9_.-]+$", t):
        return
    if " " not in t and not t[0].isupper() and t.lower() not in {
        "save", "cancel", "close", "delete", "remove", "edit", "add", "create", "refresh",
        "retry", "apply", "install", "connect", "configure", "details", "search", "filter",
        "status", "enabled", "disabled", "active", "pending", "failed", "success", "error",
        "warning", "all", "none", "yes", "no", "back", "next", "previous", "done", "copy",
        "export", "import", "upload", "download", "preview", "open", "send", "reply",
        "forward", "archive", "trash", "spam", "inbox", "today", "yesterday", "tomorrow",
        "loading", "working", "overview", "settings", "account", "security",
    }:
        return
    phrases.add(t)


for file in files:
    source = open(file, encoding="utf-8").read()
    for match in re.finditer(r">([^<>]{2,300})<", source):
        add(match.group(1))
    for match in re.finditer(r"\b(?:placeholder|aria-label|title|alt)\s*=\s*[\"']([^\"']{2,240})[\"']", source):
        add(match.group(1))
    for match in re.finditer(r"[\"']([^\"'\n]{2,240})[\"']", source):
        add(match.group(1))
    # Control and administration views build most of their markup with
    # template literals. Extract the human text between tags and around
    # interpolations so those labels reach the phrase catalogs too.
    for match in re.finditer(r"`((?:[^`\\]|\\.)*)`", source):
        body = re.sub(r"\$\{[^}]*\}", "\n", match.group(1))
        body = re.sub(r"<[^>]*>", "\n", body)
        for part in body.split("\n"):
            add(part.strip())

for file in ["backend/internal/httpapi/addons.go", "backend/internal/control/addons.go"]:
    try:
        source = open(file, encoding="utf-8").read()
    except FileNotFoundError:
        continue
    for match in re.finditer(r'"([^"\n]{2,300})"', source):
        add(match.group(1))

for file in glob.glob("backend/internal/httpapi/*.go") + glob.glob("backend/internal/control/*.go"):
    for line in open(file, encoding="utf-8"):
        if re.search(r"writeError(?:Params)?\(|s\.audit\(|appendStateAudit\(|recordAdminAudit\(|insertAudit\(", line):
            for match in re.finditer(r'"([^"\n]{2,300})"', line):
                add(match.group(1))

manual = [
    "All address books", "New message", "No recent messages.", "Recent conversations", "Choose a person",
    "Select someone from the directory or your saved contacts to see their details.",
    "Copy email", "Save contact", "Create contact", "Notes",
    "Do more from your inbox",
    "Install first-party-reviewed add-ons for your workspace. Nothing runs inside the mail page, and every permission is shown before you connect.",
    "Curated by Alt8Mail", "First-party reviewed", "Refresh", "All", "Installed", "Available", "Needs setup",
    "Add-on actions", "First-party add-on", "No add-ons match this view", "Try another search or filter.",
    "What this add-on can do", "Alt8Mail only sends data for actions you explicitly run. You can remove access at any time.",
    "Disable for workspace", "Automatic", "Workspace", "Translation", "Tasks", "Communication", "CRM",
    "Operations", "Productivity", "Storage", "Calendar", "Security",
    "Draft started in compose", "Updated language", "New sign-in detected on your email account",
    "Failed password sign-in", "Task updated", "Opened in Office with edit access", "Updated theme",
    "Workspace preferences", "That email and password do not match a Alt8Mail account",
    "That email and password do not match a System Control account", "Email and password are required",
    "Invalid admin second factor", "{count} add-ons · {ready} ready · {installed} installed",
    "{count} add-on · {ready} ready · {installed} installed",
    "{shown} shown · {directory} directory · {contacts} saved", "{name} removed", "{name} connected",
    "{name} could not be connected", "Added {value}", "Installed {value}", "Created {value}",
    "Updated {value}", "Deleted {value}", "Removed {value}", "Added {name}", "Installed {name}",
    "Created {name}", "Updated {name}", "Deleted {name}", "Removed {name}", "Connected {name}",
    "Requested workspace setup for {name}", "Cancelled booking on page {name}",
    "Updated signature for {name}", "Created signature {name}", "Removed signature {name}",
    "Created automation rule {name}", "Deleted automation rule {name}", "Updated automation rule {name}",
    "Created reaction rule {name}", "Deleted reaction rule {name}", "Updated reaction rule {name}",
    "Created booking page {name}", "Disabled booking page {name}", "Updated booking page {name}",
    "Created group {name}", "Deleted group {name}", "Updated group {name}", "Created team template {name}",
    "Updated team template {name}", "Archived team template {name}", "Created shared inbox {name}",
    "Updated shared inbox {name}", "Added alias {name}", "Updated alias {name}", "Removed an alias",
    "Published {name}", "Queued a DLP false-positive review",
    "Language", "System default", "Language updated.", "Could not update the language.",
    "Choose an enabled language", "Could not save the language preference",
    "Updated the Control Plane language", "Updated the administration console language",
    "The control service could not be reached. Check the network and service status.",
    "Your panel session has expired. Sign in again.",
    "Request failed with HTTP {status}", "Sign-in could not complete",
    "Enter both your panel email and password.", "Sign in to System Control",
    "Control plane unavailable", "The browser could not reach the System Control service.",
    "Connection failed", "Try again", "Default system language",
    "Used by webmail, the administration console and System Control unless the user or administrator has chosen their own language.",
    "Choose the languages available across webmail, the administration console and System Control.",
    "Control which shipped languages this Alt8Mail system offers. Changes apply to webmail, the administration console and System Control on their next request.",
    "Disabling a language removes it from every language picker and rejects it if a stale client tries to save it. English (United States) always stays enabled as the fallback, and the current default language cannot be disabled until another default is chosen.",
    "Second factor verified", "Second factor verified · {method}", "Second factor verified · {value}",
    "Password verified · {method}", "Password verified · {value}",
    "Password verified; authenticator code required",
    "Password verified · second factor required",
    "Password verified · first-time second-factor enrollment required",
    "Failed second factor", "Authenticator verified", "Recovery code accepted",
    "Trusted device accepted", "Passkey verified", "WebAuthn passkey verified",
    "Email language", "Default language for new mailbox users in this organization.",
    "Default language for new users", "just now",
    "Password + authenticator code", "Single sign-on · {value}",
    "Delivered via MX · {value}", "Sieve ruleset saved · revision {value}",
    "Conversations archived from {value}", "Chrome on Windows",
    # Count units used in audit details and summaries. The pipe separates the
    # singular and plural translations; transport runtimes pick the branch
    # from the numeric {count} value.
    "{count} recipient", "{count} recipients", "{count} message", "{count} messages",
    "{count} conversation", "{count} conversations", "{count} rule", "{count} rules",
    "{count} file", "{count} files", "{count} alias", "{count} aliases",
    "{count} mailbox", "{count} mailboxes", "{count} domain", "{count} domains",
    "{count} contact", "{count} contacts", "{count} event", "{count} events",
    "{count} task", "{count} tasks", "{count} member", "{count} members",
    "{count} user", "{count} users", "{count} day", "{count} days",
    "{count} hour", "{count} hours", "{count} minute", "{count} minutes",
    "{count} week", "{count} weeks", "{count} month", "{count} months",
    "{count} Sieve rules",
    "Chrome on macOS", "Chrome on Linux", "Firefox on Windows", "Firefox on macOS",
    "Firefox on Linux", "Safari on macOS", "Safari on Windows", "Edge on Windows",
    "Edge on macOS", "Authenticator app", "Recovery code", "Passkey", "Trusted device",
    "Second factor verified · {value}", "Failed second factor · {value}",
    "{value} · trusted device",
    "Language policy updated · {count} disabled · default {value}",
    "Restored folder {folder} from backup {backup}",
    "OS package update {value}", "Pruned events older than {count} days",
    "Office monitoring {value}", "Office {value} for webmail",
    "enabled", "disabled", "completed", "failed", "running", "pending", "active", "inactive", "success",
    "sent today", "received today", "bounced (30d)", "deferred (30d)",
    "package updates", "System IPs used by Postfix · DNS blocklists", "Check now",
    "{percent} busy", "{used} of {total}", "Some blocklists declined the query",
    "Outbound and inbound events per day · last {value} days",
    "LTR · {value} · required fallback", "RTL · {value} · required fallback",
    "LTR · {value}", "RTL · {value}", "reviewed", "source", "available", "draft", "required fallback",
]
phrases.update(manual)

# Audit details generated by the Go mail server. Keep the rendered shapes in
# the source catalog so the phrase layer can translate them with their values.
for verb in [
    "archived", "deleted", "marked read", "marked unread", "starred", "unstarred",
    "flagged for follow-up", "flag cleared", "marked as spam", "moved back to Inbox",
    "reported to security", "pinned", "unpinned", "categorized", "snoozed",
    "returned to Inbox", "ignored", "no longer ignored", "blocked", "copied",
]:
    phrases.add("{count} message " + verb)
    phrases.add("{count} messages " + verb)
phrases.update({
    "Ruleset saved · revision {value}",
    "Ruleset saved · revision {value} · vacation responder enabled",
    "Vacation responder", "Trusted external sender", "Removed trust for external sender",
    "Updated by organization administrator",
    "Moved {count} message to {folder}", "Moved {count} messages to {folder}",
    "{count} message deleted to Trash", "{count} messages deleted to Trash",
    "{deleted} message deleted · {removedCount} removed permanently",
    "{deleted} messages deleted · {removedCount} removed permanently",
    "{count} message reported · reason: {reason}", "{count} messages reported · reason: {reason}",
    "{count} message snoozed until {when}", "{count} messages snoozed until {when}",
    "{count} message blocked · sender added to the blocked list",
    "{count} messages blocked · sender added to the blocked list",
    "{count} message categorized as {categories}", "{count} messages categorized as {categories}",
    "Copied {count} message to {folder}", "Copied {count} messages to {folder}",
})

clean = set()
for text in phrases:
    text = text.strip()
    if not text:
        continue
    if text[0] in "{}%":
        clean.add(text)
        continue
    if not text[0].isalpha():
        continue
    if any(c in text for c in ["<", ">", chr(92)]):
        continue
    if re.match(r"^(?:https?|www|i-|data-|aria-)", text, re.I):
        continue
    if len(text) > 300:
        continue
    clean.add(text)

data = {text: text for text in sorted(clean)}
with open("public/console-locales/en.json", "w", encoding="utf-8") as handle:
    json.dump(data, handle, ensure_ascii=False, indent=2)
    handle.write("\n")
print("source phrases", len(data))
