/* People directory, contacts and address books. */
import { api } from "../core/api.js";
import { appConfirm, isMobile } from "../core/dom.js";
import { avatar, esc, normalizeEmail } from "../core/helpers.js";
import { formatDate, formatDateOnly, formatListWhen } from "../core/format.js";
import { state } from "../core/state.js";
import { showError, showToast } from "../core/toast.js";
import { t, tn } from "../../i18n.mjs";

let hooks = {
  messageById: () => null, folderDef: () => ({ label: "" }), selectionIdForMessage: () => "",
  syncNav: () => {}, syncSearchContext: () => {}, syncStatusContext: () => {},
  syncStatusRange: () => {}, updateSearchClear: () => {}
};
export function configurePeople(next = {}) { hooks = { ...hooks, ...next }; }

export function peopleSourceLabel(person) {
  if (person.source === "directory") return t("people.source.directory");
  if (person.source === "auto") return t("people.source.auto-saved");
  return t("people.contact");
}

export function peopleSourceClass(person) {
  if (person.source === "directory") return "directory";
  if (person.source === "auto") return "auto";
  return "contact";
}

export function peopleRoleLine(person) {
  const primary = String(person.title || (person.source === "directory" ? person.role : t("people.contact")) || "").trim();
  const company = String(person.company || "").trim();
  if (primary && company && primary !== company) return `${primary} · ${company}`;
  return primary || company || t("people.saved-contact");
}

let contactCryptoPromise = null;

function contactFingerprint(fingerprint) {
  return String(fingerprint || "").toUpperCase().replace(/[^0-9A-F]/g, "").replace(/(.{4})/g, "$1 ").trim();
}

function contactTrustLabel(trust) {
  if (trust === "verified") return t("ui.people.trust-verified");
  if (trust === "enterprise") return t("ui.people.trust-enterprise");
  if (trust === "tofu") return t("ui.people.trust-tofu");
  if (trust === "revoked") return t("ui.people.trust-revoked");
  return t("ui.people.trust-unknown");
}

function contactKeysFor(email) {
  const address = normalizeEmail(email);
  return (state.cryptoContactKeys || []).filter((key) => normalizeEmail(key.email) === address);
}

function contactProtocols() {
  const protocols = state.encryption?.protocols;
  if (Array.isArray(protocols) && protocols.length) return protocols;
  if (state.encryption?.protocol === "both") return ["openpgp", "smime"];
  return [state.encryption?.protocol || "openpgp"];
}

function contactProtocolLabel(protocol) {
  return protocol === "smime" ? t("ui.settings.smime") : t("ui.settings.openpgp");
}

async function ensureContactCryptoKeys(force = false) {
  if (contactCryptoPromise) return contactCryptoPromise;
  contactCryptoPromise = (async () => {
    if (!state.encryptionLoaded) {
      try {
        state.encryption = await api("/api/encryption");
      } catch {
        state.encryption = { enabled: false };
      }
      state.encryptionLoaded = true;
    }
    if (!state.encryption?.enabled) return;
    if (state.cryptoContactKeysLoaded && !force) return;
    try {
      const data = await api("/api/encryption/contacts");
      state.cryptoContactKeys = data.keys || [];
      state.cryptoContactKeysLoaded = true;
    } catch {
      return;
    }
    if (state.task === "people") renderPersonDetail();
  })().finally(() => { contactCryptoPromise = null; });
  return contactCryptoPromise;
}

async function reloadContactCryptoKeys() {
  state.cryptoContactKeysLoaded = false;
  await ensureContactCryptoKeys(true);
  if (state.task === "people") renderPersonDetail();
}

