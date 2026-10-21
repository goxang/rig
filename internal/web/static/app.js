'use strict';

// ---------- screens ----------
// A screen with `screen` shows when the project configures it (the TUI's rule); views come from
// the server: the project's, its plugins' and the user's own.
const BUILTIN = [
  { id: 'services', title: 'Services', icon: 'services', group: 'run', render: pageServices, keys: [['↑↓', 'move'], ['space', 'mark'], ['s/x/r', 'start/stop/restart'], ['l', 'logs'], ['enter', 'open']] },
  { id: 'logs', title: 'Logs', icon: 'logs', group: 'run', render: pageLogs, keys: [['space', 'pause'], ['w', 'wrap'], ['c', 'clear']] },
  { id: 'metrics', title: 'Metrics', icon: 'metrics', group: 'run', screen: 'metrics', render: pageMetrics, keys: [['r', 'refresh'], ['[ ]', 'dashboard']] },
  { id: 'traces', title: 'Traces', icon: 'traces', group: 'run', screen: 'traces', render: pageTraces },
  { id: 'hosts', title: 'Hosts', icon: 'hosts', group: 'run', screen: 'hosts', render: pageHosts },
  { id: 'load', title: 'Load', icon: 'load', group: 'run', screen: 'load', render: pageLoad },
  { id: 'data', title: 'Data', icon: 'data', group: 'data', screen: 'data', render: pageData, keys: [['enter', 'open'], ['backspace', 'up']] },
  { id: 'kv', title: 'KV', icon: 'kv', group: 'data', screen: 'kv', render: pageKV },
  { id: 'queries', title: 'Queries', icon: 'queries', group: 'data', screen: 'queries', render: pageQueries, keys: [['ctrl+enter', 'run']] },
  { id: 'manifests', title: 'Manifests', icon: 'manifests', group: 'data', screen: 'manifests', render: pageManifests },
  { id: 'tasks', title: 'Tasks', icon: 'tasks', group: 'dev', render: pageTasks, show: () => taskList().length > 0 },
  { id: 'tests', title: 'Tests', icon: 'tests', group: 'dev', screen: 'tests', render: pageTests },
  { id: 'jobs', title: 'Activity', icon: 'jobs', group: 'system', render: pageJobs },
  { id: 'inbox', title: 'Inbox', icon: 'inbox', group: 'system', render: pageInbox },
  { id: 'plugins', title: 'Plugins', icon: 'plugin', group: 'system', render: pagePlugins },
  { id: 'api', title: 'API', icon: 'api', group: 'system', render: pageAPI },
  { id: 'settings', title: 'Settings', icon: 'settings', group: 'system', render: pageSettings },
];
const GROUPS = [
  { id: 'run', title: 'Operate' }, { id: 'data', title: 'Data' }, { id: 'dev', title: 'Develop' },
  { id: 'project', title: 'Project' }, { id: 'user', title: 'Yours' }, { id: 'system', title: 'System' },
];
let PAGES = [];
const slug = t => String(t).toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
function viewIcon(v) {
  const t = (v.plugin + ' ' + v.title).toLowerCase();
  return /git(?!lab|hub)|change/.test(t) ? 'git' : /jira|issue|board/.test(t) ? 'board' : /confluence|doc|wiki/.test(t) ? 'doc'
    : /gitlab|github|merge|pull/.test(t) ? 'git' : /game|terminal|arcade/.test(t) ? 'terminal' : v.group === 'home' ? 'home' : 'view';
}
function buildPages() {
  const screens = new Set(S.info?.screens || []);
  const tabs = S.info?.tabs?.length ? S.info.tabs.map(t => t.toLowerCase()) : null;
  const shown = p => (!p.screen || screens.has(p.screen)) && (!p.show || p.show()) && (!tabs || p.group === 'system' || p.id === 'tasks' || tabs.includes(p.id));
  const views = S.views.filter(v => !v.command).map(v => ({
    id: 'v-' + slug(v.title), title: v.title, icon: viewIcon(v), view: v, render: ctx => pageView(ctx, v),
    group: v.group === 'home' ? 'run' : v.group, home: v.group === 'home',
  })).filter(p => !tabs || tabs.includes(p.title.toLowerCase()));
  const order = Object.fromEntries(GROUPS.map((g, i) => [g.id, i]));
  PAGES = [...views.filter(v => v.home), ...BUILTIN.filter(shown), ...views.filter(v => !v.home)]
    .map((p, i) => ({ ...p, i })).sort((a, b) => order[a.group] - order[b.group] || a.i - b.i);
}
const groupTitle = g => g.id === 'project' ? (S.info?.project ? S.info.project[0].toUpperCase() + S.info.project.slice(1) : 'Project') : g.title;
const pageOf = id => PAGES.find(p => p.id === id);

