// Code generated from protocol/rig1.schema.json by go generate ./protocol; DO NOT EDIT.

export const VERSION = "rig/1";

export const METHODS = {
  "initialize": "host",
  "configure": "host",
  "query": "host",
  "stream": "host",
  "run": "host",
  "plan": "host",
  "import": "host",
  "complete": "host",
  "render": "host",
  "view.event": "host",
  "widget.render": "host",
  "resolve": "host",
  "search": "host",
  "doctor": "host",
  "event": "host",
  "shutdown": "host",
  "host/query": "plugin",
  "host/run": "plugin",
  "host/ask": "plugin",
  "host/ai": "plugin",
  "host/state.get": "plugin",
  "host/state.set": "plugin",
  "host/secret": "plugin",
  "host/secret.set": "plugin",
  "host/emit": "plugin",
  "host/log": "plugin",
  "$/cancel": "any",
  "$/progress": "plugin",
  "$/stream": "plugin",
  "$/ack": "host",
};

export const ERRORS = {
  ParseError: -32700,
  InvalidRequest: -32600,
  MethodNotFound: -32601,
  InvalidParams: -32602,
  Internal: -32603,
  Denied: -32001,
  NotFound: -32002,
  Unavailable: -32003,
  Conflict: -32004,
  NeedsInput: -32005,
};
