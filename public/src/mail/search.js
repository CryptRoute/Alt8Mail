/* Mail search: server search, notes strip, search hints and context. */
import { parseQuery } from "../../search.mjs";
import { api } from "../core/api.js";
import { isMobile, searchClear, searchHints, searchInput } from "../core/dom.js";
import { esc } from "../core/helpers.js";
import { state } from "../core/state.js";
import { t, tn } from "../../i18n.mjs";

let hooks = {
  rebuildIndex: () => {},
  renderList: () => {},
  renderReader: () => {},
  syncSelectionUi: () => {},
  writeAppHash: () => {},
  stickyNoteOwnerId: () => "",
  notesDeskPreview: () => "",
  renderTasks: () => {},
  renderFiles: () => {},
  renderScheduled: () => {},
  syncStatusRange: () => {},
  renderPeople: () => {}
};
export function configureSearch(next = {}) { hooks = { ...hooks, ...next }; }

let searchHintsTimer = null;

export function mergeSearchMessages(results) {
  const byId = new Map(state.messages.map((message) => [message.id, message]));
  for (const message of results || []) {
    if (message && message.id) byId.set(message.id, message);
  }
  state.messages = Array.from(byId.values());
  hooks.rebuildIndex?.();
}

export function searchNoteStripMarkup() {
  if (!state.query || state.noteSearchQuery !== state.query) return "";
  const notes = state.noteSearchResults || [];
  if (!notes.length && !state.noteSearchLoading) return "";
  const ownerId = notes[0]?.owner?.id || hooks.stickyNoteOwnerId?.();
  const rows = notes.map((note) => {
    const owner = note.owner || {};
    const team = note.team ? t("mail.search.notesTeam") : "";
    const ownerLabel = owner.name || owner.email || "";
    return `<button class="search-note-row" type="button" data-search-note="${esc(note.id)}" data-search-note-owner="${esc(owner.id || "")}">
      <span class="search-note-mark" aria-hidden="true">${note.archived ? "🗄" : "📌"}</span>
      <span class="search-note-copy"><b>${esc(note.title || t("mail.search.untitledNote"))}</b><small>${esc(hooks.notesDeskPreview?.(note))}</small></span>
      <span class="search-note-meta">${esc(team)}${esc(ownerLabel)}${note.archived ? ` · ${esc(t("mail.search.archive"))}` : ""}</span>
    </button>`;
  }).join("");
  return `<section class="search-notes-strip" aria-label="${esc(t("mail.search.notesAria"))}">
    <div class="search-notes-head"><span class="search-notes-label"><span aria-hidden="true">📌</span>${esc(t("mail.search.notesLabel"))}</span><strong>${state.noteSearchLoading && !notes.length ? esc(t("mail.search.searchingNotes")) : esc(tn("mail.search.matchingNotes", notes.length, { count: notes.length }))}</strong><button class="link-btn" type="button" data-open-notes-search="${esc(ownerId)}">${esc(t("mail.search.openNotesDesk"))}</button></div>
    ${rows}
  </section>`;
}

export async function runGlobalNoteSearch(query) {
  const value = String(query || "").trim();
  const sequence = ++state.noteSearchSeq;
  state.noteSearchQuery = value;
  if (!value) {
    state.noteSearchResults = [];
    state.noteSearchLoading = false;
    return;
  }
  state.noteSearchLoading = true;
  try {
    const data = await api(`/api/notes/search?q=${encodeURIComponent(value)}&scope=all&limit=6`);
    if (sequence !== state.noteSearchSeq || state.query !== value) return;
    state.noteSearchResults = Array.isArray(data.notes) ? data.notes.slice(0, 6) : [];
  } catch {
    if (sequence !== state.noteSearchSeq) return;
    state.noteSearchResults = [];
  } finally {
    if (sequence === state.noteSearchSeq) {
      state.noteSearchLoading = false;
      if (state.task === "mail" && state.query === value) hooks.renderList?.({ preserveScroll: true });
    }
  }
}

export async function runServerSearch(query) {
  const sequence = ++state.searchSeq;
  if (!query || state.task !== "mail") {
    state.searchLoading = false;
    state.noteSearchResults = [];
    state.noteSearchQuery = "";
    state.noteSearchLoading = false;
    state.noteSearchSeq += 1;
    if (state.task === "mail") {
      state.searchResults = [];
      hooks.renderList?.();
      hooks.renderReader?.();
    }
    return;
  }
  state.searchLoading = true;
  runGlobalNoteSearch(query);
  try {
    const data = await api("/api/search?q=" + encodeURIComponent(query));
    if (sequence !== state.searchSeq || state.query !== query) return;
    const results = Array.isArray(data.messages) ? [...data.messages] : [];
    // Client-side mode has no server-side body index. Include messages that
    // have already been decrypted in this browser session so the query can
    // match their plaintext without sending it to the server.
    if (state.encryption?.mode === "client_side" && state.clientDecryptedMessages?.size) {
      const needle = String(query || "").toLowerCase().trim();
      if (needle) {
        const seen = new Set(results.map((message) => message.id));
        for (const [messageId, decrypted] of state.clientDecryptedMessages) {
          if (seen.has(messageId)) continue;
          const body = `${decrypted.bodyText || ""} ${String(decrypted.bodyHtml || "").replace(/<[^>]*>/g, " ")}`.toLowerCase();
          if (!body.includes(needle)) continue;
          const message = state.messages.find((item) => item.id === messageId);
          if (message) results.push(message);
        }
      }
    }
    mergeSearchMessages(results);
    state.searchResults = results;
    state.searchParsed = parseQuery(query);
    state.searchErrors = (data.parsed && data.parsed.errors) || [];
    state.searchLoading = false;
    hooks.renderList?.();
    hooks.renderReader?.();
    hooks.syncSelectionUi?.();
    syncSearchContext();
  } catch (error) {
    if (sequence !== state.searchSeq) return;
    state.searchLoading = false;
    state.searchResults = [];
    state.searchErrors = [error.message || t("mail.search.unavailable")];
    hooks.renderList?.();
    hooks.renderReader?.();
    syncSearchContext();
  }
}