// ---------- chrome ----------
let shell;
const collapsed = new Set(store.get('nav.collapsed', []));
function envPicker() {
  const envs = resources('environment').map(r => r.Name).sort();
  const cur = S.info?.env;
  const sp = specOf('environment', cur) || {};
  const sel = h('select', { 'aria-label': 'environment', onchange: e => switchEnv(e.target.value) },
    envs.map(n => h('option', { value: n, selected: n === cur }, n)));
  sel.value = cur;
  return h('label', { class: 'env-pill' + (sp.ReadOnly ? ' readonly' : sp.Protected ? ' protected' : ''), title: (sp.Description || 'environment') + ' · ctrl+e' }, icon('env'), sel);
}
function renderChrome() {
  const cur = parseRoute().id, curPage = pageOf(cur);
  const link = (p, n) => h('a', { href: '#/' + p.id, class: p.id === cur ? 'on' : '', 'data-page': p.id, title: p.title },
    icon(p.icon), h('span', { class: 'ellipsis' }, p.title), h('span', { class: 'count', hidden: true }));
  const groups = GROUPS.map(g => ({ g, pages: PAGES.filter(p => p.group === g.id) })).filter(x => x.pages.length);
  const rail = store.get('nav.rail', true);
  const toggleRail = e => { const on = !store.get('nav.rail', true); store.set('nav.rail', on); $('.shell')?.classList.toggle('rail', on); e.currentTarget.title = on ? 'Expand the sidebar' : 'Collapse the sidebar'; window.dispatchEvent(new Event('resize')); };
  const brand = () => h('div', { class: 'brand' }, h('span', { class: 'logo' }, 'r'), h('div', { class: 'grow' }, S.info?.project || 'rig', h('small', {}, 'rig ' + (S.info?.version || ''))));
  const side = h('aside', { class: 'sidebar' }, h('div', { class: 'side-top' }, brand(), ibtn('menu', toggleRail, rail ? 'Expand the sidebar' : 'Collapse the sidebar', 'rail-btn')), h('div', { class: 'side-env' }, envPicker()),
    h('nav', { class: 'nav' }, groups.map(({ g, pages }) => {
      const closed = collapsed.has(g.id) && !pages.some(p => p.id === cur);
      return h('div', { class: 'nav-group' + (closed ? ' closed' : '') },
        h('button', { class: 'nav-h', type: 'button', onclick: e => {
          collapsed.has(g.id) ? collapsed.delete(g.id) : collapsed.add(g.id); store.set('nav.collapsed', [...collapsed]);
          e.currentTarget.parentElement.classList.toggle('closed');
        } }, icon('chev', 'chev'), groupTitle(g)),
        h('div', { class: 'nav-items' }, pages.map(p => link(p))));
    })),
    h('div', { class: 'side-foot' }, h('button', { class: 'btn ghost side-ai', onclick: toggleAI, type: 'button', title: 'Assistant (@)' }, icon('ai'), h('span', { class: 'lbl' }, 'Assistant'), h('span', { class: 'kbd' }, '@'))));
  const curGroup = curPage?.group;
  const top = h('header', { class: 'topbar' }, ibtn('menu', () => document.body.classList.toggle('nav-open'), 'Screens', 'nav-btn'), brand(),
    h('nav', { class: 'topnav' }, groups.map(({ g, pages }) => h('a', { href: '#/' + (pages.find(p => p.id === store.get('last.' + g.id)) || pages[0]).id, class: g.id === curGroup ? 'on' : '', 'data-group': g.id },
      groupTitle(g)))),
    h('div', { class: 'crumb' }, curPage ? [groupTitle(GROUPS.find(g => g.id === curGroup) || {}), h('span', { class: 'sep' }, '/'), curPage.title] : ''),
    h('div', { class: 'spacer' }),
    h('button', { class: 'palette-btn', onclick: () => openPalette(), type: 'button', title: 'Search (ctrl+k) · : runs a command' }, icon('search'), h('span', { class: 'ellipsis' }, 'Search or :command'), h('span', { class: 'kbd' }, navigator.platform.includes('Mac') ? '⌘K' : 'Ctrl K')),
    h('span', { class: 'top-env' }, envPicker()),
    h('a', { class: 'btn icon ghost top-act', href: '#/jobs', 'data-page': 'jobs', title: 'Activity (!)', 'aria-label': 'Activity' }, icon('jobs'), h('span', { class: 'count', hidden: true })),
    ibtn('ai', toggleAI, 'Assistant (@)', 'ai-btn'), ibtn('ext', () => browserFull(), 'Full screen (F11)', 'full-btn'),
    ibtn(document.documentElement.dataset.scheme === 'light' ? 'moon' : 'sun', () => setPref('scheme', document.documentElement.dataset.scheme === 'light' ? 'dark' : 'light'), 'Light or dark'));
  const sub = h('nav', { class: 'subnav' }, PAGES.filter(p => p.group === curGroup).map(p => link(p)));
  side.addEventListener('click', e => { if (e.target.closest('a')) document.body.classList.remove('nav-open'); });
  shell.replaceChildren(h('div', { class: 'nav-scrim', onclick: () => document.body.classList.remove('nav-open') }), side, top, sub, h('main', { class: 'main', id: 'main' }), h('footer', { class: 'keys', id: 'keys' }));
  shell.classList.toggle('ai-open', aiOpen);
  shell.classList.toggle('rail', rail);
  if (aiOpen) shell.append(aiPanel());
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
  const keys = [...(extra || []), [':', 'command'], ['S', 'save'], ['ctrl+k', 'search'], ['ctrl+e', 'env'], ['@', 'AI'], ['.', 'menu'], ['?', 'help']];
  el.replaceChildren(...keys.map(([k, d]) => h('span', {}, h('b', {}, k), d)), h('span', { class: 'spacer' }), h('span', {}, (S.info?.project || '') + ' · ' + (S.info?.env || '')));
}
async function switchEnv(env) {
  if (!env || env === S.info?.env) return;
  S.env = env; store.set('env', env);
  const main = $('#main');
  main?.replaceChildren(h('div', { class: 'page' }, h('div', { class: 'empty' }, h('span', { class: 'spin' }), h('div', { class: 'big', style: { marginTop: '10px' } }, 'Opening ' + env + '…'))));
  await boot(true);
  toast('Environment ' + S.info.env, 'ok', 2000);
}

