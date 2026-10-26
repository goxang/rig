// Code generated from protocol/rig1.schema.json by go generate ./protocol; DO NOT EDIT.

package protocol

// Version is the protocol this package speaks.
const Version = "rig/1"

const (
	MethodInitialize     = "initialize"
	MethodConfigure      = "configure"
	MethodQuery          = "query"
	MethodStream         = "stream"
	MethodRun            = "run"
	MethodPlan           = "plan"
	MethodImport         = "import"
	MethodComplete       = "complete"
	MethodRender         = "render"
	MethodViewEvent      = "view.event"
	MethodWidgetRender   = "widget.render"
	MethodResolve        = "resolve"
	MethodSearch         = "search"
	MethodDoctor         = "doctor"
	MethodEvent          = "event"
	MethodShutdown       = "shutdown"
	MethodHostQuery      = "host/query"
	MethodHostRun        = "host/run"
	MethodHostAsk        = "host/ask"
	MethodHostAI         = "host/ai"
	MethodHostStateGet   = "host/state.get"
	MethodHostStateSet   = "host/state.set"
	MethodHostSecret     = "host/secret"
	MethodHostSecretSet  = "host/secret.set"
	MethodHostEmit       = "host/emit"
	MethodHostLog        = "host/log"
	MethodNotifyCancel   = "$/cancel"
	MethodNotifyProgress = "$/progress"
	MethodNotifyStream   = "$/stream"
	MethodNotifyAck      = "$/ack"
)

const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
	CodeDenied         = -32001
	CodeNotFound       = -32002
	CodeUnavailable    = -32003
	CodeConflict       = -32004
	CodeNeedsInput     = -32005
)

// No fields.
type Empty struct{}

// A URI whose scheme a plugin or core owns: svc://api, k8s://ns/deployment/api.
type Ref = string

// Arguments, validated by the source's or action's args schema.
type Args = map[string]any

// A JSON Schema.
type Schema = map[string]any

// One JSON-RPC 2.0 message: a request (id and method), a notification (method only) or a response (id and result or error).
type Message struct {
	Jsonrpc string `json:"jsonrpc"`
	// A number or a string; absent on notifications.
	ID     any    `json:"id,omitempty"`
	Method string `json:"method,omitempty"`
	Params any    `json:"params,omitempty"`
	Result any    `json:"result,omitempty"`
	Error  *Error `json:"error,omitempty"`
}

type Error struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *ErrorData `json:"data,omitempty"`
}

type ErrorData struct {
	// What to do about it, such as the tool to install.
	Hint string `json:"hint,omitempty"`
}

// Travels with every call.
type Context struct {
	Project string   `json:"project,omitempty"`
	Env     string   `json:"env,omitempty"`
	User    string   `json:"user,omitempty"`
	Roles   []string `json:"roles,omitempty"`
	Scope   *Scope   `json:"scope,omitempty"`
	TraceID string   `json:"trace_id,omitempty"`
	// RFC 3339.
	Deadline string `json:"deadline,omitempty"`
	DryRun   bool   `json:"dry_run,omitempty"`
}

type Scope struct {
	Service   string     `json:"service,omitempty"`
	Selection []Ref      `json:"selection,omitempty"`
	Range     *TimeRange `json:"range,omitempty"`
}

type TimeRange struct {
	// RFC 3339 or relative, such as -15m.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	Step string `json:"step,omitempty"`
}

type HostInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type InitializeParams struct {
	Host HostInfo `json:"host"`
	// Protocol versions the host speaks, such as rig/1.
	Protocols    []string          `json:"protocols"`
	Capabilities *HostCapabilities `json:"capabilities,omitempty"`
}

type HostCapabilities struct {
	// Widget kind to the highest version the host renders.
	Widgets  map[string]int `json:"widgets,omitempty"`
	Features []string       `json:"features,omitempty"`
	// Contract versions the host has views for, such as metrics/1.
	Contracts []string `json:"contracts,omitempty"`
}

type InitializeResult struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	// The protocol version chosen from the host's list.
	Protocol string    `json:"protocol"`
	Provides *Provides `json:"provides,omitempty"`
	// Optional methods and features the plugin supports, such as stream or plan.
	Capabilities []string `json:"capabilities,omitempty"`
}

type Provides struct {
	Sources    map[string]SourceSpec   `json:"sources,omitempty"`
	Actions    map[string]ActionSpec   `json:"actions,omitempty"`
	Importers  map[string]ImporterSpec `json:"importers,omitempty"`
	Views      map[string]ViewSpec     `json:"views,omitempty"`
	Widgets    []string                `json:"widgets,omitempty"`
	Schemes    []string                `json:"schemes,omitempty"`
	Languages  []string                `json:"languages,omitempty"`
	Implements []string                `json:"implements,omitempty"`
	Events     []string                `json:"events,omitempty"`
	Search     bool                    `json:"search,omitempty"`
}

