// Code generated from protocol/rig1.schema.json by go generate ./protocol; DO NOT EDIT.

export const VERSION: "rig/1";
export const METHODS: Record<string, "host" | "plugin" | "any">;
export const ERRORS: {
  readonly ParseError: -32700;
  readonly InvalidRequest: -32600;
  readonly MethodNotFound: -32601;
  readonly InvalidParams: -32602;
  readonly Internal: -32603;
  readonly Denied: -32001;
  readonly NotFound: -32002;
  readonly Unavailable: -32003;
  readonly Conflict: -32004;
  readonly NeedsInput: -32005;
};

/** No fields. */
export type Empty = Record<string, unknown>;

/** A URI whose scheme a plugin or core owns: svc://api, k8s://ns/deployment/api. */
export type Ref = string;

/** Arguments, validated by the source's or action's args schema. */
export type Args = Record<string, unknown>;

/** A JSON Schema. */
export type Schema = Record<string, unknown>;

/** One JSON-RPC 2.0 message: a request (id and method), a notification (method only) or a response (id and result or error). */
export interface Message {
  jsonrpc: "2.0";
  /** A number or a string; absent on notifications. */
  id?: unknown;
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: Error;
}

export interface Error {
  code: number;
  message: string;
  data?: ErrorData;
}

export interface ErrorData {
  /** What to do about it, such as the tool to install. */
  hint?: string;
}

/** Travels with every call. */
export interface Context {
  project?: string;
  env?: string;
  user?: string;
  roles?: string[];
  scope?: Scope;
  trace_id?: string;
  /** RFC 3339. */
  deadline?: string;
  dry_run?: boolean;
}

export interface Scope {
  service?: string;
  selection?: Ref[];
  range?: TimeRange;
}

export interface TimeRange {
  /** RFC 3339 or relative, such as -15m. */
  from?: string;
  to?: string;
  step?: string;
}

export interface HostInfo {
  name: string;
  version: string;
}

export interface InitializeParams {
  host: HostInfo;
  /** Protocol versions the host speaks, such as rig/1. */
  protocols: string[];
  capabilities?: HostCapabilities;
}

export interface HostCapabilities {
  /** Widget kind to the highest version the host renders. */
  widgets?: Record<string, number>;
  features?: string[];
  /** Contract versions the host has views for, such as metrics/1. */
  contracts?: string[];
}

export interface InitializeResult {
  name: string;
  version?: string;
  /** The protocol version chosen from the host's list. */
  protocol: string;
  provides?: Provides;
  /** Optional methods and features the plugin supports, such as stream or plan. */
  capabilities?: string[];
}

export interface Provides {
  sources?: Record<string, SourceSpec>;
  actions?: Record<string, ActionSpec>;
  importers?: Record<string, ImporterSpec>;
  views?: Record<string, ViewSpec>;
  widgets?: string[];
  schemes?: string[];
  languages?: string[];
  implements?: string[];
  events?: string[];
  search?: boolean;
}

export interface SourceSpec {
  description?: string;
  args?: Schema;
  /** How long a result stays fresh, such as 30s. */
  cache?: string;
  stream?: boolean;
  language?: string;
}

export interface ActionSpec {
  description?: string;
  effect: "read" | "write" | "danger";
  args?: Schema;
  /** Supports a dry run. */
  plan?: boolean;
  keys?: string[];
  /** Schemes, kinds or sources whose menus list it. */
  on?: string[];
}

export interface ImporterSpec {
  description?: string;
  files?: string[];
}

export interface ViewSpec {
  title?: string;
  /** tab, subtab:<view>, service.section, detail:<scheme>, panel or home. */
  slot?: string;
  key?: string;
}

export interface ConfigureParams {
  config?: Args;
  components?: Record<string, Args>;
  secrets?: Record<string, string>;
  context?: Context;
}

export interface Field {
  name: string;
  type: "string" | "number" | "int" | "bool" | "time" | "duration" | "bytes" | "json" | "ref";
  unit?: string;
  /** A ref template over the row, such as svc://{service}. */
  ref?: string;
}

export interface FrameMeta {
  total?: number;
  /** Cursor for the next page; empty when there is none. */
  next?: string;
  notice?: string;
}

/** Columnar, typed, renderer-neutral data. columns[i] holds every value of fields[i]. time is Unix seconds, duration is seconds. */
export interface Frame {
  name?: string;
  fields: Field[];
  columns: unknown[][];
  meta?: FrameMeta;
}

export interface QueryParams {
  source: string;
  component?: string;
  args?: Args;
  cursor?: string;
  context?: Context;
}

