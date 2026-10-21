package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/api"
	"github.com/goxang/rig/kernel/events"
	"github.com/goxang/rig/kernel/identity"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/kernel/view"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/protocol/rpc"
)

// Plugin is one discovered plugin and, once called, its process.
type Plugin struct {
	*Manifest
	// Config is plugins.<name> from rig.yaml over the manifest's config defaults.
	Config map[string]any

	h       *Host
	mu      sync.Mutex
	proc    *rpc.Process
	ready   *protocol.InitializeResult
	starts  []time.Time
	busy    int
	idle    *time.Timer
	jobs    sync.Map // job id to *jobs.Job, for $/progress
	streams sync.Map // stream id to chan protocol.StreamBatch
	traceMu sync.Mutex
	trace   *os.File
}

func unavailable(format string, a ...any) error {
	return &protocol.Error{Code: protocol.CodeUnavailable, Message: fmt.Sprintf(format, a...)}
}

func notFound(what, name string) error {
	return &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("no %s %s", what, name)}
}

var errNoMethod = &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "method not found"}

func (p *Plugin) call(ctx context.Context, method string, params, result any) error {
	switch p.Kind() {
	case "script":
		return p.script(ctx, method, params, result)
	case "declarative":
		return errNoMethod
	}
	c, err := p.conn(ctx)
	if err != nil {
		return err
	}
	p.enter()
	defer p.leave()
	if p.MCP != nil {
		return p.mcpCall(ctx, c, method, params, result)
	}
	return c.Call(ctx, method, params, result)
}

// conn starts the process on first use, and again after a crash: with backoff, at most 5 times a minute.
func (p *Plugin) conn(ctx context.Context) (*rpc.Conn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.proc != nil {
		select {
		case <-p.proc.Done():
			_ = p.proc.Close(time.Second)
			p.proc = nil
		default:
			return p.proc.Conn, nil
		}
	}
	var recent []time.Time
	for _, t := range p.starts {
		if time.Since(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 5 {
		return nil, unavailable("plugin %s stopped 5 times in a minute; see rig plugin logs %s", p.Name, p.Name)
	}
	if n := len(recent); n > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond << (n - 1)):
		}
	}
	p.starts = append(recent, time.Now())
	srv := p.Server
	if p.MCP != nil {
		srv = p.MCP
	}
	if srv == nil || len(srv.Command) == 0 {
		return nil, unavailable("plugin %s has no command", p.Name)
	}
	cmd := p.command(context.Background(), srv.Command, srv.Env)
	logf, err := p.openLog()
	if err == nil {
		cmd.Stderr = logf
		defer logf.Close() // the child holds its own descriptor
	}
	proc, err := rpc.Start(cmd, rpc.Options{OnRequest: p.serve, OnNotify: p.notify, Tap: p.tap})
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: fmt.Sprintf("plugin %s: %v", p.Name, err),
			Data: &protocol.ErrorData{Hint: "rig plugin info " + p.Name}}
	}
	ictx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := p.handshake(ictx, proc.Conn); err != nil {
		_ = proc.Close(time.Second)
		return nil, fmt.Errorf("plugin %s: start: %w", p.Name, err)
	}
	p.proc = proc
	return proc.Conn, nil
}

func (p *Plugin) handshake(ctx context.Context, c *rpc.Conn) error {
	if p.MCP != nil {
		return p.mcpHandshake(ctx, c)
	}
	var ir protocol.InitializeResult
	err := c.Call(ctx, protocol.MethodInitialize, protocol.InitializeParams{
		Host:         protocol.HostInfo{Name: "rig", Version: Version},
		Protocols:    []string{protocol.Version},
		Capabilities: &protocol.HostCapabilities{Widgets: view.Widgets},
	}, &ir)
	if err != nil {
		return err
	}
	if ir.Protocol != protocol.Version {
		return fmt.Errorf("speaks %q, want %s", ir.Protocol, protocol.Version)
	}
	err = c.Call(ctx, protocol.MethodConfigure, protocol.ConfigureParams{Config: p.Config, Components: p.h.components(p.Name), Context: p.h.context(ctx)}, nil)
	if pe := (*protocol.Error)(nil); errors.As(err, &pe) && pe.Code == protocol.CodeMethodNotFound {
		err = nil
	}
	if err != nil {
		return fmt.Errorf("configure: %w", err)
	}
	p.ready = &ir
	p.saveProvides(ir.Provides)
	return nil
}

func (p *Plugin) enter() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.busy++
	if p.idle != nil {
		p.idle.Stop()
	}
}

