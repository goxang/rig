'use strict';

// ---------- logs ----------
// sectionGroups buckets service names by rig.yaml's sections, as the Services screen shows them.
function sectionGroups(names) {
  const sec = n => specOf('service', n)?.Section || S.statusSection?.[n] || '';
  const order = (S.info.sections || []).map(s => s.name);
  return group(names.map(n => ({ name: n })), x => sec(x.name), { sort: false })
    .sort((a, b) => (order.indexOf(a.id) + 1 || 999) - (order.indexOf(b.id) + 1 || 999) || a.id.localeCompare(b.id))
    .map(g => ({ ...g, title: g.id || 'other' }));
}
function pageLogs(ctx) {
  const all = serviceNames();
  const picked = new Set((ctx.q.get('svc') || '').split(',').filter(n => all.includes(n)));
  let text = '', level = store.get('logs.level', ''), paused = false, held = [], ctrl, instance = ctx.q.get('i') || '';
  const levelRank = { trace: 0, debug: 1, info: 2, warn: 3, error: 4, fatal: 5, panic: 5 };
  const box = h('div', { class: 'logs' });
  let match = () => true;
  const setText = v => {
    text = v;
    if (v.length > 2 && v.startsWith('/') && v.endsWith('/')) {
      try { const re = new RegExp(v.slice(1, -1), 'i'); match = r => re.test(String(r.text)); } catch { match = () => true; }
      return;
    }
    // a.b=value reaches into the line's fields; other words match the line or its service
    const terms = splitTerms(v.replace(/\s*(!=|=|~)\s*/g, '$1'));
    const conds = terms.map(t => {
      const m = /^([\w.\-@]+)(!=|=|~)(.*)$/.exec(t);
      if (m) {
        const path = m[1].split('.'), want = m[3].replace(/^"|"$/g, '').toLowerCase();
        return r => {
          let x = parsed(r).tree; for (const p of path) x = x?.[p];
          const got = cellText(x).toLowerCase();
          return m[2] === '~' ? got.includes(want) : m[2] === '!=' ? got !== want : (want.includes('*') ? globRe(want).test(got) : got === want);
        };
      }
      const w = t.toLowerCase();
      return r => String(r.text).toLowerCase().includes(w) || String(r.service).toLowerCase().includes(w);
    });
    match = r => conds.every(c => c(r));
  };
  const lv = logView(box, { filter: r => {
    if (level && (levelRank[parsed(r).level] ?? 2) < levelRank[level]) return false;
    return !text || match(r);
  } });
  const status = h('span', { class: 'muted small' });
  const groups = sectionGroups(all);
  const svcList = h('div', { class: 'svc-pick' });
  const drawPick = (f = '') => svcList.replaceChildren(
    h('button', { class: 'pick-all' + (picked.size === 0 ? ' on' : ''), type: 'button', onclick: () => { picked.clear(); restart(); } }, 'All services', h('span', { class: 'n' }, all.length)),
    ...groups.map(g => {
      const items = g.items.filter(x => matchText(x.name, f));
      if (!items.length) return null;
      const allOn = items.every(x => picked.has(x.name));
      return h('div', { class: 'pick-g' },
        h('button', { class: 'pick-gh', type: 'button', title: 'pick the whole section', onclick: () => { items.forEach(x => allOn ? picked.delete(x.name) : picked.add(x.name)); restart(); } }, g.title, h('span', { class: 'n' }, items.length)),
        items.map(x => h('label', { class: 'pick' + (picked.has(x.name) ? ' on' : ''), style: { '--c': colorOf(x.name) } },
          h('input', { type: 'checkbox', checked: picked.has(x.name), onchange: e => { e.target.checked ? picked.add(x.name) : picked.delete(x.name); restart(); } }),
          h('span', { class: 'dot' }), h('span', { class: 'ellipsis', ondblclick: e => { e.preventDefault(); picked.clear(); picked.add(x.name); restart(); } }, x.name))));
    }));
  const pickFilter = h('input', { type: 'search', placeholder: 'services…', oninput: e => drawPick(e.target.value) });
  const toggle = (label, on, f, title) => { const b = btn(label, () => { b.classList.toggle('on', f()); }, 'sm' + (on ? ' on' : ''), title); return b; };
  const pauseBtn = btn([icon('pause'), 'Pause'], () => togglePause(), 'sm', 'p / space');
  const wrapBtn = toggle([icon('wrap'), 'Wrap'], S.info?.logs?.Wrap ?? true, () => lv.wrap(), 'w');
  const rawBtn = toggle('Raw', !!S.info?.logs?.Raw, () => lv.raw(), 's: structured or raw');
  function togglePause() {
    paused = !paused;
    pauseBtn.replaceChildren(icon(paused ? 'play' : 'pause'), paused ? 'Resume' : 'Pause');
    pauseBtn.classList.toggle('on', paused);
    if (!paused) { lv.push(held); held = []; }
    status.textContent = paused ? 'paused' : 'following';
  }
  function restart() {
    drawPick(pickFilter.value);
    ctrl?.abort();
    ctrl = new AbortController();
    lv.clear(); held = [];
    const q = [...picked].join(',');
    history.replaceState(null, '', '#/logs' + (q ? '?svc=' + encodeURIComponent(q) + (instance ? '&i=' + encodeURIComponent(instance) : '') : ''));
    status.textContent = 'connecting…';
    const c = ctrl;
    stream('services.logs', { services: [...picked], tail: 300, instance: picked.size === 1 ? instance : '' }, b => {
      if (c.signal.aborted) return;
      status.textContent = paused ? 'paused' : 'following';
      const rows = frameRows(b.frame);
      paused ? held.push(...rows) : lv.push(rows);
      if (b.done) status.textContent = 'ended';
    }, c.signal).catch(e => { if (e.name !== 'AbortError') { status.textContent = ''; box.append(h('div', { class: 'ln err' }, h('span'), h('span'), h('span'), h('span', { class: 'msg' }, e.message))); } });
  }
  async function pickInstance() {
    if (picked.size !== 1) { toast('Pick one service first'); return; }
    const n = [...picked][0];
    const r = frameRows(await query('services.status', { services: [n] }))[0];
    const ids = (r?.instances || []).map(i => i.id);
    if (!ids.length) { toast(n + ' has no instances'); return; }
    const v = await ask('Instance of ' + n, { value: instance, placeholder: 'empty: every instance', choices: ids });
    if (v != null) { instance = v; restart(); }
  }
  const levelSel = h('select', { onchange: e => { level = e.target.value; store.set('logs.level', level); lv.refilter(); }, 'aria-label': 'minimum level' },
    [['', 'All levels'], ['debug', 'Debug+'], ['info', 'Info+'], ['warn', 'Warn+'], ['error', 'Errors']].map(([v, l]) => h('option', { value: v, selected: v === level }, l)));
  const refilter = debounce(() => lv.refilter(), 120);
  const logPane = h('div', { class: 'logs-main' },
    h('div', { class: 'toolbar' }, searchBox('Filter: words, /regex/, a.b=value', v => { setText(v); refilter(); }), levelSel, status, h('span', { class: 'spacer' }),
      rawBtn, btn('Fields', () => pickFields(lv), 'sm', 'h: fields shown'), wrapBtn, pauseBtn,
      ibtn('down', () => lv.follow(), 'Follow (G)', 'sm'), ibtn('trash', () => lv.clear(), 'Clear (c)', 'sm'),
      ibtn('copy', () => copy(lv.shown.map(r => `${r.time || ''} ${r.service} ${String(r.text).replace(ANSI_RE, '')}`).join('\n')), 'Copy shown (y)', 'sm'),
      ibtn('ext', () => zoom(ctx.el.querySelector('.logs-layout')), 'Full screen (z)', 'sm')),
    box);
  const side = h('div', { class: 'logs-side' }, pickFilter, svcList);
  ctx.el.append(pageHead('Logs', 'Lines from the services you pick, read into fields: double-click or enter inspects one'),
    h('div', { class: 'logs-layout' }, splitPane(side, logPane, { key: 'logs.side', size: 230, min: 150 })));
  restart();
  ctx.onCleanup(() => ctrl?.abort());
  ctx.menu([
    { key: 'p', label: 'Pause or follow', run: togglePause }, { key: 's', label: 'Structured or raw', run: () => rawBtn.click() },
    { key: 'h', label: 'Fields shown', run: () => pickFields(lv) }, { key: 'w', label: 'Wrap lines', run: () => wrapBtn.click() },
    { key: 'i', label: 'Pick an instance', run: pickInstance }, { key: 'c', label: 'Clear', run: () => lv.clear() },
    { key: 'Y', label: 'Copy every line', run: () => copy(lv.lines.map(r => `${r.time || ''} ${r.service} ${String(r.text).replace(ANSI_RE, '')}`).join('\n')) },
  ]);
  ctx.onKey(e => {
    if (lv.keys(e)) return true;
    switch (e.key) {
      case ' ': case 'p': togglePause(); return true;
      case 'w': wrapBtn.click(); return true;
      case 's': case 'S': rawBtn.click(); return true;
      case 'h': pickFields(lv); return true;
      case 'i': pickInstance(); return true;
      case 'c': lv.clear(); return true;
      case 'f': pickFilter.focus(); return true;
      case 'z': zoom(ctx.el.querySelector('.logs-layout')); return true;
      case 'Y': copy(lv.lines.map(r => String(r.text).replace(ANSI_RE, '')).join('\n')); return true;
    }
    return false;
  });
}

