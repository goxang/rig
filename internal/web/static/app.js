'use strict';

// ---------- dom ----------
const $ = (s, el = document) => el.querySelector(s);
function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'style' && typeof v === 'object') Object.assign(el.style, v);
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'html') el.innerHTML = v;
    else if (k in el && k !== 'list' && typeof v !== 'string') el[k] = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  add(el, kids);
  return el;
}
function add(el, kids) {
  for (const k of kids.flat(Infinity)) {
    if (k == null || k === false) continue;
    el.append(k instanceof Node ? k : document.createTextNode(String(k)));
  }
  return el;
}
const svgNS = 'http://www.w3.org/2000/svg';
function s(tag, attrs, ...kids) {
  const el = document.createElementNS(svgNS, tag);
  for (const [k, v] of Object.entries(attrs || {})) if (v != null) el.setAttribute(k, v);
  for (const k of kids.flat()) if (k != null) el.append(k instanceof Node ? k : document.createTextNode(String(k)));
  return el;
}
const ICONS = {
  services: '<rect x="3" y="4" width="18" height="6" rx="2"/><rect x="3" y="14" width="18" height="6" rx="2"/><path d="M7 7h.01M7 17h.01"/>',
  logs: '<path d="M4 6h16M4 10h10M4 14h16M4 18h8"/>',
  metrics: '<path d="M3 20h18"/><path d="M5 16l4-6 4 3 6-8"/>',
  tasks: '<path d="M9 6h11M9 12h11M9 18h11"/><path d="M4 6l1 1 2-2M4 12l1 1 2-2M4 18l1 1 2-2"/>',
  queries: '<ellipse cx="12" cy="6" rx="8" ry="3"/><path d="M4 6v6c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6"/>',
  jobs: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  inbox: '<path d="M3 13h5l2 3h4l2-3h5"/><path d="M5 5h14l2 8v6H3v-6z"/>',
  api: '<path d="M8 6l-6 6 6 6M16 6l6 6-6 6M14 4l-4 16"/>',
  settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>',
  play: '<path d="M7 5v14l11-7z"/>', stop: '<rect x="6" y="6" width="12" height="12" rx="1"/>',
  restart: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="M20 20l-3.5-3.5"/>', x: '<path d="M6 6l12 12M18 6L6 18"/>',
  more: '<circle cx="12" cy="12" r="1"/><circle cx="12" cy="5" r="1"/><circle cx="12" cy="19" r="1"/>',
  up: '<path d="M12 19V5M5 12l7-7 7 7"/>', down: '<path d="M12 5v14M5 12l7 7 7-7"/>',
  refresh: '<path d="M21 12a9 9 0 1 1-3-6.7L21 8"/><path d="M21 3v5h-5"/>', pause: '<path d="M8 5v14M16 5v14"/>',
  trash: '<path d="M4 7h16M10 11v6M14 11v6M5 7l1 13h12l1-13M9 7V4h6v3"/>', copy: '<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/>',
  wrap: '<path d="M4 6h16M4 12h13a3 3 0 0 1 0 6h-4m0 0l2-2m-2 2l2 2M4 18h5"/>', chev: '<path d="M6 9l6 6 6-6"/>',
  ext: '<path d="M14 4h6v6M20 4l-9 9M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
  moon: '<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/>', check: '<path d="M5 12l5 5 9-11"/>',
  scale: '<path d="M4 12h16M12 4v16"/>',
};
function icon(name, cls = 'ico') {
  const el = document.createElementNS(svgNS, 'svg');
  el.setAttribute('viewBox', '0 0 24 24'); el.setAttribute('class', cls);
  el.setAttribute('fill', 'none'); el.setAttribute('stroke', 'currentColor'); el.setAttribute('stroke-width', '2');
  el.setAttribute('stroke-linecap', 'round'); el.setAttribute('stroke-linejoin', 'round');
  el.innerHTML = ICONS[name] || '';
  return el;
}
const btn = (label, onclick, cls = '', title) => h('button', { class: 'btn ' + cls, onclick, title, type: 'button' }, label);
const ibtn = (name, onclick, title, cls = '') => h('button', { class: 'btn icon ghost ' + cls, onclick, title, 'aria-label': title, type: 'button' }, icon(name));
const badge = (text, cls = '', dot = false) => h('span', { class: 'badge ' + cls }, dot && h('span', { class: 'dot' }), text);
// Go writes nanoseconds, which Date does not parse
const toDate = v => new Date(typeof v === 'string' ? v.replace(/(\.\d{3})\d+/, '$1') : v);
const firstLine = t => String(t || '').split('\n')[0];
const debounce = (f, ms) => { let t; return (...a) => { clearTimeout(t); t = setTimeout(() => f(...a), ms); }; };

// ---------- state ----------
const store = {
  get(k, d) { try { const v = localStorage.getItem('rig.web.' + k); return v == null ? d : JSON.parse(v); } catch { return d; } },
  set(k, v) { try { localStorage.setItem('rig.web.' + k, JSON.stringify(v)); } catch { /* private mode */ } },
};
const S = {
  token: store.get('token', ''), env: store.get('env', ''), info: null, provides: { sources: {}, actions: {} },
  catalog: [], jobs: [], inbox: [], running: 0,
  prefs: Object.assign({ theme: '', scheme: 'auto', layout: 'side', density: 'comfortable', font: 'sans' }, store.get('prefs', {})),
  cleanups: [], keyHandler: null, pageKeys: [],
};
const series = () => S.palette?.Series || ['#73BF69', '#F2CC0C', '#5794F2', '#FF780A', '#F2495C', '#B877D9'];
function colorOf(name) {
  let x = 0; for (const c of String(name)) x = (x * 31 + c.charCodeAt(0)) >>> 0;
  const ss = series(); return ss[x % ss.length];
}

// ---------- api ----------
class ApiError extends Error {}
async function api(method, body = {}, confirm = false) {
  const headers = { 'Content-Type': 'application/json', Authorization: 'Bearer ' + S.token, 'X-Rig-Env': S.env };
  if (confirm) headers['X-Rig-Confirm'] = '1';
  const r = await fetch('/v1/engine/' + method, { method: 'POST', headers, body: JSON.stringify(body) });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) {
    const e = new ApiError(j.error?.message || r.statusText);
    Object.assign(e, { status: r.status, code: j.error?.code, hint: j.error?.data?.hint });
    if (r.status === 401) showLocked();
    throw e;
  }
  return j;
}
const query = (source, args = {}) => api('query', { source, args });
function frameRows(f) {
  if (!f || !f.fields) return [];
  const n = f.columns?.[0]?.length || 0, rows = [];
  for (let i = 0; i < n; i++) {
    const r = {};
    f.fields.forEach((fl, c) => { r[fl.name] = f.columns[c]?.[i]; });
    rows.push(r);
  }
  return rows;
}
async function stream(source, args, onBatch, signal) {
  const r = await fetch('/v1/engine/stream', {
    method: 'POST', signal, body: JSON.stringify({ source, args }),
    headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + S.token, 'X-Rig-Env': S.env },
  });
  if (!r.ok) { const j = await r.json().catch(() => ({})); throw new ApiError(j.error?.message || r.statusText); }
  const rd = r.body.getReader(), dec = new TextDecoder();
  let buf = '';
  for (;;) {
    const { value, done } = await rd.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let i;
    while ((i = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, i); buf = buf.slice(i + 1);
      if (line.trim()) onBatch(JSON.parse(line));
    }
  }
}
// act runs an action through policy: a confirm comes back as a question, asked here.
async function act(action, args = {}, label = action) {
  const go = async confirm => {
    S.running++; refreshJobs(); setTimeout(refreshJobs, 400);
    try { return await api('run', { action, args }, confirm); } finally { S.running--; refreshJobs(); }
  };
  try {
    let res;
    try { res = await go(false); } catch (e) {
      if (e.hint !== 'confirm' && e.hint !== 'reauth') throw e;
      if (!await confirmBox('Confirm ' + label, e.message.replace(/^(confirm|reauth): /, ''), 'Run', true)) return null;
      res = await go(true);
    }
    toast((res.result?.message || label) + (res.result?.message ? '' : ' done'), 'ok');
    return res;
  } catch (e) {
    toast(label + ': ' + e.message, 'bad', 8000);
    return null;
  }
}
async function refreshJobs() {
  try { S.jobs = (await api('jobs')) || []; } catch { return; }
  updateCounts();
  document.dispatchEvent(new Event('rig:jobs'));
}
async function refreshInbox() {
  try { S.inbox = (await api('inbox')) || []; } catch { return; }
  updateCounts();
}
const isRunning = j => !j.end || j.end.startsWith('0001');

// ---------- theme ----------
function applyPrefs() {
  const p = S.prefs, themes = S.info?.themes || {};
  const name = themes[p.theme] ? p.theme : (themes[S.info?.theme] ? S.info.theme : 'default');
  const pal = themes[name];
  const light = p.scheme === 'light' || (p.scheme === 'auto' && matchMedia('(prefers-color-scheme: light)').matches);
  const root = document.documentElement;
  Object.assign(root.dataset, { layout: p.layout, density: p.layout === 'terminal' ? 'compact' : p.density, font: p.font, scheme: light ? 'light' : 'dark' });
  S.themeName = name;
  if (!pal) return;
  S.palette = pal;
  const sh = light ? pal.Light : pal;
  const vars = { accent: pal.Accent, 'on-accent': pal.OnAccent, green: pal.Green, amber: pal.Amber, red: pal.Red, purple: pal.Purple,
    text: sh.Text, dim: sh.Dim, placeholder: sh.Placeholder, panel: sh.Panel, bar: sh.Bar, cursor: sh.Cursor, selected: sh.Selected, hover: sh.Hover };
  for (const [k, v] of Object.entries(vars)) if (v) root.style.setProperty('--' + k, v);
}
function setPref(k, v) { S.prefs[k] = v; store.set('prefs', S.prefs); applyPrefs(); renderChrome(); route(); }
matchMedia('(prefers-color-scheme: light)').addEventListener('change', () => S.prefs.scheme === 'auto' && applyPrefs());

// ---------- overlays ----------
let toastsEl;
function toast(msg, kind = '', ms = 4000) {
  const t = h('div', { class: 'toast ' + kind, onclick: () => t.remove() }, h('span', { class: 'bar' }), h('div', { class: 'm' }, msg));
  toastsEl.append(t);
  setTimeout(() => t.remove(), ms);
}
let overlay = null;
function closeOverlay() { if (overlay) { overlay.close(); overlay = null; } }
function openOverlay(nodes, onClose) {
  closeOverlay();
  const scrim = h('div', { class: 'scrim', onclick: () => closeOverlay() });
  document.body.append(scrim, ...nodes);
  overlay = { close() { scrim.remove(); nodes.forEach(n => n.remove()); onClose?.(); } };
  return overlay;
}
function modal(title, body, buttons) {
  return new Promise(resolve => {
    let done = false;
    const finish = v => { if (!done) { done = true; resolve(v); closeOverlay(); } };
    const m = h('div', { class: 'modal', role: 'dialog' },
      h('div', { class: 'modal-h' }, title), h('div', { class: 'modal-b' }, body),
      h('div', { class: 'modal-f' }, buttons.map(([label, val, cls]) => btn(label, () => finish(typeof val === 'function' ? val() : val), cls))));
    openOverlay([m], () => finish(null));
    m.addEventListener('keydown', e => { if (e.key === 'Enter' && e.target.tagName !== 'TEXTAREA') { e.preventDefault(); m.querySelector('.btn.primary,.btn.solid')?.click(); } });
    setTimeout(() => (m.querySelector('input,select,textarea') || m.querySelector('.btn.primary,.btn.solid'))?.focus(), 30);
  });
}
const confirmBox = (title, text, ok = 'OK', danger = false) => modal(title, h('div', {}, text), [['Cancel', false, 'ghost'], [ok, true, danger ? 'danger solid' : 'primary']]).then(Boolean);
function drawer(title, sub, body, onClose) {
  const d = h('aside', { class: 'drawer' },
    h('div', { class: 'drawer-h' }, h('div', { class: 'grow' }, h('h2', {}, title), sub && h('div', { class: 'muted' }, sub)), ibtn('x', () => closeOverlay(), 'Close')),
    h('div', { class: 'drawer-b' }, body));
  openOverlay([d], onClose);
  return d;
}

