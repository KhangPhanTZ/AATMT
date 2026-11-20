/* AATMT — tasks: list (table / board), capture, task detail. */
'use strict';

const T = { list: [], archived: false, mode: localStorage.getItem('taskMode') || 'table', q: '', stage: '', project: '', sel: new Set(),
  task: null, sum: null, ws: '', proj: null, events: [], tab: 'overview', planEdit: false, diffs: null, llm: null, evFilter: '' };
const PRIO = { urgent: 0, high: 1, normal: 2, low: 3 };

// ---------------------------------------------------------------- task list
ROUTES.tasks = async () => {
  S.view = 'tasks';
  $('#app').innerHTML = `
    <div class="page-head"><div><h1>Tasks</h1><p>Everything captured from your manager, with stage, progress and token spend.</p></div>
      <div class="right row"><div class="seg" id="arch-seg"><button data-a="0">Current</button><button data-a="1">Archived</button></div>
      <div class="seg" id="mode-seg"><button data-m="table" title="Table">${ic('list')}</button><button data-m="board" title="Board">${ic('kanban')}</button></div>
      <button class="primary" id="nt-btn">${ic('plus')} New task</button></div></div>
    <div id="tasks-body"></div>`;
  $$('#arch-seg button').forEach((b) => (b.onclick = () => { T.archived = b.dataset.a === '1'; T.sel.clear(); loadTaskList(); }));
  $$('#mode-seg button').forEach((b) => (b.onclick = () => { T.mode = b.dataset.m; localStorage.setItem('taskMode', T.mode); drawTasks(); }));
  $('#nt-btn').onclick = () => openCapture();
  S.onEvent = (ev) => { if (ev.type === 'task' || ev.type === 'stage' || ev.type === 'project') refreshTaskList(); };
  await loadTaskList();
};
async function loadTaskList() {
  const r = await act(() => api('GET', '/tasks?archived=' + (T.archived ? 1 : 0)));
  if (r) T.list = r;
  drawTasks();
}
const refreshTaskList = debounce(() => { if (S.view === 'tasks') loadTaskList(); }, 400);

function filteredTasks() {
  const q = T.q.toLowerCase();
  return T.list.filter((x) => (!T.stage || x.stage === T.stage) && (!T.project || x.project_id === T.project) &&
    (!q || (x.id + ' ' + x.title + ' ' + x.snippet + ' ' + x.project_name + ' ' + x.tags.join(' ')).toLowerCase().includes(q)))
    .sort((a, b) => (b.pinned - a.pinned) || (PRIO[a.priority] - PRIO[b.priority]) || (new Date(b.updated_at) - new Date(a.updated_at)));
}

function drawTasks() {
  const body = $('#tasks-body'); if (!body) return;
  $$('#arch-seg button').forEach((b) => b.classList.toggle('on', (b.dataset.a === '1') === T.archived));
  $$('#mode-seg button').forEach((b) => b.classList.toggle('on', b.dataset.m === T.mode));
  const rows = filteredTasks();
  const projects = [...new Map(T.list.filter((x) => x.project_id).map((x) => [x.project_id, x.project_name])).entries()];
  const toolbar = `<div class="toolbar">
      <input id="tq" placeholder="Search title / ID / tag…" value="${esc(T.q)}" style="min-width:240px">
      <select id="tstage"><option value="">All stages</option>${STAGES.map((s) => `<option value="${s}" ${T.stage === s ? 'selected' : ''}>${STAGE_LABEL[s]}</option>`).join('')}</select>
      <select id="tproj"><option value="">All projects</option>${projects.map(([id, n]) => `<option value="${esc(id)}" ${T.project === id ? 'selected' : ''}>${esc(n)}</option>`).join('')}</select>
      <span class="muted small">${rows.length}/${T.list.length} tasks</span>
      <span class="right row" id="bulk"></span></div>`;
  if (T.mode === 'board' && !T.archived) {
    body.innerHTML = `<div class="table-wrap" style="margin-bottom:12px">${toolbar}</div><div class="board" id="board">${STAGES.map((st) => {
      const items = rows.filter((x) => x.stage === st);
      return `<section class="column" data-stage="${st}"><div class="column-head">${stageBadge(st)}<span class="muted">${items.length}</span></div>
        <div class="column-body">${items.map(cardHTML).join('') || '<div class="empty small" style="padding:16px">—</div>'}</div></section>`;
    }).join('')}</div>`;
    $$('.tcard').forEach((c) => {
      c.onclick = () => (location.hash = '#/task/' + c.dataset.id);
      c.ondragstart = (e) => e.dataTransfer.setData('text/plain', c.dataset.id);
    });
    $$('.column').forEach((col) => {
      col.ondragover = (e) => { e.preventDefault(); col.classList.add('drop'); };
      col.ondragleave = () => col.classList.remove('drop');
      col.ondrop = async (e) => {
        e.preventDefault(); col.classList.remove('drop');
        const id = e.dataTransfer.getData('text/plain'); const stage = col.dataset.stage;
        if (stage === 'running' || stage === 'analyzing') return toast('Use the task actions to start work', true);
        await act(() => api('PATCH', '/tasks/' + id, { stage })); loadTaskList();
      };
    });
  } else {
    body.innerHTML = `<div class="table-wrap">${toolbar}
      <table class="t"><thead><tr><th style="width:28px"><input type="checkbox" id="selall"></th><th>ID</th><th>Title</th><th>Project</th><th>Stage</th>
        <th>Progress</th><th class="num">Tokens</th><th>Updated</th><th style="text-align:right">Actions</th></tr></thead>
      <tbody>${rows.map(rowHTML).join('')}</tbody></table>
      ${rows.length ? '' : empty('tasks', T.archived ? 'No archived tasks' : 'No tasks yet — press N or paste a screenshot to capture one')}
      <div class="foot">${fmtK(rows.reduce((a, x) => a + usageTotal(x.usage), 0))} tokens · ${rows.filter((x) => x.running).length} running</div></div>`;
    $$('tr.click').forEach((tr) => (tr.onclick = (e) => { if (!e.target.closest('button,input')) location.hash = '#/task/' + tr.dataset.id; }));
    $$('[data-sel]').forEach((c) => (c.onchange = () => { c.checked ? T.sel.add(c.dataset.sel) : T.sel.delete(c.dataset.sel); drawBulk(); }));
    $('#selall').onchange = (e) => { rows.forEach((x) => (e.target.checked ? T.sel.add(x.id) : T.sel.delete(x.id))); drawTasks(); };
    $$('[data-act]').forEach((b) => (b.onclick = () => rowAction(b.dataset.act, b.dataset.id)));
  }
  $('#tq').oninput = (e) => { T.q = e.target.value; const pos = e.target.selectionStart; drawTasks(); const n = $('#tq'); n.focus(); n.setSelectionRange(pos, pos); };
  $('#tstage').onchange = (e) => { T.stage = e.target.value; drawTasks(); };
  $('#tproj').onchange = (e) => { T.project = e.target.value; drawTasks(); };
  drawBulk();
}
function drawBulk() {
  const el = $('#bulk'); if (!el) return;
  if (!T.sel.size) { el.innerHTML = ''; return; }
  el.innerHTML = `<span class="small muted">${T.sel.size} selected</span>
    <button class="sm" id="b-arch">${ic('archive')} ${T.archived ? 'Restore' : 'Archive'}</button><button class="sm danger" id="b-del">${ic('trash')} Delete</button>`;
  $('#b-arch').onclick = async () => { for (const id of T.sel) await act(() => api('PATCH', '/tasks/' + id, { archived: !T.archived })); T.sel.clear(); loadTaskList(); };
  $('#b-del').onclick = async () => {
    if (!confirm(`Delete ${T.sel.size} task(s)? Workspace changes stay on disk.`)) return;
    for (const id of T.sel) await act(() => api('DELETE', '/tasks/' + id)); T.sel.clear(); loadTaskList();
  };
}
function rowHTML(x) {
  const pct = x.total ? Math.round((x.done / x.total) * 100) : 0;
  const live = x.running ? `<span class="badge running pulse">● ${esc(x.running)}</span>` : '';
  return `<tr class="click" data-id="${esc(x.id)}">
    <td><input type="checkbox" data-sel="${esc(x.id)}" ${T.sel.has(x.id) ? 'checked' : ''}></td>
    <td><span class="chip">${esc(x.id.slice(-6))}</span></td>
    <td style="max-width:360px"><div class="row nowrap">${x.pinned ? `<span style="color:var(--warn)">${ic('star')}</span>` : ''}<b class="ellipsis">${esc(x.title)}</b></div>
      <div class="small muted ellipsis">${esc(x.snippet)}</div></td>
    <td class="small">${x.project_name ? `<a href="#/project/${esc(x.project_id)}" class="row nowrap">${ic('folder')}${esc(x.project_name)}</a>` : '<span class="faint">—</span>'}</td>
    <td><div class="row nowrap">${stageBadge(x.stage)}${live}${x.pending ? '<span class="badge paused">approval</span>' : ''}${x.error ? '<span class="badge err">error</span>' : ''}
      ${x.priority !== 'normal' ? `<span class="badge ${esc(x.priority)}">${esc(x.priority)}</span>` : ''}</div></td>
    <td>${x.total ? `<div class="row nowrap"><div class="bar ${pct === 100 ? 'ok' : ''}" style="width:70px"><i style="width:${pct}%"></i></div><span class="small muted">${x.done}/${x.total}</span></div>` : '<span class="faint">—</span>'}</td>
    <td class="num small">${fmtK(usageTotal(x.usage))}</td>
    <td class="small muted">${ago(x.updated_at)}</td>
    <td><div class="acts">
      <button title="Open" data-act="open" data-id="${esc(x.id)}">${ic('eye')}</button>
      ${x.running ? `<button title="Pause" data-act="pause" data-id="${esc(x.id)}">${ic('pause')}</button>` :
        x.total > x.done && ['planned', 'paused', 'review'].includes(x.stage) ? `<button title="Run" data-act="run" data-id="${esc(x.id)}">${ic('play')}</button>` : ''}
      <button title="${x.pinned ? 'Unpin' : 'Pin'}" data-act="pin" data-id="${esc(x.id)}">${ic('pin')}</button>
      <button title="${x.archived ? 'Restore' : 'Archive'}" data-act="archive" data-id="${esc(x.id)}">${ic('archive')}</button>
      <button class="del" title="Delete" data-act="delete" data-id="${esc(x.id)}">${ic('trash')}</button></div></td></tr>`;
}
async function rowAction(a, id) {
  const x = T.list.find((t) => t.id === id);
  if (a === 'open') { location.hash = '#/task/' + id; return; }
  if (a === 'run') await act(() => api('POST', `/tasks/${id}/run`));
  if (a === 'pause') await act(() => api('POST', `/tasks/${id}/pause`));
  if (a === 'pin') await act(() => api('PATCH', '/tasks/' + id, { pinned: !x.pinned }));
  if (a === 'archive') await act(() => api('PATCH', '/tasks/' + id, { archived: !x.archived }));
  if (a === 'delete') { if (!confirm('Delete this task? Workspace changes stay on disk.')) return; await act(() => api('DELETE', '/tasks/' + id)); }
  loadTaskList();
}
function cardHTML(x) {
  const pct = x.total ? Math.round((x.done / x.total) * 100) : 0;
  return `<article class="tcard" draggable="true" data-id="${esc(x.id)}">
    <div class="title">${x.pinned ? `<span style="color:var(--warn)">${ic('star')}</span> ` : ''}${esc(x.title)}</div>
    ${x.snippet ? `<div class="snip">${esc(x.snippet)}</div>` : ''}
    ${x.total ? `<div class="bar ${pct === 100 ? 'ok' : ''}" style="margin:6px 0"><i style="width:${pct}%"></i></div>` : ''}
    <div class="meta">${x.priority !== 'normal' ? `<span class="badge ${esc(x.priority)}">${esc(x.priority)}</span>` : ''}
      ${x.running ? `<span class="badge running pulse">● ${esc(x.running)}</span>` : ''}${x.pending ? '<span class="badge paused">approval</span>' : ''}
      ${x.error ? '<span class="badge err">error</span>' : ''}${x.project_name ? `<span>${ic('folder')} ${esc(x.project_name)}</span>` : ''}
      ${x.total ? `<span>${x.done}/${x.total}</span>` : ''}<span class="right">${fmtK(usageTotal(x.usage))}</span></div></article>`;
}