function contactCryptoMarkup(person) {
  if (state.encryption?.enabled !== true) return "";
  const protocols = contactProtocols();
  const keys = contactKeysFor(person.email).filter((key) => protocols.includes(key.protocol));
  const rows = keys.map((key) => {
    const changed = key.keyChangedAt && !["verified", "enterprise"].includes(key.trust);
    const smime = key.protocol === "smime";
    return `<div class="contact-key-row${changed ? " is-changed" : ""}">
      <span class="contact-key-icon"><svg class="icon" aria-hidden="true"><use href="#${smime ? "i-shield-check" : "i-key"}"/></svg></span>
      <div class="contact-key-copy">
        <strong>${esc(contactFingerprint(key.fingerprint))}</strong>
        <span>${esc(contactProtocolLabel(key.protocol))} · <span class="contact-key-trust is-${esc(key.trust || "unknown")}">${esc(contactTrustLabel(key.trust))}</span></span>
        ${smime && key.subject ? `<span>${esc(key.subject)}</span>` : ""}
        ${smime && key.notAfter ? `<span>${esc(t("ui.people.valid-until", { date: formatDateOnly(key.notAfter) }))}</span>` : ""}
        ${changed ? `<span class="contact-key-warning"><svg class="icon" aria-hidden="true"><use href="#i-alert"/></svg>${esc(t("ui.people.key-changed-warning"))}</span>` : ""}
        ${key.keyChangedAt ? `<small>${esc(t("ui.people.key-changed", { date: formatDateOnly(key.keyChangedAt) }))}</small>` : ""}
      </div>
      <div class="contact-key-actions">
        ${key.trust !== "verified" && key.trust !== "enterprise" ? `<button class="btn-secondary btn-small" type="button" data-verify-contact-key="${esc(key.id)}">${esc(t("ui.people.verify-key"))}</button>` : ""}
        <button class="link-btn danger-link" type="button" data-remove-contact-key="${esc(key.id)}">${esc(t("ui.people.remove-key"))}</button>
      </div>
    </div>`;
  }).join("");
  const discoverButtons = [];
  if (protocols.includes("openpgp")) discoverButtons.push(`<button class="btn-secondary btn-small" type="button" data-discover-contact-key="${esc(person.email)}" data-protocol="openpgp"><svg class="icon" aria-hidden="true"><use href="#i-globe"/></svg>${esc(t("ui.people.discover-key"))}</button>`);
  if (protocols.includes("smime") && state.encryption?.smimeDirectory) discoverButtons.push(`<button class="btn-secondary btn-small" type="button" data-discover-contact-key="${esc(person.email)}" data-protocol="smime"><svg class="icon" aria-hidden="true"><use href="#i-globe"/></svg>${esc(t("ui.people.discover-smime"))}</button>`);
  return `<div class="person-encryption-card">
    <div class="person-encryption-head">
      <span class="person-encryption-art"><svg class="icon" aria-hidden="true"><use href="#i-lock"/></svg></span>
      <div><strong>${esc(t("ui.people.encryption-keys"))}</strong><span>${esc(t("ui.people.encryption-keys-help"))}</span></div>
    </div>
    <div class="person-encryption-actions">
      ${discoverButtons.join("")}
      <button class="btn-secondary btn-small" type="button" data-import-contact-key="${esc(person.email)}"><svg class="icon" aria-hidden="true"><use href="#i-upload"/></svg>${esc(t("ui.people.import-public-key"))}</button>
    </div>
    <div class="contact-key-list">${rows || `<div class="settings-note">${esc(t("ui.people.no-encryption-key"))}</div>`}</div>
  </div>`;
}

export function peopleListDeduped() {
  const seen = new Set();
  const result = [];
  for (const person of state.people) {
    const key = normalizeEmail(person.email);
    if (!key || seen.has(key)) continue;
    seen.add(key);
    result.push(person);
  }
  return result;
}

export function renderPeopleBookOptions() {
  const select = document.getElementById("peopleBookFilter");
  if (!select) return;
  const options = [`<option value="all">${t("people.all-address-books")}</option>`, ...(state.addressBooks || []).map((book) => `<option value="${esc(book.id)}">${esc(book.name)} (${book.count || 0})</option>`)].join("");
  if (select.innerHTML !== options) select.innerHTML = options;
  if (state.peopleBook !== "all" && !(state.addressBooks || []).some((book) => book.id === state.peopleBook)) state.peopleBook = "all";
  select.value = state.peopleBook;
}

