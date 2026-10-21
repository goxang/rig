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
  term: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M7 9l3 3-3 3M13 15h4"/>',
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
  home: '<path d="M3 11l9-7 9 7v9a1 1 0 0 1-1 1h-5v-6h-6v6H4a1 1 0 0 1-1-1z"/>',
  data: '<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v14c0 1.7 3.6 3 8 3s8-1.3 8-3V5M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3"/>',
  kv: '<circle cx="8" cy="15" r="4"/><path d="M11 12l9-9M17 6l3 3M15 8l2 2"/>',
  traces: '<path d="M3 6h8M7 12h10M11 18h10"/><circle cx="3" cy="6" r="1"/>',
  load: '<path d="M12 21a9 9 0 1 1 9-9"/><path d="M12 12l5-5"/>',
  hosts: '<rect x="3" y="3" width="18" height="8" rx="2"/><rect x="3" y="13" width="18" height="8" rx="2"/><path d="M7 7h.01M7 17h.01M11 7h6M11 17h6"/>',
  tests: '<path d="M9 3h6M10 3v6L4 20a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1l-6-11V3"/><path d="M7 15h10"/>',
  manifests: '<path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"/><path d="M14 3v6h6M8 13h8M8 17h5"/>',
  ai: '<path d="M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9z"/><path d="M19 15l.9 2.1L22 18l-2.1.9L19 21l-.9-2.1L16 18l2.1-.9z"/>',
  plugin: '<path d="M9 2v6M15 2v6M6 8h12v4a6 6 0 0 1-12 0zM12 18v4"/>',
  view: '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M3 9h18M9 21V9"/>',
  git: '<circle cx="6" cy="6" r="2"/><circle cx="6" cy="18" r="2"/><circle cx="18" cy="8" r="2"/><path d="M6 8v8M18 10c0 4-6 3-10 7"/>',
  board: '<rect x="3" y="4" width="5" height="16" rx="1"/><rect x="10" y="4" width="5" height="10" rx="1"/><rect x="17" y="4" width="4" height="13" rx="1"/>',
  doc: '<path d="M4 4h10l6 6v10H4z"/><path d="M14 4v6h6"/>',
  terminal: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M7 9l3 3-3 3M13 15h4"/>',
  cmd: '<path d="M5 7l5 5-5 5M12 19h7"/>', send: '<path d="M4 12l16-8-6 16-2-7z"/>', plus: '<path d="M12 5v14M5 12h14"/>',
  folder: '<path d="M3 6a1 1 0 0 1 1-1h5l2 2h9a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1z"/>', back: '<path d="M15 6l-6 6 6 6"/>',
  right: '<path d="M9 6l6 6-6 6"/>', edit: '<path d="M4 20h4L19 9l-4-4L4 16z"/>', menu: '<path d="M4 6h16M4 12h16M4 18h16"/>',
  bell: '<path d="M6 16V11a6 6 0 1 1 12 0v5l2 2H4z"/><path d="M10 21h4"/>', env: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18"/>',
};
function icon(name, cls = 'ico') {
  const el = document.createElementNS(svgNS, 'svg');
  el.setAttribute('viewBox', '0 0 24 24'); el.setAttribute('class', cls);
  el.setAttribute('fill', 'none'); el.setAttribute('stroke', 'currentColor'); el.setAttribute('stroke-width', '2');
  el.setAttribute('stroke-linecap', 'round'); el.setAttribute('stroke-linejoin', 'round');
  el.innerHTML = ICONS[name] || '';
  return el;
}
// a click that starts async work shows a spinner on the button and ignores clicks until it settles
const busyClick = onclick => !onclick ? onclick : function (e) {
  const el = e?.currentTarget;
  if (el?.classList?.contains('busy')) return;
  const r = onclick.call(this, e);
  if (r && typeof r.then === 'function' && el) {
    el.classList.add('busy'); el.setAttribute('aria-busy', 'true');
    const done = () => { el.classList.remove('busy'); el.removeAttribute('aria-busy'); };
    r.then(done, done);
  }
  return r;
};
const btn = (label, onclick, cls = '', title) => h('button', { class: 'btn ' + cls, onclick: busyClick(onclick), title, type: 'button' }, label);
const ibtn = (name, onclick, title, cls = '') => h('button', { class: 'btn icon ghost ' + cls, onclick: busyClick(onclick), title, 'aria-label': title, type: 'button' }, icon(name));
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
  cleanups: [], trail: [], keyHandler: null, pageKeys: [], views: [], menu: [],
};
const series = () => S.palette?.Series || ['#73BF69', '#F2CC0C', '#5794F2', '#FF780A', '#F2495C', '#B877D9'];
function colorOf(name) {
  let x = 0; for (const c of String(name)) x = (x * 31 + c.charCodeAt(0)) >>> 0;
  const ss = series(); return ss[x % ss.length];
}