// ---------------------------------------------------------------- capture modal
let capture = null;
async function openCapture(initialFiles = [], preset = {}) {
  if (capture) { addCaptureFiles(initialFiles); return; }
  if (!S.cfg) await loadConfig();
  await loadProjects();
  const st = S.cfg ? S.cfg.settings : {};
  const tpls = (S.cfg && S.cfg.templates) || [];
  const live = S.projects.filter((p) => !p.archived);
  let lastProject = preset.project_id ?? localStorage.getItem('lastProject');
  if (lastProject === null || (lastProject && !live.some((p) => p.id === lastProject))) lastProject = live[0] ? live[0].id : '';
  const m = modal(`
    <div class="modal-h"><h2>New task</h2><button class="ghost icon right" data-close>${ic('x')}</button></div>
    <div class="modal-b">
      ${tpls.length ? `<label class="f">Template<select id="c-tpl"><option value="">— none —</option>${tpls.map((t) => `<option value="${esc(t.id)}">${esc(t.name)}</option>`).join('')}</select></label>` : ''}
      <input id="c-title" placeholder="Title (leave blank — the AI names it)">
      <div id="c-msgs" class="col"></div>
      <button class="sm ghost" id="c-more" style="align-self:flex-start">${ic('plus')} Another message</button>
      <div><div class="dropzone" id="c-drop">${ic('image')} Paste screenshots (Ctrl+V), drag &amp; drop, or click to browse</div>
        <input type="file" id="c-file" accept="image/*" multiple class="hidden">
        <div class="row" style="margin-top:6px"><button class="sm" id="c-screen">${ic('camera')} Capture screen</button></div>
        <div class="thumbs" id="c-thumbs"></div></div>
      <div class="col"><span class="section-title">Source code</span>
        <select id="c-proj">${projectOptions(lastProject, '— no source code —')}<option value="__new">＋ New project from a source…</option></select>
        <div id="c-newproj" class="col hidden"><input id="c-pname" placeholder="Project name (optional)">${sourceFormHTML('cs')}</div></div>
      <div class="grid3">
        <label class="f">Priority<select id="c-prio"><option>low</option><option selected>normal</option><option>high</option><option>urgent</option></select></label>
        <label class="f">Due<input id="c-due" type="date"></label>
        <label class="f">Tags<input id="c-tags" placeholder="frontend, bug"></label>
      </div>
      <details><summary class="small muted" style="cursor:pointer">Models for this task</summary><div class="grid2" style="margin-top:8px">
        <label class="f">Analyzer<select id="c-an">${profileOptions('')}</select></label><label class="f">Worker<select id="c-wk">${profileOptions('')}</select></label></div>
        <label class="chk small" style="margin-top:8px">${sw('c-rot', false)} Multi-LLM worker rotation (step i starts on provider i of the worker chain)</label></details>
    </div>
    <div class="modal-f"><label class="chk small">${sw('c-analyze', st.auto_analyze !== false)} Analyze right away</label>
      <span class="right"></span><button data-close>Cancel</button><button class="primary" id="c-create">Create task</button></div>`);
  capture = { files: [] };
  S.onModalClose = () => { capture = null; };
  const addMsg = (text = '', from = localStorage.getItem('lastFrom') || '') => {
    const d = document.createElement('div'); d.className = 'row nowrap'; d.style.alignItems = 'flex-start';
    d.innerHTML = `<input class="c-from" placeholder="From" style="max-width:150px" value="${esc(from)}">
      <textarea class="c-text grow" rows="4" placeholder="Paste the message from your manager / client…">${esc(text)}</textarea>`;
    $('#c-msgs').appendChild(d); $('.c-text', d).focus();
  };
  addMsg(preset.message || '');
  $('#c-more').onclick = () => addMsg();
  wireDrop($('#c-drop'), $('#c-file'), addCaptureFiles);
  wireSourceForm('cs', 'path');
  const projSel = () => $('#c-newproj').classList.toggle('hidden', $('#c-proj').value !== '__new');
  $('#c-proj').onchange = projSel; projSel();
  $('#c-screen').onclick = captureScreen;
  if ($('#c-tpl')) $('#c-tpl').onchange = (e) => {
    const t = tpls.find((x) => x.id === e.target.value); if (!t) return;
    if (t.title) $('#c-title').value = t.title;
    if (t.message) $('.c-text').value = t.message;
    if (t.priority) $('#c-prio').value = t.priority;
    if (t.tags) $('#c-tags').value = t.tags.join(', ');
    if (t.project_id) { $('#c-proj').value = t.project_id; projSel(); }
  };
  addCaptureFiles(initialFiles);
  $('#c-create').onclick = async (e) => {
    const btn = e.currentTarget; const fd = new FormData();
    const msgs = $$('#c-msgs .row').map((r) => ({ from: $('.c-from', r).value, text: $('.c-text', r).value })).filter((x) => x.text.trim());
    if (!msgs.length && !capture.files.length && !($('#c-tpl') && $('#c-tpl').value)) return toast('Add a message or a screenshot', true);
    if (msgs[0] && msgs[0].from) localStorage.setItem('lastFrom', msgs[0].from);
    fd.append('title', $('#c-title').value); fd.append('messages', JSON.stringify(msgs));
    fd.append('priority', $('#c-prio').value); fd.append('due', $('#c-due').value); fd.append('tags', $('#c-tags').value);
    fd.append('analyze', $('#c-analyze').checked ? '1' : '0'); fd.append('rotate', $('#c-rot').checked ? '1' : '0');
    fd.append('analyzer', $('#c-an').value); fd.append('worker', $('#c-wk').value);
    if ($('#c-tpl')) fd.append('template_id', $('#c-tpl').value);
    const pv = $('#c-proj').value;
    if (pv === '__new') { sourceFormData('cs', fd); fd.append('project_name', $('#c-pname').value); } else { fd.append('project_id', pv); localStorage.setItem('lastProject', pv); }
    capture.files.forEach((f, i) => fd.append('images', f, f.name || `screenshot-${i + 1}.png`));
    btn.disabled = true; btn.innerHTML = '<span class="spin"></span> Creating…';
    const r = await act(() => api('POST', '/tasks', fd));
    btn.disabled = false; btn.textContent = 'Create task';
    if (r) { if (r.task.project_id) localStorage.setItem('lastProject', r.task.project_id); m.close(); loadProjects(); location.hash = '#/task/' + r.task.id; }
  };
}
function addCaptureFiles(files) {
  if (!capture) return;
  files.forEach((f) => capture.files.push(f));
  $('#c-thumbs').innerHTML = capture.files.map((f, i) => `<div class="thumb"><img src="${URL.createObjectURL(f)}" alt=""><button data-rm="${i}">✕</button></div>`).join('');
  $$('#c-thumbs [data-rm]').forEach((b) => (b.onclick = () => { capture.files.splice(+b.dataset.rm, 1); addCaptureFiles([]); }));
}
async function captureScreen() {
  try {
    const stream = await navigator.mediaDevices.getDisplayMedia({ video: true });
    const v = document.createElement('video'); v.srcObject = stream; v.muted = true; await v.play();
    await new Promise((r) => setTimeout(r, 400));
    const c = document.createElement('canvas'); c.width = v.videoWidth; c.height = v.videoHeight;
    c.getContext('2d').drawImage(v, 0, 0); stream.getTracks().forEach((tr) => tr.stop());
    const blob = await new Promise((r) => c.toBlob(r, 'image/png'));
    addCaptureFiles([new File([blob], `screen-${Date.now()}.png`, { type: 'image/png' })]);
  } catch (e) { toast('Screen capture: ' + e.message, true); }
}
// An image pasted anywhere (outside a text field) starts a capture, or attaches to the open task.
window.addEventListener('paste', (e) => {
  const files = [...(e.clipboardData?.files || [])].filter((f) => f.type.startsWith('image/'));
  if (!files.length) return;
  e.preventDefault();
  if (S.view === 'task' && !capture && T.tab === 'overview' && T.task) {
    const fd = new FormData(); files.forEach((f, i) => fd.append('images', f, f.name || `paste-${i}.png`));
    act(() => api('POST', `/tasks/${T.task.id}/images`, fd), 'Screenshot added').then(refreshTask);
    return;
  }
  if (capture) addCaptureFiles(files); else openCapture(files);
});