// ---------- tables ----------
function fmtCell(v, field) {
  if (v == null) return '';
  if (field?.type === 'time' && typeof v === 'string') { const d = toDate(v); return isNaN(d) ? v : d.toLocaleString(); }
  if (typeof v === 'object') return JSON.stringify(v);
  if (typeof v === 'number' && !Number.isInteger(v)) return +v.toFixed(4);
  return String(v);
}
// dataTable sorts by a click on a header, filters by text, and keeps its sort across updates.
function dataTable({ columns, onRow, rowKey, selected, empty = 'Nothing here', rowClass }) {
  let rows = [], sort = { key: null, dir: 1 }, filter = '';
  const wrap = h('div', { class: 'table-wrap' });
  const draw = () => {
    let list = rows;
    if (filter) { const f = filter.toLowerCase(); list = list.filter(r => columns.some(c => String(r[c.key] ?? '').toLowerCase().includes(f))); }
    if (sort.key) {
      list = [...list].sort((a, b) => {
        const x = a[sort.key], y = b[sort.key];
        if (typeof x === 'number' && typeof y === 'number') return (x - y) * sort.dir;
        return String(x ?? '').localeCompare(String(y ?? ''), undefined, { numeric: true }) * sort.dir;
      });
    }
    const head = h('tr', {}, columns.map(c => c.check
      ? h('th', { class: 'chk' }, h('input', { type: 'checkbox', checked: list.length > 0 && list.every(r => selected.has(rowKey(r))), onchange: e => { list.forEach(r => e.target.checked ? selected.add(rowKey(r)) : selected.delete(rowKey(r))); c.onchange?.(); draw(); } }))
      : h('th', { class: [c.num && 'num', c.key && c.sort !== false && 'sort'].filter(Boolean).join(' '), onclick: () => { if (!c.key || c.sort === false) return; sort = { key: c.key, dir: sort.key === c.key ? -sort.dir : 1 }; draw(); } },
        c.label ?? c.key, sort.key === c.key && h('span', { class: 'arrow' }, sort.dir > 0 ? '↑' : '↓'))));
    const body = list.map(r => h('tr', { class: [onRow && 'click', selected?.has(rowKey?.(r)) && 'sel', rowClass?.(r)].filter(Boolean).join(' '), onclick: e => { if (onRow && !e.target.closest('button,input,a')) onRow(r, e); } },
      columns.map(c => c.check
        ? h('td', { class: 'chk' }, h('input', { type: 'checkbox', checked: selected.has(rowKey(r)), onchange: e => { e.target.checked ? selected.add(rowKey(r)) : selected.delete(rowKey(r)); c.onchange?.(); draw(); } }))
        : h('td', { class: [c.num && 'num', c.act && 'act', c.cls].filter(Boolean).join(' '), title: c.render || c.act ? null : fmtCell(r[c.key], c.field) }, c.render ? c.render(r) : fmtCell(r[c.key], c.field)))));
    wrap.replaceChildren(h('table', {}, h('thead', {}, head), h('tbody', {}, body)));
    if (!list.length) wrap.append(h('div', { class: 'empty' }, rows.length ? 'No rows match the filter' : empty));
    t.visible = list;
  };
  const t = { el: wrap, visible: [], update(r) { rows = r || []; draw(); }, filter(f) { filter = f; draw(); }, redraw: draw };
  return t;
}
function frameTable(frame, opts = {}) {
  const t = dataTable({ columns: (frame?.fields || []).map(f => ({ key: f.name, field: f, num: ['int', 'number'].includes(f.type) })), empty: 'No rows', ...opts });
  t.update(frameRows(frame));
  return t;
}
function searchBox(placeholder, oninput) {
  return h('div', { class: 'search' }, icon('search'), h('input', { type: 'search', placeholder, oninput: e => oninput(e.target.value), 'data-filter': '1' }));
}
function toCSV(rows, cols) {
  const q = v => { const x = v == null ? '' : typeof v === 'object' ? JSON.stringify(v) : String(v); return /[",\n]/.test(x) ? '"' + x.replace(/"/g, '""') + '"' : x; };
  return [cols.join(','), ...rows.map(r => cols.map(c => q(r[c])).join(','))].join('\n');
}
const copy = text => navigator.clipboard?.writeText(text).then(() => toast('Copied', 'ok', 1500), () => toast('Copy failed', 'bad'));

// ---------- catalog helpers ----------
const resources = kind => S.catalog.filter(r => r.Kind === kind);
const specOf = (kind, name) => S.catalog.find(r => r.Kind === kind && r.Name === name)?.Spec;
const serviceNames = () => resources('service').map(r => r.Name).sort();

// ---------- chrome ----------
const PAGES = [
  { id: 'services', title: 'Services', icon: 'services', render: pageServices, keys: [['↑↓', 'move'], ['space', 'mark'], ['s/x', 'start/stop'], ['r', 'restart'], ['l', 'logs'], ['enter', 'open']] },
  { id: 'logs', title: 'Logs', icon: 'logs', render: pageLogs, keys: [['space', 'pause'], ['w', 'wrap'], ['c', 'clear']] },
  { id: 'metrics', title: 'Metrics', icon: 'metrics', render: pageMetrics, keys: [['r', 'refresh'], ['[ ]', 'dashboard']] },
  { id: 'tasks', title: 'Tasks', icon: 'tasks', render: pageTasks },
  { id: 'queries', title: 'Queries', icon: 'queries', render: pageQueries, keys: [['ctrl+enter', 'run']] },
  { id: 'jobs', title: 'Activity', icon: 'jobs', render: pageJobs },
  { id: 'inbox', title: 'Inbox', icon: 'inbox', render: pageInbox },
  { id: 'api', title: 'API', icon: 'api', render: pageAPI },
  { id: 'settings', title: 'Settings', icon: 'settings', render: pageSettings },
];
let shell;
function renderChrome() {
  const cur = parseRoute().id;
  const navLinks = cls => PAGES.map((p, i) => h('a', { href: '#/' + p.id, class: p.id === cur ? 'on' : '', 'data-page': p.id },
    h('span', { class: 'tabnum' }, i + 1), icon(p.icon), h('span', {}, p.title), h('span', { class: 'count', hidden: true }), cls === 'side' && h('span', { class: 'kbd' }, i + 1)));
  const envs = resources('environment').map(r => r.Name).sort();
  const envSpec = specOf('environment', S.info?.env) || {};
  const envSel = h('label', { class: 'env-pill' + (envSpec.ReadOnly ? ' readonly' : envSpec.Protected ? ' protected' : ''), title: envSpec.Description || 'environment' },
    h('select', { onchange: e => switchEnv(e.target.value), 'aria-label': 'environment' }, envs.map(n => h('option', { value: n, selected: n === S.info?.env }, n))));
  const brand = h('div', { class: 'brand' }, h('span', { class: 'logo' }, 'r'), h('div', {}, S.info?.project || 'rig', h('small', {}, 'rig ' + (S.info?.version || ''))));
  const schemeBtn = ibtn(document.documentElement.dataset.scheme === 'light' ? 'moon' : 'sun', () => setPref('scheme', document.documentElement.dataset.scheme === 'light' ? 'dark' : 'light'), 'Toggle light and dark');
  shell.replaceChildren(
    h('aside', { class: 'sidebar' }, brand, h('nav', { class: 'nav' }, navLinks('side')),
      h('div', { class: 'side-foot' }, h('div', { class: 'muted', style: { fontSize: '12px', padding: '0 4px' } }, 'Environment'), envSel.cloneNode(true))),
    h('header', { class: 'topbar' }, brand.cloneNode(true), h('nav', { class: 'topnav' }, navLinks('top')),
      h('div', { class: 'crumb' }, PAGES.find(p => p.id === cur)?.title || ''), h('div', { class: 'spacer' }),
      h('button', { class: 'palette-btn', onclick: openPalette, type: 'button' }, icon('search'), 'Search or run…', h('span', { class: 'kbd' }, navigator.platform.includes('Mac') ? '⌘K' : 'Ctrl K')),
      S.prefs.layout !== 'side' && envSel, schemeBtn),
    h('main', { class: 'main', id: 'main' }),
    h('footer', { class: 'keys', id: 'keys' }));
  // the sidebar's copy of the env picker needs its own listener
  shell.querySelector('.sidebar select')?.addEventListener('change', e => switchEnv(e.target.value));
  updateCounts();
}
function updateCounts() {
  const running = S.jobs.filter(isRunning).length, pending = S.inbox.filter(i => i.status === 'pending').length;
  for (const el of document.querySelectorAll('[data-page="jobs"] .count')) { el.hidden = !running; el.textContent = running; }
  for (const el of document.querySelectorAll('[data-page="inbox"] .count')) { el.hidden = !pending; el.textContent = pending; }
}
function renderKeys(extra) {
  const el = $('#keys');
  if (!el) return;
  const keys = [...(extra || []), ['1-9', 'screens'], ['ctrl+k', 'commands'], ['/', 'filter'], ['?', 'help']];
  el.replaceChildren(...keys.map(([k, d]) => h('span', {}, h('b', {}, k), d)), h('span', { class: 'spacer' }), h('span', {}, (S.info?.project || '') + ' · ' + (S.info?.env || '')));
}
async function switchEnv(env) {
  S.env = env; store.set('env', env);
  await boot(true);
}

// ---------- routing ----------
function parseRoute() {
  const [path, qs] = location.hash.replace(/^#\/?/, '').split('?');
  const [id, ...rest] = path.split('/');
  return { id: PAGES.some(p => p.id === id) ? id : 'services', rest, q: new URLSearchParams(qs || '') };
}
function go(id, params) { location.hash = '#/' + id + (params ? '?' + new URLSearchParams(params) : ''); }
function route() {
  S.cleanups.splice(0).forEach(f => { try { f(); } catch { /* gone */ } });
  S.keyHandler = null;
  const r = parseRoute(), page = PAGES.find(p => p.id === r.id);
  for (const a of document.querySelectorAll('[data-page]')) a.classList.toggle('on', a.dataset.page === r.id);
  const crumb = $('.crumb'); if (crumb) crumb.textContent = page.title;
  document.title = page.title + ' · ' + (S.info?.project || 'rig');
  const main = $('#main'), el = h('div', { class: 'page' });
  main.replaceChildren(el); main.scrollTop = 0;
  renderKeys(page.keys);
  const ctx = {
    el, q: r.q, rest: r.rest,
    every(ms, f) { const t = setInterval(() => { if (!document.hidden) f(); }, ms); S.cleanups.push(() => clearInterval(t)); },
    onCleanup(f) { S.cleanups.push(f); },
    onKey(f) { S.keyHandler = f; },
  };
  try { page.render(ctx); } catch (e) { el.append(h('div', { class: 'err' }, e.stack || e.message)); }
}
window.addEventListener('hashchange', () => { closeOverlay(); route(); });

function pageHead(title, desc, ...actions) {
  return h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, title), desc && h('p', {}, desc)), actions.length > 0 && h('div', { class: 'actions' }, actions));
}
const emptyState = (big, small, ...more) => h('div', { class: 'empty' }, h('div', { class: 'big' }, big), small && h('div', {}, small), more);