// ---------- routing ----------
function parseRoute() {
  const [path, qs] = location.hash.replace(/^#\/?/, '').split('?');
  const [id, ...rest] = path.split('/').map(decodeURIComponent);
  return { id: pageOf(id) ? id : PAGES[0]?.id, rest, q: new URLSearchParams(qs || '') };
}
function go(id, params) { location.hash = '#/' + id + (params ? '?' + new URLSearchParams(params) : ''); }
function route() {
  S.cleanups.splice(0).forEach(f => { try { f(); } catch { /* gone */ } });
  S.keyHandler = null; S.menu = []; S.screen = '';
  const r = parseRoute(), page = pageOf(r.id);
  if (!page) return;
  if (S.trail.at(-1) !== r.id) S.trail.push(r.id);
  store.set('last.' + page.group, page.id);
  if (!document.querySelector(`.subnav [data-page="${page.id}"]`) || !document.querySelector(`.topnav [data-group="${page.group}"].on`)) renderChrome();
  for (const a of document.querySelectorAll('[data-page]')) a.classList.toggle('on', a.dataset.page === r.id);
  $('.crumb')?.replaceChildren(groupTitle(GROUPS.find(g => g.id === page.group) || {}), h('span', { class: 'sep' }, '/'), page.title);
  document.title = page.title + ' · ' + (S.info?.project || 'rig');
  const main = $('#main'), el = h('div', { class: 'page' });
  main.replaceChildren(el); main.scrollTop = 0;
  renderKeys(page.keys);
  const ctx = {
    el, q: r.q, rest: r.rest, page,
    every(ms, f) { const t = setInterval(() => { if (!document.hidden) f(); }, ms); S.cleanups.push(() => clearInterval(t)); },
    onCleanup(f) { S.cleanups.push(f); },
    onKey(f) { S.keyHandler = f; },
    menu(items) { S.menu = items; },
  };
  S.ctx = ctx;
  try { page.render(ctx); } catch (e) { el.append(h('div', { class: 'err' }, e.stack || e.message)); }
}
window.addEventListener('hashchange', () => { closeOverlay(); route(); });
function stepPage(d) {
  const i = PAGES.findIndex(p => p.id === parseRoute().id);
  go(PAGES[(i + d + PAGES.length) % PAGES.length].id);
}

function pageHead(title, desc, ...actions) {
  return h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, title), desc && h('p', {}, desc)), actions.length > 0 && h('div', { class: 'actions' }, actions));
}
const emptyState = (big, small, ...more) => h('div', { class: 'empty' }, h('div', { class: 'big' }, big), small && h('div', {}, small), more);