export function renderPeople() {
  const list = document.getElementById("peopleList");
  if (!list) return;
  const everyone = peopleListDeduped();
  const query = state.peopleQuery.trim().toLowerCase();
  const filter = state.peopleFilter || "all";
  const book = state.peopleBook || "all";
  const matches = (person) => {
    if (filter === "directory" && person.source !== "directory") return false;
    if (filter === "contacts" && person.source === "directory") return false;
    if (book !== "all" && person.source !== "directory" && (person.addressBookId || "default") !== book) return false;
    if (!query) return true;
    return [person.name, person.email, person.company, person.title, person.team, person.role]
      .join(" ").toLowerCase().includes(query);
  };
  const visible = everyone.filter(matches).sort((a, b) => {
    if (a.source === "directory" && b.source !== "directory") return -1;
    if (a.source !== "directory" && b.source === "directory") return 1;
    return String(a.name || "").localeCompare(String(b.name || ""));
  });
  const internal = everyone.filter((person) => person.source === "directory").length;
  const contacts = everyone.length - internal;
  const count = document.getElementById("peopleCount");
  if (count) count.textContent = t("people.count-summary", { shown: visible.length, directory: internal, contacts });
  renderPeopleBookOptions();
  document.querySelectorAll("[data-people-filter]").forEach((button) => {
    const active = button.getAttribute("data-people-filter") === filter;
    button.classList.toggle("is-active", active);
    button.setAttribute("aria-selected", active ? "true" : "false");
  });
  if (!visible.some((person) => normalizeEmail(person.email) === normalizeEmail(state.contactEmail))) {
    state.contactEmail = visible[0] ? visible[0].email : null;
  }
  if (!visible.length) {
    list.innerHTML = `<div class="people-empty-rich">
      <div class="empty-icon"><svg class="icon" aria-hidden="true"><use href="#i-people"/></svg></div>
      <strong>${query ? t("people.no-people-found") : t("people.no-contacts")}</strong>
      <span>${query ? t("people.try-different") : t("people.auto-saved-note")}</span>
    </div>`;
    renderPersonDetail();
    hooks.syncStatusRange?.();
    return;
  }
  const row = (person) => `<button class="contact${normalizeEmail(person.email) === normalizeEmail(state.contactEmail) ? " is-active" : ""}" data-contact="${esc(person.email)}" type="button">
      ${avatar(person, "md")}
      <span class="contact-id"><span class="contact-name">${esc(person.name || person.email)}</span><span class="contact-role">${esc(peopleRoleLine(person))}</span><span class="contact-email">${esc(person.email)}</span></span>
      <span class="people-source ${peopleSourceClass(person)}">${peopleSourceLabel(person)}</span>
    </button>`;
  const groups = [];
  if (filter !== "contacts") groups.push({ key: "directory", label: t("people.internal-directory"), people: visible.filter((person) => person.source === "directory") });
  if (filter !== "directory") groups.push({ key: "contacts", label: t("people.your-contacts"), people: visible.filter((person) => person.source !== "directory") });
  list.innerHTML = groups.filter((group) => group.people.length).map((group) => `
    <section class="people-section" aria-label="${esc(group.label)}">
      <div class="people-section-head"><span>${esc(group.label)}</span><span>${group.people.length}</span></div>
      ${group.people.map(row).join("")}
    </section>`).join("");
  renderPersonDetail();
  hooks.syncStatusRange?.();
}

export let contactEditingId = null;
export let contactEditingEmail = null;

export async function loadContactRecord(email) {
  const data = await api("/api/contacts");
  const contacts = data.contacts || [];
  return contacts.find((item) => item.email === email) || null;
}

export async function openContactDialog(email = null) {
  contactEditingId = null;
  contactEditingEmail = email;
  const record = email ? await loadContactRecord(email) : null;
  if (email && !record) { showToast(t("people.contact-not-found")); return; }
  contactEditingId = record ? record.id : null;
  document.getElementById("contactDialogTitle").textContent = record ? t("people.edit-contact") : t("people.new-contact");
  document.getElementById("contactName").value = record ? record.name : "";
  document.getElementById("contactEmailField").value = record ? record.email : "";
  document.getElementById("contactEmailField").disabled = Boolean(record);
  document.getElementById("contactCompany").value = record ? record.company || "" : "";
  document.getElementById("contactTitle").value = record ? record.title || "" : "";
  document.getElementById("contactPhone").value = record ? record.phone || "" : "";
  document.getElementById("contactLocation").value = record ? record.location || "" : "";
  document.getElementById("contactNotes").value = record ? record.notes || "" : "";
  const bookSelect = document.getElementById("contactAddressBook");
  if (bookSelect) {
    bookSelect.innerHTML = (state.addressBooks || []).map((book) => `<option value="${esc(book.id)}"${(record?.addressBookId || "default") === book.id ? " selected" : ""}>${esc(book.name)}</option>`).join("");
  }
  const birthday = document.getElementById("contactBirthday");
  if (birthday) birthday.value = record?.birthday || "";
  document.getElementById("contactDelete").hidden = !record;
  document.getElementById("contactSave").innerHTML = record
    ? `<svg class="icon" aria-hidden="true"><use href="#i-check"/></svg>${t("people.save-contact")}`
    : `<svg class="icon" aria-hidden="true"><use href="#i-plus"/></svg>${t("people.create-contact")}`;
  document.getElementById("contactOverlay").hidden = false;
  setTimeout(() => document.getElementById("contactName").focus(), 30);
}

