'use strict';

// ---------- markdown, diff ----------
const esc = t => String(t ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
function inlineMd(t) {
  return esc(t)
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*]+)\*\*/g, '<b>$1</b>')
    .replace(/(^|[\s(])\*([^*\s][^*]*)\*/g, '$1<i>$2</i>')
    .replace(/\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>')
    .replace(/(^|\s)(https?:\/\/[^\s<]+)/g, '$1<a href="$2" target="_blank" rel="noopener">$2</a>');
}
// markdown renders the common subset: headings, lists, quotes, code, tables, paragraphs; text is escaped first.
function markdown(src) {
  const out = [], lines = String(src ?? '').replace(/\r/g, '').split('\n');
  for (let i = 0; i < lines.length;) {
    const l = lines[i];
    if (/^```/.test(l)) {
      const code = []; i++;
      while (i < lines.length && !/^```/.test(lines[i])) code.push(lines[i++]);
      i++; out.push('<pre class="out">' + esc(code.join('\n')) + '</pre>'); continue;
    }
    const hd = /^(#{1,6})\s+(.*)/.exec(l);
    if (hd) { out.push(`<h${Math.min(6, hd[1].length + 2)}>${inlineMd(hd[2])}</h${Math.min(6, hd[1].length + 2)}>`); i++; continue; }
    if (/^\s*([-*+]|\d+\.)\s/.test(l)) {
      const ordered = /^\s*\d+\./.test(l), items = [];
      while (i < lines.length && /^\s*([-*+]|\d+\.)\s/.test(lines[i])) items.push('<li>' + inlineMd(lines[i++].replace(/^\s*([-*+]|\d+\.)\s/, '').replace(/^\[( |x)\]\s/, (_, x) => x === 'x' ? '☑ ' : '☐ ')) + '</li>');
      out.push((ordered ? '<ol>' : '<ul>') + items.join('') + (ordered ? '</ol>' : '</ul>')); continue;
    }
    if (/^>\s?/.test(l)) {
      const q = [];
      while (i < lines.length && /^>\s?/.test(lines[i])) q.push(inlineMd(lines[i++].replace(/^>\s?/, '')));
      out.push('<blockquote>' + q.join('<br>') + '</blockquote>'); continue;
    }
    if (/^\|.*\|\s*$/.test(l) && /^\|[\s:|-]+\|\s*$/.test(lines[i + 1] || '')) {
      const cells = r => r.trim().replace(/^\||\|$/g, '').split('|').map(c => inlineMd(c.trim()));
      const head = cells(l); i += 2;
      const rows = [];
      while (i < lines.length && /^\|/.test(lines[i])) rows.push(cells(lines[i++]));
      out.push('<div class="table-wrap"><table><thead><tr>' + head.map(c => '<th>' + c + '</th>').join('') + '</tr></thead><tbody>' + rows.map(r => '<tr>' + r.map(c => '<td>' + c + '</td>').join('') + '</tr>').join('') + '</tbody></table></div>');
      continue;
    }
    if (/^(-{3,}|\*{3,})$/.test(l.trim())) { out.push('<hr>'); i++; continue; }
    if (!l.trim()) { i++; continue; }
    const p = [];
    while (i < lines.length && lines[i].trim() && !/^(#{1,6}\s|```|>|\s*([-*+]|\d+\.)\s|\|)/.test(lines[i])) p.push(inlineMd(lines[i++]));
    if (!p.length) p.push(inlineMd(lines[i++]));
    out.push('<p>' + p.join('<br>') + '</p>');
  }
  return h('div', { class: 'md', html: out.join('') });
}
function diffView(text) {
  return h('pre', { class: 'out diff' }, String(text ?? '').split('\n').map(l => h('div', {
    class: l.startsWith('+++') || l.startsWith('---') ? 'f' : l.startsWith('+') ? 'a' : l.startsWith('-') ? 'd' : l.startsWith('@@') ? 'hk' : /^(diff|index) /.test(l) ? 'f' : '',
  }, l || ' ')));
}

// ---------- scopes: timers that stop with the part of the page they belong to ----------
function scopeOf(ctx) {
  const s = { fns: [], every(ms, f) { const t = setInterval(() => !document.hidden && f(), ms); s.fns.push(() => clearInterval(t)); }, onCleanup(f) { s.fns.push(f); },
    dispose() { s.fns.splice(0).forEach(f => { try { f(); } catch { /* gone */ } }); }, child() { const c = scopeOf(null); s.fns.push(() => c.dispose()); return c; } };
  ctx?.onCleanup(() => s.dispose());
  return s;
}
const durMs = (v, d) => { const m = /^(\d+(?:\.\d+)?)(ms|s|m|h)?$/.exec(String(v || '')); return m ? +m[1] * ({ ms: 1, s: 1e3, m: 6e4, h: 36e5 }[m[2] || 's']) : d; };

// ---------- placeholders and actions ----------
const fill = (t, row) => String(t).replace(/\$\{row\.([\w.-]+)\}/g, (_, k) => row?.[k] ?? '');
const fillArgs = (args, row) => JSON.parse(JSON.stringify(args || {}), (k, v) => typeof v === 'string' ? fill(v, row) : v);
// resolve asks for ${ask}, ${ask:field} and ${pick:field} before an action runs; null when cancelled.
async function resolveArgs(args, row, rows, name) {
  const out = {};
  for (const [k, v] of Object.entries(args || {})) {
    if (typeof v !== 'string') { out[k] = v; continue; }
    let m;
    if ((m = /^\$\{ask(?::([\w.-]+))?\}$/.exec(v))) {
      const r = await ask(name, { label: k, value: m[1] ? row?.[m[1]] ?? '' : '', multiline: /body|message|text|description/.test(k) && !/branch/.test(k) });
      if (r == null) return null;
      out[k] = r;
    } else if ((m = /^\$\{pick:([\w.-]+)\}$/.exec(v))) {
      const choices = [...new Set((rows || []).map(r => r[m[1]]).filter(x => x != null && x !== ''))].sort();
      const r = await ask(name, { label: k, value: row?.[m[1]] ?? '', choices });
      if (r == null) return null;
      out[k] = r;
    } else out[k] = fill(v, row);
  }
  return out;
}
const usable = a => !a.action || S.provides.actions?.[a.action];
// runAction runs a view's action on a row, or on each marked row when its args name the row.
async function runAction(a, row, rows, marked, refresh) {
  const label = a.name || a.action || a.command || 'AI';
  if (a.ai) { if (!aiOpen) toggleAI(); aiPrefill(fill(a.ai, row)); return; }
  if (a.command) {
    const line = fill(a.command, row);
    if (/\$\{ask\}/.test(line)) { const v = await ask(label, { label: line.replace('${ask}', '…') }); if (v == null) return; return runCommand(line.replace('${ask}', v)); }
    return runCommand(line);
  }
  const perRow = marked?.length > 0 && JSON.stringify(a.args || {}).includes('${row.');
  if (a.danger && !await confirmBox(label, (perRow ? marked.length + ' rows' : row ? Object.values(row).slice(0, 2).join(' ') : '') + ' · ' + a.action, label, true)) return;
  for (const r of perRow ? marked : [row]) {
    const args = await resolveArgs(a.args, r, rows, label);
    if (!args) return;
    await act(a.action, args, label);
  }
  refresh?.();
}

// ---------- transform ----------
function transform(rows, steps) {
  for (const st of steps || []) {
    if (st.filter) {
      const m = /^(\w[\w.-]*)\s*(!=|>=|<=|=|~|>|<)\s*(.*)$/.exec(st.filter);
      rows = rows.filter(r => {
        if (!m) return JSON.stringify(r).toLowerCase().includes(st.filter.toLowerCase());
        const v = r[m[1]], w = m[3];
        switch (m[2]) {
          case '=': return String(v) === w; case '!=': return String(v) !== w; case '~': return String(v ?? '').toLowerCase().includes(w.toLowerCase());
          case '>': return +v > +w; case '<': return +v < +w; case '>=': return +v >= +w; case '<=': return +v <= +w;
        }
        return true;
      });
    }
    if (st.sort) { const desc = st.sort.startsWith('-'), k = st.sort.replace(/^-/, ''); rows = [...rows].sort((a, b) => (typeof a[k] === 'number' ? a[k] - b[k] : String(a[k] ?? '').localeCompare(String(b[k] ?? ''))) * (desc ? -1 : 1)); }
    if (st.limit) rows = rows.slice(0, st.limit);
    if (st.select) rows = rows.map(r => Object.fromEntries(st.select.map(k => [k, r[k]])));
    if (st.rename) rows = rows.map(r => Object.fromEntries(Object.entries(r).map(([k, v]) => [st.rename[k] || k, v])));
  }
  return rows;
}

// ---------- the view renderer ----------
const LAYOUTS_W = new Set(['grid', 'row', 'split', 'tabs', 'stack', 'box', 'scroll']);
const P = (n, k) => n.props?.[k];
const nodeTitle = n => P(n, 'title') || (n.bind?.source ? n.bind.source.split('.').slice(-1)[0] : n.widget);

// pageView renders a view (the project's, a plugin's or the user's) the way the TUI lays it out.
function pageView(ctx, v, inDrawer) {
  const scope = scopeOf(ctx);
  const st = { focus: null, tabs: [] };
  const body = h('div', { class: 'view' });
  if (!inDrawer) ctx.el.append(pageHead(v.title, v.plugin ? 'from the ' + v.plugin + ' plugin' : null, ibtn('refresh', () => route(), 'Refresh (r)')));
  if (v.secret) {
    ctx.el.append(h('div', { class: 'banner amber' }, icon('kv'), h('span', { class: 'grow' }, h('b', {}, v.secret), ' is not set: this screen stays empty until it is.'),
      btn('Set ' + v.secret, async () => {
        const val = await ask('Set ' + v.secret, { label: 'a personal access token; kept in your config directory, like rig secret set', secret: true });
        if (!val) return;
        try { await web('secret', { name: v.secret, value: val }); toast(v.secret + ' saved', 'ok'); S.views = await web('views'); v.secret = ''; route(); } catch (e) { toast(e.message, 'bad', 8000); }
      }, 'primary sm')));
  }
  ctx.el.append(body);
  if (v.error) { body.append(h('div', { class: 'err' }, v.error)); return; }
  const show = root => { body.replaceChildren(renderNode(root, scope, st, true)); };
  if (v.root) show(v.root);
  else if (v.remote) {
    body.append(loading());
    web('views/render', { remote: v.remote }).then(show, e => body.replaceChildren(errBox(e)));
  } else body.append(emptyState('This view is empty'));
  ctx.onKey(e => {
    if ((e.key === ']' || e.key === '[') && st.tabs[0]) { st.tabs[0](e.key === ']' ? 1 : -1); return true; }
    if (e.key === 'r') { st.focus?.refresh?.(); return !!st.focus; }
    const f = st.focus;
    const a = f?.actions?.find(x => x.key === e.key);
    if (a) { runAction(a, f.row(), f.rows(), f.marked(), f.refresh); return true; }
    return f?.key?.(e) || false;
  });
  ctx.menu(() => (st.focus?.actions || []).map(a => ({ key: a.key, label: a.name || a.action || a.command, run: () => runAction(a, st.focus.row(), st.focus.rows(), st.focus.marked(), st.focus.refresh) })));
}

function renderNode(n, scope, st, top) {
  if (!n) return h('div');
  if (LAYOUTS_W.has(n.widget)) return renderLayout(n, scope, st, top);
  return renderWidget(n, scope, st);
}
function sizeStyle(n) {
  const w = P(n, 'width'), hh = P(n, 'height'), span = P(n, 'span');
  const st = {};
  if (w) st.flex = String(w).endsWith('%') ? `0 0 ${w}` : `0 0 ${w * 8}px`;
  else if (span) st.flex = span + ' 1 0';
  if (hh) st.height = String(hh).endsWith('%') ? hh : (hh * 22) + 'px';
  return st;
}
function renderLayout(n, scope, st, top) {
  const kids = n.children || [];
  switch (n.widget) {
    case 'tabs': {
      const key = 'vtab.' + location.hash.split('?')[0] + '.' + (n.id || kids.length);
      let cur = Math.min(store.get(key, 0), kids.length - 1), child;
      const bar = h('div', { class: 'subtabs' }), pane = h('div', { class: 'tabpane' });
      const show = i => {
        cur = (i + kids.length) % kids.length; store.set(key, cur);
        bar.querySelectorAll('button').forEach((b, j) => b.classList.toggle('on', j === cur));
        child?.dispose(); child = scope.child();
        pane.replaceChildren(renderNode(kids[cur], child, st));
      };
      add(bar, [...kids.map((c, i) => h('button', { type: 'button', onclick: () => show(i) }, nodeTitle(c))), h('span', { class: 'spacer' }), top && h('span', { class: 'muted hint' }, '[ ] switch')]);
      st.tabs.push(d => show(cur + d));
      const el = h('div', { class: 'tabs' }, bar, pane);
      show(cur);
      return el;
    }
    case 'grid': return h('div', { class: 'vgrid' }, kids.map(c => c.widget === 'row' ? renderLayout(c, scope, st) : h('div', { class: 'vrow' }, renderNode(c, scope, st))));
    case 'row': case 'split': return h('div', { class: 'vrow' }, kids.map(c => h('div', { class: 'vcell', style: sizeStyle(c) }, renderNode(c, scope, st))));
    case 'stack': return h('div', { class: 'vstack' }, kids.map(c => h('div', { style: sizeStyle(c) }, renderNode(c, scope, st))));
    default: return h('div', { class: 'vbox' }, kids.map(c => renderNode(c, scope, st)));
  }
}

// renderWidget draws a widget in a panel and keeps it fed from its binding.
function renderWidget(n, scope, st) {
  const w = n.widget.split('@')[0];
  const title = P(n, 'title');
  const status = h('span', { class: 'muted small' });
  const tools = h('div', { class: 'row ptools' });
  const content = h('div', { class: 'pbody' });
  const panel = h('div', { class: 'panel vpanel' + (P(n, 'border') === false ? ' bare' : ''), style: sizeStyle(n) },
    (title || w === 'table' || w === 'board' || w === 'terminal') && h('div', { class: 'ph' }, h('span', { class: 'ellipsis' }, title || ''), status, h('span', { class: 'spacer' }), tools), content);
  const ctl = { actions: (P(n, 'actions') || []).filter(usable), rows: () => [], row: () => null, marked: () => [], refresh: () => load() };
  if (!st.focus && (w === 'table' || w === 'board')) st.focus = ctl;
  panel.addEventListener('mousedown', () => { st.focus = ctl; });
  let frame = null, draw = null, ctrl;
  const bind = n.bind;
  async function load() {
    if (!bind?.source) return;
    try {
      const f = await query(bind.source, bind.args || {});
      frame = f;
      status.textContent = '';
      draw?.(f);
    } catch (e) {
      if (frame) status.replaceChildren(h('span', { class: 'err', title: e.message }, '⚠ stale'));
      else content.replaceChildren(h('div', { class: 'err pad' }, e.message));
    }
  }
  const mk = {
    table: () => tableWidget(n, content, tools, ctl, () => load()),
    board: () => boardWidget(n, content, tools, ctl, () => load()),
  }[w];
  if (mk) draw = mk();
  else draw = f => content.replaceChildren(simpleWidget(w, n, f));
  if (w === 'terminal') {
    const box = h('div', { class: 'term-host' });
    // the PTY starts when the panel is clicked, so a view never spawns processes nobody looks at
    const start = () => { box.replaceChildren(); scope.onCleanup(termView(box, { kind: 'command', command: P(n, 'command'), dir: P(n, 'dir'), env: P(n, 'env') }, { onExit: () => box.append(h('div', { class: 'muted small pad' }, 'exited · ', h('a', { href: '#', onclick: e => { e.preventDefault(); start(); } }, 'run again'))) })); };
    box.append(h('button', { class: 'term-start', type: 'button', onclick: start }, icon('term'), h('code', {}, [].concat(P(n, 'command') || '$SHELL').join(' ')), h('span', { class: 'muted' }, 'click to start')));
    content.replaceChildren(box);
    tools.append(ibtn('ext', () => zoom(panel), 'Full screen', 'sm'));
    return panel;
  }
  if (!bind?.source) { draw(null); return panel; }
  if (!mk) content.replaceChildren(loading());
  if (P(n, 'stream')) {
    const rows = [];
    ctrl = new AbortController();
    scope.onCleanup(() => ctrl.abort());
    stream(bind.source, bind.args || {}, b => {
      if (b.frame) { rows.push(...frameRows(b.frame)); if (rows.length > (P(n, 'keep') || 5000)) rows.splice(0, rows.length - (P(n, 'keep') || 5000)); draw({ fields: b.frame.fields, rows }); }
      if (b.lines?.length) { rows.push(...b.lines.map(text => ({ text }))); draw({ fields: [{ name: 'text' }], rows }); }
    }, ctrl.signal).catch(e => { if (e.name !== 'AbortError') content.replaceChildren(h('div', { class: 'err pad' }, e.message)); });
    return panel;
  }
  load();
  if (bind.every !== 'never') scope.every(Math.max(2000, durMs(bind.every, 10000)), load);
  return panel;
}
const rowsOf = (f, n) => transform(f?.rows || frameRows(f), P(n, 'transform'));
function fieldsOf(f, n) {
  const cols = P(n, 'columns'), hide = new Set();
  let order = null;
  if (Array.isArray(cols)) { order = cols.filter(c => !c.hide).map(c => c.name || c); cols.filter(c => c.hide).forEach(c => hide.add(c.name)); }
  else if (cols) for (const [k, c] of Object.entries(cols)) if (c?.hide) hide.add(k);
  let fields = (f?.fields || []).filter(x => !hide.has(x.name));
  if (order?.length) fields = order.map(o => fields.find(x => x.name === o) || { name: o });
  return fields;
}

function tableWidget(n, content, tools, ctl, refresh) {
  const markBy = P(n, 'mark'), preview = P(n, 'preview');
  const marked = new Set();
  let cur = null, rows = [], table = null, filterText = '', pv = null, pvShown = preview && !preview.hidden;
  const left = h('div', { class: 'pleft' }), right = h('div', { class: 'pright', hidden: !pvShown, style: { flex: `0 0 ${preview?.width || '45%'}` } });
  const wrap = h('div', { class: 'psplit' }, left, preview && right);
  const pvScope = scopeOf(null);
  const showPreview = r => {
    if (!preview || !right || right.hidden) return;
    pvScope.dispose();
    pv = { widget: preview.widget || (preview.field === 'markdown' ? 'markdown' : 'text'), props: { title: fill(preview.title || '', r), field: preview.field, border: false }, bind: preview.source ? { source: preview.source, args: fillArgs(preview.args, r), every: 'never' } : null };
    right.replaceChildren(renderWidget(pv, pvScope, {}));
  };
  ctl.rows = () => rows; ctl.row = () => cur; ctl.marked = () => markBy ? rows.filter(r => marked.has(r[markBy])) : [];
  const select = r => { cur = r; table.el.querySelectorAll('tbody tr').forEach((tr, i) => tr.classList.toggle('cur', table.visible[i] === r)); showPreview(r); };
  ctl.key = e => {
    const vis = table?.visible || [], i = vis.indexOf(cur);
    if (e.key === 'ArrowDown' || e.key === 'j') { select(vis[Math.min(vis.length - 1, i + 1)]); return true; }
    if (e.key === 'ArrowUp' || e.key === 'k') { select(vis[Math.max(0, i - 1)]); return true; }
    if (e.key === ' ' && markBy && cur) { marked.has(cur[markBy]) ? marked.delete(cur[markBy]) : marked.add(cur[markBy]); table.redraw(); return true; }
    if (e.key === 'v' && preview) { right.hidden = !right.hidden; if (!right.hidden && cur) showPreview(cur); return true; }
    return false;
  };
  add(tools, [searchBox('Filter', v => { filterText = v; table?.filter(v); }),
    ...ctl.actions.filter(a => !JSON.stringify(a.args || {}).includes('${row.') && !String(a.command || '').includes('${row.')).slice(0, 4)
      .map(a => btn(a.name || a.action, () => runAction(a, cur, rows, ctl.marked(), refresh), 'sm', a.key && 'key ' + a.key)),
    ctl.actions.length > 0 && ibtn('more', () => rowMenu(), 'Actions (.)', 'sm'),
    preview && ibtn('view', () => { right.hidden = !right.hidden; if (!right.hidden && cur) showPreview(cur); }, 'Preview (v)', 'sm')]);
  const rowMenu = (r = cur, at) => {
    const items = ctl.actions.map(a => h('button', { class: 'menu-item', type: 'button', onclick: () => { closeOverlay(); runAction(a, r, rows, ctl.marked(), refresh); } }, h('kbd', {}, a.key || ' '), a.name || a.action || a.command));
    if (!items.length) return;
    const m = h('div', { class: 'popmenu', style: at ? { left: at.x + 'px', top: at.y + 'px' } : { left: '50%', top: '30%' } }, items);
    openOverlay([m]);
    m.querySelector('button')?.focus();
  };
  content.replaceChildren(wrap);
  left.append(loading());
  return f => {
    rows = rowsOf(f, n);
    const fields = fieldsOf(f, n);
    if (!table || table.fieldsKey !== fields.map(x => x.name).join()) {
      table = dataTable({
        columns: [markBy && { check: true }, ...fields.map(fl => ({ key: fl.name, field: fl, num: ['int', 'number'].includes(fl.type), label: fl.name }))].filter(Boolean),
        rowKey: r => r[markBy], selected: marked, empty: 'No rows',
        onRow: r => select(r), rowClass: r => r === cur ? 'cur' : '',
      });
      table.fieldsKey = fields.map(x => x.name).join();
      table.el.addEventListener('contextmenu', e => { const tr = e.target.closest('tbody tr'); if (!tr) return; e.preventDefault(); const r = table.visible[[...tr.parentNode.children].indexOf(tr)]; select(r); rowMenu(r, { x: e.clientX, y: e.clientY }); });
      table.el.addEventListener('dblclick', e => { const tr = e.target.closest('tbody tr'); if (!tr || !ctl.actions[0]) return; const r = table.visible[[...tr.parentNode.children].indexOf(tr)]; const ref = Object.values(r).find(v => /^https?:\/\//.test(String(v))); if (ref) window.open(ref, '_blank', 'noopener'); });
      left.replaceChildren(table.el);
    }
    const keep = cur && markBy ? rows.find(r => r[markBy] === cur[markBy]) : cur ? rows.find(r => JSON.stringify(r) === JSON.stringify(cur)) : null;
    table.update(rows);
    if (filterText) table.filter(filterText);
    if (keep) cur = keep; else if (!cur && rows[0] && preview) select(rows[0]);
    else cur = keep || null;
  };
}

function boardWidget(n, content, tools, ctl, refresh) {
  const by = P(n, 'by') || 'status', cardF = P(n, 'card') || 'title', keyF = P(n, 'key') || 'key', meta = [].concat(P(n, 'meta') || []), preview = P(n, 'preview');
  let rows = [], cur = null, filterText = '';
  const lanes = h('div', { class: 'board' });
  const right = h('div', { class: 'pright', hidden: !preview || preview.hidden, style: { flex: `0 0 ${preview?.width || '40%'}` } });
  const pvScope = scopeOf(null);
  ctl.rows = () => rows; ctl.row = () => cur; ctl.marked = () => [];
  const select = r => {
    cur = r;
    lanes.querySelectorAll('.bcard').forEach(c => c.classList.toggle('cur', c._r === r));
    if (preview && !right.hidden) {
      pvScope.dispose();
      right.replaceChildren(renderWidget({ widget: preview.widget || (preview.field === 'markdown' ? 'markdown' : 'text'), props: { field: preview.field, border: false }, bind: { source: preview.source, args: fillArgs(preview.args, r), every: 'never' } }, pvScope, {}));
    }
  };
  add(tools, [searchBox('Filter cards', v => { filterText = v.toLowerCase(); draw(); }), ctl.actions.length > 0 && ibtn('more', () => menu(), 'Actions', 'sm')]);
  const menu = (r = cur) => {
    const items = ctl.actions.map(a => h('button', { class: 'menu-item', type: 'button', onclick: () => { closeOverlay(); runAction(a, r, rows, [], refresh); } }, h('kbd', {}, a.key || ' '), a.name || a.action || a.command));
    openOverlay([h('div', { class: 'popmenu', style: { left: '50%', top: '30%' } }, items)]);
  };
  const draw = () => {
    const shown = rows.filter(r => !filterText || JSON.stringify(r).toLowerCase().includes(filterText));
    const groups = [...new Set(rows.map(r => r[by] ?? ''))];
    lanes.replaceChildren(...groups.map(g => {
      const list = shown.filter(r => (r[by] ?? '') === g);
      return h('div', { class: 'lane' }, h('div', { class: 'lane-h' }, h('span', { class: 'ellipsis' }, g || '—'), h('span', { class: 'n' }, list.length)),
        h('div', { class: 'lane-b' }, list.map(r => {
          const c = h('div', { class: 'bcard' + (r === cur ? ' cur' : ''), onclick: () => select(r), oncontextmenu: e => { e.preventDefault(); select(r); menu(r); } },
            h('div', { class: 'bt' }, r[keyF] && h('span', { class: 'bk' }, r[keyF]), r[cardF]), meta.length > 0 && h('div', { class: 'bm' }, meta.map(m => r[m] && h('span', {}, r[m]))));
          c._r = r;
          return c;
        })));
    }));
  };
  ctl.key = e => {
    if (e.key === 'v' && preview) { right.hidden = !right.hidden; if (cur) select(cur); return true; }
    return false;
  };
  content.replaceChildren(h('div', { class: 'psplit' }, h('div', { class: 'pleft' }, lanes), preview && right));
  return f => { rows = rowsOf(f, n); if (cur) cur = rows.find(r => r[keyF] === cur[keyF]) || null; draw(); };
}

function simpleWidget(w, n, f) {
  const rows = f ? rowsOf(f, n) : [], fields = f ? fieldsOf(f, n) : [];
  const field = P(n, 'field');
  const textOf = () => P(n, 'text') ?? (field ? rows.map(r => r[field]).join('\n') : rows.map(r => Object.values(r).join('  ')).join('\n'));
  const numOf = () => { const k = P(n, 'value') || fields.find(x => ['int', 'number', 'duration'].includes(x.type))?.name || fields[0]?.name; const r = rows.at(-1); return r ? +r[k] : NaN; };
  switch (w) {
    case 'markdown': return markdown(textOf());
    case 'text': return h('div', { class: 'vtext' }, textOf());
    case 'code': return h('pre', { class: 'out' }, textOf());
    case 'diff': return diffView(textOf());
    case 'list': return h('div', { class: 'vlist' }, rows.map(r => h('div', { class: 'ellipsis' }, Object.values(r).join('  '))));
    case 'logs': {
      const el = h('div', { class: 'logs small' });
      logView(el, {}).push(rows.map(r => ({ text: r.text ?? r.line ?? r.message ?? Object.values(r).join(' '), time: r.time, service: r.service || '' })));
      return el;
    }
    case 'kv': {
      const pairs = fields.length === 2 && rows.length > 1 ? rows.map(r => [r[fields[0].name], r[fields[1].name]]) : Object.entries(rows[0] || {});
      return h('dl', { class: 'kv' }, pairs.map(([k, v]) => [h('dt', {}, k), h('dd', {}, fmtCell(v))]));
    }
    case 'tree': return h('pre', { class: 'out' }, JSON.stringify(rows.length === 1 ? rows[0] : rows, null, 2));
    case 'stat': { const v = numOf(); return h('div', { class: 'bigv' }, fmtNum(v, P(n, 'unit'))); }
    case 'gauge': case 'progress': {
      const v = numOf(), max = P(n, 'max') ?? (P(n, 'unit') === '%' ? 100 : 1);
      return w === 'gauge' ? gauge(v, P(n, 'min') ?? 0, max, 'var(--accent)', P(n, 'unit'))
        : h('div', { class: 'bars' }, h('div', { class: 'b' }, h('span', {}, fmtNum(v, P(n, 'unit'))), h('div', { class: 'track' }, h('div', { class: 'fill', style: { width: Math.min(100, 100 * v / max) + '%', background: 'var(--accent)' } })), h('span')));
    }
    case 'chart': case 'sparkline': case 'heatmap': {
      const fit = !!P(n, 'height');
      const el = h('div', { style: fit ? { height: '100%' } : {} });
      const list = chartSeries(rows, fields, n);
      if (!list.length) return h('div', { class: 'empty' }, 'No data');
      requestAnimationFrame(() => lineChart(el, list, { unit: P(n, 'unit'), height: w === 'sparkline' ? 60 : 240, fit, memo: n, legend: w === 'sparkline' ? 'none' : 'auto' }));
      return el;
    }
    case 'bar': {
      const nameK = P(n, 'series') || fields.find(x => x.type === 'string')?.name || fields[0]?.name, valK = P(n, 'value') || fields.find(x => ['int', 'number'].includes(x.type))?.name;
      const vals = rows.map(r => ({ name: r[nameK], v: +r[valK] })), max = Math.max(1e-9, ...vals.map(x => x.v));
      return h('div', { class: 'bars' }, vals.map((x, i) => h('div', { class: 'b' }, h('span', { class: 'ellipsis' }, x.name), h('div', { class: 'track' }, h('div', { class: 'fill', style: { width: 100 * x.v / max + '%', background: series()[i % series().length] } })), h('span', { class: 'mono' }, fmtNum(x.v, P(n, 'unit'))))));
    }
    case 'form': return formWidget(n);
    default: {
      const t = dataTable({ columns: fields.map(fl => ({ key: fl.name, field: fl, num: ['int', 'number'].includes(fl.type) })), empty: 'No rows' });
      t.update(rows);
      return t.el;
    }
  }
}
// chartSeries reads long form (time, series, value) or wide form (time and numeric columns).
function chartSeries(rows, fields, n) {
  const tk = P(n, 'time') || fields.find(x => x.type === 'time' || x.name === 'time')?.name;
  if (!tk) return [];
  if (fields.some(x => x.name === 'series') && fields.some(x => x.name === 'value')) return groupSeries(rows, P(n, 'legend'));
  return fields.filter(x => x.name !== tk && ['int', 'number', 'duration'].includes(x.type)).map(x => ({ name: x.name, key: x.name, pts: rows.map(r => [toDate(r[tk]).getTime(), +r[x.name]]).sort((a, b) => a[0] - b[0]) }));
}
function formWidget(n) {
  const name = P(n, 'action'), spec = S.provides.actions?.[name];
  const form = schemaForm(P(n, 'schema') || spec?.args);
  return h('div', { class: 'vform' }, form.el, h('div', { class: 'row' }, h('span', { class: 'spacer' }), btn(P(n, 'submit') || 'Run', async () => {
    let args; try { args = form.value(); } catch (e) { return toast(e.message, 'bad'); }
    await act(name, args, name);
  }, 'primary')));
}