// ---------- palette (ctrl+k) and the command line (:) ----------
function paletteItems() {
  return [
    ...PAGES.map(p => ({ k: 'screen', t: p.title, d: groupTitle(GROUPS.find(g => g.id === p.group)), run: () => go(p.id) })),
    ...serviceNames().flatMap(n => [
      { k: 'service', t: n, d: 'open', run: () => { go('services'); setTimeout(() => openService(n), 50); } },
      { k: 'logs', t: n, d: 'logs', run: () => go('logs', { svc: n }) },
      { k: 'restart', t: n, d: 'restart', run: () => act('runtime.restart', { service: n }, 'restart ' + n) }]),
    ...taskList().map(t => ({ k: 'task', t: t.name, d: firstLine(t.help), run: () => runTask(t) })),
    ...resources('dashboard').map(r => ({ k: 'dashboard', t: r.Name, run: () => go('metrics/' + r.Name) })),
    ...resources('query').map(r => ({ k: 'query', t: r.Name, d: firstLine(r.Spec?.Help), run: () => go('queries/' + r.Name) })),
    ...S.views.filter(v => v.command).map(v => ({ k: 'view', t: ':' + v.command, d: v.title, run: () => openCommandView(v) })),
    ...Object.keys(S.info?.themes || {}).map(n => ({ k: 'theme', t: 'Theme: ' + n, run: () => setPref('theme', n) })),
    ...LAYOUTS.map(l => ({ k: 'layout', t: 'Layout: ' + l.t, run: () => setPref('layout', l.id) })),
    { k: 'mode', t: 'Toggle light / dark', run: () => setPref('scheme', document.documentElement.dataset.scheme === 'light' ? 'dark' : 'light') },
    { k: 'session', t: 'Save the session (S)', d: 'filters, sorts, screen; reopens on it', run: () => saveSession() },
    { k: 'session', t: 'Forget the saved session', run: forgetSession },
    { k: 'ai', t: 'Ask the assistant', run: () => { if (!aiOpen) toggleAI(); } },
    ...resources('environment').map(r => ({ k: 'environment', t: r.Name, d: r.Spec?.Description, run: () => switchEnv(r.Name) })),
  ];
}
// openPalette searches everything; a line starting with : runs as a command instead.
function openPalette({ only, prompt, value = '' } = {}) {
  const items = paletteItems().filter(x => !only || x.k === only);
  let sel = 0, shown = items;
  const input = h('input', { placeholder: prompt || 'Search screens, services, tasks, dashboards, environments… or type : and a command', 'aria-label': 'search', value });
  const res = h('div', { class: 'res' });
  const draw = () => {
    if (input.value.startsWith(':')) {
      res.replaceChildren(h('div', { class: 'opt sel' }, icon('cmd'), h('span', { class: 'mono' }, input.value), h('span', { class: 'k' }, 'enter runs')));
      return;
    }
    shown = rankBy(items, input.value, x => x.t, x => x.k + ' ' + (x.d || '')).slice(0, 80);
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
    else if (e.key === 'Enter') {
      e.preventDefault();
      if (input.value.startsWith(':')) { const line = input.value.slice(1); closeOverlay(); runCommand(line); }
      else if (shown[sel]) pick(shown[sel]);
    }
  });
  openOverlay([h('div', { class: 'palette' }, input, res)]);
  draw();
  input.focus();
}
const VERBS = ['open', 'service', 'start', 'stop', 'restart', 'deploy', 'build', 'logs', 'env', 'theme', 'task', 'plugins', 'search', 'menu', 'reload', 'ai', 'help', 'save', 'forget',
  'up', 'down', 'status', 'ps', 'exec', 'query', 'kv', 'do', 'test', 'doctor', 'plugin', 'secret', 'trace', 'load', 'manifests', 'why', 'ship', 'diff', 'version'];
