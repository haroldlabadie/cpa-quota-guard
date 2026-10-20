(() => {
  "use strict";
  const base = "/v0/management/plugins/cpa-quota-guard";
  const AUTH_KEY = "cpa_quota_guard_mgmt_key", CPAMC_AUTH_KEY = "cli-proxy-auth";
  let key = "", etag = "", draft = [], accounts = [], openPicker = null;
  const el = id => document.getElementById(id);
  const show = (text, tone = "") => { const message = el("message"); message.textContent = text; message.className = "message" + (tone ? " " + tone : ""); };
  const providerName = value => value === "codex" ? "Codex" : "Antigravity";
  const accountLabel = item => item.email || item.name || "未命名账号";
  const accountMeta = item => item.provider === "codex" ? "Codex" : "Antigravity";
  function closePicker() { if (openPicker) { openPicker.menu.hidden = true; openPicker.button.setAttribute("aria-expanded", "false"); openPicker = null; } }
  document.addEventListener("click", event => { if (openPicker && !openPicker.root.contains(event.target)) closePicker(); });
  document.addEventListener("keydown", event => { if (event.key === "Escape") closePicker(); });
  function accountPicker(rule) {
    const root = document.createElement("div"); root.className = "select-shell";
    const button = document.createElement("button"); button.type = "button"; button.className = "select-button"; button.setAttribute("aria-haspopup", "listbox"); button.setAttribute("aria-expanded", "false");
    const value = document.createElement("span"); value.className = "select-value"; const title = document.createElement("strong"); const subtitle = document.createElement("small"); value.append(title, subtitle);
    const chevron = document.createElementNS("http://www.w3.org/2000/svg", "svg"); chevron.setAttribute("class", "select-chevron"); chevron.setAttribute("viewBox", "0 0 16 16"); chevron.setAttribute("fill", "none"); chevron.setAttribute("stroke", "currentColor"); chevron.setAttribute("stroke-width", "1.5"); const path = document.createElementNS("http://www.w3.org/2000/svg", "path"); path.setAttribute("d", "m4 6 4 4 4-4"); chevron.append(path); button.append(value, chevron);
    const menu = document.createElement("div"); menu.className = "select-menu"; menu.setAttribute("role", "listbox"); menu.hidden = true;
    const renderValue = () => { const selected = accounts.find(item => item.email === rule.email && item.provider === rule.provider); title.textContent = selected ? accountLabel(selected) : rule.email || "选择账号"; subtitle.textContent = selected ? accountMeta(selected) : rule.email ? providerName(rule.provider) : "从凭证列表中选择"; };
    const candidates = () => accounts.filter(item => item.provider === rule.provider && (item.email === rule.email || !draft.some(other => other !== rule && other.email === item.email && other.provider === item.provider)));
    const renderMenu = () => { menu.replaceChildren(); const rows = candidates(); if (!rows.length) { const empty = document.createElement("p"); empty.className = "empty"; empty.textContent = "当前渠道没有可用账号"; menu.append(empty); return; } rows.forEach(item => { const option = document.createElement("button"); option.type = "button"; option.className = "select-option"; option.setAttribute("role", "option"); option.setAttribute("aria-selected", String(item.email === rule.email && item.provider === rule.provider)); const copy = document.createElement("span"); const name = document.createElement("strong"); name.textContent = accountLabel(item); const meta = document.createElement("small"); meta.textContent = item.name; copy.append(name, meta); const mark = document.createElement("em"); mark.textContent = item.disabled ? "已停用" : "可用"; option.append(copy, mark); option.onclick = () => { rule.email = item.email; rule.provider = item.provider; renderValue(); closePicker(); }; menu.append(option); }); };
    button.onclick = event => { event.stopPropagation(); if (openPicker && openPicker.menu === menu) { closePicker(); return; } closePicker(); renderMenu(); menu.hidden = false; button.setAttribute("aria-expanded", "true"); openPicker = { root, menu, button }; };
    renderValue(); root.append(button, menu); return { root, renderValue };
  }
  function rules() {
    el("rules").replaceChildren();
    if (!draft.length) { const empty = document.createElement("p"); empty.className = "empty"; empty.textContent = "还没有保护规则。添加账号后，插件才会对它进行采样和保护。"; el("rules").append(empty); return; }
    draft.forEach((rule, index) => {
      const row = document.createElement("div"); row.className = "rule";
      const account = document.createElement("div"); account.className = "rule-field account"; const accountLabelElement = document.createElement("span"); accountLabelElement.className = "rule-field-label"; accountLabelElement.textContent = "账号邮箱"; const picker = accountPicker(rule); account.append(accountLabelElement, picker.root);
      const providerField = document.createElement("div"); providerField.className = "rule-field"; const providerLabel = document.createElement("span"); providerLabel.className = "rule-field-label"; providerLabel.textContent = "供应商"; const provider = document.createElement("select"); provider.className = "cell-select"; ["codex", "antigravity"].forEach(name => { const option = document.createElement("option"); option.value = name; option.textContent = providerName(name); provider.append(option); }); provider.value = rule.provider; provider.onchange = () => { rule.provider = provider.value; rule.email = ""; picker.renderValue(); }; provider.setAttribute("aria-label", "供应商"); providerField.append(providerLabel, provider);
      const enabledField = document.createElement("div"); enabledField.className = "rule-field switch-field"; const enabledLabel = document.createElement("span"); enabledLabel.className = "rule-field-label"; enabledLabel.textContent = "保护"; const switchLabel = document.createElement("label"); switchLabel.className = "switch"; const checkbox = document.createElement("input"); checkbox.type = "checkbox"; checkbox.checked = rule.enabled; checkbox.setAttribute("aria-label", "启用保护"); const track = document.createElement("span"); track.className = "switch-track"; const stateText = document.createElement("span"); stateText.textContent = rule.enabled ? "已开启" : "已关闭"; checkbox.onchange = () => { rule.enabled = checkbox.checked; stateText.textContent = checkbox.checked ? "已开启" : "已关闭"; }; switchLabel.append(checkbox, track, stateText); enabledField.append(enabledLabel, switchLabel);
      const numberField = (field, label) => { const wrap = document.createElement("div"); wrap.className = "rule-field number"; const labelElement = document.createElement("span"); labelElement.className = "rule-field-label"; labelElement.textContent = label; const input = document.createElement("input"); input.type = "number"; input.className = "cell-input"; input.min = "0"; input.max = "99.99"; input.step = "0.1"; input.value = rule[field]; input.setAttribute("aria-label", label); input.oninput = () => { rule[field] = Number(input.value); }; wrap.append(labelElement, input); return wrap; };
      const five = numberField("reserve_five_hour", "五小时保留 %"), weekly = numberField("reserve_weekly", "每周保留 %");
      const actions = document.createElement("div"); actions.className = "rule-field actions"; const remove = document.createElement("button"); remove.type = "button"; remove.className = "rule-delete"; remove.setAttribute("aria-label", "删除规则"); remove.title = "删除规则"; const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg"); svg.setAttribute("viewBox", "0 0 20 20"); svg.setAttribute("fill", "none"); svg.setAttribute("stroke", "currentColor"); svg.setAttribute("stroke-width", "1.6"); svg.setAttribute("stroke-linecap", "round"); for (const d of ["M3 5h14", "M8 5V3h4v2", "m6 8 .5 8h7L14 8"]) { const p = document.createElementNS("http://www.w3.org/2000/svg", "path"); p.setAttribute("d", d); svg.append(p); } remove.append(svg); remove.onclick = () => { draft.splice(index, 1); rules(); }; actions.append(remove);
      row.append(account, providerField, enabledField, five, weekly, actions); el("rules").append(row);
    });
  }
  function quota(listElement, windows) {
    if (!windows.length) { const empty = document.createElement("span"); empty.className = "sample-reason"; empty.textContent = "暂无可信采样"; listElement.append(empty); return; }
    windows.forEach(window => { const item = document.createElement("span"); item.className = "quota" + (window.remaining < 8 ? " low" : ""); const scope = window.scope === "third_party" ? "Claude / GPT" : window.scope === "gemini" ? "Gemini" : "全部"; const kind = window.kind === "five_hour" ? "五小时" : "每周"; item.append(scope + " " + kind); const amount = document.createElement("strong"); amount.textContent = window.remaining.toFixed(1) + "%"; item.append(amount); listElement.append(item); });
  }
  function observations(rows) {
    el("observations").replaceChildren();
    const items = Object.values(rows);
    if (!items.length) { const empty = document.createElement("p"); empty.className = "empty"; empty.textContent = "暂无账号采样。连接后等待插件完成第一次检查。"; el("observations").append(empty); return; }
    items.forEach(item => {
      const card = document.createElement("article"); card.className = "sample-card";
      const main = document.createElement("div"); main.className = "sample-main";
      const title = document.createElement("div"); title.className = "sample-title"; const name = document.createElement("strong"); name.textContent = item.email || item.name; name.title = item.email || item.name; const badge = document.createElement("span"); badge.className = "badge " + (item.orphan_reason ? "danger" : item.action === "transaction-pending" ? "warning" : item.manual ? "warning" : item.blocked ? "warning" : "success"); badge.textContent = item.orphan_reason ? "所有权孤儿" : item.action === "transaction-pending" ? "事务待核对" : item.manual ? "人工限制" : item.blocked ? "额度保护" : "额度正常"; title.append(name, badge);
      const meta = document.createElement("div"); meta.className = "sample-meta"; const provider = document.createElement("span"); provider.textContent = providerName(item.provider); const file = document.createElement("span"); file.className = "sample-file"; file.textContent = item.name; file.title = item.name; meta.append(provider, file);
      const quotas = document.createElement("div"); quotas.className = "sample-quotas"; quota(quotas, item.sample ? item.sample.windows : []);
      const reason = document.createElement("p"); reason.className = "sample-reason"; reason.textContent = [item.error, ...(item.reasons || []), item.action, item.transaction_id ? "事务 " + item.transaction_id + " " + (item.transaction_phase || "") : "", item.orphan_reason, item.snapshot_error].filter(Boolean).join("；");
      main.append(title, meta, quotas, reason);
      const side = document.createElement("div"); side.className = "sample-side"; const time = document.createElement("span"); time.className = "sample-time"; time.textContent = item.updated_ms ? new Date(item.updated_ms).toLocaleString() : "等待采样"; side.append(time);
      card.append(main, side); el("observations").append(card);
    });
  }
  async function request(path, options = {}) {
    const response = await fetch(base + path, {...options, credentials: "same-origin", headers: {"Authorization": "Bearer " + key, "Content-Type": "application/json", ...(options.headers || {})}});
    const body = await response.json().catch(() => ({}));
    if (!response.ok) { const error = new Error(body.error || "管理请求失败 " + response.status); error.status = response.status; throw error; }
    return {body, response};
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
  function decodeStoredValue(raw) {
    if (!raw || !raw.startsWith("enc::")) return raw;
    const prefix = raw.startsWith("enc::v2::") ? "enc::v2::" : "enc::v1::";
    try { const encoded = atob(raw.slice(prefix.length)); const encrypted = Uint8Array.from(encoded, c => c.charCodeAt(0)); const salt = prefix === "enc::v2::" ? `cli-proxy-api-webui::secure-storage|v2|${window.location.host}` : `cli-proxy-api-webui::secure-storage|${window.location.host}|${navigator.userAgent}`; const keyBytes = new TextEncoder().encode(salt); return new TextDecoder().decode(encrypted.map((byte, index) => byte ^ keyBytes[index % keyBytes.length])); } catch (_) { return raw; }
  }
  function readStoredValue(name) { try { const raw = localStorage.getItem(name); if (raw === null) return null; const decoded = decodeStoredValue(raw); try { return JSON.parse(decoded); } catch (_) { return decoded; } } catch (_) { return null; } }
  function readCPAMCManagementKey() { try { if (localStorage.getItem("isLoggedIn") !== "true") return ""; const persisted = readStoredValue(CPAMC_AUTH_KEY); const current = persisted && typeof persisted === "object" ? persisted.state?.managementKey : ""; if (typeof current === "string" && current.trim()) return current.trim(); const legacy = readStoredValue("managementKey"); return typeof legacy === "string" ? legacy.trim() : ""; } catch (_) { return ""; } }
  function readSavedManagementKey() { try { const direct = localStorage.getItem(AUTH_KEY) || sessionStorage.getItem(AUTH_KEY); if (typeof direct === "string" && direct.trim()) return direct.trim(); } catch (_) {} return readCPAMCManagementKey(); }
  function clearSavedManagementKey() { try { localStorage.removeItem(AUTH_KEY); sessionStorage.removeItem(AUTH_KEY); } catch (_) {} }
  function rememberManagementKey(value, remember) { try { if (remember) localStorage.setItem(AUTH_KEY, value); else sessionStorage.setItem(AUTH_KEY, value); } catch (_) {} }
  function showAuth(kind, title, message) { el("authGate").hidden = false; el("app").hidden = true; el("authTitle").textContent = title; el("authMessage").textContent = message; el("manualAuth").hidden = kind === "loading"; el("retrySession").hidden = kind !== "error"; }
  function showManualAuth(title, message) { key = ""; el("manualKey").value = ""; showAuth("manual", title, message); el("manualKey").focus(); }
  async function connectWithKey(candidate, remember) { const value = String(candidate || "").trim(); if (!value) return; key = value; showAuth("loading", "正在接入管理会话", "正在验证管理令牌并读取额度保护设置…"); try { await load(); if (remember) rememberManagementKey(value, true); el("authGate").hidden = true; el("app").hidden = false; el("manualKey").value = ""; } catch (error) { if (error.status === 401 || error.status === 403) clearSavedManagementKey(); showManualAuth(error.status === 401 ? "管理令牌已失效" : "无法连接管理会话", error.message || "请检查管理令牌后重试。"); } }
  async function connectFromSession() { const saved = readSavedManagementKey(); if (!saved) { showManualAuth("接入管理会话", "未检测到 CPAMP 会话，请输入 CPA 管理令牌。"); return; } key = saved; showAuth("loading", "正在接入管理会话", "正在读取 CPAMP 当前会话，无需再次输入管理令牌。"); try { await load(); el("authGate").hidden = true; el("app").hidden = false; } catch (error) { if (error.status === 401 || error.status === 403) clearSavedManagementKey(); showManualAuth("管理会话不可用", error.message || "请重新输入管理令牌。"); } }
  function logOut() { clearSavedManagementKey(); key = ""; draft = []; etag = ""; accounts = []; el("rules").replaceChildren(); el("observations").replaceChildren(); el("app").hidden = true; el("authGate").hidden = false; showAuth("manual", "接入管理会话", "已退出当前管理会话。请输入管理令牌，或重新登录 CPAMP。"); el("manualKey").focus(); }
  async function load() {
    show("正在读取运行状态…");
    const [status, settings, data, inventory] = await Promise.all([request("/status"), request("/settings"), request("/observations"), request("/accounts")]);
    setStatus(status.body); draft = settings.body.settings.rules; accounts = inventory.body.accounts || []; rules(); observations(data.body.observations); etag = settings.response.headers.get("ETag") || '"' + settings.body.revision + '"'; show("已加载最新状态", "success");
  }
  el("manualConnect").addEventListener("click", () => connectWithKey(el("manualKey").value, el("rememberKey").checked));
  el("manualKey").addEventListener("keydown", event => { if (event.key === "Enter") { event.preventDefault(); connectWithKey(el("manualKey").value, el("rememberKey").checked); } });
  el("retrySession").addEventListener("click", connectFromSession); el("logout").addEventListener("click", logOut);
  el("refresh").onclick = () => load().catch(error => show(error.message, "error"));
  el("add").onclick = () => { draft.push({email: "", provider: "codex", enabled: true, reserve_five_hour: 8, reserve_weekly: 3}); rules(); };
  el("save").onclick = async () => { try { await request("/settings", {method: "PUT", headers: {"If-Match": etag}, body: JSON.stringify({schema: 1, rules: draft})}); await load(); show("已保存，下一次检查将使用新规则", "success"); } catch (error) { show(error.message, "error"); } };
  connectFromSession();
})();