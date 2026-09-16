const state = {
  filter: "",
  query: "",
  bookmarks: [],
  selected: new Set(),
  selecting: false,
  capture: location.pathname === "/add",
  captureInitialized: false,
  importFile: null,
  importMapping: {},
  setupToken: new URLSearchParams(location.search).get("token") || "",
};

const $ = (selector) => document.querySelector(selector);
const authView = $("#authView");
const appView = $("#appView");
const authButton = $("#authButton");
const authError = $("#authError");
const bookmarkDialog = $("#bookmarkDialog");
const settingsDialog = $("#settingsDialog");
const importDialog = $("#importDialog");
const captureView = $("#captureView");
const captureParams = new URLSearchParams(location.hash.slice(1) || location.search);
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
  if (!response.ok) {
    const error = new Error(payload.error || `请求失败（${response.status}）`);
    error.status = response.status;
    throw error;
  }
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
    const status = {
      authenticated: document.body.dataset.authenticated === "true",
      setupRequired: document.body.dataset.setupRequired === "true",
    };
    if (status.authenticated) {
      showApp();
      await loadBookmarks();
      return;
    }
    showAuth(status);
  } catch (error) {
    showToast(error.message);
  }
}

function showAuth(status) {
  appView.hidden = true;
  captureView.hidden = true;
  if (bookmarkDialog.open) bookmarkDialog.close();
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
    $("#authDescription").textContent = "登录后管理全部书签；只有你主动公开的书签可供访客查看。";
    authButton.textContent = "使用 Passkey";
    authButton.hidden = false;
    setupHelp.hidden = true;
    authButton.onclick = loginPasskey;
  }
}

function showApp() {
  authView.hidden = true;
  appView.hidden = state.capture;
  if (state.capture) showCaptureForm();
}

async function loadBookmarks() {
  if (state.capture) return;
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
  selected.hidden = !state.selecting;
  selected.className = "select-bookmark";
  selected.setAttribute("aria-label", `选择 ${bookmark.title || bookmark.url}`);
  selected.checked = state.selected.has(bookmark.id);
  selected.addEventListener("change", () => {
    if (selected.checked) state.selected.add(bookmark.id);
    else state.selected.delete(bookmark.id);
    renderBulkBar();
  });
  const star = bookmarkAction(bookmark.starred ? "取消星标" : "加星标", "star");
  star.classList.toggle("is-starred", bookmark.starred);
  star.setAttribute("aria-pressed", String(bookmark.starred));
  star.type = "button";
  star.title = bookmark.starred ? "取消星标" : "加星标";
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
  if (bookmark.public) meta.append(element("span", "badge", "公开"));
  if (bookmark.unread) meta.append(element("span", "badge", "稍后阅读"));
  for (const tag of bookmark.tags || []) meta.append(element("span", "badge", `#${tag}`));
  if (bookmark.archiveStatus === "complete") {
    const archiveLink = element("a", "archive-link", "存档");
    archiveLink.href = `/archive/${bookmark.id}`;
    meta.append(archiveLink);
  } else if (bookmark.archiveStatus === "failed") {
    meta.append(element("span", "badge failed", "归档失败"));
  }
  if (bookmark.starred) {
    const marker = element("span", "star-marker", "★");
    marker.setAttribute("aria-label", "已加星标");
    body.append(marker);
  }
  body.append(title, meta);
  if (bookmark.publicComment) body.append(element("p", "bookmark-note", `短评：${bookmark.publicComment}`));
  if (bookmark.note) body.append(element("p", "bookmark-note", bookmark.note));

  const actions = element("div", "row-actions");
  actions.setAttribute("role", "group");
  actions.setAttribute("aria-label", "书签操作");
  const unread = bookmarkAction(bookmark.unread ? "标为已读" : "稍后阅读", bookmark.unread ? "check" : "clock");
  unread.type = "button";
  unread.addEventListener("click", () => updateBookmark(bookmark.id, { unread: !bookmark.unread }));
  const edit = bookmarkAction("编辑", "edit");
  edit.type = "button";
  edit.addEventListener("click", () => openEditDialog(bookmark));
  const remove = bookmarkAction("删除", "trash");
  remove.classList.add("danger");
  remove.type = "button";
  remove.addEventListener("click", () => deleteBookmark(bookmark));
  actions.append(star, unread, edit);
  if (bookmark.archiveStatus === "failed" || bookmark.archiveStatus === "idle") {
    const retry = bookmarkAction(bookmark.archiveStatus === "idle" ? "归档正文" : "重试归档", "archive");
    retry.type = "button";
    retry.addEventListener("click", () => retryArchive(bookmark.id));
    actions.append(retry);
  }
  actions.append(remove);
  body.append(actions);
  row.append(selected, body);
  return row;
}

