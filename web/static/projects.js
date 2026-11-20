/* AATMT — projects: one codebase, many tasks, shared knowledge. */
'use strict';

const P = { archived: false, proj: null, git: null, tab: 'overview', tasks: [], files: null, file: null, taskFilter: 'all' };

const srcChip = (s) => {
  if (!s || s.kind === 'none') return '<span class="chip">no source</span>';
  const label = s.kind === 'git' ? s.url : s.kind === 'upload' ? s.path : s.path;
  return `<span class="chip" title="${esc(label)}">${ic(s.kind === 'git' ? 'git' : 'folder')} ${esc(s.kind)}</span>`;
};
const stageBar = (stages, total) => (total ? `<div class="stages">${STAGES.filter((s) => stages[s]).map((s) => `<i title="${STAGE_LABEL[s]}: ${stages[s]}" style="width:${(stages[s] / total) * 100}%;background:${STAGE_COLOR[s]}"></i>`).join('')}</div>` : '<div class="stages"></div>');

ROUTES.projects = async () => {
  S.view = 'projects';
  $('#app').innerHTML = `<div class="page-head"><div><h1>Projects</h1><p>A project is a codebase. Many tasks share it — and its knowledge base, commands and history.</p></div>
    <div class="right row"><div class="seg" id="parch"><button data-a="0">Active</button><button data-a="1">Archived</button></div>
    <button class="primary" id="np-btn">${ic('plus')} New project</button></div></div><div id="plist"></div>`;
  $$('#parch button').forEach((b) => (b.onclick = () => { P.archived = b.dataset.a === '1'; drawProjects(); }));
  $('#np-btn').onclick = newProjectModal;
  S.onEvent = (ev) => { if (ev.type === 'project' || ev.type === 'stage' || ev.type === 'task') debounceProjects(); };
  await loadProjects(); drawProjects();
};
const debounceProjects = debounce(async () => { if (S.view === 'projects') { await loadProjects(); drawProjects(); } }, 500);
function drawProjects() {
  $$('#parch button').forEach((b) => b.classList.toggle('on', (b.dataset.a === '1') === P.archived));
  const list = S.projects.filter((p) => !!p.archived === P.archived);
  $('#plist').innerHTML = list.length ? `<div class="cards">${list.map((p) => `
    <div class="card pcard click" data-id="${esc(p.id)}">
      <div class="row nowrap"><span class="name ellipsis grow">${esc(p.name)}</span>${srcChip(p.source)}</div>
      <div class="sub mono ellipsis">${esc(p.workspace || '—')}</div>
      ${p.description ? `<div class="small muted" style="display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden">${esc(p.description)}</div>` : ''}
      ${stageBar(p.stages, p.tasks)}
      <div class="row small muted"><span><b style="color:var(--text)">${p.tasks}</b> tasks</span><span>${p.active} active</span><span>${p.done} done</span>
        <span>${p.knowledge.length} notes</span><span class="right">${fmtK(usageTotal(p.usage))} tok · ${ago(p.last_task || p.updated_at)}</span></div>
    </div>`).join('')}</div>` : `<div class="card">${empty('folder', P.archived ? 'No archived projects' : 'No projects yet. Create one, or attach source code when capturing a task.')}</div>`;
  $$('.pcard[data-id]').forEach((c) => (c.onclick = () => (location.hash = '#/project/' + c.dataset.id)));
}
function newProjectModal() {
  const m = modal(`<div class="modal-h"><h2>New project</h2><button class="ghost icon right" data-close>${ic('x')}</button></div>
    <div class="modal-b"><label class="f">Name<input id="np-name" placeholder="defaults to the folder / repo name"></label>
      <label class="f">Description <span class="hint">what the codebase is, stack, conventions — injected into every prompt</span><textarea id="np-desc" rows="3"></textarea></label>
      <div class="col"><span class="section-title">Source code</span>${sourceFormHTML('nps')}</div></div>
    <div class="modal-f"><button data-close>Cancel</button><button class="primary" id="np-create">Create project</button></div>`, 'narrow');
  wireSourceForm('nps', 'path');
  $('#np-create').onclick = async (e) => {
    const fd = sourceFormData('nps'); fd.append('name', $('#np-name').value); fd.append('description', $('#np-desc').value);
    e.currentTarget.disabled = true; e.currentTarget.innerHTML = '<span class="spin"></span> Creating…';
    const r = await act(() => api('POST', '/projects', fd));
    if (r) { m.close(); localStorage.setItem('lastProject', r.project.id); await loadProjects(); location.hash = '#/project/' + r.project.id; } else { e.currentTarget.disabled = false; e.currentTarget.textContent = 'Create project'; }
  };
}

