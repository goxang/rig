// Write rig plugins in TypeScript or JavaScript: register sources, streams, actions and the rest, then
// serve(). Node only, no dependencies; the protocol (rig/1), not this module, is the contract.
import { createInterface } from "node:readline";
import { ERRORS, METHODS, VERSION } from "./protocol.js";

export { ERRORS, METHODS, VERSION };

const FLUSH_EVERY = 50;
const FLUSH_ROWS = 500;
const ACK_WINDOW = 8;

export class RigError extends Error {
  constructor(code, message, hint = "") {
    super(message);
    this.code = code;
    this.hint = hint;
  }
  wire() {
    const e = { code: this.code, message: this.message };
    if (this.hint) e.data = { hint: this.hint };
    return e;
  }
}

export const unavailable = (message, hint) => new RigError(ERRORS.Unavailable, message, hint);

const typeOf = (v) =>
  typeof v === "boolean" ? "bool" : Number.isInteger(v) ? "int" : typeof v === "number" ? "number" : typeof v === "string" ? "string" : "json";

// frame makes a frame from an array of objects (fields from the first row's keys), or passes one through.
export function frame(rows) {
  if (!Array.isArray(rows)) return rows;
  if (rows.length === 0) return { fields: [], columns: [] };
  const names = Object.keys(rows[0]);
  return {
    fields: names.map((n) => ({ name: n, type: typeOf(rows[0][n]) })),
    columns: names.map((n) => rows.map((r) => r[n] ?? null)),
  };
}

function schema(args, required) {
  if (!args && !required?.length) return undefined;
  const s = { type: "object", properties: args ?? {} };
  if (required?.length) s.required = required;
  return s;
}

function prune(o) {
  for (const k of Object.keys(o)) if (o[k] === undefined || o[k] === "" || (Array.isArray(o[k]) && !o[k].length)) delete o[k];
  return o;
}

export class Ctx {
  constructor(plugin, params, signal) {
    this._p = plugin;
    this.call = params.context ?? {};
    this.component = params.component ?? "";
    this.cursor = params.cursor ?? "";
    this.job = params.job ?? "";
    this.signal = signal;
  }
  get config() { return this._p._config; }
  spec() { return this._p._config.components?.[this.component] ?? {}; }
  progress(done, total, message = "") { this._p._notify("$/progress", { job: this.job, done, total, message }); }
  line(s) { this._p._notify("$/progress", { job: this.job, line: s }); }
  log(level, message) { this._p._notify("host/log", { level, message }); }
  query(source, args = {}) { return this._p._call("host/query", { source, args, context: this.call }, this.signal); }
  run(action, args = {}) { return this._p._call("host/run", { action, args, context: this.call }, this.signal); }
  ask(kind, prompt, options = []) { return this._p._call("host/ask", { kind, prompt, options }, this.signal); }
  async ai(prompt, system = "") { return (await this._p._call("host/ai", { prompt, system }, this.signal)).text; }
  state(key) { return this._p._call("host/state.get", { key }, this.signal); }
  setState(key, value) { return this._p._call("host/state.set", { key, value }, this.signal); }
  async secret(name) { return (await this._p._call("host/secret", { name }, this.signal)).value; }
  emit(topic, data = {}) { return this._p._call("host/emit", { topic, data }, this.signal); }
}

// Out is a stream's output: frames and lines, batched every 50 ms or 500 rows.
export class Out {
  constructor(plugin, id, signal) {
    this._p = plugin;
    this._id = id;
    this._signal = signal;
    this._seq = 0;
    this._acked = 0;
    this._waiters = [];
    this._frame = null;
    this._lines = [];
    this._ended = false;
  }
  _rows() { return this._lines.length + (this._frame?.columns[0]?.length ?? 0); }
  async frame(rows) {
    const f = frame(rows);
    if (this._frame && JSON.stringify(this._frame.fields) !== JSON.stringify(f.fields)) await this._flush(false);
    this._frame ??= { fields: f.fields, columns: f.fields.map(() => []) };
    f.columns.forEach((c, i) => this._frame.columns[i].push(...c));
    if (this._rows() >= FLUSH_ROWS) await this._flush(false);
  }
  async line(s) {
    this._lines.push(s);
    if (this._rows() >= FLUSH_ROWS) await this._flush(false);
  }
  _ack(seq) {
    this._acked = Math.max(this._acked, seq);
    this._waiters.splice(0).forEach((w) => w());
  }
  async _flush(done, error) {
    if (this._ended || (!done && !this._frame && !this._lines.length)) return;
    this._ended = done;
    while (!done && this._seq - this._acked >= ACK_WINDOW && !this._signal.aborted) {
      await new Promise((r) => { this._waiters.push(r); setTimeout(r, 100); });
    }
    if (!done && this._ended) return;
    const b = { id: this._id, seq: ++this._seq };
    if (this._frame) b.frame = this._frame;
    if (this._lines.length) b.lines = this._lines;
    if (done) b.done = true;
    if (error) b.error = error;
    this._frame = null;
    this._lines = [];
    this._p._notify("$/stream", b);
  }
}