function bookmarkAction(label, icon) {
  const paths = {
    star: "m12 3 2.8 5.7 6.2.9-4.5 4.4 1.1 6.2-5.6-3-5.6 3 1.1-6.2L3 9.6l6.2-.9Z",
    check: "m5 12 4 4L19 6",
    clock: "M12 8v4l3 2 M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0",
    edit: "m15 5 4 4 M4 20l4-1L20 7a2.8 2.8 0 0 0-4-4L4 15Z",
    archive: "M4 4h16v4H4Z M6 8v12h12V8 M10 12h4",
    trash: "M3 6h18 M9 6V3h6v3 M5 6l1 14h12l1-14 M10 10v6 M14 10v6",
  };
  const button = element("button", "bookmark-action");
  button.type = "button";
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("focusable", "false");
  const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("d", paths[icon]);
  svg.append(path);
  button.append(svg, element("span", "", label));
  return button;
}

function renderBulkBar() {
  const bar = $("#bulkBar");
  const count = state.selected.size;
  bar.hidden = !state.selecting;
  $("#bookmarkList").classList.toggle("selecting", state.selecting);
  $("#selectionModeButton").textContent = state.selecting ? "完成" : "选择";
  $("#selectionModeButton").setAttribute("aria-pressed", String(state.selecting));
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
  $("#publicCommentField").hidden = true;
  $("#pageDescription").hidden = true;
  $("#dialogTitle").textContent = "保存链接";
  $("#urlField").hidden = false;
  $("#urlInput").disabled = false;
  $("#dialogError").textContent = "";
  $("#archiveChoice").hidden = false;
  bookmarkDialog.showModal();
  $("#urlInput").focus();
}