// ---------------------------------------------------------------- task detail
ROUTES.task = async ([id]) => {
  S.view = 'task';
  if (!T.task || T.task.id !== id) { T.tab = 'overview'; T.events = []; T.diffs = null; T.llm = null; T.evFilter = ''; }
  T.planEdit = false;
  $('#app').innerHTML = `<div id="t-head"></div><div id="t-banners"></div><div class="tabs" id="t-tabs"></div>
    <div class="layout"><div id="t-body"></div><aside id="t-side" class="stack"></aside></div>`;
  const r = await act(() => api('GET', '/tasks/' + id));
  if (S.view !== 'task' || !$('#t-head')) return; // navigated away while loading
  if (!r) { $('#app').innerHTML = empty('tasks', 'Task not found'); return; }
  setTask(r);
  const evs = await act(() => api('GET', `/tasks/${id}/events?limit=1000`));
  if (S.view !== 'task' || !$('#t-head')) return;
  if (evs) T.events = evs;
  S.onEvent = (ev) => {
    if (ev.task_id !== T.task.id) return;
    if (ev.type !== 'task' && ev.seq && !T.events.some((x) => x.seq === ev.seq)) {
      T.events.push(ev);
      if (T.tab === 'activity') appendEvent(ev);
      if (ev.type === 'change') T.diffs = null;
      if (ev.type === 'llm') T.llm = null;
    }
    refreshTask();
  };
  drawTask(true);
};
function setTask(r) { T.task = r.task; T.sum = r.summary; T.ws = r.workspace; T.proj = r.project || null; }
let pendingRefresh = false;
const refreshTask = debounce(async () => {
  if (S.view !== 'task' || !T.task) return;
  const r = await api('GET', '/tasks/' + T.task.id).catch(() => null);
  if (!r) return;
  setTask(r);
  const body = $('#t-body');
  const editing = body && body.contains(document.activeElement) && /INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName);
  if (editing) { pendingRefresh = true; drawTask(false, true); return; }
  drawTask(false);
}, 300);
document.addEventListener('focusout', () => { if (pendingRefresh && S.view === 'task') { pendingRefresh = false; setTimeout(() => drawTask(false), 60); } });

function drawTask(first, skipBody) {
  if (!$('#t-head')) return;
  drawHead(); drawBanners(); drawSide(); drawTabs();
  if (skipBody) return;
  if (T.tab === 'plan' && T.planEdit && !first) return;
  if (T.tab === 'graph' && !first) return drawGraph($('#t-body'), true);
  drawBody();
}
const stageIndex = (st) => ({ inbox: 0, analyzing: 1, planned: 2, running: 3, paused: 3, review: 4, done: 5 }[st] ?? 0);