export function openAddressBookDialog() {
  const overlay = document.getElementById("addressBookOverlay");
  if (!overlay) return;
  document.getElementById("addressBookName").value = "";
  document.getElementById("addressBookColor").value = "#3d6b62";
  overlay.hidden = false;
  setTimeout(() => document.getElementById("addressBookName").focus(), 30);
}

export async function saveAddressBook() {
  const name = document.getElementById("addressBookName").value.trim();
  if (!name) { showToast(t("people.address-book-name-required")); return; }
  try {
    const data = await api("/api/address-books", {
      method: "POST",
      body: { name, color: document.getElementById("addressBookColor").value || "#3d6b62" }
    });
    state.addressBooks = data.addressBooks || state.addressBooks;
    document.getElementById("addressBookOverlay").hidden = true;
    state.peopleBook = data.id || "all";
    renderPeople();
    showToast(t("people.address-book-created"));
  } catch (error) { showError(error); }
}

export function closeContactDialog() {
  document.getElementById("contactOverlay").hidden = true;
  contactEditingId = null;
  contactEditingEmail = null;
}

export function contactToPerson(contact) {
  const source = contact.source || "contact";
  return {
    name: contact.name, email: contact.email, role: source === "directory" ? contact.role || t("people.contact") : t("people.contact"),
    company: contact.company || "", team: "", title: contact.title || "", phone: contact.phone || "",
    location: contact.location || "", reportsTo: "", initials: contact.initials || initialsFrom(contact.name),
    color: contact.color || "#3d6b62", source,
    birthday: contact.birthday || "", addressBookId: contact.addressBookId || "default"
  };
}

export function mergeContactPerson(contact) {
  if (!contact?.email) return;
  const person = contactToPerson(contact);
  const index = state.people.findIndex((item) => normalizeEmail(item.email) === normalizeEmail(person.email));
  if (index >= 0) state.people[index] = { ...state.people[index], ...person };
  else state.people.push(person);
  if (state.task === "people") renderPeople();
}

export async function saveContact() {
  const name = document.getElementById("contactName").value.trim();
  const email = document.getElementById("contactEmailField").value.trim();
  if (!name) { showToast(t("people.name-required")); return; }
  if (!email || !email.includes("@")) { showToast(t("people.email-required")); return; }
  const body = {
    name, email,
    company: document.getElementById("contactCompany").value.trim(),
    title: document.getElementById("contactTitle").value.trim(),
    phone: document.getElementById("contactPhone").value.trim(),
    location: document.getElementById("contactLocation").value.trim(),
    notes: document.getElementById("contactNotes").value.trim(),
    addressBookId: document.getElementById("contactAddressBook")?.value || "default",
    birthday: document.getElementById("contactBirthday")?.value || ""
  };
  try {
    const editingId = contactEditingId;
    const data = await api(editingId ? `/api/contacts/${encodeURIComponent(editingId)}` : "/api/contacts", {
      method: editingId ? "PATCH" : "POST", body
    });
    const updated = contactToPerson(data.contact);
    state.people = state.people.filter((person) => person.email !== contactEditingEmail && person.email !== updated.email);
    state.people.push(updated);
    state.contactEmail = updated.email;
    closeContactDialog();
    if (state.task === "people") renderPeople();
    showToast(editingId ? t("people.contact-updated") : t("people.contact-created"));
  } catch (error) { showError(error); }
}

export async function deleteContactByEmail(email) {
  if (!email || !(await appConfirm({ title: t("people.delete-title"), message: t("people.delete-message"), confirmLabel: t("people.delete-contact"), tone: "danger" }))) return;
  try {
    const record = await loadContactRecord(email);
    if (!record) { showToast(t("people.contact-not-found")); return; }
    await api(`/api/contacts/${encodeURIComponent(record.id)}`, { method: "DELETE" });
    state.people = state.people.filter((person) => person.email !== email);
    if (state.contactEmail === email) state.contactEmail = state.people[0]?.email || null;
    closeContactDialog();
    renderPeople();
    showToast(t("people.contact-deleted"));
  } catch (error) { showError(error); }
}

