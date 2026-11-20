/* AATMT — dashboard, audit pages and system configuration. */
'use strict';

const pageHead = (title, sub, right = '') => `<div class="page-head"><div><h1>${esc(title)}</h1>${sub ? `<p>${sub}</p>` : ''}</div><div class="right row">${right}</div></div>`;

// ---------------------------------------------------------------- dashboard
ROUTES[''] = async () => {
  S.view = 'dashboard';
  $('#app').innerHTML = pageHead('Dashboard', 'What needs you, what is running, and where the tokens go.') + '<div id="dash"><p class="muted"><span class="spin"></span></p></div>';
  const draw = async () => {
    const d = await act(() => api('GET', '/dashboard'));
    if (!d || S.view !== 'dashboard') return;
    const active = STAGES.reduce((a, s) => a + (d.stages[s] || 0), 0);
    const max = Math.max(1, ...d.daily.map((x) => x.input + x.output + x.cache_read));
    const kindBadge = { approval: 'paused', question: 'planned', error: 'err', review: 'review' };
    $('#dash').innerHTML = `
      <div class="grid4" style="margin-bottom:14px">
        <div class="stat accent"><div class="v">${active}</div><div class="l">open tasks · ${d.running} running</div></div>
        <div class="stat"><div class="v">${d.attention.length}</div><div class="l">need your attention</div></div>
        <div class="stat"><div class="v">${d.done_week}</div><div class="l">completed this week</div></div>
        <div class="stat"><div class="v">${fmtK(usageTotal(d.week))}</div><div class="l">tokens this week${d.week.cost ? ` · $${d.week.cost.toFixed(2)}` : ''}</div></div></div>
      <div class="grid2" style="margin-bottom:14px">
        <div class="card"><div class="card-h"><h3>Needs attention</h3></div>
          ${d.attention.map((a) => `<a class="row nowrap" href="#/task/${esc(a.task_id)}" style="padding:7px 0;border-bottom:1px solid var(--line)">
            <span class="badge ${kindBadge[a.kind] || ''}">${esc(a.kind)}</span><b class="ellipsis" style="max-width:40%">${esc(a.title)}</b><span class="small muted ellipsis grow">${esc(a.text)}</span></a>`).join('') || empty('check', 'Nothing waiting for you')}</div>
        <div class="card"><div class="card-h"><h3>Pipeline</h3><span class="muted small right">${d.tasks} tasks · ${d.projects} projects</span></div>
          ${STAGES.map((s) => `<div class="row nowrap" style="margin:6px 0"><span style="width:96px">${stageBadge(s)}</span>
            <div class="bar grow"><i style="width:${active ? ((d.stages[s] || 0) / active) * 100 : 0}%;background:${STAGE_COLOR[s]}"></i></div><b style="width:28px;text-align:right">${d.stages[s] || 0}</b></div>`).join('')}</div></div>
      <div class="grid2" style="margin-bottom:14px">
        <div class="card"><div class="card-h"><h3>Tokens · last 14 days</h3><span class="muted small right">dark = input+output · light = cache reads</span></div>
          <div class="chart">${d.daily.map((x) => { const io = x.input + x.output; return `<div class="b" title="${x.date}: in ${fmtN(x.input)} · out ${fmtN(x.output)} · cache ${fmtN(x.cache_read)} · ${x.calls} calls">
            <i class="c" style="height:${(x.cache_read / max) * 100}%"></i><i style="height:${(io / max) * 100}%"></i><em>${x.date.slice(8)}</em></div>`; }).join('')}</div></div>
        <div class="card"><div class="card-h"><h3>Providers</h3><a class="btn sm right" href="#/providers">Manage</a></div>
          ${d.providers.map((p) => `<div class="row nowrap" style="padding:6px 0;border-bottom:1px solid var(--line)"><span class="badge dot ${!p.enabled ? '' : p.healthy ? 'ok' : 'err'}" title="${esc(p.error || '')}">${!p.enabled ? 'off' : p.healthy ? 'healthy' : 'tripped'}</span>
            <b class="ellipsis">${esc(p.name)}</b><span class="small muted ellipsis grow">${esc(p.model)}</span><span class="small">${fmtK(usageTotal(p.usage))} · ${fmtN(p.usage.calls)} calls</span></div>`).join('') || empty('cpu', 'No providers yet')}</div></div>
      <div class="grid2">
        <div class="card"><div class="card-h"><h3>Recent activity</h3></div><div class="log" style="max-height:340px">
          ${d.recent.map((e) => `<a class="ev k-${esc(e.type)}" href="#/task/${esc(e.task_id)}" style="grid-template-columns:70px 70px 1fr"><span class="time">${time(e.at)}</span><span class="ty">${esc(e.type)}</span>
            <span class="ellipsis"><b>${esc(e.task_title)}</b> <span class="muted">${esc(e.title)}</span></span></a>`).join('') || empty('wave', 'No activity yet')}</div></div>
        <div class="card"><div class="card-h"><h3>Most expensive tasks</h3></div>
          ${d.top_tasks.map((t) => `<a class="row nowrap" href="#/task/${esc(t.id)}" style="padding:6px 0;border-bottom:1px solid var(--line)">${stageBadge(t.stage)}<span class="ellipsis grow">${esc(t.title)}</span><b class="small">${fmtK(t.tokens)}</b></a>`).join('') || empty('tasks', '—')}</div></div>`;
  };
  S.onEvent = debounce((ev) => { if (ev.type !== 'llm' && ev.type !== 'tool') draw(); }, 800);
  await draw();
};