function drawHead() {
  const x = T.task, s = T.sum, run = s.running;
  const pending = x.steps.some((st) => st.status === 'pending' || st.status === 'running');
  const btn = [];
  if (run) btn.push(`<button class="danger" data-a="pause">${ic('pause')} Pause</button>`);
  else {
    if (['inbox', 'planned', 'paused'].includes(x.stage)) btn.push(`<button class="${x.stage === 'inbox' ? 'primary' : ''}" data-a="analyze">${ic('search')} ${x.brief ? 'Re-analyze' : 'Analyze'}</button>`);
    if (pending && ['planned', 'paused', 'review'].includes(x.stage)) btn.push(`<button class="primary" data-a="run">${ic('play')} ${x.stage === 'planned' ? 'Approve & run' : 'Resume'}</button>`);
    if (x.stage === 'review') btn.push(`<button class="ok" data-a="done">${ic('check')} Mark done</button>`);
    if (x.stage === 'done') btn.push(`<button data-a="reopen">${ic('refresh')} Reopen</button>`);
  }
  btn.push(`<button class="ghost icon" data-a="pin" title="${x.pinned ? 'Unpin' : 'Pin'}" style="${x.pinned ? 'color:var(--warn)' : ''}">${ic('star')}</button>`);
  btn.push(`<button class="ghost icon" data-a="duplicate" title="Duplicate">${ic('copy')}</button>`);
  btn.push(`<button class="ghost icon" data-a="template" title="Save as template">${ic('layers')}</button>`);
  btn.push(`<button class="ghost icon" data-a="archive" title="${x.archived ? 'Restore' : 'Archive'}">${ic('archive')}</button>`);
  btn.push(`<button class="ghost icon danger" data-a="delete" title="Delete">${ic('trash')}</button>`);
  const cur = stageIndex(x.stage);
  const steps = ['inbox', 'analyzing', 'planned', 'running', 'review', 'done'];
  const worker = profileName(x.overrides.worker) || '';
  $('#t-head').innerHTML = `
    <div class="t-head"><a class="btn ghost icon" href="${T.proj ? '#/project/' + esc(T.proj.id) + '/tasks' : '#/tasks'}" title="Back">${ic('back')}</a>
      <input class="t-title" id="title" value="${esc(x.title)}">
      <span class="chip">${esc(x.id)}</span>${run ? `<span class="badge running pulse dot">${esc(run)}</span>` : stageBadge(x.stage)}
      ${x.archived ? '<span class="badge outline">archived</span>' : ''}<div class="row">${btn.join('')}</div></div>
    <div class="t-sub">${T.proj ? `<a href="#/project/${esc(T.proj.id)}" class="chip">${ic('folder')} ${esc(T.proj.name)}</a>` : '<span class="chip">no project</span>'}
      ${worker ? `<span class="chip">${ic('cpu')} ${esc(worker)}</span>` : ''}${x.rotate ? '<span class="chip">rotation</span>' : ''}
      <span>${esc(x.status || '')}</span></div>
    <div class="pipeline">${steps.map((st, i) => {
      const cls = i < cur ? 'past' : i === cur ? 'now' + (x.stage === 'paused' ? ' paused' : '') : '';
      let sub = '';
      if (st === 'running' && s.total) sub = `${s.done}/${s.total} steps`;
      if (st === 'running' && x.stage === 'paused') sub = 'Paused';
      if (st === 'done' && x.done_at) sub = dt(x.done_at);
      return `<div class="st ${cls}">${esc(STAGE_LABEL[st])}${sub ? `<small>${esc(sub)}</small>` : ''}</div>`;
    }).join('')}</div>`;
  $('#title').onchange = (e) => act(() => api('PATCH', '/tasks/' + x.id, { title: e.target.value }));
  $$('#t-head [data-a]').forEach((b) => (b.onclick = () => taskAction(b.dataset.a)));
}

async function taskAction(a) {
  const id = T.task.id;
  if (a === 'delete') {
    if (!confirm('Delete this task? Workspace changes stay on disk.')) return;
    await act(() => api('DELETE', '/tasks/' + id)); location.hash = '#/tasks'; return;
  }
  if (a === 'analyze') {
    const est = await act(() => api('GET', `/tasks/${id}/estimate`));
    if (est) toast(`≈ ${fmtK(est.analysis_tokens + est.vision_tokens)} input tokens`);
    await act(() => api('POST', `/tasks/${id}/analyze`));
  }
  if (a === 'run' || a === 'pause') await act(() => api('POST', `/tasks/${id}/${a}`));
  if (a === 'done') await act(() => api('PATCH', '/tasks/' + id, { stage: 'done' }), 'Task completed');
  if (a === 'reopen') await act(() => api('PATCH', '/tasks/' + id, { stage: 'review' }));
  if (a === 'pin') await act(() => api('PATCH', '/tasks/' + id, { pinned: !T.task.pinned }));
  if (a === 'archive') await act(() => api('PATCH', '/tasks/' + id, { archived: !T.task.archived }), T.task.archived ? 'Restored' : 'Archived');
  if (a === 'duplicate') { const r = await act(() => api('POST', `/tasks/${id}/duplicate`), 'Duplicated'); if (r) { location.hash = '#/task/' + r.task.id; return; } }
  if (a === 'template') {
    const name = prompt('Template name', T.task.title); if (!name) return;
    await act(() => api('POST', `/tasks/${id}/template`, { name }), 'Template saved'); loadConfig();
  }
  refreshTask();
}

function drawBanners() {
  const x = T.task; const out = [];
  if (x.pending) {
    out.push(`<div class="banner warn"><b>${ic('shield')} The agent wants to run a command</b><pre class="mono">${esc(x.pending.command)}</pre>
      <div class="row"><button class="ok" data-ap="1">Allow once</button><button data-ap="2">Always allow</button>
      <button class="danger" data-ap="0">Deny</button></div></div>`);
  }
  if (x.stage === 'paused' && (x.status || '').startsWith('question:')) {
    out.push(`<div class="banner info"><b>${ic('chat')} The agent asks</b><div>${esc(x.status.slice(9).trim())}</div>
      <textarea id="answer" rows="2" placeholder="Your answer…"></textarea><div class="row"><button class="primary" id="answer-btn">Answer &amp; resume</button></div></div>`);
  }
  if (x.error) out.push(`<div class="banner bad"><b>Error</b><pre>${esc(x.error)}</pre></div>`);
  const el = $('#t-banners');
  const keep = $('#answer'); const keepVal = keep ? keep.value : '';
  if (keep && document.activeElement === keep) return;
  el.innerHTML = out.join('');
  if ($('#answer')) $('#answer').value = keepVal;
  $$('[data-ap]', el).forEach((b) => (b.onclick = () => act(() => api('POST', `/tasks/${x.id}/approval`,
    { id: x.pending.id, approve: b.dataset.ap !== '0', always: b.dataset.ap === '2' })).then(refreshTask)));
  if ($('#answer-btn')) $('#answer-btn').onclick = async () => {
    const text = $('#answer').value.trim(); if (!text) return;
    await act(() => api('POST', `/tasks/${x.id}/note`, { text: 'Answer to your question: ' + text, run: true })); refreshTask();
  };
}

function drawTabs() {
  const x = T.task;
  const changes = x.changes.filter((c) => !c.reverted).length;
  const tabs = [['overview', 'Overview'], ['chat', 'Chat', x.chat.length], ['plan', 'Plan', x.steps.length], ['activity', 'Activity', T.events.length],
    ['graph', 'Graph'], ['changes', 'Changes', changes], ['llm', 'LLM calls', x.llm_seq], ['report', 'Report']];
  $('#t-tabs').innerHTML = tabs.map(([k, label, n]) => `<button data-tab="${k}" class="${T.tab === k ? 'active' : ''}">${esc(label)}${n ? `<span class="n ${k === 'chat' && T.sum.chat_busy ? 'warn' : ''}">${n}</span>` : ''}</button>`).join('');
  $$('#t-tabs button').forEach((b) => (b.onclick = () => { T.tab = b.dataset.tab; T.planEdit = false; drawTabs(); drawBody(); }));
}

function drawBody() {
  const body = $('#t-body');
  ({ plan: drawPlan, activity: drawActivity, changes: drawChanges, chat: drawTaskChat, graph: drawGraph, llm: drawLLM, report: drawReport }[T.tab] || drawOverview)(body);
}

function listHTML(title, items) {
  if (!items || !items.length) return '';
  return `<div style="margin-bottom:12px"><div class="section-title">${esc(title)}</div><ul class="clean">${items.map((i) => `<li>${esc(i)}</li>`).join('')}</ul></div>`;
}