// ---------- services ----------
function stateBadge(st) {
  const cls = { running: 'green', starting: 'amber pulse', degraded: 'amber', failed: 'red' }[st] || '';
  return badge(st || 'unknown', cls, true);
}
function pageServices(ctx) {
  const selected = new Set();
  let rows = [], cursor = -1, filterText = '';
  const names = () => selected.size ? [...selected] : rows.map(r => r.service);
  const bulk = h('div', { class: 'bulk', hidden: true });
  const stats = h('div', { class: 'grid stats' });
  const svcAct = (action, n) => act('runtime.' + action, { service: n }, action + ' ' + n).then(load);
  const table = dataTable({
    rowKey: r => r.service, selected, empty: 'No services in this environment',
    onRow: r => openService(r.service),
    columns: [
      { check: true, onchange: () => drawBulk() },
      { key: 'service', label: 'Service', render: r => h('span', { class: 'svc-name' }, r.service) },
      { key: 'state', label: 'State', render: r => stateBadge(r.state) },
      { key: 'ready', label: 'Ready', num: true, render: r => h('span', { class: r.desired > 0 && r.ready < r.desired ? 'muted' : '' }, `${r.ready ?? 0}/${r.desired ?? 0}`) },
      { key: 'image', label: 'Image', cls: 'mono muted' },
      { key: 'message', label: 'Message', cls: 'muted' },
      { act: true, label: '', render: r => h('div', { class: 'row' },
        ibtn('play', () => svcAct('start', r.service), 'Start'), ibtn('stop', () => svcAct('stop', r.service), 'Stop'),
        ibtn('restart', () => svcAct('restart', r.service), 'Restart'), ibtn('logs', () => go('logs', { svc: r.service }), 'Logs')) },
    ],
  });
  function drawBulk() {
    bulk.hidden = !selected.size;
    bulk.replaceChildren(h('b', {}, selected.size + ' selected'), h('span', { class: 'spacer' }),
      btn([icon('play'), 'Start'], () => bulkRun('start')), btn([icon('stop'), 'Stop'], () => bulkRun('stop')),
      btn([icon('restart'), 'Restart'], () => bulkRun('restart')), btn([icon('logs'), 'Logs'], () => go('logs', { svc: [...selected].join(',') })),
      btn('Clear', () => { selected.clear(); table.redraw(); drawBulk(); }, 'ghost'));
  }
  async function bulkRun(what) {
    const list = [...selected];
    if (what === 'start') await act('env.up', { targets: list }, 'start ' + list.join(', '));
    else if (what === 'stop') await act('env.down', { targets: list }, 'stop ' + list.join(', '));
    else await Promise.all(list.map(n => act('runtime.restart', { service: n }, 'restart ' + n)));
    load();
  }
  async function load() {
    try { rows = frameRows(await query('services.status')); } catch (e) { stats.replaceChildren(h('div', { class: 'card stat err' }, e.message)); return; }
    const count = st => rows.filter(r => r.state === st).length;
    stats.replaceChildren(
      ...[['Services', rows.length, ''], ['Running', count('running'), 'var(--green)'], ['Starting', count('starting') + count('degraded'), 'var(--amber)'],
        ['Failed', count('failed'), 'var(--red)'], ['Stopped', rows.length - count('running') - count('starting') - count('degraded') - count('failed'), 'var(--dim)']]
        .map(([k, v, c]) => h('div', { class: 'card stat' }, h('div', { class: 'k' }, k), h('div', { class: 'v', style: { color: v && c ? c : '' } }, v))));
    table.update(rows);
    markCursor();
  }
  const markCursor = () => table.el.querySelectorAll('tbody tr').forEach((tr, i) => tr.classList.toggle('sel', i === cursor || selected.has(table.visible[i]?.service)));
  ctx.el.append(
    pageHead('Services', `${S.info.project} on ${S.info.env}`,
      searchBox('Filter services', v => { filterText = v; table.filter(v); }),
      btn([icon('up'), 'Up'], async () => { await act('env.up', { targets: filterText ? table.visible.map(r => r.service) : [] }, 'up'); load(); }, 'primary', 'Deploy services and their dependencies'),
      btn([icon('down'), 'Down'], async () => { await act('env.down', { targets: filterText ? table.visible.map(r => r.service) : [] }, 'down'); load(); }, '', 'Stop services in reverse dependency order'),
      ibtn('refresh', load, 'Refresh')),
    stats, bulk, table.el);
  load();
  ctx.every(3000, load);
  ctx.onKey(e => {
    const n = table.visible.length, cur = table.visible[cursor];
    switch (e.key) {
      case 'ArrowDown': case 'j': cursor = Math.min(n - 1, cursor + 1); markCursor(); return true;
      case 'ArrowUp': case 'k': cursor = Math.max(0, cursor - 1); markCursor(); return true;
      case ' ': if (cur) { selected.has(cur.service) ? selected.delete(cur.service) : selected.add(cur.service); table.redraw(); drawBulk(); markCursor(); } return true;
      case 'Enter': if (cur) openService(cur.service); return true;
      case 's': names().length && (selected.size ? bulkRun('start') : cur && svcAct('start', cur.service)); return true;
      case 'x': names().length && (selected.size ? bulkRun('stop') : cur && svcAct('stop', cur.service)); return true;
      case 'r': selected.size ? bulkRun('restart') : cur && svcAct('restart', cur.service); return true;
      case 'l': go('logs', { svc: selected.size ? [...selected].join(',') : cur?.service || '' }); return true;
    }
    return false;
  });
}
// brief is a spec struct's set fields as k: v, for a glance.
const brief = o => o && Object.entries(o).filter(([, v]) => v != null && v !== '' && v !== false && !(Array.isArray(v) && !v.length)).map(([k, v]) => `${k.toLowerCase()}: ${typeof v === 'object' ? JSON.stringify(v) : v}`).join(' · ');
function openService(name) {
  const sp = specOf('service', name) || {};
  const ctrl = new AbortController();
  const logs = h('div', { class: 'logs' });
  const status = h('div', {});
  const replicas = h('input', { type: 'number', min: 0, value: 1, style: { width: '80px' } });
  const ports = Object.entries(sp.Ports || {});
  const dd = (k, v) => v != null && v !== '' && (Array.isArray(v) ? v.length : true) ? [h('dt', {}, k), h('dd', {}, v)] : null;
  const svcAct = a => act('runtime.' + a, { service: name }, a + ' ' + name).then(loadStatus);
  async function loadStatus() {
    try {
      const r = frameRows(await query('services.status', { services: [name] }))[0];
      if (!r) return;
      replicas.value = r.desired ?? 1;
      status.replaceChildren(h('dl', { class: 'kv' }, dd('State', stateBadge(r.state)), dd('Ready', `${r.ready}/${r.desired}`), dd('Image', h('span', { class: 'mono' }, r.image)), dd('Message', r.message)));
    } catch (e) { status.replaceChildren(h('div', { class: 'err' }, e.message)); }
  }
  drawer(name, sp.Help || sp.Description || 'service', [
    h('div', { class: 'row wrap' },
      btn([icon('play'), 'Start'], () => svcAct('start'), 'primary'), btn([icon('stop'), 'Stop'], () => svcAct('stop')),
      btn([icon('restart'), 'Restart'], () => svcAct('restart')), h('span', { class: 'spacer' }),
      h('span', { class: 'row' }, 'Replicas', replicas, btn('Scale', () => act('runtime.scale', { service: name, replicas: +replicas.value }, `scale ${name} to ${replicas.value}`).then(loadStatus)))),
    status,
    h('dl', { class: 'kv' },
      dd('Ports', ports.length ? h('span', { class: 'row wrap' }, ports.map(([k, p]) => h('a', { href: `http://localhost:${p}/`, target: '_blank', rel: 'noopener' }, `${k} ${p}`))) : null),
      dd('Depends on', (sp.DependsOn || []).length ? h('span', { class: 'row wrap' }, sp.DependsOn.map(d => h('a', { href: '#', onclick: e => { e.preventDefault(); openService(d); } }, d))) : null),
      dd('Health', brief(sp.Health)), dd('Build', brief(sp.Build))),
    h('div', { class: 'row' }, h('b', {}, 'Live log'), h('span', { class: 'spacer' }), btn([icon('ext'), 'Open in Logs'], () => go('logs', { svc: name }), 'sm')),
    logs,
  ], () => ctrl.abort());
  loadStatus();
  const lv = logView(logs, { showService: false });
  stream('services.logs', { services: [name], tail: 200 }, b => lv.push(frameRows(b.frame)), ctrl.signal)
    .catch(e => { if (e.name !== 'AbortError') logs.append(h('div', { class: 'ln err' }, h('span', { class: 'msg' }, e.message))); });
}

