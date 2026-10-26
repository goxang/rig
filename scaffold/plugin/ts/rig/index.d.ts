import type * as P from "./protocol.js";
export * from "./protocol.js";
export type { P as protocol };

type Awaitable<T> = T | Promise<T>;
type Rows = Record<string, unknown>[] | P.Frame;

export class RigError extends Error {
  constructor(code: number, message: string, hint?: string);
  code: number;
  hint: string;
}
export function unavailable(message: string, hint: string): RigError;
/** A frame from an array of objects (fields from the first row's keys), or a frame as it is. */
export function frame(rows: Rows): P.Frame;

export class Ctx {
  /** Who called and where: project, env, user, scope. */
  call: P.Context;
  component: string;
  cursor: string;
  job: string;
  signal: AbortSignal;
  readonly config: P.ConfigureParams;
  spec(): P.Args;
  progress(done: number, total: number, message?: string): void;
  line(s: string): void;
  log(level: "debug" | "info" | "warn" | "error", message: string): void;
  query(source: string, args?: P.Args): Promise<P.Frame>;
  run(action: string, args?: P.Args): Promise<P.RunResult>;
  ask(kind: P.AskParams["kind"], prompt: string, options?: string[]): Promise<P.AskResult>;
  ai(prompt: string, system?: string): Promise<string>;
  state(key: string): Promise<P.StateValue>;
  setState(key: string, value: unknown): Promise<void>;
  secret(name: string): Promise<string>;
  setSecret(name: string, value: string): Promise<void>;
  emit(topic: string, data?: P.Args): Promise<void>;
}

export class Out {
  frame(rows: Rows): Promise<void>;
  line(s: string): Promise<void>;
}

interface ArgOpts {
  description?: string;
  /** JSON Schema properties of the arguments. */
  args?: Record<string, unknown>;
  required?: string[];
}

export class Plugin {
  constructor(name: string, version?: string);
  implements(...contracts: string[]): this;
  emits(...topics: string[]): this;
  source<A = P.Args>(name: string, fn: (ctx: Ctx, args: A) => Awaitable<Rows>, o?: ArgOpts & { cache?: string; language?: string }): this;
  stream<A = P.Args>(name: string, fn: (ctx: Ctx, args: A, out: Out) => Awaitable<void>, o?: ArgOpts): this;
  action<A = P.Args>(name: string, effect: P.ActionSpec["effect"], fn: (ctx: Ctx, args: A) => Awaitable<P.RunResult | string>, o?: ArgOpts & { keys?: string[]; on?: string[] }): this;
  plan<A = P.Args>(name: string, fn: (ctx: Ctx, args: A) => Awaitable<P.RunResult>): this;
  importer(name: string, files: string[], fn: (ctx: Ctx, p: P.ImportParams) => Awaitable<P.ImportResult>): this;
  view(name: string, spec: P.ViewSpec, fn: (ctx: Ctx, p: P.RenderParams) => Awaitable<P.Node>): this;
  onViewEvent(fn: (ctx: Ctx, p: P.ViewEventParams) => Awaitable<P.Node>): this;
  widget(name: string, fn: (ctx: Ctx, p: P.WidgetRenderParams) => Awaitable<P.WidgetRenderResult>): this;
  completer(language: string, fn: (ctx: Ctx, p: P.CompleteParams) => Awaitable<P.Completion[]>): this;
  resolver(schemes: string[], fn: (ctx: Ctx, ref: P.Ref) => Awaitable<P.Resolved>): this;
  searcher(fn: (ctx: Ctx, p: P.SearchParams) => Awaitable<P.Hit[]>): this;
  doctor(fn: (ctx: Ctx) => Awaitable<P.Check[]>): this;
  onEvent(fn: (ctx: Ctx, e: P.Event) => Awaitable<void>): this;
  onConfigure(fn: (ctx: Ctx, p: P.ConfigureParams) => Awaitable<void>): this;
  /** Speak rig/1 on stdin and stdout until shutdown or EOF. Log to stderr. */
  serve(): void;
}
