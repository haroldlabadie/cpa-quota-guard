"use strict";
const test = require("node:test"), assert = require("node:assert/strict"), vm = require("node:vm"), fs = require("node:fs");
const source = fs.readFileSync(__dirname + "/settings.js", "utf8");
class Element {
  constructor(tag = "div") { this.tag = tag; this.children = []; this.value = ""; this.textContent = ""; }
  append(item) { this.children.push(item); }
  replaceChildren() { this.children = []; }
  setAttribute(name, value) { this[name] = value; }
  addEventListener(type, handler) { this["on" + type] = handler; }
  focus() {}
  setAttribute(name, value) { this[name] = value; }
  set innerHTML(_) { throw new Error("unsafe HTML rendering"); }
}
function app(initial = {}) {
  const elements = {}, requests = [], saved = new Map(Object.entries(initial)); let failure = false;
  const get = id => elements[id] || (elements[id] = new Element());
  const storage = {getItem: name => saved.has(name) ? saved.get(name) : null, setItem: (name, value) => saved.set(name, String(value)), removeItem: name => saved.delete(name)};
  vm.runInNewContext(source, {document: {getElementById: get, createElement: tag => new Element(tag)}, localStorage: storage, sessionStorage: storage, navigator: {userAgent: "test-agent"}, window: {location: {host: "cpamp.example.invalid"}}, TextEncoder, TextDecoder, Uint8Array, atob, fetch: async (url, options) => {
    requests.push({url, options});
    if (options.method === "PUT") return {ok: !failure, status: failure ? 412 : 200, json: async () => failure ? {error: "conflict"} : {settings: {schema: 1, rules: [{email: "test@example.invalid", provider: "codex", enabled: true, reserve_five_hour: 12, reserve_weekly: 3}]}, revision: "r2"}};
    let body = {};
    if (url.endsWith("/settings")) body = {settings: {schema: 1, rules: [{email: "test@example.invalid", provider: "codex", enabled: true, reserve_five_hour: 8, reserve_weekly: 3}]}, revision: "r1"};
    if (url.endsWith("/status")) body = {version: "0.2.0", mode: "observe", running: true};
    if (url.endsWith("/observations")) body = {observations: {a: {name: "<img onerror=alert(1)>", provider: "codex", blocked: true, reasons: ["<script>bad</script>"], transaction_id: "tx-1", transaction_phase: "conflict", orphan_reason: "owner marker without matching prior proof"}}};
    return {ok: true, headers: {get: () => '"r1"'}, json: async () => body};
  }, Date, JSON, Number, Object, console, setTimeout, clearTimeout});
  return {get, requests, saved, fail: () => { failure = true; }};
}
test("manual Key stays in memory; save includes If-Match and edited fields", async () => {
  const a = app(); a.get("manualKey").value = "FAKE_ADMIN"; a.get("manualConnect").onclick();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(a.get("manualKey").value, "");
  const row = a.get("rules").children[0]; const reserve = row.children[3].children[0]; reserve.value = "12"; reserve.oninput();
  await a.get("save").onclick(); const put = a.requests.find(r => r.options.method === "PUT");
  assert.equal(put.options.headers["If-Match"], '"r1"'); assert.equal(JSON.parse(put.options.body).rules[0].reserve_five_hour, 12);
  assert.equal(put.options.headers.Authorization, "Bearer FAKE_ADMIN");
});
test("conflict preserves the edited rule and does not reload it", async () => {
  const a = app(); a.get("manualKey").value = "FAKE_ADMIN"; a.get("manualConnect").onclick(); await new Promise(r => setImmediate(r)); const email = a.get("rules").children[0].children[0].children[0]; email.value = "draft@example.invalid"; email.oninput();
  a.fail(); const count = a.requests.length; await a.get("save").onclick(); assert.equal(a.requests.length, count + 1); assert.equal(email.value, "draft@example.invalid"); assert.equal(a.get("message").textContent, "conflict");
});
test("untrusted credential names and reasons render as text", async () => {
  const a = app(); a.get("manualKey").value = "FAKE_ADMIN"; a.get("manualConnect").onclick(); await new Promise(r => setImmediate(r)); const row = a.get("observations").children[0]; assert.equal(row.children[0].children[0].textContent, "<img onerror=alert(1)>"); assert.match(row.children[3].textContent, /<script>/);
});

test("transaction, orphan and conflict status are distinguishable", async () => {
  const a = app(); a.get("manualKey").value = "FAKE_ADMIN"; a.get("manualConnect").onclick(); await new Promise(r => setImmediate(r)); const row = a.get("observations").children[0];
  assert.equal(row.children[1].children[0].textContent, "所有权孤儿");
  assert.match(row.children[3].textContent, /事务 tx-1 conflict/);
  assert.match(row.children[3].textContent, /owner marker/);
});

test("CPAMP session auto-connects and remembered manual keys persist", async () => {
  const b = app({"isLoggedIn": "true", "cli-proxy-auth": JSON.stringify({state: {managementKey: "FAKE_SESSION"}})});
  await new Promise(r => setImmediate(r));
  assert.equal(b.get("authGate").hidden, true);
  assert.equal(b.requests[0].options.headers.Authorization, "Bearer FAKE_SESSION");
  const a = app(); a.get("manualKey").value = "FAKE_ADMIN"; a.get("rememberKey").checked = true; a.get("manualConnect").onclick();
  await new Promise(r => setImmediate(r));
  assert.equal(a.saved.get("cpa_quota_guard_mgmt_key"), "FAKE_ADMIN");
  const c = app({cpa_quota_guard_mgmt_key: "FAKE_ADMIN"});
  await new Promise(r => setImmediate(r));
  assert.equal(c.get("authGate").hidden, true);
  c.get("logout").onclick();
  assert.equal(c.saved.has("cpa_quota_guard_mgmt_key"), false);
});
test("main settings page never exposes a management token control", () => {
  const html = fs.readFileSync(__dirname + "/settings.html", "utf8");
  assert.doesNotMatch(html.slice(html.indexOf('id="app"')), /type="password"|Management Key|管理令牌|连接管理/);
  assert.match(html, /class="runtime-rail"/);
  assert.doesNotMatch(html, /class="status-card"/);
});
test("design reflects the blue admin system", () => {
  const html = fs.readFileSync(__dirname + "/settings.html", "utf8");
  assert.match(html, /--accent:#265fae/);
  assert.match(html, /data-label|data-label=/);
  assert.match(html, /@media\(max-width:520px\)/);
  assert.match(html, /prefers-reduced-motion:reduce/);
});