// ---------------------------------------------------------------- approvals (pending + audit trail)
ROUTES.approvals = async () => {
  S.view = 'approvals';
  $('#app').innerHTML = pageHead('Approvals', 'Commands waiting for you, and the audit trail of every command decision (human, rule, policy or project command).', '<a class="btn" href="#/rules">' + ic('shield') + ' Command rules</a>') + '<div id="ap"></div>';
  const draw = async () => {
    const r = await act(() => api('GET', '/approvals'));
    if (!r || S.view !== 'approvals') return;
    setPending(r.pending.length);
    $('#ap').innerHTML = `${r.pending.length ? `<div class="stack" style="margin-bottom:14px">${r.pending.map((p) => `<div class="banner warn"><div class="row"><b>${esc(p.task_title)}</b><span class="small muted">${dt(p.at)}</span>
        <a class="btn sm right" href="#/task/${esc(p.task_id)}">Open task</a></div><pre class="mono">${esc(p.command)}</pre>
        <div class="row"><button class="ok" data-ap="1" data-t="${esc(p.task_id)}" data-id="${esc(p.id)}">Allow once</button><button data-ap="2" data-t="${esc(p.task_id)}" data-id="${esc(p.id)}">Always allow</button>
        <button class="danger" data-ap="0" data-t="${esc(p.task_id)}" data-id="${esc(p.id)}">Deny</button></div></div>`).join('')}</div>` : ''}
      <div class="table-wrap"><table class="t"><thead><tr><th>Time</th><th>Task</th><th>Command</th><th>Decision</th><th>Decided by</th><th>Rule</th></tr></thead><tbody>
      ${r.records.map((x) => `<tr><td class="small muted">${dt(x.at)}</td><td class="small ellipsis" style="max-width:220px"><a href="#/task/${esc(x.task_id)}">${esc(x.task_title || x.task_id)}</a></td>
        <td><code>${esc(x.command)}</code></td><td><span class="badge ${esc(x.decision)}">${esc(x.decision)}</span></td><td><span class="badge outline">${esc(x.decided_by)}</span></td>
        <td class="small mono muted">${esc(x.rule || '')}</td></tr>`).join('')}</tbody></table>${r.records.length ? '' : empty('shieldcheck', 'No command decisions yet')}</div>`;
    $$('[data-ap]').forEach((b) => (b.onclick = async () => {
      await act(() => api('POST', `/tasks/${b.dataset.t}/approval`, { id: b.dataset.id, approve: b.dataset.ap !== '0', always: b.dataset.ap === '2' }));
      setTimeout(draw, 300);
    }));
  };
  S.onEvent = debounce((ev) => { if (ev.type === 'approval' || ev.type === 'tool') draw(); }, 400);
  await draw();
};

// ---------------------------------------------------------------- LLM records
ROUTES.llm = async () => {
  S.view = 'llm';
  $('#app').innerHTML = pageHead('LLM Records', 'Every provider call across tasks: agent, provider, tokens (incl. cache hits), latency, and the truncated request/response. Click a row to inspect.') + '<div id="llmr"><p class="muted"><span class="spin"></span></p></div>';
  const rows = await act(() => api('GET', '/llm-records'));
  if (!rows) return;
  $('#llmr').innerHTML = llmTable(rows, true);
  wireLLMTable($('#llmr'));
};

