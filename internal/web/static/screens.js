'use strict';

// ---------- services: grouped by rig.yaml's sections ----------
function stateBadge(st, svc) {
  if (svc && pending.has(svc)) return badge(pending.get(svc), 'accent pulse', true);
  const cls = { running: 'green', starting: 'amber pulse', degraded: 'amber', failed: 'red' }[st] || '';
  return badge(st || 'unknown', cls, true);
}
const STATE_FILTERS = [['', 'All'], ['running', 'Running'], ['problem', 'Problems'], ['stopped', 'Stopped']];
// degraded still serves (some pods are up), so it counts as running and as a problem
const isUp = r => r.state === 'running' || r.state === 'degraded' || (r.ready ?? 0) > 0;
const inState = (r, f) => !f || (f === 'running' ? isUp(r) : f === 'problem' ? ['failed', 'degraded', 'starting'].includes(r.state) || (r.desired > 0 && r.ready < r.desired) : !isUp(r) && !['failed', 'starting'].includes(r.state));
const ago = t => { const d = toDate(t); if (!t || isNaN(d) || d.getFullYear() < 2000) return ''; const s = (Date.now() - d) / 1000; return s < 60 ? Math.round(s) + 's' : s < 3600 ? Math.round(s / 60) + 'm' : s < 86400 ? (s / 3600).toFixed(1) + 'h' : Math.round(s / 86400) + 'd'; };
function podDots(r) {
  const ins = r.instances || [];
  if (!ins.length) return '';
  const ready = ins.filter(i => i.ready).length, bad = ins.some(i => i.state === 'failed' || /crash|error|backoff/i.test(i.state));
  return h('span', { class: 'pods-n mono' + (ready === ins.length ? '' : bad ? ' bad' : ' wait'), title: ins.map(i => `${i.id} · ${i.state}${i.restarts ? ' · ' + i.restarts + ' restarts' : ''}${i.host ? ' · ' + i.host : ''}`).join('\n') },
    ready === ins.length ? String(ins.length) : `${ready}/${ins.length}`);
}
// requests, limits and the autoscaler only exist on Kubernetes
const K8S = () => S.info?.runtime === 'kubernetes';
// svcOps are what the TUI's service keys do, as menu items over one or more services.
function svcOps(names, row, after = () => {}) {
  const one = names.length === 1 ? names[0] : null, label = names.join(', ');
  const run = (a, args, l) => act(a, args, l).then(after);
  const scale = d => run('runtime.scale', { service: one, replicas: Math.max(0, (row?.desired ?? 1) + d) }, `scale ${one} to ${Math.max(0, (row?.desired ?? 1) + d)}`);
  return [
    one && { label: 'Open', icon: 'ext', key: 'enter', run: () => openService(one) },
    { label: 'Start', icon: 'play', key: 's', run: () => run('env.up', { targets: names }, 'start ' + label) },
    { label: 'Stop', icon: 'stop', key: 'x', run: () => run('env.down', { targets: names }, 'stop ' + label) },
    { label: 'Restart', icon: 'restart', key: 'r', run: () => Promise.all(names.map(n => act('runtime.restart', { service: n }, 'restart ' + n))).then(after) },
    { label: 'Logs', icon: 'logs', key: 'l', run: () => go('logs', { svc: names.join(',') }) },
    one && '-',
    one && { label: 'Scale up', icon: 'plus', key: '+', run: () => scale(1) },
    one && { label: 'Scale down', key: '-', run: () => scale(-1), disabled: (row?.desired ?? 0) < 1 },
    one && { label: 'Scale to…', run: async () => { const v = await ask('Replicas of ' + one, { value: row?.desired ?? 1 }); if (v != null && v !== '') run('runtime.scale', { service: one, replicas: +v }, `scale ${one} to ${v}`); } },
    one && { label: 'Env vars', key: '$', run: () => openService(one, 'env') },
    one && K8S() && { label: 'Requests and limits', key: 'R', run: () => openService(one, 'resources') },
    one && K8S() && { label: 'Autoscaler', key: 'h', run: () => openService(one, 'autoscale') },
    one && { label: 'Shell', icon: 'term', key: 'e', run: () => openTerminal(one, 'shell in ' + one, { kind: 'service', service: one }) },
    '-',
    { label: 'Deploy an image tag…', key: 'd', run: async () => { const t = await ask('Deploy ' + label, { label: 'image tag in the registry', placeholder: 'master-20261003' }); if (t) cliRun(`deploy ${names.join(' ')} -t ${t}`); } },
    { label: 'Build, push and deploy', key: 'b', run: async () => { if (await confirmBox('Build ' + label, 'Build from the working tree, push and roll out.', 'Build')) cliRun(`deploy ${names.join(' ')} --build`); } },
    { label: 'Metrics', icon: 'metrics', key: 'm', run: () => go('metrics') },
    { label: 'Manifests', key: 'F', run: () => go('manifests') },
    { label: 'Kill what holds its ports', key: 'K', danger: true, run: () => run('runtime.kill-ports', { services: names }, 'kill ports of ' + label) },
  ];
}
function pageServices(ctx) {
  const marked = new Set(), closed = new Set(store.get('svc.closed', []));
  let rows = [], cursor = null, filterText = '', stateF = ctx.q.get('state') || '';
  const sections = [...(S.info.sections || []), { name: 'infra', help: 'databases, brokers and the rest services need' }, { name: 'other', help: '' }];
  const body = h('div', { class: 'svc-groups' }, loading());
  const summary = h('div', { class: 'row wrap svc-sum' });
  const bulk = h('div', { class: 'bulk', hidden: true });
  const tables = new Map();
  const reload = () => load();
  const rowOf = n => rows.find(r => r.service === n);
  let flat = store.get('svc.flat', false);
  const visible = () => flat ? tables.get('__all')?.visible || [] : sections.flatMap(s => closed.has(s.name) && !filterText ? [] : tables.get(s.name)?.visible || []);
  const targets = () => marked.size ? [...marked] : cursor ? [cursor] : [];
  const ops = () => svcOps(targets(), rowOf(targets()[0]), reload);
  const columns = [
    { check: true, onchange: () => drawBulk() },
    { key: 'service', label: 'Service', render: r => h('span', { class: 'svc-name' }, r.service, r.role && !['service', 'app'].includes(r.role) && h('span', { class: 'muted small' }, ' ' + r.role)) },
    { key: 'state', label: 'State', render: r => h('span', { title: r.message || '' }, stateBadge(r.state, r.service)) },
    { key: 'ready', label: 'Ready', num: true, sortBy: r => r.ready ?? 0, render: r => h('span', { class: r.desired > 0 && r.ready < r.desired ? 'amber' : 'muted' }, `${r.ready ?? 0}/${r.desired ?? 0}`) },
    { key: 'pods', label: 'Pods', sort: false, render: podDots },
    { key: 'restarts', label: 'Restarts', num: true, render: r => h('span', { class: !r.restarts ? 'dim' : r.restarts > 3 ? 'red' : 'amber' }, r.restarts || 0) },
    { key: 'image', label: 'Image', cls: 'mono muted', render: r => r.message && !isUp(r) && r.state !== 'stopped' ? h('span', { class: 'amber', title: r.message }, firstLine(r.message)) : h('span', { title: r.image }, shortImage(r.image)) },
    { act: true, label: '', render: r => h('div', { class: 'row hover-acts' },
      ibtn('play', () => act('env.up', { targets: [r.service] }, 'start ' + r.service).then(reload), 'Start (s)', 'sm'), ibtn('stop', () => act('env.down', { targets: [r.service] }, 'stop ' + r.service).then(reload), 'Stop (x)', 'sm'),
      ibtn('restart', () => act('runtime.restart', { service: r.service }, 'restart ' + r.service).then(reload), 'Restart (r)', 'sm'), ibtn('logs', () => go('logs', { svc: r.service }), 'Logs (l)', 'sm')) },
  ];
  const flatColumns = [columns[0], columns[1], { key: 'section', label: 'Section', cls: 'muted', render: r => r.section || 'other' }, ...columns.slice(2)];
  function drawBulk() {
    bulk.hidden = !marked.size;
    const items = svcOps([...marked], null, reload).filter(x => x && x !== '-' && ['s', 'x', 'r', 'l', 'd', 'b'].includes(x.key));
    bulk.replaceChildren(h('b', {}, marked.size + ' marked'), h('span', { class: 'spacer' }), ...items.map(x => btn([x.icon && icon(x.icon), x.label.replace('…', '')], x.run, 'sm')),
      btn('Clear', () => { marked.clear(); tables.forEach(t => t.redraw()); drawBulk(); }, 'ghost sm'));
  }
  function draw() {
    const shown = rows.filter(r => inState(r, stateF));
    S.statusSection = Object.fromEntries(rows.map(r => [r.service, r.section]));
    summary.replaceChildren(...STATE_FILTERS.map(([v, l]) => h('button', { class: 'chip' + (stateF === v ? ' on' : ''), type: 'button', onclick: () => { stateF = v; history.replaceState(null, '', '#/services' + (v ? '?state=' + v : '')); draw(); } },
      v && h('span', { class: 'dot', style: { background: { running: 'var(--green)', problem: 'var(--amber)', stopped: 'var(--dim)' }[v] } }), l)));
    const groups = flat ? (shown.length ? [{ name: '__all', rows: shown }] : []) : sections.map(s => ({ ...s, rows: shown.filter(r => (r.section || 'other') === s.name) })).filter(g => g.rows.length);
    const els = groups.map(g => {
      let t = tables.get(g.name);
      if (!t) {
        t = dataTable({ columns: flat ? flatColumns : columns, key: flat ? 'svc.flat' : 'svc', rowKey: r => r.service, selected: marked, onRow: (r, e) => { cursor = r.service; mark(); if (e?.key === 'Enter' || (e?.type === 'click' && matchMedia('(max-width: 900px)').matches && !e.target.closest('button,input,a'))) openService(r.service); },
          menu: r => { cursor = r.service; mark(); return svcOps(marked.size && marked.has(r.service) ? [...marked] : [r.service], r, reload); }, rowClass: r => r.service === cursor ? 'cur' : '' });
        t.el.addEventListener('dblclick', e => { const tr = e.target.closest('tbody tr'); const r = tr && t.visible[[...tr.parentNode.children].indexOf(tr)]; if (r && !e.target.closest('button,input')) openService(r.service); });
        tables.set(g.name, t);
      }
      t.update(g.rows);
      t.filter(filterText);
      if (flat) return h('section', { class: 'svc-group' }, t.el);
      const up = g.rows.filter(isUp).length, isClosed = closed.has(g.name) && !filterText;
      const names = g.rows.map(r => r.service);
      return h('section', { class: 'svc-group' + (isClosed ? ' closed' : ''), hidden: !!filterText && !t.visible.length },
        h('div', { class: 'svc-gh' },
          h('button', { class: 'svc-toggle', type: 'button', onclick: () => { closed.has(g.name) ? closed.delete(g.name) : closed.add(g.name); store.set('svc.closed', [...closed]); draw(); } },
            icon('chev', 'chev'), h('b', {}, g.name), g.help && h('span', { class: 'muted ellipsis' }, g.help)),
          h('span', { class: 'spacer' }),
          h('span', { class: 'meter', title: `${up} of ${g.rows.length} running` }, h('i', { style: { width: 100 * up / g.rows.length + '%' } })),
          h('span', { class: 'muted small mono' }, `${up}/${g.rows.length}`),
          h('div', { class: 'row hover-acts' }, ibtn('play', () => act('env.up', { targets: names }, 'start ' + g.name).then(reload), 'Start the section', 'sm'), ibtn('stop', () => act('env.down', { targets: names }, 'stop ' + g.name).then(reload), 'Stop the section', 'sm'), ibtn('logs', () => go('logs', { svc: names.join(',') }), 'The section\'s logs', 'sm'))),
        !isClosed && t.el);
    });
    body.replaceChildren(...els, ...(els.length ? [] : [emptyState(rows.length ? 'No service matches' : 'No services in this environment')]));
    mark();
  }
  const mark = () => tables.forEach(t => t.el.querySelectorAll('tbody tr').forEach((tr, i) => tr.classList.toggle('cur', t.visible[i]?.service === cursor)));
  const load = poller(ctx, 3000, async () => {
    try { rows = frameRows(await query('services.status')); } catch (e) { body.replaceChildren(errBox(e)); return; }
    draw();
  });
  ctx.el.append(
    pageHead('Services', `${S.info.project} on ${S.info.env} · right-click or . for a service's actions`,
      searchBox('Filter services…', v => { filterText = v; draw(); }),
      btn([icon('up'), 'Up'], () => act('env.up', { targets: filterText ? visible().map(r => r.service) : [] }, 'up').then(reload), 'primary', 'Deploy services and their dependencies'),
      btn([icon('down'), 'Down'], () => act('env.down', { targets: filterText ? visible().map(r => r.service) : [] }, 'down').then(reload), '', 'Stop services in reverse dependency order'),
      ibtn('refresh', reload, 'Refresh')),
    h('div', { class: 'toolbar' }, summary, h('span', { class: 'spacer' }),
      h('div', { class: 'subtabs' }, [[false, 'By section'], [true, 'All at once']].map(([v, l]) => h('button', { class: flat === v ? 'on' : '', type: 'button', onclick: e => { flat = v; store.set('svc.flat', v); e.currentTarget.parentElement.querySelectorAll('button').forEach(b => b.classList.toggle('on', b === e.currentTarget)); draw(); } }, l)))),
    body, bulk);
  const repaint = () => tables.forEach(t => t.redraw());
  document.addEventListener('rig:pending', repaint);
  ctx.onCleanup(() => document.removeEventListener('rig:pending', repaint));
  load();
  ctx.menu(() => targets().length ? ops().filter(Boolean) : [
    { key: 'u', label: 'Up everything', run: () => act('env.up', { targets: [] }, 'up').then(reload) }, { key: 'D', label: 'Down everything', run: () => act('env.down', { targets: [] }, 'down').then(reload) }]);
  ctx.onKey(e => {
    const vis = visible(), i = vis.findIndex(r => r.service === cursor);
    const moveTo = r => { cursor = r?.service ?? cursor; mark(); document.querySelector('.svc-groups tr.cur')?.scrollIntoView({ block: 'nearest' }); };
    switch (e.key) {
      case 'ArrowDown': case 'j': moveTo(vis[Math.min(vis.length - 1, i + 1)]); return true;
      case 'ArrowUp': case 'k': moveTo(vis[Math.max(0, i - 1)]); return true;
      case 'Home': case 'g': moveTo(vis[0]); return true;
      case 'End': case 'G': moveTo(vis.at(-1)); return true;
      case ' ': if (cursor) { marked.has(cursor) ? marked.delete(cursor) : marked.add(cursor); tables.forEach(t => t.redraw()); mark(); drawBulk(); } return true;
      case 'Escape': if (marked.size) { marked.clear(); tables.forEach(t => t.redraw()); drawBulk(); return true; } return false;
    }
    if (!targets().length) return false;
    const hit = ops().find(x => x && x.key && (x.key === e.key || (x.key === 'enter' && e.key === 'Enter')));
    if (hit && !hit.disabled) { hit.run(); return true; }
    return false;
  });
}
// brief is a spec struct's set fields as k: v, for a glance.
const brief = o => o && Object.entries(o).filter(([, v]) => v != null && v !== '' && v !== false && !(Array.isArray(v) && !v.length)).map(([k, v]) => `${k.toLowerCase()}: ${typeof v === 'object' ? JSON.stringify(v) : v}`).join(' · ');
// openService is the TUI's service view: its pods, env vars, requests and limits, autoscaler and log.
let svcDrawer = null;
function openService(name, tab = 'pods') {
  if (svcDrawer?.isConnected) svcDrawer.close();
  const sp = specOf('service', name) || {};
  const ctrl = new AbortController();
  let row = null;
  const status = h('div', {});
  const pane = h('div', { class: 'svc-pane' });
  const ports = Object.entries(sp.Ports || {});
  const dd = (k, v) => v != null && v !== '' && (Array.isArray(v) ? v.length : true) ? [h('dt', {}, k), h('dd', {}, v)] : null;
  const instCols = [
    { key: 'id', label: 'Instance', cls: 'mono' }, { key: 'state', render: i => stateBadge(i.ready ? 'running' : i.state) }, { key: 'ready', render: i => i.ready ? '✓' : '✕' },
    { key: 'restarts', num: true, render: i => i.restarts ? h('span', { class: 'amber' }, i.restarts) : '0' }, { key: 'started', label: 'Age', render: i => ago(i.started), sortBy: i => -toDate(i.started) },
    { key: 'host', cls: 'muted' }, { key: 'ip', cls: 'mono muted' }, { key: 'cpu', num: true }, { key: 'memory', num: true }];
  const instOps = i => [
    { label: 'Its log', icon: 'logs', key: 'enter', run: () => { closeAll(); go('logs', { svc: name, i: i.id }); } },
    { label: 'Shell', icon: 'term', key: 'e', run: () => openTerminal(i.id, 'shell in ' + name, { kind: 'service', service: name, instance: i.id }) },
    { label: 'Copy the name', icon: 'copy', key: 'y', run: () => copy(i.id) }];
  const inst = dataTable({ key: 'svc.inst', columns: instCols, empty: 'No instances: the service is not running', onRow: (i, e) => { if (e?.key === 'Enter' || e?.detail === 2) instOps(i)[0].run(); }, menu: i => instOps(i) });
  const tabs = [['pods', 'Pods'], ['env', 'Env vars'], ['resources', 'Requests and limits'], ['autoscale', 'Autoscaler'], ['log', 'Log'], ['spec', 'Spec']].filter(t => K8S() || !['resources', 'autoscale'].includes(t[0]));
  const seg = h('div', { class: 'seg subtabs' });
  const drawSeg = () => seg.replaceChildren(...tabs.map(([id, l]) => h('button', { class: id === tab ? 'on' : '', type: 'button', onclick: () => { tab = id; drawSeg(); drawPane(); } }, l)));
  async function loadStatus() {
    try {
      row = frameRows(await query('services.status', { services: [name] }, ctrl.signal))[0];
      if (!row) return;
      status.replaceChildren(h('div', { class: 'row wrap svc-facts' }, stateBadge(row.state, name), h('span', {}, h('b', {}, `${row.ready ?? 0}/${row.desired ?? 0}`), ' ready'),
        row.restarts ? h('span', { class: 'amber' }, row.restarts + ' restarts') : null, row.autoscale && h('span', { class: 'muted' }, `autoscale ${row.autoscale.min}-${row.autoscale.max}`),
        h('span', { class: 'mono muted ellipsis', title: row.image }, row.image), row.message && h('span', { class: 'muted ellipsis' }, firstLine(row.message))));
      inst.update(row.instances || []);
    } catch (e) { if (e.name !== 'AbortError') status.replaceChildren(h('div', { class: 'err' }, e.message)); }
  }
  let lv = null;
  async function drawPane() {
    if (tab !== 'log') { ctrl.logs?.abort(); lv = null; }
    if (tab === 'pods') { pane.replaceChildren(h('div', { class: 'muted small' }, 'enter or dbl-click: the instance\'s log · right-click: shell and more'), inst.el); return; }
    if (tab === 'spec') {
      pane.replaceChildren(h('dl', { class: 'kv' },
        dd('Ports', ports.length ? h('span', { class: 'row wrap' }, ports.map(([k, p]) => h('a', { href: `http://localhost:${p}/`, target: '_blank', rel: 'noopener' }, `${k} ${p}`))) : null),
        dd('Depends on', (sp.DependsOn || []).length ? h('span', { class: 'row wrap' }, sp.DependsOn.map(d => h('a', { href: '#', onclick: e => { e.preventDefault(); openService(d); } }, d))) : null),
        dd('Section', row?.section), dd('Health', brief(sp.Health)), dd('Build', brief(sp.Build))), jsonViewer(sp));
      return;
    }
    if (tab === 'log') {
      const box = h('div', { class: 'logs' });
      pane.replaceChildren(h('div', { class: 'row' }, h('span', { class: 'spacer' }), btn([icon('ext'), 'Open in Logs'], () => { closeAll(); go('logs', { svc: name }); }, 'sm')), box);
      lv = logView(box, { showService: false });
      const c = ctrl.logs = new AbortController();
      stream('services.logs', { services: [name], tail: 200 }, b => lv?.push(frameRows(b.frame)), c.signal)
        .catch(e => { if (e.name !== 'AbortError') box.append(h('div', { class: 'ln err' }, h('span', { class: 'msg' }, e.message))); });
      return;
    }
    pane.replaceChildren(loading());
    try {
      if (tab === 'env') pane.replaceChildren(await envEditor(name, ctrl.signal, loadStatus));
      else if (tab === 'resources') pane.replaceChildren(await resourcesEditor(name, ctrl.signal));
      else pane.replaceChildren(autoscaleEditor(name, row?.autoscale, loadStatus));
    } catch (e) { if (e.name !== 'AbortError') pane.replaceChildren(errBox(e)); }
  }
  const timer = setInterval(() => !document.hidden && loadStatus(), 3000);
  document.addEventListener('rig:pending', loadStatus);
  const d = drawer(name, sp.Help || sp.Description || 'service', [
    h('div', { class: 'row wrap' }, svcOps([name], null, loadStatus).filter(x => x && x !== '-' && ['s', 'x', 'r', 'e', '+', '-'].includes(x.key))
      .map(x => btn([x.icon && icon(x.icon), x.label], () => { const it = svcOps([name], row, loadStatus).find(y => y && y.key === x.key); it?.run(); }, x.key === 's' ? 'primary sm' : 'sm')),
    h('span', { class: 'spacer' }), btn('More…', e => ctxMenu(e.currentTarget, svcOps([name], row, loadStatus).filter(x => x && x.key !== 'enter')), 'sm')),
    status, seg, pane,
  ], () => { clearInterval(timer); document.removeEventListener('rig:pending', loadStatus); ctrl.abort(); ctrl.logs?.abort(); });
  svcDrawer = d;
  d.classList.add('wide');
  d.addEventListener('keydown', e => {
    if (e.target.closest('input,textarea,select')) return;
    if (e.ctrlKey && (e.key === 'ArrowRight' || e.key === 'ArrowLeft')) { const i = tabs.findIndex(t => t[0] === tab); tab = tabs[(i + (e.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length][0]; drawSeg(); drawPane(); e.preventDefault(); return; }
    if (tab === 'pods' && inst.keys(e)) { e.preventDefault(); e.stopPropagation(); }
    else if (tab === 'log' && lv?.keys(e)) { e.preventDefault(); e.stopPropagation(); }
  });
  d.tabIndex = -1;
  drawSeg();
  loadStatus().then(drawPane);
  setTimeout(() => d.focus(), 30);
}
async function envEditor(name, signal, after) {
  const rows = frameRows(await query('services.env', { service: name }, signal));
  const items = rows.map(r => ({ name: r.name, value: r.value, was: r.value, over: r.overridden }));
  const tbody = h('tbody');
  let filter = '';
  const draw = () => tbody.replaceChildren(...rankBy(items.filter(it => !it.gone), filter, it => it.name, it => it.value).map(it => h('tr', { class: it.value !== it.was ? 'changed' : '' },
    h('td', {}, it.isNew ? h('input', { class: 'mono', value: it.name, placeholder: 'NAME', oninput: e => { it.name = e.target.value; } }) : h('span', { class: 'mono' }, it.name, it.over && h('span', { class: 'muted small' }, ' overridden'))),
    h('td', { class: 'grow' }, h('input', { class: 'mono', value: it.value, style: { width: '100%' }, oninput: e => { it.value = e.target.value; e.target.closest('tr').classList.toggle('changed', it.value !== it.was); } })),
    h('td', {}, ibtn('trash', () => { it.gone = true; draw(); }, 'Remove', 'sm')))));
  draw();
  const save = async () => {
    const env = {};
    for (const it of items) {
      if (!it.name) continue;
      if (it.gone) { if (!it.isNew) env[it.name] = ''; } else if (it.value !== it.was || it.isNew) env[it.name] = it.value;
    }
    if (!Object.keys(env).length) { toast('Nothing changed'); return; }
    if (await act('runtime.set-env', { service: name, env }, 'env of ' + name)) { after?.(); }
  };
  return h('div', { class: 'col' },
    h('div', { class: 'toolbar' }, searchBox('Filter variables', v => { filter = v; draw(); }), h('span', { class: 'muted small' }, rows.length + ' variables'), h('span', { class: 'spacer' }),
      btn([icon('plus'), 'Add'], () => { items.unshift({ name: '', value: '', was: '', isNew: true }); draw(); tbody.querySelector('input')?.focus(); }, 'sm'), btn('Save and redeploy', save, 'primary sm')),
    h('div', { class: 'table-wrap' }, h('table', { class: 'env-t' }, h('thead', {}, h('tr', {}, h('th', {}, 'Name'), h('th', {}, 'Value'), h('th'))), tbody)));
}
async function resourcesEditor(name, signal) {
  const rows = frameRows(await query('services.resources', { service: name }, signal));
  if (!rows.length) return emptyState('No containers', 'This runtime does not report requests and limits');
  const keys = [['cpu_request', 'CPU request', '100m'], ['cpu_limit', 'CPU limit', '500m'], ['mem_request', 'Memory request', '128Mi'], ['mem_limit', 'Memory limit', '512Mi']];
  return h('div', { class: 'col' }, rows.map(r => {
    const inputs = Object.fromEntries(keys.map(([k, , ph]) => [k, h('input', { class: 'mono', value: r[k] || '', placeholder: ph })]));
    return h('div', { class: 'card card-b col' }, h('div', { class: 'row' }, h('b', {}, r.container), h('span', { class: 'spacer' }),
      btn('Apply', () => act('runtime.set-resources', { service: name, container: r.container, ...Object.fromEntries(keys.map(([k]) => [k, inputs[k].value])) }, 'resources of ' + name), 'primary sm')),
    h('div', { class: 'params' }, keys.map(([k, l]) => h('label', { class: 'field' }, l, inputs[k]))));
  }), h('div', { class: 'muted small' }, 'Applying restarts the pods.'));
}
function autoscaleEditor(name, cur, after) {
  const f = Object.fromEntries([['min', 1], ['max', 3], ['cpu', ''], ['memory', '']].map(([k, d]) => [k, h('input', { type: 'number', min: 0, value: cur?.[k] || d, placeholder: k === 'cpu' || k === 'memory' ? 'target % (optional)' : '' })]));
  return h('div', { class: 'card card-b col' }, h('div', { class: 'muted' }, cur ? `The autoscaler keeps ${cur.min} to ${cur.max} replicas.` : 'No autoscaler yet: saving creates one on cpu or memory utilization.'),
    h('div', { class: 'params' }, [['min', 'Min replicas'], ['max', 'Max replicas'], ['cpu', 'CPU target %'], ['memory', 'Memory target %']].map(([k, l]) => h('label', { class: 'field' }, l, f[k]))),
    h('div', { class: 'row' }, h('span', { class: 'spacer' }), btn('Save', async () => { if (await act('runtime.autoscale', { service: name, ...Object.fromEntries(Object.entries(f).map(([k, i]) => [k, i.value])) }, 'autoscale ' + name)) after?.(); }, 'primary sm')));
}

// ---------- shared pieces ----------
// picker is a left list of names that remembers the last one picked per screen.
function sideList(items, cur, onPick, { label = x => x.name, sub = x => x.sub, key = 'name' } = {}) {
  return h('div', { class: 'card list side-list' }, items.map(x => h('div', { class: 'item' + (x[key] === cur ? ' sel' : ''), onclick: () => onPick(x) },
    h('div', { class: 'n' }, label(x)), sub(x) && h('div', { class: 'd' }, sub(x)))));
}
// resultTable is a frame with its row count, filter, copy buttons, inspect (enter, dbl-click) and a row menu.
function resultTable(f, opts = {}) {
  const rows = frameRows(f), cols = (f?.fields || []).map(c => c.name);
  let t;
  t = frameTable(f, { onRow: (r, e) => { if (e?.key === 'Enter') inspectRow(r); }, menu: (r, e) => rowMenu(r, cols, t, e), key: opts.key, ...opts });
  t.el.addEventListener('dblclick', e => { const tr = e.target.closest('tbody tr'); if (tr) inspectRow(t.visible[[...tr.parentNode.children].indexOf(tr)]); });
  const box = h('div', { class: 'result-box' }, h('div', { class: 'toolbar' }, h('b', {}, rows.length + ' rows'), f?.meta?.notice && h('span', { class: 'muted' }, f.meta.notice), h('span', { class: 'spacer' }),
    searchBox('Filter rows…', v => t.filter(v)), btn([icon('copy'), 'CSV'], () => copy(toCSV(t.visible, cols)), 'sm'), btn([icon('copy'), 'JSON'], () => copy(JSON.stringify(t.visible, null, 2)), 'sm')), t.el);
  box.table = t;
  return box;
}
const isJSON = v => { if (typeof v !== 'string' || !/^\s*[{[]/.test(v)) return false; try { JSON.parse(v); return true; } catch { return false; } };
const has = (c, cap) => String(c?.caps || '').split(' ').includes(cap);

// ---------- data: browse, query, queues ----------
function pageData(ctx) {
  let comps = [], cur = ctx.rest[0], path = ctx.rest.slice(1), tab = store.get('data.tab', 'browse');
  const left = h('div', { class: 'side-col' }, loading()), right = h('div', { class: 'grow' });
  let list = null;
  ctx.el.append(pageHead('Data', 'Databases, caches and brokers: browse, query, edit, peek · ctrl+← → switches the tab'), splitPane(left, right, { key: 'data.side', size: 260 }));
  const place = () => history.replaceState(null, '', '#/data' + (cur ? '/' + [cur, ...path].map(encodeURIComponent).join('/') : ''));
  query('components').then(f => {
    comps = frameRows(f).filter(c => /browse|query|queues|actions/.test(c.caps || ''));
    if (!comps.length) { left.replaceChildren(); right.replaceChildren(emptyState('No database, cache or broker', 'Components rig.yaml names show here')); return; }
    if (!comps.some(c => c.name === cur)) { cur = store.get('data.cur', comps[0].name); if (!comps.some(c => c.name === cur)) cur = comps[0].name; path = []; }
    drawLeft(); drawRight();
  }, e => left.replaceChildren(errBox(e)));
  const drawLeft = () => {
    list = groupedList({ groups: group(comps, c => c.kind || '', { title: k => k || 'other' }), key: 'data', cur, id: c => c.name, text: c => c.name + ' ' + c.type + ' ' + c.kind,
      render: c => [h('div', { class: 'n' }, h('span', { class: 'ellipsis' }, c.name), c.type && h('span', { class: 'tag' }, c.type)), h('div', { class: 'd' }, (c.caps || '').split(' ').filter(x => x !== 'inspect').join(' · '))],
      onPick: c => { cur = c.name; path = []; store.set('data.cur', cur); drawRight(); } });
    left.replaceChildren(list.el);
  };
  ctx.subTab = d => { const b = [...right.querySelectorAll('.subtabs button')], i = b.findIndex(x => x.classList.contains('on')); b[(i + d + b.length) % b.length]?.click(); };
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
      const t = frameTable(f, { key: 'data.nav', onRow: r => { path = [...path, String(r[first])]; drawRight(); }, empty: 'Empty' });
      cursorTable = t;
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
    const t = dataTable({ key: 'data.' + c.name + '.' + path.join('.'), columns: [editable && { check: true, onchange: () => del.hidden = !sel.size }, ...cols].filter(Boolean), rowKey: r => idx.get(r), selected: sel, empty: 'No rows',
      onRow: (r, e) => { if (e?.key === 'Enter') inspectRow(r, path.join('/')); }, menu: (r, e) => [...rowMenu(r, cols.map(x => x.key), t, e),
        editable && '-', editable && { label: 'Delete the row', icon: 'trash', danger: true, run: async () => { if (await confirmBox('Delete a row', [c.name, ...path].join('/'), 'Delete', true) && await act('data.delete', { component: c.name, path, rows: [String(idx.get(r))] }, 'delete a row')) drawRight(); } }] });
    cursorTable = t;
    t.el.addEventListener('dblclick', e => { if (editable) return; const tr = e.target.closest('tbody tr'); if (tr) inspectRow(t.visible[[...tr.parentNode.children].indexOf(tr)], path.join('/')); });
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
    const ta = h('textarea', { rows: 5, class: 'mono', placeholder: (c.language || 'query') + ' for ' + c.name + (path.length ? ' at ' + path.join('/') : '') + ' · ctrl+enter runs · tab takes the AI\'s completion · @? describe it', spellcheck: false }, store.get(key, ''));
    const ghost = aiAssist(ta, () => `a ${c.language || 'query'} for ${c.name} (${c.type})` + (path.length ? ' on ' + path.join('/') : ''));
    const res = h('div', {});
    const run = async () => {
      store.set(key, ta.value);
      res.replaceChildren(loading());
      try { res.replaceChildren(resultTable(await query('data.query', { component: c.name, query: ta.value, path }), { key: 'dq.' + c.name })); } catch (e) { res.replaceChildren(errBox(e)); }
    };
    ta.addEventListener('keydown', e => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); run(); } });
    pane.replaceChildren(h('div', { class: 'card card-b col' }, ta, ghost, h('div', { class: 'row' }, path.length > 0 && h('span', { class: 'muted' }, 'at ' + path.join('/')), h('span', { class: 'spacer' }),
      has(c, 'suggest') && path.length > 0 && btn('Suggest', async () => { try { ta.value = frameRows(await query('data.suggest', { component: c.name, path }))[0]?.query || ta.value; } catch (e) { toast(e.message, 'bad'); } }, 'sm'),
      btn([icon('play'), 'Run'], run, 'primary'))), res);
    ta.focus();
  }
  async function queues(pane, c) {
    pane.replaceChildren(loading());
    let f;
    try { f = await query('data.queues', { component: c.name }); } catch (e) { pane.replaceChildren(errBox(e)); return; }
    const t = frameTable(f, { key: 'data.queues', onRow: r => peek(c, r.queue), empty: 'No queues', menu: r => [{ label: 'Peek', key: 'enter', run: () => peek(c, r.queue) }, { label: 'Purge', icon: 'trash', danger: true, run: async () => { if (await act('data.purge', { component: c.name, queue: r.queue }, 'purge ' + r.queue)) drawRight(); } }] });
    cursorTable = t;
    pane.replaceChildren(h('div', { class: 'toolbar' }, h('b', {}, frameRows(f).length + ' queues'), h('span', { class: 'muted' }, 'click a queue to peek'), h('span', { class: 'spacer' }), searchBox('Filter', v => t.filter(v)), ibtn('refresh', () => drawRight(), 'Refresh')), t.el);
  }
  function peek(c, q) {
    const box = h('div', {}, loading());
    drawer(q, c.name + ' · messages stay queued', [h('div', { class: 'row' }, btn([icon('trash'), 'Purge'], async () => { if (await act('data.purge', { component: c.name, queue: q }, 'purge ' + q)) { closeOverlay(); drawRight(); } }, 'danger sm'),
      h('span', { class: 'spacer' }), btn('Settings', async () => { try { box.replaceChildren(resultTable(await query('data.peek', { component: c.name, queue: q, detail: 'info' }))); } catch (e) { box.replaceChildren(errBox(e)); } }, 'sm')), box]).classList.add('wide');
    query('data.peek', { component: c.name, queue: q, n: 50 }).then(f => box.replaceChildren(resultTable(f)), e => box.replaceChildren(errBox(e)));
  }
  let cursorTable = null;
  ctx.onKey(e => {
    if ((e.key === 'Backspace' || e.key === 'Escape') && path.length && tab === 'browse') { path = path.slice(0, -1); drawRight(); return true; }
    if (cursorTable?.el.isConnected && cursorTable.keys(e)) return true;
    return list?.keys(e) || false;
  });
}
// compActions lists what an adapter can do beyond the screens (rig do), each with its arguments and last result in place.
async function compActions(pane, c) {
  pane.replaceChildren(loading());
  let rows;
  try { rows = frameRows(await query('component.actions', { component: c.name })); } catch (e) { pane.replaceChildren(errBox(e)); return; }
  if (!rows.length) { pane.replaceChildren(emptyState('No actions', c.name + ' offers none')); return; }
  let filter = '';
  const list = h('div', { class: 'col acts' });
  const draw = () => list.replaceChildren(...group(rankBy(rows, filter, r => r.name, r => r.help), r => r.mutate ? 'changes' : 'reads', { sort: false })
    .sort((a, b) => a.id.localeCompare(b.id)).map(g => h('section', { class: 'card' }, h('div', { class: 'card-h' }, g.id === 'reads' ? 'Reads' : 'Changes', h('span', { class: 'muted small', style: { fontWeight: 400 } }, g.items.length)),
      h('div', { class: 'act-list' }, g.items.map(r => {
        const args = h('input', { class: 'mono', placeholder: 'arguments (optional)', onkeydown: e => { if (e.key === 'Enter') run(); } });
        const out = h('div', {});
        const run = async () => {
          out.replaceChildren(loading());
          const res = await act('component.do', { component: c.name, action: r.name, args: args.value.split(/\s+/).filter(Boolean) }, c.name + ' ' + r.name);
          out.replaceChildren(res ? runResult(res) : '');
        };
        return h('div', { class: 'act-row' }, h('div', { class: 'row' }, h('div', { class: 'grow' }, h('div', { class: 'mono' }, h('b', {}, r.name), r.mutate && badge('changes', 'amber')), r.help && h('div', { class: 'muted small' }, r.help)),
          args, btn([icon('play'), 'Run'], run, r.mutate ? 'sm' : 'primary sm')), out);
      })))));
  pane.replaceChildren(h('div', { class: 'toolbar' }, searchBox('Filter actions', v => { filter = v; draw(); }), h('span', { class: 'muted small' }, rows.length + ' actions')), list);
  draw();
}

