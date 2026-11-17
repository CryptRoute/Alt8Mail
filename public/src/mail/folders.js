/* Folder tree, folder menu, folder editor and folder drag-and-drop. */
import { api } from "../core/api.js";
import { appConfirm, folderEditorOverlay, folderList, folderMenu, messageList } from "../core/dom.js";
import { esc, localizedFolderLabel } from "../core/helpers.js";
import { state } from "../core/state.js";
import { showError, showToast } from "../core/toast.js";
import { t, tn } from "../../i18n.mjs";

let hooks = {
  refreshMailbox: async () => {},
  renderRules: () => {},
  renderList: () => {},
  renderReader: () => {},
  syncSelectionUi: () => {},
  syncCommandBar: () => {},
  performBulkAction: async () => {},
  selectedThreads: () => [],
  threadById: () => null,
  ensureSubscriptionsFolder: () => {},
  showSubscriptionCleanupReport: () => {},
  cloneMessageRecord: (message) => message,
  removeFolderMessagesLocally: () => {},
  rebuildIndex: () => {}
};
export function configureFolders(next = {}) { hooks = { ...hooks, ...next }; }

export function unreadCount(folderId) {
  if (folderId === "starred") return 0;
  if (Number.isFinite(Number(state.folderUnread[folderId]))) return Number(state.folderUnread[folderId]);
  return state.messages.filter((message) => message.folder === folderId && message.unread).length;
}

function folderBadge(folder) {
  if (folder.id === "starred" || (folder.virtual && folder.id !== "subscriptions")) return null;
  const mode = folder.badgeMode || "unread";
  if (mode === "none") return null;
  const count = mode === "total"
    ? (Number.isFinite(Number(folder.total)) ? Number(folder.total)
      : Number.isFinite(Number(state.folderCounts[folder.id])) ? Number(state.folderCounts[folder.id])
      : state.messages.filter((message) => message.folder === folder.id).length)
    : (Number.isFinite(Number(folder.unread)) ? Number(folder.unread) : unreadCount(folder.id));
  return count ? { count, neutral: mode === "total" } : null;
}

export function folderSortValue(folder) {
  return Number.isFinite(Number(folder.order)) ? Number(folder.order) : 999;
}

function folderDepth(folder, folders = state.folders) {
  let depth = 0;
  let parentId = folder.parentId || "";
  const seen = new Set();
  while (parentId && depth < 5 && !seen.has(parentId)) {
    seen.add(parentId);
    const parent = folders.find((item) => item.id === parentId);
    if (!parent) break;
    depth += 1;
    parentId = parent.parentId || "";
  }
  return depth;
}

const PRIMARY_FOLDER_IDS = new Set(["inbox", "starred", "snoozed", "sent", "drafts", "archive", "spam", "trash"]);

function folderById(id, folders = state.folders) {
  return (folders || []).find((folder) => folder.id === id) || null;
}

function folderIsCustom(folder) {
  return Boolean(folder && !folder.system && !folder.virtual && !folder.delegated && !PRIMARY_FOLDER_IDS.has(folder.id));
}

function folderCanAcceptChildren(folder) {
  return Boolean(folder && !folder.virtual && !folder.delegated);
}

function folderChildrenOf(id, folders = state.folders) {
  return (folders || [])
    .filter((folder) => (folder.parentId || "") === id)
    .sort((a, b) => {
      const order = folderSortValue(a) - folderSortValue(b);
      return order || String(a.label || "").localeCompare(String(b.label || ""), undefined, { sensitivity: "base" });
    });
}

function folderDescendantIds(id, folders = state.folders) {
  const descendants = new Set();
  const collect = (parentId) => {
    for (const child of folderChildrenOf(parentId, folders)) {
      if (child.id === id || descendants.has(child.id)) continue;
      descendants.add(child.id);
      collect(child.id);
    }
  };
  collect(id);
  return descendants;
}

function folderHasRenderableParent(folder, folders) {
  if (!folder.parentId) return false;
  const parent = folderById(folder.parentId, folders);
  return Boolean(parent && !parent.virtual && parent.id !== folder.id);
}