// ---------------------------------------------------------------- project detail
ROUTES.project = async ([id, tab]) => {
  S.view = 'project';
  if (!P.proj || P.proj.id !== id) { P.files = null; P.file = null; }
  P.tab = tab || 'overview';
  $('#app').innerHTML = `<div id="p-head"></div><div class="tabs" id="p-tabs"></div><div id="p-body"></div>`;
  const ok = await loadProject(id);
  if (S.view !== 'project' || !$('#p-head')) return; // navigated away while loading
  if (!ok) { $('#app').innerHTML = empty('folder', 'Project not found'); return; }
  S.onEvent = (ev) => {
    const pid = ev.data && ev.data.project_id;
    if ((ev.type === 'project' && pid === id) || ev.type === 'stage' || ev.type === 'task') refreshProject();
  };
  drawProject();
};
async function loadProject(id) {
  const [r, tasks] = await Promise.all([act(() => api('GET', '/projects/' + id)), act(() => api('GET', `/tasks?project=${id}&archived=all`))]);
  if (!r) return false;
  P.proj = r.project; P.git = r.git; P.tasks = tasks || [];
  return true;
}
const refreshProject = debounce(async () => {
  if (S.view !== 'project' || !P.proj) return;
  if (!(await loadProject(P.proj.id))) return;
  const body = $('#p-body');
  if (body && body.contains(document.activeElement) && /INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName)) { drawProjectHead(); return; }
  drawProject();
}, 400);
function drawProject() { if (!$('#p-head')) return; drawProjectHead(); drawProjectTabs(); drawProjectBody(); }
function drawProjectHead() {
  const p = P.proj, g = P.git;
  $('#p-head').innerHTML = `<div class="t-head"><a class="btn ghost icon" href="#/projects">${ic('back')}</a>
      <h1 class="grow">${esc(p.name)}</h1>
      <button class="primary" id="p-newtask">${ic('plus')} New task</button>
      ${g ? `<button id="p-sync">${ic('refresh')} Pull latest</button>` : ''}
      <button class="ghost icon" id="p-arch" title="${p.archived ? 'Restore' : 'Archive'}">${ic('archive')}</button>
      <button class="ghost icon danger" id="p-del" title="Delete project">${ic('trash')}</button></div>
    <div class="t-sub">${srcChip(p.source)}<span class="mono">${esc(p.workspace || 'no workspace')}</span>
      ${g ? `<span class="chip">${ic('git')} ${esc(g.branch)}</span><span class="chip">${esc(g.head)}</span>${g.dirty ? `<span class="badge paused">${g.dirty} uncommitted</span>` : '<span class="badge ok">clean</span>'}` : ''}</div>`;
  $('#p-newtask').onclick = () => openCapture([], { project_id: p.id });
  if ($('#p-sync')) $('#p-sync').onclick = async () => { const r = await act(() => api('POST', `/projects/${p.id}/sync`)); if (r) { toast(r.ok ? 'Pulled: ' + (r.output || 'up to date') : r.output, !r.ok); refreshProject(); } };
  $('#p-arch').onclick = async () => { await act(() => api('PATCH', '/projects/' + p.id, { archived: !p.archived })); refreshProject(); };
  $('#p-del').onclick = async () => {
    if (!confirm('Delete this project? Tasks keep their history but lose the link. A cloned/uploaded workspace is removed; a local folder is left untouched.')) return;
    await act(() => api('DELETE', '/projects/' + p.id)); await loadProjects(); location.hash = '#/projects';
  };
}
function drawProjectTabs() {
  const p = P.proj;
  const tabs = [['overview', 'Overview'], ['tasks', 'Task history', P.tasks.length], ['chat', 'Chat', p.chat.length], ['knowledge', 'Knowledge', p.knowledge.length],
    ['commands', 'Commands', p.commands.length], ['skills', 'Skills', p.skill_ids.length], ['files', 'Files'], ['settings', 'Settings']];
  $('#p-tabs').innerHTML = tabs.map(([k, l, n]) => `<button data-tab="${k}" class="${P.tab === k ? 'active' : ''}">${l}${n ? `<span class="n">${n}</span>` : ''}</button>`).join('');
  $$('#p-tabs button').forEach((b) => (b.onclick = () => { P.tab = b.dataset.tab; drawProjectTabs(); drawProjectBody(); }));
}
function drawProjectBody() {
  const body = $('#p-body');
  ({ tasks: pTasks, chat: pChat, knowledge: pKnowledge, commands: pCommands, skills: pSkills, files: pFiles, settings: pSettings }[P.tab] || pOverview)(body);
}

