"""Write rig plugins in Python: register sources, streams, actions and the rest, then serve().

Standard library only. The protocol (rig/1), not this module, is the contract.
"""
from __future__ import annotations

import json
import sys
import threading
import traceback
from typing import Any, Callable

from . import protocol
from .protocol import VERSION

__all__ = ["Plugin", "Ctx", "Out", "Error", "frame", "VERSION", "protocol"]

FLUSH_EVERY = 0.05
FLUSH_ROWS = 500
ACK_WINDOW = 8


class Error(Exception):
    """A protocol error: raise Error(protocol.NOT_FOUND, "no such thing")."""

    def __init__(self, code: int, message: str, hint: str = ""):
        super().__init__(message)
        self.code, self.message, self.hint = code, message, hint

    def wire(self) -> dict:
        e = {"code": self.code, "message": self.message}
        if self.hint:
            e["data"] = {"hint": self.hint}
        return e


def unavailable(message: str, hint: str) -> Error:
    return Error(protocol.UNAVAILABLE, message, hint)


def _type_of(v: Any) -> str:
    if isinstance(v, bool):
        return "bool"
    if isinstance(v, int):
        return "int"
    if isinstance(v, float):
        return "number"
    if isinstance(v, str):
        return "string"
    return "json"


def frame(rows: list[dict] | dict) -> dict:
    """A frame from a list of dicts (fields from the first row's keys), or a frame as it is."""
    if isinstance(rows, dict):
        return rows
    if not rows:
        return {"fields": [], "columns": []}
    names = list(rows[0].keys())
    fields = [{"name": n, "type": _type_of(rows[0][n])} for n in names]
    return {"fields": fields, "columns": [[r.get(n) for r in rows] for n in names]}


def _schema(args: dict | None, required: list[str] | None) -> dict | None:
    if args is None and not required:
        return None
    s: dict = {"type": "object", "properties": args or {}}
    if required:
        s["required"] = list(required)
    return s


class _Window:
    def __init__(self) -> None:
        self.cond = threading.Condition()
        self.acked = 0

    def ack(self, seq: int) -> None:
        with self.cond:
            self.acked = max(self.acked, seq)
            self.cond.notify_all()


class Ctx:
    """One call: its cancellation, the caller's context and the way back to the host."""

    def __init__(self, plugin: "Plugin", call: dict | None, component: str = "", job: str = "", cursor: str = ""):
        self._p = plugin
        self.call = call or {}
        self.component = component
        self.cursor = cursor
        self.job = job
        self.cancelled = threading.Event()

    @property
    def config(self) -> dict:
        return self._p._config

    def spec(self) -> dict:
        return self._p._config.get("components", {}).get(self.component, {})

    def progress(self, done: int, total: int, message: str = "") -> None:
        self._p._notify("$/progress", {"job": self.job, "done": done, "total": total, "message": message})

    def line(self, s: str) -> None:
        self._p._notify("$/progress", {"job": self.job, "line": s})

    def log(self, level: str, message: str) -> None:
        self._p._notify("host/log", {"level": level, "message": message})

    def query(self, source: str, args: dict | None = None) -> dict:
        return self._p._call("host/query", {"source": source, "args": args or {}, "context": self.call}, self)

    def run(self, action: str, args: dict | None = None) -> dict:
        return self._p._call("host/run", {"action": action, "args": args or {}, "context": self.call}, self)

    def ask(self, kind: str, prompt: str, options: list[str] | None = None) -> dict:
        return self._p._call("host/ask", {"kind": kind, "prompt": prompt, "options": options or []}, self)

    def ai(self, prompt: str, system: str = "") -> str:
        return self._p._call("host/ai", {"prompt": prompt, "system": system}, self)["text"]

    def state(self, key: str) -> dict:
        return self._p._call("host/state.get", {"key": key}, self)

    def set_state(self, key: str, value: Any) -> None:
        self._p._call("host/state.set", {"key": key, "value": value}, self)

    def secret(self, name: str) -> str:
        return self._p._call("host/secret", {"name": name}, self)["value"]

    def set_secret(self, name: str, value: str) -> None:
        self._p._call("host/secret.set", {"name": name, "value": value}, self)

    def emit(self, topic: str, data: dict | None = None) -> None:
        self._p._call("host/emit", {"topic": topic, "data": data or {}}, self)