function drawOverview(body) {
  const x = T.task; const b = x.brief;
  const imgURL = (im) => `/api/tasks/${x.id}/images/${im.id}${S.token ? '?' + tokenQ() : ''}`;
  body.innerHTML = `<div class="stack">
    <div class="card"><div class="card-h"><h3>Brief</h3>${b && b.estimate ? `<span class="badge outline right">est. ${esc(b.estimate)}</span>` : ''}</div>
      ${b ? `<p style="margin-top:0">${esc(b.summary)}</p>
        <div class="grid2"><div>${listHTML('Requirements', b.requirements)}${listHTML('Acceptance criteria', b.acceptance_criteria)}</div>
        <div>${listHTML('Questions / assumptions', b.open_questions)}${listHTML('Risks', b.risks)}
        ${b.relevant_files && b.relevant_files.length ? `<div class="section-title">Relevant files</div><div class="chips">${b.relevant_files.map((f) => `<code class="chip">${esc(f)}</code>`).join('')}</div>` : ''}</div></div>
        ${b.memory && b.memory.length ? `<div style="margin-top:10px">${listHTML('Agent memory (this task)', b.memory)}
          ${x.project_id ? '<button class="sm" id="promote">' + ic('book') + ' Promote to project knowledge</button>' : ''}</div>` : ''}`
        : `<p class="muted" style="margin:0">Not analysed yet. Press <b>Analyze</b> to let the AI read the request, project knowledge and code, then plan the work.</p>`}
    </div>
    <div class="card"><div class="card-h"><h3>Messages</h3><span class="muted small">${x.messages.length}</span></div>
      ${x.messages.map((m) => `<div class="msg"><div class="from"><span>${esc(m.from || '—')}</span><span class="faint">${dt(m.at)}</span>
        <button class="ghost icon right" data-delmsg="${esc(m.id)}">${ic('x')}</button></div><pre>${esc(m.text)}</pre></div>`).join('') || '<p class="muted">—</p>'}
      <div class="row nowrap" style="align-items:flex-start;margin-top:8px">
        <input id="nm-from" placeholder="From" style="max-width:150px">
        <textarea id="nm-text" class="grow" rows="2" placeholder="Add a follow-up message…"></textarea>
        <button id="nm-add">Add</button></div></div>
    <div class="card"><div class="card-h"><h3>Screenshots</h3><span class="muted small">${x.images.length}</span>
        <button class="sm right" id="re-ocr" title="Read screenshots that have no transcript">${ic('search')} Read new screenshots</button></div>
      <div class="shots">${x.images.map((im) => `
        <div class="shot"><img src="${imgURL(im)}" data-full="${imgURL(im)}" alt="${esc(im.name)}" loading="lazy">
          <div class="body"><div class="row small muted nowrap"><span class="ellipsis">${esc(im.name)}</span><span>${im.width ? im.width + '×' + im.height : ''}</span>
            <button class="ghost icon right" data-delimg="${esc(im.id)}">${ic('x')}</button></div>
            <label class="f">Text read from the image (editable)${im.transcribed_by ? ` <span class="hint">by ${esc(im.transcribed_by)}</span>` : ''}
              <textarea data-tr="${esc(im.id)}">${esc(im.transcript)}</textarea></label>
            <button class="sm hidden" data-savetr="${esc(im.id)}">Save</button>
          </div></div>`).join('')}</div>
      <div class="dropzone" id="t-drop" style="margin-top:10px">${ic('image')} Paste (Ctrl+V), drop or click to add screenshots</div>
      <input type="file" id="t-file" accept="image/*" multiple class="hidden"></div></div>`;
  $$('[data-delmsg]', body).forEach((bt) => (bt.onclick = () => act(() => api('DELETE', `/tasks/${x.id}/messages/${bt.dataset.delmsg}`)).then(refreshTask)));
  $('#nm-add').onclick = async () => {
    const text = $('#nm-text').value.trim(); if (!text) return;
    await act(() => api('POST', `/tasks/${x.id}/messages`, { from: $('#nm-from').value, text })); refreshTask();
  };
  if ($('#promote')) $('#promote').onclick = async () => { const r = await act(() => api('POST', `/tasks/${x.id}/promote`)); if (r) toast(`${r.promoted} note(s) added to project knowledge`); };
  $('#re-ocr').onclick = () => act(() => api('POST', `/tasks/${x.id}/transcribe`)).then(refreshTask);
  $$('[data-delimg]', body).forEach((bt) => (bt.onclick = () => confirm('Remove screenshot?') && act(() => api('DELETE', `/tasks/${x.id}/images/${bt.dataset.delimg}`)).then(refreshTask)));
  $$('textarea[data-tr]', body).forEach((ta) => (ta.oninput = () => $(`[data-savetr="${ta.dataset.tr}"]`).classList.remove('hidden')));
  $$('[data-savetr]', body).forEach((bt) => (bt.onclick = async () => {
    const v = $(`textarea[data-tr="${bt.dataset.savetr}"]`).value;
    await act(() => api('PUT', `/tasks/${x.id}/images/${bt.dataset.savetr}`, { transcript: v }), 'Saved'); bt.classList.add('hidden');
  }));
  $$('.shot img', body).forEach((im) => (im.onclick = () => lightbox(im.dataset.full)));
  wireDrop($('#t-drop'), $('#t-file'), async (files) => {
    const fd = new FormData(); files.forEach((f, i) => fd.append('images', f, f.name || `paste-${i}.png`));
    await act(() => api('POST', `/tasks/${x.id}/images`, fd)); refreshTask();
  });
}

// ---- chat (side questions + instructions to the worker)
function chatHTML(msgs, busy, withInstr) {
  return `<div class="chat"><div class="chat-log" id="chat-log">${msgs.map((m) => `<div class="bubble ${esc(m.role)}">${md(m.text)}
      <div class="meta"><span>${dt(m.at)}</span>${m.profile ? `<span>${esc(m.profile)}</span>` : ''}${m.tokens ? `<span>${fmtK(m.tokens)} tok</span>` : ''}
      ${withInstr && m.role === 'assistant' ? `<button class="ghost" data-fwd="${esc(m.id)}">Send to worker as instruction</button>` : ''}</div></div>`).join('') ||
      `<div class="empty">${ic('chat')}Ask anything about this ${withInstr ? 'task' : 'codebase'}: progress, why a change was made, where something lives…<br><span class="small">Answers use read-only tools and never modify files.</span></div>`}
      ${busy ? '<div class="bubble assistant"><span class="spin"></span> thinking…</div>' : ''}</div>
    <div class="chat-in"><textarea id="chat-text" rows="2" placeholder="${withInstr ? 'Ask a side question — or switch to “Instruction” to steer the worker' : 'Ask about the codebase…'}"></textarea>
      <div class="col">${withInstr ? '<div class="seg" id="chat-mode"><button data-m="ask" class="on">Ask</button><button data-m="instr">Instruction</button></div>' : ''}
      <button class="primary" id="chat-send">${ic('send')} Send</button></div></div></div>`;
}
function drawTaskChat(body) {
  const x = T.task;
  const keep = $('#chat-text') ? $('#chat-text').value : '';
  body.innerHTML = chatHTML(x.chat, T.sum.chat_busy, true);
  $('#chat-text').value = keep;
  const log = $('#chat-log'); log.scrollTop = log.scrollHeight;
  let mode = 'ask';
  $$('#chat-mode button').forEach((b) => (b.onclick = () => { mode = b.dataset.m; $$('#chat-mode button').forEach((o) => o.classList.toggle('on', o === b)); }));
  const send = async () => {
    const text = $('#chat-text').value.trim(); if (!text) return;
    if (mode === 'instr') {
      const r = await act(() => api('POST', `/tasks/${x.id}/note`, { text, run: !T.sum.running && x.steps.some((s) => s.status === 'pending') }), 'Instruction sent to the worker');
      if (r) $('#chat-text').value = '';
      return;
    }
    const r = await act(() => api('POST', `/tasks/${x.id}/chat`, { text }));
    if (r) { $('#chat-text').value = ''; setTask(r); drawTaskChat(body); drawTabs(); }
  };
  $('#chat-send').onclick = send;
  $('#chat-text').onkeydown = (e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) send(); };
  $$('[data-fwd]').forEach((b) => (b.onclick = async () => {
    const m = x.chat.find((c) => c.id === b.dataset.fwd);
    await act(() => api('POST', `/tasks/${x.id}/note`, { text: m.text }), 'Sent to the worker');
  }));
}