// ---------------------------------------------------------------- providers
const PRESETS = {
  openai: { name: 'OpenAI', format: 'openai', base_url: 'https://api.openai.com/v1', model: 'gpt-4o', vision: true, context_window: 128000 },
  anthropic: { name: 'Anthropic Claude', format: 'anthropic', base_url: 'https://api.anthropic.com', model: 'claude-opus-5-5', vision: true, prompt_cache: true, context_window: 1000000, max_tokens: 16000 },
  azure: { name: 'Azure OpenAI', format: 'openai', base_url: 'https://YOUR-RESOURCE.openai.azure.com/openai/deployments/YOUR-DEPLOYMENT/chat/completions?api-version=2024-10-21', auth_header: 'api-key', model: 'YOUR-DEPLOYMENT', vision: true },
  gemini: { name: 'Google Gemini', format: 'openai', base_url: 'https://generativelanguage.googleapis.com/v1beta/openai', model: 'gemini-2.5-flash', vision: true, context_window: 1000000 },
  openrouter: { name: 'OpenRouter', format: 'openai', base_url: 'https://openrouter.ai/api/v1', model: 'openai/gpt-4o-mini', vision: true },
  deepseek: { name: 'DeepSeek', format: 'openai', base_url: 'https://api.deepseek.com/v1', model: 'deepseek-chat', vision: false, context_window: 64000 },
  ollama: { name: 'Ollama (local)', format: 'openai', base_url: 'http://localhost:11434/v1', model: 'qwen2.5-coder:14b', vision: false, context_window: 32000, tool_mode: 'json' },
  lmstudio: { name: 'LM Studio (local)', format: 'openai', base_url: 'http://localhost:1234/v1', model: 'local-model', vision: false, context_window: 32000 },
  gateway: { name: 'Company gateway', format: 'openai', base_url: 'https://llm-gateway.company.local/v1', model: 'gpt-4o', vision: true },
};
ROUTES.providers = async () => {
  S.view = 'providers';
  await loadConfig(); const c = S.cfg; if (!c) return;
  $('#app').innerHTML = pageHead('AI Providers', 'Endpoints, models and limits shared by all agents. Assign them to agents (with failover order) on the Agents page.',
    `<a class="btn" href="#/agents">${ic('bot')} Agent chains</a><button class="primary" id="p-add">${ic('plus')} Add provider</button>`) +
    `<div class="cards">${c.profiles.map(profileCard).join('')}</div>${c.profiles.length ? '' : `<div class="card">${empty('cpu', 'No providers yet. Add an OpenAI-compatible endpoint (OpenAI, Azure, Gemini, OpenRouter, Ollama, your company gateway…) or Anthropic.')}</div>`}
    <p class="help" style="margin-top:16px">${ic('zap')} <b>Failover:</b> 401/402/403/404 or exhausted quota trips a provider immediately; 429/5xx/network errors are retried with backoff (honouring Retry-After) and then fail over. A tripped provider cools down (1 → 5 → 30 min, or the fixed cooldown in Settings) for every task.</p>`;
  const byId = Object.fromEntries(c.profiles.map((p) => [p.id, p]));
  $('#p-add').onclick = () => editProfile(null);
  $$('[data-edit]').forEach((b) => (b.onclick = () => editProfile(byId[b.dataset.edit])));
  $$('[data-test]').forEach((b) => (b.onclick = async () => {
    b.innerHTML = '<span class="spin"></span>'; const r = await act(() => api('POST', `/profiles/${b.dataset.test}/test`)); b.textContent = 'Test';
    if (r) r.ok ? toast(`✓ ${r.ms} ms — "${(r.reply || '').slice(0, 60)}"`) : toast('✗ ' + r.error, true);
    ROUTES.providers();
  }));
  $$('[data-reset]').forEach((b) => (b.onclick = () => act(() => api('POST', `/profiles/${b.dataset.reset}/reset`)).then(ROUTES.providers)));
  $$('[data-toggle]').forEach((b) => (b.onchange = async () => {
    await act(() => api('PUT', '/profiles/' + b.dataset.toggle, { ...byId[b.dataset.toggle], enabled: b.checked, api_key: '' })); ROUTES.providers();
  }));
};
function profileCard(p) {
  const u = p.usage || {}; const br = p.breaker;
  const cooling = br && new Date(br.open_until) > new Date();
  const secs = cooling ? Math.round((new Date(br.open_until) - new Date()) / 1000) : 0;
  return `<div class="card pcard"><div class="row nowrap"><span class="name ellipsis grow">${esc(p.name)}</span><span class="badge outline">${esc(p.format.toUpperCase())}</span>
      ${!p.enabled ? '<span class="badge">off</span>' : cooling ? `<span class="badge err" title="${esc(br.last_error)}">tripped · ${secs}s</span>` : '<span class="badge ok">healthy</span>'}
      <label class="switch" title="enabled"><input type="checkbox" data-toggle="${esc(p.id)}" ${p.enabled ? 'checked' : ''}><span></span></label></div>
    <div class="sub mono">${esc(p.model)}</div><div class="sub ellipsis">${esc(p.base_url || '(default URL)')}</div>
    <div class="chips">${p.vision ? '<span class="badge planned">vision</span>' : ''}${p.tool_mode === 'json' ? '<span class="badge">json tools</span>' : ''}
      ${p.prompt_cache ? '<span class="badge ok">cache</span>' : ''}${p.rate_per_minute ? `<span class="badge">${p.rate_per_minute}/min</span>` : ''}
      ${p.effort ? `<span class="badge">effort ${esc(p.effort)}</span>` : ''}<span class="chip">${esc(p.key_set ? p.key_hint : 'no key')}</span></div>
    <div class="small muted">${fmtN(u.calls)} calls · in ${fmtK(u.input)} · out ${fmtK(u.output)} · cache ${fmtK(u.cache_read)}${u.cost ? ` · $${u.cost.toFixed(3)}` : ''}</div>
    <div class="row" style="margin-top:4px"><button class="sm" data-test="${esc(p.id)}">Test</button><button class="sm" data-edit="${esc(p.id)}">${ic('edit')} Edit</button>
      ${cooling ? `<button class="sm" data-reset="${esc(p.id)}">Reset breaker</button>` : ''}</div></div>`;
}
function editProfile(p) {
  const isNew = !p;
  p = p || { format: 'openai', enabled: true, tool_mode: 'native', vision: false };
  const hdrs = Object.entries(p.headers || {}).map(([k, v]) => `${k}: ${v}`).join('\n');
  const m = modal(`
    <div class="modal-h"><h2>${isNew ? 'Add provider' : esc(p.name)}</h2><button class="ghost icon right" data-close>${ic('x')}</button></div>
    <div class="modal-b">
      ${isNew ? `<label class="f">Preset<select id="pf-preset"><option value="">—</option>${Object.entries(PRESETS).map(([k, v]) => `<option value="${k}">${esc(v.name)}</option>`).join('')}</select></label>` : ''}
      <div class="grid2"><label class="f">Name<input id="pf-name" value="${esc(p.name)}"></label>
        <label class="f">API format<select id="pf-format"><option value="openai">OpenAI-compatible (/chat/completions)</option><option value="anthropic">Anthropic (/v1/messages)</option></select></label></div>
      <label class="f">Base URL <span class="hint">base (…/v1) or the full …/chat/completions URL (Azure: include ?api-version=…)</span><input id="pf-url" value="${esc(p.base_url)}"></label>
      <div class="grid2"><label class="f">API key <span class="hint">${isNew ? 'or env:VAR_NAME' : 'blank = keep · "-" = remove'}</span><input id="pf-key" type="password" autocomplete="off" placeholder="${isNew ? 'sk-… / env:OPENAI_API_KEY' : esc(p.key_hint || '')}"></label>
        <label class="f">Model<div class="row nowrap"><input id="pf-model" class="grow" value="${esc(p.model)}" list="pf-models"><datalist id="pf-models"></datalist>
          ${isNew ? '' : '<button class="sm" id="pf-fetch" type="button">Fetch models</button>'}</div></label></div>
      <div class="grid3"><label class="f">Max output tokens<input id="pf-maxt" type="number" value="${p.max_tokens || ''}" placeholder="8192"></label>
        <label class="f">Context window<input id="pf-ctx" type="number" value="${p.context_window || ''}" placeholder="128000"></label>
        <label class="f">Rate limit / min<input id="pf-rpm" type="number" value="${p.rate_per_minute || ''}" placeholder="unlimited"></label></div>
      <div class="row" style="gap:18px"><label class="chk">${sw('pf-vision', p.vision)} Vision (accepts images)</label>
        <label class="chk">${sw('pf-cache', p.prompt_cache)} Prompt cache (Anthropic)</label><label class="chk">${sw('pf-enabled', p.enabled)} Enabled</label></div>
      <label class="f">Tool calling<select id="pf-tools"><option value="native">Native tool calls</option><option value="json">JSON protocol (gateway / model without function calling)</option></select></label>
      <details><summary class="small muted" style="cursor:pointer">Advanced</summary><div class="col" style="margin-top:8px">
        <div class="grid3"><label class="f">Effort / reasoning_effort<select id="pf-effort"><option value="">(omit)</option>${['minimal', 'low', 'medium', 'high', 'xhigh', 'max'].map((x) => `<option ${p.effort === x ? 'selected' : ''}>${x}</option>`).join('')}</select></label>
          <label class="f">Temperature<input id="pf-temp" value="${p.temperature ?? ''}" placeholder="(omit)"></label>
          <label class="f">Timeout (s)<input id="pf-timeout" type="number" value="${p.timeout_sec || ''}" placeholder="300"></label></div>
        <div class="grid3"><label class="f">Max-tokens field<select id="pf-mtf"><option value="">max_tokens</option><option value="max_completion_tokens" ${p.max_tokens_field === 'max_completion_tokens' ? 'selected' : ''}>max_completion_tokens</option></select></label>
          <label class="f">Auth header<input id="pf-ah" value="${esc(p.auth_header || '')}" placeholder="Authorization / x-api-key / api-key"></label>
          <label class="f">Auth prefix<input id="pf-ap" value="${esc(p.auth_prefix || '')}" placeholder="Bearer  (none = raw key)"></label></div>
        <label class="f">Extra headers <span class="hint">one per line — Key: Value</span><textarea id="pf-hdrs" rows="2">${esc(hdrs)}</textarea></label>
        <div class="grid2"><label class="f">Price in ($ / 1M tokens)<input id="pf-pin" value="${p.price_in || ''}"></label><label class="f">Price out ($ / 1M tokens)<input id="pf-pout" value="${p.price_out || ''}"></label></div>
      </div></details></div>
    <div class="modal-f">${isNew ? '' : `<button class="danger" id="pf-del">${ic('trash')} Delete</button>`}<span class="right"></span>
      <button data-close>Cancel</button><button class="primary" id="pf-save">Save</button></div>`);
  $('#pf-format').value = p.format || 'openai';
  $('#pf-tools').value = p.tool_mode || 'native';
  if ($('#pf-preset')) $('#pf-preset').onchange = (e) => {
    const v = PRESETS[e.target.value]; if (!v) return;
    $('#pf-name').value = v.name; $('#pf-format').value = v.format; $('#pf-url').value = v.base_url; $('#pf-model').value = v.model;
    $('#pf-vision').checked = !!v.vision; $('#pf-cache').checked = !!v.prompt_cache; $('#pf-ctx').value = v.context_window || '';
    $('#pf-maxt').value = v.max_tokens || ''; $('#pf-tools').value = v.tool_mode || 'native'; $('#pf-ah').value = v.auth_header || '';
  };
  if ($('#pf-fetch')) $('#pf-fetch').onclick = async () => {
    const r = await act(() => api('POST', `/profiles/${p.id}/models`));
    if (r && r.ok) { $('#pf-models').innerHTML = r.models.map((x) => `<option value="${esc(x)}">`).join(''); toast(`${r.models.length} models`); } else if (r) toast(r.error, true);
  };
  if ($('#pf-del')) $('#pf-del').onclick = async () => { if (!confirm('Delete provider?')) return; await act(() => api('DELETE', '/profiles/' + p.id)); m.close(); ROUTES.providers(); };
  $('#pf-save').onclick = async () => {
    const num = (id) => parseInt($(id).value, 10) || 0; const flt = (id) => parseFloat($(id).value) || 0;
    const headers = {};
    $('#pf-hdrs').value.split('\n').forEach((l) => { const i = l.indexOf(':'); if (i > 0) headers[l.slice(0, i).trim()] = l.slice(i + 1).trim(); });
    const temp = $('#pf-temp').value.trim();
    const body = { name: $('#pf-name').value, format: $('#pf-format').value, base_url: $('#pf-url').value, api_key: $('#pf-key').value, model: $('#pf-model').value,
      max_tokens: num('#pf-maxt'), context_window: num('#pf-ctx'), rate_per_minute: num('#pf-rpm'), vision: $('#pf-vision').checked, prompt_cache: $('#pf-cache').checked,
      enabled: $('#pf-enabled').checked, tool_mode: $('#pf-tools').value, effort: $('#pf-effort').value, timeout_sec: num('#pf-timeout'),
      max_tokens_field: $('#pf-mtf').value, auth_header: $('#pf-ah').value.trim(), auth_prefix: $('#pf-ap').value, headers, price_in: flt('#pf-pin'), price_out: flt('#pf-pout') };
    if (temp !== '') body.temperature = parseFloat(temp);
    const r = await act(() => (isNew ? api('POST', '/profiles', body) : api('PUT', '/profiles/' + p.id, body)), 'Saved');
    if (r) { S.cfg = r; m.close(); ROUTES.providers(); }
  };
}

