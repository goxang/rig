'use strict';

// ---------- the assistant: a panel beside every screen, like the TUI's @ ----------
let aiOpen = store.get('ai.open', false);
const aiState = { session: '', busy: false, ctrl: null, msgs: [], info: null, next: '' };
let aiEls = null;
function toggleAI() {
  aiOpen = !aiOpen; store.set('ai.open', aiOpen);
  shell.classList.toggle('ai-open', aiOpen);
  if (aiOpen) shell.append(aiPanel()); else { $('.ai-panel')?.remove(); aiEls = null; }
  if (aiOpen) setTimeout(() => aiEls?.input.focus(), 30);
}
// screenContext tells the assistant what the user is looking at.
function screenContext() {
  const page = pageOf(parseRoute().id);
  const sel = document.querySelector('.main tr.cur, .main .bcard.cur, .main .item.sel');
  return [page && 'screen: ' + page.title, location.hash && 'route: ' + location.hash.split('?')[0], sel && 'selected: ' + sel.textContent.trim().slice(0, 300)].filter(Boolean).join('\n');
}
function aiPrefill(text) { if (aiEls) { aiEls.input.value = text; aiEls.input.focus(); } }
async function aiLoadInfo() {
  try { aiState.info = await web('ai'); } catch (e) { aiState.info = { error: e.message }; }
  aiDraw();
}
function aiPanel() {
  const msgs = h('div', { class: 'ai-msgs' }), pend = h('div', { class: 'ai-pending' });
  const cmds = h('div', { class: 'ai-cmds', hidden: true });
  let pick = 0;
  const matches = () => { const v = input.value; return /^\/\S*$/.test(v) ? AI_COMMANDS.filter(c => c[0].startsWith(v.split(' ')[0])) : []; };
  const drawCmds = () => {
    const m = matches();
    cmds.hidden = !m.length;
    pick = Math.min(pick, Math.max(0, m.length - 1));
    cmds.replaceChildren(...m.map(([c, a, d], i) => h('button', { class: 'menu-item' + (i === pick ? ' on' : ''), type: 'button', onclick: () => { input.value = c + (a ? ' ' : ''); drawCmds(); input.focus(); if (!a) aiSend(input.value); } },
      h('b', { class: 'mono' }, c), a && h('span', { class: 'muted mono' }, a), h('span', { class: 'muted small ellipsis grow' }, d))));
  };
  const input = h('textarea', { rows: 2, placeholder: 'Ask about this project · / commands · enter sends, shift+enter a new line',
    oninput: () => drawCmds(),
    onkeydown: e => {
      const m = matches();
      if (m.length && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) { e.preventDefault(); pick = (pick + (e.key === 'ArrowDown' ? 1 : -1) + m.length) % m.length; drawCmds(); return; }
      if (m.length && e.key === 'Tab') { e.preventDefault(); input.value = m[pick][0] + (m[pick][1] ? ' ' : ''); drawCmds(); return; }
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault();
        if (m.length && m[pick][0] !== input.value.trim() && !m.some(c => c[0] === input.value.trim())) { input.value = m[pick][0] + (m[pick][1] ? ' ' : ''); drawCmds(); if (m[pick][1]) return; }
        aiSend(input.value); drawCmds();
      }
    } });
  const ghost = aiAssist(input, () => 'a question or request for the assistant of the rig project ' + (S.info?.project || '') + ', on the screen ' + (pageOf(parseRoute().id)?.title || ''));
  const sessions = h('select', { 'aria-label': 'conversation', onchange: e => aiOpenSession(e.target.value) });
  const status = h('span', { class: 'muted small ellipsis' });
  const sendBtn = ibtn('send', () => aiState.busy ? aiState.ctrl?.abort() : aiSend(input.value), 'Send (enter)');
  const el = h('aside', { class: 'ai-panel' },
    h('div', { class: 'ai-h' }, icon('ai'), h('b', {}, 'Assistant'), sessions, h('span', { class: 'spacer' }),
      ibtn('plus', () => aiOpenSession(''), 'New conversation'), ibtn('settings', () => go('settings', { s: 'ai' }), 'AI settings'), ibtn('x', toggleAI, 'Close (esc)')),
    status, msgs, pend, cmds, h('div', { class: 'ai-in' }, h('div', { class: 'grow col', style: { gap: '2px' } }, input, ghost), sendBtn));
  edgeResize(el, 'ai', 420, w => { el.style.width = w + 'px'; document.documentElement.style.setProperty('--ai-w', w + 'px'); });
  aiEls = { el, msgs, pend, input, sessions, status, sendBtn };
  if (!aiState.info) aiLoadInfo(); else aiDraw();
  return el;
}
function aiDraw() {
  if (!aiEls) return;
  const i = aiState.info || {};
  aiEls.status.textContent = i.error || (i.enabled ? i.describe : 'Not set up: ' + (i.describe || '') + ' · open Settings → Assistant');
  aiEls.sessions.replaceChildren(h('option', { value: '' }, 'New conversation'),
    ...(i.sessions || []).sort((a, b) => String(b.updated).localeCompare(String(a.updated))).slice(0, 30).map(s => h('option', { value: s.id, selected: s.id === aiState.session }, (s.title || s.id).slice(0, 48))));
  aiEls.sessions.value = aiState.session;
  aiEls.msgs.replaceChildren(...(aiState.msgs.length ? aiState.msgs.map(aiMsg) : [h('div', { class: 'empty small' },
    h('div', {}, 'Ask what is wrong, how a service is wired, or to start, stop and test things.'),
    h('div', { class: 'row wrap', style: { justifyContent: 'center', marginTop: '10px' } }, ['Why is anything failing?', 'Summarize this screen', 'What changed recently?'].map(q => btn(q, () => aiSend(q), 'sm ghost'))))]));
  if (aiState.next && !aiState.busy) aiEls.msgs.append(h('div', { class: 'row' }, btn(aiState.next, () => aiSend(aiState.next), 'sm ghost')));
  aiEls.sendBtn.replaceChildren(icon(aiState.busy ? 'stop' : 'send'));
  aiEls.msgs.scrollTop = aiEls.msgs.scrollHeight;
}
function aiMsg(m) {
  return h('div', { class: 'ai-m ' + m.role }, m.tools?.length > 0 && h('details', { class: 'ai-tools' }, h('summary', {}, m.tools.length + ' tool calls'), h('pre', { class: 'out' }, m.tools.join('\n'))),
    m.role === 'user' ? h('div', { class: 'ai-u' }, m.text) : markdown(m.text || (m.busy ? '…' : '')), m.error && h('div', { class: 'err small' }, m.error));
}
async function aiOpenSession(id) {
  aiState.ctrl?.abort();
  aiState.session = id; aiState.msgs = []; aiState.next = '';
  if (id) {
    try {
      const s = await web('ai/session?id=' + encodeURIComponent(id));
      aiState.msgs = (s.Messages || s.messages || []).map(m => ({ role: m.Role || m.role, text: m.Text || m.text, tools: m.Tools || m.tools }));
      aiState.next = s.Next || s.next || '';
    } catch (e) { toast(e.message, 'bad'); }
  }
  aiDraw();
}
// aiSend runs a turn; approvals the assistant asks for show above the input while it runs.
async function aiSend(text) {
  text = String(text || '').trim();
  if (!text) return;
  if (text.startsWith('/')) { if (aiEls) aiEls.input.value = ''; aiCommand(text); return; }
  if (aiState.busy) return;
  if (!aiOpen) toggleAI();
  if (aiEls) aiEls.input.value = '';
  const me = { role: 'user', text }, bot = { role: 'assistant', text: '', tools: [], busy: true };
  aiState.msgs.push(me, bot); aiState.busy = true; aiState.next = '';
  aiDraw();
  const ctrl = aiState.ctrl = new AbortController();
  const poll = setInterval(aiPending, 1000);
  const redraw = debounce(aiDraw, 60);
  try {
    await ndjson('ai/chat', { text, session: aiState.session, screen: screenContext() }, e => {
      if (e.kind === 'session') { if (aiState.session !== e.text) { aiState.session = e.text; aiLoadInfo(); } }
      else if (e.kind === 'text') bot.text += e.text;
      else if (e.kind === 'tool') bot.tools.push(e.text);
      else if (e.kind === 'error') bot.error = e.text;
      else if (e.kind === 'next') aiState.next = e.text;
      else if (e.kind === 'done' && e.text) aiState.next = e.text;
      redraw();
    }, ctrl.signal);
  } catch (e) { if (e.name !== 'AbortError') bot.error = e.message; else bot.error = 'stopped'; }
  clearInterval(poll);
  bot.busy = false; aiState.busy = false;
  aiEls?.pend.replaceChildren();
  aiDraw();
  refreshJobs();
}
async function aiPending() {
  if (!aiEls) return;
  let list;
  try { list = await web('ai/pending'); } catch { return; }
  aiEls.pend.replaceChildren(...(list || []).map(p => h('div', { class: 'card card-b ai-ask' },
    h('div', {}, h('b', {}, 'The assistant wants to run'), h('pre', { class: 'out' }, p.text)),
    h('div', { class: 'row' }, h('span', { class: 'spacer' }),
      btn('Deny', () => aiAnswer(p.id, false), 'ghost sm'), btn('Always', () => aiAnswer(p.id, true, true), 'sm'), btn('Allow', () => aiAnswer(p.id, true), 'primary sm')))));
}
async function aiAnswer(id, ok, always = false) {
  try { await web('ai/approve', { id, ok, always }); } catch (e) { toast(e.message, 'bad'); }
  aiPending();
}

