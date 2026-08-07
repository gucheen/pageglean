const state = {
  filter: "",
  query: "",
  bookmarks: [],
  selected: new Set(),
  importFile: null,
  importMapping: {},
  setupToken: new URLSearchParams(location.search).get("token") || "",
};

const $ = (selector) => document.querySelector(selector);
const loadingView = $("#loadingView");
const authView = $("#authView");
const appView = $("#appView");
const authButton = $("#authButton");
const authError = $("#authError");
const bookmarkDialog = $("#bookmarkDialog");
const settingsDialog = $("#settingsDialog");
const importDialog = $("#importDialog");
let searchTimer;
let toastTimer;
let importPreviewVersion = 0;

async function request(path, options = {}) {
  const headers = new Headers(options.headers || {});
  if (options.body && !(options.body instanceof FormData) && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  const response = await fetch(path, { ...options, headers });
  if (response.status === 204) return null;
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(payload.error || `请求失败（${response.status}）`);
  return payload;
}

function base64URLToBytes(value) {
  const padding = "=".repeat((4 - (value.length % 4)) % 4);
  const base64 = (value + padding).replace(/-/g, "+").replace(/_/g, "/");
  return Uint8Array.from(atob(base64), (char) => char.charCodeAt(0));
}

function bytesToBase64URL(value) {
  const bytes = new Uint8Array(value);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
}

function creationOptions(payload) {
  const options = structuredClone(payload.publicKey || payload);
  options.challenge = base64URLToBytes(options.challenge);
  options.user.id = base64URLToBytes(options.user.id);
  options.excludeCredentials = (options.excludeCredentials || []).map((item) => ({
    ...item,
    id: base64URLToBytes(item.id),
  }));
  return options;
}

function requestOptions(payload) {
  const options = structuredClone(payload.publicKey || payload);
  options.challenge = base64URLToBytes(options.challenge);
  options.allowCredentials = (options.allowCredentials || []).map((item) => ({
    ...item,
    id: base64URLToBytes(item.id),
  }));
  return options;
}

function credentialJSON(credential) {
  if (typeof credential.toJSON === "function") return credential.toJSON();
  const response = credential.response;
  const result = {
    id: credential.id,
    rawId: bytesToBase64URL(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      clientDataJSON: bytesToBase64URL(response.clientDataJSON),
    },
  };
  if (response.attestationObject) {
    result.response.attestationObject = bytesToBase64URL(response.attestationObject);
    result.response.transports = response.getTransports?.() || [];
  } else {
    result.response.authenticatorData = bytesToBase64URL(response.authenticatorData);
    result.response.signature = bytesToBase64URL(response.signature);
    result.response.userHandle = response.userHandle ? bytesToBase64URL(response.userHandle) : null;
  }
  return result;
}

async function registerPasskey() {
  setButtonBusy(authButton, true, "正在创建 Passkey…");
  authError.textContent = "";
  try {
    const payload = await request("/api/auth/register/start", {
      method: "POST",
      body: JSON.stringify({ token: state.setupToken, label: "Passkey" }),
    });
    const credential = await navigator.credentials.create({ publicKey: creationOptions(payload) });
    await request("/api/auth/register/finish", {
      method: "POST",
      body: JSON.stringify(credentialJSON(credential)),
    });
    history.replaceState({}, "", "/");
    state.setupToken = "";
    showApp();
    await loadBookmarks();
  } catch (error) {
    authError.textContent = friendlyCredentialError(error);
  } finally {
    setButtonBusy(authButton, false, "注册 Passkey");
  }
}

async function loginPasskey() {
  setButtonBusy(authButton, true, "等待 Passkey…");
  authError.textContent = "";
  try {
    const payload = await request("/api/auth/login/start", { method: "POST" });
    const credential = await navigator.credentials.get({ publicKey: requestOptions(payload) });
    await request("/api/auth/login/finish", {
      method: "POST",
      body: JSON.stringify(credentialJSON(credential)),
    });
    showApp();
    await loadBookmarks();
  } catch (error) {
    authError.textContent = friendlyCredentialError(error);
  } finally {
    setButtonBusy(authButton, false, "使用 Passkey");
  }
}

function friendlyCredentialError(error) {
  if (error?.name === "NotAllowedError") return "操作已取消，或 Passkey 请求已超时。";
  if (error?.name === "InvalidStateError") return "这个 Passkey 已经注册。";
  return error?.message || "Passkey 操作失败。";
}

function setButtonBusy(button, busy, label) {
  button.disabled = busy;
  button.textContent = label;
}

async function init() {
  try {
    const status = await request("/api/status");
    loadingView.hidden = true;
    if (status.authenticated) {
      showApp();
      await loadBookmarks();
      return;
    }
    showAuth(status);
  } catch (error) {
    loadingView.querySelector("p").textContent = error.message;
  }
}

function showAuth(status) {
  appView.hidden = true;
  authView.hidden = false;
  const setupHelp = $("#setupHelp");
  if (state.setupToken) {
    $("#authTitle").textContent = status.setupRequired ? "创建你的 Passkey" : "恢复访问权限";
    $("#authDescription").textContent = "这个一次性链接将注册一个新的 Passkey，完成后链接立即失效。";
    authButton.textContent = "注册 Passkey";
    authButton.hidden = false;
    setupHelp.hidden = true;
    authButton.onclick = registerPasskey;
  } else if (status.setupRequired) {
    $("#authTitle").textContent = "完成首次初始化";
    $("#authDescription").textContent = "先从服务器生成一个短时有效的初始化地址。";
    authButton.hidden = true;
    setupHelp.hidden = false;
  } else {
    $("#authTitle").textContent = "用 Passkey 登录";
    $("#authDescription").textContent = "你的书签保持私密，只能通过已注册的 Passkey 访问。";
    authButton.textContent = "使用 Passkey";
    authButton.hidden = false;
    setupHelp.hidden = true;
    authButton.onclick = loginPasskey;
  }
}

function showApp() {
  loadingView.hidden = true;
  authView.hidden = true;
  appView.hidden = false;
}

async function loadBookmarks() {
  const params = new URLSearchParams();
  if (state.query) params.set("q", state.query);
  if (state.filter) params.set("state", state.filter);
  params.set("limit", "100");
  try {
    const payload = await request(`/api/bookmarks?${params}`);
    state.bookmarks = payload.bookmarks || [];
    state.selected.clear();
    renderBookmarks();
  } catch (error) {
    if (error.message.includes("Passkey")) {
      const status = await request("/api/status");
      showAuth(status);
      return;
    }
    showToast(error.message);
  }
}

function renderBookmarks() {
  const list = $("#bookmarkList");
  const empty = $("#emptyState");
  list.replaceChildren();
  renderBulkBar();
  $("#resultSummary").textContent = state.bookmarks.length ? `${state.bookmarks.length} 条结果` : "";
  if (!state.bookmarks.length) {
    empty.hidden = false;
    list.hidden = true;
    $("#emptyTitle").textContent = state.query ? "没有找到相关书签" : "保存第一个链接";
    $("#emptyDescription").textContent = state.query ? "换一个记得住的词试试。" : "从一个值得日后重看的网页开始。";
    $("#emptyAddButton").hidden = Boolean(state.query);
    return;
  }
  empty.hidden = true;
  list.hidden = false;
  for (const bookmark of state.bookmarks) list.append(bookmarkRow(bookmark));
}

function bookmarkRow(bookmark) {
  const row = element("article", "bookmark-row");
  const selected = document.createElement("input");
  selected.type = "checkbox";
  selected.className = "select-bookmark";
  selected.setAttribute("aria-label", `选择 ${bookmark.title || bookmark.url}`);
  selected.checked = state.selected.has(bookmark.id);
  selected.addEventListener("change", () => {
    if (selected.checked) state.selected.add(bookmark.id);
    else state.selected.delete(bookmark.id);
    renderBulkBar();
  });
  const star = element("button", `star-button${bookmark.starred ? " active" : ""}`, bookmark.starred ? "★" : "☆");
  star.type = "button";
  star.title = bookmark.starred ? "取消收藏" : "收藏";
  star.addEventListener("click", () => updateBookmark(bookmark.id, { starred: !bookmark.starred }));

  const body = element("div", "bookmark-body");
  const title = element("a", "bookmark-title", bookmark.title || bookmark.url);
  title.href = bookmark.url;
  title.target = "_blank";
  title.rel = "noopener noreferrer";
  const meta = element("div", "bookmark-meta");
  const domain = element("span", "bookmark-domain", safeDomain(bookmark.url));
  const date = element("time", "", formatDate(bookmark.createdAt));
  date.dateTime = bookmark.createdAt;
  meta.append(domain, element("span", "", "·"), date);
  if (bookmark.unread) meta.append(element("span", "badge", "稍后阅读"));
  for (const tag of bookmark.tags || []) meta.append(element("span", "badge", `#${tag}`));
  if (bookmark.archiveStatus === "complete") {
    const archiveLink = element("a", "archive-link", "正文归档");
    archiveLink.href = `/archive/${bookmark.id}`;
    meta.append(archiveLink);
  } else if (bookmark.archiveStatus === "failed") {
    meta.append(element("span", "badge failed", "归档失败"));
  } else if (bookmark.archiveStatus === "idle") {
    meta.append(element("span", "badge", "未归档"));
  } else {
    meta.append(element("span", "badge pending", bookmark.archiveStatus === "processing" ? "正在归档" : "等待归档"));
  }
  body.append(title, meta);
  if (bookmark.note) body.append(element("p", "bookmark-note", bookmark.note));

  const actions = element("div", "row-actions");
  const unread = element("button", "text-button", bookmark.unread ? "已读" : "稍后读");
  unread.type = "button";
  unread.addEventListener("click", () => updateBookmark(bookmark.id, { unread: !bookmark.unread }));
  const edit = element("button", "text-button", "编辑");
  edit.type = "button";
  edit.addEventListener("click", () => openEditDialog(bookmark));
  const remove = element("button", "text-button danger", "删除");
  remove.type = "button";
  remove.addEventListener("click", () => deleteBookmark(bookmark));
  actions.append(unread, edit);
  if (bookmark.archiveStatus === "failed" || bookmark.archiveStatus === "idle") {
    const retry = element("button", "text-button", bookmark.archiveStatus === "idle" ? "归档" : "重试归档");
    retry.type = "button";
    retry.addEventListener("click", () => retryArchive(bookmark.id));
    actions.append(retry);
  }
  actions.append(remove);
  row.append(selected, star, body, actions);
  return row;
}

function renderBulkBar() {
  const bar = $("#bulkBar");
  const count = state.selected.size;
  bar.hidden = count === 0;
  $("#selectionCount").textContent = `已选择 ${count} 条`;
}

function selectedIDs() {
  return [...state.selected];
}

async function applyBulkUpdate(patch, successMessage) {
  const ids = selectedIDs();
  if (!ids.length) return;
  try {
    const result = await request("/api/bookmarks/bulk", {
      method: "PATCH",
      body: JSON.stringify({ ids, ...patch }),
    });
    showToast(`${successMessage}（${result.updated} 条）`);
    await loadBookmarks();
  } catch (error) {
    showToast(error.message);
  }
}

async function bulkDelete() {
  const ids = selectedIDs();
  if (!ids.length || !confirm(`确定删除选中的 ${ids.length} 条书签？此操作无法撤销。`)) return;
  try {
    const result = await request("/api/bookmarks/bulk", {
      method: "DELETE",
      body: JSON.stringify({ ids }),
    });
    showToast(`已删除 ${result.deleted} 条书签`);
    await loadBookmarks();
  } catch (error) {
    showToast(error.message);
  }
}

function element(tag, className = "", text = "") {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text) node.textContent = text;
  return node;
}