// ---------- metrics ----------
// A time window is a length (range) ending now or at end; drag on a chart zooms, Z zooms out.
function pageMetrics(ctx) {
  const dashes = resources('dashboard').map(r => r.Name).sort();
  let cur = ctx.rest[0] && (dashes.includes(ctx.rest[0]) || ctx.rest[0] === '_query') ? ctx.rest[0] : (store.get('metrics.dash') && dashes.includes(store.get('metrics.dash')) ? store.get('metrics.dash') : dashes[0] || '_query');
  let range = store.get('metrics.range', '15m'), end = null, every = store.get('metrics.every', 30), timer, vars = {}, gen = 0;
  const zooms = [];
  const body = h('div', { class: 'dash-body' });
  const tabs = h('div', { class: 'dash-tabs' });
  const varBar = h('div', { class: 'row wrap' });
  const winLabel = h('span', { class: 'muted small mono' });
  const ms = r => { const m = /^(\d+(?:\.\d+)?)(ms|s|m|h|d)$/.exec(r); return m ? +m[1] * { ms: 1, s: 1e3, m: 6e4, h: 36e5, d: 864e5 }[m[2]] : 9e5; };
  const showWin = () => {
    rangeSel.value = RANGES.includes(range) && !end ? range : 'custom';
    const to = end ? new Date(end) : null;
    winLabel.textContent = end ? `${new Date(to - ms(range)).toLocaleString([], { hour12: false })} → ${to.toLocaleString([], { hour12: false })}` : '';
    zoomOut.hidden = !zooms.length && !end;
  };
  const setWin = (r, e, push = true) => { if (push) zooms.push([range, end]); range = r; end = e; if (!e) store.set('metrics.range', r); showWin(); refresh(); };
  const onZoom = (t0, t1) => setWin(Math.max(10, Math.round((t1 - t0) / 1000)) + 's', new Date(t1).toISOString());
  const unzoom = () => { const z = zooms.pop(); if (z) setWin(z[0], z[1], false); else setWin(range, null, false); };
  const shift = d => { const len = ms(range), to = (end ? new Date(end).getTime() : Date.now()) + d * len / 2; setWin(range, to >= Date.now() ? null : new Date(to).toISOString()); };
  const drawTabs = () => tabs.replaceChildren(
    ...(dashes.length > 7
      ? [h('select', { 'aria-label': 'dashboard', onchange: e => go('metrics/' + e.target.value) }, dashes.map(d => h('option', { value: d, selected: d === cur }, d)), h('option', { value: '_query', selected: cur === '_query' }, 'Ad-hoc query'))]
      : [h('div', { class: 'subtabs' }, dashes.map(d => h('button', { class: d === cur ? 'on' : '', type: 'button', onclick: () => go('metrics/' + d) }, d)),
        h('button', { class: cur === '_query' ? 'on' : '', type: 'button', onclick: () => go('metrics/_query') }, 'Ad-hoc query'))]));
  const charts = new Set();
  const resize = debounce(() => charts.forEach(c => c.isConnected ? c._draw?.() : charts.delete(c)), 100);
  window.addEventListener('resize', resize);
  const ro = new ResizeObserver(resize);
  ro.observe(body);
  // panels load when they scroll into view, at most four at once; a new screen or window drops the rest
  const queue = [];
  let running = 0;
  const pump = () => { while (running < 4 && queue.length) { const f = queue.shift(); running++; f().finally(() => { running--; pump(); }); } };
  const io = new IntersectionObserver(es => es.forEach(e => { if (e.isIntersecting && e.target._due) { e.target._due = false; queue.push(e.target._load); pump(); } }), { rootMargin: '200px' });
  let ctrl = new AbortController();
  ctx.onCleanup(() => { window.removeEventListener('resize', resize); ro.disconnect(); io.disconnect(); ctrl.abort(); clearInterval(timer); const t = document.querySelector('.tip'); if (t) t.hidden = true; });
  const setTimer = () => { clearInterval(timer); if (every) timer = setInterval(() => !document.hidden && !end && refresh(), every * 1000); };
  async function loadVars(d) {
    varBar.replaceChildren();
    vars = {};
    const order = d.VarOrder?.length ? d.VarOrder : Object.keys(d.Vars || {});
    await Promise.all(order.map(async name => {
      const v = d.Vars[name];
      let values = v.Values || [];
      if (!values.length && v.Query) {
        try {
          const rows = frameRows(await query('metrics.query', { query: v.Query, source: v.Source || d.Source || '', instant: true }, ctrl.signal));
          values = [...new Set(rows.map(r => Object.fromEntries(String(r.series).split(',').map(p => p.split('=')))[v.Label]).filter(Boolean))].sort();
        } catch { /* leave it empty */ }
      }
      const def = v.Default && (!values.length || values.includes(v.Default)) ? v.Default : v.All ? '$__all' : values[0];
      vars[name] = def ? [def] : [];
      v._values = values; v._def = def;
    }));
    for (const name of order) {
      const v = d.Vars[name];
      varBar.append(h('label', { class: 'var' }, h('span', { class: 'muted' }, '$' + name),
        h('select', { onchange: e => { vars[name] = [e.target.value]; refresh(); } },
          v.All && h('option', { value: '$__all', selected: v._def === '$__all' }, 'All'), v._values.map(x => h('option', { value: x, selected: x === v._def }, x)))));
    }
  }
  const panelHeight = p => p.Height ? Math.max(4, p.Height) * 30 : ['stat', 'gauge'].includes(p.Kind) ? 150 : 300;
  function panelEl(p, d) {
    const kind = p.Kind || 'line';
    const width = p.Width || (kind === 'stat' || kind === 'gauge' ? 6 : 12);
    const bodyEl = h('div', { class: 'body' }, h('div', { class: 'pmsg' }, h('span', { class: 'spin' })));
    const targets = p.Queries?.length ? p.Queries : p.Query ? [{ Query: p.Query, Legend: p.Legend }] : [];
    const el = h('div', { class: 'panel kind-' + kind, tabindex: '0', style: { gridColumn: `span ${Math.min(24, width)}`, height: panelHeight(p) + 'px' } },
      h('div', { class: 'ph' }, h('span', { class: 'ellipsis grow', title: p.Title }, p.Title || firstLine(p.Query)), p.Help && h('span', { class: 'help', title: p.Help }, 'ⓘ'),
        ibtn('copy', () => copy(targets.map(t => t.Query).join('\n')), 'Copy query (y)', 'sm ph-btn'), ibtn('ext', () => view(), 'View full screen (v)', 'sm ph-btn')), bodyEl);
    const view = () => zoom(el, on => { el.style.height = on ? '' : panelHeight(p) + 'px'; });
    el._view = view;
    onMenu(el, () => [
      { label: el.classList.contains('zoomed') ? 'Back to the dashboard' : 'View full screen', icon: 'ext', key: 'v', run: view },
      { label: 'Refresh', icon: 'refresh', key: 'r', run: () => { queue.push(el._load); pump(); } },
      { label: 'Copy query', icon: 'copy', key: 'y', run: () => copy(targets.map(t => t.Query).join('\n')) },
      { label: 'Open as ad-hoc query', icon: 'queries', key: 'e', run: () => { store.set('metrics.adhoc', targets[0]?.Query || ''); go('metrics/_query'); } },
    ]);
    el.addEventListener('dblclick', e => { if (e.target.closest('.ph')) view(); });
    const my = gen;
    el._load = async () => {
      if (my !== gen) return;
      try {
        const args = t => ({ query: t.Query, source: p.Source || d?.Source || '', range, end: end || undefined, vars, instant: kind !== 'line' && kind !== 'stat' });
        const lists = await Promise.all(targets.map(async t => groupSeries(frameRows(await query('metrics.query', args(t), ctrl.signal)), t.Legend)));
        if (my !== gen) return;
        const list = lists.flat();
        const last = x => x.pts.at(-1)?.[1];
        const colorFor = v => p.Crit != null && v >= p.Crit ? 'var(--red)' : p.Warn != null && v >= p.Warn ? 'var(--amber)' : 'var(--green)';
        if (!list.length) { bodyEl.replaceChildren(h('div', { class: 'pmsg' }, 'No data')); return; }
        if (kind === 'stat') {
          const v = last(list[0]);
          const spark = h('div', { class: 'spark' });
          bodyEl.replaceChildren(h('div', { class: 'bigv', style: { color: p.Warn != null || p.Crit != null ? colorFor(v) : 'var(--text)' } }, fmtNum(v, p.Unit)), spark);
          const c = lineChart(spark, [list[0]], { unit: p.Unit, fit: true, legend: 'none' });
          spark.querySelectorAll('.grid').forEach(g => g.remove());
          spark._draw = () => { c.draw(); spark.querySelectorAll('.grid').forEach(g => g.remove()); };
          charts.add(spark);
        } else if (kind === 'gauge') {
          const v = last(list[0]);
          const max = p.Max ?? (p.Unit === '%' ? 100 : Math.max(...list.map(last), 1));
          bodyEl.replaceChildren(h('div', { class: 'gauge-box' }, gauge(v, p.Min ?? 0, max, colorFor(v), p.Unit)));
        } else if (kind === 'bar' || kind === 'table') {
          const vals = list.map(x => ({ name: x.name, v: last(x) })).sort((a, b) => b.v - a.v);
          const max = Math.max(...vals.map(x => x.v), 1e-9);
          bodyEl.replaceChildren(kind === 'bar'
            ? h('div', { class: 'bars' }, vals.map((x, i) => h('div', { class: 'b' }, h('span', { class: 'ellipsis', title: x.name }, x.name),
              h('div', { class: 'track' }, h('div', { class: 'fill', style: { width: (100 * x.v / max) + '%', background: p.Warn != null || p.Crit != null ? colorFor(x.v) : series()[i % series().length] } })), h('span', { class: 'mono' }, fmtNum(x.v, p.Unit)))))
            : (() => { const t = dataTable({ key: 'mt', columns: [{ key: 'name', label: 'Series' }, { key: 'v', label: 'Value', num: true, render: r => fmtNum(r.v, p.Unit) }] }); t.update(vals); return t.el; })());
        } else {
          const c = lineChart(bodyEl, list, { unit: p.Unit, stack: p.Stack, fit: true, onZoom });
          bodyEl._draw = c.draw;
          charts.add(bodyEl);
        }
      } catch (e) { if (e.name !== 'AbortError') bodyEl.replaceChildren(h('div', { class: 'pmsg err' }, e.message)); }
    };
    return el;
  }
  let panels = [];
  async function show() {
    gen++; ctrl.abort(); ctrl = new AbortController(); queue.length = 0;
    store.set('metrics.dash', cur);
    drawTabs();
    charts.clear();
    if (cur === '_query') return showQuery();
    const d = specOf('dashboard', cur) || {};
    body.replaceChildren(loading());
    await loadVars(d);
    const grid = h('div', { class: 'dash' });
    panels = [];
    let rowPanels = null;
    const closedRows = new Set(store.get('metrics.rows.' + cur, []));
    for (const p of d.Panels || []) {
      if (p.Row && !p.Title && !p.Query && !p.Queries?.length) {
        const members = [], name = p.Row;
        const hd = h('div', { class: 'rowh' + (closedRows.has(name) ? ' closed' : ''), onclick: () => {
          const shut = !hd.classList.toggle('closed') ? false : true;
          shut ? closedRows.add(name) : closedRows.delete(name); store.set('metrics.rows.' + cur, [...closedRows]);
          members.forEach(m => { m.hidden = shut; });
        } }, icon('chev', 'chev'), name, h('span', { class: 'muted small' }, ''));
        grid.append(hd);
        rowPanels = members;
        continue;
      }
      const el = panelEl(p, d);
      if (rowPanels) { rowPanels.push(el); el.hidden = grid.lastElementChild && [...grid.querySelectorAll('.rowh')].at(-1)?.classList.contains('closed'); }
      grid.append(el);
      panels.push(el);
    }
    body.replaceChildren(d.Help ? h('p', { class: 'muted', style: { marginTop: 0 } }, d.Help) : '', grid);
    if (!panels.length) body.replaceChildren(emptyState('This dashboard has no panels'));
    refresh();
  }
  function refresh() {
    gen++;
    ctrl.abort(); ctrl = new AbortController(); queue.length = 0;
    const my = gen;
    for (const p of panels) {
      const load = p._load;
      p._load = () => my === gen ? load() : Promise.resolve();
      p._due = true;
      io.unobserve(p); io.observe(p);
    }
  }
  function showQuery() {
    varBar.replaceChildren();
    const comps = resources('component').filter(r => (r.Spec?.Kind || '') === 'metrics').map(r => r.Name);
    const src = h('select', {}, h('option', { value: '' }, 'default metrics'), comps.map(c => h('option', { value: c }, c)));
    const ta = h('textarea', { rows: 3, class: 'mono', spellcheck: false, placeholder: 'PromQL, such as sum by (service) (rate(http_requests_total[1m]))  ·  ctrl+enter runs' }, store.get('metrics.adhoc', ''));
    const ghost = aiAssist(ta, () => 'a PromQL query for Prometheus; the dashboards use: ' + resources('dashboard').flatMap(r => (r.Spec?.Panels || []).map(p => p.Query).filter(Boolean)).slice(0, 8).join(' ; '));
    const out = h('div', { class: 'panel', style: { height: '420px' } }, h('div', { class: 'pmsg' }, 'Run a query to chart it'));
    const runQ = async () => {
      store.set('metrics.adhoc', ta.value);
      out.replaceChildren(h('div', { class: 'pmsg' }, h('span', { class: 'spin' })));
      try {
        const list = groupSeries(frameRows(await query('metrics.query', { query: ta.value, source: src.value, range, end: end || undefined }, ctrl.signal)), '');
        if (!list.length) { out.replaceChildren(h('div', { class: 'pmsg' }, 'No data')); return; }
        const b = h('div', { class: 'body' });
        out.replaceChildren(h('div', { class: 'ph' }, h('span', { class: 'grow ellipsis mono' }, ta.value), ibtn('ext', () => zoom(out), 'Full screen', 'sm ph-btn')), b);
        const c = lineChart(b, list, { fit: true, onZoom });
        b._draw = c.draw; charts.add(b);
      } catch (e) { if (e.name !== 'AbortError') out.replaceChildren(h('div', { class: 'pmsg err' }, e.message)); }
    };
    ta.addEventListener('keydown', e => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); runQ(); } });
    panels = [];
    refreshQuery = () => ta.value && runQ();
    body.replaceChildren(h('div', { class: 'card card-b col', style: { marginBottom: '14px' } }, ta, ghost,
      h('div', { class: 'row' }, src, h('span', { class: 'spacer' }), h('span', { class: 'muted small' }, 'ctrl+enter runs · tab takes the AI\'s completion · @? describes it'), btn([icon('play'), 'Run query'], runQ, 'primary'))), out);
    if (ta.value) runQ();
  }
  let refreshQuery = null;
  const rangeSel = h('select', { 'aria-label': 'time range', onchange: async e => {
    if (e.target.value !== 'custom') { zooms.length = 0; setWin(e.target.value, null, false); return; }
    const pad = n => String(n).padStart(2, '0'), loc = d => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
    const to = end ? new Date(end) : new Date(), from = new Date(to - ms(range));
    const fi = h('input', { type: 'datetime-local', value: loc(from) }), ti = h('input', { type: 'datetime-local', value: loc(to) });
    const ok = await modal('Time range', h('div', { class: 'row wrap' }, h('label', { class: 'field' }, 'From', fi), h('label', { class: 'field' }, 'To', ti)), [['Cancel', false, 'ghost'], ['Apply', true, 'primary']]);
    if (!ok) { showWin(); return; }
    const a = new Date(fi.value), b = new Date(ti.value);
    if (!(b > a)) { toast('From must come before To', 'bad'); showWin(); return; }
    setWin(Math.round((b - a) / 1000) + 's', b >= Date.now() - 5000 ? null : b.toISOString());
  } }, RANGES.map(r => h('option', { value: r, selected: r === range }, 'Last ' + r)), h('option', { value: 'custom' }, 'Custom…'));
  const zoomOut = btn('Zoom out', unzoom, 'sm', 'Z');
  const everySel = h('select', { onchange: e => { every = +e.target.value; store.set('metrics.every', every); setTimer(); }, 'aria-label': 'refresh' },
    [[0, 'Off'], [10, '10s'], [30, '30s'], [60, '1m'], [300, '5m']].map(([v, l]) => h('option', { value: v, selected: v === every }, '⟳ ' + l)));
  const doRefresh = () => cur === '_query' ? refreshQuery?.() : refresh();
  ctx.el.append(pageHead('Metrics', null, winLabel, ibtn('back', () => shift(-1), 'Earlier (,)', 'sm'), rangeSel, ibtn('right', () => shift(1), 'Later (.)', 'sm'), zoomOut, everySel,
    ibtn('refresh', doRefresh, 'Refresh (R)'), ibtn('ext', () => browserFull(), 'Full screen')),
  h('div', { class: 'toolbar dash-bar' }, tabs, h('span', { class: 'spacer' }), varBar), body);
  showWin();
  if (!dashes.length && !resources('component').length) { body.append(emptyState('No dashboards', 'Add dashboards: to rig.yaml, or run a query')); }
  show();
  setTimer();
  const focused = () => document.activeElement?.closest?.('.panel') || panels.find(p => p.matches(':hover'));
  ctx.onKey(e => {
    const all = [...dashes, '_query'], i = all.indexOf(cur);
    switch (e.key) {
      case 'r': case 'R': doRefresh(); return true;
      case ']': go('metrics/' + all[(i + 1) % all.length]); return true;
      case '[': go('metrics/' + all[(i - 1 + all.length) % all.length]); return true;
      case 'Z': unzoom(); return true;
      case ',': shift(-1); return true;
      case '.': shift(1); return true;
      case 't': rangeSel.focus(); rangeSel.showPicker?.(); return true;
      case 'v': case 'z': case 'Enter': { const p = focused(); if (p?._view) { p._view(); return true; } return false; }
      case 'ArrowRight': case 'ArrowDown': case 'ArrowLeft': case 'ArrowUp': {
        const vis = panels.filter(p => !p.hidden); if (!vis.length) return false;
        const at = vis.indexOf(focused());
        vis[Math.max(0, Math.min(vis.length - 1, at + (e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : -1)))].focus();
        return true;
      }
      case '+': panels.forEach(p => { p.hidden = false; }); document.querySelectorAll('.rowh').forEach(r => r.classList.remove('closed')); return true;
      case '-': document.querySelectorAll('.rowh:not(.closed)').forEach(r => r.click()); return true;
    }
    return false;
  });
  ctx.subTab = d => { const all = [...dashes, '_query'], i = all.indexOf(cur); go('metrics/' + all[(i + d + all.length) % all.length]); };
}

