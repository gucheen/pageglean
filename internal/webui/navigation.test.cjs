const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { test } = require("node:test");
const vm = require("node:vm");

const source = readFileSync(`${__dirname}/assets/app.js`, "utf8");

function createApp(path = "/?q=old") {
  const location = new URL(path, "https://example.test");
  function node(dataset = {}) {
    return {
      dataset, value: "", attributes: {}, listeners: {},
      classList: { toggle() {}, add() {}, remove() {} },
      addEventListener(type, handler) { this.listeners[type] = handler; },
      setAttribute(key, value) { this.attributes[key] = value; },
      removeAttribute(key) { delete this.attributes[key]; },
      replaceChildren() {}, append() {},
    };
  }
  const nodes = new Map();
  const get = selector => {
    if (!nodes.has(selector)) nodes.set(selector, node());
    return nodes.get(selector);
  };
  const links = ["home", "library", "all"].map(scope => {
    const link = new URL(scope === "home" ? "/" : `/${scope}`, location);
    Object.assign(link, node({ scope }));
    return link;
  });
  const filters = ["", "unread", "starred", "public"].map(state => node({ state }));
  const entries = [location.href];
  let index = 0;
  const timers = new Map();
  const requests = [];
  const window = node();
  const context = vm.createContext({
    location, window, URL, URLSearchParams, Headers, FormData,
    document: {
      body: { dataset: { authenticated: "true" } },
      querySelector: get,
      querySelectorAll: selector => selector === "[data-scope]" ? links : selector === "[data-state]" ? filters : [],
      addEventListener() {},
    },
    history: {
      state: null,
      replaceState(_, __, url) { location.href = new URL(url, location).href; entries[index] = location.href; },
      pushState(_, __, url) { location.href = new URL(url, location).href; entries.splice(++index); entries.push(location.href); },
    },
    setTimeout(callback) { const id = Symbol(); timers.set(id, callback); return id; },
    clearTimeout(id) { timers.delete(id); },
    fetch(url) {
      return new Promise(resolve => requests.push({
        url: new URL(url, location),
        finish: bookmarks => resolve({ ok: true, status: 200, json: async () => ({ bookmarks }) }),
      }));
    },
  });
  vm.runInContext(source, context);
  return {
    location, links, filters, requests, timers, entries, get,
    state: () => vm.runInContext("state", context),
    input(value) { get("#searchInput").value = value; get("#searchInput").listeners.input({ target: get("#searchInput") }); },
    click(scope, extra = {}) {
      let prevented = false;
      links.find(link => link.dataset.scope === scope).listeners.click({ button: 0, preventDefault() { prevented = true; }, ...extra });
      return prevented;
    },
    move(delta) { index += delta; location.href = entries[index]; window.listeners.popstate(); },
  };
}

test("clearing a URL search removes it immediately from navigation and reloads", () => {
  const app = createApp("/library?q=old&extra=keep#section");
  app.input("");
  assert.equal(app.location.search, "?extra=keep");
  assert.equal(app.location.hash, "#section");
  assert.ok(app.links.every(link => !link.search));
  assert.equal(app.requests.at(-1).url.searchParams.has("q"), false);
  assert.equal(app.click("all"), true);
  assert.equal(app.location.pathname, "/all");
  assert.equal(app.requests.at(-1).url.searchParams.get("scope"), "all");
  assert.equal(createApp(app.location.href).get("#searchInput").value, "");
});

test("switching during debounce uses current search and resets workspace modes", () => {
  const app = createApp();
  Object.assign(app.state(), { filter: "starred", selecting: true, managingHome: true });
  app.state().selected.add(42);
  app.input(" 中文 & tag ");
  assert.equal(app.location.searchParams.get("q"), "中文 & tag");
  assert.equal(app.requests.length, 1);
  app.click("library");
  assert.equal(app.timers.size, 0);
  assert.equal(app.requests.at(-1).url.searchParams.get("q"), "中文 & tag");
  assert.equal(app.state().filter, "");
  assert.equal(app.state().selecting, false);
  assert.equal(app.state().managingHome, false);
  assert.equal(app.state().selected.size, 0);
  assert.equal(app.links[1].attributes["aria-current"], "page");
  assert.equal(app.filters[0].attributes["aria-pressed"], "true");
});

test("back and forward restore scope and search without retaining pending input", () => {
  const app = createApp();
  app.click("library");
  app.input("new");
  app.move(-1);
  assert.equal(app.state().scope, "home");
  assert.equal(app.get("#searchInput").value, "old");
  assert.equal(app.timers.size, 0);
  app.move(1);
  assert.equal(app.state().scope, "library");
  assert.equal(app.get("#searchInput").value, "new");
  assert.equal(app.requests.at(-1).url.searchParams.get("q"), "new");
});

test("modified clicks keep native navigation and current tab adds no history entry", () => {
  const app = createApp();
  for (const extra of [{ ctrlKey: true }, { metaKey: true }, { shiftKey: true }, { altKey: true }, { button: 1 }]) {
    assert.equal(app.click("library", extra), false);
  }
  app.click("home");
  assert.equal(app.entries.length, 1);
  assert.equal(app.requests.length, 1);
});

test("results from an earlier query are ignored during debounce", async () => {
  const app = createApp();
  app.input("new");
  app.requests[0].finish([{ id: 42 }]);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(app.state().bookmarks.length, 0);
});
