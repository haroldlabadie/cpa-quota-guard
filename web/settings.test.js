"use strict";
const test = require("node:test"), assert = require("node:assert/strict"), vm = require("node:vm"), fs = require("node:fs");
const source = fs.readFileSync(__dirname + "/settings.js", "utf8");
class Element {
  constructor(tag = "div") { this.tag = tag; this.children = []; this.value = ""; this.textContent = ""; }
  append(item) { this.children.push(item); }
  replaceChildren() { this.children = []; }
  setAttribute(name, value) { this[name] = value; }
  set innerHTML(_) { throw new Error("unsafe HTML rendering"); }
}
function app() {
  const elements = {}, requests = []; let failure = false;
  const get = id => elements[id] || (elements[id] = new Element());
  vm.runInNewContext(source, {document: {getElementById: get, createElement: tag => new Element(tag)}, localStorage: {getItem: () => null}, fetch: async (url, options) => {
    requests.push({url, options});
    if (options.method === "PUT") return {ok: !failure, status: failure ? 412 : 200, json: async () => failure ? {error: "conflict"} : {}};
    let body = {};
    if (url.endsWith("/settings")) body = {settings: {schema: 1, rules: [{email: "test@example.invalid", provider: "codex", enabled: true, reserve_five_hour: 8, reserve_weekly: 3}]}, revision: "r1"};
    if (url.endsWith("/status")) body = {version: "0.2.0", mode: "observe", running: true};
    if (url.endsWith("/observations")) body = {observations: {a: {name: "<img onerror=alert(1)>", provider: "codex", blocked: true, reasons: ["<script>bad</script>"], transaction_id: "tx-1", transaction_phase: "conflict", orphan_reason: "owner marker without matching prior proof"}}};
    return {ok: true, headers: {get: () => '"r1"'}, json: async () => body};
  }, Date, JSON, Number, Object, console});
  return {get, requests, fail: () => { failure = true; }};
}
test("manual Key stays in memory; save includes If-Match and edited fields", async () => {
  const a = app(); a.get("key").value = "FAKE_ADMIN"; await a.get("connect").onclick();
  assert.equal(a.get("key").value, "");
  const row = a.get("rules").children[0]; const reserve = row.children[3].children[0]; reserve.value = "12"; reserve.oninput();
  await a.get("save").onclick(); const put = a.requests.find(r => r.options.method === "PUT");
  assert.equal(put.options.headers["If-Match"], '"r1"'); assert.equal(JSON.parse(put.options.body).rules[0].reserve_five_hour, 12);
  assert.equal(put.options.headers.Authorization, "Bearer FAKE_ADMIN");
});
test("conflict preserves the edited rule and does not reload it", async () => {
  const a = app(); await a.get("connect").onclick(); const email = a.get("rules").children[0].children[0].children[0]; email.value = "draft@example.invalid"; email.oninput();
  a.fail(); const count = a.requests.length; await a.get("save").onclick(); assert.equal(a.requests.length, count + 1); assert.equal(email.value, "draft@example.invalid"); assert.equal(a.get("message").textContent, "conflict");
});
test("untrusted credential names and reasons render as text", async () => {
  const a = app(); await a.get("connect").onclick(); const row = a.get("observations").children[0]; assert.match(row.children[0].textContent, /^<img/); assert.match(row.children[3].textContent, /<script>/);
});

test("transaction, orphan and conflict status are distinguishable", async () => {
  const a = app(); await a.get("connect").onclick(); const row = a.get("observations").children[0];
  assert.equal(row.children[1].textContent, "所有权孤儿，待人工决定");
  assert.match(row.children[3].textContent, /事务 tx-1 conflict/);
  assert.match(row.children[3].textContent, /owner marker/);
});