// ---------- tasks ----------
function taskList() {
  return Object.entries(S.provides.actions || {}).filter(([n]) => n.startsWith('task.')).map(([n, a]) => {
    const name = n.slice(5), sp = specOf('task', name) || {};
    return { name, action: n, help: a.description || sp.Help || '', readonly: a.effect === 'read', spec: sp, group: sp.Group || taskGroup(name) };
  }).sort((a, b) => a.group.localeCompare(b.group) || a.name.localeCompare(b.name));
}
// taskGroup is the category a task without group: belongs to: its name's prefix (db:seed, test-unit).
const taskGroup = n => { const m = /^([a-z0-9]+)[:/._-]/i.exec(n); return m ? m[1] : ''; };
async function runTask(t) {
  const sp = t.spec;
  const fields = (sp.Args || []).map(a => {
    const input = a.Choices?.length
      ? h('select', {}, a.Choices.map(c => h('option', { value: c, selected: c === a.Default }, c)))
      : h('input', { value: a.Default || '', placeholder: a.Help || a.Name });
    return { a, input, el: h('label', { class: 'field' }, h('span', {}, h('b', {}, a.Name), a.Help && h('span', { class: 'muted' }, ' — ' + a.Help)), input) };
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
function pageTasks(ctx) {
  const tasks = taskList();
  let cur = ctx.rest[0] || store.get('tasks.cur', tasks[0]?.name);
  const groups = group(tasks, t => t.group, { title: k => k ? k[0].toUpperCase() + k.slice(1) : 'General' });
  const detail = h('div', { class: 'grow' });
  const list = groupedList({ groups, key: 'tasks', cur, text: t => t.name + ' ' + t.help + ' ' + t.group, id: t => t.name,
    render: t => [h('div', { class: 'n' }, h('span', { class: 'ellipsis' }, t.name), t.readonly && badge('read', 'accent'), t.spec.Confirm && badge('confirm', 'amber')), h('div', { class: 'd' }, firstLine(t.help) || 'no description')],
    onPick: t => { cur = t.name; store.set('tasks.cur', cur); drawDetail(); },
    menu: t => [{ label: 'Run', icon: 'play', key: 'enter', run: () => runTask(t) }, { label: 'Copy the command', icon: 'copy', run: () => copy('rig do ' + t.name) }],
    empty: 'No tasks: add tasks: to rig.yaml' });
  function drawDetail() {
    const t = tasks.find(x => x.name === cur);
    history.replaceState(null, '', '#/tasks' + (t ? '/' + encodeURIComponent(t.name) : ''));
    if (!t) { detail.replaceChildren(emptyState(tasks.length ? 'Pick a task' : 'No tasks', tasks.length ? '' : 'Add tasks: to rig.yaml')); return; }
    const sp = t.spec;
    detail.replaceChildren(h('div', { class: 'card' },
      h('div', { class: 'card-h' }, h('span', { class: 'grow' }, h('span', { class: 'muted small' }, (t.group || 'general') + ' / '), t.name), t.readonly && badge('read-only', 'accent'), sp.Confirm && badge('asks first', 'amber'),
        btn([icon('play'), 'Run'], () => runTask(t), sp.Confirm ? 'danger solid' : 'primary')),
      h('div', { class: 'card-b col' },
        t.help ? h('div', { style: { whiteSpace: 'pre-wrap' } }, t.help) : h('div', { class: 'muted' }, 'No description'),
        (sp.Args || []).length > 0 && h('div', {}, h('h4', {}, 'Arguments'), h('table', { class: 'plain' }, h('tbody', {}, sp.Args.map(a => h('tr', {}, h('td', { class: 'mono' }, a.Name), h('td', { class: 'muted' }, a.Help || ''), h('td', { class: 'mono muted' }, a.Default ? 'default ' + a.Default : a.Choices?.length ? a.Choices.join(' | ') : '')))))),
        (sp.Steps || []).length > 0 && h('div', {}, h('h4', {}, sp.Steps.length + ' steps'), h('pre', { class: 'out' }, sp.Steps.map((s, i) => `${i + 1}. ${s}`).join('\n'))),
        (sp.Needs || sp.DependsOn || []).length > 0 && h('div', { class: 'muted' }, 'needs ' + (sp.Needs || sp.DependsOn).join(', ')),
        h('div', { class: 'muted small mono' }, 'rig do ' + t.name))));
  }
  ctx.el.append(pageHead('Tasks', `${tasks.length} tasks in ${groups.length} groups for ${S.info.env} · enter runs the picked one`, searchBox('Filter tasks', v => list.filter(v))),
    splitPane(h('div', { class: 'side-col' }, list.el), detail, { key: 'tasks.side', size: 340 }));
  drawDetail();
  ctx.onKey(e => {
    if (list.keys(e)) return true;
    if (e.key === 'Enter' || e.key === 'r') { const t = tasks.find(x => x.name === cur); if (t) runTask(t); return true; }
    return false;
  });
}

// ---------- queries ----------
function pageQueries(ctx) {
  const qs = resources('query').sort((a, b) => a.Name.localeCompare(b.Name));
  const comps = frameRowsCache.components || [];
  let cur = ctx.rest[0] || store.get('queries.cur', qs[0]?.Name || '_adhoc');
  if (cur !== '_adhoc' && !qs.some(q => q.Name === cur)) cur = qs[0]?.Name || '_adhoc';
  const groups = [...group(qs, q => q.Spec?.Group || q.Spec?.Source || '', { title: k => k || 'Other' }), { id: '~adhoc', title: 'Ad hoc', items: [{ Name: '_adhoc', adhoc: true }] }];
  const right = h('div', { class: 'grow' });
  const list = groupedList({ groups, key: 'queries', cur, id: q => q.Name, text: q => q.adhoc ? 'ad-hoc query' : q.Name + ' ' + (q.Spec?.Help || '') + ' ' + (q.Spec?.Query || '') + ' ' + (q.Spec?.Source || ''),
    render: q => q.adhoc ? [h('div', { class: 'n' }, 'Ad-hoc query'), h('div', { class: 'd' }, 'any component that answers queries')]
      : [h('div', { class: 'n' }, h('span', { class: 'ellipsis' }, q.Name), q.Spec?.Source && h('span', { class: 'tag' }, q.Spec.Source)), h('div', { class: 'd' }, firstLine(q.Spec?.Help) || firstLine(q.Spec?.Query))],
    onPick: q => { cur = q.Name; store.set('queries.cur', cur); drawRight(); },
    menu: q => q.adhoc ? null : [{ label: 'Run', icon: 'play', run: () => { cur = q.Name; drawRight(true); } }, { label: 'Copy the query', icon: 'copy', run: () => copy(q.Spec?.Query || '') }, { label: 'Edit as ad hoc', icon: 'edit', run: () => { store.set('queries.adhoc', { comp: q.Spec?.Source, text: q.Spec?.Query }); cur = '_adhoc'; list.set(cur); drawRight(); } }] });
  let ctrl;
  function result(promise) {
    const out = h('div', {}, loading());
    promise.then(f => out.replaceChildren(resultTable(f, { key: 'q.' + cur })), e => { if (e.name !== 'AbortError') out.replaceChildren(errBox(e)); });
    return out;
  }
  const langOf = c => frameRowsCache.components?.find(x => x.name === c)?.language || '';
  function drawRight(autorun) {
    ctrl?.abort(); ctrl = new AbortController();
    history.replaceState(null, '', '#/queries/' + encodeURIComponent(cur));
    if (cur === '_adhoc') {
      const saved = store.get('queries.adhoc', {});
      const queryable = comps.filter(c => /query/.test(c.caps || ''));
      const comp = h('select', { onchange: () => { ta.placeholder = ph(); } }, (queryable.length ? queryable : comps).map(c => h('option', { value: c.name, selected: c.name === saved.comp }, c.name + (c.type ? ' · ' + c.type : ''))));
      const ph = () => `${langOf(comp.value) || 'the component\'s query language'} for ${comp.value}: SQL, PromQL, a Redis command…  ·  ctrl+enter runs`;
      const ta = h('textarea', { rows: 6, class: 'mono', spellcheck: false, placeholder: '' }, saved.text || '');
      ta.placeholder = ph();
      const ghost = aiAssist(ta, () => `a ${langOf(comp.value) || 'query'} for the ${comp.value} component (${comps.find(c => c.name === comp.value)?.type || ''}); saved queries on it: ` + qs.filter(q => q.Spec?.Source === comp.value).map(q => q.Spec.Query).slice(0, 5).join(' ; '));
      const res = h('div', {});
      const run = () => { store.set('queries.adhoc', { comp: comp.value, text: ta.value }); res.replaceChildren(result(query('queries.run', { component: comp.value, text: ta.value }, ctrl.signal))); };
      ta.addEventListener('keydown', e => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); run(); } });
      right.replaceChildren(h('div', { class: 'card' }, h('div', { class: 'card-h' }, h('span', { class: 'grow' }, 'Ad-hoc query'), comp),
        h('div', { class: 'card-b col' }, ta, ghost, h('div', { class: 'row' }, h('span', { class: 'muted small' }, 'tab takes the AI\'s completion · @? then words: the AI writes the query'), h('span', { class: 'spacer' }), btn([icon('play'), 'Run'], run, 'primary')))), res);
      setTimeout(() => ta.focus(), 30);
      return;
    }
    const q = qs.find(x => x.Name === cur);
    if (!q) { right.replaceChildren(emptyState('No saved queries', 'Add queries: to rig.yaml, or use an ad-hoc query')); return; }
    const sp = q.Spec || {};
    const used = [...new Set([...(sp.Query || '').matchAll(/[$:@]\{?(\w+)\}?|\{\{\s*\.?(\w+)\s*\}\}/g)].map(m => m[1] || m[2]))];
    const params = Object.entries(sp.Params || {}).map(([k, d]) => {
      const desc = typeof d === 'object' && d ? d : { Default: d };
      const input = h('input', { value: store.get(`q.${q.Name}.${k}`, desc.Default ?? ''), placeholder: desc.Help || desc.Description || (desc.Default != null && desc.Default !== '' ? 'default ' + desc.Default : 'value of ' + k), class: 'mono',
        onkeydown: e => { if (e.key === 'Enter') { e.preventDefault(); run(); } } });
      const ghost = aiAssist(input, () => `a value for the ${k} parameter of the query ${q.Name}: ${sp.Query}`);
      return { k, input, el: h('label', { class: 'field' }, h('span', { class: 'mono' }, k), input, ghost) };
    });
    const res = h('div', {});
    const run = () => {
      params.forEach(p => store.set(`q.${q.Name}.${p.k}`, p.input.value));
      res.replaceChildren(result(query('queries.run', { name: q.Name, params: Object.fromEntries(params.map(p => [p.k, p.input.value])) }, ctrl.signal)));
    };
    right.replaceChildren(h('div', { class: 'card' },
      h('div', { class: 'card-h' }, h('span', { class: 'grow' }, h('span', { class: 'muted small' }, (sp.Group || sp.Source || 'query') + ' / '), q.Name), sp.Source && badge(sp.Source, 'accent'),
        btn([icon('copy'), 'Copy'], () => copy(sp.Query || ''), 'sm ghost'), btn([icon('edit'), 'Edit as ad hoc'], () => { store.set('queries.adhoc', { comp: sp.Source, text: sp.Query }); cur = '_adhoc'; list.set(cur); drawRight(); }, 'sm ghost'),
        btn([icon('play'), 'Run'], run, 'primary')),
      h('div', { class: 'card-b col' },
        sp.Help && h('div', { class: 'muted', style: { whiteSpace: 'pre-wrap' } }, sp.Help),
        h('pre', { class: 'out' }, sp.Query || ''),
        params.length > 0 && h('div', { class: 'params' }, params.map(p => p.el)),
        !params.length && used.length > 0 && h('div', { class: 'muted small' }, 'uses ' + used.join(', ')))), res);
    if (!params.length || autorun) run();
  }
  ctx.el.append(pageHead('Queries', `${qs.length} saved queries from rig.yaml by group, and ad-hoc ones`, searchBox('Filter queries', v => list.filter(v))),
    splitPane(h('div', { class: 'side-col' }, list.el), right, { key: 'queries.side', size: 320 }));
  drawRight();
  ctx.onCleanup(() => ctrl?.abort());
  ctx.onKey(e => list.keys(e));
}
// frameRowsCache keeps the components frame the boot read, for screens that only need names and types.
const frameRowsCache = {};