// ---------- api ----------
class ApiError extends Error {}
let inflight = 0, barTimer;
function progress(delta) {
  inflight = Math.max(0, inflight + delta);
  clearTimeout(barTimer);
  const bar = document.getElementById('progress');
  if (!bar) return;
  if (inflight > 0) barTimer = setTimeout(() => { bar.hidden = false; }, 700);
  else bar.hidden = true;
}
async function api(method, body = {}, confirm = false, signal) {
  const slow = method === 'run';
  if (slow) progress(1);
  try { return await apiCall(method, body, confirm, signal); } finally { if (slow) progress(-1); }
}
async function apiCall(method, body = {}, confirm = false, signal) {
  const headers = authHeaders();
  if (confirm) headers['X-Rig-Confirm'] = '1';
  const r = await fetch('/v1/engine/' + method, { method: 'POST', headers, body: JSON.stringify(body), signal });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) {
    const e = new ApiError(j.error?.message || r.statusText);
    Object.assign(e, { status: r.status, code: j.error?.code, hint: j.error?.data?.hint });
    if (r.status === 401) showLocked();
    throw e;
  }
  return j;
}
const query = (source, args = {}, signal) => {
  const t = AbortSignal.timeout(source.startsWith('metrics.') ? 25000 : 90000);
  return api('query', { source, args }, false, signal ? AbortSignal.any([signal, t]) : t)
    .catch(e => { throw e.name === 'TimeoutError' ? new Error(source + ' timed out: the data source is slow or unreachable') : e; });
};
const authHeaders = () => ({ 'Content-Type': 'application/json', Authorization: 'Bearer ' + S.token, 'X-Rig-Env': S.env });
// web calls the page's own routes (views, the command line, the assistant) beside the Engine API.
async function web(path, body) {
  const r = await fetch('/v1/' + path, body === undefined ? { headers: authHeaders() } : { method: 'POST', headers: authHeaders(), body: JSON.stringify(body) });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) { if (r.status === 401) showLocked(); throw new ApiError(j.error?.message || r.statusText); }
  return j;
}
async function ndjson(path, body, onLine, signal) {
  const r = await fetch('/v1/' + path, { method: 'POST', signal, headers: authHeaders(), body: JSON.stringify(body) });
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
      if (line.trim()) onLine(JSON.parse(line));
    }
  }
}
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
const stream = (source, args, onBatch, signal) => ndjson('engine/stream', { source, args }, onBatch, signal);
// act runs an action through policy: a confirm comes back as a question, asked here.
// pending maps a service to what is happening to it, so its row shows "restarting…" until the action returns.
const pending = new Map();
const PENDING_VERB = { 'env.up': 'starting…', 'env.down': 'stopping…', 'runtime.restart': 'restarting…' };
async function act(action, args = {}, label = action) {
  const names = PENDING_VERB[action] ? (args.targets || [args.service]).filter(Boolean) : [];
  const mark = on => { names.forEach(n => on ? pending.set(n, PENDING_VERB[action]) : pending.delete(n)); if (names.length) document.dispatchEvent(new Event('rig:pending')); };
  const go = async confirm => {
    S.running++; refreshJobs(); setTimeout(refreshJobs, 400); mark(true);
    try { return await api('run', { action, args }, confirm); } finally { mark(false); S.running--; refreshJobs(); }
  };
  try {
    let res;
    try { res = await go(false); } catch (e) {
      if (e.hint !== 'confirm' && e.hint !== 'reauth') throw e;
      if (!await confirmBox('Confirm ' + label, e.message.replace(/^(confirm|reauth): /, ''), 'Run', true)) return null;
      res = await go(true);
    }
    const m = res.result?.message;
    toast(m && !m.startsWith(action) ? m : label + ' done', 'ok');
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
// Overlays stack: a dialog over a drawer closes alone, and esc closes the top one.
let overlay = null;
const overlays = [];
function closeOverlay() { overlays.at(-1)?.close(); }
function closeAll() { while (overlays.length) overlays.at(-1).close(); }
function openOverlay(nodes, onClose, { clear = false } = {}) {
  const scrim = h('div', { class: 'scrim' + (clear ? ' clear' : ''), onclick: () => o.close() });
  scrim.style.zIndex = 40 + overlays.length * 2;
  if (clear) scrim.oncontextmenu = e => {
    e.preventDefault(); o.close();
    document.elementFromPoint(e.clientX, e.clientY)?.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: e.clientX, clientY: e.clientY, view: window }));
  };
  nodes.forEach(n => { n.style.zIndex = 41 + overlays.length * 2; });
  document.body.append(scrim, ...nodes);
  const o = { closed: false, close() {
    if (o.closed) return; o.closed = true;
    overlays.splice(overlays.indexOf(o), 1); overlay = overlays.at(-1) || null;
    scrim.remove(); nodes.forEach(n => n.remove()); onClose?.();
  } };
  overlays.push(o); overlay = o;
  return o;
}
function modal(title, body, buttons) {
  return new Promise(resolve => {
    let done = false, ov;
    const finish = v => { if (!done) { done = true; resolve(v); ov.close(); } };
    const m = h('div', { class: 'modal', role: 'dialog' },
      h('div', { class: 'modal-h' }, title), h('div', { class: 'modal-b' }, body),
      h('div', { class: 'modal-f' }, buttons.map(([label, val, cls]) => btn(label, () => finish(typeof val === 'function' ? val() : val), cls))));
    ov = openOverlay([m], () => finish(null));
    m.addEventListener('keydown', e => { if (e.key === 'Enter' && e.target.tagName !== 'TEXTAREA') { e.preventDefault(); m.querySelector('.btn.primary,.btn.solid')?.click(); } });
    setTimeout(() => (m.querySelector('input,select,textarea') || m.querySelector('.btn.primary,.btn.solid'))?.focus(), 30);
  });
}
const confirmBox = (title, text, ok = 'OK', danger = false) => modal(title, h('div', {}, text), [['Cancel', false, 'ghost'], [ok, true, danger ? 'danger solid' : 'primary']]).then(Boolean);
function drawer(title, sub, body, onClose) {
  const d = h('aside', { class: 'drawer' },
    h('div', { class: 'drawer-h' }, h('div', { class: 'grow' }, h('h2', {}, title), sub && h('div', { class: 'muted' }, sub)), ibtn('x', () => ov.close(), 'Close')),
    h('div', { class: 'drawer-b' }, body));
  const ov = openOverlay([d], onClose);
  d.close = () => ov.close();
  const fit = w => Math.round(Math.max(320, Math.min(innerWidth - 40, w)));
  const saved = store.get('drawer.w', 0);
  if (saved) d.style.width = fit(saved) + 'px';
  const grip = h('div', { class: 'edge-grip', title: 'drag to resize · double-click resets', ondblclick: () => { d.style.width = ''; store.set('drawer.w', 0); } });
  grip.addEventListener('pointerdown', e => {
    e.preventDefault(); grip.setPointerCapture(e.pointerId); document.body.classList.add('resizing', 'cols');
    const move = ev => { d.style.width = fit(innerWidth - ev.clientX) + 'px'; };
    const up = () => {
      grip.removeEventListener('pointermove', move); grip.removeEventListener('pointerup', up);
      document.body.classList.remove('resizing', 'cols'); store.set('drawer.w', Math.round(d.offsetWidth));
      window.dispatchEvent(new Event('resize'));
    };
    grip.addEventListener('pointermove', move); grip.addEventListener('pointerup', up);
  });
  d.prepend(grip);
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
// dataTable sorts by a click on a header, filters with the TUI's grammar, keeps its sort across updates,
// resizes columns (key keeps the widths), moves a cursor (↑↓ enter) and opens menu(row) on a right-click.
function dataTable({ columns, onRow, rowKey, selected, empty = 'Nothing here', rowClass, key, menu, onCursor }) {
  columns = columns.filter(Boolean);
  let rows = [], sort = store.get('sort.' + key, null) || { key: null, dir: 1 }, filter = '', cur = -1;
  const wrap = h('div', { class: 'table-wrap' });
  const firstKey = columns.find(c => c.key)?.key;
  const ck = r => rowKey ? rowKey(r) : r?.[firstKey];
  const draw = () => {
    let list = rows;
    if (filter) { const m = rowMatcher(filter, columns.filter(c => c.key).map(c => c.key)); list = list.filter(m); }
    if (sort.key) {
      const col = columns.find(c => c.key === sort.key), val = col?.sortBy || (r => r[sort.key]);
      list = [...list].sort((a, b) => {
        const x = val(a), y = val(b);
        if (typeof x === 'number' && typeof y === 'number') return (x - y) * sort.dir;
        return String(x ?? '').localeCompare(String(y ?? ''), undefined, { numeric: true }) * sort.dir;
      });
    }
    const keep = t.visible[cur] && ck(t.visible[cur]);
    const head = h('tr', {}, columns.map(c => c.check
      ? h('th', { class: 'chk' }, h('input', { type: 'checkbox', checked: list.length > 0 && list.every(r => selected.has(rowKey(r))), onchange: e => { list.forEach(r => e.target.checked ? selected.add(rowKey(r)) : selected.delete(rowKey(r))); c.onchange?.(); draw(); } }))
      : h('th', { 'data-col': c.key || c.label || '', class: [c.num && 'num', c.key && c.sort !== false && 'sort', c.thCls].filter(Boolean).join(' '), title: c.help, onclick: () => {
        if (!c.key || c.sort === false) return;
        sort = { key: c.key, dir: sort.key === c.key ? -sort.dir : 1 }; if (key) store.set('sort.' + key, sort); draw();
      } }, c.label ?? c.key, sort.key === c.key && h('span', { class: 'arrow' }, sort.dir > 0 ? '↑' : '↓'))));
    const body = list.map((r, i) => {
      const tr = h('tr', { class: [onRow && 'click', selected?.has(rowKey?.(r)) && 'sel', rowClass?.(r)].filter(Boolean).join(' '), onclick: e => {
        if (e.target.closest('button,input,a,select,.col-grip')) return;
        setCur(i); onRow?.(r, e);
      } },
      columns.map(c => c.check
        ? h('td', { class: 'chk' }, h('input', { type: 'checkbox', checked: selected.has(rowKey(r)), onchange: e => { e.target.checked ? selected.add(rowKey(r)) : selected.delete(rowKey(r)); c.onchange?.(); draw(); } }))
        : h('td', { class: [c.num && 'num', c.act && 'act', c.cls].filter(Boolean).join(' '), title: c.render || c.act ? null : fmtCell(r[c.key], c.field) }, c.render ? c.render(r) : fmtCell(r[c.key], c.field))));
      return tr;
    });
    const table = h('table', {}, h('thead', {}, head), h('tbody', {}, body));
    wrap.replaceChildren(table);
    colResize(table, key || 'auto.' + location.hash.replace(/[?].*/, '') + '.' + columns.map(c => c.key || c.label || '').join(','));
    if (!list.length) wrap.append(h('div', { class: 'empty' }, rows.length ? 'No rows match the filter' : empty));
    t.visible = list;
    cur = keep != null ? list.findIndex(r => ck(r) === keep) : -1;
    paint();
  };
  const paint = () => wrap.querySelectorAll('tbody tr').forEach((tr, i) => tr.classList.toggle('cur', i === cur));
  const setCur = i => {
    cur = Math.max(-1, Math.min(t.visible.length - 1, i)); paint();
    wrap.querySelectorAll('tbody tr')[cur]?.scrollIntoView({ block: 'nearest' });
    onCursor?.(t.visible[cur]);
  };
  if (menu) onMenu(wrap, e => {
    const tr = e.target.closest('tbody tr'); if (!tr) return null;
    const i = [...tr.parentNode.children].indexOf(tr); setCur(i);
    return menu(t.visible[i], e);
  });
  const t = { el: wrap, visible: [],
    update(r) { rows = r || []; draw(); }, filter(f) { filter = f; draw(); }, redraw: draw,
    current: () => t.visible[cur], move(d) { setCur(cur < 0 ? (d > 0 ? 0 : t.visible.length - 1) : cur + d); },
    select(pred) { const i = t.visible.findIndex(pred); if (i >= 0) setCur(i); },
    // keys are the walking keys every list shares; it reports whether it took the key
    keys(e) {
      switch (e.key) {
        case 'ArrowDown': case 'j': t.move(1); return true;
        case 'ArrowUp': case 'k': t.move(-1); return true;
        case 'PageDown': t.move(15); return true;
        case 'PageUp': t.move(-15); return true;
        case 'Home': case 'g': setCur(0); return true;
        case 'End': case 'G': setCur(t.visible.length - 1); return true;
        case 'Enter': if (t.current() && onRow) { onRow(t.current(), e); return true; } return false;
        case 'ContextMenu': if (t.current() && menu) { const tr = wrap.querySelectorAll('tbody tr')[cur]; const b = tr.getBoundingClientRect(); ctxMenu({ x: b.left + 40, y: b.bottom }, menu(t.current())); return true; } return false;
      }
      return false;
    } };
  return t;
}
function frameTable(frame, opts = {}) {
  const t = dataTable({ columns: (frame?.fields || []).map(f => ({ key: f.name, field: f, num: ['int', 'number'].includes(f.type) })), empty: 'No rows', ...opts });
  t.update(frameRows(frame));
  return t;
}
const FILTER_HELP = 'words match fuzzy · *glob* · col=value · col!=value · col~text';
// A filter is remembered per screen and placeholder, so leaving a page and coming back (or reloading) keeps it.
function searchBox(placeholder, oninput, { value, title = FILTER_HELP } = {}) {
  const key = 'filter.' + parseRoute().id + '.' + placeholder;
  const remember = v => store.set(key, v || '');
  const saved = value === undefined ? store.get(key, '') : '';
  const input = h('input', { type: 'search', placeholder, value: value ?? saved, title, 'data-filter': '1',
    oninput: debounce(e => { remember(e.target.value); if (!/@\?/.test(e.target.value)) oninput(e.target.value); }, 80),
    onkeydown: e => { if (e.key === 'Escape' && input.value) { e.stopPropagation(); input.value = ''; remember(''); oninput(''); } else if (e.key === 'Escape' || e.key === 'Enter' || e.key === 'ArrowDown') input.blur(); } });
  // The list behind a filter is often built after this call (or after its data arrives): apply again until it exists.
  if (saved) [0, 300, 1200].forEach(ms => setTimeout(() => { try { oninput(saved); } catch { /* list not built yet */ } }, ms));
  const ghost = typeof aiAssist === 'function' ? aiAssist(input, () => 'a filter expression for a table or list on the screen ' + (pageOf(parseRoute().id)?.title || '') + ': ' + FILTER_HELP, { describeOnly: true }) : '';
  return h('div', { class: 'search' }, icon('search'), input, ghost);
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


// ---------- logs ----------
const ANSI_RE = /\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07]*\x07/g;
const LEVEL_RE = /^\W{0,3}(TRACE|DEBUG|DBG|INFO|INF|NOTICE|WARN|WARNING|WRN|ERROR|ERR|FATAL|PANIC|CRIT)\b\W?/i;
const LEVELS = { dbg: 'debug', inf: 'info', wrn: 'warn', warning: 'warn', err: 'error', crit: 'fatal', notice: 'info' };
// parsed is a log row read once: its tree (logTree) and what a line shows of it, per ui.logs.
function parsed(r) {
  if (r._p) return r._p;
  const text = String(r.text ?? '');
  const { tree, structured } = logTree(text);
  const f = S.logFmt ||= logFormat();
  const used = new Set();
  const pick = keys => { for (const k of keys) if (tree[k] != null && typeof tree[k] !== 'object') { used.add(k); return String(tree[k]); } return ''; };
  const time = structured ? pick(f.time) : '';
  let level = structured ? pick(f.level).toLowerCase() : '';
  const msg = structured ? pick(f.message) : text.replace(ANSI_RE, '');
  if (!level) { const m = LEVEL_RE.exec(msg); if (m) level = m[1].toLowerCase(); }
  level = level.replace(/[^a-z]/g, '');
  level = LEVELS[level] || level;
  const keys = [...new Set([...f.fields, ...Object.keys(tree)])].filter(k => k in tree && !used.has(k));
  r._p = { tree, structured, time, level, msg, keys };
  return r._p;
}
const compact = v => typeof v === 'object' && v ? JSON.stringify(v) : String(v);
// logView appends lines as they come, keeps the last max, stays at the bottom unless scrolled up, and
// shows each line structured (LEVEL message key=value, ui.logs' keys) or raw.
function logView(el, { showService = true, max = 5000, filter = () => true } = {}) {
  let lines = [], wrap = S.info?.logs?.Wrap ?? true, raw = !!S.info?.logs?.Raw, cur = null;
  const fmt = S.logFmt ||= logFormat();
  const choice = store.get('logs.fields.' + S.info?.project, {});
  const visible = k => k in choice ? choice[k] : fmt.fields.length ? fmt.fields.includes(k) : !fmt.hide.has(k);
  el.classList.add('logs');
  el.tabIndex = 0;
  el.classList.toggle('nowrap', !wrap);
  const lineEl = r => {
    const p = parsed(r);
    const t = r.time ? toDate(r.time) : null;
    const kv = !raw && p.structured ? p.keys.filter(visible).map(k => {
      const v = compact(p.tree[k]);
      return [' ', h('span', { class: 'kv' + (/^err(or)?$/i.test(k) ? ' bad' : '') }, h('b', {}, k + '='), v.length > 400 ? v.slice(0, 400) + '…' : v)];
    }) : null;
    const row = h('div', { class: 'ln' + (r.stream === 'stderr' && !p.level ? ' err' : '') + (p.level ? ' lv-' + p.level : '') },
      h('span', { class: 't' }, t && !isNaN(t) ? t.toLocaleTimeString([], { hour12: false }) : p.time),
      showService ? h('span', { class: 's', style: { color: colorOf(r.service) }, title: r.instance || r.service }, r.service) : h('span', {}),
      h('span', { class: 'lv ' + p.level }, p.level ? p.level.slice(0, 5).toUpperCase() : ''),
      h('span', { class: 'msg' }, raw ? String(r.text ?? '').replace(ANSI_RE, '') : p.msg, kv));
    row._r = r;
    return row;
  };
  const atBottom = () => el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  const rowsEl = () => [...el.children].filter(x => x._r);
  const select = row => {
    el.querySelector('.ln.cur')?.classList.remove('cur');
    cur = row?._r || null;
    row?.classList.add('cur'); row?.scrollIntoView({ block: 'nearest' });
  };
  const inspect = r => {
    if (!r) return;
    const p = parsed(r);
    const d = drawer(r.service + (p.level ? ' · ' + p.level.toUpperCase() : ''), [r.time && toDate(r.time).toLocaleString(), r.instance].filter(Boolean).join(' · '), [
      jsonViewer(p.tree),
      h('details', {}, h('summary', { class: 'muted' }, 'Raw line'), h('pre', { class: 'out' }, String(r.text ?? '').replace(ANSI_RE, ''))),
      h('div', { class: 'row' }, btn([icon('copy'), 'Copy line'], () => copy(String(r.text ?? '').replace(ANSI_RE, '')), 'sm'), btn([icon('copy'), 'Copy JSON'], () => copy(JSON.stringify(p.tree, null, 2)), 'sm')),
    ]);
    d.classList.add('wide');
  };
  el.addEventListener('click', e => { const row = e.target.closest('.ln'); if (row && !window.getSelection()?.toString()) select(row); });
  el.addEventListener('dblclick', e => { const row = e.target.closest('.ln'); if (row) inspect(row._r); });
  onMenu(el, e => {
    const row = e.target.closest('.ln'); if (!row) return null;
    select(row);
    const r = row._r, p = parsed(r);
    return [
      { label: 'Inspect', icon: 'search', key: 'v', run: () => inspect(r) },
      { label: 'Copy line', icon: 'copy', key: 'y', run: () => copy(String(r.text ?? '').replace(ANSI_RE, '')) },
      p.structured && { label: 'Copy as JSON', icon: 'copy', run: () => copy(JSON.stringify(p.tree, null, 2)) },
      r.service && showService && { label: 'Only ' + r.service, icon: 'logs', run: () => go('logs', { svc: r.service }) },
      r.service && { label: 'Open ' + r.service, icon: 'services', run: () => { go('services'); setTimeout(() => openService(r.service), 60); } },
    ];
  });
  const v = {
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
    refilter() { el.replaceChildren(...lines.filter(filter).map(lineEl)); const c = cur && rowsEl().find(x => x._r === cur); c ? select(c) : (el.scrollTop = el.scrollHeight); },
    clear() { lines = []; cur = null; el.replaceChildren(); },
    wrap(on) { wrap = on ?? !wrap; el.classList.toggle('nowrap', !wrap); return wrap; },
    raw(on) { raw = on ?? !raw; v.refilter(); return raw; },
    fields() { return [...new Set(lines.slice(-500).flatMap(r => parsed(r).keys))].sort(); },
    known: () => choice, visible, setVisible(map) { Object.assign(choice, map); store.set('logs.fields.' + S.info?.project, choice); v.refilter(); },
    follow() { select(null); el.scrollTop = el.scrollHeight; },
    get lines() { return lines; }, get shown() { return lines.filter(filter); },
    keys(e) {
      const rs = rowsEl(), i = cur ? rs.findIndex(x => x._r === cur) : -1;
      switch (e.key) {
        case 'ArrowDown': case 'j': select(rs[Math.min(rs.length - 1, i < 0 ? rs.length - 1 : i + 1)]); return true;
        case 'ArrowUp': case 'k': select(rs[Math.max(0, i < 0 ? rs.length - 1 : i - 1)]); return true;
        case 'PageDown': select(rs[Math.min(rs.length - 1, Math.max(0, i) + 20)]); return true;
        case 'PageUp': select(rs[Math.max(0, (i < 0 ? rs.length : i) - 20)]); return true;
        case 'Enter': case 'v': if (cur) { inspect(cur); return true; } return false;
        case 'G': v.follow(); return true;
        case 'y': if (cur) { copy(String(cur.text ?? '').replace(ANSI_RE, '')); return true; } return false;
      }
      return false;
    },
  };
  return v;
}
// pickFields asks which log fields show; the rest hide.
async function pickFields(lv) {
  const all = [...new Set([...lv.fields(), ...Object.keys(lv.known())])].sort();
  if (!all.length) { toast('No fields in the lines so far'); return; }
  const boxes = all.map(k => ({ k, el: h('input', { type: 'checkbox', checked: lv.visible(k) }) }));
  const ok = await modal('Fields shown', h('div', { class: 'col' }, h('div', { class: 'row' }, btn('All', () => boxes.forEach(b => { b.el.checked = true; }), 'sm ghost'), btn('None', () => boxes.forEach(b => { b.el.checked = false; }), 'sm ghost')),
    h('div', { class: 'field-picks' }, boxes.map(b => h('label', { class: 'row' }, b.el, h('span', { class: 'mono' }, b.k))))), [['Cancel', false, 'ghost'], ['Apply', true, 'primary']]);
  if (ok) lv.setVisible(Object.fromEntries(boxes.map(b => [b.k, b.el.checked])));
}

// ---------- metrics ----------
const RANGES = ['5m', '15m', '30m', '1h', '3h', '6h', '12h', '24h', '2d', '7d'];
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
// lineChart draws series in el: fit takes the height el has, the legend is a table sortable by
// last/max/min/mean (click isolates a series, ctrl+click toggles one), and a drag zooms to a range:
// through onZoom when the page owns the time window, else on the chart itself (double-click resets).
function lineChart(el, list, { unit = '', stack = false, height = 190, fit = false, onZoom, legend: legendKind = 'auto', memo = el } = {}) {
  const hidden = new Set();
  let view = memo._zoom || null;
  const colorAt = x => series()[list.indexOf(x) % series().length];
  let sort = store.get('legend.sort', { key: '', dir: -1 });
  const legend = h('div', {});
  const holder = h('div', { class: 'chart-h' });
  const reset = h('button', { class: 'zoom-reset', hidden: !view, onclick: () => setView(null) }, 'Reset zoom');
  el.replaceChildren(holder, legend);
  el.classList.toggle('chart-fit', fit);
  if (!fit) holder.style.height = height + 'px';
  const inView = x => view ? { ...x, pts: x.pts.filter(p => p[0] >= view[0] && p[0] <= view[1]) } : x;
  const setView = v => { view = v; memo._zoom = v; reset.hidden = !v; draw(); };
  const drawLegend = shown => {
    const stats = list.map(x => {
      const v = inView(x).pts.map(p => p[1]).filter(isFinite);
      return { last: v.at(-1), max: v.length ? Math.max(...v) : NaN, min: v.length ? Math.min(...v) : NaN, mean: v.length ? v.reduce((a, b) => a + b, 0) / v.length : NaN };
    });
    const room = fit ? el.clientHeight : height + 120;
    const asTable = legendKind === 'table' || (legendKind === 'auto' && list.length > 1 && room >= 320);
    legend.hidden = legendKind === 'none' || (list.length <= 1 && legendKind !== 'table');
    legend.className = asTable ? 'legend-t' : 'legend';
    const toggle = (x, e) => {
      if (e.ctrlKey || e.metaKey) hidden.has(x.key) ? hidden.delete(x.key) : hidden.add(x.key);
      else if (hidden.size === list.length - 1 && !hidden.has(x.key)) hidden.clear();
      else { hidden.clear(); list.forEach(y => y.key !== x.key && hidden.add(y.key)); }
      draw();
    };
    if (!asTable) {
      legend.replaceChildren(...list.map(x => h('span', { class: hidden.has(x.key) ? 'off' : '', title: x.key, onclick: e => toggle(x, e) }, h('i', { style: { background: colorAt(x) } }), x.name)));
      return;
    }
    const order = list.map((x, i) => ({ x, st: stats[i] }));
    if (sort.key) order.sort((a, b) => sort.key === 'name' ? a.x.name.localeCompare(b.x.name) * sort.dir : ((a.st[sort.key] ?? -Infinity) - (b.st[sort.key] ?? -Infinity)) * sort.dir);
    const th = (k, l) => h('th', { class: 'sort' + (k !== 'name' ? ' num' : ''), onclick: () => { sort = { key: k, dir: sort.key === k ? -sort.dir : -1 }; store.set('legend.sort', sort); drawLegend(shown); } }, l, sort.key === k && h('span', { class: 'arrow' }, sort.dir > 0 ? '↑' : '↓'));
    legend.replaceChildren(h('table', {}, h('thead', {}, h('tr', {}, th('name', 'Series'), th('last', 'Last'), th('max', 'Max'), th('min', 'Min'), th('mean', 'Mean'))),
      h('tbody', {}, order.map(({ x, st }) => h('tr', { class: 'click' + (hidden.has(x.key) ? ' off' : ''), title: x.key, onclick: e => toggle(x, e) },
        h('td', { class: 'ellipsis' }, h('i', { class: 'sw', style: { background: colorAt(x) } }), x.name),
        ['last', 'max', 'min', 'mean'].map(k => h('td', { class: 'num mono' }, fmtNum(st[k], unit))))))));
  };
  const draw = () => {
    const box = holder.getBoundingClientRect();
    const W = Math.max(Math.round(box.width) || el.clientWidth || 400, 120);
    const H = Math.max(Math.round(box.height) || height, 60), bare = el.classList.contains('spark'), padB = bare ? 2 : 20, padT = 6, padR = bare ? 2 : 10;
    const shown = list.filter(x => !hidden.has(x.key));
    let data = shown.map(inView).map(x => ({ ...x, v: x.pts.map(p => p[1]) }));
    if (stack) { const acc = new Map(); data = data.map(x => ({ ...x, v: x.pts.map(([t, v]) => { const y = (acc.get(t) || 0) + (isNaN(v) ? 0 : v); acc.set(t, y); return y; }) })); }
    const ts = view ? view : [Math.min(...list.flatMap(x => x.pts.map(p => p[0]))), Math.max(...list.flatMap(x => x.pts.map(p => p[0])))];
    const vs = data.flatMap(x => x.v).filter(v => isFinite(v));
    const t0 = ts[0], t1 = ts[1];
    let lo = bare ? Math.min(...vs) : Math.min(0, ...vs), hi = Math.max(...vs);
    if (!vs.length) { lo = 0; hi = 1; }
    if (hi - lo < 1e-12) hi = lo + 1;
    hi += (hi - lo) * 0.08;
    const rowsY = Math.max(2, Math.min(5, Math.floor(H / 45)));
    const yLabels = Array.from({ length: rowsY + 1 }, (_, i) => fmtNum(lo + (hi - lo) * i / rowsY, unit));
    const padL = bare ? 2 : Math.max(W < 260 ? 44 : 56, Math.ceil(Math.max(...yLabels.map(l => l.length)) * 6.6) + 12);
    const X = t => padL + (t1 === t0 ? 0.5 : (t - t0) / (t1 - t0)) * (W - padL - padR);
    const Y = v => padT + (1 - (v - lo) / (hi - lo)) * (H - padT - padB);
    const svg = s('svg', { class: 'chart', viewBox: `0 0 ${W} ${H}`, width: W, height: H, preserveAspectRatio: 'none' });
    const g = s('g', { class: 'grid' });
    for (let i = 0; i <= rowsY; i++) {
      const v = lo + (hi - lo) * i / rowsY, y = Y(v);
      g.append(s('line', { x1: padL, x2: W - padR, y1: y, y2: y }), s('text', { x: padL - 6, y: y + 3.5, 'text-anchor': 'end' }, yLabels[i]));
    }
    const ticks = t1 > t0 ? Math.max(2, Math.min(8, Math.floor(W / 110))) : 0;
    const long = t1 - t0 > 86400e3;
    for (let i = 0; i <= ticks && isFinite(t0); i++) {
      const t = ticks ? t0 + (t1 - t0) * i / ticks : t0, d = new Date(t);
      g.append(s('text', { x: X(t), y: H - 4, 'text-anchor': i === 0 ? 'start' : i === ticks ? 'end' : 'middle' },
        (long ? d.toLocaleDateString([], { month: 'short', day: 'numeric' }) + ' ' : '') + d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: t1 - t0 < 600e3 ? '2-digit' : undefined, hour12: false })));
    }
    if (!bare) svg.append(g);
    data.forEach((x, i) => {
      const c = colorAt(shown[i]);
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
    const band = s('rect', { class: 'zoomband', y: padT, height: H - padT - padB, visibility: 'hidden' });
    svg.append(band, cross);
    const tip = tipEl();
    const tAt = e => { const rect = svg.getBoundingClientRect(), mx = (e.clientX - rect.left) * (W / rect.width); return { mx, t: t0 + (mx - padL) / (W - padL - padR) * (t1 - t0) }; };
    let drag = null;
    svg.addEventListener('pointerdown', e => { if (e.button !== 0 || bare) return; const { mx, t } = tAt(e); if (mx < padL) return; drag = { mx, t }; svg.setPointerCapture(e.pointerId); });
    svg.addEventListener('pointerup', e => {
      if (!drag) return;
      const { mx, t } = tAt(e), d0 = drag; drag = null; band.setAttribute('visibility', 'hidden');
      if (Math.abs(mx - d0.mx) > 6) { const a = Math.min(t, d0.t), b = Math.max(t, d0.t); onZoom ? onZoom(a, b) : setView([a, b]); }
    });
    svg.addEventListener('pointermove', e => {
      const { mx, t } = tAt(e);
      if (drag) { band.setAttribute('x', Math.min(mx, drag.mx)); band.setAttribute('width', Math.abs(mx - drag.mx)); band.setAttribute('visibility', 'visible'); }
      if (mx < padL || !shown.length || !data.some(x => x.pts.length)) return;
      cross.setAttribute('x1', mx); cross.setAttribute('x2', mx); cross.setAttribute('visibility', 'visible');
      const vals = data.map((x, i) => {
        if (!x.pts.length) return null;
        let best = 0; x.pts.forEach((p, j) => { if (Math.abs(p[0] - t) < Math.abs(x.pts[best][0] - t)) best = j; });
        return { name: x.name, v: x.pts[best][1], c: colorAt(shown[i]), t: x.pts[best][0] };
      }).filter(Boolean).sort((a, b) => (b.v ?? -Infinity) - (a.v ?? -Infinity)).slice(0, 14);
      tip.replaceChildren(h('div', { class: 'tt' }, new Date(vals[0]?.t || t).toLocaleString([], { hour12: false })),
        ...vals.map(x => h('div', { class: 'r' }, h('i', { style: { background: x.c } }), h('span', { class: 'ellipsis' }, x.name), h('span', { class: 'v' }, fmtNum(x.v, unit)))));
      tip.hidden = false;
      const tw = tip.offsetWidth, th = tip.offsetHeight;
      tip.style.left = (e.clientX + 16 + tw > innerWidth ? e.clientX - tw - 16 : e.clientX + 16) + 'px';
      tip.style.top = Math.min(e.clientY + 14, innerHeight - th - 8) + 'px';
    });
    svg.addEventListener('pointerleave', () => { cross.setAttribute('visibility', 'hidden'); tip.hidden = true; });
    svg.addEventListener('dblclick', () => view && setView(null));
    holder.replaceChildren(svg, reset);
    drawLegend(shown);
  };
  draw();
  let seen = '';
  new ResizeObserver(() => {
    const r = holder.getBoundingClientRect(), k = Math.round(r.width) + 'x' + Math.round(r.height);
    if (r.width && k !== seen) { seen = k; requestAnimationFrame(draw); }
  }).observe(holder);
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

// ask is a one-field dialog; it resolves to the text, or null when cancelled.
function ask(title, { label = '', value = '', placeholder = '', secret = false, multiline = false, choices } = {}) {
  let input;
  if (choices) {
    input = h('input', { value, placeholder, list: 'ask-choices' });
  } else input = multiline ? h('textarea', { rows: 6, placeholder }, value) : h('input', { value, placeholder, type: secret ? 'password' : 'text', autocomplete: 'off' });
  const body = h('label', { class: 'field' }, label, input, choices && h('datalist', { id: 'ask-choices' }, choices.map(c => h('option', { value: c }))));
  return modal(title, body, [['Cancel', null, 'ghost'], ['OK', () => input.value, 'primary']]);
}
const isRunning = j => !j.end || j.end.startsWith('0001');
// a run that waits for input, or stopped partway through its steps, is pending rather than ended
const partialFail = j => { const m = /failed at step (\d+) of (\d+)/.exec(j.error || ''); return !!m && +m[2] > 1; };
const jobPhase = j => isRunning(j) ? (j.waiting ? 'pending' : 'running') : j.error ? (partialFail(j) ? 'pending' : 'failed') : 'done';
const jobNote = j => j.waiting ? 'waits for input: ' + j.waiting : partialFail(j) ? firstLine(j.error) : '';
const jobIcon = j => ({ running: () => h('span', { class: 'spin' }), pending: () => h('span', { style: { color: 'var(--amber)' }, title: jobNote(j) }, '◔'),
  failed: () => h('span', { style: { color: 'var(--red)' } }, '✕'), done: () => h('span', { style: { color: 'var(--green)' } }, '✓') })[jobPhase(j)]();
const jobBadge = j => ({ running: () => badge('running', 'accent pulse', true), pending: () => badge('pending', 'amber', true), failed: () => badge('failed', 'red', true), done: () => badge('done', 'green', true) })[jobPhase(j)]();
function duration(j) {
  const end = isRunning(j) ? Date.now() : toDate(j.end).getTime(), ms = end - toDate(j.start).getTime();
  return fmtMs(ms);
}
const fmtMs = ms => ms < 1000 ? Math.round(ms) + 'ms' : ms < 60000 ? (ms / 1000).toFixed(1) + 's' : Math.floor(ms / 60000) + 'm ' + Math.round(ms % 60000 / 1000) + 's';
// shortImage keeps an image's last path part, and a long digest-like tag short.
function shortImage(img) {
  if (!img) return '';
  let s = String(img).split('@')[0];
  s = s.slice(s.lastIndexOf('/') + 1);
  const [n, tag] = s.split(':');
  return tag ? n + ':' + (tag.length > 14 ? tag.slice(0, 12) + '…' : tag) : n;
}
const loading = () => h('div', { class: 'loading' }, h('span', { class: 'spin' }), 'Loading…');
const errBox = e => h('div', { class: 'card card-b err' }, e.message || String(e));
// poller runs f every ms while the page is visible, never two at once.
function poller(ctx, ms, f) {
  let busy = false;
  const run = async () => { if (busy || document.hidden) return; busy = true; try { await f(); } finally { busy = false; } };
  ctx.every(ms, run);
  return run;
}