function safeDomain(value) {
  try { return new URL(value).hostname; } catch { return value; }
}

function formatDate(value) {
  return new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "short", day: "numeric" }).format(new Date(value));
}

function openCreateDialog() {
  $("#bookmarkForm").reset();
  $("#bookmarkId").value = "";
  $("#dialogTitle").textContent = "保存链接";
  $("#urlField").hidden = false;
  $("#urlInput").disabled = false;
  $("#dialogError").textContent = "";
  bookmarkDialog.showModal();
  $("#urlInput").focus();
}

function openEditDialog(bookmark) {
  $("#bookmarkForm").reset();
  $("#bookmarkId").value = bookmark.id;
  $("#dialogTitle").textContent = "编辑书签";
  $("#urlField").hidden = true;
  $("#urlInput").disabled = true;
  $("#titleInput").value = bookmark.title;
  $("#noteInput").value = bookmark.note;
  $("#tagsInput").value = (bookmark.tags || []).join(", ");
  $("#unreadInput").checked = bookmark.unread;
  $("#starredInput").checked = bookmark.starred;
  $("#dialogError").textContent = "";
  bookmarkDialog.showModal();
  $("#titleInput").focus();
}

async function submitBookmark(event) {
  event.preventDefault();
  const id = $("#bookmarkId").value;
  const payload = {
    title: $("#titleInput").value,
    note: $("#noteInput").value,
    tags: $("#tagsInput").value.split(/[,，]/).map((value) => value.trim()).filter(Boolean),
    unread: $("#unreadInput").checked,
    starred: $("#starredInput").checked,
  };
  if (!id) payload.url = $("#urlInput").value;
  setButtonBusy($("#saveButton"), true, "保存中…");
  $("#dialogError").textContent = "";
  try {
    const result = await request(id ? `/api/bookmarks/${id}` : "/api/bookmarks", {
      method: id ? "PATCH" : "POST",
      body: JSON.stringify(payload),
    });
    bookmarkDialog.close();
    showToast(result?.duplicate ? "这个链接已经保存过" : id ? "书签已更新" : "链接已保存");
    await loadBookmarks();
  } catch (error) {
    $("#dialogError").textContent = error.message;
  } finally {
    setButtonBusy($("#saveButton"), false, "保存");
  }
}