function folderRowMarkup(folder, depth, folders, visited) {
  if (visited.has(folder.id)) return "";
  visited.add(folder.id);
  const children = folderChildrenOf(folder.id, folders).filter((child) => !visited.has(child.id));
  const hasChildren = children.length > 0;
  const collapsed = Boolean(folder.collapsed);
  const active = state.task === "mail" && state.folder === folder.id && !state.query;
  const badge = folderBadge(folder);
  const color = folder.color ? ` style="color:${esc(folder.color)}"` : "";
  const canDrag = folderIsCustom(folder) && !folder.connected;
  const canDrop = folderCanAcceptChildren(folder) && !folder.connected;
  const toggle = hasChildren
    ? `<button class="folder-toggle${collapsed ? " is-collapsed" : ""}" data-folder-toggle="${esc(folder.id)}" type="button" aria-expanded="${collapsed ? "false" : "true"}" aria-label="${esc(collapsed ? t("mail.folders.expandSubfoldersAria", { folder: folder.label }) : t("mail.folders.collapseSubfoldersAria", { folder: folder.label }))}" title="${esc(collapsed ? t("mail.folders.expandSubfoldersTitle") : t("mail.folders.collapseSubfoldersTitle"))}"><svg class="icon" aria-hidden="true"><use href="#i-chevron-down"/></svg></button>`
    : `<span class="folder-toggle-spacer" aria-hidden="true"></span>`;
  const row = `<div class="folder-row${active ? " is-active" : ""}${depth ? " is-child" : ""}${hasChildren ? " is-parent" : ""}${collapsed ? " is-collapsed" : ""}${canDrag ? " is-draggable" : ""}" data-folder-row="${esc(folder.id)}"${canDrop ? ` data-folder-drop="${esc(folder.id)}"` : ""} style="--folder-depth:${depth}"${canDrag ? ' draggable="true"' : ""}>
    ${toggle}
    <button class="folder-item${active ? " is-active" : ""}" data-folder="${esc(folder.id)}" type="button"${active ? ' aria-current="page"' : ""} title="${esc(folder.label)}">
      <svg class="icon" aria-hidden="true"${color}><use href="#${esc(folder.icon || "i-folder")}"/></svg>
      <span class="folder-name">${esc(folder.label)}</span>
      ${badge ? `<span class="folder-badge${badge.neutral ? " neutral" : ""}" data-folder-badge="${esc(folder.id)}">${badge.count > 999 ? "999+" : badge.count}</span>` : ""}
    </button>
    <button class="folder-more" data-folder-menu="${esc(folder.id)}" type="button" aria-label="${esc(t("mail.folders.actionsFor", { folder: folder.label }))}" title="${esc(t("mail.folders.actions"))}"${folder.delegated || folder.connected ? " hidden" : ""}><span aria-hidden="true">⋯</span></button>
  </div>`;
  if (!hasChildren || collapsed) return row;
  return row + children.map((child) => folderRowMarkup(child, depth + 1, folders, visited)).join("");
}

export function renderFolders() {
  const connected = Boolean(state.activeAccount && !state.activeAccount.isNative);
  const addButton = document.getElementById("folderAddBtn");
  if (addButton) addButton.closest(".folder-add-row")?.toggleAttribute("hidden", connected);
  const folders = (state.folders || []).slice().sort((a, b) => folderSortValue(a) - folderSortValue(b));
  const sections = [
    { key: "favorites", label: t("mail.folders.section.favorites") },
    { key: "folders", label: t("mail.folders.section.folders") }
  ];
  const visited = new Set();
  const reachable = new Set();
  const markReachable = (id) => {
    for (const child of folderChildrenOf(id, folders)) {
      if (reachable.has(child.id)) continue;
      reachable.add(child.id);
      markReachable(child.id);
    }
  };
  folders.filter((folder) => !folderHasRenderableParent(folder, folders)).forEach((folder) => {
    reachable.add(folder.id);
    markReachable(folder.id);
  });
  let html = "";
  for (const section of sections) {
    html += `<div class="folder-section">${section.label}</div>`;
    const roots = folders.filter((folder) => folder.section === section.key && !folderHasRenderableParent(folder, folders));
    html += roots.map((folder) => folderRowMarkup(folder, 0, folders, visited)).join("");
    if (section.key === "favorites") {
      const scheduledCount = state.pendingSends.filter((item) => item.scheduled).length;
      const active = state.task === "scheduled";
      html += `<div class="folder-row folder-scheduled-row"><span class="folder-toggle-spacer" aria-hidden="true"></span><button class="folder-item${active ? " is-active" : ""}" data-open-scheduled type="button"${active ? ' aria-current="page"' : ""}>
        <svg class="icon" aria-hidden="true"><use href="#i-calendar-clock"/></svg>
        <span class="folder-name">${esc(t("shell.scheduled"))}</span>
        ${scheduledCount ? `<span class="folder-badge neutral">${scheduledCount}</span>` : ""}
      </button></div>`;
    }
  }
  // Safety net for legacy records whose parent disappeared or formed a cycle:
  // never hide a folder just because its place in the tree is broken. Folders
  // that are only hidden because a parent is collapsed stay out of this list.
  const orphans = folders.filter((folder) => !reachable.has(folder.id));
  if (orphans.length) {
    html += orphans.map((folder) => folderRowMarkup(folder, 0, folders, visited)).join("");
  }
  folderList.innerHTML = html;
}

