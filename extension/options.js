const pairForm = document.querySelector("#pairForm");
const pairStatus = document.querySelector("#pairStatus");
const connectedView = document.querySelector("#connectedView");

function normalizeServer(value) {
  const parsed = new URL(value.trim());
  if (parsed.protocol !== "https:" && !(parsed.protocol === "http:" && ["localhost", "127.0.0.1"].includes(parsed.hostname))) {
    throw new Error("正式服务必须使用 HTTPS；HTTP 只允许 localhost");
  }
  if (parsed.pathname !== "/" || parsed.search || parsed.hash || parsed.username || parsed.password) {
    throw new Error("服务地址只能包含协议、域名和端口");
  }
  return parsed.origin;
}

async function refresh() {
  const { serverUrl, token } = await chrome.storage.local.get(["serverUrl", "token"]);
  connectedView.hidden = !(serverUrl && token);
  if (serverUrl && token) {
    document.querySelector("#connectedServer").textContent = serverUrl;
    document.querySelector("#serverInput").value = serverUrl;
  }
}

pairForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = document.querySelector("#connectButton");
  button.disabled = true;
  button.textContent = "连接中…";
  pairStatus.textContent = "";
  try {
    const serverUrl = normalizeServer(document.querySelector("#serverInput").value);
    const originPattern = `${serverUrl}/*`;
    const granted = await chrome.permissions.request({ origins: [originPattern] });
    if (!granted) throw new Error("需要允许扩展访问你的 Links 服务地址");
    const response = await fetch(`${serverUrl}/api/extension/pair`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        code: document.querySelector("#codeInput").value,
        deviceName: document.querySelector("#deviceInput").value,
      }),
    });
    const result = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(result.error || `连接失败（${response.status}）`);
    await chrome.storage.local.set({ serverUrl, token: result.token });
    document.querySelector("#codeInput").value = "";
    pairStatus.className = "status success";
    pairStatus.textContent = "连接成功，可以开始保存网页了。";
    await refresh();
  } catch (error) {
    pairStatus.className = "status error";
    pairStatus.textContent = error.message || String(error);
  } finally {
    button.disabled = false;
    button.textContent = "连接服务";
  }
});

document.querySelector("#disconnectButton").addEventListener("click", async () => {
  await chrome.storage.local.remove(["token"]);
  connectedView.hidden = true;
  pairStatus.textContent = "已在当前浏览器中断开；如需彻底撤销，请同时在 Links 网页中撤销该设备。";
});

refresh();