async function updateBookmark(id, patch) {
  try {
    await request(`/api/bookmarks/${id}`, { method: "PATCH", body: JSON.stringify(patch) });
    await loadBookmarks();
  } catch (error) {
    showToast(error.message);
  }
}

async function deleteBookmark(bookmark) {
  if (!confirm(`删除“${bookmark.title || bookmark.url}”？`)) return;
  try {
    await request(`/api/bookmarks/${bookmark.id}`, { method: "DELETE" });
    showToast("书签已删除");
    await loadBookmarks();
  } catch (error) {
    showToast(error.message);
  }
}

async function retryArchive(id) {
  try {
    await request(`/api/bookmarks/${id}/archive/retry`, { method: "POST" });
    showToast("已重新加入归档队列");
    await loadBookmarks();
  } catch (error) {
    showToast(error.message);
  }
}

async function openSettings() {
  $("#pairingCodeView").hidden = true;
  settingsDialog.showModal();
  await Promise.all([loadExtensionClients(), loadStorageStats()]);
}

async function loadStorageStats() {
  const container = $("#storageStats");
  try {
    const stats = await request("/api/stats");
    const totalSize = formatBytes(stats.databaseBytes + stats.archiveBytes);
    container.replaceChildren();
    container.append(element("span", "", `${stats.bookmarks} 条书签 · ${stats.archived} 条已归档 · 占用 ${totalSize}`));
    container.append(document.createElement("br"));
    container.append(element("span", "", stats.ftsEnabled ? "中文全文索引已启用" : "当前构建未启用 FTS5，正在使用基础检索"));
  } catch (error) {
    container.textContent = error.message;
  }
}

