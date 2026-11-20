/* AATMT — MCP servers: external tools for the agents. */
'use strict';

const kvText = (m) => Object.entries(m || {}).map(([k, v]) => `${k}=${v}`).join('\n');
function kvParse(text) {
  const o = {};
  text.split('\n').forEach((l) => { const i = l.indexOf('='); if (i > 0) o[l.slice(0, i).trim()] = l.slice(i + 1).trim(); });
  return o;
}
const mcpPlain = (m) => ({ id: m.id, name: m.name, transport: m.transport, url: m.url, headers: m.headers, command: m.command, args: m.args,
  env: m.env, roles: m.roles, tools: m.tools, approval: m.approval, timeout_sec: m.timeout_sec, enabled: m.enabled });

ROUTES.mcp = async () => {
  S.view = 'mcp';
  const list = await act(() => api('GET', '/mcp')); if (!list) return;
  $('#app').innerHTML = pageHead('MCP Servers', 'Connect Model Context Protocol servers (issue trackers, docs, databases, browsers, internal services). Their tools are offered to the agents as <code>mcp__server__tool</code>.',
    `<button class="primary" id="mcp-add">${ic('plus')} Add server</button>`) +
    `<div class="cards">${list.map(mcpCard).join('')}</div>${list.length ? '' : `<div class="card">${empty('plug', 'No MCP servers yet')}</div>`}
    <p class="help" style="margin-top:16px">${ic('zap')} Transports: <b>Streamable HTTP</b> (current spec, e.g. <code>https://mcp.company.local/mcp</code>), <b>SSE</b> (legacy servers exposing <code>/sse</code>)
      and <b>stdio</b> (a local process such as <code>npx -y @modelcontextprotocol/server-github</code>). Header and env values accept <code>env:NAME</code> so secrets stay out of the config.
      Every tool schema costs prompt tokens on each worker turn — use the tool allowlist to expose only what the agents need.</p>`;
  const save = async (next) => { const r = await act(() => api('PUT', '/mcp', next), 'MCP servers saved'); if (r) ROUTES.mcp(); return r; };
  $('#mcp-add').onclick = () => editMCP(list, -1, save);
  $$('[data-ed]').forEach((b) => (b.onclick = () => editMCP(list, +b.dataset.ed, save)));
  $$('[data-del]').forEach((b) => (b.onclick = () => confirm('Remove MCP server?') && save(list.filter((_, j) => j !== +b.dataset.del).map(mcpPlain))));
  $$('[data-on]').forEach((c) => (c.onchange = () => save(list.map((m, j) => ({ ...mcpPlain(m), enabled: j === +c.dataset.on ? c.checked : m.enabled })))));
  $$('[data-test]').forEach((b) => (b.onclick = async () => {
    b.innerHTML = '<span class="spin"></span> Connecting…';
    const r = await act(() => api('POST', `/mcp/${list[+b.dataset.test].id}/test`));
    if (r) r.connected ? toast(`Connected — ${r.tools.length} tools`) : toast(r.error, true);
    ROUTES.mcp();
  }));
};

function mcpCard(m, i) {
  const st = m.status;
  const state = !m.enabled ? '<span class="badge">off</span>' : !st ? '<span class="badge outline">not connected yet</span>'
    : st.connected ? '<span class="badge ok dot">connected</span>' : `<span class="badge err dot" title="${esc(st.error)}">error</span>`;
  const target = m.transport === 'stdio' ? [m.command, ...m.args].join(' ') : m.url;
  const label = { http: 'STREAMABLE HTTP', sse: 'SSE', stdio: 'STDIO' }[m.transport] || m.transport;
  return `<div class="card pcard"><div class="row nowrap">${ic('plug')}<span class="name ellipsis grow">${esc(m.name)}</span><span class="badge outline">${label}</span>${state}</div>
    <div class="sub mono ellipsis" title="${esc(target)}">${esc(target)}</div>
    ${st && st.server ? `<div class="small muted">${esc(st.server)}</div>` : ''}
    ${st && st.error ? `<div class="small" style="color:var(--bad)">${esc(st.error)}</div>` : ''}
    <div class="chips">${m.roles.map((r) => `<span class="chip">${esc(r)}</span>`).join('') || '<span class="chip">no agent</span>'}
      ${m.approval ? '<span class="badge paused">approval</span>' : ''}
      ${st ? `<span class="badge planned">${m.tools.length ? m.tools.length + ' of ' : ''}${st.tools.length} tools</span>` : ''}</div>
    ${st && st.tools.length ? `<div class="chips">${st.tools.slice(0, 12).map((t) => `<span class="chip" title="${esc(t.description || '')}" style="${m.tools.length && !m.tools.includes(t.name) ? 'opacity:.4' : ''}">${esc(t.name)}</span>`).join('')}${st.tools.length > 12 ? `<span class="chip">+${st.tools.length - 12}</span>` : ''}</div>` : ''}
    <div class="row" style="margin-top:4px"><button class="sm" data-test="${i}">${ic('refresh')} Test / reconnect</button>
      <button class="sm" data-ed="${i}">${ic('edit')} Edit</button><button class="sm danger" data-del="${i}">${ic('trash')}</button>
      <label class="switch right" title="enabled"><input type="checkbox" data-on="${i}" ${m.enabled ? 'checked' : ''}><span></span></label></div></div>`;
}