// ---------- logs ----------
const LEVEL_RE = /^\W{0,3}(TRACE|DEBUG|DBG|INFO|INF|NOTICE|WARN|WARNING|WRN|ERROR|ERR|FATAL|PANIC|CRIT)\b\W?/i;
const LEVELS = { dbg: 'debug', inf: 'info', wrn: 'warn', warning: 'warn', err: 'error', crit: 'fatal', notice: 'info' };
function parseLine(text) {
  const out = { level: '', msg: text, kv: [], json: null };
  const t = text.trim();
  if (t.startsWith('{')) {
    try {
      const j = JSON.parse(t);
      out.json = j;
      out.level = String(j.level ?? j.lvl ?? j.severity ?? j.L ?? '').toLowerCase();
      out.msg = String(j.msg ?? j.message ?? j.M ?? '');
      for (const [k, v] of Object.entries(j)) {
        if (['level', 'lvl', 'severity', 'msg', 'message', 'time', 'ts', 'timestamp', '@timestamp', 'L', 'M', 'T'].includes(k)) continue;
        out.kv.push([k, typeof v === 'object' ? JSON.stringify(v) : String(v)]);
      }
      if (!out.msg && !out.kv.length) out.msg = t;
    } catch { /* not JSON after all */ }
  } else {
    const lf = /(?:^|\s)(?:level|lvl)=("?)(\w+)\1/.exec(t);
    if (lf) out.level = lf[2].toLowerCase();
    else { const m = LEVEL_RE.exec(t); if (m) { out.level = m[1].toLowerCase(); out.msg = t.slice(m[0].length).trimStart(); } }
  }
  out.level = LEVELS[out.level] || out.level;
  return out;
}
// logView appends lines as they come, keeps the last max, and stays at the bottom unless scrolled up.
function logView(el, { showService = true, max = 5000, filter = () => true } = {}) {
  let lines = [], wrap = true;
  const lineEl = r => {
    const p = parseLine(String(r.text ?? ''));
    const t = r.time ? toDate(r.time) : null;
    const msg = h('span', { class: 'msg' }, p.msg, p.kv.length > 0 && h('span', { class: 'kv' }, p.kv.map(([k, v]) => [' ', h('b', {}, k), '=', v])));
    const row = h('div', { class: 'ln' + (r.stream === 'stderr' && !p.level ? ' err' : ''), onclick: () => {
      if (window.getSelection()?.toString()) return;
      const open = row.querySelector('pre');
      if (open) { open.remove(); row.classList.remove('open'); return; }
      row.classList.add('open');
      row.append(h('pre', {}, p.json ? JSON.stringify(p.json, null, 2) : r.text, r.instance ? `\n\ninstance ${r.instance}` : ''));
    } },
    h('span', { class: 't' }, t && !isNaN(t) ? t.toLocaleTimeString([], { hour12: false }) : ''),
    showService ? h('span', { class: 's', style: { color: colorOf(r.service) }, title: r.instance || r.service }, r.service) : h('span', {}),
    h('span', { class: 'lv ' + p.level }, p.level ? p.level.slice(0, 5).toUpperCase() : ''), msg);
    row._r = r; row._p = p;
    return row;
  };
  const atBottom = () => el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  return {
    push(rows) {
      if (!rows?.length) return;
      const stick = atBottom();
      lines.push(...rows);
      const frag = document.createDocumentFragment();
      for (const r of rows) if (filter(r)) frag.append(lineEl(r));
      el.append(frag);
      if (lines.length > max) { lines = lines.slice(-max); while (el.childElementCount > max) el.firstElementChild.remove(); }
      if (stick) el.scrollTop = el.scrollHeight;
    },
    refilter() { el.replaceChildren(...lines.filter(filter).map(lineEl)); el.scrollTop = el.scrollHeight; },
    clear() { lines = []; el.replaceChildren(); },
    wrap(on) { wrap = on ?? !wrap; el.classList.toggle('nowrap', !wrap); return wrap; },
    get lines() { return lines; },
  };
}
function pageLogs(ctx) {
  const all = serviceNames();
  const picked = new Set((ctx.q.get('svc') || '').split(',').filter(n => all.includes(n)));
  let text = '', level = '', paused = false, held = [], ctrl;
  const levelRank = { trace: 0, debug: 1, info: 2, warn: 3, error: 4, fatal: 5, panic: 5 };
  const box = h('div', { class: 'logs' });
  const lv = logView(box, { filter: r => {
    if (level && (levelRank[parseLine(String(r.text)).level] ?? 2) < levelRank[level]) return false;
    if (!text) return true;
    if (text.length > 2 && text.startsWith('/') && text.endsWith('/')) { try { return new RegExp(text.slice(1, -1), 'i').test(r.text); } catch { return true; } }
    return String(r.text).toLowerCase().includes(text.toLowerCase()) || String(r.service).toLowerCase().includes(text.toLowerCase());
  } });
  const status = h('span', { class: 'muted' });
  const chips = h('div', { class: 'chips' });
  const drawChips = () => chips.replaceChildren(
    h('button', { class: 'chip' + (picked.size === 0 ? ' on' : ''), onclick: () => { picked.clear(); restart(); } }, 'All services'),
    ...all.map(n => h('button', { class: 'chip' + (picked.has(n) ? ' on' : ''), style: { '--c': colorOf(n) }, onclick: e => {
      if (!(e.ctrlKey || e.metaKey || e.shiftKey)) { const only = picked.size === 1 && picked.has(n); picked.clear(); if (!only) picked.add(n); }
      else picked.has(n) ? picked.delete(n) : picked.add(n);
      restart();
    }, title: 'click: only this · ctrl/shift-click: add' }, h('span', { class: 'dot' }), n)));
  const pauseBtn = btn([icon('pause'), 'Pause'], () => togglePause());
  const wrapBtn = btn([icon('wrap'), 'Wrap'], () => wrapBtn.classList.toggle('on', lv.wrap()), 'on');
  function togglePause() {
    paused = !paused;
    pauseBtn.replaceChildren(icon(paused ? 'play' : 'pause'), paused ? 'Resume' : 'Pause');
    pauseBtn.classList.toggle('on', paused);
    if (!paused) { lv.push(held); held = []; }
    status.textContent = paused ? 'paused' : 'following';
  }
  function restart() {
    drawChips();
    ctrl?.abort();
    ctrl = new AbortController();
    lv.clear(); held = [];
    const q = [...picked].join(',');
    history.replaceState(null, '', '#/logs' + (q ? '?svc=' + encodeURIComponent(q) : ''));
    status.textContent = 'connecting…';
    const c = ctrl;
    stream('services.logs', { services: [...picked], tail: 300 }, b => {
      if (c.signal.aborted) return;
      status.textContent = paused ? 'paused' : 'following';
      const rows = frameRows(b.frame);
      paused ? held.push(...rows) : lv.push(rows);
      if (b.done) status.textContent = 'ended';
    }, c.signal).catch(e => { if (e.name !== 'AbortError') { status.textContent = ''; box.append(h('div', { class: 'ln err' }, h('span'), h('span'), h('span'), h('span', { class: 'msg' }, e.message))); } });
  }
  const refilter = debounce(() => lv.refilter(), 150);
  ctx.el.append(
    pageHead('Logs', 'Live lines from the services you pick; click a line to inspect it',
      status, searchBox('Filter: text or /regex/', v => { text = v; refilter(); }),
      h('select', { onchange: e => { level = e.target.value; lv.refilter(); }, 'aria-label': 'minimum level' },
        [['', 'All levels'], ['debug', 'Debug+'], ['info', 'Info+'], ['warn', 'Warn+'], ['error', 'Errors']].map(([v, l]) => h('option', { value: v }, l))),
      pauseBtn, wrapBtn, ibtn('trash', () => lv.clear(), 'Clear'),
      ibtn('copy', () => copy(lv.lines.map(r => `${r.time || ''} ${r.service} ${r.text}`).join('\n')), 'Copy all')),
    h('div', { class: 'toolbar' }, chips), box);
  restart();
  ctx.onCleanup(() => ctrl?.abort());
  ctx.onKey(e => {
    if (e.key === ' ') { togglePause(); return true; }
    if (e.key === 'w') { wrapBtn.click(); return true; }
    if (e.key === 'c') { lv.clear(); return true; }
    return false;
  });
}

// ---------- metrics ----------
const RANGES = ['5m', '15m', '1h', '3h', '6h', '12h', '24h'];
function fmtNum(v, unit = '') {
  if (v == null || isNaN(v)) return '–';
  const a = Math.abs(v);
  const short = x => { const ax = Math.abs(x); return ax >= 1e9 ? (x / 1e9).toFixed(2) + 'G' : ax >= 1e6 ? (x / 1e6).toFixed(2) + 'M' : ax >= 1e4 ? (x / 1e3).toFixed(1) + 'k' : ax >= 100 ? x.toFixed(0) : ax >= 1 ? +x.toFixed(2) + '' : ax === 0 ? '0' : +x.toPrecision(3) + ''; };
  switch (unit) {
    case '%': return v.toFixed(1) + '%';
    case 's': return a >= 60 ? (v / 60).toFixed(1) + 'm' : a >= 1 ? v.toFixed(2) + 's' : a >= 1e-3 ? (v * 1e3).toFixed(1) + 'ms' : (v * 1e6).toFixed(0) + 'µs';
    case 'ms': return a >= 1000 ? (v / 1000).toFixed(2) + 's' : v.toFixed(1) + 'ms';
    case 'bytes': case 'B': { const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB']; let i = 0, x = v; while (Math.abs(x) >= 1024 && i < u.length - 1) { x /= 1024; i++; } return x.toFixed(i ? 1 : 0) + ' ' + u[i]; }
    default: return short(v) + (unit ? (unit.startsWith('/') ? '' : ' ') + unit : '');
  }
}
function legendName(tpl, series) {
  if (!tpl) return series;
  const labels = Object.fromEntries(String(series).split(',').map(p => p.split('=')));
  return tpl.replace(/\{\{\s*(\w+)\s*\}\}/g, (_, k) => labels[k] ?? '');
}
function groupSeries(rows, legend) {
  const m = new Map();
  for (const r of rows) {
    const k = r.series && r.series !== 'series={}' ? r.series : '';
    if (!m.has(k)) m.set(k, { name: legendName(legend, k) || 'value', key: k, pts: [] });
    m.get(k).pts.push([toDate(r.time).getTime(), +r.value]);
  }
  return [...m.values()].map(x => { x.pts.sort((a, b) => a[0] - b[0]); return x; });
}
const tipEl = () => document.querySelector('.tip') || document.body.appendChild(h('div', { class: 'tip', hidden: true }));
function lineChart(el, list, { unit = '', stack = false, height = 190 } = {}) {
  const hidden = new Set();
  const legend = h('div', { class: 'legend' });
  const holder = h('div', {});
  el.replaceChildren(holder, legend);
  const draw = () => {
    const W = Math.max(holder.clientWidth || el.clientWidth || 400, 200), H = height, padL = 52, padB = 20, padT = 6, padR = 8;
    const shown = list.filter(x => !hidden.has(x.key));
    let data = shown.map(x => ({ ...x, v: x.pts.map(p => p[1]) }));
    if (stack) { const acc = new Map(); data = data.map(x => ({ ...x, v: x.pts.map(([t, v]) => { const y = (acc.get(t) || 0) + (isNaN(v) ? 0 : v); acc.set(t, y); return y; }) })); }
    const ts = list.flatMap(x => x.pts.map(p => p[0]));
    const vs = data.flatMap(x => x.v).filter(v => isFinite(v));
    const t0 = Math.min(...ts), t1 = Math.max(...ts);
    let lo = Math.min(0, ...vs), hi = Math.max(...vs);
    if (!vs.length) { lo = 0; hi = 1; }
    if (hi - lo < 1e-12) hi = lo + 1;
    hi += (hi - lo) * 0.08;
    const X = t => padL + (t1 === t0 ? 0.5 : (t - t0) / (t1 - t0)) * (W - padL - padR);
    const Y = v => padT + (1 - (v - lo) / (hi - lo)) * (H - padT - padB);
    const svg = s('svg', { class: 'chart', viewBox: `0 0 ${W} ${H}`, width: W, height: H });
    const g = s('g', { class: 'grid' });
    for (let i = 0; i <= 4; i++) {
      const v = lo + (hi - lo) * i / 4, y = Y(v);
      g.append(s('line', { x1: padL, x2: W - padR, y1: y, y2: y }), s('text', { x: padL - 6, y: y + 3.5, 'text-anchor': 'end' }, fmtNum(v, unit)));
    }
    const ticks = t1 > t0 ? Math.max(2, Math.min(6, Math.floor(W / 120))) : 0;
    for (let i = 0; i <= ticks && isFinite(t0); i++) {
      const t = ticks ? t0 + (t1 - t0) * i / ticks : t0;
      g.append(s('text', { x: X(t), y: H - 4, 'text-anchor': i === 0 ? 'start' : i === ticks ? 'end' : 'middle' }, new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: t1 - t0 < 600e3 ? '2-digit' : undefined, hour12: false })));
    }
    svg.append(g);
    data.forEach((x, i) => {
      const c = series()[list.indexOf(shown[i]) % series().length];
      let d = '', pen = false;
      x.pts.forEach(([t], j) => { const v = x.v[j]; if (!isFinite(v)) { pen = false; return; } d += (pen ? 'L' : 'M') + X(t).toFixed(1) + ',' + Y(v).toFixed(1); pen = true; });
      if (stack || data.length === 1) {
        const first = x.pts.findIndex((_, j) => isFinite(x.v[j]));
        if (first >= 0) svg.append(s('path', { d: d + `L${X(x.pts.at(-1)[0]).toFixed(1)},${Y(lo)}L${X(x.pts[first][0]).toFixed(1)},${Y(lo)}Z`, fill: c, 'fill-opacity': stack ? 0.25 : 0.1, stroke: 'none' }));
      }
      svg.append(s('path', { d, fill: 'none', stroke: c, 'stroke-width': 1.6, 'stroke-linejoin': 'round' }));
      if (x.pts.length === 1 && isFinite(x.v[0])) svg.append(s('circle', { cx: X(x.pts[0][0]), cy: Y(x.v[0]), r: 3, fill: c }));
    });
    const cross = s('line', { class: 'cross', y1: padT, y2: H - padB, visibility: 'hidden' });
    svg.append(cross);
    const tip = tipEl();
    svg.addEventListener('mousemove', e => {
      const rect = svg.getBoundingClientRect(), mx = (e.clientX - rect.left) * (W / rect.width);
      if (mx < padL || !ts.length) return;
      const t = t0 + (mx - padL) / (W - padL - padR) * (t1 - t0);
      cross.setAttribute('x1', mx); cross.setAttribute('x2', mx); cross.setAttribute('visibility', 'visible');
      const vals = shown.map((x, i) => {
        let best = 0; x.pts.forEach((p, j) => { if (Math.abs(p[0] - t) < Math.abs(x.pts[best][0] - t)) best = j; });
        return { name: x.name, v: x.pts[best]?.[1], c: series()[list.indexOf(x) % series().length], t: x.pts[best]?.[0] };
      }).sort((a, b) => (b.v ?? -Infinity) - (a.v ?? -Infinity)).slice(0, 12);
      tip.replaceChildren(h('div', { class: 'tt' }, new Date(vals[0]?.t || t).toLocaleTimeString([], { hour12: false })),
        ...vals.map(x => h('div', { class: 'r' }, h('i', { style: { background: x.c } }), h('span', { class: 'ellipsis' }, x.name), h('span', { class: 'v' }, fmtNum(x.v, unit)))));
      tip.hidden = false;
      const tw = tip.offsetWidth;
      tip.style.left = (e.clientX + 16 + tw > innerWidth ? e.clientX - tw - 16 : e.clientX + 16) + 'px';
      tip.style.top = (e.clientY + 14) + 'px';
    });
    svg.addEventListener('mouseleave', () => { cross.setAttribute('visibility', 'hidden'); tip.hidden = true; });
    holder.replaceChildren(svg);
    legend.replaceChildren(...list.map((x, i) => h('span', { class: hidden.has(x.key) ? 'off' : '', title: x.key, onclick: e => {
      if (e.ctrlKey || e.metaKey) hidden.has(x.key) ? hidden.delete(x.key) : hidden.add(x.key);
      else if (hidden.size === list.length - 1 && !hidden.has(x.key)) hidden.clear();
      else { hidden.clear(); list.forEach(y => y.key !== x.key && hidden.add(y.key)); }
      draw();
    } }, h('i', { style: { background: series()[i % series().length] } }), x.name)));
  };
  draw();
  return { draw };
}
function gauge(value, min, max, color, unit) {
  const W = 200, H = 120, r = 80, cx = 100, cy = 100;
  const frac = Math.max(0, Math.min(1, (value - min) / ((max - min) || 1)));
  const pt = f => [cx - r * Math.cos(Math.PI * f), cy - r * Math.sin(Math.PI * f)];
  const [x1, y1] = pt(0), [x2, y2] = pt(frac), [x3, y3] = pt(1);
  return s('svg', { viewBox: `0 0 ${W} ${H}`, class: 'chart', style: 'height:140px' },
    s('path', { d: `M${x1},${y1} A${r},${r} 0 0 1 ${x3},${y3}`, stroke: 'var(--surface-2)', 'stroke-width': 14, fill: 'none', 'stroke-linecap': 'round' }),
    s('path', { d: `M${x1},${y1} A${r},${r} 0 0 1 ${x2},${y2}`, stroke: color, 'stroke-width': 14, fill: 'none', 'stroke-linecap': 'round' }),
    s('text', { x: cx, y: cy - 8, 'text-anchor': 'middle', style: `font:650 24px var(--font);fill:var(--text)` }, fmtNum(value, unit)));
}
function pageMetrics(ctx) {
  const dashes = resources('dashboard').map(r => r.Name).sort();
  let cur = ctx.rest[0] && (dashes.includes(ctx.rest[0]) || ctx.rest[0] === '_query') ? ctx.rest[0] : (dashes[0] || '_query');
  let range = store.get('metrics.range', '15m'), every = store.get('metrics.every', 30), timer, vars = {};
  const body = h('div', {});
  const tabs = h('div', { class: 'seg' });
  const varBar = h('div', { class: 'row wrap' });
  const drawTabs = () => tabs.replaceChildren(...dashes.map(d => h('button', { class: d === cur ? 'on' : '', onclick: () => go('metrics/' + d) }, d)),
    h('button', { class: cur === '_query' ? 'on' : '', onclick: () => go('metrics/_query') }, 'Query'));
  const charts = [];
  const resize = debounce(() => charts.forEach(c => c.draw()), 120);
  window.addEventListener('resize', resize);
  ctx.onCleanup(() => { window.removeEventListener('resize', resize); clearInterval(timer); const t = document.querySelector('.tip'); if (t) t.hidden = true; });
  const setTimer = () => { clearInterval(timer); if (every) timer = setInterval(() => !document.hidden && refresh(), every * 1000); };
  async function loadVars(d) {
    varBar.replaceChildren();
    vars = {};
    const order = d.VarOrder?.length ? d.VarOrder : Object.keys(d.Vars || {});
    for (const name of order) {
      const v = d.Vars[name];
      let values = v.Values || [];
      if (!values.length && v.Query) {
        try {
          const rows = frameRows(await query('metrics.query', { query: v.Query, source: v.Source || '', instant: true }));
          values = [...new Set(rows.map(r => Object.fromEntries(String(r.series).split(',').map(p => p.split('=')))[v.Label]).filter(Boolean))].sort();
        } catch { /* leave it empty */ }
      }
      const def = v.Default && (!values.length || values.includes(v.Default)) ? v.Default : v.All ? '$__all' : values[0];
      vars[name] = def ? [def] : [];
      varBar.append(h('label', { class: 'row' }, h('span', { class: 'muted' }, '$' + name),
        h('select', { onchange: e => { vars[name] = [e.target.value]; refresh(); } },
          v.All && h('option', { value: '$__all', selected: def === '$__all' }, 'All'), values.map(x => h('option', { value: x, selected: x === def }, x)))));
    }
  }
  async function panelEl(p) {
    const kind = p.Kind || 'line';
    const width = p.Width || (kind === 'stat' || kind === 'gauge' ? 6 : 12);
    const bodyEl = h('div', { class: 'body' }, h('div', { class: 'muted', style: { padding: '30px 0', textAlign: 'center' } }, h('span', { class: 'spin' })));
    const el = h('div', { class: 'panel', style: { gridColumn: `span ${Math.min(24, width)}` } },
      h('div', { class: 'ph' }, h('span', { class: 'ellipsis' }, p.Title || firstLine(p.Query)), p.Help && h('span', { class: 'help', title: p.Help }, 'ⓘ')), bodyEl);
    const targets = p.Queries?.length ? p.Queries : p.Query ? [{ Query: p.Query, Legend: p.Legend }] : [];
    el._load = async () => {
      try {
        const lists = await Promise.all(targets.map(async t => groupSeries(frameRows(await query('metrics.query', { query: t.Query, source: p.Source || '', range, vars, instant: kind !== 'line' && kind !== 'stat' })), t.Legend)));
        const list = lists.flat();
        const last = x => x.pts.at(-1)?.[1];
        const colorFor = v => p.Crit != null && v >= p.Crit ? 'var(--red)' : p.Warn != null && v >= p.Warn ? 'var(--amber)' : 'var(--green)';
        if (!list.length) { bodyEl.replaceChildren(h('div', { class: 'empty', style: { padding: '30px 0' } }, 'No data')); return; }
        if (kind === 'stat') {
          const v = last(list[0]);
          const spark = h('div', {});
          bodyEl.replaceChildren(h('div', { class: 'bigv', style: { color: p.Warn != null || p.Crit != null ? colorFor(v) : 'var(--text)' } }, fmtNum(v, p.Unit)), spark);
          lineChart(spark, [list[0]], { unit: p.Unit, height: 50 });
          spark.querySelector('.legend')?.remove();
          spark.querySelectorAll('.grid').forEach(g => g.remove());
        } else if (kind === 'gauge') {
          const v = last(list[0]);
          const max = p.Max ?? (p.Unit === '%' ? 100 : Math.max(...list.map(last), 1));
          bodyEl.replaceChildren(gauge(v, p.Min ?? 0, max, colorFor(v), p.Unit));
        } else if (kind === 'bar' || kind === 'table') {
          const vals = list.map(x => ({ name: x.name, v: last(x) })).sort((a, b) => b.v - a.v);
          const max = Math.max(...vals.map(x => x.v), 1e-9);
          bodyEl.replaceChildren(kind === 'bar'
            ? h('div', { class: 'bars' }, vals.map((x, i) => h('div', { class: 'b' }, h('span', { class: 'ellipsis', title: x.name }, x.name),
              h('div', { class: 'track' }, h('div', { class: 'fill', style: { width: (100 * x.v / max) + '%', background: p.Warn != null || p.Crit != null ? colorFor(x.v) : series()[i % series().length] } })), h('span', { class: 'mono' }, fmtNum(x.v, p.Unit)))))
            : (() => { const t = dataTable({ columns: [{ key: 'name', label: 'Series' }, { key: 'v', label: 'Value', num: true, render: r => fmtNum(r.v, p.Unit) }] }); t.update(vals); return t.el; })());
        } else {
          const c = lineChart(bodyEl, list, { unit: p.Unit, stack: p.Stack, height: p.Height ? Math.max(120, p.Height * 22) : 190 });
          el._chart = c;
          const i = charts.findIndex(x => x.el === el);
          if (i >= 0) charts[i] = { el, draw: c.draw }; else charts.push({ el, draw: c.draw });
        }
      } catch (e) { bodyEl.replaceChildren(h('div', { class: 'err', style: { padding: '12px 0' } }, e.message)); }
    };
    return el;
  }
  let panels = [];
  async function show() {
    drawTabs();
    charts.length = 0;
    if (cur === '_query') return showQuery();
    const d = specOf('dashboard', cur) || {};
    await loadVars(d);
    const grid = h('div', { class: 'dash' });
    panels = [];
    let rowPanels = null;
    for (const p of d.Panels || []) {
      if (p.Row && !p.Title && !p.Query && !p.Queries?.length) {
        const members = [];
        const hd = h('div', { class: 'rowh', onclick: () => { hd.classList.toggle('closed'); members.forEach(m => { m.hidden = hd.classList.contains('closed'); }); if (!hd.classList.contains('closed')) charts.forEach(c => c.draw()); } }, icon('chev', 'chev'), p.Row);
        grid.append(hd);
        rowPanels = members;
        continue;
      }
      const el = await panelEl(p);
      rowPanels?.push(el);
      grid.append(el);
      panels.push(el);
    }
    body.replaceChildren(d.Help ? h('p', { class: 'muted', style: { marginTop: 0 } }, d.Help) : '', grid);
    if (!panels.length) body.replaceChildren(emptyState('This dashboard has no panels'));
    refresh();
  }
  function refresh() { panels.forEach(p => p._load()); }
  function showQuery() {
    varBar.replaceChildren();
    const comps = resources('component').filter(r => (r.Spec?.Kind || '') === 'metrics').map(r => r.Name);
    const src = h('select', {}, h('option', { value: '' }, 'default metrics'), comps.map(c => h('option', { value: c }, c)));
    const ta = h('textarea', { rows: 3, placeholder: 'PromQL, such as sum by (service) (rate(http_requests_total[1m]))', value: store.get('metrics.adhoc', '') });
    const out = h('div', { class: 'panel' }, h('div', { class: 'muted' }, 'Run a query to chart it'));
    const runQ = async () => {
      store.set('metrics.adhoc', ta.value);
      out.replaceChildren(h('span', { class: 'spin' }));
      try {
        const list = groupSeries(frameRows(await query('metrics.query', { query: ta.value, source: src.value, range })), '');
        if (!list.length) { out.replaceChildren(h('div', { class: 'empty' }, 'No data')); return; }
        const c = lineChart(out, list, {});
        charts.splice(0, charts.length, { el: out, draw: c.draw });
      } catch (e) { out.replaceChildren(h('div', { class: 'err' }, e.message)); }
    };
    ta.addEventListener('keydown', e => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); runQ(); } });
    panels = [{ _load: () => ta.value && runQ() }];
    body.replaceChildren(h('div', { class: 'card card-b', style: { marginBottom: '14px', display: 'flex', flexDirection: 'column', gap: '10px' } }, ta,
      h('div', { class: 'row' }, src, h('span', { class: 'spacer' }), h('span', { class: 'muted' }, 'ctrl+enter runs'), btn('Run query', runQ, 'primary'))), out);
    if (ta.value) runQ();
  }
  const rangeSel = h('select', { onchange: e => { range = e.target.value; store.set('metrics.range', range); refresh(); }, 'aria-label': 'time range' },
    RANGES.map(r => h('option', { value: r, selected: r === range }, 'Last ' + r)));
  const everySel = h('select', { onchange: e => { every = +e.target.value; store.set('metrics.every', every); setTimer(); }, 'aria-label': 'refresh' },
    [[0, 'Off'], [10, '10s'], [30, '30s'], [60, '1m']].map(([v, l]) => h('option', { value: v, selected: v === every }, '⟳ ' + l)));
  ctx.el.append(pageHead('Metrics', null, varBar, rangeSel, everySel, ibtn('refresh', refresh, 'Refresh')),
    h('div', { class: 'toolbar' }, tabs), body);
  if (!dashes.length && !resources('component').length) { body.append(emptyState('No dashboards', 'Add dashboards: to rig.yaml, or run a query')); }
  show();
  setTimer();
  ctx.onKey(e => {
    if (e.key === 'r') { refresh(); return true; }
    const all = [...dashes, '_query'], i = all.indexOf(cur);
    if (e.key === ']') { go('metrics/' + all[(i + 1) % all.length]); return true; }
    if (e.key === '[') { go('metrics/' + all[(i - 1 + all.length) % all.length]); return true; }
    return false;
  });
}