class Out:
    """A stream's output: frames and lines, batched every 50 ms or 500 rows."""

    def __init__(self, plugin: "Plugin", ctx: Ctx, sid: str):
        self._p, self._ctx, self._id = plugin, ctx, sid
        self._win = _Window()
        self._lock = threading.Lock()
        self._seq = 0
        self._frame: dict | None = None
        self._lines: list[str] = []
        self._ended = False

    def _rows(self) -> int:
        n = len(self._lines)
        if self._frame and self._frame["columns"]:
            n += len(self._frame["columns"][0])
        return n

    def frame(self, rows: list[dict] | dict) -> None:
        f = frame(rows)
        with self._lock:
            if self._frame is not None and self._frame["fields"] != f["fields"]:
                self._flush(False)
            if self._frame is None:
                self._frame = {"fields": f["fields"], "columns": [[] for _ in f["fields"]]}
            for i, col in enumerate(f["columns"]):
                self._frame["columns"][i].extend(col)
            if self._rows() >= FLUSH_ROWS:
                self._flush(False)

    def line(self, s: str) -> None:
        with self._lock:
            self._lines.append(s)
            if self._rows() >= FLUSH_ROWS:
                self._flush(False)

    def _flush(self, done: bool, error: dict | None = None) -> None:
        if self._ended or (not done and self._frame is None and not self._lines):
            return
        self._ended = done
        with self._win.cond:
            while not done and self._seq - self._win.acked >= ACK_WINDOW and not self._ctx.cancelled.is_set():
                self._win.cond.wait(0.1)
        self._seq += 1
        b: dict = {"id": self._id, "seq": self._seq}
        if self._frame is not None:
            b["frame"] = self._frame
        if self._lines:
            b["lines"] = self._lines
        if done:
            b["done"] = True
        if error:
            b["error"] = error
        self._p._notify("$/stream", b)
        self._frame, self._lines = None, []