function formatBytes(value) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  if (value < 1024 * 1024 * 1024) return `${(value / 1024 / 1024).toFixed(1)} MB`;
  return `${(value / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

async function loadExtensionClients() {
  const container = $("#extensionClients");
  container.textContent = "正在读取…";
  try {
    const result = await request("/api/extension/clients");
    container.replaceChildren();
    if (!result.clients.length) {
      container.append(element("p", "client-empty", "还没有连接浏览器扩展。"));
      return;
    }
    for (const client of result.clients) {
      const row = element("div", "client-row");
      const info = element("div");
      info.append(element("strong", "", client.label));
      const used = client.lastUsedAt ? `最后使用：${formatDate(client.lastUsedAt)}` : `连接于：${formatDate(client.createdAt)}`;
      info.append(element("small", "", used));
      const revoke = element("button", "text-button danger", "撤销");
      revoke.type = "button";
      revoke.addEventListener("click", async () => {
        if (!confirm(`撤销“${client.label}”的扩展访问权限？`)) return;
        await request(`/api/extension/clients/${client.id}`, { method: "DELETE" });
        await loadExtensionClients();
      });
      row.append(info, revoke);
      container.append(row);
    }
  } catch (error) {
    container.textContent = error.message;
  }
}

async function generatePairingCode() {
  const button = $("#generatePairingButton");
  setButtonBusy(button, true, "生成中…");
  try {
    const result = await request("/api/extension/pairings", { method: "POST" });
    $("#pairingCode").textContent = result.code;
    $("#pairingExpiry").textContent = `有效期至 ${new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit" }).format(new Date(result.expiresAt))}`;
    $("#pairingCodeView").hidden = false;
  } catch (error) {
    showToast(error.message);
  } finally {
    setButtonBusy(button, false, "生成配对码");
  }
}