export function renderPersonDetail() {
  const person = state.people.find((item) => normalizeEmail(item.email) === normalizeEmail(state.contactEmail));
  if (!person) {
    personDetail.innerHTML = `<div class="people-empty-rich">
      <div class="empty-icon"><svg class="icon" aria-hidden="true"><use href="#i-people"/></svg></div>
      <strong>${t("people.choose-person")}</strong>
      <span>${t("people.choose-person-hint")}</span>
    </div>`;
    return;
  }
  ensureContactCryptoKeys().catch(() => {});
  const recent = state.messages
    .filter((message) => normalizeEmail(message.from?.email) === normalizeEmail(person.email) || (message.to || []).some((recipient) => normalizeEmail(recipient.email) === normalizeEmail(person.email)))
    .sort((a, b) => Date.parse(b.date) - Date.parse(a.date))
    .slice(0, 3);
  const sourceClass = peopleSourceClass(person);
  const sourceLabel = person.source === "directory" ? t("people.internal-directory") : person.source === "auto" ? t("people.auto-saved-recipient") : t("people.personal-contact");
  const contactActions = person.source !== "directory"
    ? `<button class="btn-secondary" data-edit-contact="${esc(person.email)}" type="button">${t("people.edit-contact")}</button><button class="btn-secondary danger-btn" data-delete-contact="${esc(person.email)}" type="button">${t("people.delete")}</button>`
    : "";
  personDetail.innerHTML = `
    <div class="person-hero">
      ${avatar(person, "xl")}
      <div class="person-hero-id">
        <h2>${esc(person.name || person.email)}</h2>
        <p>${esc(peopleRoleLine(person))}</p>
        <span class="directory-badge ${sourceClass}"><svg class="icon" aria-hidden="true"><use href="#${person.source === "directory" ? "i-shield-check" : "i-people"}"/></svg>${sourceLabel}</span>
      </div>
      <div class="person-actions">
        <button class="btn-primary" data-message-contact="${esc(person.email)}" type="button"><svg class="icon" aria-hidden="true"><use href="#i-compose"/></svg>${t("people.new-message")}</button>
        <button class="btn-secondary" data-copy-email="${esc(person.email)}" type="button">${t("people.copy-email")}</button>
        ${contactActions}
      </div>
    </div>
    <div class="info-card">
      <div class="info-row"><span class="info-label">${t("people.detail.email")}</span><span class="info-value">${esc(person.email)}</span></div>
      <div class="info-row"><span class="info-label">${t("people.detail.phone")}</span><span class="info-value">${esc(person.phone || "—")}</span></div>
      <div class="info-row"><span class="info-label">${t("people.detail.company")}</span><span class="info-value">${esc(person.company || "—")}</span></div>
      <div class="info-row"><span class="info-label">${t("people.detail.birthday")}</span><span class="info-value">${esc(person.birthday ? formatDateOnly(person.birthday) : "—")}</span></div>
      <div class="info-row"><span class="info-label">${t("people.detail.address-book")}</span><span class="info-value">${esc((state.addressBooks || []).find((book) => book.id === (person.addressBookId || "default"))?.name || t("people.contacts"))}</span></div>
      <div class="info-row"><span class="info-label">${t("people.detail.title")}</span><span class="info-value">${esc(person.title || "—")}</span></div>
      <div class="info-row"><span class="info-label">${t("people.detail.team")}</span><span class="info-value">${esc(person.team || "—")}</span></div>
      <div class="info-row"><span class="info-label">${t("people.detail.location")}</span><span class="info-value">${esc(person.location || "—")}</span></div>
      <div class="info-row"><span class="info-label">${t("people.detail.reports-to")}</span><span class="info-value">${esc(person.reportsTo || "—")}</span></div>
    </div>
    ${contactCryptoMarkup(person)}
    ${person.notes ? `<div class="person-notes"><h3>${t("people.notes-heading")}</h3><p>${esc(person.notes)}</p></div>` : ""}
    <div class="recent-block">
      <h3>${t("people.recent-conversations")}</h3>
      ${recent.length ? recent.map((message) => `<button class="recent-item" data-recent="${esc(message.id)}" type="button">
        <span class="recent-icon"><svg class="icon" aria-hidden="true"><use href="#i-mail"/></svg></span>
        <span class="recent-main"><span class="recent-subject">${esc(message.subject)}</span><span class="recent-when">${esc(formatListWhen(message.date))} · ${esc(hooks.folderDef?.(message.folder).label)}</span></span>
        <svg class="icon recent-arrow" aria-hidden="true"><use href="#i-chevron-right"/></svg>
      </button>`).join("") : `<p class="people-empty">${t("people.no-recent-messages")}</p>`}
    </div>`;
}