function openEditDialog(bookmark) {
  $("#bookmarkForm").reset();
  $("#bookmarkId").value = bookmark.id;
  $("#dialogTitle").textContent = "编辑书签";
  $("#archiveChoice").hidden = true;
  $("#urlField").hidden = true;
  $("#urlInput").disabled = true;
  $("#titleInput").value = bookmark.title;
  $("#noteInput").value = bookmark.note;
  $("#publicInput").checked = Boolean(bookmark.public);
  $("#publicCommentInput").value = bookmark.publicComment || "";
  $("#publicCommentField").hidden = !bookmark.public;
  $("#pageDescription").hidden = !bookmark.description;
  $("#pageDescription").open = false;
  $("#pageDescriptionText").textContent = bookmark.description || "";
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
    public: $("#publicInput").checked,
    publicComment: $("#publicCommentInput").value,
    tags: $("#tagsInput").value.split(/[,，]/).map((value) => value.trim()).filter(Boolean),
    unread: $("#unreadInput").checked,
    starred: $("#starredInput").checked,
  };
  if (!id) {
    payload.url = $("#urlInput").value;
    payload.description = state.capture && $("#urlInput").value === captureParams.get("url") ? (captureParams.get("description") || "") : "";
    payload.archive = $("#archiveInput").checked;
  }
  setButtonBusy($("#saveButton"), true, "保存中…");
  $("#dialogError").textContent = "";
  try {
    const result = await request(id ? `/api/bookmarks/${id}` : "/api/bookmarks", {
      method: id ? "PATCH" : "POST",
      body: JSON.stringify(payload),
    });
    bookmarkDialog.close();
    if (state.capture) {
      $("#captureSuccess").hidden = false;
      $("#captureResult").textContent = result?.duplicate
        ? "这个链接已经保存过，公开设置保持不变。修改公开设置请编辑原书签。"
        : "可以关闭此窗口，继续浏览原网页。";
      history.replaceState({}, "", "/add");
      window.close();
      return;
    }
    showToast(result?.duplicate ? "链接已存在；修改公开设置请编辑原书签" : id ? "书签已更新" : "链接已保存");
    await loadBookmarks();
  } catch (error) {
    if (state.capture && error.status === 401) {
      showAuth(await request("/api/status"));
      return;
    }
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

function showCaptureForm() {
  captureView.hidden = false;
  if (!state.captureInitialized) {
    captureView.append(bookmarkDialog);
    bookmarkDialog.classList.add("capture-form");
    $("#bookmarkForm").reset();
    $("#urlInput").value = captureParams.get("url") || "";
    $("#titleInput").value = captureParams.get("title") || "";
    $("#noteInput").value = captureParams.get("note") || "";
    $("#pageDescriptionText").textContent = captureParams.get("description") || "";
    $("#pageDescription").hidden = !captureParams.get("description");
    $("#tagsInput").value = captureParams.get("tags") || "";
    $("#unreadInput").checked = captureParams.get("unread") === "1";
    $("#starredInput").checked = captureParams.get("starred") === "1";
    state.captureInitialized = true;
  }
  bookmarkDialog.show();
  $("#urlInput").focus();
}

function cancelBookmark() {
  if (state.capture) {
    window.close();
    location.assign("/");
    return;
  }
  bookmarkDialog.close();
}

function configureBookmarklet() {
  const destination = JSON.stringify(`${location.origin}/add`);
  const code = `javascript:void(function(){var p=new URLSearchParams({url:location.href,title:document.title,description:(document.querySelector('meta[name="description" i]')?.content||document.querySelector('meta[property="og:description" i]')?.content||"").slice(0,2000),note:String(window.getSelection()||"").slice(0,2000)});window.open(${destination}+"#"+p.toString(),"_blank","popup,width=720,height=760,noopener,noreferrer");}())`;
  $("#bookmarkletLink").href = code;
  $("#bookmarkletLink").addEventListener("click", (event) => {
    event.preventDefault();
    showToast("请把“保存到拾页”拖到浏览器书签栏，再在要保存的网页上使用。");
  });
}

async function openSettings() {
  settingsDialog.showModal();
  await Promise.all([loadStorageStats(), loadPublicationStatus()]);
}

async function loadPublicationStatus() {
  const status = $("#publicationStatus");
  const button = $("#retryPublicationButton");
  button.disabled = true;
  try {
    const result = await request("/api/publication");
    button.disabled = !result.configured;
    if (!result.configured) {
      status.textContent = "尚未配置更新通知；公开 JSON 可独立使用。";
      return;
    }
    const notification = result.notification;
    let message;
    if (notification.dueAt) message = notification.lastError
      ? `通知失败：${notification.lastError}，将自动重试。`
      : "已排队，等待发送更新通知。";
    else if (notification.lastError) message = `通知失败：${notification.lastError}，请检查接收端后重试。`;
    else if (notification.notifiedAt) message = `通知已送达（${new Date(notification.notifiedAt).toLocaleString("zh-CN")}）；后续处理由接收方负责。`;
    else message = "等待公开书签更新。";
    status.textContent = `${message} 通知状态仅保留到服务重启。`;
  } catch (error) {
    status.textContent = error.message;
  }
}

async function retryPublication() {
  const button = $("#retryPublicationButton");
  setButtonBusy(button, true, "正在排队…");
  try {
    await request("/api/publication/retry", { method: "POST" });
  } catch (error) {
    showToast(error.message);
  } finally {
    button.textContent = "发送更新通知";
    await loadPublicationStatus();
  }
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
    ["url", "网址（必选）"], ["title", "标题"], ["note", "私人备注"], ["description", "网页描述"], ["publicComment", "公开短评"], ["tags", "标签"],
    ["unread", "稍后阅读"], ["starred", "星标"], ["createdAt", "创建时间"],
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

$("#publicInput").addEventListener("change", () => {
  $("#publicCommentField").hidden = !$("#publicInput").checked;
});
$("#retryPublicationButton").addEventListener("click", retryPublication);
$("#refreshPublicationButton").addEventListener("click", loadPublicationStatus);
$("#addButton").addEventListener("click", openCreateDialog);
$("#settingsButton").addEventListener("click", openSettings);
$("#closeSettingsButton").addEventListener("click", () => settingsDialog.close());
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
$("#closeDialogButton").addEventListener("click", cancelBookmark);
$("#cancelDialogButton").addEventListener("click", cancelBookmark);
$("#closeCaptureButton").addEventListener("click", cancelBookmark);
$("#bookmarkForm").addEventListener("submit", submitBookmark);
$("#selectionModeButton").addEventListener("click", () => {
  state.selecting = !state.selecting;
  state.selected.clear();
  renderBookmarks();
});
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
    if (action === "starred") applyBulkUpdate({ starred: true }, "已添加星标");
    if (action === "unstarred") applyBulkUpdate({ starred: false }, "已取消星标");
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
    document.querySelectorAll(".nav-item").forEach((item) => {
      item.classList.remove("active");
      item.setAttribute("aria-pressed", "false");
    });
    button.setAttribute("aria-pressed", "true");
    button.classList.add("active");
    state.filter = button.dataset.state;
    $("#listTitle").textContent = state.filter === "unread" ? "稍后阅读" : state.filter === "starred" ? "星标" : state.filter === "public" ? "公开书签" : "全部书签";
    loadBookmarks();
  });
});

document.addEventListener("keydown", (event) => {
  if (!state.capture && event.key === "/" && !["INPUT", "TEXTAREA"].includes(document.activeElement.tagName)) {
    event.preventDefault();
    $("#searchInput").focus();
  }
  if (!state.capture && event.key === "Escape" && bookmarkDialog.open) bookmarkDialog.close();
  if (event.key === "Escape" && importDialog.open) importDialog.close();
});

configureBookmarklet();
init();
