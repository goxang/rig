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

function pageMetrics(ctx) {
  const dashes = resources('dashboard').map(r => r.Name).sort();
  let cur = ctx.rest[0] && (dashes.includes(ctx.rest[0]) || ctx.rest[0] === '_query') ? ctx.rest[0] : (dashes[0] || '_query');
  let range = store.get('metrics.range', '15m'), every = store.get('metrics.every', 30), timer, vars = {};
  const body = h('div', {});
  const tabs = h('div', { class: 'seg' });
  const varBar = h('div', { class: 'row wrap' });
  // a long row of dashboards becomes a picker, like the TUI's
  const drawTabs = () => dashes.length > 5
    ? tabs.replaceChildren(h('select', { 'aria-label': 'dashboard', onchange: e => go('metrics/' + e.target.value) },
      dashes.map(d => h('option', { value: d, selected: d === cur }, d)), h('option', { value: '_query', selected: cur === '_query' }, 'Query…')))
    : tabs.replaceChildren(...dashes.map(d => h('button', { class: d === cur ? 'on' : '', onclick: () => go('metrics/' + d) }, d)),
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
  // grouped by domain (services, metrics, a plugin's name): a group opens on a click, or while filtering
  const open = new Set(store.get('api.open', []));
  const domain = n => n.split('.')[0];
  const drawList = () => {
    const f = filter.toLowerCase();
    const m = x => !f || (x.name + ' ' + (x.description || '')).toLowerCase().includes(f);
    const groups = new Map();
    for (const x of all.filter(m)) { const d = domain(x.name); if (!groups.has(d)) groups.set(d, []); groups.get(d).push(x); }
    list.replaceChildren(...[...groups].sort(([a], [b]) => a.localeCompare(b)).flatMap(([d, items]) => {
      const shown = f || open.has(d) || domain(cur || '') === d;
      return [h('button', { class: 'group-h click' + (shown ? '' : ' closed'), type: 'button', onclick: () => { open.has(d) ? open.delete(d) : open.add(d); store.set('api.open', [...open]); drawList(); } },
        icon('chev', 'chev'), d, h('span', { class: 'spacer' }), h('span', { class: 'muted' }, items.filter(x => x.kind === 'source').length + ' · ' + items.filter(x => x.kind === 'action').length)),
      ...(shown ? items : []).map(x => h('div', { class: 'item' + (x.name === cur ? ' sel' : ''), onclick: () => { cur = x.name; drawList(); drawRight(); } },
        h('div', { class: 'n mono', style: { fontSize: '13px' } }, x.name.slice(d.length + 1) || x.name, x.kind === 'action' ? effectBadge(x.effect) : badge(x.stream ? 'stream' : 'source', x.stream ? 'purple' : '')), h('div', { class: 'd' }, firstLine(x.description))))];
    }));
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
  ctx.el.append(pageHead('API', 'What the engine serves, by domain: the same calls the CLI, the MCP server and the assistant make; a group shows sources · actions', searchBox('Filter', v => { filter = v; drawList(); })),
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
    h('div', { class: 'section', id: 'set-ai' }, h('h3', {}, 'Assistant'), h('p', { class: 'muted', style: { marginTop: 0 } }, 'rig ai config: shared with the terminal UI, the CLI and the MCP server'), aiSettings()),
    h('div', { class: 'section' }, h('h3', {}, 'Keyboard'), shortcutsTable()),
    h('div', { class: 'section' }, btn('Reset to defaults', () => { S.prefs = { theme: '', scheme: 'auto', layout: 'side', density: 'comfortable', font: 'sans' }; store.set('prefs', S.prefs); applyPrefs(); renderChrome(); route(); }, 'ghost')));
  if (ctx.q.get('s') === 'ai') setTimeout(() => $('#set-ai')?.scrollIntoView({ block: 'start' }), 50);
}
function shortcutsTable() {
  const rows = [['1 – 9', 'go to a screen'], ['Shift ← →', 'previous / next screen'], ['Ctrl/⌘ K', 'search and run anything'], [':', 'run a command: rig\'s own (start api, env staging, theme) or any rig command'],
    ['@', 'the assistant'], ['.', 'what this screen can do'], ['!', 'activity'], ['T', 'run a task'], ['Ctrl E', 'switch environment'], ['Ctrl P', 'pick a theme'],
    ['[ ]', 'previous / next sub-tab'], ['/', 'focus the page\'s filter'], ['?', 'this help'], ['Esc', 'close a panel or dialog'],
    ...PAGES.filter(p => p.keys).flatMap(p => p.keys.map(([k, d]) => [k, p.title + ': ' + d]))];
  return h('div', { class: 'table-wrap' }, h('table', {}, h('tbody', {}, rows.map(([k, d]) => h('tr', {}, h('td', { style: { width: '160px' } }, h('kbd', {}, k)), h('td', {}, d))))));
}