type SourceSpec struct {
	Description string `json:"description,omitempty"`
	Args        Schema `json:"args,omitempty"`
	// How long a result stays fresh, such as 30s.
	Cache    string `json:"cache,omitempty"`
	Stream   bool   `json:"stream,omitempty"`
	Language string `json:"language,omitempty"`
}

type ActionSpec struct {
	Description string `json:"description,omitempty"`
	Effect      string `json:"effect"`
	Args        Schema `json:"args,omitempty"`
	// Supports a dry run.
	Plan bool     `json:"plan,omitempty"`
	Keys []string `json:"keys,omitempty"`
	// Schemes, kinds or sources whose menus list it.
	On []string `json:"on,omitempty"`
}

type ImporterSpec struct {
	Description string   `json:"description,omitempty"`
	Files       []string `json:"files,omitempty"`
}

type ViewSpec struct {
	Title string `json:"title,omitempty"`
	// tab, subtab:<view>, service.section, detail:<scheme>, panel or home.
	Slot string `json:"slot,omitempty"`
	Key  string `json:"key,omitempty"`
}

type ConfigureParams struct {
	Config     Args              `json:"config,omitempty"`
	Components map[string]Args   `json:"components,omitempty"`
	Secrets    map[string]string `json:"secrets,omitempty"`
	Context    *Context          `json:"context,omitempty"`
}

type Field struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Unit string `json:"unit,omitempty"`
	// A ref template over the row, such as svc://{service}.
	Ref string `json:"ref,omitempty"`
}

type FrameMeta struct {
	Total int `json:"total,omitempty"`
	// Cursor for the next page; empty when there is none.
	Next   string `json:"next,omitempty"`
	Notice string `json:"notice,omitempty"`
}

// Columnar, typed, renderer-neutral data. columns[i] holds every value of fields[i]. time is Unix seconds, duration is seconds.
type Frame struct {
	Name    string     `json:"name,omitempty"`
	Fields  []Field    `json:"fields"`
	Columns [][]any    `json:"columns"`
	Meta    *FrameMeta `json:"meta,omitempty"`
}

type QueryParams struct {
	Source    string   `json:"source"`
	Component string   `json:"component,omitempty"`
	Args      Args     `json:"args,omitempty"`
	Cursor    string   `json:"cursor,omitempty"`
	Context   *Context `json:"context,omitempty"`
}

type StreamParams struct {
	// Names the stream in $/stream, $/ack and $/cancel.
	ID        string   `json:"id"`
	Source    string   `json:"source"`
	Component string   `json:"component,omitempty"`
	Args      Args     `json:"args,omitempty"`
	Context   *Context `json:"context,omitempty"`
}

type StreamBatch struct {
	ID string `json:"id"`
	// Starts at 1; $/ack names the last seq the host consumed.
	Seq   int      `json:"seq"`
	Frame *Frame   `json:"frame,omitempty"`
	Lines []string `json:"lines,omitempty"`
	Done  bool     `json:"done,omitempty"`
	Error *Error   `json:"error,omitempty"`
}

type Ack struct {
	ID  string `json:"id"`
	Seq int    `json:"seq"`
}

type RunParams struct {
	Action    string `json:"action"`
	Component string `json:"component,omitempty"`
	Args      Args   `json:"args,omitempty"`
	// The job id progress is reported against.
	Job     string   `json:"job,omitempty"`
	Context *Context `json:"context,omitempty"`
}

type RunResult struct {
	Message string `json:"message,omitempty"`
	Frame   *Frame `json:"frame,omitempty"`
	Refs    []Ref  `json:"refs,omitempty"`
	Outputs Args   `json:"outputs,omitempty"`
	// What a plan would do, or what a run did.
	Changes []Change `json:"changes,omitempty"`
}

type Change struct {
	Op     string `json:"op"`
	Target Ref    `json:"target"`
	Detail string `json:"detail,omitempty"`
}

type Progress struct {
	Job     string `json:"job"`
	Message string `json:"message,omitempty"`
	// One line of output.
	Line  string `json:"line,omitempty"`
	Done  int    `json:"done,omitempty"`
	Total int    `json:"total,omitempty"`
}

type CancelParams struct {
	// The request id, or a stream id.
	ID any `json:"id"`
}

type ImportParams struct {
	Importer string   `json:"importer"`
	Files    []string `json:"files,omitempty"`
	Dir      string   `json:"dir,omitempty"`
	Config   Args     `json:"config,omitempty"`
}

