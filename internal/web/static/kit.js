'use strict';

// ---------- filter: the TUI's grammar ----------
// Words match fuzzy (letters in order, gaps allowed), *glob* as a pattern, col=value, col!=value, col~text.
function fuzzy(s, q) {
  s = String(s ?? '').toLowerCase();
  for (const w of String(q).toLowerCase().split(/\s+/).filter(Boolean)) {
    let at = 0;
    for (const c of w) { const i = s.indexOf(c, at); if (i < 0) return false; at = i + 1; }
  }
  return true;
}
const globRe = w => new RegExp('^' + w.split('*').map(x => x.replace(/[.+?^${}()|[\]\\]/g, '\\$&')).join('.*') + '$', 'is');
const cellText = v => v == null ? '' : typeof v === 'object' ? JSON.stringify(v) : String(v);
function findColumn(cols, key) {
  key = key.toLowerCase();
  let at = null;
  for (const c of cols) {
    const l = c.toLowerCase();
    if (l === key) return c;
    if (l.includes(key)) { if (at) return null; at = c; }
  }
  return at;
}
function splitTerms(f) {
  const out = []; let cur = '', q = false;
  for (const c of f) {
    if (c === '"') q = !q;
    if (c === ' ' && !q) { if (cur) out.push(cur); cur = ''; } else cur += c;
  }
  if (cur) out.push(cur);
  return out;
}
// rowMatcher reads a filter over rows (objects); cols are the keys a col= term may name.
function rowMatcher(filter, cols) {
  filter = String(filter || '').trim().replace(/\s*(!=|=|~)\s*/g, '$1');
  if (!filter) return () => true;
  const conds = splitTerms(filter).map(term => {
    const m = /^([\w.\-@]+)(!=|=|~)(.*)$/.exec(term);
    const col = m && findColumn(cols, m[1]);
    if (col) {
      const want = m[3].replace(/^"|"$/g, ''), re = want.includes('*') ? globRe(want) : null;
      const is = got => re ? re.test(got) : got.trim().toLowerCase() === want.toLowerCase();
      return r => { const got = cellText(r[col]); return m[2] === '~' ? got.toLowerCase().includes(want.toLowerCase()) : m[2] === '!=' ? !is(got) : is(got); };
    }
    const word = term.replace(/^"|"$/g, '');
    const hit = word.includes('*') ? (re => c => re.test(c))(globRe(word)) : c => fuzzy(c, word);
    return r => cols.some(c => hit(cellText(r[c])));
  });
  return r => conds.every(c => c(r));
}
// matchText filters plain items (palette entries, list rows): typed text first, then fuzzy.
const matchText = (text, q) => !q || String(text).toLowerCase().includes(q.toLowerCase()) || fuzzy(text, q);