function pOverview(body) {
  const p = P.proj; const u = p.usage;
  const recent = P.tasks.slice().sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at)).slice(0, 6);
  body.innerHTML = `<div class="grid4" style="margin-bottom:14px">
      <div class="stat accent"><div class="v">${p.tasks}</div><div class="l">tasks</div></div>
      <div class="stat"><div class="v">${p.active}</div><div class="l">active</div></div>
      <div class="stat"><div class="v">${p.done}</div><div class="l">done</div></div>
      <div class="stat"><div class="v">${fmtK(usageTotal(u))}</div><div class="l">tokens · ${fmtN(u.calls)} calls${u.cost ? ` · $${u.cost.toFixed(2)}` : ''}</div></div></div>
    <div class="grid2"><div class="card"><div class="card-h"><h3>Recent tasks</h3><a class="btn sm right" href="#/project/${esc(p.id)}/tasks">All history</a></div>
      ${recent.length ? recent.map((t) => `<a class="row nowrap" href="#/task/${esc(t.id)}" style="padding:6px 0;border-bottom:1px solid var(--line)">${stageBadge(t.stage)}<span class="ellipsis grow">${esc(t.title)}</span><span class="small muted">${ago(t.updated_at)}</span></a>`).join('') : empty('tasks', 'No tasks yet')}
      <div style="margin-top:12px">${stageBar(p.stages, p.tasks)}</div></div>
    <div class="card"><div class="card-h"><h3>Description</h3></div>${p.description ? md(p.description) : '<p class="muted">No description. Add one in Settings — it is injected into every prompt for this codebase.</p>'}
      <div class="section-title" style="margin-top:14px">Latest knowledge</div>
      ${p.knowledge.slice(-5).reverse().map((k) => `<div class="small" style="padding:3px 0">• ${esc(k.text)}</div>`).join('') || '<p class="muted small">Agents add knowledge with save_memory(scope=project); finished tasks promote their memory automatically.</p>'}</div></div>`;
}

function pTasks(body) {
  const f = P.taskFilter;
  const rows = P.tasks.filter((t) => f === 'all' || (f === 'active' && !t.archived && t.stage !== 'done') || (f === 'done' && t.stage === 'done') || (f === 'archived' && t.archived))
    .sort((a, b) => new Date(b.created_at) - new Date(a.created_at));
  body.innerHTML = `<div class="table-wrap"><div class="toolbar"><div class="seg" id="ptf">${['all', 'active', 'done', 'archived'].map((k) => `<button data-f="${k}" class="${f === k ? 'on' : ''}">${k}</button>`).join('')}</div>
      <span class="muted small">${rows.length} tasks · ${fmtK(rows.reduce((a, t) => a + usageTotal(t.usage), 0))} tokens</span>
      <button class="sm right" id="pt-new">${ic('plus')} New task</button></div>
    <table class="t"><thead><tr><th>Created</th><th>Title</th><th>Stage</th><th>Progress</th><th class="num">Tokens</th><th>Completed</th></tr></thead><tbody>
    ${rows.map((t) => `<tr class="click" data-id="${esc(t.id)}"><td class="small muted">${dt(t.created_at)}</td>
      <td><b>${esc(t.title)}</b>${t.archived ? ' <span class="badge outline">archived</span>' : ''}<div class="small muted ellipsis" style="max-width:420px">${esc(t.snippet)}</div></td>
      <td>${stageBadge(t.stage)}</td><td class="small">${t.total ? `${t.done}/${t.total}` : '—'}</td><td class="num small">${fmtK(usageTotal(t.usage))}</td>
      <td class="small muted">${t.done_at ? dt(t.done_at) : '—'}</td></tr>`).join('')}</tbody></table>
    ${rows.length ? '' : empty('tasks', 'No tasks in this view')}</div>`;
  $$('#ptf button').forEach((b) => (b.onclick = () => { P.taskFilter = b.dataset.f; pTasks(body); }));
  $$('tr[data-id]', body).forEach((tr) => (tr.onclick = () => (location.hash = '#/task/' + tr.dataset.id)));
  $('#pt-new').onclick = () => openCapture([], { project_id: P.proj.id });
}