async function toggleFolderCollapsed(id) {
  const folder = folderById(id);
  if (!folder) return;
  const next = !folder.collapsed;
  folder.collapsed = next;
  renderFolders();
  try {
    const data = await api(`/api/folders/${encodeURIComponent(id)}`, { method: "PATCH", body: { collapsed: next } });
    if (data?.folder) {
      const current = folderById(id);
      if (current) Object.assign(current, data.folder);
    }
  } catch (error) {
    folder.collapsed = !next;
    renderFolders();
    showError(error);
  }
}

function setupMailFolderDragAndDrop() {
  if (!folderList || !messageList) return;
  let draggedThreadIds = null;
  let draggedFolderId = null;
  const clearDropHighlights = () => {
    folderList.querySelectorAll(".is-mail-drop, .is-folder-drop").forEach((row) => row.classList.remove("is-mail-drop", "is-folder-drop"));
  };
  const cleanupDrag = () => {
    draggedThreadIds = null;
    draggedFolderId = null;
    clearDropHighlights();
    messageList.querySelectorAll(".msg.is-dragging").forEach((row) => row.classList.remove("is-dragging"));
    folderList.querySelectorAll(".folder-row.is-dragging").forEach((row) => row.classList.remove("is-dragging"));
  };
  const interactiveDragStart = (event) => {
    const under = document.elementFromPoint(event.clientX, event.clientY);
    return Boolean(under?.closest("button, input, label, a"));
  };

  folderList.addEventListener("click", (event) => {
    const toggle = event.target.closest("[data-folder-toggle]");
    if (!toggle) return;
    event.preventDefault();
    event.stopPropagation();
    toggleFolderCollapsed(toggle.getAttribute("data-folder-toggle"));
  });

  messageList.addEventListener("dragstart", (event) => {
    const row = event.target.closest(".msg[data-thread-id]");
    if (!row || !event.dataTransfer || interactiveDragStart(event)) return;
    const id = row.getAttribute("data-thread-id");
    const threads = state.selectedIds.has(id)
      ? hooks.selectedThreads?.() || []
      : [hooks.threadById?.(id)].filter(Boolean);
    if (!threads.length) { event.preventDefault(); return; }
    draggedThreadIds = threads.map((thread) => thread.id);
    event.dataTransfer.setData("application/x-a8m-threads", JSON.stringify(draggedThreadIds));
    event.dataTransfer.setData("text/plain", threads.map((thread) => thread.subject || "").join("\n"));
    event.dataTransfer.effectAllowed = "move";
    row.classList.add("is-dragging");
  });

  folderList.addEventListener("dragstart", (event) => {
    const row = event.target.closest('.folder-row[data-folder-row][draggable="true"]');
    if (!row || !event.dataTransfer) return;
    const under = document.elementFromPoint(event.clientX, event.clientY);
    if (under?.closest("button:not(.folder-item), a")) { event.preventDefault(); return; }
    draggedFolderId = row.getAttribute("data-folder-row");
    event.dataTransfer.setData("application/x-a8m-mail-folder", draggedFolderId);
    event.dataTransfer.effectAllowed = "move";
    row.classList.add("is-dragging");
  });

  folderList.addEventListener("dragover", (event) => {
    const types = Array.from(event.dataTransfer?.types || []);
    if (!types.includes("application/x-a8m-threads") && !types.includes("application/x-a8m-mail-folder")) return;
    const target = event.target.closest("[data-folder-drop]");
    if (!target) return;
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
    clearDropHighlights();
    target.classList.add(types.includes("application/x-a8m-threads") ? "is-mail-drop" : "is-folder-drop");
  });

  folderList.addEventListener("dragleave", (event) => {
    if (event.relatedTarget && folderList.contains(event.relatedTarget)) return;
    clearDropHighlights();
  });

  folderList.addEventListener("drop", async (event) => {
    const target = event.target.closest("[data-folder-drop]");
    if (!target) return;
    const types = Array.from(event.dataTransfer?.types || []);
    const targetFolderId = target.getAttribute("data-folder-drop");
    const targetFolder = folderById(targetFolderId);
    if (!targetFolder) return;
    if (types.includes("application/x-a8m-threads")) {
      event.preventDefault();
      let ids = draggedThreadIds || [];
      try {
        const encoded = event.dataTransfer.getData("application/x-a8m-threads");
        if (encoded) ids = JSON.parse(encoded);
      } catch {}
      cleanupDrag();
      const threads = (ids || []).map((id) => hooks.threadById?.(id)).filter(Boolean);
      if (!threads.length) return;
      if (state.task === "mail" && state.folder === targetFolderId && !state.query) {
        showToast(t("mail.folders.alreadyIn", { folder: targetFolder.label }));
        return;
      }
      await hooks.performBulkAction?.("move", targetFolderId, { threads });
      showToast(tn("mail.folders.movedTo", threads.length, { count: threads.length, folder: targetFolder.label }));
      return;
    }
    if (types.includes("application/x-a8m-mail-folder")) {
      event.preventDefault();
      const sourceId = event.dataTransfer.getData("application/x-a8m-mail-folder") || draggedFolderId;
      cleanupDrag();
      const source = folderById(sourceId);
      if (!source) return;
      if (!folderIsCustom(source)) { showToast(t("mail.folders.primaryMove")); return; }
      if (sourceId === targetFolderId) return;
      if ((source.parentId || "") === targetFolderId) return;
      if (folderDescendantIds(sourceId).has(targetFolderId)) {
        showToast(t("mail.folders.cycleMove"));
        return;
      }
      await patchFolder(sourceId, { parentId: targetFolderId }, t("mail.folders.movedToFolder", { folder: targetFolder.label }));
    }
  });

  document.addEventListener("dragend", cleanupDrag);
}