// ---- plan
function drawPlan(body) {
  const x = T.task;
  const running = !!T.sum.running;
  if (T.planEdit) {
    body.innerHTML = `<div class="steps" id="plan-ed">${x.steps.map(stepEditHTML).join('')}</div>
      <div class="row" style="margin-top:10px"><button id="plan-add">${ic('plus')} Add step</button>
      <button class="right" id="plan-cancel">Cancel</button><button class="primary" id="plan-save">Save plan</button></div>`;
    const wire = () => $$('#plan-ed .stepi').forEach((row) => {
      $('[data-up]', row).onclick = () => { const p = row.previousElementSibling; if (p) row.parentNode.insertBefore(row, p); };
      $('[data-down]', row).onclick = () => { const n = row.nextElementSibling; if (n) row.parentNode.insertBefore(n, row); };
      $('[data-del]', row).onclick = () => row.remove();
    });
    wire();
    $('#plan-add').onclick = () => { $('#plan-ed').insertAdjacentHTML('beforeend', stepEditHTML({ id: '', title: '', detail: '', status: 'pending', files: [] })); wire(); };
    $('#plan-cancel').onclick = () => { T.planEdit = false; drawPlan(body); };
    $('#plan-save').onclick = async () => {
      const steps = $$('#plan-ed .stepi').map((row) => ({
        id: row.dataset.id, title: $('[data-f=title]', row).value, detail: $('[data-f=detail]', row).value,
        status: $('[data-f=status]', row).value, files: $('[data-f=files]', row).value.split(',').map((s) => s.trim()).filter(Boolean),
      }));
      const r = await act(() => api('PATCH', '/tasks/' + x.id, { steps }), 'Plan saved');
      if (r) { T.planEdit = false; setTask(r); drawTask(false); }
    };
    return;
  }
  const s = T.sum;
  body.innerHTML = `
    <div class="row" style="margin-bottom:10px"><span class="muted small">${s.done}/${s.total} steps</span>
      <div class="bar grow ${s.total && s.done === s.total ? 'ok' : ''}"><i style="width:${s.total ? (s.done / s.total) * 100 : 0}%"></i></div>
      <button ${running ? 'disabled title="Pause first"' : ''} id="plan-edit">${ic('edit')} Edit plan</button></div>
    <div class="steps">${x.steps.map((st, i) => `
      <div class="stepi ${st.status}"><div class="num">${st.status === 'done' ? '✓' : i + 1}</div>
        <div><div style="font-weight:600">${esc(st.title)}</div>
          ${st.detail ? `<div class="small muted" style="white-space:pre-wrap">${esc(st.detail)}</div>` : ''}
          ${st.files && st.files.length ? `<div class="chips" style="margin-top:4px">${st.files.map((f) => `<code class="chip">${esc(f)}</code>`).join('')}</div>` : ''}
          ${st.summary ? `<div class="sum">${esc(st.summary)}</div>` : ''}</div>
        <div class="col small muted" style="align-items:flex-end;gap:2px"><span class="badge ${st.status === 'running' ? 'running pulse' : st.status === 'done' ? 'done' : ''}">${esc(st.status)}</span>
          ${st.turns ? `<span>${st.turns} turns · ${fmtK(st.tokens)} tok</span>` : ''}${st.profile ? `<span>${esc(st.profile)}</span>` : ''}</div>
      </div>`).join('') || empty('list', 'No plan yet — analyze the task, or add steps manually')}</div>`;
  $('#plan-edit').onclick = () => { T.planEdit = true; drawPlan(body); };
}
function stepEditHTML(st) {
  return `<div class="stepi" data-id="${esc(st.id)}"><div class="col" style="gap:2px"><button class="sm ghost" data-up>▲</button><button class="sm ghost" data-down>▼</button></div>
    <div class="col"><input data-f="title" value="${esc(st.title)}" placeholder="Step title">
      <textarea data-f="detail" rows="2" placeholder="Detail / how to verify">${esc(st.detail)}</textarea>
      <input data-f="files" value="${esc((st.files || []).join(', '))}" placeholder="files, comma separated"></div>
    <div class="col"><select data-f="status">${['pending', 'done', 'skipped'].map((s) => `<option ${st.status === s ? 'selected' : ''}>${s}</option>`).join('')}</select>
      <button class="sm danger" data-del>${ic('trash')}</button></div></div>`;
}

// ---- activity
function evHTML(ev) {
  const d = ev.data || {};
  let extra = '';
  if (ev.type === 'llm') extra = `<span class="tok">in ${fmtN(d.input)}${d.cache_read ? ` · cache ${fmtN(d.cache_read)}` : ''} · out ${fmtN(d.output)} · ${((d.ms || 0) / 1000).toFixed(1)}s</span>`;
  if (d.decided_by) extra = `<span class="tok">by ${esc(d.decided_by)}</span>`;
  const title = `<span class="ttl">${esc(ev.title)}</span>${extra}`;
  const body = ev.detail ? `<details${ev.type === 'question' || ev.type === 'error' ? ' open' : ''}><summary>${title}</summary><pre>${esc(ev.detail)}</pre></details>` : `<div>${title}</div>`;
  return `<div class="ev k-${esc(ev.type)}"><span class="time">${time(ev.at)}</span><span class="ty">${esc(ev.type)}</span>${body}</div>`;
}
function drawActivity(body) {
  const types = ['stage', 'step', 'llm', 'tool', 'change', 'approval', 'question', 'chat', 'note', 'error', 'info'];
  const evs = T.events.filter((e) => !T.evFilter || e.type === T.evFilter);
  body.innerHTML = `<div class="filters"><button class="${T.evFilter ? '' : 'on'}" data-f="">All</button>${types.map((t) => `<button data-f="${t}" class="${T.evFilter === t ? 'on' : ''}">${t}</button>`).join('')}</div>
    <div class="card" style="padding:8px 12px"><div class="log" id="log">${evs.map(evHTML).join('') || empty('wave', 'No activity yet')}</div></div>`;
  $$('.filters button', body).forEach((b) => (b.onclick = () => { T.evFilter = b.dataset.f; drawActivity(body); }));
  const log = $('#log'); log.scrollTop = log.scrollHeight;
}
function appendEvent(ev) {
  const log = $('#log'); if (!log || (T.evFilter && ev.type !== T.evFilter)) return;
  const stick = log.scrollHeight - log.scrollTop - log.clientHeight < 60;
  log.insertAdjacentHTML('beforeend', evHTML(ev));
  if (stick) log.scrollTop = log.scrollHeight;
}

// ---- changes
async function drawChanges(body) {
  const x = T.task;
  if (!T.diffs) {
    body.innerHTML = '<p class="muted"><span class="spin"></span></p>';
    T.diffs = (await act(() => api('GET', `/tasks/${x.id}/diff`))) || [];
    if (T.tab !== 'changes') return;
  }
  if (!T.diffs.length) { body.innerHTML = `<div class="card">${empty('git', 'The agent has not changed any file yet')}</div>`; return; }
  body.innerHTML = `<div class="row"><span class="muted small mono">${esc(T.ws)}</span>
      <button class="danger right" id="rev-all" ${T.sum.running ? 'disabled' : ''}>${ic('refresh')} Revert all</button></div>
    ${T.diffs.map((d) => `<div class="file-head"><span class="badge ${d.op === 'create' ? 'done' : d.op === 'delete' ? 'err' : 'planned'}">${esc(d.op)}</span>
      <code>${esc(d.path)}</code><button class="sm right" data-rev="${esc(d.path)}" ${T.sum.running ? 'disabled' : ''}>Revert</button></div>
      <div class="diff">${diffHTML(d.diff)}</div>`).join('')}`;
  const doRevert = async (path) => {
    if (!confirm(`Revert ${path || 'all files'} to the original?`)) return;
    await act(() => api('POST', `/tasks/${x.id}/revert`, { path })); T.diffs = null; refreshTask(); drawChanges(body);
  };
  $('#rev-all').onclick = () => doRevert('');
  $$('[data-rev]', body).forEach((b) => (b.onclick = () => doRevert(b.dataset.rev)));
}
function diffHTML(d) {
  if (!d) return '<div class="hdr">(no textual difference)</div>';
  return d.split('\n').map((l) => {
    const c = l.startsWith('@@') ? 'hunk' : l.startsWith('+++') || l.startsWith('---') ? 'hdr' : l[0] === '+' ? 'add' : l[0] === '-' ? 'del' : '';
    return `<div class="${c}">${esc(l) || ' '}</div>`;
  }).join('');
}