// ---------- kv ----------
function pageKV(ctx) {
  let comps = [], cur = ctx.rest[0], prefix = ctx.q.get('prefix') || '';
  const left = h('div', { class: 'side-col' }, loading()), right = h('div', { class: 'grow' });
  let table = null;
  ctx.el.append(pageHead('KV', 'Config stores: keys under a prefix · filter matches keys and values · enter edits'), splitPane(left, right, { key: 'kv.side', size: 220 }));
  query('components').then(f => {
    comps = frameRows(f).filter(c => c.kind === 'kv');
    if (!comps.length) { left.replaceChildren(); right.replaceChildren(emptyState('No config store', 'A kv component (consul, etcd) shows here')); return; }
    if (!comps.some(c => c.name === cur)) cur = comps[0].name;
    drawLeft(); draw();
  }, e => left.replaceChildren(errBox(e)));
  const drawLeft = () => left.replaceChildren(sideList(comps, cur, c => { cur = c.name; prefix = ''; drawLeft(); draw(); }, { sub: c => c.type }));
  ctx.onKey(e => table?.el.isConnected ? table.keys(e) : false);
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
    const t = table = dataTable({ key: 'kv', rowKey: r => r.key, selected: sel, onRow: (r, e) => { if (e?.key === 'Enter' || e?.detail === 2) edit(r); }, empty: 'No keys',
      menu: r => [{ label: 'Edit', icon: 'edit', key: 'enter', run: () => edit(r) }, { label: 'Copy the key', icon: 'copy', run: () => copy(r.key) }, { label: 'Copy the value', icon: 'copy', run: () => copy(r.value) },
        String(r.key).endsWith('/') && { label: 'List under it', run: () => { prefix = r.key; draw(); } },
        '-', { label: 'Delete', icon: 'trash', danger: true, run: async () => { if (await confirmBox('Delete ' + r.key, cur, 'Delete', true) && await act('kv.delete', { component: cur, keys: [r.key] }, 'delete ' + r.key)) draw(); } }],
      columns: [{ check: true, onchange: () => del.hidden = !sel.size }, { key: 'key', cls: 'mono' }, { key: 'value', cls: 'mono muted', render: r => [isJSON(r.value) && h('span', { class: 'tag' }, 'json'), ' ', isJSON(r.value) ? JSON.stringify(JSON.parse(r.value)) : firstLine(r.value)] }, { key: 'size', num: true }] });
    t.el.classList.add('kv-t');
    t.update(frameRows(f));
    box.replaceChildren(h('div', { class: 'toolbar' }, h('b', {}, t.visible.length + ' keys'), h('span', { class: 'muted' }, 'click a key to edit'), h('span', { class: 'spacer' }), del, searchBox('Filter', v => t.filter(v))), t.el);
  }
  // edit opens a value: JSON as an editable tree beside its raw text, anything else as text.
  function edit(r) {
    let v = r.value;
    const ta = h('textarea', { class: 'mono', rows: 16, spellcheck: false }, isJSON(v) ? JSON.stringify(JSON.parse(v), null, 2) : v);
    const treeBox = h('div', {});
    const drawTree = () => { if (!isJSON(ta.value)) { treeBox.replaceChildren(); return; } treeBox.replaceChildren(jsonViewer(JSON.parse(ta.value), { onEdit: val => { ta.value = JSON.stringify(val, null, 2); } })); };
    ta.addEventListener('input', debounce(drawTree, 300));
    drawTree();
    const save = async () => { if (await act('kv.put', { component: cur, key: r.key, value: isJSON(ta.value) && isJSON(v) && !/\n/.test(v) ? JSON.stringify(JSON.parse(ta.value)) : ta.value }, 'put ' + r.key)) { d.close(); draw(); } };
    const d = drawer(r.key, cur + ' · right-click a JSON field to edit, add or delete it', [h('div', { class: 'row' }, h('span', { class: 'spacer' }), btn([icon('copy'), 'Copy'], () => copy(ta.value), 'sm'), btn('Save', save, 'primary sm')), treeBox, h('label', { class: 'field' }, 'Value', ta)]);
    d.classList.add('wide');
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
// Each generator is polled on its own, so a slow one never holds the rest; history stays in the page for the charts.
function pageLoad(ctx) {
  let names = (frameRowsCache.components || []).filter(c => c.kind === 'load').map(c => c.name);
  const hist = new Map(), rowsBy = new Map();
  let detail = null;
  const doAct = (a, args, label) => act(a, args, label).then(() => poll(args.component));
  const ops = r => r && [
    r.running ? { label: 'Stop', icon: 'stop', key: 'x', run: () => doAct('load.stop', { component: r.name }, 'stop ' + r.name) } : { label: 'Start', icon: 'play', key: 's', run: () => doAct('load.start', { component: r.name }, 'start ' + r.name) },
    { label: 'Raise the rate', icon: 'plus', key: '+', run: () => doAct('load.rate', { component: r.name, rps: String(Math.round((r.rate || 0) * 1.25 + 1)) }, 'rate ' + r.name) },
    { label: 'Lower the rate', key: '-', run: () => doAct('load.rate', { component: r.name, rps: String(Math.max(0, Math.round((r.rate || 0) * 0.8))) }, 'rate ' + r.name) },
    { label: 'Set the rate…', key: 'r', run: async () => { const v = await ask('Rate of ' + r.name, { label: 'requests per second', value: r.rate || '' }); if (v) doAct('load.rate', { component: r.name, rps: v }, 'rate ' + v); } },
    r.replicas != null && { label: 'More instances', key: ']', run: () => doAct('load.replicas', { component: r.name, replicas: r.replicas + 1 }, 'instances ' + r.name) },
    r.replicas != null && { label: 'Fewer instances', key: '[', run: () => doAct('load.replicas', { component: r.name, replicas: Math.max(0, r.replicas - 1) }, 'instances ' + r.name) },
    r.replicas != null && { label: 'Set instances…', key: 'R', run: async () => { const v = await ask('Instances of ' + r.name, { value: r.replicas }); if (v) doAct('load.replicas', { component: r.name, replicas: +v }, 'instances ' + v); } },
    { label: 'Restart', icon: 'restart', key: 'b', run: async () => { await act('load.stop', { component: r.name }, 'stop ' + r.name); doAct('load.start', { component: r.name }, 'start ' + r.name); } },
    { label: 'Details', icon: 'ext', key: 'enter', run: () => openDetail(r.name) },
  ];
  const t = dataTable({ key: 'load', empty: 'No load generator', rowKey: r => r.name, onRow: (r, e) => { if (e?.type === 'click' || e?.key === 'Enter') openDetail(r.name); }, menu: r => ops(r), columns: [
    { key: 'name', render: r => h('b', {}, r.name) },
    { key: 'running', label: 'Status', render: r => r.error ? badge('error', 'red', true) : r.running ? badge('running', 'green pulse', true) : badge('idle', '') },
    { key: 'rate', label: 'Target', num: true, render: r => fmtNum(r.rate) + '/s' },
    { key: 'sentps', label: 'Sent', num: true, render: r => r.sentps == null ? '–' : fmtNum(r.sentps) + '/s' },
    { key: 'failed', num: true, render: r => r.failed ? h('span', { class: 'red' }, fmtNum(r.failed)) : '0' },
    { key: 'p95', num: true, render: r => fmtMs((r.p95 || 0) * 1000) },
    { key: 'replicas', label: 'Instances', num: true },
    { act: true, label: '', render: r => h('div', { class: 'row hover-acts' }, ...ops(r).filter(x => x && ['s', 'x'].includes(x.key)).map(x => ibtn(x.icon, e => { e.stopPropagation(); x.run(); }, x.label, 'sm')), ibtn('ext', e => { e.stopPropagation(); openDetail(r.name); }, 'Details', 'sm')) }] });
  ctx.el.append(pageHead('Load', 'Load generators and what they send · click one for its charts', searchBox('Filter', v => t.filter(v)), ibtn('refresh', () => pollAll(), 'Refresh')), t.el);

  function openDetail(name) {
    const r = rowsBy.get(name);
    if (!r) return;
    const stats = h('div', { class: 'stats-row' }), acts = h('div', { class: 'row wrap' });
    const rateBox = h('div', { class: 'load-chart' }), latBox = h('div', { class: 'load-chart' });
    const d = drawer(name, 'Load generator', [acts, stats, h('h4', { class: 'sec-h' }, 'Requests per second'), rateBox, h('h4', { class: 'sec-h' }, 'Latency'), latBox], () => { detail = null; });
    d.classList.add('wide');
    detail = { name, stats, acts, rateBox, latBox };
    drawDetail();
  }
  function drawDetail() {
    if (!detail) return;
    const r = rowsBy.get(detail.name), hs = hist.get(detail.name) || [];
    if (!r) return;
    detail.acts.replaceChildren(r.running ? badge('running', 'green pulse', true) : badge('idle', ''), r.replicas != null && h('span', { class: 'muted small' }, r.replicas + ' instances'), h('span', { class: 'spacer' }),
      ...ops(r).filter(x => x && !['enter', 'b'].includes(x.key)).map(x => btn([x.icon && icon(x.icon), x.label], () => x.run(), x.key === 's' ? 'primary sm' : 'sm')));
    detail.stats.replaceChildren(...[['target', fmtNum(r.rate) + '/s'], ['sent', r.sentps == null ? fmtNum(r.sent) : fmtNum(r.sentps) + '/s'], ['failed', fmtNum(r.failed)], ['p50', fmtMs((r.p50 || 0) * 1000)], ['p95', fmtMs((r.p95 || 0) * 1000)], ['p99', fmtMs((r.p99 || 0) * 1000)]]
      .map(([k, v]) => h('div', {}, h('div', { class: 'k' }, k), h('div', { class: 'v' + (k === 'failed' && r.failed ? ' red' : '') }, v))), r.error ? h('div', { class: 'err small', style: { gridColumn: '1/-1' } }, r.error) : '');
    if (hs.length < 2) return;
    const s = (n, f) => ({ name: n, pts: hs.filter(x => f(x) != null).map(x => [x.t, f(x)]) });
    lineChart(detail.rateBox, [s('sent', x => x.rps), s('failed', x => x.fps), s('target', () => r.rate || 0)], { unit: '/s', fit: true, legend: 'auto', memo: detail.rateBox });
    lineChart(detail.latBox, [s('p50', x => x.p50), s('p95', x => x.p95), s('p99', x => x.p99)], { unit: 's', fit: true, legend: 'auto', memo: detail.latBox });
  }
  function record(r) {
    const hs = hist.get(r.name) || []; hist.set(r.name, hs);
    const now = Date.now(), prev = hs.at(-1), dt = prev ? (now - prev.t) / 1000 : 0;
    const rps = dt > 0 ? Math.max(0, (r.sent - prev.sent) / dt) : null, fps = dt > 0 ? Math.max(0, (r.failed - prev.failed) / dt) : null;
    hs.push({ t: now, sent: r.sent || 0, failed: r.failed || 0, rps, fps, p50: r.p50 || 0, p95: r.p95 || 0, p99: r.p99 || 0 });
    if (hs.length > 600) hs.shift();
    rowsBy.set(r.name, { ...r, sentps: rps });
  }
  const busy = new Set();
  async function poll(name) {
    if (busy.has(name)) return;
    busy.add(name);
    try {
      const rows = frameRows(await query('load.status', name ? { component: name } : {}));
      if (!name) names = rows.map(r => r.name);
      rows.forEach(record);
      t.update(names.map(n => rowsBy.get(n)).filter(Boolean));
      drawDetail();
    } catch (e) { if (!rowsBy.size) t.el.replaceChildren(errBox(e)); else toast(e.message, 'bad'); }
    finally { busy.delete(name); }
  }
  const pollAll = () => names.length ? names.forEach(poll) : poll('');
  pollAll();
  ctx.every(3000, pollAll);
  ctx.menu(() => ops(t.current()) || []);
  ctx.onKey(e => {
    if (t.keys(e)) return true;
    const hit = (ops(t.current()) || []).find(x => x && x.key === e.key);
    if (hit) { hit.run(); return true; }
    return false;
  });
}

// ---------- hosts ----------
// Enter opens a shell on the host, as the TUI does; c runs one command there, p lists its pods.
function pageHosts(ctx) {
  const box = h('div', {}, loading());
  const shell = r => openTerminal(r.host, 'shell on ' + r.host, { kind: 'host', host: r.host });
  const runCmd = async r => { const c = await ask('Run on ' + r.host, { label: 'command', placeholder: 'df -h' }); if (c) openTerminal(r.host, c, { kind: 'host', host: r.host, command: c }); };
  const pods = async r => {
    const list = frameRows(await query('hosts.pods', { host: r.host }));
    const mine = new Set(frameRows(await query('services.status')).flatMap(s => (s.instances || []).filter(i => i.host === r.host || i.host === r.addr).map(i => i.id)));
    const t = dataTable({ key: 'host.pods', empty: 'No pods on ' + r.host,
      columns: [{ key: 'pod', cls: 'mono', render: p => h('span', {}, p.pod, mine.has(p.pod) && badge('project', 'blue', true)) }, { key: 'namespace', cls: 'muted' },
        { key: 'cpu', num: true, render: p => p.cpu == null ? '' : p.cpu.toFixed(2) }, { key: 'mem', label: 'Memory', num: true, render: p => fmtNum(p.mem, 'bytes') }, { key: 'started', label: 'Age', render: p => ago(p.started) }] });
    t.update(list);
    const d = drawer(r.host, list.length + ' pods on this host · ' + mine.size + ' of ' + S.info.project, [searchBox('Filter pods', v => t.filter(v)), t.el]);
    d.classList.add('wide');
  };
  const ops = r => [{ label: 'Shell', icon: 'term', key: 'enter', run: () => shell(r) }, { label: 'Run a command…', key: 'c', run: () => runCmd(r) }, { label: 'Pods on it', icon: 'services', key: 'p', run: () => pods(r) },
    { label: 'Copy the name', icon: 'copy', key: 'y', run: () => copy(r.host) }];
  const bar = (v, label) => v == null ? '' : h('span', { class: 'row', title: label }, h('span', { class: 'meter' }, h('i', { style: { width: Math.min(100, v * 100) + '%', background: v > 0.9 ? 'var(--red)' : v > 0.7 ? 'var(--amber)' : '' } })), h('span', { class: 'mono small' }, Math.round(v * 100) + '%'));
  const t = dataTable({ key: 'hosts', empty: 'No hosts', onRow: (r, e) => { if (e?.key === 'Enter' || (e?.type === 'click' && e.detail === 1)) shell(r); }, menu: r => ops(r), columns: [
    { key: 'host', render: r => h('span', {}, h('b', {}, r.host), r.addr && h('span', { class: 'muted small' }, ' ' + r.addr)) },
    { key: 'ready', render: r => r.ready === false ? badge('not ready', 'red', true) : badge('ready', 'green', true) },
    { key: 'roles', cls: 'muted' }, { key: 'cpu', render: r => bar(r.cpu, 'cpu') }, { key: 'mem', render: r => bar(r.mem, 'memory') }, { key: 'disk', render: r => bar(r.disk, 'disk') },
    { key: 'load1', num: true }, { key: 'pods', num: true }, { key: 'reason', cls: 'muted' },
    { act: true, label: '', render: r => h('div', { class: 'row hover-acts' }, ibtn('term', () => shell(r), 'Shell (enter)', 'sm'), ibtn('play', () => runCmd(r), 'Run a command (c)', 'sm'), ibtn('services', () => pods(r), 'Pods (p)', 'sm')) }] });
  ctx.el.append(pageHead('Hosts', 'Machines and nodes the services run on · click or enter: a shell there · right-click: more', searchBox('Filter hosts', v => t.filter(v)), ibtn('refresh', () => load(), 'Refresh')), box);
  let first = true;
  const load = poller(ctx, 5000, async () => {
    try { t.update(frameRows(await query('hosts.list'))); if (first) { box.replaceChildren(t.el); first = false; } } catch (e) { box.replaceChildren(errBox(e)); first = true; }
  });
  load();
  ctx.menu(() => t.current() ? ops(t.current()) : []);
  ctx.onKey(e => {
    if (t.keys(e)) return true;
    const r = t.current(), hit = r && ops(r).find(x => x.key === e.key);
    if (hit) { hit.run(); return true; }
    return false;
  });
}

// ---------- tests ----------
function pageTests(ctx) {
  let suites = [], cur = ctx.rest[0], runId = '';
  const left = h('div', {}, loading()), right = h('div', { class: 'grow' });
  ctx.el.append(pageHead('Tests', 'Test suites of rig.yaml and their last runs'), splitPane(left, right, { key: 'tests.side', size: 300 }));
  const refresh = async () => {
    try { suites = frameRows(await query('tests.suites')); } catch (e) { left.replaceChildren(errBox(e)); return; }
    if (!suites.length) { left.replaceChildren(); right.replaceChildren(emptyState('No test suites', 'A tests: entry in rig.yaml shows here')); return; }
    if (!suites.some(s => s.suite === cur)) cur = suites[0].suite;
    left.replaceChildren(sideList(suites, cur, s => { cur = s.suite; runId = ''; refresh(); }, { key: 'suite', label: s => [s.suite, s.failed ? badge(s.failed + ' failed', 'red') : s.passed ? badge(s.passed + ' passed', 'green') : null], sub: s => s.help || s.packages }));
    drawRun();
  };
  async function drawRun() {
    const s = suites.find(x => x.suite === cur);
    const pat = h('input', { placeholder: '-run pattern (optional)', class: 'mono', style: { width: '240px' } });
    const res = h('div', {});
    right.replaceChildren(h('div', { class: 'toolbar' }, h('b', {}, s.suite), s.when && h('span', { class: 'muted' }, 'last ' + toDate(s.when).toLocaleString()), h('span', { class: 'spacer' }), pat,
      btn([icon('play'), 'Run'], async () => { const r = await act('tests.start', { suite: s.suite, run: pat.value }, 'test ' + s.suite); if (r) { runId = ''; refresh(); } }, 'primary')), res);
    try {
      const f = await query('tests.run', { suite: s.suite, id: runId });
      const t = dataTable({ empty: 'No run yet', onRow: r => r.output && drawer(r.test || r.package, r.status, [h('pre', { class: 'out', style: { maxHeight: 'none' } }, r.output)]),
        columns: [{ key: 'status', render: r => badge(r.status, { pass: 'green', fail: 'red', skip: 'amber' }[r.status] || '') }, { key: 'package', cls: 'mono muted' }, { key: 'test', cls: 'mono' }, { key: 'elapsed', num: true, render: r => fmtMs((r.elapsed || 0) * 1000) }] });
      t.update(frameRows(f).sort((a, b) => (a.status === 'fail' ? -1 : 0) - (b.status === 'fail' ? -1 : 0)));
      res.replaceChildren(f.meta?.notice ? h('div', { class: 'banner amber' }, f.meta.notice) : '', h('div', { class: 'toolbar' }, searchBox('Filter', v => t.filter(v))), t.el);
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
// Three shelves: what this project's rig.yaml uses, what is installed but idle here, and the registry's others.
function pagePlugins(ctx) {
  const box = h('div', { class: 'col plugins' }, loading());
  let filter = '', rows = [], registry = null;
  const verb = (v, names) => act('plugin.' + v, { names }, v + ' ' + names.join(', ')).then(load);
  ctx.el.append(pageHead('Plugins', 'Components, importers and screens rig gets from plugins',
    searchBox('Filter plugins', v => { filter = v; draw(); }),
    btn([icon('plus'), 'Add'], async () => { const n = await ask('Add a plugin', { label: 'name (from the registry) or a path' }); if (n) verb('add', [n]); }, 'primary')), box);
  const views = () => S.views.reduce((m, v) => (v.plugin && (m[v.plugin] = [...(m[v.plugin] || []), v.title]), m), {});
  const ops = r => [
    r.installed !== false && { label: r.state === 'off' ? 'Turn on' : 'Turn off for me', key: 'o', run: () => verb(r.state === 'off' ? 'on' : 'off', [r.name]) },
    r.installed === false ? { label: 'Install', icon: 'plus', key: 'a', run: () => verb('add', [r.name]) } : { label: 'Upgrade', key: 'u', run: () => verb('up', [r.name]) },
    r.installed !== false && { label: 'Logs', icon: 'logs', key: 'l', run: () => runCommand('plugin logs ' + r.name) },
    r.installed !== false && r.kind && r.kind !== 'builtin' && { label: 'Remove', icon: 'trash', danger: true, run: () => verb('rm', [r.name]) }];
  let tab = store.get('plug.tab', 'use');
  const row = r => {
    const v = views()[r.name], acts = ops(r).filter(Boolean);
    const el = h('div', { class: 'plug-tile' + (r.state === 'off' ? ' dim' : '') },
      h('div', { class: 'plug-top' }, icon('plugin'), h('b', { class: 'ellipsis grow' }, r.name), r.version && h('span', { class: 'muted small mono' }, r.version), r.state === 'off' && badge('off')),
      h('div', { class: 'muted small clamp3', title: r.description }, r.description || 'No description'),
      v && h('div', { class: 'row wrap small' }, v.map(t => h('a', { href: '#/v-' + slug(t) }, t))),
      h('div', { class: 'plug-foot' }, r.kind && h('span', { class: 'tag' }, r.kind), h('span', { class: 'spacer' }),
        ...acts.slice(0, 2).map(x => btn(x.label, x.run, 'sm' + (x.label === 'Install' ? ' primary' : ''))), acts.length > 2 && ibtn('more', e => ctxMenu(e.currentTarget, acts), 'More', 'sm')));
    onMenu(el, () => ops(r));
    return el;
  };
  function draw() {
    const used = new Set(S.info?.used || []);
    const warns = rows.filter(r => r.state === 'warn');
    const got = rows.filter(r => r.state !== 'warn');
    const inUse = got.filter(r => used.has(r.name)), idle = got.filter(r => !used.has(r.name));
    const idleOn = idle.filter(r => r.state !== 'off').map(r => r.name);
    const names = new Set(got.map(r => r.name));
    const avail = (registry || []).filter(r => !names.has(r.name));
    const tabs = [['use', 'In use here', inUse, 'rig.yaml names them'], ['idle', 'Installed, unused', idle, 'loaded but nothing in rig.yaml needs them'], ['reg', 'Registry', avail, 'not installed']];
    const [, , list, sub] = tabs.find(t => t[0] === tab) || tabs[0];
    const shown = rankBy(list, filter, r => r.name, r => (r.description || '') + ' ' + (r.kind || ''));
    box.replaceChildren(...warns.map(w => h('div', { class: 'banner amber' }, w.description)),
      h('div', { class: 'toolbar' }, h('div', { class: 'subtabs' }, tabs.map(([id, l, items]) => h('button', { class: tab === id ? 'on' : '', type: 'button', onclick: () => { tab = id; store.set('plug.tab', id); draw(); } },
        l, h('span', { class: 'muted small', style: { marginLeft: '6px' } }, id === 'reg' && registry == null ? '…' : items.length)))), h('span', { class: 'muted small' }, sub), h('span', { class: 'spacer' }),
        tab === 'idle' && idleOn.length > 0 && btn('Turn off the ' + idleOn.length + ' unused', async () => { if (await confirmBox('Turn off unused plugins', idleOn.join(', ') + '\n\nOnly for you, in .rig/local.yaml; turn any back on here.', 'Turn off')) verb('off', idleOn); }, 'sm')),
      tab === 'reg' && registry == null ? h('div', { class: 'muted' }, h('span', { class: 'spin' }), ' Reading the registry…')
        : h('div', { class: 'plug-grid' }, shown.length ? shown.map(row) : h('div', { class: 'empty small' }, list.length ? 'None match' : 'None')));
  }
  async function load() {
    try { rows = frameRows(await query('plugins')); frameRowsCache.plugins = rows; } catch (e) { box.replaceChildren(errBox(e)); return; }
    draw();
    if (registry == null) {
      api('run', { action: 'plugin.search', args: { text: '' } }).then(r => {
        registry = String(r?.message || '').split('\n').map(l => /^(\S+) (\S+): (.*)$/.exec(l)).filter(Boolean).map(m => ({ name: m[1], version: m[2], description: m[3], installed: false }));
        draw();
      }, () => { registry = []; draw(); });
    }
  }
  load();
}