setupMailFolderDragAndDrop();

function folderMenuSystem(folder) {
  return Boolean(folder.system || ["inbox", "starred", "sent", "drafts", "archive", "spam", "trash"].includes(folder.id));
}

const folderMenuIcon = (name) => `<svg class="icon" aria-hidden="true"><use href="#i-${name}"/></svg>`;

function folderMenuCounts(folder) {
  const local = (state.messages || []).filter((message) => {
    if (folder.id === "starred") return (message.starred || message.flagged) && message.folder !== "spam" && message.folder !== "trash";
    if (folder.id === "snoozed") return message.snoozedUntil && Date.parse(message.snoozedUntil) > Date.now();
    return message.folder === folder.id;
  });
  const total = Number.isFinite(Number(state.folderCounts?.[folder.id])) ? Number(state.folderCounts[folder.id]) : local.length;
  const unread = Number.isFinite(Number(state.folderUnread?.[folder.id])) ? Number(state.folderUnread[folder.id]) : local.filter((message) => message.unread).length;
  return { total, unread };
}

function subscriptionFolderMenuHTML(folder) {
  const counts = folderMenuCounts(folder);
  const disabled = (condition) => condition ? " disabled" : "";
  const unsubscribable = (state.messages || []).filter((message) =>
    message.folder === "subscriptions" && message.unsubscribe && typeof message.unsubscribe === "object").length;
  return `<div class="folder-menu-head"><svg class="icon" aria-hidden="true"><use href="#i-mail"/></svg><div><strong>${esc(folder.label)}</strong><span>${esc(tn("mail.folders.menuSummarySystem", counts.total, { count: counts.total, unread: counts.unread }))}</span></div></div>
    <button type="button" data-folder-action="rename" disabled title="${esc(t("mail.folders.systemRename"))}">${folderMenuIcon("file")}${esc(t("mail.folders.rename"))}</button>
    <button type="button" data-folder-action="move" disabled title="${esc(t("mail.folders.systemMove"))}">${folderMenuIcon("folder")}${esc(t("mail.folders.move"))}</button>
    <button type="button" data-folder-action="delete" disabled title="${esc(t("mail.folders.systemDelete"))}">${folderMenuIcon("trash")}${esc(t("mail.folders.delete"))}</button>
    <div class="folder-menu-sep"></div>
    <button type="button" data-folder-action="empty"${disabled(counts.total === 0)}>${folderMenuIcon("archive")}${esc(t("mail.folders.emptySubscriptions"))}</button>
    <button type="button" data-folder-action="unsubscribe-all"${disabled(unsubscribable === 0)}>${folderMenuIcon("mail")}${esc(t("mail.folders.unsubscribeAll"))}<span class="folder-menu-count">${unsubscribable}</span></button>
    <div class="folder-menu-sep"></div>
    <button type="button" data-folder-action="mark-read"${disabled(counts.unread === 0)}>${folderMenuIcon("mail")}${esc(t("mail.folders.markAllRead"))}</button>`;
}