// ---------- resizing ----------
// colResize lets a header's right edge drag its column wider; widths stay per table key.
function colResize(table, key) {
  const saved = store.get('cols.' + key, {});
  const ths = [...table.querySelectorAll('thead th')];
  const set = (th, w) => { th.style.width = th.style.minWidth = th.style.maxWidth = w + 'px'; };
  ths.forEach(th => {
    const id = th.dataset.col;
    if (!id || th.classList.contains('chk')) return;
    if (saved[id]) { set(th, saved[id]); table.classList.add('fixed'); }
    const grip = h('span', { class: 'col-grip', title: 'drag to resize · double-click resets' });
    grip.addEventListener('click', e => e.stopPropagation());
    grip.addEventListener('dblclick', e => { e.stopPropagation(); delete saved[id]; store.set('cols.' + key, saved); th.style.width = th.style.minWidth = th.style.maxWidth = ''; });
    grip.addEventListener('pointerdown', e => {
      e.preventDefault(); e.stopPropagation();
      const x0 = e.clientX, w0 = th.getBoundingClientRect().width;
      grip.setPointerCapture(e.pointerId);
      document.body.classList.add('resizing');
      const move = ev => set(th, Math.max(40, w0 + ev.clientX - x0));
      const up = () => {
        grip.removeEventListener('pointermove', move); document.body.classList.remove('resizing');
        saved[id] = Math.round(th.getBoundingClientRect().width); store.set('cols.' + key, saved);
      };
      grip.addEventListener('pointermove', move);
      grip.addEventListener('pointerup', up, { once: true });
    });
    th.append(grip);
  });
}
// splitPane puts a and b side by side (or stacked) with a divider that drags; the size stays per key.
function splitPane(a, b, { key, vertical = false, size = 320, min = 160 } = {}) {
  const box = h('div', { class: 'splitpane' + (vertical ? ' vertical' : '') });
  let cur = store.get('pane.' + key, size);
  const apply = () => { a.style.flexBasis = cur + 'px'; };
  const grip = h('div', { class: 'pane-grip', title: 'drag to resize · double-click resets', ondblclick: () => { cur = size; apply(); store.set('pane.' + key, cur); } });
  grip.addEventListener('pointerdown', e => {
    e.preventDefault();
    const start = vertical ? e.clientY : e.clientX, s0 = a.getBoundingClientRect()[vertical ? 'height' : 'width'];
    const total = box.getBoundingClientRect()[vertical ? 'height' : 'width'];
    grip.setPointerCapture(e.pointerId);
    document.body.classList.add('resizing', vertical ? 'rows' : 'cols');
    const move = ev => { cur = Math.max(min, Math.min(total - min, s0 + (vertical ? ev.clientY : ev.clientX) - start)); apply(); window.dispatchEvent(new Event('resize')); };
    grip.addEventListener('pointermove', move);
    grip.addEventListener('pointerup', () => { grip.removeEventListener('pointermove', move); document.body.classList.remove('resizing', 'rows', 'cols'); store.set('pane.' + key, Math.round(cur)); }, { once: true });
  });
  a.classList.add('pane-a'); b.classList.add('pane-b');
  apply();
  return add(box, [a, grip, b]);
}
// edgeResize drags el's left edge (a side panel); onSize gets the width as it changes.
function edgeResize(el, key, def, onSize) {
  let w = store.get('pane.' + key, def);
  const set = v => { w = Math.max(280, Math.min(innerWidth - 320, v)); onSize(w); };
  const grip = h('div', { class: 'edge-grip', title: 'drag to resize', ondblclick: () => { set(def); store.set('pane.' + key, w); } });
  grip.addEventListener('pointerdown', e => {
    e.preventDefault(); grip.setPointerCapture(e.pointerId); document.body.classList.add('resizing', 'cols');
    const move = ev => set(innerWidth - ev.clientX);
    grip.addEventListener('pointermove', move);
    grip.addEventListener('pointerup', () => { grip.removeEventListener('pointermove', move); document.body.classList.remove('resizing', 'cols'); store.set('pane.' + key, Math.round(w)); window.dispatchEvent(new Event('resize')); }, { once: true });
  });
  el.prepend(grip);
  set(w);
}

// ---------- context menu ----------
// ctxMenu opens items at the pointer (or under an element): ↑↓ move, enter runs, a letter runs its key.
function ctxMenu(at, items, title) {
  items = items.filter(Boolean);
  if (!items.length) return;
  const rows = [];
  const m = h('div', { class: 'popmenu', role: 'menu', tabindex: '-1' }, title && h('div', { class: 'pop-h' }, title));
  for (const it of items) {
    if (it === '-') { m.append(h('div', { class: 'pop-sep' })); continue; }
    if (it.group) { m.append(h('div', { class: 'pop-h' }, it.group)); continue; }
    const b = h('button', { class: 'menu-item' + (it.danger ? ' danger' : ''), type: 'button', disabled: it.disabled, onclick: () => { closeOverlay(); it.run(); } },
      it.icon ? icon(it.icon) : h('span', { class: 'ico' }), h('span', { class: 'grow ellipsis' }, it.label), it.key && h('kbd', {}, it.key));
    rows.push([b, it]); m.append(b);
  }
  openOverlay([m]);
  const r = m.getBoundingClientRect();
  let x = at.x ?? 0, y = at.y ?? 0;
  if (at instanceof Element) { const b = at.getBoundingClientRect(); x = b.left; y = b.bottom + 4; }
  m.style.left = Math.max(8, Math.min(x, innerWidth - r.width - 8)) + 'px';
  m.style.top = Math.max(8, Math.min(y, innerHeight - r.height - 8)) + 'px';
  let sel = 0;
  const focus = () => rows[sel]?.[0].focus();
  m.addEventListener('keydown', e => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); sel = (sel + (e.key === 'ArrowDown' ? 1 : -1) + rows.length) % rows.length; focus(); return; }
    const hit = rows.find(([, it]) => it.key && it.key === e.key);
    if (hit) { e.preventDefault(); e.stopPropagation(); closeOverlay(); hit[1].run(); }
  });
  focus();
}
// onMenu wires right-click (and the context-menu key) on el to items(target).
function onMenu(el, items) {
  el.addEventListener('contextmenu', e => {
    if (e.shiftKey) return;
    const list = items(e);
    if (!list?.length) return;
    e.preventDefault();
    ctxMenu({ x: e.clientX, y: e.clientY }, list);
  });
}