// ---- LLM calls
async function drawLLM(body) {
  if (!T.llm) {
    body.innerHTML = '<p class="muted"><span class="spin"></span></p>';
    T.llm = (await act(() => api('GET', `/tasks/${T.task.id}/llm`))) || [];
    if (T.tab !== 'llm') return;
  }
  body.innerHTML = llmTable(T.llm.slice().reverse(), false);
  wireLLMTable(body);
}
function llmTable(rows, withTask) {
  if (!rows.length) return `<div class="card">${empty('wave', 'No recorded LLM calls (enable “Record LLM calls” in Settings)')}</div>`;
  return `<div class="table-wrap"><table class="t"><thead><tr><th>#</th><th>Time</th>${withTask ? '<th>Task</th>' : ''}<th>Agent</th><th>Provider</th>
    <th class="num">In</th><th class="num">Cache</th><th class="num">Out</th><th class="num">ms</th><th></th></tr></thead><tbody>
    ${rows.map((r, i) => `<tr class="click" data-ix="${i}"><td class="small muted">${r.seq}</td><td class="small">${dt(r.at)}</td>
      ${withTask ? `<td class="small ellipsis" style="max-width:220px"><a href="#/task/${esc(r.task_id)}">${esc(r.task_title || r.task_id)}</a></td>` : ''}
      <td><span class="badge">${esc(r.role)}</span></td><td class="small">${esc(r.profile)} <span class="faint">${esc(r.model)}</span></td>
      <td class="num small">${fmtN(r.usage.input)}</td><td class="num small">${fmtN(r.usage.cache_read)}</td><td class="num small">${fmtN(r.usage.output)}</td>
      <td class="num small">${fmtN(r.ms)}</td><td>${r.error ? '<span class="badge err">error</span>' : ''}</td></tr>
      <tr class="hidden" data-detail="${i}"><td colspan="${withTask ? 10 : 9}"><div class="grid2">
        <div><div class="section-title">Request (last turn · ${r.messages} msgs · ${r.tools} tools · system ${fmtK(r.system_len)} chars)</div><pre class="code" style="padding:8px">${esc(r.request)}</pre></div>
        <div><div class="section-title">Response</div><pre class="code" style="padding:8px">${esc(r.error || r.response)}</pre></div></div></td></tr>`).join('')}
    </tbody></table></div>`;
}
function wireLLMTable(root) {
  $$('tr[data-ix]', root).forEach((tr) => (tr.onclick = (e) => { if (e.target.closest('a')) return; $(`tr[data-detail="${tr.dataset.ix}"]`, root).classList.toggle('hidden'); }));
}

// ---- report
async function drawReport(body) {
  body.innerHTML = '<p class="muted"><span class="spin"></span></p>';
  const mdText = await act(() => api('GET', `/tasks/${T.task.id}/report`));
  if (mdText === null || T.tab !== 'report') return;
  body.innerHTML = `<div class="row" style="margin-bottom:10px"><span class="muted small">Markdown report: request, brief, plan, diffs, timeline, usage.</span>
    <a class="btn right" href="/api/tasks/${esc(T.task.id)}/report?download=1${S.token ? '&' + tokenQ() : ''}">${ic('download')} Download .md</a>
    <button id="rep-copy">${ic('copy')} Copy</button></div><div class="card">${md(mdText)}</div>`;
  $('#rep-copy').onclick = () => navigator.clipboard.writeText(mdText).then(() => toast('Copied'));
}

// ---- graph (request → brief → steps → changes / knowledge / questions)
const G = { scale: 1, x: 0, y: 0 };
const NODE_COLORS = { request: '#6a6359', image: '#2f62c9', brief: '#2e8b57', step: '#6b4fd8', running: '#e5322d', change: '#0f8577', memory: '#c77700', question: '#b42318', review: '#1a1a1a' };
function graphModel() {
  const x = T.task; const nodes = []; const edges = [];
  const add = (id, type, title, sub, col, row) => nodes.push({ id, type, title, sub, col, row });
  x.messages.forEach((m, i) => add('m' + i, 'request', m.from || 'message', m.text, 0, i));
  x.images.forEach((im, i) => add('i' + i, 'image', im.name, im.transcript ? 'transcribed' : 'not read yet', 0, x.messages.length + i));
  const inputs = nodes.map((n) => n.id);
  if (x.brief) {
    add('brief', 'brief', 'Brief', x.brief.summary, 1, 0);
    inputs.forEach((id) => edges.push([id, 'brief', 'spawns']));
  }
  let prev = x.brief ? 'brief' : null;
  x.steps.forEach((s, i) => {
    const id = 's' + i;
    add(id, s.status === 'running' ? 'running' : 'step', `${i + 1}. ${s.title}`, s.status + (s.summary ? ' — ' + s.summary : ''), 2 + i, 0);
    if (prev) edges.push([prev, id, 'chain']); else inputs.forEach((n) => edges.push([n, id, 'spawns']));
    prev = id;
    x.changes.filter((c) => c.step === s.id && !c.reverted).forEach((c, k) => { add(`c${i}_${k}`, 'change', c.path, c.op, 2 + i, k + 1); edges.push([id, `c${i}_${k}`, 'yields']); });
  });
  const mem = (x.brief && x.brief.memory) || [];
  mem.forEach((mm, i) => { add('k' + i, 'memory', 'memory', mm, 1, i + 1); if (x.brief) edges.push(['brief', 'k' + i, 'yields']); });
  const qs = T.events.filter((e) => e.type === 'question');
  qs.forEach((q, i) => {
    const si = x.steps.findIndex((s) => s.id === q.step);
    const col = si >= 0 ? 2 + si : 1;
    const row = nodes.filter((n) => n.col === col).length;
    add('q' + i, 'question', 'question', q.detail, col, row);
    edges.push([si >= 0 ? 's' + si : 'brief', 'q' + i, 'asks']);
  });
  if (x.stage === 'review' || x.stage === 'done') { add('end', 'review', x.stage === 'done' ? 'Done' : 'Review', T.sum.changes + ' file(s) changed', 2 + x.steps.length, 0); if (prev) edges.push([prev, 'end', 'chain']); }
  return { nodes, edges };
}
function drawGraph(body, keepView) {
  const { nodes, edges } = graphModel();
  if (!nodes.length) { body.innerHTML = `<div class="card">${empty('graph', 'Nothing to draw yet')}</div>`; return; }
  const W = 200, H = 64, CX = 250, RY = 90;
  const pos = {}; nodes.forEach((n) => (pos[n.id] = { x: 40 + n.col * CX, y: 60 + n.row * RY }));
  const maxX = Math.max(...nodes.map((n) => pos[n.id].x)) + W + 40, maxY = Math.max(...nodes.map((n) => pos[n.id].y)) + H + 60;
  const ecol = { spawns: '#2e8b57', chain: '#6b4fd8', yields: '#c77700', asks: '#b42318' };
  const svgEdges = edges.filter(([a, b]) => pos[a] && pos[b]).map(([a, b, k]) => {
    const p = pos[a], q = pos[b];
    const x1 = p.x + W, y1 = p.y + H / 2, x2 = q.x, y2 = q.y + H / 2;
    const sameCol = Math.abs(p.x - q.x) < 5;
    const d = sameCol ? `M${p.x + W / 2},${p.y + H} L${q.x + W / 2},${q.y}` : `M${x1},${y1} C${x1 + 50},${y1} ${x2 - 50},${y2} ${x2},${y2}`;
    return `<path d="${d}" stroke="${ecol[k]}" stroke-width="1.6" fill="none" opacity=".75"/>`;
  }).join('');
  const clip = (s, n) => { s = String(s || '').replace(/\s+/g, ' '); return s.length > n ? s.slice(0, n) + '…' : s; };
  const svgNodes = nodes.map((n) => {
    const p = pos[n.id]; const c = NODE_COLORS[n.type];
    return `<g transform="translate(${p.x},${p.y})"><title>${esc(n.title)}\n${esc(n.sub)}</title>
      <rect x="3" y="3" width="${W}" height="${H}" rx="6" fill="var(--shc)"/><rect width="${W}" height="${H}" rx="6" fill="var(--panel)" stroke="var(--bc)" stroke-width="1.5"/><rect width="4" height="${H}" rx="1" fill="${c}"/>
      <text x="12" y="20" font-size="10" font-weight="700" fill="${c}" style="text-transform:uppercase">${esc(n.type)}</text>
      <text x="12" y="36" font-size="12" font-weight="600" fill="var(--text)">${esc(clip(n.title, 28))}</text>
      <text x="12" y="52" font-size="11" fill="var(--muted)">${esc(clip(n.sub, 32))}</text></g>`;
  }).join('');
  if (!keepView || !$('#gsvg')) { G.scale = Math.min(1, 900 / maxX); G.x = 0; G.y = 50; }
  body.innerHTML = `<div class="graph-wrap"><svg class="g" id="gsvg"><g id="gview">${svgEdges}${svgNodes}</g></svg>
    <div class="legend">${Object.entries({ Request: 'request', Screenshot: 'image', Brief: 'brief', Step: 'step', Change: 'change', Memory: 'memory', Question: 'question' })
      .map(([l, k]) => `<span><i style="background:${NODE_COLORS[k]}"></i>${l}</span>`).join('')}</div>
    <div class="zoom"><button id="gz-in">+</button><button id="gz-out">−</button><button id="gz-fit">⤢</button></div></div>`;
  const apply = () => $('#gview').setAttribute('transform', `translate(${G.x},${G.y}) scale(${G.scale})`);
  apply();
  const svg = $('#gsvg');
  let drag = null;
  svg.onmousedown = (e) => { drag = { x: e.clientX - G.x, y: e.clientY - G.y }; };
  window.onmouseup = () => { drag = null; };
  svg.onmousemove = (e) => { if (drag) { G.x = e.clientX - drag.x; G.y = e.clientY - drag.y; apply(); } };
  svg.onwheel = (e) => { e.preventDefault(); const f = e.deltaY < 0 ? 1.1 : 0.9; G.scale = Math.min(3, Math.max(0.2, G.scale * f)); apply(); };
  $('#gz-in').onclick = () => { G.scale = Math.min(3, G.scale * 1.2); apply(); };
  $('#gz-out').onclick = () => { G.scale = Math.max(0.2, G.scale / 1.2); apply(); };
  $('#gz-fit').onclick = () => { const r = svg.getBoundingClientRect(); G.scale = Math.min(r.width / maxX, (r.height - 50) / maxY, 1.5); G.x = 0; G.y = 50; apply(); };
}