// ---------- tasks ----------
function taskList() {
  return Object.entries(S.provides.actions || {}).filter(([n]) => n.startsWith('task.')).map(([n, a]) => {
    const name = n.slice(5), sp = specOf('task', name) || {};
    return { name, action: n, help: a.description || sp.Help || '', readonly: a.effect === 'read', spec: sp, group: sp.Group || '' };
  }).sort((a, b) => a.group.localeCompare(b.group) || a.name.localeCompare(b.name));
}
async function runTask(t) {
  const sp = t.spec;
  const fields = (sp.Args || []).map(a => {
    const input = a.Choices?.length
      ? h('select', {}, a.Choices.map(c => h('option', { value: c, selected: c === a.Default }, c)))
      : h('input', { value: a.Default || '', placeholder: a.Help || '' });
    return { a, input, el: h('label', { class: 'field' }, a.Name + (a.Help ? ' — ' + a.Help : ''), input) };
  });
  const extra = h('input', { placeholder: 'more arguments (optional)' });
  if (fields.length || sp.Confirm || (sp.Steps || []).length) {
    const ok = await modal('Run ' + t.name, [
      t.help && h('div', { style: { whiteSpace: 'pre-wrap' } }, t.help),
      fields.map(f => f.el),
      h('label', { class: 'field' }, 'Arguments', extra),
      (sp.Steps || []).length > 0 && h('details', {}, h('summary', {}, (sp.Steps || []).length + ' steps'), h('pre', { class: 'out' }, sp.Steps.join('\n'))),
      sp.Confirm && h('div', { style: { color: 'var(--amber)' } }, 'This task asks for confirmation before it runs.'),
    ], [['Cancel', false, 'ghost'], ['Run', true, sp.Confirm ? 'danger solid' : 'primary']]);
    if (!ok) return;
  }
  const args = [...fields.filter(f => f.input.value !== '').map(f => `${f.a.Name}=${f.input.value}`), ...extra.value.split(/\s+/).filter(Boolean)];
  const started = Date.now();
  watchJob(t.action, started);
  await act(t.action, { args }, 'task ' + t.name);
}
// watchJob opens the newest job named action started after since, following its output.
function watchJob(action, since) {
  const out = h('pre', { class: 'out', style: { maxHeight: 'none', flex: 1 } }, 'starting…');
  const head = h('div', { class: 'row' });
  const draw = () => {
    const j = [...S.jobs].reverse().find(j => j.label === action && toDate(j.start).getTime() >= since - 2000);
    if (!j) return;
    const stick = out.scrollHeight - out.scrollTop - out.clientHeight < 30;
    out.textContent = (j.output || []).join('\n') || (isRunning(j) ? 'running…' : '(no output)');
    if (j.error) out.append(h('div', { class: 'err' }, '\n' + j.error));
    if (stick) out.scrollTop = out.scrollHeight;
    head.replaceChildren(isRunning(j) ? h('span', { class: 'row' }, h('span', { class: 'spin' }), 'running') : j.error ? badge('failed', 'red', true) : badge('done', 'green', true),
      h('span', { class: 'muted' }, duration(j)));
  };
  const poll = setInterval(refreshJobs, 800);
  document.addEventListener('rig:jobs', draw);
  drawer(action.replace(/^task\./, ''), 'output', [head, out], () => { clearInterval(poll); document.removeEventListener('rig:jobs', draw); });
}
function duration(j) {
  const end = isRunning(j) ? Date.now() : toDate(j.end).getTime(), ms = end - toDate(j.start).getTime();
  return ms < 1000 ? ms + 'ms' : ms < 60000 ? (ms / 1000).toFixed(1) + 's' : Math.floor(ms / 60000) + 'm ' + Math.round(ms % 60000 / 1000) + 's';
}
function pageTasks(ctx) {
  const tasks = taskList();
  const grid = h('div', {});
  const draw = f => {
    const list = tasks.filter(t => !f || (t.name + ' ' + t.help + ' ' + t.group).toLowerCase().includes(f.toLowerCase()));
    const groups = [...new Set(list.map(t => t.group))];
    grid.replaceChildren(...groups.map(g => h('div', { class: 'section' }, g && h('h3', {}, g),
      h('div', { class: 'grid cards' }, list.filter(t => t.group === g).map(t => h('div', { class: 'card task' },
        h('div', { class: 'n' }, t.name, t.readonly && badge('read-only', 'accent'), t.spec.Confirm && badge('confirm', 'amber')),
        h('div', { class: 'd' }, firstLine(t.help) || h('span', { class: 'muted' }, 'no description')),
        h('div', { class: 'foot' }, (t.spec.Args || []).length > 0 && h('span', { class: 'muted' }, t.spec.Args.length + ' args'), h('span', { class: 'spacer' }),
          btn([icon('play'), 'Run'], () => runTask(t), 'primary sm'))))))));
    if (!list.length) grid.replaceChildren(emptyState(tasks.length ? 'No task matches' : 'No tasks', tasks.length ? '' : 'Add tasks: to rig.yaml'));
  };
  ctx.el.append(pageHead('Tasks', 'The project\'s tasks for this environment', searchBox('Filter tasks', draw)), grid);
  draw('');
}