function openImportDialog() {
  settingsDialog.close();
  $("#importForm").reset();
  $("#importError").textContent = "";
  $("#importPreview").hidden = true;
  $("#csvMapping").hidden = true;
  $("#importButton").disabled = true;
  state.importFile = null;
  state.importMapping = {};
  importDialog.showModal();
}

async function previewImport() {
  if (!state.importFile) return;
  const version = ++importPreviewVersion;
  const preview = $("#importPreview");
  const importButton = $("#importButton");
  $("#importError").textContent = "";
  preview.hidden = false;
  preview.textContent = "正在分析文件…";
  importButton.disabled = true;
  try {
    const form = importFormData(false);
    const result = await request("/api/import/preview", { method: "POST", body: form });
    if (version !== importPreviewVersion) return;
    state.importMapping = result.mapping || {};
    renderCSVMapping(result);
    renderImportPreview(result);
    importButton.disabled = result.importable === 0 || (result.format === "csv" && !result.mapping?.url);
  } catch (error) {
    if (version !== importPreviewVersion) return;
    preview.hidden = true;
    $("#importError").textContent = error.message;
  }
}

function importFormData(includeArchive) {
  const form = new FormData();
  form.append("file", state.importFile);
  if (Object.keys(state.importMapping).length) form.append("mapping", JSON.stringify(state.importMapping));
  if (includeArchive) form.append("archive", String($("#importArchive").checked));
  return form;
}

function renderCSVMapping(result) {
  const container = $("#csvMapping");
  if (result.format !== "csv") {
    container.hidden = true;
    return;
  }
  container.hidden = false;
  const fields = [
    ["url", "网址（必选）"], ["title", "标题"], ["note", "备注"], ["tags", "标签"],
    ["unread", "稍后阅读"], ["starred", "收藏"], ["createdAt", "创建时间"],
  ];
  const mappingFields = $("#mappingFields");
  mappingFields.replaceChildren();
  for (const [key, label] of fields) {
    const wrapper = element("label", "mapping-field");
    wrapper.append(element("span", "", label));
    const select = document.createElement("select");
    select.dataset.mapping = key;
    select.append(new Option("不导入", ""));
    for (const header of result.headers || []) select.append(new Option(header, header));
    select.value = result.mapping?.[key] || "";
    select.addEventListener("change", () => {
      state.importMapping = Object.fromEntries(
        [...document.querySelectorAll("[data-mapping]")].map((node) => [node.dataset.mapping, node.value]),
      );
      previewImport();
    });
    wrapper.append(select);
    mappingFields.append(wrapper);
  }
}

function renderImportPreview(result) {
  const container = $("#importPreview");
  container.replaceChildren();
  const formatName = { json: "拾页 JSON", html: "浏览器书签 HTML", csv: "CSV" }[result.format] || result.format;
  container.append(element("p", "import-summary", `${formatName} · ${result.rows} 行 · 可导入 ${result.importable} 条 · 跳过 ${result.invalid} 条`));
  if (!result.preview?.length) return;
  const list = element("ul", "import-preview-list");
  for (const item of result.preview.slice(0, 5)) {
    const row = document.createElement("li");
    row.append(element("strong", "", item.title || item.url));
    row.append(element("span", "", item.url));
    list.append(row);
  }
  container.append(list);
}

async function submitImport(event) {
  event.preventDefault();
  if (!state.importFile) return;
  const button = $("#importButton");
  setButtonBusy(button, true, "导入中…");
  $("#importError").textContent = "";
  try {
    const result = await request("/api/import", { method: "POST", body: importFormData(true) });
    importDialog.close();
    showToast(`导入完成：新增 ${result.created}，重复 ${result.duplicates}，跳过 ${result.invalid}`);
    await loadBookmarks();
  } catch (error) {
    $("#importError").textContent = error.message;
  } finally {
    setButtonBusy(button, false, "开始导入");
  }
}

