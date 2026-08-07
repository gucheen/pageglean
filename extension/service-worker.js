const MENU_PAGE = "pageglean-save-page";
const MENU_LINK = "pageglean-save-link";

chrome.runtime.onInstalled.addListener(() => {
  chrome.contextMenus.removeAll(() => {
    chrome.contextMenus.create({ id: MENU_PAGE, title: "保存页面到拾页", contexts: ["page", "selection"] });
    chrome.contextMenus.create({ id: MENU_LINK, title: "保存链接到拾页", contexts: ["link"] });
  });
});

chrome.contextMenus.onClicked.addListener(async (info, tab) => {
  if (!tab?.id) return;
  if (info.menuItemId === MENU_LINK && info.linkUrl) {
    await capturePayload({ url: info.linkUrl, title: info.linkUrl, selection: "" }, tab.id);
    return;
  }
  await captureTab(tab.id);
});

chrome.commands.onCommand.addListener(async (command) => {
  if (command !== "save-current-page") return;
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  if (tab?.id) await captureTab(tab.id);
});

chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  if (message?.type === "capture-current") {
    captureCurrentFromMessage(message).then(sendResponse);
    return true;
  }
  if (message?.type === "connection-status") {
    getConnection().then((connection) => sendResponse({ connected: Boolean(connection) }));
    return true;
  }
  return false;
});

async function captureCurrentFromMessage(message) {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  if (!tab?.id) return { ok: false, error: "找不到当前标签页" };
  return captureTab(tab.id, {
    unread: Boolean(message.unread),
    starred: Boolean(message.starred),
    archiveCurrent: Boolean(message.archiveCurrent),
  });
}

async function captureTab(tabId, overrides = {}) {
  try {
    const [{ result }] = await chrome.scripting.executeScript({
      target: { tabId },
      func: collectPage,
      args: [Boolean(overrides.archiveCurrent)],
    });
    const { archiveCurrent: _archiveCurrent, ...captureOverrides } = overrides;
    return await capturePayload({ ...result, ...captureOverrides }, tabId);
  } catch (error) {
    return finishCapture(tabId, { ok: false, error: readableError(error) });
  }
}

function collectPage(includeText) {
  const canonical = document.querySelector('link[rel~="canonical"]')?.href || "";
  const selection = window.getSelection()?.toString().trim().slice(0, 10000) || "";
  return {
    url: location.href,
    canonicalUrl: canonical,
    title: document.title,
    selection,
    contentText: includeText
      ? (document.querySelector("article, main, [role='main']") || document.body)?.innerText?.trim().slice(0, 2 * 1024 * 1024) || ""
      : "",
  };
}

async function capturePayload(payload, tabId) {
  const connection = await getConnection();
  if (!connection) {
    await chrome.runtime.openOptionsPage();
    return finishCapture(tabId, { ok: false, error: "请先连接拾页服务" });
  }
  try {
    const response = await fetch(`${connection.serverUrl}/api/capture`, {
      method: "POST",
      headers: {
        "Authorization": `Bearer ${connection.token}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify(payload),
    });
    const result = await response.json().catch(() => ({}));
    if (!response.ok) {
      if (response.status === 401) await chrome.storage.local.remove(["token"]);
      throw new Error(result.error || `保存失败（${response.status}）`);
    }
    return finishCapture(tabId, { ok: true, duplicate: Boolean(result.duplicate), bookmark: result.bookmark });
  } catch (error) {
    return finishCapture(tabId, { ok: false, error: readableError(error) });
  }
}

async function finishCapture(tabId, result) {
  if (tabId) {
    await chrome.action.setBadgeBackgroundColor({ tabId, color: result.ok ? "#3b7b59" : "#ad2d23" });
    await chrome.action.setBadgeText({ tabId, text: result.ok ? "✓" : "!" });
    setTimeout(() => chrome.action.setBadgeText({ tabId, text: "" }).catch(() => {}), 2200);
  }
  return result;
}

async function getConnection() {
  const { serverUrl, token } = await chrome.storage.local.get(["serverUrl", "token"]);
  if (!serverUrl || !token) return null;
  return { serverUrl, token };
}

function readableError(error) {
  const message = error?.message || String(error);
  if (message.includes("Cannot access contents")) return "这个浏览器页面不能由扩展读取";
  if (message.includes("Failed to fetch")) return "无法连接拾页服务";
  return message;
}