// ---------- queries ----------
function pageQueries(ctx) {
  const qs = resources('query').sort((a, b) => (a.Spec?.Group || '').localeCompare(b.Spec?.Group || '') || a.Name.localeCompare(b.Name));
  const comps = resources('component').map(r => r.Name).sort();
  let cur = ctx.rest[0] || qs[0]?.Name || '_adhoc';
  const list = h('div', { class: 'card list' });
  const right = h('div', {});
  const drawList = () => {
    let group = null;
    list.replaceChildren(...qs.flatMap(q => {
      const out = [];
      if ((q.Spec?.Group || '') !== group) { group = q.Spec?.Group || ''; if (group) out.push(h('div', { class: 'group-h' }, group)); }
      out.push(h('div', { class: 'item' + (q.Name === cur ? ' sel' : ''), onclick: () => { cur = q.Name; drawList(); drawRight(); } },
        h('div', { class: 'n' }, q.Name, h('span', { class: 'muted', style: { fontWeight: 400, fontSize: '12px' } }, q.Spec?.Source)), h('div', { class: 'd' }, firstLine(q.Spec?.Help) || firstLine(q.Spec?.Query))));
      return out;
    }), h('div', { class: 'item' + (cur === '_adhoc' ? ' sel' : ''), onclick: () => { cur = '_adhoc'; drawList(); drawRight(); } }, h('div', { class: 'n' }, 'Ad-hoc query'), h('div', { class: 'd' }, 'any component that answers queries')));
  };
  function result(promise) {
    const out = h('div', {}, h('span', { class: 'spin' }));
    promise.then(f => {
      const t = frameTable(f);
      const rows = frameRows(f), cols = (f.fields || []).map(x => x.name);
      out.replaceChildren(h('div', { class: 'toolbar' }, h('b', {}, rows.length + ' rows'), f.meta?.notice && h('span', { class: 'muted' }, f.meta.notice), h('span', { class: 'spacer' }),
        searchBox('Filter rows', v => t.filter(v)), btn([icon('copy'), 'CSV'], () => copy(toCSV(rows, cols)), 'sm'),
        btn([icon('copy'), 'JSON'], () => copy(JSON.stringify(rows, null, 2)), 'sm')), t.el);
    }, e => out.replaceChildren(h('div', { class: 'card card-b err' }, e.message)));
    return out;
  }
  function drawRight() {
    history.replaceState(null, '', '#/queries/' + encodeURIComponent(cur));
    if (cur === '_adhoc') {
      const comp = h('select', {}, comps.map(c => h('option', { value: c }, c)));
      const ta = h('textarea', { rows: 6, placeholder: 'the component\'s query language: SQL, PromQL, a Redis command…' });
      const res = h('div', {});
      const run = () => res.replaceChildren(result(query('queries.run', { component: comp.value, text: ta.value })));
      ta.addEventListener('keydown', e => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); run(); } });
      right.replaceChildren(h('div', { class: 'card card-b', style: { display: 'flex', flexDirection: 'column', gap: '10px', marginBottom: '14px' } },
        h('div', { class: 'row' }, h('b', {}, 'Ad-hoc query'), h('span', { class: 'spacer' }), comp), ta,
        h('div', { class: 'row' }, h('span', { class: 'spacer' }), h('span', { class: 'muted' }, 'ctrl+enter runs'), btn([icon('play'), 'Run'], run, 'primary'))), res);
      return;
    }
    const q = qs.find(x => x.Name === cur);
    if (!q) { right.replaceChildren(emptyState('No saved queries', 'Add queries: to rig.yaml, or use an ad-hoc query')); return; }
    const params = Object.entries(q.Spec?.Params || {}).map(([k, d]) => ({ k, input: h('input', { value: d || '', placeholder: k }) }));
    const res = h('div', {});
    const run = () => res.replaceChildren(result(query('queries.run', { name: q.Name, params: Object.fromEntries(params.map(p => [p.k, p.input.value])) })));
    right.replaceChildren(h('div', { class: 'card card-b', style: { display: 'flex', flexDirection: 'column', gap: '10px', marginBottom: '14px' } },
      h('div', { class: 'row' }, h('b', { style: { fontSize: '15px' } }, q.Name), badge(q.Spec?.Source || 'query', 'accent'), h('span', { class: 'spacer' }), btn([icon('play'), 'Run'], run, 'primary')),
      q.Spec?.Help && h('div', { class: 'muted', style: { whiteSpace: 'pre-wrap' } }, q.Spec.Help),
      h('pre', { class: 'out' }, q.Spec?.Query || ''),
      params.length > 0 && h('div', { class: 'row wrap' }, params.map(p => h('label', { class: 'field' }, p.k, p.input)))), res);
    if (!params.length) run();
  }
  ctx.el.append(pageHead('Queries', 'Saved queries from rig.yaml, and ad-hoc ones'), h('div', { class: 'split' }, list, right));
  drawList(); drawRight();
}