// AI_COMMANDS are the TUI chat's / commands.
const AI_COMMANDS = [
  ['/new', '', 'start a new conversation'], ['/sessions', '', 'pick a conversation'], ['/close', '', 'forget this conversation'], ['/stop', '', 'stop the running turn'],
  ['/why', '<service or symptom>', 'gather an incident\'s logs, traces, metrics and changes; ask for its root cause and a fix'],
  ['/connect', '', 'use your installed opencode or Claude Code'], ['/model', '', 'the chat\'s model'], ['/effort', '', 'the chat\'s reasoning level'], ['/fast', '', 'the model of completions while typing'],
  ['/autocomplete', '', 'suggestions while typing: on or off'], ['/redact', '', 'masking of secrets in what the model sees: on or off'], ['/help', '', 'these commands'],
];
async function aiSet(k, v) {
  try { const r = await web('ai/config', { [k]: v }); toast(k + ' ' + (v || 'default') + ' · ' + r.describe, 'ok'); aiState.info = null; aiEnabled = null; aiLoadInfo(); } catch (e) { toast(e.message, 'bad', 8000); }
}
async function aiCommand(line) {
  const [cmd, ...rest] = line.split(/\s+/), arg = rest.join(' ');
  const cfg = aiState.info?.config || {};
  const note = text => { aiState.msgs.push({ role: 'assistant', text }); aiDraw(); };
  switch (cmd) {
    case '/new': return aiOpenSession('');
    case '/stop': aiState.ctrl?.abort(); return;
    case '/close':
      if (aiState.busy) return toast('A turn is running: /stop first');
      if (aiState.session) { try { await web('ai/close', { id: aiState.session }); } catch (e) { return toast(e.message, 'bad'); } }
      aiState.info = null; await aiOpenSession(''); aiLoadInfo(); return toast('Conversation closed');
    case '/sessions': aiEls?.sessions.focus(); aiEls?.sessions.showPicker?.(); return;
    case '/why': if (!arg) return note('Say what to look into: /why checkout, /why 502s on the gateway'); return aiSend(`Why is this happening: ${arg}? Gather its logs, traces, metrics and recent changes, find the root cause and propose a fix. Do not change anything until I answer fix.`);
    case '/connect': { const v = await ask('Connect', { label: 'backend', value: cfg.backend || '', choices: ['opencode', 'claude', ''] }); if (v != null) aiSet('backend', v); return; }
    case '/model': case '/fast': { const k = cmd === '/fast' ? 'fast_model' : 'model'; const v = arg || await ask(cmd === '/fast' ? 'Completion model' : 'Chat model', { value: cfg[k] || '', placeholder: 'provider/model · empty: default' }); if (v != null) aiSet(k, v); return; }
    case '/effort': { const v = arg || await ask('Reasoning level', { value: cfg.effort || '', choices: ['low', 'medium', 'high', 'max', ''] }); if (v != null) aiSet('effort', v); return; }
    case '/autocomplete': return aiSet('autocomplete', cfg.autocomplete === 'false' ? 'true' : 'false');
    case '/redact': return aiSet('redact', cfg.redact === 'false' ? 'true' : 'false');
    case '/help': case '/?': return note(AI_COMMANDS.map(([c, a, d]) => `- \`${c}${a ? ' ' + a : ''}\` ${d}`).join('\n'));
  }
  const near = AI_COMMANDS.find(c => c[0].startsWith(cmd.slice(0, 3)));
  note('Unknown command ' + cmd + '.' + (near ? ' Did you mean ' + near[0] + '?' : '') + ' /help lists them.');
}