// ---------- fullscreen ----------
// zoom shows a panel over the whole page (z or esc toggles it back); full uses the browser's.
function zoom(el, onChange) {
  const on = !el.classList.contains('zoomed');
  el.classList.toggle('zoomed', on);
  document.body.classList.toggle('has-zoom', on);
  const back = e => { if (e.key === 'Escape' || (e.key === 'z' && !e.target.closest('input,textarea'))) { e.preventDefault(); e.stopPropagation(); zoom(el, onChange); } };
  if (on) { el._zoomKey = back; document.addEventListener('keydown', back, true); } else document.removeEventListener('keydown', el._zoomKey, true);
  onChange?.(on);
  requestAnimationFrame(() => window.dispatchEvent(new Event('resize')));
}
function browserFull(el = document.documentElement) {
  if (document.fullscreenElement) document.exitFullscreen(); else el.requestFullscreen?.().catch(e => toast(e.message, 'bad'));
}

// ---------- JSON tree ----------
// jsonTree folds objects and arrays, copies a path or a value, and edits in place when onEdit is given.
function jsonTree(value, { onEdit, open = 2, filter = '' } = {}) {
  let root = value;
  const box = h('div', { class: 'jtree' });
  const isObj = v => v && typeof v === 'object';
  const scalar = v => h('span', { class: 'jv ' + (v === null ? 'null' : typeof v) }, v === null ? 'null' : typeof v === 'string' ? JSON.stringify(v) : String(v));
  const setAt = (path, v, del) => {
    if (!path.length) { root = v; return; }
    let o = root; for (const p of path.slice(0, -1)) o = o[p];
    const k = path.at(-1);
    if (del) Array.isArray(o) ? o.splice(k, 1) : delete o[k]; else o[k] = v;
  };
  const parse = t => { try { return JSON.parse(t); } catch { return t; } };
  const commit = () => { onEdit(root); draw(); };
  const menu = (path, v) => [
    { label: 'Copy value', icon: 'copy', key: 'c', run: () => copy(isObj(v) ? JSON.stringify(v, null, 2) : String(v)) },
    { label: 'Copy path', key: 'p', run: () => copy(path.map(p => typeof p === 'number' ? `[${p}]` : (/^\w+$/.test(p) ? '.' + p : `["${p}"]`)).join('').replace(/^\./, '') || '.') },
    onEdit && '-',
    onEdit && { label: 'Edit value', icon: 'edit', key: 'e', run: async () => { const t = await ask('Edit ' + (path.join('.') || 'value'), { value: JSON.stringify(v, null, isObj(v) ? 2 : 0), multiline: isObj(v) }); if (t != null) { setAt(path, parse(t)); commit(); } } },
    onEdit && isObj(v) && { label: Array.isArray(v) ? 'Append item' : 'Add field', icon: 'plus', key: 'a', run: async () => {
      if (Array.isArray(v)) { const t = await ask('Append to ' + (path.join('.') || 'list'), { placeholder: 'JSON or text' }); if (t != null) { v.push(parse(t)); commit(); } return; }
      const k = await ask('Field name'); if (!k) return;
      const t = await ask('Value of ' + k, { placeholder: 'JSON or text' }); if (t != null) { v[k] = parse(t); commit(); }
    } },
    onEdit && path.length && { label: 'Delete', icon: 'trash', key: 'd', danger: true, run: () => { setAt(path, null, true); commit(); } },
  ];
  const hit = (k, v) => !filter || fuzzy(k + ' ' + (isObj(v) ? '' : String(v)), filter);
  const keep = (k, v) => hit(k, v) || (isObj(v) && Object.entries(v).some(([kk, vv]) => keep(kk, vv)));
  const node = (k, v, path, depth) => {
    const key = k != null && h('span', { class: 'jk' }, typeof k === 'number' ? k : JSON.stringify(k).slice(1, -1), ': ');
    const line = (...kids) => {
      const el = h('div', { class: 'jl', tabindex: '-1' }, kids);
      el.addEventListener('contextmenu', e => { e.preventDefault(); e.stopPropagation(); ctxMenu({ x: e.clientX, y: e.clientY }, menu(path, v)); });
      if (onEdit && !isObj(v)) el.addEventListener('dblclick', () => menu(path, v)[3].run());
      return el;
    };
    if (!isObj(v)) return line(key, scalar(v));
    const entries = Array.isArray(v) ? v.map((x, i) => [i, x]) : Object.entries(v);
    const shown = filter ? entries.filter(([kk, vv]) => keep(kk, vv)) : entries;
    const [o, c] = Array.isArray(v) ? ['[', ']'] : ['{', '}'];
    let opened = depth < open || !!filter;
    const kids = h('div', { class: 'jkids' });
    const sum = h('span', { class: 'jsum' }, Array.isArray(v) ? v.length + ' items' : entries.length + ' keys');
    const tog = h('span', { class: 'jtog' }, '▸');
    const fill = () => { kids.replaceChildren(...shown.map(([kk, vv]) => node(kk, vv, [...path, kk], depth + 1))); };
    const setOpen = on => { opened = on; head.classList.toggle('open', on); kids.hidden = !on; if (on && !kids.childElementCount) fill(); };
    const head = line(tog, key, o, sum, h('span', { class: 'jclose' }, c));
    head.addEventListener('click', e => { if (!window.getSelection()?.toString()) { e.stopPropagation(); setOpen(!opened); } });
    head.addEventListener('keydown', e => { if (e.key === 'ArrowRight') setOpen(true); else if (e.key === 'ArrowLeft') setOpen(false); });
    const el = h('div', { class: 'jn' }, head, kids, h('div', { class: 'jl jend' }, c));
    setOpen(opened);
    return el;
  };
  const draw = () => box.replaceChildren(node(null, root, [], 0));
  draw();
  box.filter = f => { filter = f; draw(); };
  box.expandAll = () => { open = 99; draw(); };
  box.collapseAll = () => { open = 1; draw(); };
  box.value = () => root;
  return box;
}
// jsonViewer is a tree with its toolbar: filter, expand, collapse, copy, raw.
function jsonViewer(value, opts = {}) {
  let raw = false;
  const tree = jsonTree(value, opts);
  const pre = h('pre', { class: 'out jraw', hidden: true });
  const body = h('div', { class: 'jview-b' }, tree, pre);
  const bar = h('div', { class: 'toolbar' },
    searchBox('Filter keys and values', v => tree.filter(v)),
    btn('Expand', () => tree.expandAll(), 'sm ghost'), btn('Collapse', () => tree.collapseAll(), 'sm ghost'),
    btn([icon('copy'), 'Copy'], () => copy(JSON.stringify(tree.value(), null, 2)), 'sm ghost'),
    btn('Raw', e => { raw = !raw; pre.textContent = JSON.stringify(tree.value(), null, 2); pre.hidden = !raw; tree.hidden = raw; e.currentTarget.classList.toggle('on', raw); }, 'sm ghost'));
  return h('div', { class: 'jview' }, bar, body);
}