export async function discoverContactKey(email, protocol = "openpgp", button = null) {
  if (!email) return;
  if (button) button.disabled = true;
  try {
    const data = await api(`/api/encryption/contacts/${encodeURIComponent(email)}/discover?protocol=${encodeURIComponent(protocol)}`, { method: "POST", body: {} });
    await reloadContactCryptoKeys();
    if (data?.keyChanged) showToast(data.warning || t("ui.people.key-changed-warning"));
    else showToast(t("ui.people.key-discovered"));
  } catch (error) {
    if (error?.code === "WKD_NOT_FOUND") showToast(t("ui.people.discover-failed"));
    else if (error?.code === "SMIME_NOT_FOUND") showToast(t("ui.people.smime-discover-failed"));
    else showError(error);
  } finally {
    if (button) button.disabled = false;
  }
}

export function openContactKeyImport(email, protocol = "openpgp") {
  if (!email) return;
  document.getElementById("contactKeyOverlay")?.remove();
  const protocols = contactProtocols();
  const selectedProtocol = protocols.includes(protocol) ? protocol : protocols[0] || "openpgp";
  const overlay = document.createElement("div");
  overlay.className = "overlay";
  overlay.id = "contactKeyOverlay";
  overlay.innerHTML = `<div class="dialog security-dialog" role="dialog" aria-modal="true" aria-labelledby="contactKeyTitle">
    <header class="security-dialog-head">
      <span class="security-dialog-icon"><svg class="icon" aria-hidden="true"><use href="#i-key"/></svg></span>
      <div class="security-dialog-heading">
        <p class="eyebrow">${esc(t("ui.people.encryption-keys"))}</p>
        <h2 id="contactKeyTitle">${esc(t("ui.people.import-public-key"))}</h2>
      </div>
      <button class="icon-btn security-dialog-close" type="button" data-contact-key-cancel aria-label="${esc(t("common.close"))}"><svg class="icon" aria-hidden="true"><use href="#i-close"/></svg></button>
    </header>
    <div class="security-dialog-body">
      ${protocols.length > 1 ? `<label class="form-field"><span>${esc(t("ui.compose.protocol"))}</span><select data-contact-key-protocol>${protocols.map((item) => `<option value="${esc(item)}"${item === selectedProtocol ? " selected" : ""}>${esc(contactProtocolLabel(item))}</option>`).join("")}</select></label>` : ""}
      <p class="settings-note" data-contact-key-help>${esc(t("ui.people.public-key-help", { email }))}</p>
      <label class="form-field"><span data-contact-key-label>${esc(t("ui.people.public-key"))}</span><textarea rows="8" spellcheck="false" data-contact-key-public placeholder="-----BEGIN PGP PUBLIC KEY BLOCK-----"></textarea></label>
      <label class="form-field" data-contact-key-chain-wrap hidden><span>${esc(t("ui.people.certificate-chain"))}</span><textarea rows="4" spellcheck="false" data-contact-key-chain></textarea></label>
    </div>
    <footer class="security-dialog-actions">
      <button class="btn-secondary" type="button" data-contact-key-cancel>${esc(t("common.cancel"))}</button>
      <button class="btn-primary" type="button" data-contact-key-save>${esc(t("ui.people.import"))}</button>
    </footer>
  </div>`;
  document.body.appendChild(overlay);
  const close = () => overlay.remove();
  const protocolSelect = overlay.querySelector("[data-contact-key-protocol]");
  const syncProtocolFields = () => {
    const smime = (protocolSelect?.value || selectedProtocol) === "smime";
    const field = overlay.querySelector("[data-contact-key-public]");
    const label = overlay.querySelector("[data-contact-key-label]");
    const help = overlay.querySelector("[data-contact-key-help]");
    const chainWrap = overlay.querySelector("[data-contact-key-chain-wrap]");
    if (label) label.textContent = smime ? t("ui.people.certificate") : t("ui.people.public-key");
    if (help) help.textContent = smime ? t("ui.people.certificate-help", { email }) : t("ui.people.public-key-help", { email });
    if (field) field.placeholder = smime ? "-----BEGIN CERTIFICATE-----" : "-----BEGIN PGP PUBLIC KEY BLOCK-----";
    if (chainWrap) chainWrap.hidden = !smime;
  };
  protocolSelect?.addEventListener("change", syncProtocolFields);
  syncProtocolFields();
  overlay.addEventListener("click", (event) => {
    if (event.target === overlay || event.target.closest("[data-contact-key-cancel]")) close();
  });
  overlay.querySelector("[data-contact-key-save]")?.addEventListener("click", async (event) => {
    const button = event.currentTarget;
    const field = overlay.querySelector("[data-contact-key-public]");
    const chain = overlay.querySelector("[data-contact-key-chain]");
    const value = field ? field.value.trim() : "";
    const protocol = protocolSelect?.value || selectedProtocol;
    if (!value) { showToast(protocol === "smime" ? t("ui.people.certificate-required") : t("ui.people.public-key-required")); field?.focus(); return; }
    button.disabled = true;
    try {
      const body = protocol === "smime"
        ? { protocol, certificate: value, chain: chain?.value.trim() || "", trust: "unknown" }
        : { protocol, publicKey: value, trust: "unknown" };
      await api(`/api/encryption/contacts/${encodeURIComponent(email)}`, { method: "PUT", body });
      close();
      await reloadContactCryptoKeys();
      showToast(protocol === "smime" ? t("ui.people.certificate-saved") : t("ui.people.key-saved"));
    } catch (error) {
      showError(error);
      button.disabled = false;
    }
  });
  setTimeout(() => overlay.querySelector("[data-contact-key-public]")?.focus(), 40);
}