function pChat(body) {
  const p = P.proj;
  const keep = $('#chat-text') ? $('#chat-text').value : '';
  body.innerHTML = `<div class="row" style="margin-bottom:8px"><span class="muted small">Ask the codebase: the assistant reads files with read-only tools and knows the project's knowledge and task history.</span>
    ${p.chat.length ? '<button class="sm right" id="pc-clear">Clear</button>' : ''}</div>${chatHTML(p.chat, p.chat_busy, false)}`;
  $('#chat-text').value = keep;
  const log = $('#chat-log'); log.scrollTop = log.scrollHeight;
  const send = async () => {
    const text = $('#chat-text').value.trim(); if (!text) return;
    const r = await act(() => api('POST', `/projects/${p.id}/chat`, { text }));
    if (r) { $('#chat-text').value = ''; P.proj = r.project; pChat(body); if ($('#p-tabs')) drawProjectTabs(); }
  };
  $('#chat-send').onclick = send;
  $('#chat-text').onkeydown = (e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) send(); };
  if ($('#pc-clear')) $('#pc-clear').onclick = async () => { const r = await act(() => api('DELETE', `/projects/${p.id}/chat`)); if (r) { P.proj = r.project; pChat(body); } };
}

function pKnowledge(body) {
  const p = P.proj;
  body.innerHTML = `<div class="card"><div class="card-h"><h3>Knowledge base</h3><span class="muted small">Shared by every task on this codebase (newest 40 go into prompts).</span></div>
    <div class="col" id="kn">${p.knowledge.map((k) => `<div class="row nowrap" data-id="${esc(k.id)}"><input class="grow" value="${esc(k.text)}">
      <span class="tiny faint" style="min-width:110px">${k.task_id ? `<a href="#/task/${esc(k.task_id)}">from task</a> · ` : ''}${dt(k.at)}</span>
      <button class="ghost icon" data-del>${ic('x')}</button></div>`).join('')}</div>
    <div class="row nowrap" style="margin-top:10px"><input id="kn-new" class="grow" placeholder="e.g. Build with make build; UI strings live in src/i18n/en.json"><button id="kn-add">${ic('plus')} Add</button></div>
    <div class="row" style="margin-top:12px"><button class="primary" id="kn-save">Save changes</button></div></div>`;
  const collect = () => $$('#kn > div').map((r) => ({ ...p.knowledge.find((k) => k.id === r.dataset.id), text: $('input', r).value }));
  $$('#kn [data-del]').forEach((b) => (b.onclick = () => b.parentElement.remove()));
  const save = async (list) => { const r = await act(() => api('PATCH', '/projects/' + p.id, { knowledge: list }), 'Knowledge saved'); if (r) { P.proj = r.project; pKnowledge(body); drawProjectTabs(); } };
  $('#kn-add').onclick = () => { const t = $('#kn-new').value.trim(); if (t) save([...collect(), { text: t }]); };
  $('#kn-save').onclick = () => save(collect());
}