type ImportResult struct {
	// A rig.yaml fragment.
	Fragment Args `json:"fragment"`
	// Files whose change re-runs the import.
	Watch    []string `json:"watch,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

type CompleteParams struct {
	Language string `json:"language"`
	Text     string `json:"text"`
	// Byte offset of the cursor.
	Pos     int      `json:"pos"`
	Context *Context `json:"context,omitempty"`
}

type Completion struct {
	Label  string `json:"label"`
	Insert string `json:"insert,omitempty"`
	Detail string `json:"detail,omitempty"`
	Kind   string `json:"kind,omitempty"`
}

type CompleteResult struct {
	Items []Completion `json:"items"`
}

// A widget tree node.
type Node struct {
	// A widget kind, optionally versioned: table@1.
	Widget   string   `json:"widget"`
	ID       string   `json:"id,omitempty"`
	Props    Args     `json:"props,omitempty"`
	Bind     *Binding `json:"bind,omitempty"`
	Children []Node   `json:"children,omitempty"`
	// What a host without this widget renders.
	Fallback *Node `json:"fallback,omitempty"`
}

type Binding struct {
	Source string `json:"source,omitempty"`
	Args   Args   `json:"args,omitempty"`
	Every  string `json:"every,omitempty"`
}

type RenderParams struct {
	View    string   `json:"view"`
	Args    Args     `json:"args,omitempty"`
	Context *Context `json:"context,omitempty"`
}

type ViewEventParams struct {
	View    string   `json:"view"`
	Node    string   `json:"node,omitempty"`
	Event   string   `json:"event"`
	Value   any      `json:"value,omitempty"`
	Context *Context `json:"context,omitempty"`
}

type Size struct {
	W int `json:"w"`
	H int `json:"h"`
}

type WidgetRenderParams struct {
	Widget string `json:"widget"`
	Props  Args   `json:"props,omitempty"`
	Frame  *Frame `json:"frame,omitempty"`
	Size   Size   `json:"size"`
	// Theme token to colour.
	Theme map[string]string `json:"theme,omitempty"`
}

type WidgetRenderResult struct {
	// At most size.h lines of at most size.w cells; ANSI SGR allowed.
	Lines []string `json:"lines"`
}

type ResolveParams struct {
	Ref     Ref      `json:"ref"`
	Context *Context `json:"context,omitempty"`
}

type Link struct {
	Ref   Ref    `json:"ref"`
	Rel   string `json:"rel,omitempty"`
	Title string `json:"title,omitempty"`
}

type Resolved struct {
	Ref     Ref      `json:"ref"`
	Kind    string   `json:"kind,omitempty"`
	Title   string   `json:"title,omitempty"`
	View    string   `json:"view,omitempty"`
	URL     string   `json:"url,omitempty"`
	Fields  Args     `json:"fields,omitempty"`
	Actions []string `json:"actions,omitempty"`
	Links   []Link   `json:"links,omitempty"`
}

type SearchParams struct {
	Query   string   `json:"query"`
	Limit   int      `json:"limit,omitempty"`
	Context *Context `json:"context,omitempty"`
}

type Hit struct {
	Ref    Ref    `json:"ref"`
	Title  string `json:"title"`
	Kind   string `json:"kind,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type SearchResult struct {
	Hits []Hit `json:"hits"`
}

type DoctorParams struct {
	Context *Context `json:"context,omitempty"`
}

type Check struct {
	Name    string `json:"name"`
	Ok      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Hint    string `json:"hint,omitempty"`
}

type DoctorResult struct {
	Checks []Check `json:"checks"`
}

type Event struct {
	// Such as deploy.finished or alert.firing.
	Topic  string `json:"topic"`
	Time   string `json:"time,omitempty"`
	Source string `json:"source,omitempty"`
	Refs   []Ref  `json:"refs,omitempty"`
	Data   Args   `json:"data,omitempty"`
}

type AskParams struct {
	Kind    string   `json:"kind"`
	Prompt  string   `json:"prompt"`
	Options []string `json:"options,omitempty"`
	Default string   `json:"default,omitempty"`
}

type AskResult struct {
	// False when the user cancelled.
	Ok    bool   `json:"ok"`
	Value string `json:"value,omitempty"`
}

type AIParams struct {
	Prompt    string `json:"prompt"`
	System    string `json:"system,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

type AIResult struct {
	Text string `json:"text"`
}

type StateGetParams struct {
	Key string `json:"key"`
}

type StateValue struct {
	Value any  `json:"value,omitempty"`
	Found bool `json:"found,omitempty"`
}

type StateSetParams struct {
	Key   string `json:"key"`
	Value any    `json:"value,omitempty"`
}

type SecretParams struct {
	Name string `json:"name"`
}

type SecretSetParams struct {
	Name string `json:"name"`
	// empty removes the secret
	Value string `json:"value"`
}

type SecretValue struct {
	Value string `json:"value"`
}

type LogParams struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

const (
	EffectDanger = "danger"
	EffectRead   = "read"
	EffectWrite  = "write"
	KindConfirm  = "confirm"
	KindInput    = "input"
	KindPick     = "pick"
	KindSecret   = "secret"
	LevelDebug   = "debug"
	LevelError   = "error"
	LevelInfo    = "info"
	LevelWarn    = "warn"
	OpCreate     = "create"
	OpDelete     = "delete"
	OpRun        = "run"
	OpUpdate     = "update"
	TypeBool     = "bool"
	TypeBytes    = "bytes"
	TypeDuration = "duration"
	TypeInt      = "int"
	TypeJSON     = "json"
	TypeNumber   = "number"
	TypeRef      = "ref"
	TypeString   = "string"
	TypeTime     = "time"
)