class Plugin:
    def __init__(self, name: str, version: str = ""):
        self.name, self.version = name, version
        self._provides: dict = {}
        self._sources: dict[str, Callable] = {}
        self._streams: dict[str, Callable] = {}
        self._actions: dict[str, Callable] = {}
        self._plans: dict[str, Callable] = {}
        self._importers: dict[str, Callable] = {}
        self._views: dict[str, Callable] = {}
        self._widgets: dict[str, Callable] = {}
        self._completers: dict[str, Callable] = {}
        self._view_event: Callable | None = None
        self._resolve: Callable | None = None
        self._search: Callable | None = None
        self._doctor: Callable | None = None
        self._event: Callable | None = None
        self._configure: Callable | None = None
        self._config: dict = {}
        self._wlock = threading.Lock()
        self._lock = threading.Lock()
        self._next = 0
        self._pending: dict[Any, list] = {}
        self._running: dict[Any, Ctx] = {}
        self._windows: dict[str, _Window] = {}
        self._out = sys.stdout

    def _section(self, key: str) -> dict:
        return self._provides.setdefault(key, {})

    def implements(self, *contracts: str) -> "Plugin":
        self._provides.setdefault("implements", []).extend(contracts)
        return self

    def emits(self, *topics: str) -> "Plugin":
        self._provides.setdefault("events", []).extend(topics)
        return self

    def source(self, name: str, description: str = "", args: dict | None = None, required: list[str] | None = None,
               cache: str = "", language: str = ""):
        def register(fn):
            self._sources[name] = fn
            self._source_spec(name, False, description, args, required, cache, language)
            return fn
        return register

    def stream(self, name: str, description: str = "", args: dict | None = None, required: list[str] | None = None):
        def register(fn):
            self._streams[name] = fn
            self._source_spec(name, True, description, args, required, "", "")
            return fn
        return register

    def _source_spec(self, name, stream, description, args, required, cache, language):
        s = self._section("sources").setdefault(name, {})
        for k, v in (("description", description), ("cache", cache), ("language", language), ("args", _schema(args, required))):
            if v:
                s[k] = v
        if stream:
            s["stream"] = True

    def action(self, name: str, effect: str = "write", description: str = "", args: dict | None = None,
               required: list[str] | None = None, keys: list[str] | None = None, on: list[str] | None = None):
        def register(fn):
            self._actions[name] = fn
            a = self._section("actions").setdefault(name, {})
            a["effect"] = effect
            for k, v in (("description", description), ("args", _schema(args, required)), ("keys", keys), ("on", on)):
                if v:
                    a[k] = v
            return fn
        return register

    def plan(self, name: str):
        def register(fn):
            self._plans[name] = fn
            self._section("actions").setdefault(name, {"effect": "write"})["plan"] = True
            return fn
        return register

    def importer(self, name: str, files: list[str] | None = None):
        def register(fn):
            self._importers[name] = fn
            self._section("importers")[name] = {"files": files or []}
            return fn
        return register

    def view(self, name: str, slot: str = "tab", title: str = "", key: str = ""):
        def register(fn):
            self._views[name] = fn
            spec = {"slot": slot}
            if title:
                spec["title"] = title
            if key:
                spec["key"] = key
            self._section("views")[name] = spec
            return fn
        return register

    def on_view_event(self, fn):
        self._view_event = fn
        return fn

    def widget(self, name: str):
        def register(fn):
            self._widgets[name] = fn
            self._provides.setdefault("widgets", []).append(name)
            return fn
        return register

    def completer(self, language: str):
        def register(fn):
            self._completers[language] = fn
            self._provides.setdefault("languages", []).append(language)
            return fn
        return register

    def resolver(self, *schemes: str):
        def register(fn):
            self._resolve = fn
            self._provides.setdefault("schemes", []).extend(schemes)
            return fn
        return register

    def searcher(self, fn):
        self._search = fn
        self._provides["search"] = True
        return fn

    def doctor(self, fn):
        self._doctor = fn
        return fn

    def on_event(self, fn):
        self._event = fn
        return fn

    def on_configure(self, fn):
        self._configure = fn
        return fn

    # wire

    def _send(self, m: dict) -> None:
        m["jsonrpc"] = "2.0"
        line = json.dumps(m, separators=(",", ":"), default=str)
        with self._wlock:
            self._out.write(line + "\n")
            self._out.flush()

    def _notify(self, method: str, params: dict) -> None:
        self._send({"method": method, "params": params})

    def _call(self, method: str, params: dict, ctx: Ctx | None = None) -> Any:
        ev = threading.Event()
        slot: list = [ev, None, None]
        with self._lock:
            self._next += 1
            rid = self._next
            self._pending[rid] = slot
        self._send({"id": rid, "method": method, "params": params})
        while not ev.wait(0.05):
            if ctx is not None and ctx.cancelled.is_set():
                with self._lock:
                    self._pending.pop(rid, None)
                self._notify("$/cancel", {"id": rid})
                raise Error(protocol.INTERNAL, "cancelled")
        if slot[2] is not None:
            e = slot[2]
            raise Error(e.get("code", protocol.INTERNAL), e.get("message", ""), (e.get("data") or {}).get("hint", ""))
        return slot[1] or {}

    def serve(self) -> None:
        """Speak rig/1 on stdin and stdout until shutdown or EOF. Log to stderr."""
        threads: list[threading.Thread] = []
        for raw in sys.stdin.buffer:
            if not raw.strip():
                continue
            try:
                m = json.loads(raw)
            except ValueError as e:
                self._send({"id": None, "error": {"code": protocol.PARSE_ERROR, "message": str(e)}})
                continue
            method = m.get("method")
            if not method:
                with self._lock:
                    slot = self._pending.pop(m.get("id"), None)
                if slot:
                    slot[1], slot[2] = m.get("result"), m.get("error")
                    slot[0].set()
            elif "id" not in m:
                self._notification(method, m.get("params") or {})
            elif method == "shutdown":
                self._cancel_all()
                for t in threads:
                    t.join(5)
                self._send({"id": m["id"], "result": {}})
                return
            else:
                t = threading.Thread(target=self._request, args=(m,), daemon=True)
                t.start()
                threads = [x for x in threads if x.is_alive()] + [t]
        self._cancel_all()

    def _cancel_all(self) -> None:
        with self._lock:
            for c in self._running.values():
                c.cancelled.set()

    def _notification(self, method: str, params: dict) -> None:
        if method == "$/cancel":
            with self._lock:
                c = self._running.get(params.get("id"))
            if c:
                c.cancelled.set()
        elif method == "$/ack":
            with self._lock:
                w = self._windows.get(params.get("id"))
            if w:
                w.ack(params.get("seq", 0))

    def _request(self, m: dict) -> None:
        rid = m["id"]
        ctx_holder: list[Ctx] = []
        try:
            params = m.get("params") or {}
            if not isinstance(params, dict):
                raise Error(protocol.INVALID_PARAMS, "params must be an object")
            result = self._dispatch(rid, m["method"], params, ctx_holder)
            self._send({"id": rid, "result": {} if result is None else result})
        except Error as e:
            self._send({"id": rid, "error": e.wire()})
        except Exception as e:  # noqa: BLE001 - every failure becomes an answer
            traceback.print_exc(file=sys.stderr)
            self._send({"id": rid, "error": {"code": protocol.INTERNAL, "message": f"{type(e).__name__}: {e}"}})
        finally:
            with self._lock:
                self._running.pop(rid, None)

    def _ctx(self, rid, holder, params, component="", job="", cursor="") -> Ctx:
        c = Ctx(self, params.get("context"), component, job, cursor)
        with self._lock:
            self._running[rid] = c
        holder.append(c)
        return c

    @staticmethod
    def _str(params: dict, key: str) -> str:
        v = params.get(key)
        if not isinstance(v, str):
            raise Error(protocol.INVALID_PARAMS, f"{key} must be a string")
        return v

    @staticmethod
    def _find(table: dict, what: str, name: str) -> Callable:
        fn = table.get(name)
        if fn is None:
            raise Error(protocol.NOT_FOUND, f"no {what} {name!r}")
        return fn

    def _dispatch(self, rid, method: str, p: dict, holder: list) -> Any:
        if method == "initialize":
            if VERSION not in (p.get("protocols") or []):
                raise Error(protocol.INVALID_REQUEST, f"{self.name} speaks {VERSION}")
            caps = sorted(c for c, on in {
                "stream": self._streams, "plan": self._plans, "render": self._views, "widget.render": self._widgets,
                "complete": self._completers, "resolve": self._resolve, "search": self._search,
                "doctor": self._doctor, "event": self._event, "import": self._importers}.items() if on)
            r = {"name": self.name, "protocol": VERSION, "provides": self._provides, "capabilities": caps}
            if self.version:
                r["version"] = self.version
            return r
        if method == "configure":
            self._config = p
            if self._configure:
                self._configure(self._ctx(rid, holder, p), p)
            return {}
        if method == "query":
            fn = self._find(self._sources, "source", self._str(p, "source"))
            ctx = self._ctx(rid, holder, p, p.get("component", ""), cursor=p.get("cursor", ""))
            return frame(fn(ctx, p.get("args") or {}))
        if method == "stream":
            fn = self._find(self._streams, "stream", self._str(p, "source"))
            self._start_stream(fn, self._str(p, "id"), p)
            return {}
        if method in ("run", "plan"):
            table = self._actions if method == "run" else self._plans
            fn = self._find(table, method + " action", self._str(p, "action"))
            r = fn(self._ctx(rid, holder, p, p.get("component", ""), p.get("job", "")), p.get("args") or {})
            return {"message": r} if isinstance(r, str) else (r or {})
        if method == "import":
            fn = self._find(self._importers, "importer", self._str(p, "importer"))
            return fn(self._ctx(rid, holder, p), p)
        if method == "complete":
            fn = self._find(self._completers, "language", self._str(p, "language"))
            return {"items": fn(self._ctx(rid, holder, p), p) or []}
        if method == "render":
            return self._find(self._views, "view", self._str(p, "view"))(self._ctx(rid, holder, p), p)
        if method == "view.event" and self._view_event:
            return self._view_event(self._ctx(rid, holder, p), p)
        if method == "widget.render":
            return self._find(self._widgets, "widget", self._str(p, "widget"))(self._ctx(rid, holder, p), p)
        if method == "resolve":
            if not self._resolve:
                raise Error(protocol.NOT_FOUND, f"no resolver for {p.get('ref')}")
            return self._resolve(self._ctx(rid, holder, p), self._str(p, "ref"))
        if method == "search":
            hits = self._search(self._ctx(rid, holder, p), p) if self._search else []
            return {"hits": hits or []}
        if method == "doctor":
            return {"checks": (self._doctor(self._ctx(rid, holder, p)) if self._doctor else None) or []}
        if method == "event":
            if self._event:
                self._event(self._ctx(rid, holder, p), p)
            return {}
        raise Error(protocol.METHOD_NOT_FOUND, f"{self.name} does not implement {method}")

    def _start_stream(self, fn: Callable, sid: str, p: dict) -> None:
        ctx = Ctx(self, p.get("context"), p.get("component", ""))
        out = Out(self, ctx, sid)
        with self._lock:
            self._running[sid] = ctx
            self._windows[sid] = out._win

        def tick():
            while not ctx.cancelled.wait(FLUSH_EVERY):
                with out._lock:
                    out._flush(False)

        def body():
            err = None
            try:
                fn(ctx, p.get("args") or {}, out)
            except Error as e:
                err = e.wire()
            except Exception as e:  # noqa: BLE001
                traceback.print_exc(file=sys.stderr)
                err = {"code": protocol.INTERNAL, "message": f"{type(e).__name__}: {e}"}
            ctx.cancelled.set()
            with self._lock:
                self._running.pop(sid, None)
                self._windows.pop(sid, None)
            with out._lock:
                out._flush(True, err)

        threading.Thread(target=tick, daemon=True).start()
        threading.Thread(target=body, daemon=True).start()