// ---------------------------------------------------------------- agents
ROUTES.agents = async () => {
  S.view = 'agents';
  await loadConfig();
  const agents = await act(() => api('GET', '/agents')); if (!agents) return;
  const byId = Object.fromEntries(S.cfg.profiles.map((p) => [p.id, p]));
  const names = { vision: 'Vision reader', analyzer: 'Planner (analyzer)', worker: 'Work agent', chat: 'Chat assistant' };
  $('#app').innerHTML = pageHead('Agents', 'Built-in agents: their model failover chain and instructions. Empty chain = every enabled provider in list order (chat falls back to the analyzer chain).') +
    `<div class="cards" style="grid-template-columns:repeat(auto-fill,minmax(420px,1fr))">${agents.map((a) => `<div class="card pcard">
      <div class="row nowrap">${ic('bot')}<span class="name">${names[a.role]}</span><span class="chip">${a.role}</span><span class="badge outline">${a.prompt ? 'custom prompt' : 'built-in'}</span></div>
      <div class="small muted">${esc(a.description)}</div>
      ${a.tools.length ? `<div class="chips">${a.tools.map((t) => `<span class="chip">${t}</span>`).join('')}</div>` : ''}
      ${a.mcp.length ? `<div class="chips">${a.mcp.map((m) => `<a class="chip" href="#/mcp" style="color:var(--accent)">${ic('plug')} mcp: ${esc(m)}</a>`).join('')}</div>` : ''}
      <div class="section-title" style="margin-top:6px">Model chain (failover order)</div>
      <div class="chain">${a.chain.map((id, i) => byId[id] ? `<div class="ci"><span class="ix">${i + 1}</span><span class="grow">${esc(byId[id].name)}</span>
        ${a.role === 'vision' && !byId[id].vision ? '<span class="badge err">no vision</span>' : ''}${!byId[id].enabled ? '<span class="badge">off</span>' : ''}
        <button class="sm ghost" data-mv="${a.role}:${i}:-1" ${i ? '' : 'disabled'}>▲</button><button class="sm ghost" data-mv="${a.role}:${i}:1" ${i === a.chain.length - 1 ? 'disabled' : ''}>▼</button>
        <button class="sm ghost danger" data-rm="${a.role}:${i}">${ic('x')}</button></div>` : '').join('') || `<div class="small faint">auto: ${a.role === 'chat' ? 'analyzer chain' : a.role === 'vision' ? 'enabled providers with vision' : 'all enabled providers'}</div>`}</div>
      <select data-addrole="${a.role}"><option value="">Add provider to chain…</option>${S.cfg.profiles.filter((p) => !a.chain.includes(p.id)).map((p) => `<option value="${esc(p.id)}">${esc(p.name)}</option>`).join('')}</select>
      <div class="row"><button class="sm" data-prompt="${a.role}">${ic('edit')} Edit instructions</button></div></div>`).join('')}</div>
    <p class="help" style="margin-top:16px">💡 Token-saving setup: put the (usually expensive) vision model only on <b>Vision</b> — screenshots are read once and cached as text; the strongest model on <b>Planner</b>; a cheaper model on <b>Work agent</b> — every step is rebuilt from a compact brief, so switching models never replays chat history.</p>`;
  const chainOf = (role) => [...(agents.find((a) => a.role === role).chain)];
  const save = async (role, chain) => { await act(() => api('PUT', '/agents/' + role, { chain })); ROUTES.agents(); };
  $$('[data-addrole]').forEach((s) => (s.onchange = () => s.value && save(s.dataset.addrole, [...chainOf(s.dataset.addrole), s.value])));
  $$('[data-mv]').forEach((b) => (b.onclick = () => { const [role, i, d] = b.dataset.mv.split(':'); const c = chainOf(role); const j = +i + +d; [c[i], c[j]] = [c[j], c[i]]; save(role, c); }));
  $$('[data-rm]').forEach((b) => (b.onclick = () => { const [role, i] = b.dataset.rm.split(':'); const c = chainOf(role); c.splice(+i, 1); save(role, c); }));
  $$('[data-prompt]').forEach((b) => (b.onclick = () => {
    const a = agents.find((x) => x.role === b.dataset.prompt);
    const m = modal(`<div class="modal-h"><h2>${names[a.role]} — instructions</h2><button class="ghost icon right" data-close>${ic('x')}</button></div>
      <div class="modal-b"><p class="help" style="margin:0">Replaces the built-in instructions. Protocol parts (JSON schema, tool rules, output language, untrusted-data note) are always appended. Leave empty to use the built-in prompt.</p>
        <textarea id="ag-p" rows="16" class="mono">${esc(a.prompt || a.builtin)}</textarea></div>
      <div class="modal-f"><button id="ag-reset">Reset to built-in</button><span class="right"></span><button data-close>Cancel</button><button class="primary" id="ag-save">Save</button></div>`, 'wide');
    $('#ag-reset').onclick = async () => { await act(() => api('PUT', '/agents/' + a.role, { prompt: '' }), 'Reset'); m.close(); ROUTES.agents(); };
    $('#ag-save').onclick = async () => {
      const v = $('#ag-p').value.trim();
      await act(() => api('PUT', '/agents/' + a.role, { prompt: v === a.builtin.trim() ? '' : v }), 'Saved'); m.close(); ROUTES.agents();
    };
  }));
};