function openCommandLine(value = '') {
  const hist = store.get('cmd.hist', []);
  let at = hist.length;
  const input = h('input', { value, placeholder: 'restart parser · open Logs · kv get key · env k8s · status   (↑ history, tab completes)', 'aria-label': 'command', spellcheck: false });
  const hint = h('div', { class: 'cmd-hint' });
  const complete = () => {
    const parts = input.value.split(' '), last = parts.at(-1).toLowerCase();
    const pool = parts.length === 1 ? VERBS : parts[0] === 'open' ? PAGES.map(p => p.title) : parts[0] === 'env' ? resources('environment').map(r => r.Name)
      : parts[0] === 'theme' ? Object.keys(S.info?.themes || {}) : parts[0] === 'task' ? taskList().map(t => t.name) : serviceNames();
    const m = pool.filter(x => x.toLowerCase().startsWith(last));
    if (m.length === 1) { parts[parts.length - 1] = m[0]; input.value = parts.join(' ') + ' '; hint.textContent = ''; }
    else hint.textContent = m.slice(0, 20).join('  ');
  };
  input.addEventListener('keydown', e => {
    if (e.key === 'Tab') { e.preventDefault(); complete(); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); if (at > 0) input.value = hist[--at]; }
    else if (e.key === 'ArrowDown') { e.preventDefault(); input.value = at < hist.length - 1 ? hist[++at] : (at = hist.length, ''); }
    else if (e.key === 'Enter') {
      e.preventDefault();
      const line = input.value.trim();
      closeOverlay();
      if (!line) return;
      store.set('cmd.hist', [...hist.filter(x => x !== line), line].slice(-100));
      runCommand(line);
    }
  });
  input.addEventListener('input', () => { hint.textContent = ''; });
  openOverlay([h('div', { class: 'cmdline' }, h('span', { class: 'colon' }, ':'), input, hint)]);
  input.focus();
}
// runCommand is the TUI's palette line: what the page does itself, else `rig <line>`.
async function runCommand(line) {
  const f = line.trim().split(/\s+/), verb = f[0], args = f.slice(1);
  const svcs = args.filter(a => serviceNames().includes(a));
  switch (verb) {
    case 'open': {
      const want = args.join(' ').toLowerCase();
      const p = PAGES.find(x => x.title.toLowerCase() === want || x.id === want) || PAGES.find(x => x.title.toLowerCase() === (args[0] || '').toLowerCase());
      if (p) { go(p.id, args.length > 1 && p.title.toLowerCase() !== want ? { q: args.slice(1).join(' ') } : undefined); return; }
      return toast('No screen ' + want, 'bad');
    }
    case 'service': if (svcs[0]) { go('services'); setTimeout(() => openService(svcs[0]), 60); return; } break;
    case 'start': case 'stop': case 'restart':
      if (svcs.length) { for (const n of svcs) act('runtime.' + verb, { service: n }, verb + ' ' + n); return; }
      break;
    case 'logs': if (svcs.length || !args.length) { go('logs', svcs.length ? { svc: svcs.join(',') } : undefined); return; } break;
    case 'env': if (!args.length) return openPalette({ only: 'environment', prompt: 'Switch environment' }); return switchEnv(args[0]);
    case 'theme': if (!args.length) return openPalette({ only: 'theme', prompt: 'Pick a theme' }); return setPref('theme', args[0]);
    case 'task': {
      if (!args.length) return openPalette({ only: 'task', prompt: 'Run a task' });
      const t = taskList().find(x => x.name === args[0]);
      if (t) return runTask(t);
      break;
    }
    case 'plugins': return go('plugins');
    case 'search': return openPalette({ value: args.join(' ') });
    case 'menu': return openMenu();
    case 'reload': return boot(true);
    case 'save': return saveSession();
    case 'forget': return forgetSession();
    case 'help': return showHelp();
    case 'ai': if (!aiOpen) toggleAI(); if (args.length) aiSend(args.join(' ')); return;
    case 'q': case 'quit': return;
  }
  const cv = S.views.find(v => v.command === verb);
  if (cv) return openCommandView(cv);
  cliRun(line);
}
// cliRun shows `rig <line>` running in this project and environment.
function cliRun(line) {
  const out = h('pre', { class: 'out', style: { maxHeight: 'none', flex: 1 } });
  const head = h('div', { class: 'row' }, h('span', { class: 'spin' }), h('span', { class: 'muted' }, 'running'));
  const ctrl = new AbortController();
  drawer(':' + line, 'rig ' + line + ' on ' + S.info.env, [head, out], () => ctrl.abort());
  let text = '';
  ndjson('cli', { line }, m => {
    if (m.out != null) { text += m.out; out.textContent = text; out.scrollTop = out.scrollHeight; }
    if (m.exit != null) head.replaceChildren(m.exit === 0 ? badge('done', 'green', true) : badge('exit ' + m.exit, 'red', true), h('span', { class: 'spacer' }), btn([icon('copy'), 'Copy'], () => copy(text), 'sm'), btn([icon('restart'), 'Again'], () => { closeOverlay(); cliRun(line); }, 'sm'));
  }, ctrl.signal).catch(e => { if (e.name !== 'AbortError') head.replaceChildren(h('span', { class: 'err' }, e.message)); });
}
function openCommandView(v) {
  const box = h('div', { class: 'cmdview' });
  const ctx = { el: box, q: new URLSearchParams(), rest: [], every() {}, onCleanup() {}, onKey() {}, menu() {} };
  const cleanups = [];
  ctx.every = (ms, f) => { const t = setInterval(() => !document.hidden && f(), ms); cleanups.push(() => clearInterval(t)); };
  ctx.onCleanup = f => cleanups.push(f);
  const d = drawer(v.title, ':' + v.command, [box], () => cleanups.forEach(f => f()));
  d.classList.add('wide');
  pageView(ctx, v, true);
}
// openMenu lists what the screen can do: the TUI's `.` menu.
function openMenu(at) {
  const page = pageOf(parseRoute().id);
  const own = (typeof S.menu === 'function' ? S.menu() : S.menu).filter(Boolean);
  ctxMenu(at || { x: innerWidth / 2 - 140, y: 120 }, [...own, own.length && '-', { key: 'R', label: 'Reload the screen', icon: 'refresh', run: () => route() },
    { key: ':', label: 'Run a command', run: () => openCommandLine() }, { key: '@', label: 'Ask the assistant', run: toggleAI }], page?.title || 'Menu');
}
function showHelp() {
  const page = pageOf(parseRoute().id);
  modal('Keys', h('div', { class: 'help-grid' },
    shortcutsTable(), page?.keys && h('div', {}, h('h4', {}, page.title), h('table', {}, h('tbody', {}, page.keys.map(([k, d]) => h('tr', {}, h('td', {}, h('kbd', {}, k)), h('td', {}, d))))))),
  [['Close', true, 'primary']]);
}

