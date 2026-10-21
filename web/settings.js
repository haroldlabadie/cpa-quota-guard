(() => {
  "use strict";
  const base = "/v0/management/plugins/cpa-quota-guard";
  const AUTH_KEY = "cpa_quota_guard_mgmt_key", CPAMC_AUTH_KEY = "cli-proxy-auth";
  let key = "", etag = "", draft = [], accounts = [], openPicker = null;
  const el = id => document.getElementById(id);
  const show = (text, tone = "") => { const message = el("message"); message.textContent = text; message.className = "message" + (tone ? " " + tone : ""); };
  let PROVIDERS = [];
  let SCOPES = {};
  const FALLBACK_PROVIDERS = [
    {key: "codex", label: "Codex", hint: "整份凭证额度", whole_credential: true, scopes: [{key: "all", label: "全部额度", hint: "整份凭证共用五小时与每周窗口"}]},
    {key: "antigravity", label: "Antigravity", hint: "Gemini 与 Claude / GPT 分组额度", whole_credential: false, scopes: [{key: "gemini", label: "Gemini", hint: "Gemini 模型组"}, {key: "third_party", label: "Claude / GPT", hint: "Claude 与 GPT 模型组"}]}
  ];
  function setCatalog(rows) {
    const valid = Array.isArray(rows) ? rows.filter(item => item && typeof item.key === "string" && item.key && Array.isArray(item.scopes) && item.scopes.length) : [];
    const source = valid.length ? valid : FALLBACK_PROVIDERS;
    PROVIDERS = source.map(item => ({value: item.key, label: item.label || item.key, hint: item.hint || "", whole: Boolean(item.whole_credential)}));
    SCOPES = Object.fromEntries(source.map(item => [item.key, item.scopes.filter(scope => scope && typeof scope.key === "string" && scope.key).map(scope => ({value: scope.key, label: scope.label || scope.key, hint: scope.hint || ""}))]));
  }
  setCatalog(FALLBACK_PROVIDERS);
  const providerName = value => (PROVIDERS.find(item => item.value === value) || PROVIDERS[0] || {label: value}).label;
  const scopesFor = provider => SCOPES[provider] || [];
  const accountLabel = item => item.email || item.name || "未命名账号";
  const selectedScopes = rule => Array.isArray(rule.scopes) && rule.scopes.length ? rule.scopes.filter(value => scopesFor(rule.provider).some(item => item.value === value)) : scopesFor(rule.provider).map(item => item.value);
  function closePicker() { if (openPicker) { openPicker.menu.hidden = true; openPicker.button.setAttribute("aria-expanded", "false"); openPicker = null; } }
  document.addEventListener("click", event => { if (openPicker && !openPicker.root.contains(event.target)) closePicker(); });
  document.addEventListener("keydown", event => { if (event.key === "Escape") closePicker(); });
  function chevron() {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg"); svg.setAttribute("class", "select-chevron"); svg.setAttribute("viewBox", "0 0 16 16"); svg.setAttribute("fill", "none"); svg.setAttribute("stroke", "currentColor"); svg.setAttribute("stroke-width", "1.5"); const path = document.createElementNS("http://www.w3.org/2000/svg", "path"); path.setAttribute("d", "m4 6 4 4 4-4"); svg.append(path); return svg;
  }
  function picker(rule, config) {
    const root = document.createElement("div"); root.className = "select-shell";
    const button = document.createElement("button"); button.type = "button"; button.className = "select-button"; button.setAttribute("aria-haspopup", "listbox"); button.setAttribute("aria-expanded", "false");
    const value = document.createElement("span"); value.className = "select-value"; const title = document.createElement("strong"); const subtitle = document.createElement("small"); value.append(title, subtitle); button.append(value, chevron());
    const menu = document.createElement("div"); menu.className = "select-menu"; menu.setAttribute("role", "listbox"); menu.hidden = true;
    const renderValue = () => { title.textContent = config.valueLabel(); subtitle.textContent = config.valueHint(); button.title = [title.textContent, subtitle.textContent].filter(Boolean).join(" · "); };
    const renderMenu = () => { menu.replaceChildren(); const rows = config.options(); if (!rows.length) { const empty = document.createElement("p"); empty.className = "empty"; empty.textContent = config.empty; menu.append(empty); return; } rows.forEach(item => { const option = document.createElement("button"); option.type = "button"; option.className = "select-option"; option.setAttribute("role", "option"); option.setAttribute("aria-selected", String(config.isSelected(item))); const copy = document.createElement("span"); const name = document.createElement("strong"); name.textContent = item.label; copy.append(name); if (item.hint) { const hint = document.createElement("small"); hint.textContent = item.hint; copy.append(hint); } option.append(copy); if (item.meta) { const mark = document.createElement("em"); mark.textContent = item.meta; option.append(mark); } if (config.multiple) { const check = document.createElement("span"); check.className = "select-check"; check.textContent = config.isSelected(item) ? "✓" : ""; option.append(check); } option.onclick = event => { event.stopPropagation(); config.select(item); renderValue(); renderMenu(); if (!config.multiple) { closePicker(); } }; menu.append(option); }); };
    button.onclick = event => { event.stopPropagation(); if (openPicker && openPicker.menu === menu) { closePicker(); return; } closePicker(); renderMenu(); menu.hidden = false; button.setAttribute("aria-expanded", "true"); openPicker = { root, menu, button }; };
    renderValue(); root.append(button, menu); return { root, renderValue };
  }
  function accountPicker(rule) {
    return picker(rule, {
      multiple: false,
      empty: "当前供应商没有可用账号",
      options: () => accounts.filter(item => item.provider === rule.provider && (item.email === rule.email || !draft.some(other => other !== rule && other.email === item.email && other.provider === item.provider))).map(item => ({value: item.email, label: accountLabel(item), hint: item.name, meta: item.disabled ? "已停用" : "可用"})),
      isSelected: item => item.value === rule.email,
      select: item => { rule.email = item.value; },
      valueLabel: () => { const selected = accounts.find(item => item.email === rule.email && item.provider === rule.provider); return selected ? accountLabel(selected) : rule.email || "选择账号"; },
      valueHint: () => { const selected = accounts.find(item => item.email === rule.email && item.provider === rule.provider); return selected ? selected.name : rule.email ? "账号不在当前凭证列表" : "从当前供应商的凭证中选择"; }
    });
  }
  function providerPicker(rule, onChange) {
    return picker(rule, {
      multiple: false,
      empty: "暂无供应商",
      options: () => PROVIDERS.map(item => ({value: item.value, label: item.label})),
      isSelected: item => item.value === rule.provider,
      select: item => { rule.provider = item.value; rule.email = ""; rule.scopes = scopesFor(item.value).map(scope => scope.value); onChange(); },
      valueLabel: () => providerName(rule.provider),
      valueHint: () => (PROVIDERS.find(item => item.value === rule.provider) || {}).hint || ""
    });
  }
  function scopePicker(rule) {
    return picker(rule, {
      multiple: true,
      empty: "暂无模型组",
      options: () => scopesFor(rule.provider).map(item => ({value: item.value, label: item.label, hint: item.hint})),
      isSelected: item => selectedScopes(rule).includes(item.value),
      select: item => { const chosen = new Set(selectedScopes(rule)); if (chosen.has(item.value)) { if (chosen.size > 1) chosen.delete(item.value); } else { chosen.add(item.value); } rule.scopes = scopesFor(rule.provider).map(scope => scope.value).filter(value => chosen.has(value)); },
      valueLabel: () => { const chosen = selectedScopes(rule); const rows = scopesFor(rule.provider); if (rows.length === 1) return rows[0].label; if (chosen.length === rows.length) return "全部模型组"; return rows.filter(item => chosen.includes(item.value)).map(item => item.label).join(" + ") || "选择模型组"; },
      valueHint: () => { const rows = scopesFor(rule.provider); if (rows.length === 1) return (PROVIDERS.find(item => item.value === rule.provider) || {}).whole ? "整份凭证统一保护" : "仅此模型组"; return selectedScopes(rule).length === rows.length ? rows.map(item => item.label).join(" 与 ") + " 均保护" : "仅保护已选模型组"; }
    });
  }
  function rules() {
    el("rules").replaceChildren();
    if (!draft.length) { const empty = document.createElement("p"); empty.className = "empty"; empty.textContent = "还没有保护规则。添加账号后，插件才会对它进行采样和保护。"; el("rules").append(empty); return; }
    draft.forEach((rule, index) => {
      const row = document.createElement("div"); row.className = "rule";
      const field = (label, className = "") => { const wrap = document.createElement("div"); wrap.className = "rule-field" + (className ? " " + className : ""); const text = document.createElement("span"); text.className = "rule-field-label"; text.textContent = label; wrap.append(text); return wrap; };
      const providerField = field("供应商", "provider-field"); const provider = providerPicker(rule, () => { rules(); }); providerField.append(provider.root);
      const account = field("账号", "account"); const accountChoice = accountPicker(rule); account.append(accountChoice.root);
      const scope = field("保护模型组", "scope-field"); scope.append(scopePicker(rule).root);
      const enabledField = field("保护", "switch-field"); const switchLabel = document.createElement("label"); switchLabel.className = "switch"; const checkbox = document.createElement("input"); checkbox.type = "checkbox"; checkbox.checked = rule.enabled; checkbox.setAttribute("aria-label", "启用保护"); const track = document.createElement("span"); track.className = "switch-track"; const stateText = document.createElement("span"); stateText.textContent = rule.enabled ? "已开启" : "已关闭"; checkbox.onchange = () => { rule.enabled = checkbox.checked; stateText.textContent = checkbox.checked ? "已开启" : "已关闭"; }; switchLabel.append(checkbox, track, stateText); enabledField.append(switchLabel);
      const numberField = (key, label, areaClass) => { const wrap = field(label, "number " + areaClass); const input = document.createElement("div"); input.className = "number-input"; const box = document.createElement("input"); box.type = "number"; box.className = "cell-input"; box.min = "0"; box.max = "99.99"; box.step = "0.1"; box.value = rule[key]; box.setAttribute("aria-label", label); const unit = document.createElement("span"); unit.textContent = "%"; box.oninput = () => { rule[key] = Number(box.value); }; input.append(box, unit); wrap.append(input); return wrap; };
      const five = numberField("reserve_five_hour", "五小时保留", "five"); const weekly = numberField("reserve_weekly", "每周保留", "weekly"); const thresholds = document.createElement("div"); thresholds.className = "thresholds"; thresholds.append(five, weekly);
      const actions = document.createElement("div"); actions.className = "rule-field actions"; const remove = document.createElement("button"); remove.type = "button"; remove.className = "rule-delete"; remove.setAttribute("aria-label", "删除规则"); remove.title = "删除规则"; const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg"); svg.setAttribute("viewBox", "0 0 20 20"); svg.setAttribute("fill", "none"); svg.setAttribute("stroke", "currentColor"); svg.setAttribute("stroke-width", "1.6"); svg.setAttribute("stroke-linecap", "round"); for (const d of ["M3 5h14", "M8 5V3h4v2", "m6 8 .5 8h7L14 8"]) { const p = document.createElementNS("http://www.w3.org/2000/svg", "path"); p.setAttribute("d", d); svg.append(p); } remove.append(svg); remove.onclick = () => { draft.splice(index, 1); rules(); }; actions.append(remove);
      row.append(providerField, account, scope, enabledField, thresholds, actions); el("rules").append(row);
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
    const [status, settings, data, inventory, catalog] = await Promise.all([request("/status"), request("/settings"), request("/observations"), request("/accounts"), request("/catalog").catch(() => ({body: {providers: FALLBACK_PROVIDERS}}))]);
    setStatus(status.body); setCatalog(catalog.body.providers); draft = settings.body.settings.rules; accounts = inventory.body.accounts || []; rules(); observations(data.body.observations); etag = settings.response.headers.get("ETag") || '"' + settings.body.revision + '"'; show("已加载最新状态", "success");
  }
  el("manualConnect").addEventListener("click", () => connectWithKey(el("manualKey").value, el("rememberKey").checked));
  el("manualKey").addEventListener("keydown", event => { if (event.key === "Enter") { event.preventDefault(); connectWithKey(el("manualKey").value, el("rememberKey").checked); } });
  el("retrySession").addEventListener("click", connectFromSession); el("logout").addEventListener("click", logOut);
  el("refresh").onclick = () => load().catch(error => show(error.message, "error"));
  el("add").onclick = () => { const provider = PROVIDERS[0] || {value: "codex"}; draft.push({email: "", provider: provider.value, scopes: scopesFor(provider.value).map(scope => scope.value), enabled: true, reserve_five_hour: 8, reserve_weekly: 3}); rules(); };
  el("save").onclick = async () => { const missing = draft.findIndex(rule => !String(rule.email || "").trim()); if (missing >= 0) { show("请先为第 " + (missing + 1) + " 条规则选择账号", "error"); const input = el("rules").children[missing] && el("rules").children[missing].children[1].children[1].children[0]; if (input) input.focus(); return; } try { await request("/settings", {method: "PUT", headers: {"If-Match": etag}, body: JSON.stringify({schema: 1, rules: draft})}); await load(); show("已保存，下一次检查将使用新规则", "success"); } catch (error) { show(error.message, "error"); } };
  connectFromSession();
})();