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
  const input = h('textarea', { rows: 2, placeholder: 'Ask about this project · enter sends, shift+enter a new line',
    onkeydown: e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); aiSend(input.value); } } });
  const sessions = h('select', { 'aria-label': 'conversation', onchange: e => aiOpenSession(e.target.value) });
  const status = h('span', { class: 'muted small ellipsis' });
  const sendBtn = ibtn('send', () => aiState.busy ? aiState.ctrl?.abort() : aiSend(input.value), 'Send (enter)');
  const el = h('aside', { class: 'ai-panel' },
    h('div', { class: 'ai-h' }, icon('ai'), h('b', {}, 'Assistant'), sessions, h('span', { class: 'spacer' }),
      ibtn('plus', () => aiOpenSession(''), 'New conversation'), ibtn('settings', () => go('settings', { s: 'ai' }), 'AI settings'), ibtn('x', toggleAI, 'Close (esc)')),
    status, msgs, pend, h('div', { class: 'ai-in' }, input, sendBtn));
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
  if (!text || aiState.busy) return;
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

// aiSettings is Settings' Assistant section: rig ai config, in a form.
function aiSettings() {
  const box = h('div', {}, loading());
  const HELP = {
    backend: 'what runs the chat: opencode or claude (installed CLIs)', provider: 'whose API the key is for', model: 'the model, e.g. provider/model for opencode',
    effort: 'how hard it thinks', fast_model: 'a small model for quick answers (autocomplete, titles)', fast_url: 'its OpenAI-compatible URL', fast_api_key: 'its key',
    url: 'an OpenAI-compatible URL (ollama, a proxy)', api_key: 'the provider\'s API key', proxy: 'an HTTP proxy for its requests',
    autocomplete: 'suggest the rest of a command while typing', disabled: 'turn the assistant off', redact: 'mask secrets before they leave this machine',
  };
  web('ai').then(info => {
    const inputs = {};
    const field = k => {
      const v = info.config[k] || '', secret = /key/.test(k);
      let input;
      if (k === 'backend') input = h('select', {}, ['', 'opencode', 'claude'].map(x => h('option', { value: x, selected: x === v }, x || 'auto')));
      else if (k === 'provider') input = h('select', {}, h('option', { value: '' }, 'none'), Object.keys(info.providers || {}).sort().map(x => h('option', { value: x, selected: x === v, title: info.providers[x] }, x)));
      else if (['autocomplete', 'disabled', 'redact'].includes(k)) input = h('select', {}, [['', 'default'], ['true', 'on'], ['false', 'off']].map(([x, l]) => h('option', { value: x, selected: x === v }, l)));
      else input = h('input', { type: secret ? 'password' : 'text', placeholder: secret && v ? v : '', value: secret ? '' : v, autocomplete: 'off' });
      inputs[k] = { input, was: secret ? '' : v, secret };
      return h('label', { class: 'field' }, h('span', {}, h('span', { class: 'mono' }, k), h('span', { class: 'muted' }, ' — ' + (HELP[k] || ''))), input);
    };
    const state = h('div', { class: 'row' }, info.enabled ? badge('ready', 'green', true) : badge('not set up', 'amber', true), h('span', { class: 'muted' }, info.describe));
    box.replaceChildren(state, h('div', { class: 'form-grid' }, (info.keys || []).map(field)),
      h('div', { class: 'row' }, h('span', { class: 'muted small' }, info.file), h('span', { class: 'spacer' }), btn('Save', async () => {
        const out = {};
        for (const [k, x] of Object.entries(inputs)) { const v = x.input.value; if (x.secret ? v !== '' : v !== x.was) out[k] = v; }
        if (!Object.keys(out).length) return toast('Nothing changed');
        try { const r = await web('ai/config', out); toast('Saved · ' + r.describe, 'ok'); aiState.info = null; if (aiOpen) aiLoadInfo(); box.replaceWith(aiSettings()); } catch (e) { toast(e.message, 'bad', 8000); }
      }, 'primary')));
  }, e => box.replaceChildren(errBox(e)));
  return box;
}