// ---- sidebar
function drawSide() {
  const x = T.task; const u = x.usage;
  const side = $('#t-side');
  if ($('#note') && document.activeElement === $('#note')) { $('#side-usage').innerHTML = usageHTML(u, x.turns); return; }
  const noteVal = $('#note') ? $('#note').value : '';
  const canRun = !T.sum.running && x.steps.some((s) => s.status === 'pending');
  side.innerHTML = `
    <div class="card"><div class="section-title">Instruction for the agent</div>
      <textarea id="note" rows="3" placeholder="e.g. reuse the existing Button component, don't change the API…"></textarea>
      <div class="row" style="margin-top:6px">${x.stage === 'review' || x.stage === 'done' ? `<button class="primary" id="note-rework">Request changes</button>` :
        `<button id="note-send">Send</button>${canRun ? '<button class="primary" id="note-run">Send &amp; resume</button>' : ''}`}</div></div>
    <div class="card"><div class="section-title">Token usage</div><div id="side-usage">${usageHTML(u, x.turns)}</div></div>
    <div class="card"><div class="section-title">Project</div>
      <select id="t-proj">${projectOptions(x.project_id)}</select>
      ${T.proj ? `<div class="small muted" style="margin-top:6px">${T.proj.knowledge} knowledge notes · ${T.proj.commands} commands</div>` : ''}
      ${T.ws ? `<div class="mono small muted" style="margin-top:4px;overflow-wrap:anywhere">${esc(T.ws)}</div>` : ''}</div>
    <div class="card"><div class="section-title">Models</div>
      <label class="f">Analyzer<select id="ov-an">${profileOptions(x.overrides.analyzer)}</select></label>
      <label class="f" style="margin-top:6px">Worker<select id="ov-wk">${profileOptions(x.overrides.worker)}</select></label>
      <label class="chk small" style="margin-top:8px">${sw('rot', x.rotate)} Multi-LLM worker rotation</label></div>
    <div class="card"><div class="section-title">Details</div><div class="grid2">
      <label class="f">Priority<select id="prio">${['low', 'normal', 'high', 'urgent'].map((p) => `<option ${x.priority === p ? 'selected' : ''}>${p}</option>`).join('')}</select></label>
      <label class="f">Due<input type="date" id="due" value="${esc(x.due || '')}"></label></div>
      <label class="f" style="margin-top:6px">Tags<input id="tags" value="${esc((x.tags || []).join(', '))}"></label>
      <dl class="kv small" style="margin-top:10px"><dt>Created</dt><dd>${dt(x.created_at)}</dd><dt>Updated</dt><dd>${dt(x.updated_at)}</dd>${x.done_at ? `<dt>Done</dt><dd>${dt(x.done_at)}</dd>` : ''}</dl></div>`;
  $('#note').value = noteVal;
  const send = async (opts) => {
    const text = $('#note').value.trim(); if (!text) return;
    const r = await act(() => api('POST', `/tasks/${x.id}/note`, { text, ...opts }));
    if (r) { $('#note').value = ''; refreshTask(); }
  };
  if ($('#note-send')) $('#note-send').onclick = () => send({});
  if ($('#note-run')) $('#note-run').onclick = () => send({ run: true });
  if ($('#note-rework')) $('#note-rework').onclick = () => send({ rework: true, run: true });
  $('#t-proj').onchange = async (e) => { await act(() => api('POST', `/tasks/${x.id}/source`, { project_id: e.target.value }), 'Project changed'); T.diffs = null; refreshTask(); };
  const ov = () => act(() => api('PATCH', '/tasks/' + x.id, { overrides: { analyzer: $('#ov-an').value, worker: $('#ov-wk').value } }));
  $('#ov-an').onchange = ov; $('#ov-wk').onchange = ov;
  $('#rot').onchange = (e) => act(() => api('PATCH', '/tasks/' + x.id, { rotate: e.target.checked }));
  $('#prio').onchange = (e) => act(() => api('PATCH', '/tasks/' + x.id, { priority: e.target.value }));
  $('#due').onchange = (e) => act(() => api('PATCH', '/tasks/' + x.id, { due: e.target.value }));
  $('#tags').onchange = (e) => act(() => api('PATCH', '/tasks/' + x.id, { tags: e.target.value.split(',').map((s) => s.trim()).filter(Boolean) }));
}
function usageHTML(u, turns) {
  return `<dl class="kv"><dt>Input</dt><dd>${fmtN(u.input)}</dd><dt>Output</dt><dd>${fmtN(u.output)}</dd>
    <dt>Cache read</dt><dd>${fmtN(u.cache_read)}</dd><dt>Cache write</dt><dd>${fmtN(u.cache_write)}</dd>
    <dt>LLM calls</dt><dd>${fmtN(u.calls)}</dd><dt>Worker turns</dt><dd>${fmtN(turns)}</dd>${u.cost ? `<dt>Cost</dt><dd>$${u.cost.toFixed(4)}</dd>` : ''}</dl>`;
}