// ---------- jobs ----------
function pageJobs(ctx) {
  let cur = null;
  const list = h('div', { class: 'card list jobs-list' });
  const right = h('div', { class: 'grow' });
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
  ctx.el.append(pageHead('Activity', 'What ran from this page in this session: actions, tasks and their output'), splitPane(list, right, { key: 'jobs.side', size: 320 }));
  document.addEventListener('rig:jobs', draw);
  ctx.onCleanup(() => document.removeEventListener('rig:jobs', draw));
  refreshJobs().then(draw);
  ctx.every(1500, () => refreshJobs());
  ctx.onKey(e => {
    const jobs = [...S.jobs].reverse(), i = jobs.findIndex(j => j.id === cur);
    if (e.key === 'ArrowDown' || e.key === 'j') { cur = jobs[Math.min(jobs.length - 1, i + 1)]?.id; draw(); return true; }
    if (e.key === 'ArrowUp' || e.key === 'k') { cur = jobs[Math.max(0, i - 1)]?.id; draw(); return true; }
    if (e.key === 'y') { const j = jobs[i]; if (j) copy((j.output || []).join('\n')); return true; }
    return false;
  });
}

// ---------- inbox ----------
function pageInbox(ctx) {
  const box = h('div', { class: 'grid', style: { gap: '12px' } });
  const draw = () => {
    const items = [...S.inbox].sort((a, b) => (a.status === 'pending' ? 0 : 1) - (b.status === 'pending' ? 0 : 1) || toDate(b.time) - toDate(a.time));
    box.replaceChildren(...items.map(it => h('div', { class: 'card', style: { opacity: it.status === 'pending' ? 1 : 0.65 } },
      h('div', { class: 'card-h' }, h('span', { class: 'mono' }, it.action), badge(it.status, { pending: 'amber', approved: 'green', done: 'green', rejected: 'red', failed: 'red' }[it.status] || '', true),
        h('span', { class: 'spacer' }), h('span', { class: 'muted', style: { fontWeight: 400 } }, toDate(it.time).toLocaleString())),
      h('div', { class: 'card-b col' },
        h('dl', { class: 'kv' }, h('dt', {}, 'Asked by'), h('dd', {}, `${it.subject?.name || ''} (${it.subject?.kind || ''})`),
          it.env && [h('dt', {}, 'Environment'), h('dd', {}, it.env)], it.why && [h('dt', {}, 'Why'), h('dd', {}, it.why)],
          it.by && [h('dt', {}, 'Decided by'), h('dd', {}, it.by)], it.result && [h('dt', {}, 'Result'), h('dd', {}, it.result)]),
        it.args && Object.keys(it.args).length > 0 && jsonTree(it.args),
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
  const comps = (frameRowsCache.components || []).map(c => c.name);
  const fields = Object.entries(props).map(([k, p]) => {
    const t = p.type || 'string';
    let input;
    if (p.enum) input = h('select', {}, h('option', { value: '' }, ''), p.enum.map(v => h('option', { value: v }, v)));
    else if (t === 'boolean') input = h('input', { type: 'checkbox' });
    else if (t === 'integer' || t === 'number') input = h('input', { type: 'number', step: t === 'integer' ? 1 : 'any', placeholder: p.description || k });
    else if (t === 'object') input = h('textarea', { rows: 3, placeholder: '{ "key": "value" }', class: 'mono' });
    else if (k === 'service' || (t === 'array' && k === 'services') || (t === 'array' && k === 'targets')) {
      input = t === 'array' ? h('select', { multiple: true, style: { height: '90px' } }, serviceNames().map(n => h('option', { value: n }, n)))
        : h('select', {}, h('option', { value: '' }, ''), serviceNames().map(n => h('option', { value: n }, n)));
    } else if (k === 'component' && comps.length) {
      input = h('select', {}, h('option', { value: '' }, ''), comps.map(n => h('option', { value: n }, n)));
    } else input = h('input', { placeholder: t === 'array' ? 'comma separated' : p.description || k });
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
    return { k, get, el: h('label', { class: 'field' }, h('span', {}, h('span', { class: 'mono' }, k), req.has(k) && h('span', { style: { color: 'var(--red)' } }, ' *'), h('span', { class: 'muted' }, ' ' + t), p.description && h('span', { class: 'muted' }, ' — ' + p.description)), input) };
  });
  const raw = h('textarea', { rows: 4, class: 'mono', placeholder: 'extra arguments as JSON, merged over the fields' });
  return {
    el: h('div', { class: 'col' }, fields.map(f => f.el), h('details', {}, h('summary', { class: 'muted' }, 'JSON arguments'), raw)),
    value() {
      const out = {};
      for (const f of fields) { const v = f.get(); if (v !== undefined) out[f.k] = v; }
      if (raw.value.trim()) Object.assign(out, JSON.parse(raw.value));
      return out;
    },
  };
}
// CORE_DOMAINS are the engine's own; any other first part names a plugin, shown only when the project uses it.
const CORE_DOMAINS = new Set(['services', 'runtime', 'env', 'metrics', 'traces', 'logs', 'hosts', 'load', 'data', 'kv', 'queries', 'manifests', 'tests', 'task', 'plugin', 'plugins', 'component', 'components',
  'jobs', 'inbox', 'catalog', 'alerts', 'views', 'secret', 'secrets', 'deploy', 'build', 'git', 'ai', 'docs', 'project', 'doctor', 'audit']);
function apiEntries() {
  const used = new Set(S.info?.used || []);
  const plugins = new Set((frameRowsCache.plugins || []).map(p => p.name));
  const keep = n => { const d = n.split('.')[0]; return CORE_DOMAINS.has(d) || !plugins.has(d) || used.has(d); };
  const srcs = Object.entries(S.provides.sources || {}).filter(([n]) => keep(n)).map(([n, x]) => ({ kind: 'source', name: n, ...x }));
  const acts = Object.entries(S.provides.actions || {}).filter(([n]) => keep(n) && !n.startsWith('task.')).map(([n, x]) => ({ kind: 'action', name: n, ...x }));
  return [...srcs, ...acts].sort((a, b) => a.name.localeCompare(b.name));
}
function pageAPI(ctx) {
  const all = apiEntries();
  let cur = ctx.rest.join('/') || store.get('api.cur', all[0]?.name), ctrl;
  if (!all.some(x => x.name === cur)) cur = all[0]?.name;
  const right = h('div', { class: 'grow' });
  ctx.onCleanup(() => ctrl?.abort());
  const effectBadge = e => badge(e || 'read', { read: 'accent', write: 'amber', danger: 'red' }[e || 'read']);
  const domain = n => n.split('.')[0];
  const groups = group(all, x => domain(x.name)).map(g => ({ ...g, sub: `${g.items.filter(x => x.kind === 'source').length} sources · ${g.items.filter(x => x.kind === 'action').length} actions` }));
  // the open group is the current entry's: opening another closes it
  store.set('gl.api', groups.filter(g => g.id !== domain(cur || '')).map(g => g.id));
  const list = groupedList({ groups, key: 'api', accordion: true, cur, id: x => x.name, text: x => x.name + ' ' + (x.description || ''),
    render: x => [h('div', { class: 'n mono', style: { fontSize: '13px' } }, h('span', { class: 'ellipsis' }, x.name.slice(domain(x.name).length + 1) || x.name), x.kind === 'action' ? effectBadge(x.effect) : badge(x.stream ? 'stream' : 'source', x.stream ? 'purple' : '')), h('div', { class: 'd' }, firstLine(x.description))],
    onPick: x => { cur = x.name; store.set('api.cur', cur); drawRight(); } });
  function drawRight() {
    ctrl?.abort();
    const x = all.find(y => y.name === cur);
    if (!x) { right.replaceChildren(emptyState('Pick a source or an action')); return; }
    history.replaceState(null, '', '#/api/' + x.name);
    const form = schemaForm(x.args);
    const res = h('div', {});
    const runIt = async live => {
      let args;
      try { args = form.value(); } catch (e) { toast('arguments: ' + e.message, 'bad'); return; }
      if (x.kind === 'action') { res.replaceChildren(runResult(await act(x.name, args, x.name))); return; }
      if (live) {
        ctrl?.abort(); ctrl = new AbortController();
        const rows = [];
        let t = null;
        const status = h('span', { class: 'muted' }, 'streaming…');
        res.replaceChildren(h('div', { class: 'toolbar' }, status, h('span', { class: 'spacer' }), btn([icon('stop'), 'Stop'], () => { ctrl.abort(); status.textContent = 'stopped'; }, 'sm')));
        stream(x.name, args, b => {
          if (b.frame) {
            if (!t) { t = frameTable(b.frame, { key: 'api.' + x.name }); res.append(t.el); rows.length = 0; }
            rows.push(...frameRows(b.frame)); if (rows.length > 2000) rows.splice(0, rows.length - 2000);
            t.update(rows);
          }
          if (b.lines?.length) res.append(h('pre', { class: 'out' }, b.lines.join('\n')));
          if (b.done) status.textContent = 'done · ' + rows.length + ' rows';
        }, ctrl.signal).catch(e => { if (e.name !== 'AbortError') res.append(h('div', { class: 'err' }, e.message)); });
        return;
      }
      ctrl?.abort(); ctrl = new AbortController();
      res.replaceChildren(loading());
      try { res.replaceChildren(resultTable(await query(x.name, args, ctrl.signal), { key: 'api.' + x.name })); } catch (e) { if (e.name !== 'AbortError') res.replaceChildren(errBox(e)); }
    };
    right.replaceChildren(h('div', { class: 'card' },
      h('div', { class: 'card-h' }, h('b', { class: 'mono grow' }, x.name), x.kind === 'action' ? effectBadge(x.effect) : badge('source', 'accent'), x.stream && badge('stream', 'purple'),
        x.kind === 'source' && x.stream && btn([icon('play'), 'Stream'], () => runIt(true), 'sm'),
        btn([icon('play'), x.kind === 'action' ? 'Run' : 'Query'], () => runIt(false), x.effect === 'danger' ? 'danger solid' : 'primary')),
      h('div', { class: 'card-b col' }, x.description && h('div', { class: 'muted', style: { whiteSpace: 'pre-wrap' } }, x.description), form.el)), res);
  }
  ctx.el.append(pageHead('API', 'What the engine serves, by domain: the calls the CLI, the MCP server and the assistant make. Plugins the project does not use are left out.', searchBox('Filter', v => list.filter(v))),
    splitPane(h('div', { class: 'side-col' }, list.el), right, { key: 'api.side', size: 340 }));
  drawRight();
  ctx.onKey(e => list.keys(e));
}

// ---------- settings ----------
const LAYOUTS = [
  { id: 'side', t: 'Sidebar', d: 'Navigation down the left: a standard web app', pv: [['a', 0, 0, 22, 100], ['', 28, 8, 66, 10], ['', 28, 26, 66, 60]] },
  { id: 'top', t: 'Top bar', d: 'Navigation across the top, the page full width', pv: [['a', 0, 0, 100, 14], ['', 6, 22, 88, 10], ['', 6, 38, 88, 52]] },
  { id: 'terminal', t: 'Terminal', d: 'The TUI\'s look: monospace, tabs, key hints, keys first', pv: [['a', 0, 0, 30, 10], ['', 32, 0, 20, 10], ['', 3, 16, 94, 70], ['', 0, 92, 100, 8]] },
];
const DEFAULT_PREFS = { theme: '', scheme: 'auto', layout: 'side', density: 'comfortable', font: 'sans' };
function pageSettings(ctx) {
  const tabs = [['look', 'Appearance', 'Layout, theme, mode, density and font'], ['ai', 'Assistant', 'rig ai config: shared with the terminal UI, the CLI and the MCP server'],
    ['keys', 'Keyboard', 'The keys every screen shares, then each screen\'s own'], ['about', 'About', 'This page, the project and the browser storage']];
  let tab = ctx.q.get('s') || store.get('settings.tab', 'look');
  if (!tabs.some(t => t[0] === tab)) tab = 'look';
  const pane = h('div', { class: 'grow settings-pane' });
  const nav = h('div', { class: 'card list settings-nav' });
  const setting = (title, help, control) => h('div', { class: 'setting' }, h('div', { class: 'grow' }, h('div', { class: 'st' }, title), help && h('div', { class: 'muted small' }, help)), control);
  const seg = (k, opts) => h('div', { class: 'seg' }, opts.map(([v, l]) => h('button', { class: S.prefs[k] === v ? 'on' : '', type: 'button', onclick: () => setPref(k, v) }, l)));
  function look() {
    const themes = S.info?.themes || {};
    const light = document.documentElement.dataset.scheme === 'light';
    return [
      h('div', { class: 'card' }, h('div', { class: 'card-h' }, 'Layout'), h('div', { class: 'card-b' }, h('div', { class: 'layouts' }, LAYOUTS.map(l => h('button', { class: 'layout-opt' + (S.prefs.layout === l.id ? ' on' : ''), type: 'button', onclick: () => setPref('layout', l.id) },
        h('div', { class: 'pv' }, l.pv.map(([c, x, y, w, hh]) => h('b', { class: c, style: { left: x + '%', top: y + '%', width: w + '%', height: hh + '%' } }))),
        h('div', { class: 't' }, l.t), h('div', { class: 'd' }, l.d)))))),
      h('div', { class: 'card' }, h('div', { class: 'card-h' }, 'Display'), h('div', { class: 'card-b col settings-rows' },
        setting('Mode', 'Follow the system, or always dark or light', seg('scheme', [['auto', 'System'], ['dark', 'Dark'], ['light', 'Light']])),
        setting('Density', 'Row height and padding', seg('density', [['comfortable', 'Comfortable'], ['compact', 'Compact']])),
        setting('Font', 'The terminal layout is always monospace', seg('font', [['sans', 'Sans'], ['mono', 'Monospace']])))),
      h('div', { class: 'card' }, h('div', { class: 'card-h' }, 'Theme', h('span', { class: 'muted small', style: { fontWeight: 400 } }, 'the terminal UI\'s themes (rig theme), your own in ~/.config/rig/themes included · ctrl+p')),
        h('div', { class: 'card-b' }, h('div', { class: 'themes' }, Object.keys(themes).sort().map(n => {
          const t = themes[n], sh = light ? t.Light : t;
          return h('button', { class: 'theme' + (S.themeName === n ? ' on' : ''), type: 'button', onclick: () => setPref('theme', n) },
            h('div', { class: 'pv', style: { background: sh.Bar } },
              h('div', { class: 'sb', style: { background: `color-mix(in srgb, ${sh.Bar}, ${sh.Text} 5%)` } }, h('i', { style: { background: t.Accent } }), h('i', { style: { background: sh.Panel } }), h('i', { style: { background: sh.Panel } })),
              h('div', { class: 'mn' }, h('i', { style: { background: sh.Text, width: '60%', opacity: 0.8 } }), h('i', { style: { background: sh.Dim, width: '85%' } }),
                h('div', { class: 'row', style: { gap: '4px' } }, (t.Series || []).slice(0, 5).map(c => h('i', { style: { background: c, width: '18%', height: '14px' } }))))),
            h('div', { class: 'nm' }, n, S.info?.theme === n && h('span', { class: 'muted', style: { fontWeight: 400, fontSize: '12px' } }, 'tui'),
              h('span', { class: 'sw' }, [t.Accent, t.Green, t.Amber, t.Red].map(c => h('i', { style: { background: c } })))));
        })))),
    ];
  }
  function about() {
    const keys = Object.keys(localStorage).filter(k => k.startsWith('rig.web.'));
    return [h('div', { class: 'card' }, h('div', { class: 'card-h' }, 'This page'), h('div', { class: 'card-b' }, h('dl', { class: 'kv' },
      h('dt', {}, 'Project'), h('dd', {}, S.info?.project), h('dt', {}, 'Environment'), h('dd', {}, S.info?.env), h('dt', {}, 'Runtime'), h('dd', {}, S.info?.runtime),
      h('dt', {}, 'Version'), h('dd', {}, 'rig ' + (S.info?.version || '')), h('dt', {}, 'Plugins in use'), h('dd', {}, (S.info?.used || []).join(', '))))),
    h('div', { class: 'card' }, h('div', { class: 'card-h' }, 'Saved in this browser'), h('div', { class: 'card-b col settings-rows' },
      setting('Look', 'Layout, theme, mode, density and font', btn('Reset', () => { S.prefs = { ...DEFAULT_PREFS }; store.set('prefs', S.prefs); applyPrefs(); renderChrome(); route(); }, 'sm')),
      setting('Everything else', `${keys.length} entries: column widths, pane sizes, picks, filters, history`, btn('Clear', async () => {
        if (!await confirmBox('Clear saved state', 'Column widths, pane sizes, the last picks and history go; the sign-in token stays.', 'Clear', true)) return;
        keys.filter(k => k !== 'rig.web.token').forEach(k => localStorage.removeItem(k)); location.reload();
      }, 'sm danger'))))];
  }
  function draw() {
    store.set('settings.tab', tab);
    nav.replaceChildren(...tabs.map(([id, t, d]) => h('div', { class: 'item' + (id === tab ? ' sel' : ''), onclick: () => { tab = id; draw(); } }, h('div', { class: 'n' }, t), h('div', { class: 'd' }, d))));
    const [, title, desc] = tabs.find(t => t[0] === tab);
    pane.replaceChildren(h('div', { class: 'settings-head' }, h('h2', {}, title), h('p', { class: 'muted' }, desc)),
      ...(tab === 'look' ? look() : tab === 'ai' ? [h('div', { class: 'card card-b' }, aiSettings())] : tab === 'keys' ? [shortcutsTable()] : about()));
  }
  ctx.el.append(pageHead('Settings', 'Saved in this browser, except the assistant\'s, which rig ai config keeps'), h('div', { class: 'settings' }, nav, pane));
  draw();
  ctx.onKey(e => {
    const i = tabs.findIndex(t => t[0] === tab);
    if (e.key === 'ArrowDown' || e.key === 'j') { tab = tabs[Math.min(tabs.length - 1, i + 1)][0]; draw(); return true; }
    if (e.key === 'ArrowUp' || e.key === 'k') { tab = tabs[Math.max(0, i - 1)][0]; draw(); return true; }
    return false;
  });
  ctx.subTab = d => { const i = tabs.findIndex(t => t[0] === tab); tab = tabs[(i + d + tabs.length) % tabs.length][0]; draw(); };
}
const GLOBAL_KEYS = [
  ['Commands', [[':', 'command line: rig\'s own verbs (restart parser, open Logs, env k8s) or any rig command'], ['Ctrl/⌘ K', 'search screens, services, tasks, dashboards, queries'], ['T', 'run a task'],
    ['.  right-click', 'this screen\'s actions as a menu'], ['@', 'the assistant (/ in its box: commands)']]],
  ['Move around', [['Tab  ⇧Tab  ⇧← →  Alt+← →', 'next, previous screen'], ['Ctrl+← →  [ ]', 'switch the sub-tab or dashboard inside a screen'], ['↑↓  j k', 'move'], ['enter  dbl-click', 'open, run'],
    ['esc  ⌫', 'back: closes what is open, then returns to the screen you came from'], ['drag a border', 'resize panes and columns (kept for next time)']]],
  ['Lists and tables', [['/', 'filter: words (fuzzy), *glob*, column=value, column!=value, column~text'], ['click a header', 'sort; drag its edge to resize'], ['+ - ←→', 'expand all, fold all, fold a group'], ['z  v', 'full screen panel']]],
  ['Environment and look', [['Ctrl E', 'switch environment'], ['Ctrl P', 'pick a theme'], ['F11', 'browser full screen'], ['! ', 'activity'], ['? F1', 'this help']]],
];
function shortcutsTable(filter = '') {
  const page = pageOf(parseRoute().id);
  const groups = [...GLOBAL_KEYS, ...PAGES.filter(p => p.keys).map(p => [p.title + (p === page ? ' (this screen)' : ''), p.keys])];
  if (page?.keys) groups.unshift(groups.splice(groups.findIndex(g => g[0].endsWith('(this screen)')), 1)[0]);
  return h('div', { class: 'keys-grid' }, groups.map(([title, rows]) => {
    const shown = rows.filter(([k, d]) => matchText(k + ' ' + d + ' ' + title, filter));
    return shown.length > 0 && h('div', { class: 'card' }, h('div', { class: 'card-h' }, title),
      h('table', { class: 'plain keys-t' }, h('tbody', {}, shown.map(([k, d]) => h('tr', {}, h('td', {}, h('kbd', {}, k)), h('td', {}, d))))));
  }));
}