// aiSettings is Settings' Assistant section: rig ai config, offering only what this machine has.
function aiSettings() {
  const box = h('div', { class: 'ai-set' }, loading());
  Promise.all([web('ai'), web('ai/options')]).then(([info, opts]) => {
    const cfg = info.config, inputs = {};
    const backends = (opts.backends || []).map(b => ({ ...b, models: b.models || [] })), local = (opts.local || []).map(l => ({ ...l, models: l.models || [] }));
    const dl = (id, items) => h('datalist', { id }, (items || []).map(m => h('option', { value: m })));
    const inp = (k, o = {}) => {
      const secret = /key/.test(k), v = cfg[k] || '';
      const input = h('input', { type: secret ? 'password' : 'text', placeholder: secret && v ? v : (o.ph || ''), value: secret ? '' : v, autocomplete: 'off', list: o.list });
      inputs[k] = { input, was: secret ? '' : v, secret };
      return input;
    };
    const sel = (k, opts2) => {
      const v = cfg[k] || '';
      const input = h('select', {}, opts2.map(([x, l]) => h('option', { value: x, selected: x === v }, l)));
      inputs[k] = { input, was: v };
      return input;
    };
    const fld = (label, help, input) => h('label', { class: 'field' }, h('span', {}, label, help ? h('span', { class: 'muted' }, ' · ' + help) : null), input);
    const curBackend = cfg.backend || backends[0]?.backend || '';
    const modelsOf = b => (backends.find(x => x.backend === b)?.models) || [];
    const bsel = sel('backend', [['', backends.length ? 'auto' : 'none installed'], ...backends.map(b => [b.backend, b.backend + (b.ready ? '' : ' (not logged in)')])]);
    const mlist = h('datalist', { id: 'ai-models' });
    const fill = () => mlist.replaceChildren(...modelsOf(bsel.value || curBackend).map(m => h('option', { value: m })));
    bsel.onchange = fill; fill();
    const model = inp('model', { list: 'ai-models', ph: 'default of the backend' });
    const fastMode = h('select', {}, [['backend', 'A model of the backend'], ['custom', 'Custom endpoint (URL)']].map(([x, l]) => h('option', { value: x, selected: x === (cfg.fast_url ? 'custom' : 'backend') }, l)));
    const flist = h('datalist', { id: 'ai-fast-models' });
    const fastModel = inp('fast_model', { list: 'ai-models', ph: 'e.g. a flash/mini/haiku model' });
    const fastUrl = inp('fast_url', { ph: 'https://host/v1' }), fastKey = inp('fast_api_key', { ph: 'API key' });
    const probe = btn('List models', async () => {
      try { const r = await web('ai/probe', { url: fastUrl.value, api_key: fastKey.value }); flist.replaceChildren(...r.models.map(m => h('option', { value: m }))); toast(r.models.length + ' models', 'ok'); } catch (e) { toast(e.message, 'bad', 6000); }
    });
    const fastCustom = h('div', { class: 'form-grid2' }, fld('URL', 'OpenAI-compatible', fastUrl), fld('API key', '', fastKey), fld('Model', '', h('div', { class: 'row' }, h('span', { class: 'grow' }, fastModel.cloneNode(false)), probe)));
    const customModel = fastCustom.querySelector('input[list]');
    customModel.setAttribute('list', 'ai-fast-models'); customModel.value = fastModel.value;
    customModel.oninput = () => { fastModel.value = customModel.value; };
    const fastBackend = h('div', { class: 'form-grid2' }, fld('Model', 'filter by typing', fastModel));
    const applyMode = () => { const c = fastMode.value === 'custom'; fastCustom.hidden = !c; fastBackend.hidden = c; };
    fastMode.onchange = applyMode; applyMode();
    const adv = h('details', { class: 'ai-adv', open: !!(cfg.url || cfg.api_key || (cfg.provider && cfg.provider !== 'own')) },
      h('summary', {}, 'Use an API endpoint instead (ollama, OpenAI-compatible, DeepSeek…)'),
      h('div', { class: 'form-grid2' },
        fld('Provider', '', sel('provider', [['', 'none'], ...Object.keys(info.providers || {}).sort().map(x => [x, x])])),
        fld('URL', local.length ? 'found: ' + local.map(l => l.provider).join(', ') : '', inp('url', { ph: 'http://localhost:11434/v1' })),
        fld('API key', '', inp('api_key')), fld('Proxy', '', inp('proxy', { ph: 'http://…' }))),
      local.length ? h('div', { class: 'row' }, h('span', { class: 'muted small' }, 'on this machine:'), local.map(l => btn(l.provider + ' · ' + l.models.length + ' models', () => { inputs.provider.input.value = l.provider; inputs.url.input.value = l.url; mlist.replaceChildren(...l.models.map(m => h('option', { value: m }))); }, 'sm'))) : null);
    const state = h('div', { class: 'row' }, info.enabled ? badge('ready', 'green', true) : badge('not set up', 'amber', true), h('span', { class: 'muted' }, info.describe));
    const tri = k => sel(k, [['', 'default'], ['true', 'on'], ['false', 'off']]);
    box.replaceChildren(state, mlist, flist,
      h('div', { class: 'ai-sec' }, h('h4', {}, 'Chat'),
        h('div', { class: 'form-grid2' }, fld('Backend', backends.length ? 'installed: ' + backends.map(b => b.backend).join(', ') : 'install opencode or claude', bsel),
          fld('Model', 'filter by typing', model), fld('Effort', 'reasoning level', inp('effort', { ph: 'default' })))),
      adv,
      h('div', { class: 'ai-sec' }, h('h4', {}, 'Fast model'), h('p', { class: 'muted small' }, 'Small model for autocomplete and titles.'),
        h('div', { class: 'form-grid2' }, fld('Source', '', fastMode)), fastBackend, fastCustom),
      h('div', { class: 'ai-sec' }, h('h4', {}, 'Behavior'),
        h('div', { class: 'form-grid2' }, fld('Autocomplete', 'suggest while typing', tri('autocomplete')), fld('Redact secrets', 'mask before sending', tri('redact')), fld('Disabled', 'turn the assistant off', tri('disabled')))),
      h('div', { class: 'row' }, h('span', { class: 'muted small ellipsis' }, info.file), h('span', { class: 'spacer' }), btn('Save', async () => {
        const out = {};
        if (fastMode.value === 'backend') { out.fast_url = ''; out.fast_api_key = ''; }
        for (const [k, x] of Object.entries(inputs)) { const v = x.input.value; if (x.secret ? v !== '' : v !== x.was) out[k] = v; }
        if (fastMode.value === 'backend') { if (!cfg.fast_url) delete out.fast_url; if (!cfg.fast_api_key) delete out.fast_api_key; }
        if (!Object.keys(out).length) return toast('Nothing changed');
        try { const r = await web('ai/config', out); toast('Saved · ' + r.describe, 'ok'); aiState.info = null; if (aiOpen) aiLoadInfo(); box.replaceWith(aiSettings()); } catch (e) { toast(e.message, 'bad', 8000); }
      }, 'primary')));
  }, e => box.replaceChildren(errBox(e)));
  return box;
}