// ---------- jobs ----------
function pageJobs(ctx) {
  let cur = null;
  const list = h('div', { class: 'card list', style: { maxHeight: 'calc(100vh - 180px)', overflow: 'auto' } });
  const right = h('div', {});
  const draw = () => {
    const jobs = [...S.jobs].reverse();
    if (!cur && jobs[0]) cur = jobs[0].id;
    list.replaceChildren(...jobs.map(j => h('div', { class: 'job' + (j.id === cur ? ' sel' : ''), onclick: () => { cur = j.id; draw(); } },
      isRunning(j) ? h('span', { class: 'spin' }) : j.error ? h('span', { style: { color: 'var(--red)' } }, '✕') : h('span', { style: { color: 'var(--green)' } }, '✓'),
      h('div', { class: 'grow' }, h('div', { class: 'l ellipsis' }, j.label), h('div', { class: 'muted', style: { fontSize: '12px' } }, toDate(j.start).toLocaleTimeString([], { hour12: false }) + ' · ' + duration(j))))));
    if (!jobs.length) list.replaceChildren(h('div', { class: 'empty' }, 'Nothing has run yet in this session'));
    const j = jobs.find(x => x.id === cur);
    if (!j) { right.replaceChildren(); return; }
    const pre = right.querySelector('pre');
    const stick = !pre || pre.scrollHeight - pre.scrollTop - pre.clientHeight < 30;
    const out = h('pre', { class: 'out', style: { maxHeight: 'calc(100vh - 240px)' } }, (j.output || []).join('\n') || (isRunning(j) ? 'running…' : '(no output)'), j.error && h('div', { class: 'err' }, '\n' + j.error));
    right.replaceChildren(h('div', { class: 'toolbar' }, h('b', { style: { fontSize: '15px' } }, j.label), isRunning(j) ? badge('running', 'accent pulse', true) : j.error ? badge('failed', 'red', true) : badge('done', 'green', true),
      h('span', { class: 'muted' }, duration(j)), h('span', { class: 'spacer' }), btn([icon('copy'), 'Copy'], () => copy((j.output || []).join('\n')), 'sm')), out);
    if (stick) out.scrollTop = out.scrollHeight; else out.scrollTop = pre.scrollTop;
  };
  ctx.el.append(pageHead('Activity', 'What ran from this page in this session: actions, tasks and their output'), h('div', { class: 'split' }, list, right));
  document.addEventListener('rig:jobs', draw);
  ctx.onCleanup(() => document.removeEventListener('rig:jobs', draw));
  refreshJobs().then(draw);
  ctx.every(1500, () => refreshJobs());
}

// ---------- inbox ----------
function pageInbox(ctx) {
  const box = h('div', { class: 'grid', style: { gap: '12px' } });
  const draw = () => {
    const items = [...S.inbox].sort((a, b) => (a.status === 'pending' ? 0 : 1) - (b.status === 'pending' ? 0 : 1) || toDate(b.time) - toDate(a.time));
    box.replaceChildren(...items.map(it => h('div', { class: 'card', style: { opacity: it.status === 'pending' ? 1 : 0.65 } },
      h('div', { class: 'card-h' }, h('span', { class: 'mono' }, it.action), badge(it.status, { pending: 'amber', approved: 'green', done: 'green', rejected: 'red', failed: 'red' }[it.status] || '', true),
        h('span', { class: 'spacer' }), h('span', { class: 'muted', style: { fontWeight: 400 } }, toDate(it.time).toLocaleString())),
      h('div', { class: 'card-b', style: { display: 'flex', flexDirection: 'column', gap: '10px' } },
        h('dl', { class: 'kv' }, h('dt', {}, 'Asked by'), h('dd', {}, `${it.subject?.name || ''} (${it.subject?.kind || ''})`),
          it.env && [h('dt', {}, 'Environment'), h('dd', {}, it.env)], it.why && [h('dt', {}, 'Why'), h('dd', {}, it.why)],
          it.by && [h('dt', {}, 'Decided by'), h('dd', {}, it.by)], it.result && [h('dt', {}, 'Result'), h('dd', {}, it.result)]),
        it.args && Object.keys(it.args).length > 0 && h('pre', { class: 'out' }, JSON.stringify(it.args, null, 2)),
        it.plan?.message && h('div', {}, h('b', {}, 'Plan: '), it.plan.message),
        it.status === 'pending' && h('div', { class: 'row' }, h('span', { class: 'spacer' }),
          btn('Reject', () => decide(it, false), 'danger'), btn([icon('check'), 'Approve and run'], () => decide(it, true), 'primary'))))));
    if (!items.length) box.replaceChildren(emptyState('The inbox is empty', 'Writes the AI or an agent asks for wait here for approval'));
  };
  async function decide(it, approve) {
    try { const r = await api('decide', { id: it.id, approve }); toast((approve ? 'Approved ' : 'Rejected ') + it.action + (r?.message ? ': ' + r.message : ''), 'ok'); }
    catch (e) { toast(e.message, 'bad', 8000); }
    await refreshInbox(); draw();
  }
  ctx.el.append(pageHead('Inbox', 'Writes waiting for approval', ibtn('refresh', () => refreshInbox().then(draw), 'Refresh')), box);
  refreshInbox().then(draw);
  ctx.every(5000, () => refreshInbox().then(draw));
}

// ---------- api explorer ----------
function schemaForm(schema) {
  const props = schema?.properties || {}, req = new Set(schema?.required || []);
  const fields = Object.entries(props).map(([k, p]) => {
    const t = p.type || 'string';
    let input;
    if (p.enum) input = h('select', {}, h('option', { value: '' }, ''), p.enum.map(v => h('option', { value: v }, v)));
    else if (t === 'boolean') input = h('input', { type: 'checkbox' });
    else if (t === 'integer' || t === 'number') input = h('input', { type: 'number', step: t === 'integer' ? 1 : 'any' });
    else if (t === 'object') input = h('textarea', { rows: 3, placeholder: '{ }' });
    else if (k === 'service' || (t === 'array' && k === 'services') || (t === 'array' && k === 'targets')) {
      input = t === 'array' ? h('select', { multiple: true, style: { height: '90px' } }, serviceNames().map(n => h('option', { value: n }, n)))
        : h('select', {}, h('option', { value: '' }, ''), serviceNames().map(n => h('option', { value: n }, n)));
    } else input = h('input', { placeholder: t === 'array' ? 'comma separated' : p.description || '' });
    const get = () => {
      if (t === 'boolean') return input.checked || undefined;
      if (input.multiple) { const v = [...input.selectedOptions].map(o => o.value); return v.length ? v : undefined; }
      const v = input.value.trim();
      if (v === '') return undefined;
      if (t === 'integer' || t === 'number') return +v;
      if (t === 'array') return v.split(',').map(x => x.trim()).filter(Boolean);
      if (t === 'object') return JSON.parse(v);
      return v;
    };
    return { k, get, el: h('label', { class: 'field' }, h('span', {}, k, req.has(k) && h('span', { style: { color: 'var(--red)' } }, ' *'), p.description && h('span', { class: 'muted' }, ' — ' + p.description)), input) };
  });
  const raw = h('textarea', { rows: 4, placeholder: 'extra arguments as JSON, merged over the fields' });
  return {
    el: h('div', { style: { display: 'flex', flexDirection: 'column', gap: '10px' } }, fields.map(f => f.el), h('details', {}, h('summary', { class: 'muted' }, 'JSON arguments'), raw)),
    value() {
      const out = {};
      for (const f of fields) { const v = f.get(); if (v !== undefined) out[f.k] = v; }
      if (raw.value.trim()) Object.assign(out, JSON.parse(raw.value));
      return out;
    },
  };
}
function pageAPI(ctx) {
  const srcs = Object.entries(S.provides.sources || {}).map(([n, x]) => ({ kind: 'source', name: n, ...x }));
  const acts = Object.entries(S.provides.actions || {}).map(([n, x]) => ({ kind: 'action', name: n, ...x }));
  const all = [...srcs, ...acts].sort((a, b) => a.name.localeCompare(b.name));
  let cur = ctx.rest.join('/') || all[0]?.name, filter = '', ctrl;
  const list = h('div', { class: 'card list', style: { maxHeight: 'calc(100vh - 180px)', overflow: 'auto' } });
  const right = h('div', {});
  ctx.onCleanup(() => ctrl?.abort());
  const effectBadge = e => badge(e || 'read', { read: 'accent', write: 'amber', danger: 'red' }[e || 'read']);
  const drawList = () => {
    const f = filter.toLowerCase();
    const sec = (title, items) => items.length ? [h('div', { class: 'group-h' }, title + ' · ' + items.length), ...items.map(x => h('div', { class: 'item' + (x.name === cur ? ' sel' : ''), onclick: () => { cur = x.name; drawList(); drawRight(); } },
      h('div', { class: 'n mono', style: { fontSize: '13px' } }, x.name, x.kind === 'action' ? effectBadge(x.effect) : x.stream && badge('stream', 'purple')), h('div', { class: 'd' }, firstLine(x.description))))] : [];
    const m = x => !f || (x.name + ' ' + (x.description || '')).toLowerCase().includes(f);
    list.replaceChildren(...sec('Sources', srcs.filter(m)), ...sec('Actions', acts.filter(m)));
  };
  function drawRight() {
    ctrl?.abort();
    const x = all.find(y => y.name === cur);
    if (!x) { right.replaceChildren(emptyState('Pick a source or an action')); return; }
    history.replaceState(null, '', '#/api/' + x.name);
    const form = schemaForm(x.args);
    const res = h('div', {});
    const runIt = async (live) => {
      let args;
      try { args = form.value(); } catch (e) { toast('arguments: ' + e.message, 'bad'); return; }
      if (x.kind === 'action') {
        const r = await act(x.name, args, x.name);
        res.replaceChildren(r ? h('div', { class: 'card card-b' }, h('b', {}, r.result?.message || 'done'), r.result?.refs?.length > 0 && h('div', { class: 'muted' }, r.result.refs.join(' ')), r.job?.output?.length > 0 && h('pre', { class: 'out', style: { marginTop: '10px' } }, r.job.output.join('\n'))) : '');
        return;
      }
      if (live) {
        ctrl?.abort(); ctrl = new AbortController();
        const rows = [];
        let t = null;
        const status = h('span', { class: 'muted' }, 'streaming…');
        res.replaceChildren(h('div', { class: 'toolbar' }, status, h('span', { class: 'spacer' }), btn([icon('stop'), 'Stop'], () => { ctrl.abort(); status.textContent = 'stopped'; }, 'sm')));
        stream(x.name, args, b => {
          if (b.frame) {
            if (!t) { t = frameTable(b.frame); res.append(t.el); rows.length = 0; }
            rows.push(...frameRows(b.frame)); if (rows.length > 2000) rows.splice(0, rows.length - 2000);
            t.update(rows);
          }
          if (b.lines?.length) res.append(h('pre', { class: 'out' }, b.lines.join('\n')));
          if (b.done) status.textContent = 'done · ' + rows.length + ' rows';
        }, ctrl.signal).catch(e => { if (e.name !== 'AbortError') res.append(h('div', { class: 'err' }, e.message)); });
        return;
      }
      res.replaceChildren(h('span', { class: 'spin' }));
      try {
        const f = await query(x.name, args);
        const t = frameTable(f), rows = frameRows(f), cols = (f.fields || []).map(c => c.name);
        res.replaceChildren(h('div', { class: 'toolbar' }, h('b', {}, rows.length + ' rows'), f.meta?.notice && h('span', { class: 'muted' }, f.meta.notice), h('span', { class: 'spacer' }),
          searchBox('Filter rows', v => t.filter(v)), btn([icon('copy'), 'CSV'], () => copy(toCSV(rows, cols)), 'sm'), btn([icon('copy'), 'JSON'], () => copy(JSON.stringify(rows, null, 2)), 'sm')), t.el);
      } catch (e) { res.replaceChildren(h('div', { class: 'card card-b err' }, e.message)); }
    };
    right.replaceChildren(h('div', { class: 'card card-b', style: { display: 'flex', flexDirection: 'column', gap: '12px', marginBottom: '14px' } },
      h('div', { class: 'row' }, h('b', { class: 'mono', style: { fontSize: '15px' } }, x.name), x.kind === 'action' ? effectBadge(x.effect) : badge('source', 'accent'), x.stream && badge('stream', 'purple'), h('span', { class: 'spacer' }),
        x.kind === 'source' && x.stream && btn([icon('play'), 'Stream'], () => runIt(true)),
        btn([icon('play'), x.kind === 'action' ? 'Run' : 'Query'], () => runIt(false), x.effect === 'danger' ? 'danger solid' : 'primary')),
      x.description && h('div', { class: 'muted', style: { whiteSpace: 'pre-wrap' } }, x.description), form.el), res);
  }
  ctx.el.append(pageHead('API', 'Every source and action the engine serves: the same calls the CLI, the MCP server and the AI make', searchBox('Filter', v => { filter = v; drawList(); })),
    h('div', { class: 'split' }, list, right));
  drawList(); drawRight();
}