function folderMenuHTML(folder) {
  if (folder.id === "subscriptions") return subscriptionFolderMenuHTML(folder);
  const system = folderMenuSystem(folder);
  const emptyDisabled = system && folder.id !== "spam" && folder.id !== "trash";
  const disabled = (condition) => condition ? " disabled" : "";
  const counts = folderMenuCounts(folder);
  return `<div class="folder-menu-head"><svg class="icon" aria-hidden="true"><use href="#${esc(folder.icon || "i-folder")}"/></svg><div><strong>${esc(folder.label)}</strong><span>${esc(tn("mail.folders.menuSummary", counts.total, { count: counts.total, unread: counts.unread, kind: system ? t("mail.folders.primary") : t("mail.folders.custom") }))}</span></div></div>
    <button type="button" data-folder-action="create-subfolder"${disabled(Boolean(folder.virtual))}>${folderMenuIcon("plus")}${esc(t("mail.folders.createSubfolder"))}</button>
    <button type="button" data-folder-action="rename"${disabled(system)}>${folderMenuIcon("file")}${esc(t("mail.folders.rename"))}</button>
    <button type="button" data-folder-action="color">${folderMenuIcon("tag")}${esc(t("mail.folders.changeColor"))}<span class="menu-chevron">›</span></button>
    <button type="button" data-folder-action="count">${folderMenuIcon("sliders")}${esc(t("mail.folders.changeCount"))}<span class="menu-chevron">›</span></button>
    <div class="folder-menu-sep"></div>
    <button type="button" data-folder-action="move"${disabled(system)}>${folderMenuIcon("folder")}${esc(t("mail.folders.move"))}</button>
    <button type="button" data-folder-action="delete"${disabled(system)}>${folderMenuIcon("trash")}${esc(t("mail.folders.delete"))}</button>
    <button type="button" data-folder-action="empty"${disabled(emptyDisabled)}>${folderMenuIcon("archive")}${esc(t("mail.folders.empty"))}</button>
    <div class="folder-menu-sep"></div>
    <button type="button" data-folder-action="mark-read">${folderMenuIcon("mail")}${esc(t("mail.folders.markAllRead"))}</button>
    <div class="folder-menu-sep"></div>
    <button type="button" data-folder-action="favorite">${folderMenuIcon("star")}${esc(folder.section === "favorites" ? t("mail.folders.removeFavorite") : t("mail.folders.addFavorite"))}</button>
    <button type="button" data-folder-action="up">${folderMenuIcon("chevron-down")}${esc(t("mail.folders.moveUp"))}</button>
    <button type="button" data-folder-action="down">${folderMenuIcon("chevron-down")}${esc(t("mail.folders.moveDown"))}</button>`;
}

export function openFolderMenu(folderId, event) {
  const folder = (state.folders || []).find((item) => item.id === folderId);
  if (!folder || !folderMenu || folder.delegated) return;
  state.folderMenuTarget = folderId;
  folderMenu.innerHTML = folderMenuHTML(folder);
  folderMenu.hidden = false;
  const rect = event?.currentTarget?.getBoundingClientRect?.() || { left: event?.clientX || 120, right: event?.clientX || 120, bottom: event?.clientY || 120, top: event?.clientY || 120 };
  const width = 286;
  const left = Math.min(window.innerWidth - width - 10, Math.max(10, event?.clientX ? event.clientX : rect.right + 6));
  const top = Math.min(window.innerHeight - folderMenu.offsetHeight - 10, Math.max(10, event?.clientY ? event.clientY : rect.bottom + 4));
  folderMenu.style.left = `${left}px`;
  folderMenu.style.top = `${top}px`;
  folderMenu.style.width = `${width}px`;
}