// ---------------------------------------------------------------- skills
ROUTES.skills = async () => {
  S.view = 'skills';
  await loadConfig(); const skills = S.cfg.skills;
  $('#app').innerHTML = pageHead('Skills', 'Reusable playbooks. Name + description are always visible to agents; the content is injected only when a skill is always-on, attached to the project, matches a keyword — or loaded with load_skill. A big library costs almost nothing.',
    `<button class="primary" id="sk-add">${ic('plus')} New skill</button>`) +
    `<div class="table-wrap"><table class="t"><thead><tr><th>On</th><th>Name</th><th>Description</th><th>Keywords</th><th>Always</th><th></th></tr></thead><tbody>
    ${skills.map((s, i) => `<tr><td>${sw('ske' + i, s.enabled)}</td><td><b class="mono">${esc(s.name)}</b></td><td class="small">${esc(s.description)}</td>
      <td><div class="chips">${(s.keywords || []).map((k) => `<span class="chip">${esc(k)}</span>`).join('')}</div></td><td>${s.always ? '<span class="badge ok">always</span>' : ''}</td>
      <td><div class="acts"><button data-ed="${i}">${ic('edit')}</button><button class="del" data-del="${i}">${ic('trash')}</button></div></td></tr>`).join('')}</tbody></table>
    ${skills.length ? '' : empty('sparkles', 'No skills')}</div>`;
  const save = async (list) => { const r = await act(() => api('PUT', '/skills', list), 'Skills saved'); if (r) { S.cfg = r; ROUTES.skills(); } };
  skills.forEach((s, i) => ($('#ske' + i).onchange = (e) => save(skills.map((x, j) => (j === i ? { ...x, enabled: e.target.checked } : x)))));
  $$('[data-del]').forEach((b) => (b.onclick = () => confirm('Delete skill?') && save(skills.filter((_, j) => j !== +b.dataset.del))));
  const edit = (i) => {
    const s = i >= 0 ? skills[i] : { name: '', description: '', content: '', keywords: [], always: false, enabled: true };
    const m = modal(`<div class="modal-h"><h2>${i >= 0 ? 'Edit skill' : 'New skill'}</h2><button class="ghost icon right" data-close>${ic('x')}</button></div>
      <div class="modal-b"><div class="grid2"><label class="f">Name<input id="s-name" class="mono" value="${esc(s.name)}" placeholder="react-forms"></label>
        <label class="f">Keywords <span class="hint">comma separated — content is injected when the task mentions one</span><input id="s-kw" value="${esc((s.keywords || []).join(', '))}"></label></div>
        <label class="f">Description<input id="s-desc" value="${esc(s.description)}"></label>
        <label class="f">Content (Markdown)<textarea id="s-content" rows="12" class="mono">${esc(s.content)}</textarea></label>
        <div class="row"><label class="chk">${sw('s-always', s.always)} Always on</label><label class="chk">${sw('s-en', s.enabled)} Enabled</label></div></div>
      <div class="modal-f"><button data-close>Cancel</button><button class="primary" id="s-save">Save</button></div>`, 'wide');
    $('#s-save').onclick = () => {
      const n = { ...s, name: $('#s-name').value, description: $('#s-desc').value, content: $('#s-content').value, always: $('#s-always').checked, enabled: $('#s-en').checked,
        keywords: $('#s-kw').value.split(',').map((x) => x.trim()).filter(Boolean) };
      m.close(); save(i >= 0 ? skills.map((x, j) => (j === i ? n : x)) : [...skills, n]);
    };
  };
  $('#sk-add').onclick = () => edit(-1);
  $$('[data-ed]').forEach((b) => (b.onclick = () => edit(+b.dataset.ed)));
};

