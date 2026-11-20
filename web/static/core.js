/* AATMT web UI — core: state, icons, API, router, live updates, search palette. Plain JS, no build step. */
'use strict';

const S = { token: localStorage.getItem('token') || '', cfg: null, projects: [], view: '', onEvent: null, pending: 0 };
const STAGES = ['inbox', 'analyzing', 'planned', 'running', 'paused', 'review', 'done'];
const STAGE_LABEL = { inbox: 'Inbox', analyzing: 'Analyzing', planned: 'Planned', running: 'In progress', paused: 'Paused', review: 'Review', done: 'Done' };
const STAGE_COLOR = { inbox: '#9aa0ac', analyzing: '#7c3aed', planned: '#2563eb', running: '#4f46e5', paused: '#d97706', review: '#db2777', done: '#16a34a' };

// ---------------------------------------------------------------- icons (24px stroke icons)
const ICONS = {
  dashboard: '<rect x="3" y="3" width="7" height="9" rx="1"/><rect x="14" y="3" width="7" height="5" rx="1"/><rect x="14" y="12" width="7" height="9" rx="1"/><rect x="3" y="16" width="7" height="5" rx="1"/>',
  tasks: '<path d="M9 6h11M9 12h11M9 18h11"/><path d="m3 6 1.5 1.5L7 5M3 12l1.5 1.5L7 11M3 18l1.5 1.5L7 17"/>',
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  chat: '<path d="M21 12a8 8 0 0 1-11.6 7.1L4 20l1-4.6A8 8 0 1 1 21 12z"/>',
  shield: '<path d="M12 3 4 6v6c0 4.5 3.4 8.2 8 9 4.6-.8 8-4.5 8-9V6z"/>',
  check: '<path d="m5 12 4.5 4.5L19 7"/>',
  shieldcheck: '<path d="M12 3 4 6v6c0 4.5 3.4 8.2 8 9 4.6-.8 8-4.5 8-9V6z"/><path d="m9 12 2 2 4-4"/>',
  wave: '<path d="M3 12h3l3-7 6 14 3-7h3"/>',
  cpu: '<rect x="6" y="6" width="12" height="12" rx="2"/><path d="M9 2v4M15 2v4M9 18v4M15 18v4M2 9h4M2 15h4M18 9h4M18 15h4"/>',
  bot: '<rect x="4" y="8" width="16" height="12" rx="2"/><path d="M12 4v4M9 13v1M15 13v1M9 17h6"/><circle cx="12" cy="3" r="1"/>',
  sparkles: '<path d="M12 3v4M12 17v4M3 12h4M17 12h4M6 6l2.5 2.5M15.5 15.5 18 18M6 18l2.5-2.5M15.5 8.5 18 6"/>',
  bell: '<path d="M6 9a6 6 0 0 1 12 0c0 6 2.5 8 2.5 8h-17S6 15 6 9z"/><path d="M10 20a2 2 0 0 0 4 0"/>',
  copy: '<rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2"/>',
  file: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5M9 13h6M9 17h6"/>',
  sliders: '<path d="M4 6h10M18 6h2M4 12h4M12 12h8M4 18h12M20 18h0"/><circle cx="16" cy="6" r="2"/><circle cx="10" cy="12" r="2"/><circle cx="18" cy="18" r="2"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  play: '<path d="M7 5v14l11-7z"/>',
  pause: '<path d="M8 5v14M16 5v14"/>',
  archive: '<rect x="3" y="4" width="18" height="4" rx="1"/><path d="M5 8v11a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8M10 12h4"/>',
  pin: '<path d="M9 4h6l-1 6 3 3H7l3-3zM12 13v8"/>',
  trash: '<path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/>',
  download: '<path d="M12 4v11M7 10l5 5 5-5M5 20h14"/>',
  refresh: '<path d="M20 11a8 8 0 0 0-14.9-3.5M4 4v4h4M4 13a8 8 0 0 0 14.9 3.5M20 20v-4h-4"/>',
  eye: '<path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
  back: '<path d="M19 12H5M11 6l-6 6 6 6"/>',
  x: '<path d="M6 6l12 12M18 6 6 18"/>',
  git: '<circle cx="6" cy="6" r="2"/><circle cx="6" cy="18" r="2"/><circle cx="18" cy="8" r="2"/><path d="M6 8v8M18 10c0 4-6 3-10 6"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
  moon: '<path d="M20 14.5A8 8 0 1 1 9.5 4 6.5 6.5 0 0 0 20 14.5z"/>',
  panel: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M9 4v16"/>',
  send: '<path d="m4 12 16-8-6 16-2-7z"/>',
  edit: '<path d="M4 20h4L19 9l-4-4L4 16z"/>',
  graph: '<circle cx="5" cy="12" r="2"/><circle cx="19" cy="5" r="2"/><circle cx="19" cy="19" r="2"/><path d="M7 11l10-5M7 13l10 5"/>',
  zap: '<path d="M13 2 4 14h7l-1 8 9-12h-7z"/>',
  terminal: '<path d="m5 8 4 4-4 4M12 17h7"/>',
  book: '<path d="M4 5a2 2 0 0 1 2-2h13v16H6a2 2 0 0 0-2 2z"/><path d="M4 19V5"/>',
  star: '<path d="m12 3 2.8 5.7 6.2.9-4.5 4.4 1 6.2L12 17.3 6.5 20.2l1-6.2L3 9.6l6.2-.9z"/>',
  image: '<rect x="3" y="4" width="18" height="16" rx="2"/><circle cx="9" cy="10" r="2"/><path d="m21 16-5-5-9 9"/>',
  camera: '<path d="M4 8h3l2-3h6l2 3h3v11H4z"/><circle cx="12" cy="13" r="3.5"/>',
  layers: '<path d="m12 3 9 5-9 5-9-5z"/><path d="m3 13 9 5 9-5"/>',
  list: '<path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/>',
  kanban: '<rect x="3" y="4" width="5" height="16" rx="1"/><rect x="10" y="4" width="5" height="10" rx="1"/><rect x="17" y="4" width="4" height="13" rx="1"/>',
  more: '<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>',
};
// Brutalist mark: hard offset shadow + thick outline in the theme's ink, acid-yellow face, black block "A", red task stamp.
const LOGO = `<svg viewBox="0 0 64 64" aria-label="AATMT"><rect x="10" y="10" width="51" height="51" fill="currentColor"/>
  <rect x="3.5" y="3.5" width="50" height="50" fill="#FFE500" stroke="currentColor" stroke-width="5"/>
  <path fill="#111" fill-rule="evenodd" d="M11 47 22.5 12h11L45 47h-9.5l-2.3-7.5h-10.4L20.5 47zm14.6-15h5L28.1 23.5z"/>
  <rect x="40" y="9" width="10" height="10" fill="#FF3B1F" stroke="currentColor" stroke-width="3"/></svg>`;