function pCommands(body) {
  const p = P.proj;
  const rowH = (c) => `<tr><td><input data-k="name" value="${esc(c.name)}" placeholder="test"></td><td><input data-k="command" class="mono" value="${esc(c.command)}" placeholder="npm test"></td>
    <td><input data-k="description" value="${esc(c.description)}" placeholder="run unit tests"></td><td><button class="ghost icon" data-del>${ic('x')}</button></td></tr>`;
  body.innerHTML = `<div class="card"><div class="card-h"><h3>Project commands</h3></div>
    <p class="help" style="margin-top:0">Named commands the worker can run with the <b>project_command</b> tool — <b>no approval needed</b> because you defined them. Use them for build, test, lint, typecheck.</p>
    <table class="t"><thead><tr><th style="width:160px">Name</th><th>Command</th><th>Description</th><th style="width:40px"></th></tr></thead><tbody id="cmds">${p.commands.map(rowH).join('')}</tbody></table>
    <div class="row" style="margin-top:10px"><button id="cmd-add">${ic('plus')} Add command</button><button class="primary right" id="cmd-save">Save</button></div></div>`;
  const wire = () => $$('#cmds [data-del]').forEach((b) => (b.onclick = () => b.closest('tr').remove()));
  wire();
  $('#cmd-add').onclick = () => { $('#cmds').insertAdjacentHTML('beforeend', rowH({ name: '', command: '', description: '' })); wire(); };
  $('#cmd-save').onclick = async () => {
    const commands = $$('#cmds tr').map((tr) => Object.fromEntries($$('input', tr).map((i) => [i.dataset.k, i.value])));
    const r = await act(() => api('PATCH', '/projects/' + p.id, { commands }), 'Commands saved');
    if (r) { P.proj = r.project; pCommands(body); drawProjectTabs(); }
  };
}

function pSkills(body) {
  const p = P.proj; const skills = (S.cfg && S.cfg.skills) || [];
  body.innerHTML = `<div class="card"><div class="card-h"><h3>Skills attached to this project</h3><a class="btn sm right" href="#/skills">Manage skills</a></div>
    <p class="help" style="margin-top:0">Attached skills are always injected for this project's tasks. Others are only listed by name (loaded on demand or by keyword match) to save tokens.</p>
    ${skills.length ? skills.map((s) => `<label class="row nowrap" style="padding:8px 0;border-bottom:1px solid var(--line)">${sw('sk-' + s.id, p.skill_ids.includes(s.id) || s.always)}
      <div class="grow"><b>${esc(s.name)}</b> ${s.always ? '<span class="badge outline">always on</span>' : ''}${!s.enabled ? '<span class="badge">disabled</span>' : ''}<div class="small muted">${esc(s.description)}</div></div></label>`).join('') : empty('sparkles', 'No skills defined')}
    <div class="row" style="margin-top:12px"><button class="primary" id="sk-save">Save</button></div></div>`;
  skills.filter((s) => s.always).forEach((s) => ($('#sk-' + s.id).disabled = true));
  $('#sk-save').onclick = async () => {
    const ids = skills.filter((s) => !s.always && $('#sk-' + s.id).checked).map((s) => s.id);
    const r = await act(() => api('PATCH', '/projects/' + p.id, { skill_ids: ids }), 'Saved');
    if (r) { P.proj = r.project; drawProjectTabs(); }
  };
}