// ---------------------------------------------------------------- command rules
ROUTES.rules = async () => {
  S.view = 'rules';
  await loadConfig(); const rules = S.cfg.rules; const st = S.cfg.settings;
  const rowH = (r) => `<tr><td>${sw('', r.enabled)}</td><td><select data-k="kind">${['argv', 'prefix', 'regex', 'contains'].map((k) => `<option ${r.kind === k ? 'selected' : ''}>${k}</option>`).join('')}</select></td>
    <td><input data-k="pattern" class="mono" value="${esc(r.pattern)}" placeholder="git push"></td>
    <td><select data-k="action">${['allow', 'ask', 'deny'].map((k) => `<option ${r.action === k ? 'selected' : ''}>${k}</option>`).join('')}</select></td>
    <td><input data-k="note" value="${esc(r.note || '')}"></td><td class="num small">${r.hits || 0}</td><td><button class="ghost icon" data-del>${ic('x')}</button></td></tr>`;
  $('#app').innerHTML = pageHead('Command rules', `Decide shell commands before the fallback policy (<b>${esc(st.command_policy)}</b>, set in Settings). First matching rule wins.`,
    `<a class="btn" href="#/approvals">${ic('shieldcheck')} Approval records</a>`) +
    `<div class="table-wrap"><table class="t"><thead><tr><th style="width:50px">On</th><th style="width:120px">Match</th><th>Pattern</th><th style="width:110px">Action</th><th>Note</th><th class="num">Hits</th><th></th></tr></thead>
    <tbody id="rules">${rules.map(rowH).join('')}</tbody></table>
    <div class="foot"><button class="sm" id="r-add">${ic('plus')} Add rule</button><button class="primary right" id="r-save">Save rules</button></div></div>
    <p class="help" style="margin-top:14px"><b>argv</b>: exact leading tokens (<code>git push</code> matches <code>git push --force</code>). <b>prefix</b>: string prefix. <b>regex</b>: Go regular expression on the whole command. <b>contains</b>: substring.
      <b>allow</b> rules only match a single simple command (no <code>; &amp;&amp; | $( ) &gt;</code>); <b>deny</b> and <b>ask</b> rules match any segment of a chained command. Project commands always run without approval.</p>`;
  const wire = () => $$('#rules [data-del]').forEach((b) => (b.onclick = () => b.closest('tr').remove()));
  wire();
  $('#r-add').onclick = () => { $('#rules').insertAdjacentHTML('beforeend', rowH({ kind: 'argv', pattern: '', action: 'ask', enabled: true })); wire(); };
  $('#r-save').onclick = async () => {
    const list = $$('#rules tr').map((tr, i) => ({ ...(rules[i] || {}), enabled: $('input[type=checkbox]', tr).checked,
      kind: $('[data-k=kind]', tr).value, pattern: $('[data-k=pattern]', tr).value.trim(), action: $('[data-k=action]', tr).value, note: $('[data-k=note]', tr).value }));
    const r = await act(() => api('PUT', '/rules', list.filter((x) => x.pattern)), 'Rules saved'); if (r) { S.cfg = r; ROUTES.rules(); }
  };
};

// ---------------------------------------------------------------- templates
ROUTES.templates = async () => {
  S.view = 'templates';
  await Promise.all([loadConfig(), loadProjects()]); const tpls = S.cfg.templates;
  $('#app').innerHTML = pageHead('Templates', 'Pre-filled tasks for recurring requests (save one from any task with the layers icon). A template with steps skips planning.',
    `<button class="primary" id="tp-add">${ic('plus')} New template</button>`) +
    `<div class="table-wrap"><table class="t"><thead><tr><th>Name</th><th>Title</th><th>Project</th><th>Steps</th><th>Priority</th><th></th></tr></thead><tbody>
    ${tpls.map((t, i) => `<tr><td><b>${esc(t.name)}</b></td><td class="small">${esc(t.title)}</td><td class="small">${esc((S.projects.find((p) => p.id === t.project_id) || {}).name || '—')}</td>
      <td class="small">${(t.steps || []).length}</td><td>${esc(t.priority || 'normal')}</td>
      <td><div class="acts"><button title="Use" data-use="${i}">${ic('play')}</button><button data-ed="${i}">${ic('edit')}</button><button class="del" data-del="${i}">${ic('trash')}</button></div></td></tr>`).join('')}</tbody></table>
    ${tpls.length ? '' : empty('copy', 'No templates yet')}</div>`;
  const save = async (list) => { const r = await act(() => api('PUT', '/templates', list), 'Templates saved'); if (r) { S.cfg = r; ROUTES.templates(); } };
  $$('[data-del]').forEach((b) => (b.onclick = () => confirm('Delete template?') && save(tpls.filter((_, j) => j !== +b.dataset.del))));
  $$('[data-use]').forEach((b) => (b.onclick = async () => { await openCapture(); const s = $('#c-tpl'); if (s) { s.value = tpls[+b.dataset.use].id; s.onchange({ target: s }); } }));
  const edit = (i) => {
    const t = i >= 0 ? tpls[i] : { name: '', title: '', message: '', priority: 'normal', tags: [], project_id: '', steps: [] };
    const m = modal(`<div class="modal-h"><h2>${i >= 0 ? 'Edit template' : 'New template'}</h2><button class="ghost icon right" data-close>${ic('x')}</button></div>
      <div class="modal-b"><div class="grid2"><label class="f">Name<input id="tp-name" value="${esc(t.name)}"></label><label class="f">Task title<input id="tp-title" value="${esc(t.title)}"></label></div>
        <label class="f">Message<textarea id="tp-msg" rows="4">${esc(t.message)}</textarea></label>
        <div class="grid3"><label class="f">Project<select id="tp-proj">${projectOptions(t.project_id)}</select></label>
          <label class="f">Priority<select id="tp-prio">${['low', 'normal', 'high', 'urgent'].map((p) => `<option ${t.priority === p ? 'selected' : ''}>${p}</option>`).join('')}</select></label>
          <label class="f">Tags<input id="tp-tags" value="${esc((t.tags || []).join(', '))}"></label></div>
        <label class="f">Fixed steps <span class="hint">one per line (optional)</span><textarea id="tp-steps" rows="5">${esc((t.steps || []).join('\n'))}</textarea></label></div>
      <div class="modal-f"><button data-close>Cancel</button><button class="primary" id="tp-save">Save</button></div>`);
    $('#tp-save').onclick = () => {
      const n = { ...t, name: $('#tp-name').value, title: $('#tp-title').value, message: $('#tp-msg').value, project_id: $('#tp-proj').value, priority: $('#tp-prio').value,
        tags: $('#tp-tags').value.split(',').map((x) => x.trim()).filter(Boolean), steps: $('#tp-steps').value.split('\n').map((x) => x.trim()).filter(Boolean) };
      m.close(); save(i >= 0 ? tpls.map((x, j) => (j === i ? n : x)) : [...tpls, n]);
    };
  };
  $('#tp-add').onclick = () => edit(-1);
  $$('[data-ed]').forEach((b) => (b.onclick = () => edit(+b.dataset.ed)));
};