export async function verifyContactKey(id, button) {
  const key = (state.cryptoContactKeys || []).find((item) => item.id === id);
  if (!key) return;
  if (!(await appConfirm({
    title: t("ui.people.verify-key-title"),
    message: t("ui.people.verify-key-message", { fingerprint: contactFingerprint(key.fingerprint) }),
    confirmLabel: t("ui.people.verify-key")
  }))) return;
  if (button) button.disabled = true;
  try {
    await api(`/api/encryption/contacts/${encodeURIComponent(id)}/trust`, { method: "POST", body: { trust: "verified" } });
    await reloadContactCryptoKeys();
    showToast(t("ui.people.key-verified"));
  } catch (error) {
    showError(error);
  } finally {
    if (button) button.disabled = false;
  }
}

export async function deleteContactKey(id, button) {
  if (!id) return;
  if (!(await appConfirm({ title: t("ui.people.remove-key"), message: t("ui.people.remove-key-message"), confirmLabel: t("ui.people.remove-key"), tone: "danger" }))) return;
  if (button) button.disabled = true;
  try {
    await api(`/api/encryption/contacts/${encodeURIComponent(id)}`, { method: "DELETE" });
    await reloadContactCryptoKeys();
    showToast(t("ui.people.key-removed"));
  } catch (error) {
    showError(error);
  } finally {
    if (button) button.disabled = false;
  }
}

export function copyText(text) {
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(
      () => showToast(t("people.email-copied")),
      () => showToast(t("people.copy-failed"))
    );
  } else {
    showToast(t("people.copy-failed"));
  }
}

export function openMessageFromPeople(messageId) {
  const message = hooks.messageById?.(messageId);
  if (!message) return;
  state.task = "mail";
  app.setAttribute("data-task", "mail");
  mailView.hidden = false;
  calendarView.hidden = true;
  peopleView.hidden = true;
  settingsView.hidden = true;
  state.folder = ["starred", "spam", "trash"].includes(message.folder) ? "inbox" : message.folder;
  state.filter = "all";
  state.query = "";
  searchInput.value = "";
  hooks.updateSearchClear?.();
  const threadId = hooks.selectionIdForMessage?.(message);
  state.selectedThreadId = threadId;
  if (message.unread && state.markReadOnOpen) message.unread = false;
  syncChips();
  hooks.syncNav?.();
  hooks.syncSearchContext?.();
  renderAll();
  hooks.syncStatusContext?.();
  document.body.classList.remove("person-open");
  if (isMobile()) document.body.classList.add("reader-open");
}