// ---------- keys: the TUI's rig preset ----------
document.addEventListener('keydown', e => {
  const typing = e.target.closest?.('input,textarea,select,[contenteditable]');
  const k = e.key.toLowerCase();
  if ((e.ctrlKey || e.metaKey) && k === 'k') { e.preventDefault(); overlay ? closeOverlay() : openPalette(); return; }
  if (e.ctrlKey && k === 'e') { e.preventDefault(); closeOverlay(); openPalette({ only: 'environment', prompt: 'Switch environment' }); return; }
  if (e.ctrlKey && k === 'p') { e.preventDefault(); closeOverlay(); openPalette({ only: 'theme', prompt: 'Pick a theme' }); return; }
  if (e.key === 'F1') { e.preventDefault(); showHelp(); return; }
  if (e.key === 'Escape') {
    if (overlay) { closeOverlay(); e.preventDefault(); } else if (typing) e.target.blur();
    else if (document.querySelector('.zoomed')) return;
    else if (aiOpen && !e.target.closest('.main')) toggleAI();
    else if (!S.keyHandler?.(e)) back();
    return;
  }
  if (typing || overlay || e.metaKey) return;
  const arrow = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
  if (arrow && e.ctrlKey) { e.preventDefault(); subTab(arrow); return; }
  if (arrow && (e.shiftKey || e.altKey)) { e.preventDefault(); stepPage(arrow); return; }
  if (e.key === 'Tab' && !e.ctrlKey && !e.altKey && !e.target.closest?.('.drawer,.modal,.ai-panel')) { e.preventDefault(); stepPage(e.shiftKey ? -1 : 1); return; }
  if (e.ctrlKey || e.altKey) return;
  if (S.keyHandler?.(e)) { e.preventDefault(); return; }
  const take = f => { e.preventDefault(); f(); };
  switch (e.key) {
    case ':': return take(() => openCommandLine());
    case '@': return take(toggleAI);
    case '!': return take(() => go('jobs'));
    case '?': return take(showHelp);
    case '.': case 'ContextMenu': return take(() => openMenu());
    case 'T': return take(() => openPalette({ only: 'task', prompt: 'Run a task' }));
    case 'S': return take(() => saveSession());
    case 'Backspace': return take(back);
    case '/': { const f = $('.page [data-filter]'); if (f) take(() => f.focus()); return; }
  }
});
// back returns to the screen before this one, as esc and ⌫ do in the TUI.
function back() {
  if (S.trail.length < 2) return;
  S.trail.pop();
  const id = S.trail.pop();
  go(id);
}
// subTab moves a screen's own tabs: the screen's handler, else its first row of sub-tabs.
function subTab(d) {
  if (S.ctx?.subTab) { S.ctx.subTab(d); return; }
  const bar = $('.page .subtabs, .page .seg');
  const bs = bar ? [...bar.querySelectorAll('button')] : [];
  if (!bs.length) { stepPage(d); return; }
  const i = bs.findIndex(b => b.classList.contains('on'));
  bs[(i + d + bs.length) % bs.length].click();
}
// right-click on a screen's empty space opens its menu; rows and panels answer with their own
document.addEventListener('contextmenu', e => {
  if (e.defaultPrevented || e.shiftKey || !e.target.closest('.main') || e.target.closest('input,textarea,a,pre,.logs,.jtree') || String(getSelection())) return;
  e.preventDefault();
  openMenu({ x: e.clientX, y: e.clientY });
});