export function closeFolderMenu() {
  if (folderMenu) folderMenu.hidden = true;
  state.folderMenuTarget = "";
}

export function folderMenuFolder() {
  return (state.folders || []).find((item) => item.id === state.folderMenuTarget) || null;
}

export function openFolderSubmenu(kind) {
  const folder = folderMenuFolder();
  if (!folder) return;
  if (kind === "color") {
    folderMenu.innerHTML = `<div class="folder-subhead"><button type="button" data-folder-submenu-back>‹</button><strong>${esc(t("mail.folders.changeColor"))}</strong></div>
      <div class="folder-color-grid">
        ${["", "#2563eb", "#0f766e", "#6d28d9", "#be123c", "#b45309", "#475569", "#0369a1", "#4d7c0f"].map((color) => `<button type="button" data-folder-color="${color}" class="${(folder.color || "") === color ? "is-active" : ""}" title="${esc(color || t("mail.folders.defaultColor"))}"><span style="${color ? `--swatch:${color}` : ""}"></span>${color ? "" : esc(t("mail.folders.defaultColor"))}</button>`).join("")}
      </div>`;
    return;
  }
  folderMenu.innerHTML = `<div class="folder-subhead"><button type="button" data-folder-submenu-back>‹</button><strong>${esc(t("mail.folders.showInFolder"))}</strong></div>
    ${[["unread", t("mail.folders.unreadMessages"), t("mail.folders.unread")], ["total", t("mail.folders.allMessages"), t("mail.folders.all")], ["none", t("mail.folders.noCount"), "—"]].map(([value, label, chip]) => `<button type="button" data-folder-badge-mode="${value}" class="${(folder.badgeMode || "unread") === value ? "is-active" : ""}"><span class="folder-count-choice">${esc(chip)}</span>${esc(label)}${(folder.badgeMode || "unread") === value ? `<span class="folder-check">✓</span>` : ""}</button>`).join("")}`;
}

export function openFolderMoveMenu() {
  const folder = folderMenuFolder();
  if (!folder) return;
  const descendants = new Set();
  const collect = (id) => (state.folders || []).filter((item) => item.parentId === id).forEach((child) => { descendants.add(child.id); collect(child.id); });
  collect(folder.id);
  const candidates = (state.folders || []).filter((item) => item.id !== folder.id && !descendants.has(item.id) && !item.virtual && !item.delegated);
  folderMenu.innerHTML = `<div class="folder-subhead"><button type="button" data-folder-submenu-back>‹</button><strong>${esc(t("mail.folders.moveFolder"))}</strong></div>
    <button type="button" data-folder-parent-id=""><span class="folder-count-choice">/</span>${esc(t("mail.folders.topLevel"))}</button>
    ${candidates.map((item) => `<button type="button" data-folder-parent-id="${esc(item.id)}"><span class="folder-count-choice"><svg class="icon" aria-hidden="true"><use href="#i-folder"/></svg></span>${esc(item.label)}</button>`).join("")}`;
}

export async function loadFolders() {
  try {
    const data = await api("/api/folders");
    state.folders = (data.folders || []).map((folder) => ({ ...folder, label: localizedFolderLabel(folder) }));
    hooks.ensureSubscriptionsFolder?.();
    renderFolders();
  } catch (error) { showError(error); }
}

