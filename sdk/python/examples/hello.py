"""hello: the example plugin each SDK implements the same way."""
import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))

from rig import Error, Plugin, protocol  # noqa: E402

p = Plugin("hello", version="0.1.0")


@p.source("greetings", description="one greeting", args={"name": {"type": "string", "description": "who to greet; world by default"}})
def greetings(ctx, args):
    return [{"name": args.get("name") or "world", "count": 1}]


@p.stream("ticks", description="a line every 10ms", args={"n": {"type": "integer", "description": "how many ticks; 0 ticks until cancelled"}})
def ticks(ctx, args, out):
    n, i = args.get("n", 0), 1
    while n == 0 or i <= n:
        if ctx.cancelled.wait(0.01):
            return
        out.line(f"tick {i}")
        i += 1


@p.action("greet", effect="write", description="greet someone", keys=["G"],
          args={"name": {"type": "string", "description": "who to greet"}}, required=["name"])
def greet(ctx, args):
    ctx.progress(1, 1, "greeting " + args["name"])
    return "hello " + args["name"]


@p.plan("greet")
def plan_greet(ctx, args):
    return {"changes": [{"op": "run", "target": "hello://" + args["name"]}]}


@p.resolver("hello")
def resolve(ctx, ref):
    if not ref.startswith("hello://"):
        raise Error(protocol.NOT_FOUND, "not a hello ref: " + ref)
    return {"ref": ref, "kind": "greeting", "title": "hello " + ref[len("hello://"):]}


@p.searcher
def search(ctx, q):
    return [{"ref": "hello://world", "title": "hello world"}] if q["query"] in "world" else []


@p.doctor
def doctor(ctx):
    return [{"name": "hello", "ok": True}]


p.serve()
