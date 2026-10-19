(() => {
  "use strict";
  const base = "/v0/management/plugins/cpa-quota-guard";
  let key = "", etag = "", draft = [];
  const el = id => document.getElementById(id);
  const show = text => { el("message").textContent = text; };
  const cell = (row, text) => { const c = document.createElement("td"); c.textContent = text; row.append(c); return c; };
  function rules() {
    el("rules").replaceChildren();
    draft.forEach((rule, index) => {
      const row = document.createElement("tr");
      const input = (field, type) => { const item = document.createElement("input"); item.type = type; item.value = rule[field]; item.setAttribute("aria-label", field); if (type === "checkbox") item.checked = rule[field]; if (type === "number") { item.min = "0"; item.max = "99.99"; item.step = "0.1"; } item.oninput = () => { rule[field] = type === "checkbox" ? item.checked : type === "number" ? Number(item.value) : item.value.trim().toLowerCase(); }; cell(row, "").append(item); };
      input("email", "email"); const provider = document.createElement("select"); ["codex", "antigravity"].forEach(name => { const option = document.createElement("option"); option.value = name; option.textContent = name === "codex" ? "Codex" : "Antigravity"; provider.append(option); }); provider.value = rule.provider; provider.onchange = () => { rule.provider = provider.value; }; provider.setAttribute("aria-label", "供应商"); cell(row, "").append(provider);
      input("enabled", "checkbox"); input("reserve_five_hour", "number"); input("reserve_weekly", "number");
      const remove = document.createElement("button"); remove.textContent = "删除"; remove.onclick = () => { draft.splice(index, 1); rules(); }; cell(row, "").append(remove); el("rules").append(row);
    });
  }
  function observations(rows) {
    el("observations").replaceChildren();
    Object.values(rows).forEach(item => { const row = document.createElement("tr"); cell(row, item.name + " · " + item.provider); cell(row, item.manual ? "人工限制，保留" : item.blocked ? "额度保护" : "额度正常"); const windows = item.sample ? item.sample.windows : []; cell(row, windows.map(w => `${w.scope} ${w.kind === "five_hour" ? "五小时" : "每周"} ${w.remaining.toFixed(1)}%`).join("；") || "暂无可信采样"); cell(row, [item.error, ...(item.reasons || []), item.action, item.snapshot_error].filter(Boolean).join("；")); el("observations").append(row); });
  }
  async function request(path, options = {}) {
    const response = await fetch(base + path, {...options, credentials: "same-origin", headers: {"Authorization": "Bearer " + key, "Content-Type": "application/json", ...(options.headers || {})}});
    const body = await response.json(); if (!response.ok) throw new Error(body.error || "管理请求失败 " + response.status); return {body, response};
  }
  async function load() {
    try { const [status, settings, data] = await Promise.all([request("/status"), request("/settings"), request("/observations")]); el("version").textContent = "v" + status.body.version; el("status").textContent = `${status.body.mode === "apply" ? "自动保护" : "只读观测"} · ${status.body.running ? "运行中" : "已停止"}\n最近检查：${status.body.last_tick_ms ? new Date(status.body.last_tick_ms).toLocaleString() : "尚未检查"}\n${status.body.last_error || ""}`; draft = settings.body.settings.rules; rules(); observations(data.body.observations); etag = settings.response.headers.get("ETag") || '"' + settings.body.revision + '"'; el("session").hidden = true; show("已加载"); }
    catch (error) { show(error.message); el("session").hidden = false; }
  }
  function readSession() {
    try { let raw = localStorage.getItem("cli-proxy-auth"); if (!raw) return ""; if (raw.startsWith("enc::v1::")) { const bytes = Uint8Array.from(atob(raw.slice(9)), c => c.charCodeAt(0)); const salt = new TextEncoder().encode(`cli-proxy-api-webui::secure-storage|${window.location.host}|${navigator.userAgent}`); raw = new TextDecoder().decode(bytes.map((b, i) => b ^ salt[i % salt.length])); } const auth = JSON.parse(raw), state = auth.state || auth; return typeof state.managementKey === "string" ? state.managementKey : ""; } catch (_) { return ""; }
  }
  el("connect").onclick = () => { key = el("key").value.trim(); el("key").value = ""; return load(); };
  el("refresh").onclick = load;
  el("add").onclick = () => { draft.push({email: "", provider: "codex", enabled: true, reserve_five_hour: 8, reserve_weekly: 3}); rules(); };
  el("save").onclick = async () => { try { await request("/settings", {method: "PUT", headers: {"If-Match": etag}, body: JSON.stringify({schema: 1, rules: draft})}); await load(); show("已保存"); } catch (error) { show(error.message); } };
  key = readSession(); if (key) load();
})();