// ---------- settings ----------
const LAYOUTS = [
  { id: 'side', t: 'Sidebar', d: 'Navigation down the left: a standard web app', pv: [['a', 0, 0, 22, 100], ['', 28, 8, 66, 10], ['', 28, 26, 66, 60]] },
  { id: 'top', t: 'Top bar', d: 'Navigation across the top, the page full width', pv: [['a', 0, 0, 100, 14], ['', 6, 22, 88, 10], ['', 6, 38, 88, 52]] },
  { id: 'terminal', t: 'Terminal', d: 'The TUI\'s look: monospace, numbered tabs, key hints, keys first', pv: [['a', 0, 0, 30, 10], ['', 32, 0, 20, 10], ['', 3, 16, 94, 70], ['', 0, 92, 100, 8]] },
];
function pageSettings(ctx) {
  const themes = S.info?.themes || {};
  const light = document.documentElement.dataset.scheme === 'light';
  const seg = (k, opts) => h('div', { class: 'seg' }, opts.map(([v, l]) => h('button', { class: S.prefs[k] === v ? 'on' : '', onclick: () => setPref(k, v) }, l)));
  ctx.el.append(pageHead('Settings', 'Saved in this browser'),
    h('div', { class: 'section' }, h('h3', {}, 'Layout'), h('div', { class: 'layouts' }, LAYOUTS.map(l => h('button', { class: 'layout-opt' + (S.prefs.layout === l.id ? ' on' : ''), onclick: () => setPref('layout', l.id) },
      h('div', { class: 'pv' }, l.pv.map(([c, x, y, w, hh]) => h('b', { class: c, style: { left: x + '%', top: y + '%', width: w + '%', height: hh + '%' } }))),
      h('div', { class: 't' }, l.t), h('div', { class: 'd' }, l.d))))),
    h('div', { class: 'section' }, h('h3', {}, 'Theme'), h('p', { class: 'muted', style: { marginTop: 0 } }, 'The terminal UI\'s themes (rig theme), your own files in ~/.config/rig/themes included'),
      h('div', { class: 'themes' }, Object.keys(themes).sort().map(n => {
        const t = themes[n], sh = light ? t.Light : t;
        return h('button', { class: 'theme' + (S.themeName === n ? ' on' : ''), onclick: () => setPref('theme', n) },
          h('div', { class: 'pv', style: { background: sh.Bar } },
            h('div', { class: 'sb', style: { background: `color-mix(in srgb, ${sh.Bar}, ${sh.Text} 5%)` } }, h('i', { style: { background: t.Accent } }), h('i', { style: { background: sh.Panel } }), h('i', { style: { background: sh.Panel } })),
            h('div', { class: 'mn' }, h('i', { style: { background: sh.Text, width: '60%', opacity: 0.8 } }), h('i', { style: { background: sh.Dim, width: '85%' } }),
              h('div', { class: 'row', style: { gap: '4px' } }, (t.Series || []).slice(0, 5).map(c => h('i', { style: { background: c, width: '18%', height: '14px' } }))))),
          h('div', { class: 'nm' }, n, S.info?.theme === n && h('span', { class: 'muted', style: { fontWeight: 400, fontSize: '12px' } }, 'tui'),
            h('span', { class: 'sw' }, [t.Accent, t.Green, t.Amber, t.Red].map(c => h('i', { style: { background: c } })))));
      }))),
    h('div', { class: 'section' }, h('h3', {}, 'Appearance'), h('div', { class: 'row wrap', style: { gap: '18px' } },
      h('label', { class: 'field' }, 'Mode', seg('scheme', [['auto', 'System'], ['dark', 'Dark'], ['light', 'Light']])),
      h('label', { class: 'field' }, 'Density', seg('density', [['comfortable', 'Comfortable'], ['compact', 'Compact']])),
      h('label', { class: 'field' }, 'Font', seg('font', [['sans', 'Sans'], ['mono', 'Monospace']])))),
    h('div', { class: 'section' }, h('h3', {}, 'Keyboard'), shortcutsTable()),
    h('div', { class: 'section' }, btn('Reset to defaults', () => { S.prefs = { theme: '', scheme: 'auto', layout: 'side', density: 'comfortable', font: 'sans' }; store.set('prefs', S.prefs); applyPrefs(); renderChrome(); route(); }, 'ghost')));
}
function shortcutsTable() {
  const rows = [['1 – 9', 'go to a screen'], ['Ctrl/⌘ K', 'search and run anything'], ['/', 'focus the page\'s filter'], ['?', 'this help'], ['Esc', 'close a panel or dialog'],
    ...PAGES.filter(p => p.keys).flatMap(p => p.keys.map(([k, d]) => [k, p.title + ': ' + d]))];
  return h('div', { class: 'table-wrap' }, h('table', {}, h('tbody', {}, rows.map(([k, d]) => h('tr', {}, h('td', { style: { width: '160px' } }, h('kbd', {}, k)), h('td', {}, d))))));
}

// ---------- palette ----------
function openPalette() {
  const items = [
    ...PAGES.map(p => ({ k: 'screen', t: p.title, run: () => go(p.id) })),
    ...serviceNames().flatMap(n => [
      { k: 'service', t: n, d: 'open', run: () => { go('services'); setTimeout(() => openService(n), 50); } },
      { k: 'logs', t: n, d: 'logs', run: () => go('logs', { svc: n }) },
      { k: 'restart', t: n, d: 'restart', run: () => act('runtime.restart', { service: n }, 'restart ' + n) }]),
    ...taskList().map(t => ({ k: 'task', t: t.name, d: firstLine(t.help), run: () => runTask(t) })),
    ...resources('dashboard').map(r => ({ k: 'dashboard', t: r.Name, run: () => go('metrics/' + r.Name) })),
    ...resources('query').map(r => ({ k: 'query', t: r.Name, d: firstLine(r.Spec?.Help), run: () => go('queries/' + r.Name) })),
    ...Object.keys(S.info?.themes || {}).map(n => ({ k: 'theme', t: 'Theme: ' + n, run: () => setPref('theme', n) })),
    ...LAYOUTS.map(l => ({ k: 'layout', t: 'Layout: ' + l.t, run: () => setPref('layout', l.id) })),
    { k: 'mode', t: 'Toggle light / dark', run: () => setPref('scheme', document.documentElement.dataset.scheme === 'light' ? 'dark' : 'light') },
    ...resources('environment').map(r => ({ k: 'environment', t: 'Switch to ' + r.Name, d: r.Spec?.Description, run: () => switchEnv(r.Name) })),
  ];
  let sel = 0, shown = items;
  const input = h('input', { placeholder: 'Search screens, services, tasks, dashboards, themes…', 'aria-label': 'command' });
  const res = h('div', { class: 'res' });
  const draw = () => {
    const words = input.value.toLowerCase().split(/\s+/).filter(Boolean);
    shown = items.filter(x => words.every(w => (x.k + ' ' + x.t + ' ' + (x.d || '')).toLowerCase().includes(w))).slice(0, 60);
    sel = Math.min(sel, Math.max(0, shown.length - 1));
    res.replaceChildren(...shown.map((x, i) => h('div', { class: 'opt' + (i === sel ? ' sel' : ''), onmousemove: () => { if (sel !== i) { sel = i; draw(); } }, onclick: () => pick(x) },
      h('span', {}, x.t), x.d && h('span', { class: 'd' }, x.d), h('span', { class: 'k' }, x.k))));
    if (!shown.length) res.append(h('div', { class: 'empty', style: { padding: '20px' } }, 'Nothing matches'));
    res.querySelector('.sel')?.scrollIntoView({ block: 'nearest' });
  };
  const pick = x => { closeOverlay(); x.run(); };
  input.addEventListener('input', () => { sel = 0; draw(); });
  input.addEventListener('keydown', e => {
    if (e.key === 'ArrowDown') { sel = Math.min(shown.length - 1, sel + 1); draw(); e.preventDefault(); }
    else if (e.key === 'ArrowUp') { sel = Math.max(0, sel - 1); draw(); e.preventDefault(); }
    else if (e.key === 'Enter' && shown[sel]) { e.preventDefault(); pick(shown[sel]); }
  });
  openOverlay([h('div', { class: 'palette' }, input, res)]);
  draw();
  input.focus();
}

// ---------- keys ----------
document.addEventListener('keydown', e => {
  const typing = e.target.closest?.('input,textarea,select,[contenteditable]');
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); overlay ? closeOverlay() : openPalette(); return; }
  if (e.key === 'Escape') { if (overlay) { closeOverlay(); e.preventDefault(); } else if (typing) e.target.blur(); return; }
  if (typing || overlay || e.ctrlKey || e.metaKey || e.altKey) return;
  if (S.keyHandler?.(e)) { e.preventDefault(); return; }
  if (/^[1-9]$/.test(e.key) && PAGES[+e.key - 1]) { go(PAGES[+e.key - 1].id); e.preventDefault(); return; }
  if (e.key === '/') { const f = $('.page [data-filter]'); if (f) { f.focus(); e.preventDefault(); } return; }
  if (e.key === '?') { modal('Keyboard shortcuts', shortcutsTable(), [['Close', true, 'primary']]); e.preventDefault(); }
});

// ---------- boot ----------
function showLocked() {
  const app = $('#app');
  if (app.dataset.locked) return;
  app.dataset.locked = '1';
  app.replaceChildren(h('div', { class: 'empty', style: { paddingTop: '20vh' } },
    h('div', { class: 'brand', style: { justifyContent: 'center', fontSize: '20px' } }, h('span', { class: 'logo' }, 'r'), 'rig'),
    h('div', { class: 'big' }, 'Open the link rig web printed'), h('div', {}, 'It carries the token this page needs. Run ', h('code', {}, 'rig web'), ' in the project.')));
}
async function boot(again) {
  try {
    const headers = { Authorization: 'Bearer ' + S.token, 'X-Rig-Env': S.env };
    const r = await fetch('/v1/web', { headers });
    if (r.status === 401) { showLocked(); return; }
    const info = await r.json();
    if (!r.ok) throw new Error(info.error?.message || r.statusText);
    S.info = info;
    if (!S.env) S.env = '';
    const [cat, prov] = await Promise.all([api('catalog'), api('provides')]);
    S.catalog = cat || []; S.provides = prov || { sources: {}, actions: {} };
  } catch (e) {
    if (S.env && !again) { S.env = ''; store.set('env', ''); return boot(true); }
    $('#app').replaceChildren(h('div', { class: 'empty', style: { paddingTop: '20vh' } }, h('div', { class: 'big' }, 'rig web cannot reach the engine'), h('div', { class: 'err' }, e.message)));
    return;
  }
  applyPrefs();
  if (!shell) { shell = h('div', { class: 'shell' }); toastsEl = h('div', { class: 'toasts' }); $('#app').replaceChildren(shell, toastsEl); }
  renderChrome();
  route();
  refreshJobs(); refreshInbox();
}
(function start() {
  const m = /[#&]token=([^&]+)/.exec(location.hash);
  if (m) {
    S.token = decodeURIComponent(m[1]); store.set('token', S.token);
    const env = /[&#]env=([^&]+)/.exec(location.hash);
    if (env) { S.env = decodeURIComponent(env[1]); store.set('env', S.env); }
    history.replaceState(null, '', location.pathname + '#/services');
  }
  applyPrefs();
  if (!S.token) { showLocked(); return; }
  boot();
  setInterval(() => { if (document.hidden || !S.info) return; refreshInbox(); if (S.running || S.jobs.some(isRunning)) refreshJobs(); }, 3000);
})();