// leave arms the idle stop once nothing is running and no stream is open.
func (p *Plugin) leave() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy--; p.busy > 0 {
		return
	}
	d := p.h.Idle
	if d == 0 {
		d = 10 * time.Minute
	}
	if p.idle == nil {
		p.idle = time.AfterFunc(d, p.stopIdle)
	} else {
		p.idle.Reset(d)
	}
}

func (p *Plugin) stopIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy == 0 && p.proc != nil {
		_ = p.proc.Close(2 * time.Second)
		p.proc = nil
	}
}

// Running says whether its process is up, and its pid.
func (p *Plugin) Running() (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.proc == nil {
		return 0, false
	}
	select {
	case <-p.proc.Done():
		return 0, false
	default:
		return p.proc.Pid(), true
	}
}

func (p *Plugin) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idle != nil {
		p.idle.Stop()
	}
	if p.proc != nil {
		_ = p.proc.Close(2 * time.Second)
		p.proc = nil
	}
	p.traceMu.Lock()
	if p.trace != nil {
		p.trace.Close()
		p.trace = nil
	}
	p.traceMu.Unlock()
}

// command builds a plugin process: relative paths from the plugin's directory, cwd the project,
// a scrubbed environment, and the sandbox on Linux.
func (p *Plugin) command(ctx context.Context, c Command, env map[string]string) *exec.Cmd {
	args := slices.Clone([]string(c))
	if len(args) == 3 && args[0] == "sh" && args[1] == "-c" {
		if first, rest, _ := strings.Cut(args[2], " "); p.local(first) != first {
			args[2] = strings.TrimSpace(p.local(first) + " " + rest)
		}
	} else {
		args[0] = p.local(args[0])
	}
	if s := p.sandbox(); SandboxExe != "" && sandboxed && !s.unconfined() {
		args = slices.Concat([]string{SandboxExe, "__sandbox"}, s.args(), []string{"--"}, args)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = p.h.Dir
	cmd.Env = p.environ(env)
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

func (p *Plugin) local(path string) string {
	if filepath.IsAbs(path) || !strings.Contains(path, "/") {
		return path
	}
	if full := filepath.Join(p.Dir, path); exists(full) {
		return full
	}
	return path
}

func (p *Plugin) cacheDir() string { return filepath.Join(CacheDir(), "plugins", p.Name) }

func (p *Plugin) sandbox() sandboxSpec {
	_ = os.MkdirAll(p.cacheDir(), 0o700)
	s := sandboxSpec{ro: []string{p.Dir}, rw: []string{p.h.Dir, p.cacheDir()}}
	home, _ := os.UserHomeDir()
	for _, f := range p.Permissions.FS {
		ro := strings.HasPrefix(f, "ro:")
		f = p.expand(strings.TrimPrefix(f, "ro:"))
		if rest, ok := strings.CutPrefix(f, "~/"); ok {
			f = filepath.Join(home, rest)
		}
		if ro {
			s.ro = append(s.ro, f)
		} else {
			s.rw = append(s.rw, f)
		}
	}
	for _, n := range p.Permissions.Net {
		if port := netPort(p.expand(n)); port != 0 {
			s.ports = append(s.ports, port)
		}
	}
	if len(s.ports) > 0 {
		for _, k := range proxyVars {
			if v := os.Getenv(k); v != "" && !strings.Contains(k, "NO_") && !strings.Contains(k, "no_") {
				s.ports = append(s.ports, netPort(v))
			}
		}
	}
	return s
}

// proxyVars pass through: behind a proxy no plugin reaches anything without them.
var proxyVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"}

func netPort(s string) uint16 {
	if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.Host != "" {
		if u.Port() != "" {
			n, _ := strconv.ParseUint(u.Port(), 10, 16)
			return uint16(n)
		}
		return map[string]uint16{"http": 80, "https": 443, "ws": 80, "wss": 443}[u.Scheme]
	}
	if _, port, err := net.SplitHostPort(s); err == nil {
		n, _ := strconv.ParseUint(port, 10, 16)
		return uint16(n)
	}
	return 443
}

// environ is PATH, HOME, LANG, proxies and what the plugin declares; nothing else of rig's environment leaks.
func (p *Plugin) environ(extra map[string]string) []string {
	var out []string
	if slices.Contains(p.Permissions.Env, "*") {
		out = slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "RIG_") })
	}
	for _, k := range slices.Concat([]string{"PATH", "HOME", "LANG", "LC_ALL", "TZ"}, proxyVars, p.Permissions.Env) {
		if v, ok := os.LookupEnv(k); ok {
			if k == "PATH" {
				v = filepath.Join(p.Dir, "bin") + string(os.PathListSeparator) + v
			}
			out = append(out, k+"="+v)
		}
	}
	out = append(out, "RIG_PLUGIN="+p.Name, "RIG_PLUGIN_DIR="+p.Dir, "RIG_PROJECT="+p.h.Dir, "RIG_FILE="+p.h.File, "RIG_ENV="+p.h.Env,
		"RIG_CACHE="+p.cacheDir(), "RIG_PROTOCOL="+protocol.Version)
	for k, v := range extra {
		out = append(out, k+"="+p.expand(v))
	}
	return out
}

