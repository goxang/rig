// hello: the example plugin each SDK implements the same way.
import { ERRORS, RigError, Plugin } from "../index.js";

const sleep = (ms, signal) => new Promise((r) => { const t = setTimeout(r, ms); signal.addEventListener("abort", () => { clearTimeout(t); r(); }); });

new Plugin("hello", "0.1.0")
  .source("greetings", (ctx, a) => [{ name: a.name || "world", count: 1 }], {
    description: "one greeting",
    args: { name: { type: "string", description: "who to greet; world by default" } },
  })
  .stream("ticks", async (ctx, a, out) => {
    for (let i = 1; !a.n || i <= a.n; i++) {
      await sleep(10, ctx.signal);
      if (ctx.signal.aborted) return;
      await out.line(`tick ${i}`);
    }
  }, { description: "a line every 10ms", args: { n: { type: "integer", description: "how many ticks; 0 ticks until cancelled" } } })
  .action("greet", "write", (ctx, a) => {
    ctx.progress(1, 1, "greeting " + a.name);
    return "hello " + a.name;
  }, { description: "greet someone", keys: ["G"], args: { name: { type: "string", description: "who to greet" } }, required: ["name"] })
  .plan("greet", (ctx, a) => ({ changes: [{ op: "run", target: "hello://" + a.name }] }))
  .resolver(["hello"], (ctx, ref) => {
    if (!ref.startsWith("hello://")) throw new RigError(ERRORS.NotFound, "not a hello ref: " + ref);
    return { ref, kind: "greeting", title: "hello " + ref.slice("hello://".length) };
  })
  .searcher((ctx, q) => ("world".includes(q.query) ? [{ ref: "hello://world", title: "hello world" }] : []))
  .doctor(() => [{ name: "hello", ok: true }])
  .serve();