export function openFolderEditor(mode = "create", folder = null, parentId = "") {
  state.folderEditor = { mode, id: folder?.id || "", parentId: folder?.parentId || parentId };
  document.getElementById("folderEditorTitle").textContent = mode === "rename" ? t("mail.folders.renameTitle") : t("mail.folders.newTitle");
  document.getElementById("folderEditorSubtitle").textContent = mode === "rename" ? t("mail.folders.renameSubtitle") : t("mail.folders.newSubtitle");
  document.getElementById("folderEditorName").value = folder?.label || "";
  document.getElementById("folderEditorColor").value = folder?.color || "";
  const parent = document.getElementById("folderEditorParent");
  const excluded = new Set();
  if (folder) {
    excluded.add(folder.id);
    const collect = (id) => (state.folders || []).filter((item) => item.parentId === id).forEach((child) => { excluded.add(child.id); collect(child.id); });
    collect(folder.id);
  }
  parent.innerHTML = `<option value="">${esc(t("mail.folders.topLevel"))}</option>` + (state.folders || [])
    .filter((item) => !item.virtual && !item.delegated && !excluded.has(item.id))
    .map((item) => {
      const indent = "— ".repeat(folderDepth(item, state.folders || []));
      return `<option value="${esc(item.id)}"${state.folderEditor.parentId === item.id ? " selected" : ""}>${esc(indent + item.label)}</option>`;
    }).join("");
  parent.disabled = mode === "rename";
  document.getElementById("folderEditorError").hidden = true;
  document.getElementById("folderEditorSave").textContent = mode === "rename" ? t("mail.folders.saveChanges") : t("mail.folders.createFolder");
  folderEditorOverlay.hidden = false;
  setTimeout(() => document.getElementById("folderEditorName")?.focus(), 70);
}

export function closeFolderEditor() {
  folderEditorOverlay.hidden = true;
  state.folderEditor = null;
}

export async function saveFolderEditor() {
  const editor = state.folderEditor;
  if (!editor) return;
  const name = document.getElementById("folderEditorName").value.trim();
  const parentId = document.getElementById("folderEditorParent").value;
  const color = document.getElementById("folderEditorColor").value;
  const error = document.getElementById("folderEditorError");
  const button = document.getElementById("folderEditorSave");
  if (!name) { error.textContent = t("mail.folders.nameRequired"); error.hidden = false; return; }
  button.disabled = true;
  try {
    if (editor.mode === "rename") {
      await api(`/api/folders/${encodeURIComponent(editor.id)}`, { method: "PATCH", body: { label: name, color } });
    } else {
      await api("/api/folders", { method: "POST", body: { label: name, parentId, color } });
    }
    closeFolderEditor();
    closeFolderMenu();
    await loadFolders();
    await hooks.refreshMailbox?.({ quiet: true });
    if (editor.mode === "rename") {
      try { state.sieve = await api("/api/sieve"); hooks.renderRules?.(); } catch {}
    }
    showToast(editor.mode === "rename" ? t("mail.folders.renamed") : t("mail.folders.created"));
  } catch (saveError) {
    error.textContent = saveError.message || t("mail.folders.saveFailed");
    error.hidden = false;
  } finally {
    button.disabled = false;
  }
}

export async function patchFolder(id, body, message = t("mail.folders.updated")) {
  try {
    await api(`/api/folders/${encodeURIComponent(id)}`, { method: "PATCH", body });
    await loadFolders();
    closeFolderMenu();
    showToast(message);
  } catch (error) { showError(error); }
}