// expand fills ${config.x} from the plugin's config and ${NAME} only from variables it declares.
func (p *Plugin) expand(s string) string {
	return os.Expand(s, func(k string) string {
		if c, ok := strings.CutPrefix(k, "config."); ok {
			if v, ok := p.Config[c]; ok {
				return fmt.Sprint(v)
			}
			return ""
		}
		if slices.Contains(p.Permissions.Env, k) || slices.Contains(p.Permissions.Secrets, k) {
			return os.Getenv(k)
		}
		return ""
	})
}

func (p *Plugin) openLog() (*os.File, error) {
	dir := filepath.Join(StateDir(), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(LogFile(p.Name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

func LogFile(name string) string   { return filepath.Join(StateDir(), "logs", name+".log") }
func TraceFile(name string) string { return filepath.Join(StateDir(), "logs", name+".trace") }

// tap records the RPC exchange when RIG_TRACE names the plugin (or is *).
func (p *Plugin) tap(dir byte, line []byte) {
	t := os.Getenv("RIG_TRACE")
	if t == "" || t != "*" && !slices.Contains(strings.Split(t, ","), p.Name) {
		return
	}
	p.traceMu.Lock()
	defer p.traceMu.Unlock()
	if p.trace == nil {
		f, err := os.OpenFile(TraceFile(p.Name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		p.trace = f
	}
	fmt.Fprintf(p.trace, "%s %c %s\n", time.Now().Format("15:04:05.000"), dir, bytes.TrimSpace(line))
}

func (p *Plugin) log(level, msg string) {
	if f, err := p.openLog(); err == nil {
		fmt.Fprintf(f, "%s %s %s\n", time.Now().Format(time.RFC3339), level, msg)
		f.Close()
	}
}

func (p *Plugin) notify(method string, raw json.RawMessage) {
	switch method {
	case protocol.MethodNotifyProgress:
		var pr protocol.Progress
		if json.Unmarshal(raw, &pr) != nil {
			return
		}
		if v, ok := p.jobs.Load(pr.Job); ok {
			j := v.(*jobs.Job)
			if pr.Line != "" {
				fmt.Fprintln(j, pr.Line)
			}
			if pr.Total > 0 || pr.Message != "" {
				j.Progress(pr.Done, pr.Total, pr.Message)
			}
		}
	case protocol.MethodNotifyStream:
		var b protocol.StreamBatch
		if json.Unmarshal(raw, &b) != nil {
			return
		}
		if v, ok := p.streams.Load(b.ID); ok {
			v.(chan protocol.StreamBatch) <- b // the $/ack window keeps this from filling
		}
	case protocol.MethodHostLog:
		var l protocol.LogParams
		if json.Unmarshal(raw, &l) == nil {
			p.log(l.Level, l.Message)
		}
	}
}

// may says whether the plugin may call a source or action through the host: its own, or one its uses: allows.
func (p *Plugin) may(name string) error {
	if strings.HasPrefix(name, p.Name+".") {
		return nil
	}
	for _, g := range p.Permissions.Uses {
		if events.Match(g, name) {
			return nil
		}
	}
	return &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf("plugin %s may not use %s (add it to permissions.uses)", p.Name, name)}
}

// serve answers the plugin's host/* requests.
func (p *Plugin) serve(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	h := p.h
	switch method {
	case "ping": // MCP servers check the client is alive
		return struct{}{}, nil
	case protocol.MethodHostQuery:
		var q protocol.QueryParams
		if err := decode(raw, &q); err != nil {
			return nil, err
		}
		if err := p.may(q.Source); err != nil {
			return nil, err
		}
		if h.Engine == nil {
			return nil, unavailable("no engine")
		}
		return h.Engine.Query(ctx, q.Source, q.Args)
	case protocol.MethodHostRun:
		var r protocol.RunParams
		if err := decode(raw, &r); err != nil {
			return nil, err
		}
		if err := p.may(r.Action); err != nil {
			return nil, err
		}
		if h.Engine == nil {
			return nil, unavailable("no engine")
		}
		as := identity.Subject{Kind: identity.KindPlugin, Name: p.Name}
		_, res, err := h.Engine.Run(ctx, r.Action, r.Args, api.RunOpts{As: &as, DryRun: r.Context != nil && r.Context.DryRun})
		return res, err
	case protocol.MethodHostAsk:
		var a protocol.AskParams
		if err := decode(raw, &a); err != nil {
			return nil, err
		}
		if h.Ask == nil {
			return nil, &protocol.Error{Code: protocol.CodeNeedsInput, Message: "no one to ask: " + a.Prompt}
		}
		return h.Ask(ctx, a)
	case protocol.MethodHostAI:
		var a protocol.AIParams
		if err := decode(raw, &a); err != nil {
			return nil, err
		}
		if !p.Permissions.AI {
			return nil, &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf("plugin %s may not use the AI (permissions.ai)", p.Name)}
		}
		if h.AI == nil {
			return nil, unavailable("no AI backend configured")
		}
		text, err := h.AI(ctx, a)
		return protocol.AIResult{Text: text}, err
	case protocol.MethodHostStateGet:
		var g protocol.StateGetParams
		if err := decode(raw, &g); err != nil {
			return nil, err
		}
		v, ok, err := h.stateGet(p.Name, g.Key)
		return protocol.StateValue{Value: v, Found: ok}, err
	case protocol.MethodHostStateSet:
		var s protocol.StateSetParams
		if err := decode(raw, &s); err != nil {
			return nil, err
		}
		return protocol.Empty{}, h.stateSet(p.Name, s.Key, s.Value)
	case protocol.MethodHostSecret:
		var s protocol.SecretParams
		if err := decode(raw, &s); err != nil {
			return nil, err
		}
		if !slices.Contains(p.Permissions.Secrets, s.Name) {
			return nil, &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf("plugin %s did not declare secret %s", p.Name, s.Name)}
		}
		v, ok := h.secret(s.Name)
		if !ok {
			return nil, notFound("secret", s.Name)
		}
		return protocol.SecretValue{Value: v}, nil
	case protocol.MethodHostEmit:
		var e protocol.Event
		if err := decode(raw, &e); err != nil {
			return nil, err
		}
		e.Source = p.Name
		if h.Emit != nil {
			h.Emit(e)
		}
		return protocol.Empty{}, nil
	}
	return nil, errNoMethod
}

func decode(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return &protocol.Error{Code: protocol.CodeInvalidParams, Message: err.Error()}
	}
	return nil
}

// providesKey changes when the plugin's version or executable does, so a cached initialize stays true.
func (p *Plugin) providesKey() string {
	srv := p.Server
	if p.MCP != nil {
		srv = p.MCP
	}
	h := sha256.New()
	fmt.Fprint(h, p.Version, p.Dir)
	if srv != nil {
		fmt.Fprint(h, []string(srv.Command))
		for _, a := range srv.Command {
			if st, err := os.Stat(p.local(a)); err == nil && !st.IsDir() {
				fmt.Fprint(h, st.ModTime().UnixNano(), st.Size())
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

func (p *Plugin) providesFile() string {
	return filepath.Join(CacheDir(), "provides", p.Name+"-"+p.providesKey()+".json")
}

func (p *Plugin) saveProvides(pr *protocol.Provides) {
	if pr == nil {
		pr = &protocol.Provides{}
	}
	b, _ := json.Marshal(pr)
	if os.MkdirAll(filepath.Dir(p.providesFile()), 0o700) == nil {
		_ = writeAtomic(p.providesFile(), b, 0o600)
	}
}

// Provides is what the plugin offers: its manifest, plus what its process said at initialize (cached,
// so a lazy plugin need not start to be listed).
func (p *Plugin) Provides(ctx context.Context) *protocol.Provides {
	pr := p.declared()
	if p.Server == nil && p.MCP == nil {
		return pr
	}
	var got protocol.Provides
	b, err := os.ReadFile(p.providesFile())
	if err != nil {
		if _, err := p.conn(ctx); err != nil {
			p.log(protocol.LevelError, err.Error())
			return pr
		}
		b, _ = os.ReadFile(p.providesFile())
	}
	if json.Unmarshal(b, &got) != nil {
		return pr
	}
	for k, v := range got.Sources {
		if _, ok := pr.Sources[k]; !ok {
			pr.Sources[k] = v
		}
	}
	for k, v := range got.Actions {
		if _, ok := pr.Actions[k]; !ok {
			pr.Actions[k] = v
		}
	}
	for k, v := range got.Importers {
		if _, ok := pr.Importers[k]; !ok {
			pr.Importers[k] = v
		}
	}
	pr.Schemes = slices.Compact(slices.Sorted(slices.Values(append(pr.Schemes, got.Schemes...))))
	pr.Search = pr.Search || got.Search
	return pr
}

func argsSchema(props map[string]any) protocol.Schema {
	if len(props) == 0 {
		return nil
	}
	return protocol.Schema{"type": "object", "properties": props}
}

func (p *Plugin) declared() *protocol.Provides {
	pr := &protocol.Provides{Sources: map[string]protocol.SourceSpec{}, Actions: map[string]protocol.ActionSpec{},
		Importers: map[string]protocol.ImporterSpec{}, Schemes: slices.Clone(p.Schemes), Implements: p.Implements, Search: p.Search}
	for n, s := range p.Sources {
		pr.Sources[n] = protocol.SourceSpec{Description: s.Help, Args: argsSchema(s.Args), Cache: s.Cache, Stream: s.Stream}
	}
	for n, a := range p.Actions {
		pr.Actions[n] = protocol.ActionSpec{Description: a.Help, Effect: a.Effect, Args: argsSchema(a.Args), Plan: a.Plan, Keys: a.Keys, On: a.On}
	}
	for n, im := range p.Importers {
		pr.Importers[n] = protocol.ImporterSpec{Description: im.Help, Files: im.Files}
	}
	return pr
}

// script runs a run: command for a query, run, plan or import. Args go in $RIG_ARGS and on stdin.
func (p *Plugin) script(ctx context.Context, method string, params, result any) error {
	switch method {
	case protocol.MethodQuery:
		q := params.(protocol.QueryParams)
		d, ok := p.Sources[q.Source]
		if !ok || len(d.Run) == 0 {
			return notFound("source", p.Name+"."+q.Source)
		}
		out, err := p.runScript(ctx, d.Run, q.Args, nil)
		if err != nil {
			return err
		}
		f, err := parseOutput(out, d.Format)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", p.Name, q.Source, err)
		}
		*result.(*protocol.Frame) = *f
		return nil
	case protocol.MethodRun, protocol.MethodPlan:
		r := params.(protocol.RunParams)
		d, ok := p.Actions[r.Action]
		if !ok || len(d.Run) == 0 || method == protocol.MethodPlan && !d.Plan {
			return notFound(method+" action", p.Name+"."+r.Action)
		}
		var env []string
		if method == protocol.MethodPlan {
			env = []string{"RIG_DRY_RUN=1"}
		}
		out, err := p.runScript(ctx, d.Run, r.Args, jobs.Out(ctx), env...)
		if err != nil {
			return err
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		*result.(*protocol.RunResult) = protocol.RunResult{Message: lines[len(lines)-1]}
		return nil
	case protocol.MethodImport:
		ip := params.(protocol.ImportParams)
		d, ok := p.Importers[ip.Importer]
		if !ok || len(d.Run) == 0 {
			return notFound("importer", p.Name+"."+ip.Importer)
		}
		out, err := p.runScript(ctx, d.Run, ip, nil, "RIG_FILES="+strings.Join(ip.Files, string(os.PathListSeparator)))
		if err != nil {
			return err
		}
		var frag map[string]any
		if err := yaml.Unmarshal(out, &frag); err != nil {
			return fmt.Errorf("%s.%s: the fragment: %w", p.Name, ip.Importer, err)
		}
		*result.(*protocol.ImportResult) = protocol.ImportResult{Fragment: frag}
		return nil
	}
	return errNoMethod
}

// ponytail: a cancelled script loses only its direct process; a process group kill when scripts fork daemons.
func (p *Plugin) runScript(ctx context.Context, c Command, args any, live io.Writer, env ...string) ([]byte, error) {
	if text := p.MissingText(true); text != "" {
		return nil, unavailable("plugin %s %s", p.Name, text)
	}
	b, _ := json.Marshal(args)
	conf, _ := json.Marshal(p.Config)
	cmd := p.command(ctx, c, nil)
	cmd.Env = append(cmd.Env, append(env, "RIG_ARGS="+string(b), "RIG_CONFIG="+string(conf))...)
	cmd.Stdin = bytes.NewReader(b)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	if live != nil {
		cmd.Stdout = io.MultiWriter(&out, live)
	}
	cmd.Stderr = &errb
	err := cmd.Run()
	if errb.Len() > 0 {
		p.log(protocol.LevelInfo, strings.TrimSpace(errb.String()))
	}
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:]
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s: %s", p.Name, msg)
	}
	return out.Bytes(), nil
}