// ---------- saved session ----------
// S saves what the page keeps (filters, sorts, pane sizes, queries, the screen) on the server and
// pins it: from then on the page autosaves, and a page opened without a link to a screen reopens
// on it, in any browser, as `rig resume` does for the TUI.
const KEEP_OUT = new Set(['token']);
function sessionState() {
  const state = {};
  try {
    for (const k of Object.keys(localStorage)) if (k.startsWith('rig.web.') && !KEEP_OUT.has(k.slice(8))) state[k.slice(8)] = localStorage.getItem(k);
  } catch { /* storage blocked */ }
  return state;
}
async function saveSession(quiet) {
  store.set('session.pin', true);
  try {
    await web('web/session', { env: S.info?.env, hash: location.hash, state: sessionState() });
    if (!quiet) toast('Session saved: this page opens on it from now on', 'ok', 2500);
  } catch (e) { if (!quiet) toast('Cannot save the session: ' + e.message, 'bad', 6000); }
}
async function forgetSession() {
  store.set('session.pin', false);
  try { await fetch('/v1/web/session', { method: 'DELETE', headers: authHeaders() }); toast('Session forgotten', 'ok', 2000); } catch (e) { toast(e.message, 'bad'); }
}
// restoreSession reopens a saved session once per tab, only when the address names no screen.
async function restoreSession() {
  let done = false;
  try { done = sessionStorage.getItem('rig.web.restored') === '1'; sessionStorage.setItem('rig.web.restored', '1'); } catch { /* private mode */ }
  if (done || /^#\/[^?]/.test(location.hash)) return false;
  try {
    const r = await fetch('/v1/web/session', { headers: authHeaders() });
    const s = r.ok ? await r.json() : null;
    if (!s?.state) return false;
    for (const [k, v] of Object.entries(s.state)) if (!KEEP_OUT.has(k)) localStorage.setItem('rig.web.' + k, v);
    if (s.env) localStorage.setItem('rig.web.env', JSON.stringify(s.env));
    localStorage.setItem('rig.web.session.pin', 'true');
    history.replaceState(null, '', location.pathname + (s.hash || '#/'));
    location.reload();
    return true;
  } catch { return false; }
}
setInterval(() => { if (store.get('session.pin', false) && S.info && !document.hidden) saveSession(true); }, 20000);
document.addEventListener('visibilitychange', () => { if (document.hidden && store.get('session.pin', false) && S.info) saveSession(true); });

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
    const r = await fetch('/v1/web', { headers: authHeaders() });
    if (r.status === 401) { showLocked(); return; }
    const info = await r.json();
    if (!r.ok) throw new Error(info.error?.message || r.statusText);
    const [cat, prov, views] = await Promise.all([api('catalog'), api('provides'), web('views').catch(() => [])]);
    S.info = info; S.catalog = cat || []; S.provides = prov || { sources: {}, actions: {} }; S.views = views || [];
    S.jobs = []; S.inbox = [];
    const [comps, plugs] = await Promise.all([query('components').catch(() => null), query('plugins').catch(() => null)]);
    frameRowsCache.components = comps ? frameRows(comps) : [];
    frameRowsCache.plugins = plugs ? frameRows(plugs) : [];
  } catch (e) {
    if (S.env && !again) { S.env = ''; store.set('env', ''); return boot(true); }
    if (shell && again) { toast('Cannot open ' + S.env + ': ' + e.message, 'bad', 8000); S.env = S.info?.env || ''; store.set('env', S.env); route(); return; }
    $('#app').replaceChildren(h('div', { class: 'empty', style: { paddingTop: '20vh' } }, h('div', { class: 'big' }, 'rig web cannot reach the engine'), h('div', { class: 'err' }, e.message)));
    return;
  }
  buildPages();
  applyPrefs();
  if (!shell) { shell = h('div', { class: 'shell' }); toastsEl = h('div', { class: 'toasts' }); $('#app').replaceChildren(shell, toastsEl); }
  aiState.session = '';
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
    history.replaceState(null, '', location.pathname + '#/');
  }
  applyPrefs();
  if (!S.token) { showLocked(); return; }
  restoreSession().then(reloading => { if (!reloading) boot(); });
  setInterval(() => { if (document.hidden || !S.info) return; refreshInbox(); if (S.running || S.jobs.some(isRunning)) refreshJobs(); }, 3000);
})();

// truncated text gets its full text as a tooltip the moment it is hovered
document.addEventListener('mouseover', e => {
  for (let el = e.target, n = 0; el && el !== document.body && n < 5; el = el.parentElement, n++) {
    if (el.title || el.querySelector?.('[title]')) return;
    const cs = getComputedStyle(el);
    const clipped = (cs.textOverflow === 'ellipsis' && el.scrollWidth > el.clientWidth + 1) || (cs.webkitLineClamp !== 'none' && el.scrollHeight > el.clientHeight + 1);
    if (clipped) { el.title = el.textContent.trim().slice(0, 1000); return; }
  }
});