// ---------- logs: the TUI's logTree ----------
const PAIR_KEY = /(?:^|\s)([A-Za-z_][\w.\-]*)=/;
function valueEnd(v) {
  if (!v) return 0;
  if (v[0] === '"') { for (let i = 1; i < v.length; i++) { if (v[i] === '\\') i++; else if (v[i] === '"') return i + 1; } }
  if (v[0] === '{' || v[0] === '[') {
    let d = 0, str = false;
    for (let i = 0; i < v.length; i++) {
      const c = v[i];
      if (str && c === '\\') i++; else if (c === '"') str = !str; else if (!str && (c === '{' || c === '[')) d++; else if (!str && (c === '}' || c === ']') && --d === 0) return i + 1;
    }
  }
  const i = v.indexOf(' ');
  return i < 0 ? v.length : i;
}
function splitPairs(s) {
  const m = PAIR_KEY.exec(s);
  if (!m) return [s, []];
  const head = s.slice(0, m.index); let rest = s.slice(m.index).trimStart();
  const pairs = [];
  while (rest) {
    const eq = rest.indexOf('=');
    const k = eq < 0 ? '' : rest.slice(0, eq);
    if (eq < 0 || !/^[A-Za-z_][\w.\-]*$/.test(k)) { if (pairs.length) pairs.at(-1)[1] += ' ' + rest; break; }
    const v = rest.slice(eq + 1), n = valueEnd(v);
    pairs.push([k, v.slice(0, n)]);
    rest = v.slice(n).trimStart();
  }
  return [head.trim(), pairs];
}
// decodeText is s as JSON when it holds some (quoted, escaped, in backticks), else the string.
function decodeText(s) {
  let t = String(s).trim();
  for (;;) {
    let u = t.replace(/^[`']+|[`']+$/g, '').trim();
    if (u.length >= 2 && u[0] === '"' && u.at(-1) === '"') { try { u = String(JSON.parse(u)).trim(); } catch { /* not a Go string */ } }
    if (u === t) break;
    t = u;
  }
  if (t[0] === '{' || t[0] === '[') {
    for (const c of [t, t.replace(/\\"/g, '"')]) { try { return expandJSON(JSON.parse(c)); } catch { /* next */ } }
  }
  const i = t.search(/[{[]/);
  if (i > 0) { try { const j = JSON.parse(t.slice(i)); if (j && typeof j === 'object') return { text: t.slice(0, i).trim(), value: expandJSON(j) }; } catch { /* plain */ } }
  return t;
}
function expandJSON(v) {
  if (typeof v === 'string') { const d = decodeText(v); return d && typeof d === 'object' ? d : v; }
  if (v && typeof v === 'object') for (const k of Object.keys(v)) v[k] = expandJSON(v[k]);
  return v;
}
// logTree reads a JSON line, a console or logfmt line ("3:58PM INF msg k=v") or text into an object.
function logTree(line) {
  const s = String(line).replace(ANSI_RE, '').trim();
  if (s[0] === '{') { try { const j = JSON.parse(s); if (j && typeof j === 'object') return { tree: expandJSON(j), structured: true }; } catch { /* console line */ } }
  const [head0, pairs] = splitPairs(s);
  const out = {};
  let head = head0;
  const f = head.split(/\s+/);
  if (f.length >= 2 && /^\d/.test(f[0]) && f[0].includes(':')) {
    out.time = f[0];
    const m = LEVEL_RE.exec(f[1]);
    if (m && m[0].length >= f[1].length - 1) { out.level = m[1]; head = f.slice(2).join(' '); } else head = f.slice(1).join(' ');
  }
  else if (LEVEL_RE.test(head)) { const m = LEVEL_RE.exec(head); out.level = m[1]; head = head.slice(m[0].length).trim(); }
  if (!pairs.length && !out.level) return { tree: { text: s }, structured: false };
  if (head) out.message = head;
  for (const [k, v] of pairs) out[k] = decodeText(v);
  return { tree: out, structured: true };
}
// logFormat applies ui.logs: which keys hold the time, level and message, which fields show, which hide.
function logFormat() {
  const ui = S.info?.logs || {};
  const pick = (a, d) => a?.length ? a : d;
  return {
    raw: !!ui.Raw, wrap: ui.Wrap,
    time: pick(ui.Time, ['time', 'ts', 'timestamp', '@timestamp', 'Time']),
    level: pick(ui.Level, ['level', 'lvl', 'severity', 'Level', 'L']),
    message: pick(ui.Message, ['msg', 'message', 'Message', 'M']),
    fields: ui.Fields || [], hide: new Set(ui.Hide || []),
  };
}

// ---------- AI on an input ----------
// aiAssist completes what an input holds after a pause (tab takes it) and writes it from words after @?.
let aiEnabled = null;
async function aiReady() {
  if (aiEnabled == null) aiEnabled = web('ai').then(r => !!r.enabled).catch(() => false);
  return aiEnabled;
}
function aiAssist(input, hint) {
  const ghost = h('div', { class: 'ai-ghost', hidden: true });
  let seq = 0, asked = '';
  const hintOf = () => typeof hint === 'function' ? hint() : hint;
  const ask = async describe => {
    const v = input.value, my = ++seq;
    if (!v.trim() || v === asked || !await aiReady()) return;
    asked = v;
    ghost.hidden = false; ghost.replaceChildren(h('span', { class: 'spin' }), describe ? ' writing it…' : ' thinking…');
    try {
      const r = await web('ai/complete', { hint: hintOf(), text: v });
      if (my !== seq || input.value !== v) return;
      if (describe) { input.value = r.text; input.dispatchEvent(new Event('input')); ghost.hidden = true; return; }
      const rest = r.text.slice(v.length);
      if (!rest.trim()) { ghost.hidden = true; return; }
      ghost.replaceChildren(h('span', { class: 'muted' }, 'tab ↹ '), h('span', { class: 'mono' }, rest));
      ghost._rest = rest;
    } catch (e) { if (my === seq) { ghost.replaceChildren(h('span', { class: 'err' }, e.message)); } }
  };
  const later = debounce(() => ask(false), 900);
  input.addEventListener('input', () => { seq++; ghost.hidden = true; ghost._rest = ''; if (!/@\?/.test(input.value)) later(); });
  input.addEventListener('keydown', e => {
    if (e.key === 'Tab' && ghost._rest && !ghost.hidden) { e.preventDefault(); input.value += ghost._rest; ghost._rest = ''; ghost.hidden = true; input.dispatchEvent(new Event('input')); }
    else if (e.key === 'Enter' && /@\?\s*\S/.test(input.value) && !e.ctrlKey) { e.preventDefault(); e.stopPropagation(); ask(true); }
  }, true);
  aiReady().then(on => { if (on && !input.placeholder.includes('@?')) input.placeholder = (input.placeholder ? input.placeholder + '  ·  ' : '') + '@? describe it, the AI writes it'; });
  return ghost;
}

// ---------- terminal ----------
let xtermLoad;
function loadXterm() {
  xtermLoad ||= new Promise((ok, fail) => {
    document.head.append(h('link', { rel: 'stylesheet', href: 'vendor/xterm.css' }));
    const one = src => new Promise((y, n) => document.head.append(h('script', { src, onload: y, onerror: () => n(new Error('cannot load ' + src)) })));
    one('vendor/xterm.js').then(() => one('vendor/addon-fit.js')).then(ok, fail);
  });
  return xtermLoad;
}
function termTheme() {
  const v = n => getComputedStyle(document.documentElement).getPropertyValue('--' + n).trim();
  return { background: v('bar') || '#181B1F', foreground: v('text') || '#D8D9DA', cursor: v('accent'), selectionBackground: v('cursor') };
}
// termView runs a PTY on the server (req: kind host|service|command and its fields) inside el.
// It returns stop(); the session also ends when the program exits.
function termView(el, req, { onExit } = {}) {
  el.classList.add('termbox');
  el.replaceChildren(loading());
  let id = null, dead = false, term, fit, ro;
  const ctrl = new AbortController();
  const stop = () => {
    if (dead) return; dead = true;
    ctrl.abort(); ro?.disconnect(); term?.dispose();
    if (id) web('term/close', { id }).catch(() => {});
  };
  (async () => {
    try {
      await loadXterm();
      if (dead) return;
      el.replaceChildren();
      term = new window.Terminal({ fontFamily: getComputedStyle(document.body).getPropertyValue('--mono') || 'monospace', fontSize: 13, cursorBlink: true, scrollback: 5000, theme: termTheme(), allowProposedApi: true });
      fit = new window.FitAddon.FitAddon();
      term.loadAddon(fit);
      term.open(el);
      fit.fit();
      ({ id } = await web('term', { ...req, cols: term.cols, rows: term.rows }));
      if (dead) { web('term/close', { id }).catch(() => {}); return; }
      let pending = '', timer = null;
      const flush = () => { timer = null; const d = pending; pending = ''; if (d) web('term/in', { id, data: d }).catch(() => {}); };
      term.onData(d => { pending += d; timer ||= setTimeout(flush, 8); });
      term.onResize(({ cols, rows }) => web('term/resize', { id, cols, rows }).catch(() => {}));
      ro = new ResizeObserver(debounce(() => { try { if (el.clientWidth) fit.fit(); } catch { /* hidden */ } }, 60));
      ro.observe(el);
      term.focus();
      const r = await fetch('/v1/term/out', { method: 'POST', headers: authHeaders(), body: JSON.stringify({ id }), signal: ctrl.signal });
      if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error?.message || r.statusText);
      const rd = r.body.getReader();
      for (;;) {
        const { value, done } = await rd.read();
        if (done) break;
        term.write(value);
      }
      if (!dead) { term.write('\r\n\x1b[2m[process exited]\x1b[0m\r\n'); onExit?.(); }
    } catch (e) {
      if (e.name === 'AbortError' || dead) return;
      el.replaceChildren(errBox(e));
    }
  })();
  return stop;
}
// openTerminal shows a terminal in a wide drawer; closing it ends the session.
function openTerminal(title, sub, req) {
  const box = h('div', { class: 'term-host' });
  let stop = () => {};
  const d = drawer(title, sub, [h('div', { class: 'toolbar' }, h('span', { class: 'muted' }, 'esc twice or ✕ closes · the session ends with it'), h('span', { class: 'spacer' }),
    ibtn('ext', () => zoom(d), 'Full screen')), box], () => stop());
  d.classList.add('wide', 'term-drawer');
  stop = termView(box, req, { onExit: () => {} });
  let lastEsc = 0;
  box.addEventListener('keydown', e => { if (e.key === 'Escape') { const now = Date.now(); if (now - lastEsc < 400) closeOverlay(); lastEsc = now; e.stopPropagation(); } });
}

// ---------- grouped list ----------
// groupedList is a left-hand list under clear group headers: groups fold (accordion: one open at a time),
// a filter matches fuzzy over each item's text, ↑↓ walk it, enter picks, ←→ fold the current group.
function groupedList({ groups, render, text, id = x => x.name, cur, onPick, accordion = false, key, menu, empty = 'Nothing here' }) {
  let filter = '', sel = cur;
  const closed = new Set(store.get('gl.' + key, accordion ? groups.slice(1).map(g => g.id) : []));
  const el = h('div', { class: 'card list glist', tabindex: '0' });
  const groupOf = x => groups.find(g => g.items.includes(x));
  const shown = () => groups.map(g => ({ g, items: g.items.filter(x => matchText(text(x), filter)) })).filter(x => x.items.length);
  const isOpen = g => !!filter || !closed.has(g.id);
  const save = () => key && store.set('gl.' + key, [...closed]);
  const setOpen = (g, on) => {
    if (on) { if (accordion) groups.forEach(o => o !== g && closed.add(o.id)); closed.delete(g.id); } else closed.add(g.id);
    save(); draw();
  };
  const draw = () => {
    const parts = shown();
    el.replaceChildren(...parts.flatMap(({ g, items }) => {
      const open = isOpen(g);
      return [
        h('button', { class: 'glist-h' + (open ? '' : ' closed'), type: 'button', onclick: () => setOpen(g, !open), title: g.sub || '' },
          icon('chev', 'chev'), h('span', { class: 'grow ellipsis' }, g.title || 'Other')),
        open && h('div', { class: 'glist-b' }, items.map(x => {
          const it = h('div', { class: 'item' + (id(x) === sel ? ' sel' : ''), onclick: () => pick(x) }, render(x));
          if (menu) it.addEventListener('contextmenu', e => { e.preventDefault(); sel = id(x); paint(); ctxMenu({ x: e.clientX, y: e.clientY }, menu(x)); });
          it._x = x;
          return it;
        })),
      ].filter(Boolean);
    }));
    if (!parts.length) el.append(h('div', { class: 'empty' }, filter ? 'Nothing matches' : empty));
    el.querySelector('.item.sel')?.scrollIntoView({ block: 'nearest' });
  };
  const paint = () => el.querySelectorAll('.item').forEach(it => it.classList.toggle('sel', id(it._x) === sel));
  const pick = x => {
    sel = id(x);
    const g = groupOf(x);
    if (g && accordion && !filter && closed.size !== groups.length - 1) { groups.forEach(o => o !== g && closed.add(o.id)); closed.delete(g.id); save(); draw(); } else paint();
    onPick(x);
  };
  const flat = () => shown().flatMap(({ g, items }) => isOpen(g) ? items : []);
  const move = d => {
    const list = flat(); if (!list.length) return;
    const i = list.findIndex(x => id(x) === sel);
    const x = list[Math.max(0, Math.min(list.length - 1, i < 0 ? 0 : i + d))];
    sel = id(x); paint(); el.querySelector('.item.sel')?.scrollIntoView({ block: 'nearest' });
    return x;
  };
  draw();
  return {
    el, draw, move,
    filter(f) { filter = f; draw(); },
    set(v) { sel = v; draw(); },
    current: () => flat().find(x => id(x) === sel),
    keys(e) {
      switch (e.key) {
        case 'ArrowDown': case 'j': { const x = move(1); if (x) onPick(x); return true; }
        case 'ArrowUp': case 'k': { const x = move(-1); if (x) onPick(x); return true; }
        case 'ArrowLeft': { const x = flat().find(y => id(y) === sel); const g = x && groupOf(x); if (g) setOpen(g, false); return true; }
        case 'ArrowRight': { const g = groups.find(x => closed.has(x.id)); if (g) setOpen(g, true); return true; }
        case '+': closed.clear(); save(); draw(); return true;
        case '-': groups.forEach(g => closed.add(g.id)); save(); draw(); return true;
      }
      return false;
    },
  };
}
// group buckets items by a key into groupedList's groups, in first-seen order unless sorted.
function group(items, by, { sort = true, title = k => k } = {}) {
  const m = new Map();
  for (const x of items) { const k = by(x) || ''; if (!m.has(k)) m.set(k, []); m.get(k).push(x); }
  const gs = [...m].map(([k, list]) => ({ id: k, title: title(k), items: list }));
  if (sort) gs.sort((a, b) => (a.id === '' ? 1 : 0) - (b.id === '' ? 1 : 0) || a.id.localeCompare(b.id));
  return gs;
}
// runResult shows what an action returned: its message, refs and output.
function runResult(r) {
  if (!r) return '';
  return h('div', { class: 'card card-b col result' }, h('div', { class: 'row' }, badge('done', 'green', true), h('b', {}, r.result?.message || 'done')),
    r.result?.refs?.length > 0 && h('div', { class: 'muted mono small' }, r.result.refs.join(' ')),
    r.job?.output?.length > 0 && h('pre', { class: 'out' }, r.job.output.join('\n')));
}
// inspectRow opens one row as a JSON tree.
function inspectRow(r, title = 'Row') {
  drawer(title, Object.keys(r).length + ' fields', [jsonViewer(Object.fromEntries(Object.entries(r).map(([k, v]) => [k, typeof v === 'string' ? decodeText(v) : v])))]).classList.add('wide');
}
// rowMenu is what a right-click on a result row offers: inspect, copy, filter by a value.
function rowMenu(r, cols, t, e) {
  const td = e?.target?.closest?.('td');
  const col = td ? cols[[...td.parentNode.children].indexOf(td)] : null;
  const v = col != null ? r[col] : null;
  return [
    { label: 'Inspect row', icon: 'search', key: 'v', run: () => inspectRow(r) },
    col != null && { label: 'Copy ' + col, icon: 'copy', key: 'y', run: () => copy(cellText(v)) },
    { label: 'Copy row as JSON', icon: 'copy', key: 'Y', run: () => copy(JSON.stringify(r, null, 2)) },
    col != null && t && { label: `Filter ${col}=${cellText(v).slice(0, 30)}`, icon: 'search', key: 'f', run: () => { const box = t.el.closest('.result-box')?.querySelector('[data-filter]'); const f = `${col}="${cellText(v)}"`; if (box) { box.value = f; box.dispatchEvent(new Event('input')); } else t.filter(f); } },
  ];
}
