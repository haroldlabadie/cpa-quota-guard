(() => {
  "use strict";
  const base = "/v0/management/plugins/cpa-quota-guard";
  let key = "", etag = "", draft = [];
  const el = id => document.getElementById(id);
  const show = (text, tone = "") => { const message = el("message"); message.textContent = text; message.className = "message" + (tone ? " " + tone : ""); };
  const cell = (row, text) => { const c = document.createElement("td"); c.textContent = text; const table = row.parentElement && row.parentElement.closest ? row.parentElement.closest("table") : null; const labels = table ? Array.from(table.querySelectorAll("thead th"), th => th.textContent.trim()) : []; if (labels[row.children.length]) c.setAttribute("data-label", labels[row.children.length]); row.append(c); return c; };
  const quota = (row, windows) => {
    const value = cell(row, "");
    if (!windows.length) { value.textContent = "暂无可信采样"; return; }
    const list = document.createElement("div"); list.className = "quota-list";
    windows.forEach(window => {
      const item = document.createElement("span"); item.className = "quota" + (window.remaining < 8 ? " low" : "");
      const scope = window.scope === "third_party" ? "Claude / GPT" : window.scope === "gemini" ? "Gemini" : "全部";
      const kind = window.kind === "five_hour" ? "五小时" : "每周";
      item.append(scope + " " + kind); const amount = document.createElement("strong"); amount.textContent = window.remaining.toFixed(1) + "%"; item.append(amount); list.append(item);
    });
    value.append(list);
  };
  function rules() {
    el("rules").replaceChildren();
    if (!draft.length) { const row = document.createElement("tr"); const value = cell(row, "还没有保护规则。添加账号后，插件才会对它进行采样和保护。"); value.className = "empty"; value.colSpan = 6; el("rules").append(row); return; }
    draft.forEach((rule, index) => {
      const row = document.createElement("tr");
      const input = (field, type) => { const item = document.createElement("input"); item.type = type; item.value = rule[field]; item.className = "cell-input"; item.setAttribute("aria-label", field); if (type === "checkbox") { item.checked = rule[field]; const wrap = document.createElement("label"); wrap.className = "switch"; const text = document.createElement("span"); text.textContent = rule[field] ? "已开启" : "已关闭"; item.onchange = () => { rule[field] = item.checked; text.textContent = item.checked ? "已开启" : "已关闭"; }; const track = document.createElement("span"); track.className = "switch-track"; wrap.append(item, track, text); cell(row, "").append(wrap); return; } if (type === "number") { item.min = "0"; item.max = "99.99"; item.step = "0.1"; } item.oninput = () => { rule[field] = type === "number" ? Number(item.value) : item.value.trim().toLowerCase(); }; cell(row, "").append(item); };
      input("email", "email"); const provider = document.createElement("select"); provider.className = "cell-select"; ["codex", "antigravity"].forEach(name => { const option = document.createElement("option"); option.value = name; option.textContent = name === "codex" ? "Codex" : "Antigravity"; provider.append(option); }); provider.value = rule.provider; provider.onchange = () => { rule.provider = provider.value; }; provider.setAttribute("aria-label", "供应商"); cell(row, "").append(provider);
      input("enabled", "checkbox"); input("reserve_five_hour", "number"); input("reserve_weekly", "number");
      const remove = document.createElement("button"); remove.textContent = "删除"; remove.className = "button danger quiet"; remove.setAttribute("aria-label", "删除规则"); remove.onclick = () => { draft.splice(index, 1); rules(); }; const actions = cell(row, ""); actions.className = "actions"; actions.append(remove); el("rules").append(row);
    });
  }
  function observations(rows) {
    el("observations").replaceChildren();
    const items = Object.values(rows);
    if (!items.length) { const row = document.createElement("tr"); const value = cell(row, "暂无账号采样。连接后等待插件完成第一次检查。"); value.className = "empty"; value.colSpan = 4; el("observations").append(row); return; }
    items.forEach(item => { const row = document.createElement("tr"); const account = cell(row, ""); const title = document.createElement("strong"); title.textContent = item.name; account.append(title, document.createElement("br")); const provider = document.createElement("span"); provider.className = "reason"; provider.textContent = item.provider === "codex" ? "Codex" : "Antigravity"; account.append(provider); const status = cell(row, ""); const badge = document.createElement("span"); badge.className = "badge " + (item.orphan_reason ? "danger" : item.action === "transaction-pending" ? "warning" : item.manual ? "warning" : item.blocked ? "warning" : "success"); badge.textContent = item.orphan_reason ? "所有权孤儿" : item.action === "transaction-pending" ? "事务待核对" : item.manual ? "人工限制" : item.blocked ? "额度保护" : "额度正常"; status.append(badge); quota(row, item.sample ? item.sample.windows : []); const reason = cell(row, [item.error, ...(item.reasons || []), item.action, item.transaction_id ? "事务 " + item.transaction_id + " " + (item.transaction_phase || "") : "", item.orphan_reason, item.snapshot_error].filter(Boolean).join("；")); reason.className = "reason"; el("observations").append(row); });
  }
  async function request(path, options = {}) {
    const response = await fetch(base + path, {...options, credentials: "same-origin", headers: {"Authorization": "Bearer " + key, "Content-Type": "application/json", ...(options.headers || {})}});
    const body = await response.json(); if (!response.ok) throw new Error(body.error || "管理请求失败 " + response.status); return {body, response};
  }
  function setStatus(status) {
    el("version").textContent = "v" + status.version;
    const healthy = status.running && !status.last_error;
    el("statusDot").className = "dot" + (healthy ? "" : " danger");
    el("statusLabel").textContent = !status.running ? "已停止" : status.mode === "apply" ? "自动保护运行中" : "只读观测运行中";
    el("lastCheck").textContent = status.last_tick_ms ? new Date(status.last_tick_ms).toLocaleString() : "尚未检查";
    el("accountCount").textContent = status.account_count == null ? "—" : String(status.account_count);
    el("modeLabel").textContent = status.mode === "apply" ? "Apply · 自动保护" : "Observe · 只读观测";
    el("lastError").textContent = status.last_error || "";
  }
  async function load() {
    show("正在读取运行状态…");
    try { const [status, settings, data] = await Promise.all([request("/status"), request("/settings"), request("/observations")]); setStatus(status.body); draft = settings.body.settings.rules; rules(); observations(data.body.observations); etag = settings.response.headers.get("ETag") || '"' + settings.body.revision + '"'; el("session").hidden = true; show("已加载最新状态", "success"); }
    catch (error) { show(error.message, "error"); el("session").hidden = false; }
  }
  function readSession() {
    try { let raw = localStorage.getItem("cli-proxy-auth"); if (!raw) return ""; if (raw.startsWith("enc::v1::")) { const bytes = Uint8Array.from(atob(raw.slice(9)), c => c.charCodeAt(0)); const salt = new TextEncoder().encode(`cli-proxy-api-webui::secure-storage|${window.location.host}|${navigator.userAgent}`); raw = new TextDecoder().decode(bytes.map((b, i) => b ^ salt[i % salt.length])); } const auth = JSON.parse(raw), state = auth.state || auth; return typeof state.managementKey === "string" ? state.managementKey : ""; } catch (_) { return ""; }
  }
  el("connect").onclick = () => { key = el("key").value.trim(); el("key").value = ""; return load(); };
  el("refresh").onclick = load;
  el("add").onclick = () => { draft.push({email: "", provider: "codex", enabled: true, reserve_five_hour: 8, reserve_weekly: 3}); rules(); };
  el("save").onclick = async () => { try { await request("/settings", {method: "PUT", headers: {"If-Match": etag}, body: JSON.stringify({schema: 1, rules: draft})}); await load(); show("已保存，下一次检查将使用新规则", "success"); } catch (error) { show(error.message, "error"); } };
  key = readSession(); if (key) load();
})();