export async function folderMenuAction(action) {
  const folder = folderMenuFolder();
  if (!folder) return;
  if (action === "create-subfolder") { closeFolderMenu(); openFolderEditor("create", null, folder.id); return; }
  if (action === "rename") { closeFolderMenu(); openFolderEditor("rename", folder); return; }
  if (action === "color") { openFolderSubmenu("color"); return; }
  if (action === "count") { openFolderSubmenu("count"); return; }
  if (action === "move") { openFolderMoveMenu(); return; }
  if (action === "favorite") {
    await patchFolder(folder.id, { section: folder.section === "favorites" ? "folders" : "favorites" }, folder.section === "favorites" ? t("mail.folders.removedFavorite") : t("mail.folders.addedFavorite"));
    return;
  }
  if (action === "mark-read") {
    closeFolderMenu();
    try {
      const result = await api(`/api/folders/${encodeURIComponent(folder.id)}/mark-read`, { method: "POST", body: {} });
      await hooks.refreshMailbox?.({ quiet: true });
      showToast(tn("mail.folders.markedRead", result.affected || 0, { count: result.affected || 0 }));
    } catch (error) { showError(error); }
    return;
  }
  if (action === "unsubscribe-all") {
    closeFolderMenu();
    if (!(await appConfirm({
      title: t("mail.folders.unsubscribeTitle"),
      message: t("mail.folders.unsubscribeBody"),
      confirmLabel: t("mail.folders.unsubscribeConfirm"),
      tone: "danger",
      icon: "i-mail"
    }))) return;
    try {
      showToast(t("mail.folders.unsubscribing"));
      const data = await api("/api/folders/subscriptions/unsubscribe-all", { method: "POST", body: {} });
      await hooks.refreshMailbox?.({ quiet: true }).catch(() => {});
      renderFolders();
      hooks.renderList?.({ preserveScroll: true });
      hooks.renderReader?.();
      hooks.syncSelectionUi?.();
      hooks.syncCommandBar?.();
      hooks.showSubscriptionCleanupReport?.(data);
    } catch (error) { showError(error); }
    return;
  }
  if (action === "empty") {
    closeFolderMenu();
    if (!(await appConfirm({ title: t("mail.folders.emptyTitle", { folder: folder.label }), message: t("mail.folders.emptyBody"), confirmLabel: t("mail.folders.emptyConfirm"), tone: "danger" }))) return;
    const snapshot = state.messages.filter((message) => message.folder === folder.id).map(hooks.cloneMessageRecord);
    const visible = state.folder === folder.id;
    if (visible) messageList.querySelectorAll(".msg").forEach((row) => row.classList.add("is-removing"));
    const request = api(`/api/folders/${encodeURIComponent(folder.id)}/empty`, { method: "POST", body: {} })
      .then((result) => ({ ok: true, result }), (error) => ({ ok: false, error }));
    if (visible) await new Promise((resolve) => setTimeout(resolve, 190));
    hooks.removeFolderMessagesLocally?.(folder.id);
    renderFolders();
    hooks.renderList?.({ preserveScroll: true });
    hooks.renderReader?.();
    hooks.syncSelectionUi?.();
    hooks.syncCommandBar?.();
    const outcome = await request;
    if (!outcome.ok) {
      const byId = new Map(state.messages.map((message) => [message.id, message]));
      for (const saved of snapshot) {
        if (byId.has(saved.id)) Object.assign(byId.get(saved.id), saved);
        else state.messages.push(saved);
      }
      hooks.rebuildIndex?.();
      renderFolders();
      hooks.renderList?.();
      hooks.renderReader?.();
      hooks.syncSelectionUi?.();
      showError(outcome.error);
      return;
    }
    await hooks.refreshMailbox?.({ quiet: true }).catch(() => {});
    const removed = outcome.result.removed || snapshot.length || 0;
    showToast(tn("mail.folders.removed", removed, { count: removed }));
    return;
  }
  if (action === "delete") {
    closeFolderMenu();
    if (!(await appConfirm({ title: t("mail.folders.deleteTitle", { folder: folder.label }), message: t("mail.folders.deleteBody"), confirmLabel: t("mail.folders.deleteConfirm"), tone: "danger" }))) return;
    try {
      await api(`/api/folders/${encodeURIComponent(folder.id)}`, { method: "DELETE" });
      state.sieve = await api("/api/sieve");
      await loadFolders();
      await hooks.refreshMailbox?.({ quiet: true });
      hooks.renderRules?.();
      showToast(t("mail.folders.deleted"));
    } catch (error) { showError(error); }
    return;
  }
  if (action === "up" || action === "down") {
    closeFolderMenu();
    const sorted = (state.folders || []).slice().sort((a, b) => folderSortValue(a) - folderSortValue(b));
    const sectionFolders = sorted.filter((item) => item.section === folder.section && !item.virtual);
    const sectionIndex = sectionFolders.findIndex((item) => item.id === folder.id);
    const targetIndex = action === "up" ? sectionIndex - 1 : sectionIndex + 1;
    if (sectionIndex < 0 || targetIndex < 0 || targetIndex >= sectionFolders.length) return;
    const target = sectionFolders[targetIndex];
    const index = sorted.findIndex((item) => item.id === folder.id);
    const swap = sorted.findIndex((item) => item.id === target.id);
    [sorted[index], sorted[swap]] = [sorted[swap], sorted[index]];
    try {
      await api("/api/folders/order", { method: "PUT", body: { ids: sorted.filter((item) => item.id !== "subscriptions").map((item) => item.id) } });
      state.folders = sorted.map((item, order) => ({ ...item, order }));
      renderFolders();
      showToast(t("mail.folders.orderSaved"));
    } catch (error) { showError(error); }
  }
}