const ic = (n, cls = '') => `<svg class="i ${cls}" viewBox="0 0 24 24">${ICONS[n] || ''}</svg>`;

// ---------------------------------------------------------------- utils
const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const fmtN = (n) => (n || 0).toLocaleString('en-US');
const fmtK = (n) => (n >= 1e6 ? (n / 1e6).toFixed(1) + 'M' : n >= 1e3 ? (n / 1e3).toFixed(1) + 'k' : String(n || 0));
const ago = (d) => {
  if (!d) return '—';
  const s = (Date.now() - new Date(d)) / 1000;
  if (s < 60) return Math.max(1, s | 0) + 's ago'; if (s < 3600) return (s / 60 | 0) + 'm ago';
  if (s < 86400) return (s / 3600 | 0) + 'h ago'; return (s / 86400 | 0) + 'd ago';
};
const time = (d) => new Date(d).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
const dt = (d) => (d ? new Date(d).toLocaleString('en-GB', { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit' }) : '—');
function debounce(fn, ms) { let h; return (...a) => { clearTimeout(h); h = setTimeout(() => fn(...a), ms); }; }
function toast(msg, err) {
  const el = document.createElement('div'); el.className = 'toast' + (err ? ' err' : ''); el.textContent = msg;
  $('#toasts').appendChild(el); setTimeout(() => el.remove(), err ? 7000 : 3000);
}
const usageTotal = (u) => (u ? (u.input || 0) + (u.output || 0) + (u.cache_read || 0) + (u.cache_write || 0) : 0);
const stageBadge = (st, extra = '') => `<span class="badge dot ${esc(st)} ${extra}">${esc(STAGE_LABEL[st] || st)}</span>`;
const empty = (icon, text) => `<div class="empty">${ic(icon)}${esc(text)}</div>`;
const sw = (id, on) => `<label class="switch"><input type="checkbox" id="${id}" ${on ? 'checked' : ''}><span></span></label>`;
const tokenQ = () => (S.token ? 'token=' + encodeURIComponent(S.token) : '');

async function api(method, path, body) {
  const opt = { method, headers: { 'X-AATMT': '1' } };
  if (S.token) opt.headers.Authorization = 'Bearer ' + S.token;
  if (body instanceof FormData) opt.body = body;
  else if (body !== undefined) { opt.body = JSON.stringify(body); opt.headers['Content-Type'] = 'application/json'; }
  const res = await fetch('/api' + path, opt);
  const ct = res.headers.get('Content-Type') || '';
  let data = null;
  if (ct.includes('json')) { try { data = await res.json(); } catch { /* empty */ } } else data = await res.text();
  if (res.status === 401) {
    const tok = prompt('Access token (AATMT_TOKEN):');
    if (tok) { S.token = tok; localStorage.setItem('token', tok); return api(method, path, body); }
  }
  if (!res.ok) throw new Error((data && data.error) || (typeof data === 'string' && data) || res.statusText);
  return data;
}
async function act(fn, okMsg) {
  try { const r = await fn(); if (okMsg) toast(okMsg); return r; } catch (e) { toast(e.message, true); return null; }
}
async function loadConfig() { const c = await act(() => api('GET', '/config')); if (c) S.cfg = c; return c; }
async function loadProjects() { const p = await act(() => api('GET', '/projects')); if (p) S.projects = p; return p; }
const profileName = (id) => { const p = S.cfg && S.cfg.profiles.find((x) => x.id === id); return p ? p.name : id; };
const profileOptions = (sel, def = '(role default)') => `<option value="">${esc(def)}</option>` +
  (S.cfg ? S.cfg.profiles.map((p) => `<option value="${esc(p.id)}" ${sel === p.id ? 'selected' : ''}>${esc(p.name)}</option>`).join('') : '');
const projectOptions = (sel, none = '— no project —') => `<option value="">${esc(none)}</option>` +
  S.projects.filter((p) => !p.archived || p.id === sel).map((p) => `<option value="${esc(p.id)}" ${sel === p.id ? 'selected' : ''}>${esc(p.name)}</option>`).join('');

// ---------------------------------------------------------------- markdown (small, safe: escapes first)
function md(src) {
  const lines = String(src || '').replace(/\r/g, '').split('\n');
  let out = '', i = 0;
  const inline = (s) => esc(s)
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*]+)\*\*/g, '<b>$1</b>')
    .replace(/(^|[^*])\*([^*\s][^*]*)\*/g, '$1<i>$2</i>')
    .replace(/\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer" style="color:var(--accent)">$1</a>');
  while (i < lines.length) {
    const l = lines[i];
    if (l.startsWith('```')) {
      const buf = []; i++;
      while (i < lines.length && !lines[i].startsWith('```')) buf.push(lines[i++]);
      i++; out += `<pre><code>${esc(buf.join('\n'))}</code></pre>`; continue;
    }
    const h = l.match(/^(#{1,3})\s+(.*)/);
    if (h) { out += `<h${h[1].length}>${inline(h[2])}</h${h[1].length}>`; i++; continue; }
    if (/^\s*([-*]|\d+\.)\s+/.test(l)) {
      const ordered = /^\s*\d+\./.test(l); const items = [];
      while (i < lines.length && /^\s*([-*]|\d+\.)\s+/.test(lines[i])) items.push(lines[i++].replace(/^\s*([-*]|\d+\.)\s+/, '').replace(/^\[( |x)\]\s*/, (m, c) => (c === 'x' ? '☑ ' : '☐ ')));
      out += `<${ordered ? 'ol' : 'ul'}>${items.map((x) => `<li>${inline(x)}</li>`).join('')}</${ordered ? 'ol' : 'ul'}>`; continue;
    }
    if (l.startsWith('>')) {
      const buf = [];
      while (i < lines.length && lines[i].startsWith('>')) buf.push(lines[i++].replace(/^>\s?/, ''));
      out += `<blockquote>${buf.map(inline).join('<br>')}</blockquote>`; continue;
    }
    if (l.startsWith('|') && i + 1 < lines.length && /^\|[\s:|-]+\|$/.test(lines[i + 1].trim())) {
      const row = (r) => r.trim().replace(/^\||\|$/g, '').split('|').map((c) => c.trim());
      const head = row(l); i += 2; const body = [];
      while (i < lines.length && lines[i].startsWith('|')) body.push(row(lines[i++]));
      out += `<table><tr>${head.map((c) => `<th>${inline(c)}</th>`).join('')}</tr>${body.map((r) => `<tr>${r.map((c) => `<td>${inline(c)}</td>`).join('')}</tr>`).join('')}</table>`; continue;
    }
    if (/^---+$/.test(l.trim())) { out += '<hr style="border:none;border-top:1px solid var(--line)">'; i++; continue; }
    if (!l.trim()) { i++; continue; }
    const buf = [];
    while (i < lines.length && lines[i].trim() && !/^(```|#{1,3}\s|>|\s*([-*]|\d+\.)\s+|\|)/.test(lines[i])) buf.push(lines[i++]);
    if (!buf.length) { buf.push(lines[i++]); }
    out += `<p>${buf.map(inline).join('<br>')}</p>`;
  }
  return `<div class="md">${out}</div>`;
}

// ---------------------------------------------------------------- modal
function modal(html, cls = '') {
  const root = $('#modal-root');
  root.innerHTML = `<div class="modal-bg"><div class="modal ${cls}">${html}</div></div>`;
  const close = () => { root.innerHTML = ''; if (S.onModalClose) { S.onModalClose(); S.onModalClose = null; } };
  $('.modal-bg', root).onmousedown = (e) => { if (e.target.classList.contains('modal-bg')) close(); };
  $$('[data-close]', root).forEach((b) => (b.onclick = close));
  return { root, close };
}
function lightbox(src) {
  const d = document.createElement('div'); d.className = 'lightbox'; d.innerHTML = `<img src="${esc(src)}" alt="">`;
  d.onclick = () => d.remove(); document.body.appendChild(d);
}
function wireDrop(zone, input, onFiles) {
  zone.onclick = () => input.click();
  input.onchange = () => { onFiles([...input.files]); input.value = ''; };
  zone.ondragover = (e) => { e.preventDefault(); zone.classList.add('over'); };
  zone.ondragleave = () => zone.classList.remove('over');
  zone.ondrop = (e) => { e.preventDefault(); zone.classList.remove('over'); onFiles([...e.dataTransfer.files].filter((f) => f.type.startsWith('image/'))); };
}

// ---------------------------------------------------------------- source form (shared)
function sourceFormHTML(p) {
  return `<div class="seg" id="${p}-kind">${[['none', 'None'], ['path', 'Local folder'], ['git', 'Git URL'], ['upload', 'Upload .zip']]
    .map(([k, l]) => `<button type="button" data-k="${k}">${l}</button>`).join('')}</div>
    <input id="${p}-path" class="hidden" placeholder="/home/me/projects/my-app   or   C:\\code\\my-app">
    <div id="${p}-gitbox" class="row nowrap hidden"><input id="${p}-git" class="grow" placeholder="https://github.com/org/repo.git"><input id="${p}-ref" style="max-width:160px" placeholder="branch (optional)"></div>
    <input id="${p}-zip" type="file" accept=".zip" class="hidden">`;
}
function wireSourceForm(p, initial = 'none') {
  const set = (k) => {
    $(`#${p}-kind`).dataset.v = k;
    $$(`#${p}-kind button`).forEach((b) => b.classList.toggle('on', b.dataset.k === k));
    $(`#${p}-path`).classList.toggle('hidden', k !== 'path');
    $(`#${p}-gitbox`).classList.toggle('hidden', k !== 'git');
    $(`#${p}-zip`).classList.toggle('hidden', k !== 'upload');
  };
  $$(`#${p}-kind button`).forEach((b) => (b.onclick = () => set(b.dataset.k)));
  set(initial);
}
function sourceFormData(p, fd = new FormData()) {
  const k = $(`#${p}-kind`).dataset.v || 'none';
  fd.append('source_kind', k);
  if (k === 'path') fd.append('source_path', $(`#${p}-path`).value);
  if (k === 'git') { fd.append('git_url', $(`#${p}-git`).value); fd.append('git_ref', $(`#${p}-ref`).value); }
  if (k === 'upload' && $(`#${p}-zip`).files[0]) fd.append('zip', $(`#${p}-zip`).files[0]);
  return fd;
}

// ---------------------------------------------------------------- shell, router
const NAV_MAIN = [['', 'dashboard', 'Dashboard'], ['tasks', 'tasks', 'Tasks'], ['projects', 'folder', 'Projects'], ['chat', 'chat', 'Chat'],
  ['approvals', 'shieldcheck', 'Approvals'], ['llm', 'wave', 'LLM Records']];
const NAV_SYS = [['providers', 'cpu', 'AI Providers'], ['agents', 'bot', 'Agents'], ['skills', 'sparkles', 'Skills'], ['rules', 'shield', 'Command Rules'],
  ['templates', 'copy', 'Templates'], ['notifications', 'bell', 'Notifications'], ['logs', 'file', 'Logs'], ['settings', 'sliders', 'Settings']];
const navHTML = (items) => items.map(([k, i, l]) => `<a href="#/${k}" data-nav="${k}">${ic(i)}<span>${l}</span>${k === 'approvals' ? '<b class="cnt hidden" id="pending-cnt"></b>' : ''}</a>`).join('');

const ROUTES = {};
function route() {
  const parts = (location.hash.slice(2) || '').split('/').filter(Boolean);
  const key = parts[0] || '';
  const navKey = key === 'task' ? 'tasks' : key === 'project' ? 'projects' : key;
  $$('.nav a').forEach((a) => a.classList.toggle('active', a.dataset.nav === navKey));
  S.onEvent = null;
  $('#app').scrollTop = 0;
  const fn = ROUTES[key] || ROUTES[''];
  fn(parts.slice(1));
}
function setPending(n) {
  S.pending = n;
  const el = $('#pending-cnt'); if (!el) return;
  el.textContent = n; el.classList.toggle('hidden', !n);
}
const refreshPending = debounce(async () => {
  const r = await api('GET', '/approvals').catch(() => null);
  if (r) setPending(r.pending.length);
}, 500);

// ---------------------------------------------------------------- live updates (SSE)
let es;
function connect() {
  if (es) es.close();
  es = new EventSource('/api/stream' + (S.token ? '?' + tokenQ() : ''));
  es.onopen = () => $('#live').classList.add('on');
  es.onerror = () => $('#live').classList.remove('on');
  es.onmessage = (m) => {
    const ev = JSON.parse(m.data);
    if (ev.type === 'approval' || ev.type === 'stage') refreshPending();
    if (S.onEvent) S.onEvent(ev);
  };
}

// ---------------------------------------------------------------- ⌘K palette
function openPalette() {
  const m = modal(`<div class="palette"><input id="pal-q" placeholder="Search tasks, projects, pages…" autocomplete="off"><div class="res" id="pal-res"></div></div>`, 'palette');
  const pages = [...NAV_MAIN, ...NAV_SYS].map(([k, i, l]) => ({ kind: 'page', href: '#/' + k, title: l, icon: i, sub: 'page' }));
  let items = [], sel = 0;
  const draw = () => {
    $('#pal-res').innerHTML = items.map((x, i) => `<a href="${x.href}" class="${i === sel ? 'on' : ''}">${ic(x.icon)}<span class="grow ellipsis">${esc(x.title)}</span><span class="tiny muted">${esc(x.sub)}</span></a>`).join('') || '<div class="empty small">No results</div>';
    $$('#pal-res a').forEach((a) => (a.onclick = () => m.close()));
  };
  const search = debounce(async (q) => {
    const low = q.toLowerCase();
    const local = pages.filter((p) => p.title.toLowerCase().includes(low));
    let remote = [];
    if (q.trim()) {
      const r = await api('GET', '/search?q=' + encodeURIComponent(q)).catch(() => []);
      remote = r.map((h) => ({ kind: h.kind, href: `#/${h.kind}/${h.id}`, title: h.title, icon: h.kind === 'task' ? 'tasks' : 'folder', sub: `${h.kind} · ${h.sub}` }));
    }
    items = [...remote, ...local].slice(0, 30); sel = 0; draw();
  }, 150);
  const inp = $('#pal-q'); inp.focus(); items = pages; draw();
  inp.oninput = () => search(inp.value);
  inp.onkeydown = (e) => {
    if (e.key === 'ArrowDown') { sel = Math.min(sel + 1, items.length - 1); draw(); e.preventDefault(); }
    if (e.key === 'ArrowUp') { sel = Math.max(sel - 1, 0); draw(); e.preventDefault(); }
    if (e.key === 'Enter' && items[sel]) { location.hash = items[sel].href; m.close(); }
  };
}

// ---------------------------------------------------------------- boot
function applyTheme(t) {
  document.documentElement.dataset.theme = t;
  $('#theme-btn').innerHTML = ic(t === 'dark' ? 'sun' : 'moon');
}
window.addEventListener('DOMContentLoaded', () => {
  $('#logo').innerHTML = LOGO;
  $('#nav-main').innerHTML = navHTML(NAV_MAIN);
  $('#nav-sys').innerHTML = navHTML(NAV_SYS);
  $('#collapse-btn').innerHTML = ic('panel');
  $('#search-btn').innerHTML = `${ic('search')}<span>Search</span><kbd>⌘K</kbd>`;
  $('#capture-btn').innerHTML = `${ic('plus')}<span>New task</span>`;
  applyTheme(localStorage.getItem('theme') || (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'));
  if (localStorage.getItem('collapsed') === '1') $('#shell').classList.add('collapsed');
  $('#collapse-btn').onclick = () => { const c = $('#shell').classList.toggle('collapsed'); localStorage.setItem('collapsed', c ? '1' : '0'); };
  $('#theme-btn').onclick = () => { const n = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark'; applyTheme(n); localStorage.setItem('theme', n); };
  $('#search-btn').onclick = openPalette;
  $('#capture-btn').onclick = () => openCapture();
  document.addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); openPalette(); return; }
    if (e.key === 'Escape' && $('#modal-root').innerHTML) { $('#modal-root').innerHTML = ''; if (S.onModalClose) { S.onModalClose(); S.onModalClose = null; } }
    if (e.key === 'n' && !/INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName) && !$('#modal-root').innerHTML) { e.preventDefault(); openCapture(); }
  });
  window.addEventListener('hashchange', route);
  Promise.all([loadConfig(), loadProjects()]).then(() => {
    route(); connect(); refreshPending();
    if (S.cfg && !S.cfg.profiles.length && !location.hash.includes('providers')) toast('Get started: add an AI provider under System → AI Providers');
  });
});