function editMCP(list, i, save) {
  const m = i >= 0 ? list[i] : { name: '', transport: 'http', url: '', headers: {}, command: '', args: [], env: {}, roles: ['worker'], tools: [], approval: false, timeout_sec: 0, enabled: true };
  const known = (m.status && m.status.tools) || [];
  const mm = modal(`<div class="modal-h"><h2>${i >= 0 ? 'Edit MCP server' : 'Add MCP server'}</h2><button class="ghost icon right" data-close>${ic('x')}</button></div>
    <div class="modal-b">
      <div class="grid2"><label class="f">Name <span class="hint">tool prefix: mcp__name__tool</span><input id="mc-name" class="mono" value="${esc(m.name)}" placeholder="jira"></label>
        <label class="f">Transport<div class="seg" id="mc-tr">${[['http', 'Streamable HTTP'], ['sse', 'SSE (legacy)'], ['stdio', 'stdio']].map(([k, l]) => `<button type="button" data-k="${k}">${l}</button>`).join('')}</div></label></div>
      <div id="mc-net" class="col"><label class="f">URL<input id="mc-url" class="mono" value="${esc(m.url || '')}" placeholder="https://mcp.example.com/mcp"></label>
        <label class="f">Headers <span class="hint">one per line — Key=Value, e.g. Authorization=Bearer … or Authorization=env:MCP_TOKEN</span><textarea id="mc-headers" rows="2" class="mono">${esc(kvText(m.headers))}</textarea></label></div>
      <div id="mc-proc" class="col"><div class="grid2"><label class="f">Command<input id="mc-cmd" class="mono" value="${esc(m.command || '')}" placeholder="npx"></label>
        <label class="f">Arguments <span class="hint">one per line</span><textarea id="mc-args" rows="2" class="mono">${esc((m.args || []).join('\n'))}</textarea></label></div>
        <label class="f">Environment <span class="hint">KEY=value or KEY=env:NAME</span><textarea id="mc-env" rows="2" class="mono">${esc(kvText(m.env))}</textarea></label></div>
      <div class="row" style="gap:18px"><span class="section-title" style="margin:0">Agents</span>
        <label class="chk"><input type="checkbox" id="mc-r-worker" ${m.roles.includes('worker') ? 'checked' : ''}> Work agent</label>
        <label class="chk"><input type="checkbox" id="mc-r-chat" ${m.roles.includes('chat') ? 'checked' : ''}> Chat assistant</label></div>
      <div class="row" style="gap:18px;align-items:flex-end"><label class="chk">${sw('mc-appr', m.approval)} Ask me before every call</label><label class="chk">${sw('mc-en', m.enabled)} Enabled</label>
        <label class="f" style="width:150px">Timeout (s)<input id="mc-to" type="number" value="${m.timeout_sec || ''}" placeholder="default: ${S.cfg ? S.cfg.settings.limits.mcp_call_timeout_sec : ''}"></label></div>
      ${known.length ? `<div><div class="section-title">Tool allowlist <span class="faint">(none checked = all ${known.length} tools)</span></div>
        <div class="col" style="max-height:220px;overflow:auto;gap:4px">${known.map((t) => `<label class="chk small"><input type="checkbox" data-tool="${esc(t.name)}" ${m.tools.includes(t.name) ? 'checked' : ''}>
          <b class="mono">${esc(t.name)}</b><span class="muted ellipsis">${esc(t.description || '')}</span></label>`).join('')}</div></div>`
        : '<p class="help" style="margin:0">Save, then press <b>Test / reconnect</b> to load the tool list and pick an allowlist.</p>'}
    </div>
    <div class="modal-f"><button data-close>Cancel</button><button class="primary" id="mc-save">Save</button></div>`);
  let tr = m.transport;
  const setTr = (k) => {
    tr = k;
    $$('#mc-tr button').forEach((b) => b.classList.toggle('on', b.dataset.k === k));
    $('#mc-net').classList.toggle('hidden', k === 'stdio');
    $('#mc-proc').classList.toggle('hidden', k !== 'stdio');
  };
  $$('#mc-tr button').forEach((b) => (b.onclick = () => setTr(b.dataset.k)));
  setTr(tr);
  $('#mc-save').onclick = async () => {
    const n = { ...(i >= 0 ? mcpPlain(m) : {}), name: $('#mc-name').value.trim(), transport: tr, url: $('#mc-url').value.trim(), headers: kvParse($('#mc-headers').value),
      command: $('#mc-cmd').value.trim(), args: $('#mc-args').value.split('\n').map((x) => x.trim()).filter(Boolean), env: kvParse($('#mc-env').value),
      roles: ['worker', 'chat'].filter((r) => $('#mc-r-' + r).checked), approval: $('#mc-appr').checked, enabled: $('#mc-en').checked,
      timeout_sec: parseInt($('#mc-to').value, 10) || 0, tools: $$('[data-tool]').filter((c) => c.checked).map((c) => c.dataset.tool) };
    const next = i >= 0 ? list.map((x, j) => (j === i ? n : mcpPlain(x))) : [...list.map(mcpPlain), n];
    if (await save(next)) mm.close();
  };
}