// ---------------------------------------------------------------- notifications
const NOTIFY_EVENTS = ['approval', 'question', 'review', 'done', 'error', 'paused'];
ROUTES.notifications = async () => {
  S.view = 'notifications';
  await loadConfig(); const hooks = S.cfg.webhooks;
  $('#app').innerHTML = pageHead('Notifications', 'Post to Slack, Discord, Microsoft Teams or any webhook when a task needs you.', `<button class="primary" id="wh-add">${ic('plus')} Add webhook</button>`) +
    `<div class="cards">${hooks.map((h, i) => `<div class="card pcard"><div class="row nowrap">${ic('bell')}<span class="name grow ellipsis">${esc(h.name)}</span><span class="badge outline">${esc(h.format)}</span>
      ${h.enabled ? '<span class="badge ok">on</span>' : '<span class="badge">off</span>'}</div><div class="sub ellipsis mono">${esc(h.url)}</div>
      <div class="chips">${(h.events || []).map((e) => `<span class="chip">${e}</span>`).join('')}</div>
      <div class="row"><button class="sm" data-test="${i}">Send test</button><button class="sm" data-ed="${i}">${ic('edit')} Edit</button><button class="sm danger" data-del="${i}">${ic('trash')}</button></div></div>`).join('')}</div>
    ${hooks.length ? '' : `<div class="card">${empty('bell', 'No webhooks')}</div>`}
    <p class="help" style="margin-top:14px">Links in messages use the <b>Public URL</b> from Settings (${esc(S.cfg.settings.public_url)}).</p>`;
  const save = async (list) => { const r = await act(() => api('PUT', '/webhooks', list), 'Saved'); if (r) { S.cfg = r; ROUTES.notifications(); } };
  $$('[data-del]').forEach((b) => (b.onclick = () => confirm('Delete webhook?') && save(hooks.filter((_, j) => j !== +b.dataset.del))));
  $$('[data-test]').forEach((b) => (b.onclick = async () => { const r = await act(() => api('POST', '/webhooks/test', hooks[+b.dataset.test])); if (r) r.ok ? toast('Test sent') : toast(r.error, true); }));
  const edit = (i) => {
    const h = i >= 0 ? hooks[i] : { name: '', url: '', format: 'slack', events: ['approval', 'question', 'review', 'error'], enabled: true };
    const m = modal(`<div class="modal-h"><h2>${i >= 0 ? 'Edit webhook' : 'Add webhook'}</h2><button class="ghost icon right" data-close>${ic('x')}</button></div>
      <div class="modal-b"><div class="grid2"><label class="f">Name<input id="wh-name" value="${esc(h.name)}" placeholder="Team channel"></label>
        <label class="f">Format<select id="wh-fmt">${['slack', 'discord', 'teams', 'generic'].map((f) => `<option ${h.format === f ? 'selected' : ''}>${f}</option>`).join('')}</select></label></div>
        <label class="f">Webhook URL<input id="wh-url" value="${esc(h.url)}" placeholder="https://hooks.slack.com/services/…"></label>
        <div class="section-title">Events</div><div class="row">${NOTIFY_EVENTS.map((e) => `<label class="chk"><input type="checkbox" data-ev="${e}" ${(h.events || []).includes(e) ? 'checked' : ''}> ${e}</label>`).join('')}</div>
        <label class="chk">${sw('wh-en', h.enabled)} Enabled</label></div>
      <div class="modal-f"><button data-close>Cancel</button><button class="primary" id="wh-save">Save</button></div>`, 'narrow');
    $('#wh-save').onclick = () => {
      const n = { ...h, name: $('#wh-name').value, format: $('#wh-fmt').value, url: $('#wh-url').value.trim(), enabled: $('#wh-en').checked,
        events: $$('[data-ev]').filter((c) => c.checked).map((c) => c.dataset.ev) };
      m.close(); save(i >= 0 ? hooks.map((x, j) => (j === i ? n : x)) : [...hooks, n]);
    };
  };
  $('#wh-add').onclick = () => edit(-1);
  $$('[data-ed]').forEach((b) => (b.onclick = () => edit(+b.dataset.ed)));
};

// ---------------------------------------------------------------- logs
ROUTES.logs = async () => {
  S.view = 'logs';
  let level = '';
  $('#app').innerHTML = pageHead('Logs', 'Server log (last 2000 lines): captures, stage changes, failovers, notifications, errors.', `<div class="seg" id="lv">${['', 'info', 'warn', 'error'].map((l) => `<button data-l="${l}">${l || 'all'}</button>`).join('')}</div><button id="lg-r">${ic('refresh')} Refresh</button>`) +
    '<div class="card" style="padding:0"><div class="code" id="lg" style="border:none;max-height:72vh"></div></div>';
  const draw = async () => {
    const lines = (await act(() => api('GET', '/logs'))) || [];
    $$('#lv button').forEach((b) => b.classList.toggle('on', b.dataset.l === level));
    const el = $('#lg');
    el.innerHTML = lines.filter((l) => !level || l.level === level).map((l) => `<span style="${l.level === 'error' ? 'color:var(--bad)' : l.level === 'warn' ? 'color:var(--warn)' : ''}">${esc(l.text)}</span>`).join('') || '<span class="faint">no log lines</span>';
    el.scrollTop = el.scrollHeight;
  };
  $$('#lv button').forEach((b) => (b.onclick = () => { level = b.dataset.l; draw(); }));
  $('#lg-r').onclick = draw;
  S.onEvent = debounce(() => { if (S.view === 'logs') draw(); }, 1500);
  await draw();
};