export class Plugin {
  constructor(name, version = "") {
    this.name = name;
    this.version = version;
    this._provides = {};
    this._sources = new Map();
    this._streams = new Map();
    this._actions = new Map();
    this._plans = new Map();
    this._importers = new Map();
    this._views = new Map();
    this._widgets = new Map();
    this._completers = new Map();
    this._config = {};
    this._next = 0;
    this._pending = new Map();
    this._running = new Map();
    this._outs = new Map();
  }

  _section(k) { return (this._provides[k] ??= {}); }
  _list(k) { return (this._provides[k] ??= []); }

  implements(...contracts) { this._list("implements").push(...contracts); return this; }
  emits(...topics) { this._list("events").push(...topics); return this; }

  source(name, fn, o = {}) {
    this._sources.set(name, fn);
    this._section("sources")[name] = prune({ description: o.description, cache: o.cache, language: o.language, args: schema(o.args, o.required) });
    return this;
  }
  stream(name, fn, o = {}) {
    this._streams.set(name, fn);
    this._section("sources")[name] = prune({ description: o.description, args: schema(o.args, o.required), stream: true });
    return this;
  }
  action(name, effect, fn, o = {}) {
    this._actions.set(name, fn);
    this._section("actions")[name] = prune({ effect, description: o.description, args: schema(o.args, o.required), keys: o.keys, on: o.on });
    return this;
  }
  plan(name, fn) {
    this._plans.set(name, fn);
    (this._section("actions")[name] ??= { effect: "write" }).plan = true;
    return this;
  }
  importer(name, files, fn) { this._importers.set(name, fn); this._section("importers")[name] = { files }; return this; }
  view(name, spec, fn) { this._views.set(name, fn); this._section("views")[name] = spec; return this; }
  onViewEvent(fn) { this._viewEvent = fn; return this; }
  widget(name, fn) { this._widgets.set(name, fn); this._list("widgets").push(name); return this; }
  completer(language, fn) { this._completers.set(language, fn); this._list("languages").push(language); return this; }
  resolver(schemes, fn) { this._resolve = fn; this._list("schemes").push(...schemes); return this; }
  searcher(fn) { this._search = fn; this._provides.search = true; return this; }
  doctor(fn) { this._doctor = fn; return this; }
  onEvent(fn) { this._event = fn; return this; }
  onConfigure(fn) { this._configure = fn; return this; }

  _send(m) { process.stdout.write(JSON.stringify({ jsonrpc: "2.0", ...m }) + "\n"); }
  _notify(method, params) { this._send({ method, params }); }

  _call(method, params, signal) {
    const id = ++this._next;
    return new Promise((resolve, reject) => {
      this._pending.set(id, { resolve, reject });
      signal?.addEventListener("abort", () => {
        if (this._pending.delete(id)) {
          this._notify("$/cancel", { id });
          reject(new RigError(ERRORS.Internal, "cancelled"));
        }
      });
      this._send({ id, method, params });
    });
  }

  // serve speaks rig/1 on stdin and stdout until shutdown or EOF. Log to stderr.
  serve() {
    const rl = createInterface({ input: process.stdin, crlfDelay: Infinity });
    const inflight = new Set();
    rl.on("line", (raw) => {
      if (!raw.trim()) return;
      let m;
      try {
        m = JSON.parse(raw);
      } catch (e) {
        this._send({ id: null, error: { code: ERRORS.ParseError, message: String(e) } });
        return;
      }
      if (!m.method) {
        const w = this._pending.get(m.id);
        this._pending.delete(m.id);
        if (w) m.error ? w.reject(new RigError(m.error.code, m.error.message, m.error.data?.hint)) : w.resolve(m.result ?? {});
      } else if (!("id" in m)) {
        this._notification(m.method, m.params ?? {});
      } else if (m.method === "shutdown") {
        this._running.forEach((c) => c.abort());
        Promise.allSettled([...inflight]).then(() => {
          this._send({ id: m.id, result: {} });
          rl.close();
          process.stdin.destroy();
        });
      } else {
        const p = this._request(m);
        inflight.add(p);
        p.finally(() => inflight.delete(p));
      }
    });
    rl.on("close", () => this._running.forEach((c) => c.abort()));
  }

