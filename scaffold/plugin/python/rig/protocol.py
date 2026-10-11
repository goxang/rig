# Code generated from protocol/rig1.schema.json by go generate ./protocol; DO NOT EDIT.
from __future__ import annotations

from typing import Any, TypedDict

VERSION = "rig/1"

METHODS = {
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
    "host/emit": "plugin",
    "host/log": "plugin",
    "$/cancel": "any",
    "$/progress": "plugin",
    "$/stream": "plugin",
    "$/ack": "host",
}

PARSE_ERROR = -32700
INVALID_REQUEST = -32600
METHOD_NOT_FOUND = -32601
INVALID_PARAMS = -32602
INTERNAL = -32603
DENIED = -32001
NOT_FOUND = -32002
UNAVAILABLE = -32003
CONFLICT = -32004
NEEDS_INPUT = -32005


Empty = dict[str, Any]


Ref = str


Args = dict[str, Any]


Schema = dict[str, Any]


class Message(TypedDict, total=False):
    "One JSON-RPC 2.0 message: a request (id and method), a notification (method only) or a response (id and result or error)."

    jsonrpc: str
    id: Any
    method: str
    params: Any
    result: Any
    error: Error


class Error(TypedDict, total=False):
    code: int
    message: str
    data: ErrorData


class ErrorData(TypedDict, total=False):
    hint: str


class Context(TypedDict, total=False):
    "Travels with every call."

    project: str
    env: str
    user: str
    roles: list[str]
    scope: Scope
    trace_id: str
    deadline: str
    dry_run: bool


class Scope(TypedDict, total=False):
    service: str
    selection: list[Ref]
    range: TimeRange


TimeRange = TypedDict("TimeRange", {"from": str, "to": str, "step": str}, total=False)


class HostInfo(TypedDict, total=False):
    name: str
    version: str


class InitializeParams(TypedDict, total=False):
    host: HostInfo
    protocols: list[str]
    capabilities: HostCapabilities


class HostCapabilities(TypedDict, total=False):
    widgets: dict[str, int]
    features: list[str]
    contracts: list[str]


class InitializeResult(TypedDict, total=False):
    name: str
    version: str
    protocol: str
    provides: Provides
    capabilities: list[str]


class Provides(TypedDict, total=False):
    sources: dict[str, SourceSpec]
    actions: dict[str, ActionSpec]
    importers: dict[str, ImporterSpec]
    views: dict[str, ViewSpec]
    widgets: list[str]
    schemes: list[str]
    languages: list[str]
    implements: list[str]
    events: list[str]
    search: bool


class SourceSpec(TypedDict, total=False):
    description: str
    args: Schema
    cache: str
    stream: bool
    language: str


class ActionSpec(TypedDict, total=False):
    description: str
    effect: str
    args: Schema
    plan: bool
    keys: list[str]
    on: list[str]


class ImporterSpec(TypedDict, total=False):
    description: str
    files: list[str]


class ViewSpec(TypedDict, total=False):
    title: str
    slot: str
    key: str


class ConfigureParams(TypedDict, total=False):
    config: Args
    components: dict[str, Args]
    secrets: dict[str, str]
    context: Context


class Field(TypedDict, total=False):
    name: str
    type: str
    unit: str
    ref: str


class FrameMeta(TypedDict, total=False):
    total: int
    next: str
    notice: str


class Frame(TypedDict, total=False):
    "Columnar, typed, renderer-neutral data. columns[i] holds every value of fields[i]. time is Unix seconds, duration is seconds."

    name: str
    fields: list[Field]
    columns: list[list[Any]]
    meta: FrameMeta


class QueryParams(TypedDict, total=False):
    source: str
    component: str
    args: Args
    cursor: str
    context: Context


class StreamParams(TypedDict, total=False):
    id: str
    source: str
    component: str
    args: Args
    context: Context


class StreamBatch(TypedDict, total=False):
    id: str
    seq: int
    frame: Frame
    lines: list[str]
    done: bool
    error: Error


class Ack(TypedDict, total=False):
    id: str
    seq: int


class RunParams(TypedDict, total=False):
    action: str
    component: str
    args: Args
    job: str
    context: Context


class RunResult(TypedDict, total=False):
    message: str
    frame: Frame
    refs: list[Ref]
    outputs: Args
    changes: list[Change]


class Change(TypedDict, total=False):
    op: str
    target: Ref
    detail: str


class Progress(TypedDict, total=False):
    job: str
    message: str
    line: str
    done: int
    total: int


class CancelParams(TypedDict, total=False):
    id: Any


class ImportParams(TypedDict, total=False):
    importer: str
    files: list[str]
    dir: str
    config: Args


class ImportResult(TypedDict, total=False):
    fragment: Args
    watch: list[str]
    warnings: list[str]


class CompleteParams(TypedDict, total=False):
    language: str
    text: str
    pos: int
    context: Context


class Completion(TypedDict, total=False):
    label: str
    insert: str
    detail: str
    kind: str


class CompleteResult(TypedDict, total=False):
    items: list[Completion]


class Node(TypedDict, total=False):
    "A widget tree node."

    widget: str
    id: str
    props: Args
    bind: Binding
    children: list[Node]
    fallback: Node


class Binding(TypedDict, total=False):
    source: str
    args: Args
    every: str


class RenderParams(TypedDict, total=False):
    view: str
    args: Args
    context: Context


class ViewEventParams(TypedDict, total=False):
    view: str
    node: str
    event: str
    value: Any
    context: Context


class Size(TypedDict, total=False):
    w: int
    h: int


class WidgetRenderParams(TypedDict, total=False):
    widget: str
    props: Args
    frame: Frame
    size: Size
    theme: dict[str, str]


class WidgetRenderResult(TypedDict, total=False):
    lines: list[str]


class ResolveParams(TypedDict, total=False):
    ref: Ref
    context: Context


class Link(TypedDict, total=False):
    ref: Ref
    rel: str
    title: str


class Resolved(TypedDict, total=False):
    ref: Ref
    kind: str
    title: str
    view: str
    url: str
    fields: Args
    actions: list[str]
    links: list[Link]


class SearchParams(TypedDict, total=False):
    query: str
    limit: int
    context: Context


class Hit(TypedDict, total=False):
    ref: Ref
    title: str
    kind: str
    detail: str


class SearchResult(TypedDict, total=False):
    hits: list[Hit]


class DoctorParams(TypedDict, total=False):
    context: Context


class Check(TypedDict, total=False):
    name: str
    ok: bool
    message: str
    hint: str


class DoctorResult(TypedDict, total=False):
    checks: list[Check]


class Event(TypedDict, total=False):
    topic: str
    time: str
    source: str
    refs: list[Ref]
    data: Args


class AskParams(TypedDict, total=False):
    kind: str
    prompt: str
    options: list[str]
    default: str


class AskResult(TypedDict, total=False):
    ok: bool
    value: str


class AIParams(TypedDict, total=False):
    prompt: str
    system: str
    max_tokens: int


class AIResult(TypedDict, total=False):
    text: str


class StateGetParams(TypedDict, total=False):
    key: str


class StateValue(TypedDict, total=False):
    value: Any
    found: bool


class StateSetParams(TypedDict, total=False):
    key: str
    value: Any


class SecretParams(TypedDict, total=False):
    name: str


class SecretValue(TypedDict, total=False):
    value: str


class LogParams(TypedDict, total=False):
    level: str
    message: str
