'use strict';

// ---------- services: grouped by rig.yaml's sections ----------
function stateBadge(st) {
  const cls = { running: 'green', starting: 'amber pulse', degraded: 'amber', failed: 'red' }[st] || '';
  return badge(st || 'unknown', cls, true);
}
const STATE_FILTERS = [['', 'All'], ['running', 'Running'], ['problem', 'Problems'], ['stopped', 'Stopped']];
const stateClass = st => st === 'running' ? 'running' : ['failed', 'degraded', 'starting'].includes(st) ? 'problem' : 'stopped';
function pageServices(ctx) {
  const marked = new Set(), closed = new Set(store.get('svc.closed', []));
  let rows = [], cursor = null, filterText = '', stateF = ctx.q.get('state') || '';
  const sections = [...(S.info.sections || []), { name: 'infra', help: 'databases, brokers and the rest services need' }, { name: 'other', help: '' }];
  const body = h('div', { class: 'svc-groups' }, loading());
  const summary = h('div', { class: 'row wrap svc-sum' });
  const bulk = h('div', { class: 'bulk', hidden: true });
  const tables = new Map();
  const reload = () => load();
  const svcAct = (verb, names) => names.length === 1 && verb !== 'start' && verb !== 'stop'
    ? act('runtime.' + verb, { service: names[0] }, verb + ' ' + names[0]).then(reload)
    : verb === 'start' ? act('env.up', { targets: names }, 'start ' + names.join(', ')).then(reload)
      : verb === 'stop' ? act('env.down', { targets: names }, 'stop ' + names.join(', ')).then(reload)
        : Promise.all(names.map(n => act('runtime.' + verb, { service: n }, verb + ' ' + n))).then(reload);
  const visible = () => sections.flatMap(s => closed.has(s.name) && !filterText ? [] : tables.get(s.name)?.visible || []);
  const targets = () => marked.size ? [...marked] : cursor ? [cursor] : [];
  const columns = [
    { check: true, onchange: () => drawBulk() },
    { key: 'service', label: 'Service', render: r => h('span', { class: 'svc-name' }, r.service, r.role && !['service', 'app'].includes(r.role) && h('span', { class: 'muted small' }, ' ' + r.role)) },
    { key: 'state', label: 'State', render: r => stateBadge(r.state) },
    { key: 'ready', label: 'Ready', num: true, render: r => h('span', { class: r.desired > 0 && r.ready < r.desired ? 'amber' : 'muted' }, `${r.ready ?? 0}/${r.desired ?? 0}`) },
    { key: 'image', label: 'Image', cls: 'mono muted', render: r => h('span', { title: r.image }, shortImage(r.image)) },
    { key: 'message', label: '', cls: 'muted msg', render: r => h('span', { class: 'ellipsis', title: r.message }, firstLine(r.message)) },
    { act: true, label: '', render: r => h('div', { class: 'row hover-acts' },
      ibtn('play', () => svcAct('start', [r.service]), 'Start (s)', 'sm'), ibtn('stop', () => svcAct('stop', [r.service]), 'Stop (x)', 'sm'),
      ibtn('restart', () => svcAct('restart', [r.service]), 'Restart (r)', 'sm'), ibtn('logs', () => go('logs', { svc: r.service }), 'Logs (l)', 'sm')) },
  ];
  function drawBulk() {
    bulk.hidden = !marked.size;
    bulk.replaceChildren(h('b', {}, marked.size + ' marked'), h('span', { class: 'spacer' }),
      btn([icon('play'), 'Start'], () => svcAct('start', [...marked]), 'sm'), btn([icon('stop'), 'Stop'], () => svcAct('stop', [...marked]), 'sm'),
      btn([icon('restart'), 'Restart'], () => svcAct('restart', [...marked]), 'sm'), btn([icon('logs'), 'Logs'], () => go('logs', { svc: [...marked].join(',') }), 'sm'),
      btn('Clear', () => { marked.clear(); tables.forEach(t => t.redraw()); drawBulk(); }, 'ghost sm'));
  }
  function draw() {
    const shown = rows.filter(r => !stateF || stateClass(r.state) === stateF);
    const count = c => rows.filter(r => stateClass(r.state) === c).length;
    summary.replaceChildren(...STATE_FILTERS.map(([v, l]) => h('button', { class: 'chip' + (stateF === v ? ' on' : ''), type: 'button', onclick: () => { stateF = v; history.replaceState(null, '', '#/services' + (v ? '?state=' + v : '')); draw(); } },
      v && h('span', { class: 'dot', style: { background: { running: 'var(--green)', problem: 'var(--amber)', stopped: 'var(--dim)' }[v] } }), l, h('span', { class: 'n' }, v ? count(v) : rows.length))));
    const groups = sections.map(s => ({ ...s, rows: shown.filter(r => (r.section || 'other') === s.name) })).filter(g => g.rows.length);
    const els = groups.map(g => {
      let t = tables.get(g.name);
      if (!t) {
        t = dataTable({ columns, rowKey: r => r.service, selected: marked, onRow: r => { cursor = r.service; mark(); openService(r.service); }, rowClass: r => r.service === cursor ? 'cur' : '' });
        tables.set(g.name, t);
      }
      t.update(g.rows);
      if (filterText) t.filter(filterText);
      const up = g.rows.filter(r => r.state === 'running').length, isClosed = closed.has(g.name) && !filterText;
      const names = g.rows.map(r => r.service);
      return h('section', { class: 'svc-group' + (isClosed ? ' closed' : ''), hidden: !!filterText && !t.visible.length },
        h('div', { class: 'svc-gh' },
          h('button', { class: 'svc-toggle', type: 'button', onclick: () => { closed.has(g.name) ? closed.delete(g.name) : closed.add(g.name); store.set('svc.closed', [...closed]); draw(); } },
            icon('chev', 'chev'), h('b', {}, g.name), g.help && h('span', { class: 'muted ellipsis' }, g.help)),
          h('span', { class: 'spacer' }),
          h('span', { class: 'meter', title: `${up} of ${g.rows.length} running` }, h('i', { style: { width: 100 * up / g.rows.length + '%' } })),
          h('span', { class: 'muted small mono' }, `${up}/${g.rows.length}`),
          h('div', { class: 'row hover-acts' }, ibtn('play', () => svcAct('start', names), 'Start the section', 'sm'), ibtn('stop', () => svcAct('stop', names), 'Stop the section', 'sm'), ibtn('logs', () => go('logs', { svc: names.join(',') }), 'The section\'s logs', 'sm'))),
        !isClosed && t.el);
    });
    body.replaceChildren(...els, ...(els.length ? [] : [emptyState(rows.length ? 'No service matches' : 'No services in this environment')]));
  }
  const mark = () => tables.forEach(t => t.el.querySelectorAll('tbody tr').forEach((tr, i) => tr.classList.toggle('cur', t.visible[i]?.service === cursor)));
  const load = poller(ctx, 3000, async () => {
    try { rows = frameRows(await query('services.status')); } catch (e) { body.replaceChildren(errBox(e)); return; }
    draw();
  });
  ctx.el.append(
    pageHead('Services', `${S.info.project} on ${S.info.env}`,
      searchBox('Filter services', v => { filterText = v; draw(); }),
      btn([icon('up'), 'Up'], () => act('env.up', { targets: filterText ? visible().map(r => r.service) : [] }, 'up').then(reload), 'primary', 'Deploy services and their dependencies'),
      btn([icon('down'), 'Down'], () => act('env.down', { targets: filterText ? visible().map(r => r.service) : [] }, 'down').then(reload), '', 'Stop services in reverse dependency order'),
      ibtn('refresh', reload, 'Refresh')),
    h('div', { class: 'toolbar' }, summary, h('span', { class: 'spacer' }), bulk), body);
  load();
  ctx.menu([
    { key: 's', label: 'Start', run: () => svcAct('start', targets()) }, { key: 'x', label: 'Stop', run: () => svcAct('stop', targets()) },
    { key: 'r', label: 'Restart', run: () => svcAct('restart', targets()) }, { key: 'l', label: 'Logs', run: () => go('logs', { svc: targets().join(',') }) },
    { key: 'u', label: 'Up everything', run: () => act('env.up', { targets: [] }, 'up').then(reload) }, { key: 'd', label: 'Down everything', run: () => act('env.down', { targets: [] }, 'down').then(reload) },
  ]);
  ctx.onKey(e => {
    const vis = visible(), i = vis.findIndex(r => r.service === cursor);
    switch (e.key) {
      case 'ArrowDown': case 'j': cursor = vis[Math.min(vis.length - 1, i + 1)]?.service; mark(); return true;
      case 'ArrowUp': case 'k': cursor = vis[Math.max(0, i - 1)]?.service; mark(); return true;
      case ' ': if (cursor) { marked.has(cursor) ? marked.delete(cursor) : marked.add(cursor); tables.forEach(t => t.redraw()); mark(); drawBulk(); } return true;
      case 'Enter': if (cursor) openService(cursor); return true;
      case 's': case 'x': case 'r': if (targets().length) svcAct({ s: 'start', x: 'stop', r: 'restart' }[e.key], targets()); return true;
      case 'l': go('logs', { svc: targets().join(',') }); return true;
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
  const replicas = h('input', { type: 'number', min: 0, value: 1, style: { width: '70px' } });
  const ports = Object.entries(sp.Ports || {});
  const dd = (k, v) => v != null && v !== '' && (Array.isArray(v) ? v.length : true) ? [h('dt', {}, k), h('dd', {}, v)] : null;
  const svcAct = a => act('runtime.' + a, { service: name }, a + ' ' + name).then(loadStatus);
  async function loadStatus() {
    try {
      const r = frameRows(await query('services.status', { services: [name] }))[0];
      if (!r) return;
      replicas.value = r.desired ?? 1;
      status.replaceChildren(h('dl', { class: 'kv' }, dd('State', stateBadge(r.state)), dd('Ready', `${r.ready}/${r.desired}`), dd('Section', r.section), dd('Image', h('span', { class: 'mono' }, r.image)), dd('Message', r.message)));
    } catch (e) { status.replaceChildren(h('div', { class: 'err' }, e.message)); }
  }
  drawer(name, sp.Help || sp.Description || 'service', [
    h('div', { class: 'row wrap' },
      btn([icon('play'), 'Start'], () => svcAct('start'), 'primary'), btn([icon('stop'), 'Stop'], () => svcAct('stop')),
      btn([icon('restart'), 'Restart'], () => svcAct('restart')), h('span', { class: 'spacer' }),
      h('span', { class: 'row' }, 'Replicas', replicas, btn('Scale', () => act('runtime.scale', { service: name, replicas: +replicas.value }, `scale ${name} to ${replicas.value}`).then(loadStatus), 'sm'))),
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

// ---------- shared pieces ----------
// picker is a left list of names that remembers the last one picked per screen.
function sideList(items, cur, onPick, { label = x => x.name, sub = x => x.sub, key = 'name' } = {}) {
  return h('div', { class: 'card list side-list' }, items.map(x => h('div', { class: 'item' + (x[key] === cur ? ' sel' : ''), onclick: () => onPick(x) },
    h('div', { class: 'n' }, label(x)), sub(x) && h('div', { class: 'd' }, sub(x)))));
}
function resultTable(f, opts = {}) {
  const rows = frameRows(f), cols = (f?.fields || []).map(c => c.name), t = frameTable(f, opts);
  return h('div', {}, h('div', { class: 'toolbar' }, h('b', {}, rows.length + ' rows'), f?.meta?.notice && h('span', { class: 'muted' }, f.meta.notice), h('span', { class: 'spacer' }),
    searchBox('Filter rows', v => t.filter(v)), btn([icon('copy'), 'CSV'], () => copy(toCSV(rows, cols)), 'sm'), btn([icon('copy'), 'JSON'], () => copy(JSON.stringify(rows, null, 2)), 'sm')), t.el);
}
const has = (c, cap) => String(c?.caps || '').split(' ').includes(cap);

// ---------- data: browse, query, queues ----------
function pageData(ctx) {
  let comps = [], cur = ctx.rest[0], path = ctx.rest.slice(1), tab = store.get('data.tab', 'browse');
  const left = h('div', {}, loading()), right = h('div', { class: 'grow' });
  ctx.el.append(pageHead('Data', 'Databases, caches and brokers: browse, query, edit, peek'), h('div', { class: 'split' }, left, right));
  const place = () => history.replaceState(null, '', '#/data' + (cur ? '/' + [cur, ...path].map(encodeURIComponent).join('/') : ''));
  query('components').then(f => {
    comps = frameRows(f).filter(c => /browse|query|queues/.test(c.caps || ''));
    if (!comps.length) { left.replaceChildren(); right.replaceChildren(emptyState('No database, cache or broker', 'Components rig.yaml names show here')); return; }
    if (!comps.some(c => c.name === cur)) { cur = store.get('data.cur', comps[0].name); if (!comps.some(c => c.name === cur)) cur = comps[0].name; path = []; }
    drawLeft(); drawRight();
  }, e => left.replaceChildren(errBox(e)));
  const drawLeft = () => left.replaceChildren(sideList(comps, cur, c => { cur = c.name; path = []; store.set('data.cur', cur); drawLeft(); drawRight(); },
    { sub: c => [c.kind, c.type].filter(Boolean).join(' · ') }));
  function drawRight() {
    place();
    const c = comps.find(x => x.name === cur);
    const tabs = [has(c, 'browse') && 'browse', has(c, 'query') && 'query', has(c, 'queues') && 'queues', has(c, 'actions') && 'actions'].filter(Boolean);
    if (!tabs.includes(tab)) tab = tabs[0];
    const pane = h('div', {});
    right.replaceChildren(h('div', { class: 'subtabs' }, tabs.map(t => h('button', { class: t === tab ? 'on' : '', type: 'button', onclick: () => { tab = t; store.set('data.tab', t); drawRight(); } }, t[0].toUpperCase() + t.slice(1)))), pane);
    ({ browse: browse, query: queryPane, queues: queues, actions: compActions })[tab]?.(pane, c);
  }
  async function browse(pane, c) {
    const crumbs = h('div', { class: 'crumbs' }, h('a', { href: '#', onclick: e => { e.preventDefault(); path = []; drawRight(); } }, c.name),
      path.map((p, i) => [h('span', { class: 'sep' }, '/'), h('a', { href: '#', onclick: e => { e.preventDefault(); path = path.slice(0, i + 1); drawRight(); } }, p)]));
    const box = h('div', {}, loading());
    pane.replaceChildren(crumbs, box);
    let f;
    try { f = await query('data.browse', { component: c.name, path }); } catch (e) { box.replaceChildren(errBox(e)); return; }
    const leaf = f.name === 'leaf', rows = frameRows(f), first = f.fields?.[0]?.name;
    if (!leaf) {
      const t = frameTable(f, { onRow: r => { path = [...path, String(r[first])]; drawRight(); }, empty: 'Empty' });
      box.replaceChildren(h('div', { class: 'toolbar' }, h('b', {}, rows.length + ' items'), h('span', { class: 'spacer' }), searchBox('Filter', v => t.filter(v)),
        path.length > 0 && btn([icon('back'), 'Up'], () => { path = path.slice(0, -1); drawRight(); }, 'sm')), t.el);
      return;
    }
    const sel = new Set();
    const idx = new Map(rows.map((r, i) => [r, i]));
    const editable = has(c, 'edit');
    const cols = (f.fields || []).map(fl => ({ key: fl.name, field: fl, num: ['int', 'number'].includes(fl.type),
      render: editable ? r => h('span', { class: 'cell-edit', title: 'double-click to edit', ondblclick: async () => {
        const v = await ask('Set ' + fl.name, { value: r[fl.name] ?? '', multiline: String(r[fl.name] ?? '').length > 60 });
        if (v == null) return;
        if (await act('data.set', { component: c.name, path, row: idx.get(r), column: fl.name, value: v }, 'set ' + fl.name)) drawRight();
      } }, fmtCell(r[fl.name], fl)) : null }));
    const t = dataTable({ columns: [editable && { check: true, onchange: () => del.hidden = !sel.size }, ...cols].filter(Boolean), rowKey: r => idx.get(r), selected: sel, empty: 'No rows' });
    t.update(rows);
    const del = btn([icon('trash'), 'Delete marked'], async () => {
      if (!await confirmBox('Delete rows', `${sel.size} rows of ${[c.name, ...path].join('/')}`, 'Delete', true)) return;
      if (await act('data.delete', { component: c.name, path, rows: [...sel].map(String) }, 'delete rows')) drawRight();
    }, 'danger sm');
    del.hidden = true;
    box.replaceChildren(h('div', { class: 'toolbar' }, h('b', {}, rows.length + ' rows'), f.meta?.notice && h('span', { class: 'muted' }, f.meta.notice), h('span', { class: 'spacer' }),
      del, searchBox('Filter', v => t.filter(v)), has(c, 'query') && btn([icon('queries'), 'Query here'], () => { tab = 'query'; drawRight(); }, 'sm'),
      btn([icon('copy'), 'CSV'], () => copy(toCSV(rows, cols.map(x => x.key))), 'sm'), btn([icon('back'), 'Up'], () => { path = path.slice(0, -1); drawRight(); }, 'sm')), t.el);
  }
  function queryPane(pane, c) {
    const key = 'data.q.' + c.name;
    const ta = h('textarea', { rows: 5, class: 'mono', placeholder: (c.language || 'query') + (path.length ? ' at ' + path.join('/') : '') + ' · ctrl+enter runs', spellcheck: false }, store.get(key, ''));
    const res = h('div', {});
    const run = async () => {
      store.set(key, ta.value);
      res.replaceChildren(loading());
      try { res.replaceChildren(resultTable(await query('data.query', { component: c.name, query: ta.value, path }))); } catch (e) { res.replaceChildren(errBox(e)); }
    };
    ta.addEventListener('keydown', e => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); run(); } });
    pane.replaceChildren(h('div', { class: 'card card-b col' }, ta, h('div', { class: 'row' }, path.length > 0 && h('span', { class: 'muted' }, 'at ' + path.join('/')), h('span', { class: 'spacer' }),
      has(c, 'suggest') && path.length > 0 && btn('Suggest', async () => { try { ta.value = frameRows(await query('data.suggest', { component: c.name, path }))[0]?.query || ta.value; } catch (e) { toast(e.message, 'bad'); } }, 'sm'),
      btn([icon('play'), 'Run'], run, 'primary'))), res);
    ta.focus();
  }
  async function queues(pane, c) {
    pane.replaceChildren(loading());
    let f;
    try { f = await query('data.queues', { component: c.name }); } catch (e) { pane.replaceChildren(errBox(e)); return; }
    const t = frameTable(f, { onRow: r => peek(c, r.queue), empty: 'No queues' });
    pane.replaceChildren(h('div', { class: 'toolbar' }, h('b', {}, frameRows(f).length + ' queues'), h('span', { class: 'muted' }, 'click a queue to peek'), h('span', { class: 'spacer' }), searchBox('Filter', v => t.filter(v)), ibtn('refresh', () => drawRight(), 'Refresh')), t.el);
  }
  function peek(c, q) {
    const box = h('div', {}, loading());
    drawer(q, c.name + ' · messages stay queued', [h('div', { class: 'row' }, btn([icon('trash'), 'Purge'], async () => { if (await act('data.purge', { component: c.name, queue: q }, 'purge ' + q)) { closeOverlay(); drawRight(); } }, 'danger sm'),
      h('span', { class: 'spacer' }), btn('Settings', async () => { try { box.replaceChildren(resultTable(await query('data.peek', { component: c.name, queue: q, detail: 'info' }))); } catch (e) { box.replaceChildren(errBox(e)); } }, 'sm')), box]).classList.add('wide');
    query('data.peek', { component: c.name, queue: q, n: 50 }).then(f => box.replaceChildren(resultTable(f)), e => box.replaceChildren(errBox(e)));
  }
  ctx.onKey(e => { if (e.key === 'Backspace' && path.length && tab === 'browse') { path = path.slice(0, -1); drawRight(); return true; } return false; });
}
// compActions lists what an adapter can do beyond the screens (rig do).
async function compActions(pane, c) {
  pane.replaceChildren(loading());
  let rows;
  try { rows = frameRows(await query('component.actions', { component: c.name })); } catch (e) { pane.replaceChildren(errBox(e)); return; }
  pane.replaceChildren(h('div', { class: 'card list' }, rows.map(r => h('div', { class: 'item row' }, h('div', { class: 'grow' }, h('div', { class: 'n mono' }, r.name, r.mutate && badge('changes', 'amber')), h('div', { class: 'd' }, r.help)),
    btn([icon('play'), 'Run'], async () => {
      const a = await ask(c.name + ' ' + r.name, { label: 'arguments, space separated (optional)' });
      if (a == null) return;
      act('component.do', { component: c.name, action: r.name, args: a.split(/\s+/).filter(Boolean) }, c.name + ' ' + r.name);
    }, 'sm')))));
}

// ---------- kv ----------
function pageKV(ctx) {
  let comps = [], cur = ctx.rest[0], prefix = ctx.q.get('prefix') || '';
  const left = h('div', {}, loading()), right = h('div', { class: 'grow' });
  ctx.el.append(pageHead('KV', 'Config stores: keys under a prefix'), h('div', { class: 'split' }, left, right));
  query('components').then(f => {
    comps = frameRows(f).filter(c => c.kind === 'kv');
    if (!comps.length) { left.replaceChildren(); right.replaceChildren(emptyState('No config store', 'A kv component (consul, etcd) shows here')); return; }
    if (!comps.some(c => c.name === cur)) cur = comps[0].name;
    drawLeft(); draw();
  }, e => left.replaceChildren(errBox(e)));
  const drawLeft = () => left.replaceChildren(sideList(comps, cur, c => { cur = c.name; prefix = ''; drawLeft(); draw(); }, { sub: c => c.type }));
  const sel = new Set();
  async function draw() {
    history.replaceState(null, '', '#/kv/' + encodeURIComponent(cur) + (prefix ? '?prefix=' + encodeURIComponent(prefix) : ''));
    const box = h('div', {}, loading());
    const pin = h('input', { value: prefix, placeholder: 'prefix', class: 'mono', onkeydown: e => { if (e.key === 'Enter') { prefix = e.target.value; draw(); } } });
    right.replaceChildren(h('div', { class: 'toolbar' }, pin, btn('List', () => { prefix = pin.value; draw(); }, 'sm'), h('span', { class: 'spacer' }),
      btn([icon('plus'), 'Put'], () => put(prefix, ''), 'sm primary')), box);
    let f;
    try { f = await query('kv.list', { component: cur, prefix }); } catch (e) { box.replaceChildren(errBox(e)); return; }
    sel.clear();
    const del = btn([icon('trash'), 'Delete marked'], async () => { if (await act('kv.delete', { component: cur, keys: [...sel] }, 'delete ' + sel.size + ' keys')) draw(); }, 'danger sm');
    del.hidden = true;
    const t = dataTable({ rowKey: r => r.key, selected: sel, onRow: r => put(r.key, r.value), empty: 'No keys',
      columns: [{ check: true, onchange: () => del.hidden = !sel.size }, { key: 'key', cls: 'mono' }, { key: 'value', cls: 'mono muted', render: r => h('span', { class: 'ellipsis' }, firstLine(r.value)) }, { key: 'size', num: true }] });
    t.update(frameRows(f));
    box.replaceChildren(h('div', { class: 'toolbar' }, h('b', {}, t.visible.length + ' keys'), h('span', { class: 'muted' }, 'click a key to edit'), h('span', { class: 'spacer' }), del, searchBox('Filter', v => t.filter(v))), t.el);
  }
  async function put(key, value) {
    const k = key && !key.endsWith('/') ? key : await ask('Put a key', { label: 'key', value: key });
    if (!k) return;
    const v = await ask(k, { label: 'value', value, multiline: true });
    if (v == null) return;
    if (await act('kv.put', { component: cur, key: k, value: v }, 'put ' + k)) draw();
  }
}

// ---------- traces ----------
function pageTraces(ctx) {
  const f = { service: ctx.q.get('service') || store.get('tr.service', ''), operation: '', min_duration: '', lookback: store.get('tr.lookback', '1h'), limit: 50 };
  const svc = h('select', { onchange: e => { f.service = e.target.value; store.set('tr.service', f.service); search(); } }, h('option', { value: '' }, 'All services'));
  const op = h('input', { placeholder: 'operation', onchange: e => { f.operation = e.target.value; search(); } });
  const minD = h('input', { placeholder: 'min duration (100ms)', style: { width: '150px' }, onchange: e => { f.min_duration = e.target.value; search(); } });
  const lb = h('select', { onchange: e => { f.lookback = e.target.value; store.set('tr.lookback', f.lookback); search(); } }, ['15m', '1h', '6h', '24h', '7d'].map(v => h('option', { value: v, selected: v === f.lookback }, v)));
  const box = h('div', {}, loading());
  ctx.el.append(pageHead('Traces', 'Requests across services', ibtn('refresh', () => search(), 'Refresh')), h('div', { class: 'toolbar' }, svc, op, minD, lb), box);
  query('traces.services').then(fr => {
    for (const r of frameRows(fr)) { const n = Object.values(r)[0]; svc.append(h('option', { value: n, selected: n === f.service }, n)); }
  }, () => {});
  async function search() {
    box.replaceChildren(loading());
    let fr;
    try { fr = await query('traces.search', f); } catch (e) { box.replaceChildren(errBox(e)); return; }
    const rows = frameRows(fr), max = Math.max(1e-9, ...rows.map(r => r.duration));
    const t = dataTable({ onRow: r => openTrace(r.trace, r.root), empty: 'No traces in this window',
      columns: [{ key: 'root', label: 'Operation', render: r => h('span', {}, r.error && h('span', { class: 'red' }, '● '), r.root) }, { key: 'service', cls: 'muted' },
        { key: 'duration', num: true, render: r => h('span', { class: 'row', style: { justifyContent: 'flex-end' } }, h('span', { class: 'meter sm' }, h('i', { style: { width: 100 * r.duration / max + '%', background: r.error ? 'var(--red)' : '' } })), fmtMs(r.duration * 1000)) },
        { key: 'spans', num: true }, { key: 'start', field: { type: 'time' }, cls: 'muted' }] });
    t.update(rows);
    box.replaceChildren(t.el);
  }
  search();
}
async function openTrace(id, title) {
  const box = h('div', {}, loading());
  drawer(title || id, 'trace ' + id, [box]).classList.add('wide');
  let spans;
  try { spans = frameRows(await query('traces.get', { trace: id })); } catch (e) { box.replaceChildren(errBox(e)); return; }
  const t0 = Math.min(...spans.map(s => toDate(s.start).getTime())), end = Math.max(...spans.map(s => toDate(s.start).getTime() + s.duration * 1000)), total = Math.max(1, end - t0);
  const kids = new Map();
  spans.forEach(s => { const p = spans.some(x => x.id === s.parent) ? s.parent : ''; if (!kids.has(p)) kids.set(p, []); kids.get(p).push(s); });
  const order = [];
  const walk = (p, d) => (kids.get(p) || []).sort((a, b) => toDate(a.start) - toDate(b.start)).forEach(s => { order.push([s, d]); walk(s.id, d + 1); });
  walk('', 0);
  const detail = h('div', {});
  box.replaceChildren(h('div', { class: 'muted' }, `${spans.length} spans · ${fmtMs(total)}`), h('div', { class: 'wf' }, order.map(([s, d]) => {
    const x = (toDate(s.start).getTime() - t0) / total * 100, w = Math.max(0.3, s.duration * 1000 / total * 100);
    return h('div', { class: 'wf-row', onclick: () => detail.replaceChildren(h('dl', { class: 'kv' }, [['Span', s.name], ['Service', s.service], ['Duration', fmtMs(s.duration * 1000)], ['Tags', s.tags]].map(([k, v]) => [h('dt', {}, k), h('dd', { class: 'mono' }, v)]))) },
      h('div', { class: 'wf-name ellipsis', style: { paddingLeft: d * 12 + 'px' }, title: s.service + ' ' + s.name }, h('span', { class: 'dot', style: { background: colorOf(s.service) } }), s.name),
      h('div', { class: 'wf-track' }, h('i', { style: { left: x + '%', width: w + '%', background: s.error ? 'var(--red)' : colorOf(s.service) } }), h('span', { class: 'wf-d', style: { left: Math.min(88, x + w) + '%' } }, fmtMs(s.duration * 1000))));
  })), detail);
}

// ---------- load ----------
function pageLoad(ctx) {
  const box = h('div', { class: 'grid cards' }, loading());
  ctx.el.append(pageHead('Load', 'Load generators: rate, latency, errors', ibtn('refresh', () => load(), 'Refresh')), box);
  const doAct = (a, args, label) => act(a, args, label).then(() => load());
  const load = poller(ctx, 3000, async () => {
    let rows;
    try { rows = frameRows(await query('load.status')); } catch (e) { box.replaceChildren(errBox(e)); return; }
    if (!rows.length) { box.replaceChildren(emptyState('No load generator', 'A load component (k6, vegeta) shows here')); return; }
    box.replaceChildren(...rows.map(r => h('div', { class: 'card card-b col' },
      h('div', { class: 'row' }, h('b', {}, r.name), r.running ? badge('running', 'green', true) : badge('idle', ''), h('span', { class: 'spacer' }),
        r.running ? btn([icon('stop'), 'Stop'], () => doAct('load.stop', { component: r.name }, 'stop ' + r.name), 'sm') : btn([icon('play'), 'Start'], () => doAct('load.start', { component: r.name }, 'start ' + r.name), 'primary sm')),
      r.error && h('div', { class: 'err small' }, r.error),
      h('div', { class: 'stats-row' }, [['rate', fmtNum(r.rate) + '/s'], ['sent', fmtNum(r.sent)], ['failed', fmtNum(r.failed)], ['p50', fmtMs((r.p50 || 0) * 1000)], ['p95', fmtMs((r.p95 || 0) * 1000)], ['p99', fmtMs((r.p99 || 0) * 1000)]]
        .map(([k, v]) => h('div', {}, h('div', { class: 'k' }, k), h('div', { class: 'v' + (k === 'failed' && r.failed ? ' red' : '') }, v)))),
      h('div', { class: 'row' }, btn('Rate…', async () => { const v = await ask('Rate of ' + r.name, { label: 'requests per second', value: r.rate || '' }); if (v) doAct('load.rate', { component: r.name, rps: v }, 'rate ' + v); }, 'sm'),
        r.replicas != null && btn(`Replicas ${r.replicas}…`, async () => { const v = await ask('Replicas of ' + r.name, { value: r.replicas }); if (v) doAct('load.replicas', { component: r.name, replicas: +v }, 'replicas ' + v); }, 'sm')))));
  });
  load();
}

// ---------- hosts ----------
function pageHosts(ctx) {
  const box = h('div', {}, loading());
  ctx.el.append(pageHead('Hosts', 'Machines and nodes the services run on', ibtn('refresh', () => load(), 'Refresh')), box);
  const bar = (v, label) => v == null ? '' : h('span', { class: 'row', title: label }, h('span', { class: 'meter' }, h('i', { style: { width: Math.min(100, v * 100) + '%', background: v > 0.9 ? 'var(--red)' : v > 0.7 ? 'var(--amber)' : '' } })), h('span', { class: 'mono small' }, Math.round(v * 100) + '%'));
  const t = dataTable({ empty: 'No hosts', columns: [
    { key: 'host', render: r => h('span', {}, h('b', {}, r.host), r.addr && h('span', { class: 'muted small' }, ' ' + r.addr)) },
    { key: 'ready', render: r => r.ready === false ? badge('not ready', 'red', true) : badge('ready', 'green', true) },
    { key: 'roles', cls: 'muted' }, { key: 'cpu', render: r => bar(r.cpu, 'cpu') }, { key: 'mem', render: r => bar(r.mem, 'memory') }, { key: 'disk', render: r => bar(r.disk, 'disk') },
    { key: 'load1', num: true }, { key: 'pods', num: true }, { key: 'reason', cls: 'muted' }] });
  const load = poller(ctx, 5000, async () => {
    try { t.update(frameRows(await query('hosts.list'))); box.replaceChildren(t.el); } catch (e) { box.replaceChildren(errBox(e)); }
  });
  load();
}

// ---------- tests ----------
function pageTests(ctx) {
  let suites = [], cur = ctx.rest[0], runId = '';
  const left = h('div', {}, loading()), right = h('div', { class: 'grow' });
  ctx.el.append(pageHead('Tests', 'Test suites of rig.yaml and their last runs'), h('div', { class: 'split' }, left, right));
  const refresh = async () => {
    try { suites = frameRows(await query('tests.suites')); } catch (e) { left.replaceChildren(errBox(e)); return; }
    if (!suites.length) { left.replaceChildren(); right.replaceChildren(emptyState('No test suites', 'A tests: entry in rig.yaml shows here')); return; }
    if (!suites.some(s => s.suite === cur)) cur = suites[0].suite;
    left.replaceChildren(sideList(suites, cur, s => { cur = s.suite; runId = ''; refresh(); }, { key: 'suite', label: s => [s.suite, s.failed ? badge(s.failed + ' failed', 'red') : s.passed ? badge(s.passed + ' passed', 'green') : null], sub: s => s.help || s.packages }));
    drawRun();
  };
  async function drawRun() {
    const s = suites.find(x => x.suite === cur);
    const pat = h('input', { placeholder: '-run pattern (optional)', class: 'mono' });
    const res = h('div', {});
    right.replaceChildren(h('div', { class: 'toolbar' }, h('b', {}, s.suite), s.when && h('span', { class: 'muted' }, 'last ' + toDate(s.when).toLocaleString()), h('span', { class: 'spacer' }), pat,
      btn([icon('play'), 'Run'], async () => { const r = await act('tests.start', { suite: s.suite, run: pat.value }, 'test ' + s.suite); if (r) { runId = ''; refresh(); } }, 'primary')), res);
    try {
      const f = await query('tests.run', { suite: s.suite, id: runId });
      const t = dataTable({ empty: 'No run yet', onRow: r => r.output && drawer(r.test || r.package, r.status, [h('pre', { class: 'out', style: { maxHeight: 'none' } }, r.output)]),
        columns: [{ key: 'status', render: r => badge(r.status, { pass: 'green', fail: 'red', skip: 'amber' }[r.status] || '') }, { key: 'package', cls: 'mono muted' }, { key: 'test', cls: 'mono' }, { key: 'elapsed', num: true, render: r => fmtMs((r.elapsed || 0) * 1000) }] });
      t.update(frameRows(f).sort((a, b) => (a.status === 'fail' ? -1 : 0) - (b.status === 'fail' ? -1 : 0)));
      res.replaceChildren(f.meta?.notice ? h('div', { class: 'err' }, f.meta.notice) : '', h('div', { class: 'toolbar' }, searchBox('Filter', v => t.filter(v))), t.el);
    } catch (e) { res.replaceChildren(errBox(e)); }
  }
  refresh();
}

// ---------- manifests ----------
function pageManifests(ctx) {
  const box = h('div', {}, loading());
  ctx.el.append(pageHead('Manifests', 'Kubernetes objects of the manifest folders; click one for its YAML'), box);
  query('manifests').then(f => {
    const t = frameTable(f, { onRow: r => openManifest(r), empty: 'No manifests' });
    box.replaceChildren(f.meta?.notice ? h('div', { class: 'banner amber' }, f.meta.notice) : '', h('div', { class: 'toolbar' }, h('b', {}, frameRows(f).length + ' objects'), h('span', { class: 'spacer' }), searchBox('Filter', v => t.filter(v))), t.el);
  }, e => box.replaceChildren(errBox(e)));
}
function openManifest(r) {
  const out = h('pre', { class: 'out', style: { maxHeight: 'none' } });
  let live = false;
  const show = async () => {
    out.textContent = '…';
    try { out.textContent = frameRows(await query('manifests.get', { file: r.file, kind: r.kind, name: r.name, live }))[0]?.yaml || ''; } catch (e) { out.textContent = e.message; }
  };
  const seg = h('div', { class: 'seg' }, [['File', false], ['Live', true]].map(([l, v]) => h('button', { class: v === live ? 'on' : '', type: 'button', onclick: e => { live = v; seg.querySelectorAll('button').forEach(b => b.classList.toggle('on', b === e.target)); show(); } }, l)));
  drawer(r.kind + '/' + r.name, r.file, [h('div', { class: 'row' }, seg, h('span', { class: 'spacer' }), btn([icon('copy'), 'Copy'], () => copy(out.textContent), 'sm')), out]).classList.add('wide');
  show();
}

// ---------- plugins ----------
function pagePlugins(ctx) {
  const box = h('div', {}, loading());
  const verb = (v, n) => act('plugin.' + v, { names: [n] }, v + ' ' + n).then(load);
  ctx.el.append(pageHead('Plugins', 'Installed plugins and what they add',
    btn([icon('plus'), 'Add'], async () => { const n = await ask('Add a plugin', { label: 'name (from the registry) or a path' }); if (n) verb('add', n); }, 'primary')), box);
  async function load() {
    let rows;
    try { rows = frameRows(await query('plugins')); } catch (e) { box.replaceChildren(errBox(e)); return; }
    const views = S.views.reduce((m, v) => (v.plugin && (m[v.plugin] = [...(m[v.plugin] || []), v.title]), m), {});
    box.replaceChildren(h('div', { class: 'grid cards' }, rows.map(r => r.state === 'warn'
      ? h('div', { class: 'card card-b err' }, r.description)
      : h('div', { class: 'card card-b col' + (r.state === 'off' ? ' dim' : '') },
        h('div', { class: 'row' }, icon('plugin'), h('b', {}, r.name), h('span', { class: 'muted small' }, r.version), r.state === 'off' && badge('off'), h('span', { class: 'spacer' }), r.kind && badge(r.kind)),
        r.description && h('div', { class: 'muted' }, r.description),
        views[r.name] && h('div', { class: 'row wrap' }, views[r.name].map(t => h('a', { href: '#/v-' + slug(t) }, t))),
        r.permissions && h('div', { class: 'small muted mono' }, r.permissions),
        h('div', { class: 'row' }, h('span', { class: 'spacer' }),
          btn(r.state === 'off' ? 'Turn on' : 'Turn off', () => verb(r.state === 'off' ? 'on' : 'off', r.name), 'sm'),
          btn('Upgrade', () => verb('up', r.name), 'sm'), btn('Logs', () => runCommand('plugin logs ' + r.name), 'sm ghost'))))));
  }
  load();
}
