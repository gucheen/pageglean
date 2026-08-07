const saveButton = document.querySelector("#saveButton");
const status = document.querySelector("#status");

chrome.runtime.sendMessage({ type: "connection-status" }, (result) => {
  if (!result?.connected) {
    status.textContent = "尚未连接拾页服务";
    saveButton.textContent = "前往连接";
  }
});

saveButton.addEventListener("click", async () => {
  saveButton.disabled = true;
  saveButton.textContent = "保存中…";
  status.textContent = "";
  const result = await chrome.runtime.sendMessage({
    type: "capture-current",
    unread: document.querySelector("#unreadInput").checked,
    starred: document.querySelector("#starredInput").checked,
    archiveCurrent: document.querySelector("#archiveInput").checked,
  });
  if (result?.ok) {
    status.className = "status success";
    status.textContent = result.duplicate ? "已经保存过这个链接" : "已保存，正在后台归档";
    saveButton.textContent = "已保存 ✓";
    setTimeout(() => window.close(), 900);
  } else {
    status.className = "status error";
    status.textContent = result?.error || "保存失败";
    saveButton.textContent = "重试";
    saveButton.disabled = false;
  }
});

document.querySelector("#optionsButton").addEventListener("click", () => chrome.runtime.openOptionsPage());