function showToast(message) {
  const toast = $("#toast");
  clearTimeout(toastTimer);
  toast.textContent = message;
  toast.hidden = false;
  toastTimer = setTimeout(() => { toast.hidden = true; }, 2600);
}

$("#addButton").addEventListener("click", openCreateDialog);
$("#settingsButton").addEventListener("click", openSettings);
$("#closeSettingsButton").addEventListener("click", () => settingsDialog.close());
$("#generatePairingButton").addEventListener("click", generatePairingCode);
$("#openImportButton").addEventListener("click", openImportDialog);
$("#closeImportButton").addEventListener("click", () => importDialog.close());
$("#cancelImportButton").addEventListener("click", () => importDialog.close());
$("#importForm").addEventListener("submit", submitImport);
$("#importFile").addEventListener("change", (event) => {
  state.importFile = event.target.files?.[0] || null;
  state.importMapping = {};
  if (state.importFile) previewImport();
});
$("#emptyAddButton").addEventListener("click", openCreateDialog);
$("#closeDialogButton").addEventListener("click", () => bookmarkDialog.close());
$("#cancelDialogButton").addEventListener("click", () => bookmarkDialog.close());
$("#bookmarkForm").addEventListener("submit", submitBookmark);
$("#selectPageButton").addEventListener("click", () => {
  for (const bookmark of state.bookmarks) state.selected.add(bookmark.id);
  document.querySelectorAll(".select-bookmark").forEach((checkbox) => { checkbox.checked = true; });
  renderBulkBar();
});
$("#clearSelectionButton").addEventListener("click", () => {
  state.selected.clear();
  document.querySelectorAll(".select-bookmark").forEach((checkbox) => { checkbox.checked = false; });
  renderBulkBar();
});
$("#bulkDeleteButton").addEventListener("click", bulkDelete);
document.querySelectorAll("[data-bulk-state]").forEach((button) => {
  button.addEventListener("click", () => {
    const action = button.dataset.bulkState;
    if (action === "unread") applyBulkUpdate({ unread: true }, "已标记为稍后阅读");
    if (action === "read") applyBulkUpdate({ unread: false }, "已标记为已读");
    if (action === "starred") applyBulkUpdate({ starred: true }, "已添加收藏");
    if (action === "unstarred") applyBulkUpdate({ starred: false }, "已取消收藏");
  });
});
document.querySelectorAll("[data-bulk-tags]").forEach((button) => {
  button.addEventListener("click", () => {
    const mode = button.dataset.bulkTags;
    const raw = prompt(mode === "add" ? "输入要添加的标签，用逗号分隔" : "输入要移除的标签，用逗号分隔");
    if (raw === null) return;
    const tags = raw.split(/[,，]/).map((value) => value.trim()).filter(Boolean);
    if (!tags.length) return;
    applyBulkUpdate(mode === "add" ? { addTags: tags } : { removeTags: tags }, mode === "add" ? "已添加标签" : "已移除标签");
  });
});
$("#logoutButton").addEventListener("click", async () => {
  await request("/api/auth/logout", { method: "POST" });
  const status = await request("/api/status");
  showAuth(status);
});

$("#searchInput").addEventListener("input", (event) => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => {
    state.query = event.target.value.trim();
    loadBookmarks();
  }, 180);
});

document.querySelectorAll(".nav-item").forEach((button) => {
  button.addEventListener("click", () => {
    document.querySelectorAll(".nav-item").forEach((item) => item.classList.remove("active"));
    button.classList.add("active");
    state.filter = button.dataset.state;
    $("#listTitle").textContent = state.filter === "unread" ? "稍后阅读" : state.filter === "starred" ? "收藏" : "全部书签";
    loadBookmarks();
  });
});

document.addEventListener("keydown", (event) => {
  if (event.key === "/" && !["INPUT", "TEXTAREA"].includes(document.activeElement.tagName)) {
    event.preventDefault();
    $("#searchInput").focus();
  }
  if (event.key === "Escape" && bookmarkDialog.open) bookmarkDialog.close();
  if (event.key === "Escape" && importDialog.open) importDialog.close();
});

init();