export interface StreamParams {
  /** Names the stream in $/stream, $/ack and $/cancel. */
  id: string;
  source: string;
  component?: string;
  args?: Args;
  context?: Context;
}

export interface StreamBatch {
  id: string;
  /** Starts at 1; $/ack names the last seq the host consumed. */
  seq: number;
  frame?: Frame;
  lines?: string[];
  done?: boolean;
  error?: Error;
}

export interface Ack {
  id: string;
  seq: number;
}

export interface RunParams {
  action: string;
  component?: string;
  args?: Args;
  /** The job id progress is reported against. */
  job?: string;
  context?: Context;
}

export interface RunResult {
  message?: string;
  frame?: Frame;
  refs?: Ref[];
  outputs?: Args;
  /** What a plan would do, or what a run did. */
  changes?: Change[];
}

export interface Change {
  op: "create" | "update" | "delete" | "run";
  target: Ref;
  detail?: string;
}

export interface Progress {
  job: string;
  message?: string;
  /** One line of output. */
  line?: string;
  done?: number;
  total?: number;
}

export interface CancelParams {
  /** The request id, or a stream id. */
  id: unknown;
}

export interface ImportParams {
  importer: string;
  files?: string[];
  dir?: string;
  config?: Args;
}

export interface ImportResult {
  /** A rig.yaml fragment. */
  fragment: Args;
  /** Files whose change re-runs the import. */
  watch?: string[];
  warnings?: string[];
}

export interface CompleteParams {
  language: string;
  text: string;
  /** Byte offset of the cursor. */
  pos: number;
  context?: Context;
}

export interface Completion {
  label: string;
  insert?: string;
  detail?: string;
  kind?: string;
}

export interface CompleteResult {
  items: Completion[];
}

/** A widget tree node. */
export interface Node {
  /** A widget kind, optionally versioned: table@1. */
  widget: string;
  id?: string;
  props?: Args;
  bind?: Binding;
  children?: Node[];
  /** What a host without this widget renders. */
  fallback?: Node;
}

export interface Binding {
  source?: string;
  args?: Args;
  every?: string;
}

export interface RenderParams {
  view: string;
  args?: Args;
  context?: Context;
}

export interface ViewEventParams {
  view: string;
  node?: string;
  event: string;
  value?: unknown;
  context?: Context;
}

export interface Size {
  w: number;
  h: number;
}

export interface WidgetRenderParams {
  widget: string;
  props?: Args;
  frame?: Frame;
  size: Size;
  /** Theme token to colour. */
  theme?: Record<string, string>;
}

export interface WidgetRenderResult {
  /** At most size.h lines of at most size.w cells; ANSI SGR allowed. */
  lines: string[];
}

export interface ResolveParams {
  ref: Ref;
  context?: Context;
}

export interface Link {
  ref: Ref;
  rel?: string;
  title?: string;
}

export interface Resolved {
  ref: Ref;
  kind?: string;
  title?: string;
  view?: string;
  url?: string;
  fields?: Args;
  actions?: string[];
  links?: Link[];
}

export interface SearchParams {
  query: string;
  limit?: number;
  context?: Context;
}

export interface Hit {
  ref: Ref;
  title: string;
  kind?: string;
  detail?: string;
}

export interface SearchResult {
  hits: Hit[];
}

export interface DoctorParams {
  context?: Context;
}

export interface Check {
  name: string;
  ok: boolean;
  message?: string;
  hint?: string;
}

export interface DoctorResult {
  checks: Check[];
}

export interface Event {
  /** Such as deploy.finished or alert.firing. */
  topic: string;
  time?: string;
  source?: string;
  refs?: Ref[];
  data?: Args;
}

export interface AskParams {
  kind: "input" | "confirm" | "pick" | "secret";
  prompt: string;
  options?: string[];
  default?: string;
}

export interface AskResult {
  /** False when the user cancelled. */
  ok: boolean;
  value?: string;
}

export interface AIParams {
  prompt: string;
  system?: string;
  max_tokens?: number;
}

export interface AIResult {
  text: string;
}

export interface StateGetParams {
  key: string;
}

export interface StateValue {
  value?: unknown;
  found?: boolean;
}

export interface StateSetParams {
  key: string;
  value?: unknown;
}

export interface SecretParams {
  name: string;
}

export interface SecretSetParams {
  name: string;
  /** empty removes the secret */
  value: string;
}

export interface SecretValue {
  value: string;
}

export interface LogParams {
  level: "debug" | "info" | "warn" | "error";
  message: string;
}