// ---------------------------------------------------------------- settings
ROUTES.settings = async () => {
  S.view = 'settings';
  await loadConfig(); const c = S.cfg; const s = c.settings;
  const num = (id, label, v, hint = '') => `<label class="f">${label}${hint ? ` <span class="hint">${hint}</span>` : ''}<input id="${id}" type="number" value="${v}"></label>`;
  $('#app').innerHTML = pageHead('Settings', `Data directory: <code>${esc(c.data_dir)}</code>`, '<button class="primary" id="s-save">Save settings</button>') + `<div class="stack" style="max-width:980px">
    <div class="card"><div class="card-h"><h3>Agent behaviour</h3></div><div class="grid3">
      <label class="f">Language for AI output<input id="s-lang" value="${esc(s.language)}"></label>
      ${num('s-turns', 'Max tool turns per step', s.max_turns_per_step)}
      ${num('s-conc', 'Concurrent tasks', s.max_concurrent_tasks, 'parallel provider use')}</div>
      <div class="row" style="gap:20px;margin-top:12px"><label class="chk">${sw('s-aa', s.auto_analyze)} Analyze right after capture</label>
        <label class="chk">${sw('s-ar', s.auto_run)} Start work right after analysis (skip plan approval)</label>
        <label class="chk">${sw('s-ap', s.auto_promote_memory)} Promote task memory to project knowledge when done</label></div></div>
    <div class="card"><div class="card-h"><h3>Token budget &amp; context</h3></div><div class="grid3">
      ${num('s-budget', 'Token budget per task', s.task_token_budget, '0 = unlimited')}
      ${num('s-tbudget', 'Worker turn budget per task', s.task_turn_budget, '0 = unlimited')}
      ${num('s-ctx', 'Code context for analysis (tokens)', s.code_context_tokens)}
      ${num('s-compact', 'Compact step context at % of window', s.compact_at_pct)}
      ${num('s-tout', 'Max chars per tool result', s.tool_output_max_chars)}</div>
      <div class="row" style="gap:20px;margin-top:12px"><label class="chk">${sw('s-rec', s.record_llm)} Record LLM calls (LLM Records page)</label>
        <label class="chk">${sw('s-ub', s.untrusted_boundary)} Untrusted-output boundary (frame tool output as data)</label></div></div>
    <div class="card"><div class="card-h"><h3>Shell commands</h3><a class="btn sm right" href="#/rules">Command rules</a></div>
      <div class="grid2"><label class="f">Fallback policy (when no rule matches)<select id="s-policy">
        <option value="off">off — never run shell commands</option><option value="ask">ask — commands outside the allowlist need approval</option>
        <option value="allowlist">allowlist — only allowlisted commands</option><option value="auto">auto — run everything (careful)</option></select></label>
        ${num('s-ctime', 'Command timeout (s)', s.command_timeout_sec)}</div>
      <label class="f" style="margin-top:10px">Allowlist (one command prefix per line)<textarea id="s-allow" rows="5" class="mono">${esc((s.command_allowlist || []).join('\n'))}</textarea></label></div>
    <div class="card"><div class="card-h"><h3>Provider pool</h3></div><div class="grid3">
      ${num('s-retry', 'Retries per provider (transient errors)', s.retry_attempts)}
      ${num('s-cool', 'Breaker cooldown (s)', s.breaker_cooldown_sec, '0 = 1 → 5 → 30 min ladder')}</div></div>
    <div class="card"><div class="card-h"><h3>Web tools</h3></div><div class="grid3">
      <label class="f">Web search<select id="s-ws"><option value="">off</option><option value="tavily">Tavily</option><option value="brave">Brave Search</option></select></label>
      <label class="f">Search API key <span class="hint">literal or env:NAME · blank = keep</span><input id="s-wsk" type="password" placeholder="${esc(s.web_search_key || '')}"></label>
      <label class="chk" style="margin-top:20px">${sw('s-wf', s.web_fetch)} Allow web_fetch (public URLs only)</label></div></div>
    <div class="card"><div class="card-h"><h3>Notifications</h3></div>
      <label class="f">Public URL of this app <span class="hint">used for links in webhook messages</span><input id="s-url" value="${esc(s.public_url)}"></label></div></div>`;
  $('#s-policy').value = s.command_policy; $('#s-ws').value = s.web_search;
  $('#s-save').onclick = async () => {
    const n = (id) => parseInt($(id).value, 10) || 0;
    const body = { language: $('#s-lang').value, max_turns_per_step: n('#s-turns'), max_concurrent_tasks: n('#s-conc'), auto_analyze: $('#s-aa').checked, auto_run: $('#s-ar').checked,
      auto_promote_memory: $('#s-ap').checked, task_token_budget: n('#s-budget'), task_turn_budget: n('#s-tbudget'), code_context_tokens: n('#s-ctx'), compact_at_pct: n('#s-compact'),
      tool_output_max_chars: n('#s-tout'), record_llm: $('#s-rec').checked, untrusted_boundary: $('#s-ub').checked, command_policy: $('#s-policy').value,
      command_timeout_sec: n('#s-ctime'), command_allowlist: $('#s-allow').value.split('\n').map((x) => x.trim()).filter(Boolean), retry_attempts: n('#s-retry'),
      breaker_cooldown_sec: n('#s-cool'), web_search: $('#s-ws').value, web_search_key: $('#s-wsk').value, web_fetch: $('#s-wf').checked, public_url: $('#s-url').value };
    const r = await act(() => api('PUT', '/settings', body), 'Settings saved'); if (r) S.cfg = r;
  };
};