export function scheduleServerSearch() {
  clearTimeout(state.searchTimer);
  state.searchTimer = setTimeout(() => runServerSearch(state.query), 220);
}

export function setQuery(value) {
  state.query = String(value || "");
  state.selectedIds.clear();
  state.selectedThreadId = null;
  state.expandedThreadId = null;
  state.searchResults = [];
  state.noteSearchResults = [];
  state.noteSearchQuery = "";
  state.noteSearchLoading = false;
  state.noteSearchSeq += 1;
  state.searchParsed = state.query ? parseQuery(state.query) : null;
  state.searchErrors = [];
  hooks.renderList?.();
  hooks.renderReader?.();
  hooks.syncSelectionUi?.();
  syncSearchContext();
  if (state.task === "mail") scheduleServerSearch();
  hooks.writeAppHash?.({ replace: true });
}

export function clearSearch() {
  state.query = "";
  searchInput.value = "";
  updateSearchClear();
  clearTimeout(state.searchTimer);
  state.searchSeq += 1;
  state.searchResults = [];
  state.noteSearchResults = [];
  state.noteSearchQuery = "";
  state.noteSearchLoading = false;
  state.noteSearchSeq += 1;
  state.searchParsed = null;
  state.searchErrors = [];
  state.searchLoading = false;
  state.selectedIds.clear();
  state.selectedThreadId = null;
  if (state.task === "tasks") {
    hooks.renderTasks?.();
  } else if (state.task === "files") {
    hooks.renderFiles?.();
  } else if (state.task === "scheduled") {
    hooks.renderScheduled?.();
    hooks.syncStatusRange?.();
  } else {
    hooks.renderList?.();
    hooks.renderReader?.();
    hooks.syncSelectionUi?.();
  }
  syncSearchContext();
  if (state.task === "people") hooks.renderPeople?.();
  hooks.writeAppHash?.({ replace: true });
}

export function updateSearchClear() {
  const has = searchInput.value.trim().length > 0;
  searchClear.classList.toggle("is-shown", has);
  searchClear.style.display = has ? "grid" : "none";
}

export function focusSearch() {
  if (state.task === "calendar" || state.task === "settings") return;
  if (isMobile()) document.body.classList.add("search-open");
  searchInput.focus();
  searchInput.select();
  openSearchHints();
}

export function closeMobileSearch() {
  document.body.classList.remove("search-open");
  searchInput.blur();
  closeSearchHints();
}

export function openSearchHints() {
  if (state.task !== "mail") return;
  clearTimeout(searchHintsTimer);
  if (searchInput.value.trim()) {
    searchHints.hidden = true;
    state.searchHintsOpen = false;
    return;
  }
  searchHints.hidden = false;
  state.searchHintsOpen = true;
  const scope = document.getElementById("searchScope");
  if (scope) scope.textContent = t("mail.search.scopeHint");
}

export function closeSearchHints() {
  searchHintsTimer = setTimeout(() => {
    searchHints.hidden = true;
    state.searchHintsOpen = false;
  }, 120);
}

export function insertSearchOperator(operator) {
  const value = searchInput.value;
  const needsSpace = value && !/\s$/.test(value);
  searchInput.value = `${value}${needsSpace ? " " : ""}${operator}`;
  searchInput.focus();
  searchInput.setSelectionRange(searchInput.value.length, searchInput.value.length);
  updateSearchClear();
  const complete = operator.endsWith("attachment");
  if (complete) {
    setQuery(searchInput.value);
    searchHints.hidden = true;
  }
  openSearchHints();
}

export function syncSearchContext() {
  if (state.task === "people") {
    searchInput.placeholder = t("mail.search.placeholder.people");
    searchInput.setAttribute("aria-label", t("mail.search.aria.people"));
  } else if (state.task === "tasks") {
    searchInput.placeholder = t("mail.search.placeholder.tasks");
    searchInput.setAttribute("aria-label", t("mail.search.aria.tasks"));
  } else if (state.task === "files") {
    searchInput.placeholder = t("mail.search.placeholder.files");
    searchInput.setAttribute("aria-label", t("mail.search.aria.files"));
  } else if (state.task === "scheduled") {
    searchInput.placeholder = t("mail.search.placeholder.scheduled");
    searchInput.setAttribute("aria-label", t("mail.search.aria.scheduled"));
  } else if (state.task === "mail") {
    searchInput.placeholder = state.query ? t("mail.search.placeholder.allMail") : t("mail.search.placeholder.mail");
    searchInput.setAttribute("aria-label", t("mail.search.aria.allMail"));
  }
}