async function pFiles(body) {
  const p = P.proj;
  if (!p.workspace) { body.innerHTML = `<div class="card">${empty('folder', 'This project has no workspace')}</div>`; return; }
  if (!P.files) {
    body.innerHTML = '<p class="muted"><span class="spin"></span></p>';
    P.files = (await act(() => api('GET', `/projects/${p.id}/files`))) || [];
  }
  const q = (P.fileQ || '').toLowerCase();
  const files = P.files.filter((f) => !q || f.Path.toLowerCase().includes(q)).slice(0, 1500);
  let lastDir = '';
  const tree = files.map((f) => {
    const dir = f.Path.includes('/') ? f.Path.slice(0, f.Path.lastIndexOf('/')) : '';
    let head = '';
    if (dir !== lastDir) { head = `<div class="dir">${esc(dir || '.')}/</div>`; lastDir = dir; }
    return head + `<div data-p="${esc(f.Path)}" class="${P.file === f.Path ? 'on' : ''}" style="padding-left:${dir ? 18 : 6}px">${esc(f.Path.slice(dir ? dir.length + 1 : 0))}</div>`;
  }).join('');
  body.innerHTML = `<div class="grid2" style="grid-template-columns:320px 1fr"><div class="card" style="padding:8px">
      <input id="fq" placeholder="Filter ${P.files.length} files…" value="${esc(P.fileQ || '')}" style="margin-bottom:6px"><div class="tree">${tree}</div></div>
    <div><div class="row" style="margin-bottom:6px"><code>${esc(P.file || 'Select a file')}</code></div><div class="code" id="fview"><span class="faint">—</span></div></div></div>`;
  $('#fq').oninput = debounce((e) => { P.fileQ = $('#fq').value; pFiles(body).then(() => { const n = $('#fq'); n.focus(); n.setSelectionRange(n.value.length, n.value.length); }); }, 200);
  $$('.tree [data-p]', body).forEach((d) => (d.onclick = () => { P.file = d.dataset.p; $$('.tree [data-p]').forEach((x) => x.classList.toggle('on', x === d)); showFile(); }));
  if (P.file) showFile();
}
async function showFile() {
  const r = await act(() => api('GET', `/projects/${P.proj.id}/file?path=${encodeURIComponent(P.file)}`));
  if (r === null) return;
  $('#fview').innerHTML = String(r).split('\n').map((l) => `<span>${esc(l) || ' '}</span>`).join('');
  $('#fview').previousElementSibling.innerHTML = `<code>${esc(P.file)}</code>`;
}

function pSettings(body) {
  const p = P.proj;
  body.innerHTML = `<div class="card" style="max-width:820px"><div class="col">
    <label class="f">Name<input id="ps-name" value="${esc(p.name)}"></label>
    <label class="f">Description <span class="hint">stack, architecture, conventions — injected into analyzer, worker and chat prompts</span><textarea id="ps-desc" rows="6">${esc(p.description)}</textarea></label>
    <div class="grid2"><label class="f">Default analyzer model<select id="ps-an">${profileOptions(p.overrides.analyzer)}</select></label>
      <label class="f">Default worker model<select id="ps-wk">${profileOptions(p.overrides.worker)}</select></label></div>
    <div class="row"><button class="primary" id="ps-save">Save</button></div></div></div>`;
  $('#ps-save').onclick = async () => {
    const r = await act(() => api('PATCH', '/projects/' + p.id, { name: $('#ps-name').value, description: $('#ps-desc').value,
      overrides: { analyzer: $('#ps-an').value, worker: $('#ps-wk').value } }), 'Project saved');
    if (r) { P.proj = r.project; loadProjects(); drawProjectHead(); }
  };
}

// ---------------------------------------------------------------- chat page (ask any codebase)
ROUTES.chat = async ([pid]) => {
  S.view = 'chat';
  await loadProjects();
  const list = S.projects.filter((p) => !p.archived);
  const cur = pid || localStorage.getItem('chatProject') || (list[0] && list[0].id);
  $('#app').innerHTML = `<div class="page-head"><div><h1>Chat</h1><p>Ask questions about a codebase. For a specific task, use the Chat tab inside the task.</p></div></div>
    <div class="grid2" style="grid-template-columns:260px 1fr"><div class="card" style="padding:8px">${list.map((p) => `<a href="#/chat/${esc(p.id)}" class="row nowrap" style="padding:8px;border-radius:6px;${p.id === cur ? 'background:var(--accent-soft);color:var(--accent)' : ''}">${ic('folder')}<span class="ellipsis">${esc(p.name)}</span></a>`).join('') || empty('folder', 'No projects')}</div>
    <div id="p-body"></div></div>`;
  if (!cur) return;
  localStorage.setItem('chatProject', cur);
  const r = await act(() => api('GET', '/projects/' + cur));
  if (!r) return;
  P.proj = r.project;
  pChat($('#p-body'));
  S.onEvent = debounce(async (ev) => {
    if (ev.type !== 'project' || !ev.data || ev.data.project_id !== cur) return;
    const x = await api('GET', '/projects/' + cur).catch(() => null);
    if (x && S.view === 'chat') { P.proj = x.project; pChat($('#p-body')); }
  }, 300);
};