  _notification(method, params) {
    if (method === "$/cancel") this._running.get(params.id)?.abort();
    else if (method === "$/ack") this._outs.get(params.id)?._ack(params.seq ?? 0);
  }

  async _request(m) {
    const ac = new AbortController();
    this._running.set(m.id, ac);
    try {
      const p = m.params ?? {};
      if (typeof p !== "object" || Array.isArray(p)) throw new RigError(ERRORS.InvalidParams, "params must be an object");
      const r = await this._dispatch(m.method, p, ac.signal);
      this._send({ id: m.id, result: r ?? {} });
    } catch (e) {
      if (e instanceof RigError) this._send({ id: m.id, error: e.wire() });
      else {
        console.error(e);
        this._send({ id: m.id, error: { code: ERRORS.Internal, message: String(e?.message ?? e) } });
      }
    } finally {
      this._running.delete(m.id);
    }
  }

  _str(p, k) {
    if (typeof p[k] !== "string") throw new RigError(ERRORS.InvalidParams, `${k} must be a string`);
    return p[k];
  }

  _find(table, what, name) {
    const fn = table.get(name);
    if (!fn) throw new RigError(ERRORS.NotFound, `no ${what} "${name}"`);
    return fn;
  }

  async _dispatch(method, p, signal) {
    const ctx = () => new Ctx(this, p, signal);
    switch (method) {
      case "initialize": {
        if (!(p.protocols ?? []).includes(VERSION)) throw new RigError(ERRORS.InvalidRequest, `${this.name} speaks ${VERSION}`);
        const caps = Object.entries({
          stream: this._streams.size, plan: this._plans.size, render: this._views.size, "widget.render": this._widgets.size,
          complete: this._completers.size, resolve: this._resolve, search: this._search, doctor: this._doctor,
          event: this._event, import: this._importers.size,
        }).filter(([, on]) => on).map(([c]) => c).sort();
        return prune({ name: this.name, version: this.version, protocol: VERSION, provides: this._provides, capabilities: caps });
      }
      case "configure":
        this._config = p;
        if (this._configure) await this._configure(ctx(), p);
        return {};
      case "query":
        return frame(await this._find(this._sources, "source", this._str(p, "source"))(ctx(), p.args ?? {}));
      case "stream":
        this._startStream(this._find(this._streams, "stream", this._str(p, "source")), this._str(p, "id"), p);
        return {};
      case "run":
      case "plan": {
        const table = method === "run" ? this._actions : this._plans;
        const r = await this._find(table, `${method} action`, this._str(p, "action"))(ctx(), p.args ?? {});
        return typeof r === "string" ? { message: r } : r ?? {};
      }
      case "import":
        return this._find(this._importers, "importer", this._str(p, "importer"))(ctx(), p);
      case "complete":
        return { items: (await this._find(this._completers, "language", this._str(p, "language"))(ctx(), p)) ?? [] };
      case "render":
        return this._find(this._views, "view", this._str(p, "view"))(ctx(), p);
      case "view.event":
        if (this._viewEvent) return this._viewEvent(ctx(), p);
        break;
      case "widget.render":
        return this._find(this._widgets, "widget", this._str(p, "widget"))(ctx(), p);
      case "resolve":
        if (!this._resolve) throw new RigError(ERRORS.NotFound, `no resolver for ${p.ref}`);
        return this._resolve(ctx(), this._str(p, "ref"));
      case "search":
        return { hits: (this._search ? await this._search(ctx(), p) : []) ?? [] };
      case "doctor":
        return { checks: (this._doctor ? await this._doctor(ctx()) : []) ?? [] };
      case "event":
        if (this._event) await this._event(ctx(), p);
        return {};
    }
    throw new RigError(ERRORS.MethodNotFound, `${this.name} does not implement ${method}`);
  }

  _startStream(fn, id, p) {
    const ac = new AbortController();
    const out = new Out(this, id, ac.signal);
    this._running.set(id, ac);
    this._outs.set(id, out);
    const tick = setInterval(() => out._flush(false), FLUSH_EVERY);
    (async () => {
      let err;
      try {
        await fn(new Ctx(this, p, ac.signal), p.args ?? {}, out);
      } catch (e) {
        if (e instanceof RigError) err = e.wire();
        else if (!ac.signal.aborted) {
          console.error(e);
          err = { code: ERRORS.Internal, message: String(e?.message ?? e) };
        }
      }
      clearInterval(tick);
      ac.abort();
      this._running.delete(id);
      this._outs.delete(id);
      await out._flush(true, err);
    })();
  }
}
