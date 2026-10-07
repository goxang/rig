package ai

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/internal/sh"
)

// What rig's MCP server reads to know it serves an assistant, and with which bounds.
const (
	EnvLock      = "RIG_AI_ENV"
	EnvDir       = "RIG_AI_DIR"
	EnvSock      = "RIG_AI_SOCK"
	EnvProtected = "RIG_AI_PROTECTED"
	EnvKube      = "RIG_AI_KUBE"
	// EnvBareTools has the MCP server list its tools without the rig_ prefix (opencode adds the server's name)
	EnvBareTools = "RIG_MCP_BARE"
)

var proxyVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "NO_PROXY", "no_proxy"}

type Event struct {
	Kind string // text, tool, error
	Text string
}

// Runner runs turns of one project and environment.
type Runner struct {
	Setup Setup
	Scope Scope
	// Self is this rig binary, which the backend starts as its MCP server.
	Self string
	// Sock is the bridge for approvals and UI actions; empty when nothing can answer.
	Sock string
	Kube bool
	// Redactor takes the secrets out of every prompt; nil sends them as they are (redact=false).
	Redactor *Redactor
	// AuditFile is the JSONL file every action of the assistant is appended to.
	AuditFile string

	clientOnce sync.Once
	client     *http.Client
	clientErr  error
}

// LastPrompt is the user's latest message, which tools check "user_request" quotes against.
func LastPrompt(dir string) string {
	raw, _ := os.ReadFile(filepath.Join(dir, "last_prompt.txt"))
	return string(raw)
}

// Turn sends one message: typed is what the user wrote, screen what they were looking at.
func (r *Runner) Turn(ctx context.Context, s *Session, typed, screen string, on func(Event)) error {
	if !r.Setup.Enabled() {
		return errors.New("AI is " + r.Setup.Describe())
	}
	s.Add("user", typed)
	if err := s.Save(); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.Dir(), "last_prompt.txt"), []byte(typed), 0o600); err != nil {
		return err
	}
	msg := typed
	if screen != "" {
		msg = "[screen: " + screen + "]\n\n" + typed
	}
	if s.Backend != r.Setup.Backend {
		// another backend cannot resume this one's conversation: hand it the transcript instead
		msg = transcript(s.Messages[:len(s.Messages)-1]) + msg
		s.Backend, s.BackendID = r.Setup.Backend, ""
	}
	cmd, err := r.command(ctx, s, r.Redactor.Redact(msg))
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	// a stop must not wait for the backend's own children (its MCP servers) to let go of stdout
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			sh.KillGroup(cmd)
			_ = out.Close()
		case <-stopped:
		}
	}()
	s.Next = ""
	var text []string
	var tools []string
	failed := ""
	emit := func(e Event) {
		switch e.Kind {
		case "text":
			var next string
			if e.Text, next = SplitNext(e.Text); next != "" {
				s.Next = next
				on(Event{Kind: "next", Text: next})
			}
			if strings.TrimSpace(e.Text) == "" {
				return
			}
			text = append(text, e.Text)
		case "tool":
			tools = append(tools, e.Text)
		case "error":
			failed = e.Text
		}
		on(e)
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 32<<20)
	for sc.Scan() {
		if r.Setup.Backend == BackendClaude {
			parseClaude(sc.Bytes(), s, emit)
		} else {
			parseOpencode(sc.Bytes(), s, emit)
		}
	}
	werr := cmd.Wait()
	if werr != nil && len(text) == 0 && failed == "" {
		failed = tail(stderr.String(), 600)
		if failed == "" {
			failed = werr.Error()
		}
		if ctx.Err() != nil {
			failed = "stopped"
		}
		on(Event{Kind: "error", Text: failed})
	}
	if len(text) > 0 || len(tools) > 0 {
		s.Add("assistant", strings.Join(text, "\n\n"), tools...)
	}
	if failed != "" {
		s.Add("error", failed)
	}
	if err := s.Save(); err != nil {
		return err
	}
	if failed != "" {
		return ErrTurnFailed
	}
	return nil
}

// SplitNext takes the "NEXT: ..." line the system prompt asks answers to end with off text.
func SplitNext(text string) (rest, next string) {
	t := strings.TrimRight(text, " \n")
	i := strings.LastIndexByte(t, '\n') + 1
	if v, ok := strings.CutPrefix(strings.TrimSpace(t[i:]), "NEXT:"); ok {
		return strings.TrimRight(t[:i], "\n"), strings.Trim(strings.TrimSpace(v), "`\"")
	}
	return text, ""
}

// ErrTurnFailed is a turn whose failure was already reported as an error event.
var ErrTurnFailed = errors.New("the assistant's turn failed")

func transcript(ms []Message) string {
	if len(ms) > 20 {
		ms = ms[len(ms)-20:]
	}
	var b strings.Builder
	b.WriteString("[the conversation so far]\n")
	for _, m := range ms {
		if m.Role == "user" || m.Role == "assistant" {
			b.WriteString(m.Role + ": " + tail(m.Text, 2000) + "\n")
		}
	}
	return b.String() + "\n"
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = "…" + s[len(s)-n:]
	}
	return s
}

// mcpServer is how the backend starts rig as its tools: pinned to the session's environment, with
// the proxy the assistant uses taken away again (clusters and databases are reached directly).
func (r *Runner) mcpServer(s *Session) ([]string, map[string]string) {
	argv := []string{r.Self, "-f", r.Scope.File, "-e", r.Scope.Env, "mcp"}
	env := map[string]string{EnvLock: r.Scope.Env, EnvDir: s.Dir(), EnvSock: r.Sock, EnvProtected: "", EnvKube: "", EnvAudit: r.AuditFile}
	if r.Scope.Protected {
		env[EnvProtected] = "1"
	}
	if r.Kube {
		env[EnvKube] = "1"
	}
	for _, v := range proxyVars {
		env[v] = os.Getenv(v)
	}
	return argv, env
}

func (r *Runner) command(ctx context.Context, s *Session, msg string) (*exec.Cmd, error) {
	rules := filepath.Join(s.Dir(), "rules.md")
	prompt := SystemPrompt(r.Scope)
	if err := os.WriteFile(rules, []byte(prompt), 0o600); err != nil {
		return nil, err
	}
	argv, mcpEnv := r.mcpServer(s)
	var cmd *exec.Cmd
	env := r.env()
	if r.Setup.Backend == BackendClaude {
		mcp, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"rig": map[string]any{"command": argv[0], "args": argv[1:], "env": mcpEnv}}})
		var deny []string
		for _, p := range r.Scope.denied() {
			for _, tool := range []string{"Read", "Edit", "Write"} {
				deny = append(deny, tool+"(/"+filepath.Join(r.Scope.Dir, p)+")")
			}
		}
		settings, _ := json.Marshal(map[string]any{"permissions": map[string]any{"deny": deny}})
		args := []string{"-p", "--output-format", "stream-json", "--verbose", "--tools", "Read,Grep,Glob,Edit,Write,Bash",
			"--strict-mcp-config", "--mcp-config", string(mcp), "--allowedTools", "mcp__rig,Read,Grep,Glob,Edit,Write,Bash",
			"--permission-mode", "dontAsk", "--setting-sources", "", "--settings", string(settings), "--append-system-prompt", prompt}
		if r.Setup.Model != "" {
			args = append(args, "--model", r.Setup.Model)
		}
		if r.Setup.Effort != "" {
			args = append(args, "--effort", r.Setup.Effort)
		}
		if s.BackendID != "" {
			args = append(args, "--resume", s.BackendID)
		}
		cmd = exec.CommandContext(ctx, r.Setup.Bin, append(args, "--", msg)...)
	} else {
		cfg, model := r.opencodeConfig(rules, argv, mcpEnv)
		raw, _ := json.Marshal(cfg)
		env = append(env, "OPENCODE_CONFIG_CONTENT="+string(raw))
		args := []string{"run", "--format", "json"}
		if model != "" {
			args = append(args, "-m", model)
		}
		if r.Setup.Effort != "" {
			args = append(args, "--variant", r.Setup.Effort)
		}
		if s.BackendID != "" {
			args = append(args, "--session", s.BackendID)
		} else {
			args = append(args, "--title", "rig: "+s.Title)
		}
		cmd = exec.CommandContext(ctx, r.Setup.Bin, append(args, "--", msg)...)
	}
	cmd.Dir, cmd.Env = r.Scope.Dir, env
	sh.OwnGroup(cmd)
	cmd.Cancel = func() error { sh.KillGroup(cmd); return nil }
	cmd.WaitDelay = 3 * time.Second
	return cmd, nil
}

var userMCPs struct {
	sync.Once
	names []string
}

// userMCPs are the MCP servers the user's opencode config (global and project) declares.
func (r *Runner) userMCPs() []string {
	userMCPs.Do(func() {
		cmd := exec.Command(r.Setup.Bin, "debug", "config")
		cmd.Dir = r.Scope.Dir
		raw, err := cmd.Output()
		if err != nil {
			return
		}
		var c struct{ MCP map[string]json.RawMessage }
		if json.Unmarshal(raw, &c) != nil {
			return
		}
		for name := range c.MCP {
			if name != "rig" {
				userMCPs.names = append(userMCPs.names, name)
			}
		}
	})
	return userMCPs.names
}

// opencodeConfig is laid over the user's opencode config. Secret paths are "ask" rather than
// "deny": a headless run refuses what would ask, and denying drops the tool from the request,
// which opencode's free tier turns away.
func (r *Runner) opencodeConfig(rules string, mcpArgv []string, mcpEnv map[string]string) (map[string]any, string) {
	read := map[string]any{"*": "allow"}
	for _, p := range r.Scope.denied() {
		read[p], read["**/"+strings.TrimPrefix(p, "**/")], read[filepath.Join(r.Scope.Dir, p)] = "ask", "ask", "ask"
	}
	cfg := map[string]any{
		"instructions": []string{rules},
		"permission":   map[string]any{"edit": "allow", "bash": "allow", "webfetch": "allow", "external_directory": "deny", "read": read},
	}
	// The user's own MCP servers would be extra tools, and a dead one stalls every start by ~30s.
	mcp := map[string]any{}
	for _, name := range r.userMCPs() {
		mcp[name] = map[string]any{"enabled": false}
	}
	if mcpArgv != nil {
		env := map[string]string{EnvBareTools: "1"}
		for k, v := range mcpEnv {
			env[k] = v
		}
		// rig render, builds and kubectl through tools take longer than opencode's default 5s
		mcp["rig"] = map[string]any{"type": "local", "command": mcpArgv, "environment": env, "enabled": true, "timeout": 300000}
	}
	cfg["mcp"] = mcp
	model := r.Setup.Model
	if compatible(r.Setup.Provider) {
		key := r.Setup.APIKey
		if key == "" {
			key = r.Setup.Provider // a local server takes any key, the SDK wants one
		}
		cfg["provider"] = map[string]any{"rig": map[string]any{
			"npm":     "@ai-sdk/openai-compatible",
			"name":    "rig " + r.Setup.Provider,
			"options": map[string]any{"baseURL": r.Setup.URL, "apiKey": key},
			"models":  map[string]any{r.Setup.Model: map[string]any{"name": r.Setup.Model}},
		}}
		model = "rig/" + r.Setup.Model
	}
	return cfg, model
}

// env is rig's environment with the AI proxy in place of any other.
func (r *Runner) env() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		skip := false
		for _, p := range proxyVars {
			skip = skip || k == p
		}
		if !skip && (r.Setup.Provider != "anthropic" || !strings.HasPrefix(k, "ANTHROPIC_")) {
			out = append(out, kv)
		}
	}
	if p := r.proxyURL(); p != "" {
		for _, v := range proxyVars[:6] {
			out = append(out, v+"="+p)
		}
		out = append(out, "NO_PROXY=localhost,127.0.0.1,::1", "no_proxy=localhost,127.0.0.1,::1")
	} else {
		for _, v := range proxyVars {
			if x, ok := os.LookupEnv(v); ok {
				out = append(out, v+"="+x)
			}
		}
	}
	if r.Setup.Provider == "anthropic" {
		out = append(out, "ANTHROPIC_BASE_URL="+r.Setup.URL, "ANTHROPIC_AUTH_TOKEN="+r.Setup.APIKey)
	}
	return out
}

// proxyURL is the configured proxy with a scheme: "localhost:10808" means http://localhost:10808.
func (r *Runner) proxyURL() string {
	p := strings.TrimSpace(r.Setup.Proxy)
	if p != "" && !strings.Contains(p, "://") {
		p = "http://" + p
	}
	return p
}

func toolName(n string) string {
	n = strings.TrimPrefix(n, "mcp__rig__")
	if strings.HasPrefix(n, "rig_rig") {
		n = strings.TrimPrefix(n, "rig_")
	}
	return n
}

func toolText(name string, input any) string {
	raw, _ := json.Marshal(input)
	in := string(raw)
	if in == "{}" || in == "null" {
		in = ""
	}
	if len(in) > 160 {
		in = in[:160] + "…"
	}
	return strings.TrimSpace(toolName(name) + " " + in)
}

func parseClaude(line []byte, s *Session, emit func(Event)) {
	var ev struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		SessionID string `json:"session_id"`
		IsError   bool   `json:"is_error"`
		Result    string `json:"result"`
		Message   struct {
			Content []struct {
				Type    string          `json:"type"`
				Text    string          `json:"text"`
				Name    string          `json:"name"`
				Input   any             `json:"input"`
				IsError bool            `json:"is_error"`
				Content json.RawMessage `json:"content"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	if ev.SessionID != "" {
		s.BackendID = ev.SessionID
	}
	switch ev.Type {
	case "assistant":
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				if strings.TrimSpace(c.Text) != "" {
					emit(Event{Kind: "text", Text: c.Text})
				}
			case "tool_use":
				emit(Event{Kind: "tool", Text: toolText(c.Name, c.Input)})
			}
		}
	case "user":
		for _, c := range ev.Message.Content {
			if c.Type == "tool_result" && c.IsError {
				emit(Event{Kind: "tool", Text: "✖ " + tail(resultText(c.Content), 300)})
			}
		}
	case "result":
		if ev.IsError {
			emit(Event{Kind: "error", Text: ev.Result})
		}
	}
}

func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &parts)
	var out []string
	for _, p := range parts {
		out = append(out, p.Text)
	}
	return strings.Join(out, " ")
}

func parseOpencode(line []byte, s *Session, emit func(Event)) {
	var ev struct {
		Type      string `json:"type"`
		SessionID string `json:"sessionID"`
		Part      struct {
			Text  string `json:"text"`
			Tool  string `json:"tool"`
			State struct {
				Status string `json:"status"`
				Input  any    `json:"input"`
				Error  string `json:"error"`
			} `json:"state"`
		} `json:"part"`
		Error struct {
			Name string `json:"name"`
			Data struct {
				Message string `json:"message"`
			} `json:"data"`
		} `json:"error"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	if ev.SessionID != "" {
		s.BackendID = ev.SessionID
	}
	switch ev.Type {
	case "text":
		if strings.TrimSpace(ev.Part.Text) != "" {
			emit(Event{Kind: "text", Text: ev.Part.Text})
		}
	case "tool_use":
		t := toolText(ev.Part.Tool, ev.Part.State.Input)
		if ev.Part.State.Status == "error" {
			t += "  ✖ " + tail(ev.Part.State.Error, 200)
		}
		emit(Event{Kind: "tool", Text: t})
	case "error":
		m := ev.Error.Data.Message
		if m == "" {
			m = ev.Error.Name
		}
		emit(Event{Kind: "error", Text: m})
	}
}

// Complete returns what likely follows text, given hint about where it is typed. An API-key
// provider is asked directly (one request, fast); otherwise the backend runs once without tools.
func (r *Runner) Complete(ctx context.Context, hint, text string) (string, error) {
	out, err := r.quick(ctx, CompletePrompt(hint, text))
	if err != nil {
		return "", err
	}
	return cleanCompletion(text, out), nil
}

// Describe writes the input asked for in words: before is what is typed ahead of the description
// (a query's start, or nothing), want the description; it returns the whole input, before included.
//
// check, when not nil, tries the answer (compiles it, runs it where that is safe); an answer that
// fails goes back to the model with the error, up to three tries. After the last, the answer is
// returned with the error.
func (r *Runner) Describe(ctx context.Context, hint, before, want string, check func(context.Context, string) error) (string, error) {
	ask := want
	var out string
	var failed error
	for try := 0; try < 3; try++ {
		raw, err := r.once(ctx, DescribePrompt(hint, before, ask), true, 800)
		if err != nil {
			return "", err
		}
		out = flatten(raw)
		if b := strings.TrimSpace(before); b != "" && !strings.HasPrefix(out, b) {
			out = strings.TrimRight(before, " ") + " " + strings.TrimLeft(out, " ")
		}
		if check == nil {
			return out, nil
		}
		if failed = check(ctx, out); failed == nil {
			return out, nil
		}
		ask = want + "\nYour last answer was: " + out + "\nIt failed with: " + failed.Error() + "\nWrite a corrected one."
	}
	return out, fmt.Errorf("still fails after 3 tries: %w", failed)
}

// flatten is an answer without code fences, its lines joined into one (a multi-line query stays whole).
func flatten(out string) string {
	out = strings.TrimSpace(out)
	if strings.HasPrefix(out, "```") {
		out = strings.TrimPrefix(out, "```")
		if i := strings.IndexByte(out, '\n'); i >= 0 && !strings.Contains(out[:i], " ") {
			out = out[i+1:]
		}
	}
	out = strings.TrimSuffix(strings.TrimSpace(out), "```")
	var parts []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, " ")
}

// Ask puts one question to the chat model, without tools, and returns its whole answer.
func (r *Runner) Ask(ctx context.Context, prompt string) (string, error) {
	out, err := r.once(ctx, prompt, false, 2000)
	return strings.TrimSpace(out), err
}

// quick asks the fast model once, without tools: an API-key provider directly, else the backend.
func (r *Runner) quick(ctx context.Context, prompt string) (string, error) {
	return r.once(ctx, prompt, true, 120)
}

func (r *Runner) once(ctx context.Context, prompt string, fast bool, maxTokens int) (string, error) {
	if !r.Setup.Enabled() {
		return "", errors.New("AI is " + r.Setup.Describe())
	}
	prompt = r.Redactor.Redact(prompt)
	model := r.Setup.Model
	if fast {
		model = r.Setup.FastModel
	}
	var out string
	var err error
	switch {
	case fast && r.Setup.FastURL != "":
		out, err = r.chatCompletion(ctx, r.Setup.FastURL, r.Setup.FastAPIKey, model, prompt, maxTokens)
	case compatible(r.Setup.Provider) && r.Setup.URL != "":
		if model == "" {
			model = r.Setup.Model
		}
		out, err = r.chatCompletion(ctx, r.Setup.URL, r.Setup.APIKey, model, prompt, maxTokens)
	case r.Setup.Backend == BackendClaude:
		if model == "" && fast {
			model = "haiku"
		}
		args := []string{"-p", "--tools", "", "--strict-mcp-config", "--setting-sources", "", "--output-format", "text"}
		if model != "" {
			args = append(args, "--model", model)
		}
		cmd := exec.CommandContext(ctx, r.Setup.Bin, append(args, "--", prompt)...)
		cmd.Dir, cmd.Env = r.Scope.Dir, r.env()
		var b []byte
		b, err = cmd.Output()
		out = string(b)
	default:
		cfg, m := r.opencodeConfig("", nil, nil)
		delete(cfg, "instructions")
		if model == "" {
			model = m
		}
		raw, _ := json.Marshal(cfg)
		args := []string{"run", "--format", "json"}
		if model != "" {
			args = append(args, "-m", model)
		}
		cmd := exec.CommandContext(ctx, r.Setup.Bin, append(args, "--", prompt)...)
		cmd.Dir, cmd.Env = r.Scope.Dir, append(r.env(), "OPENCODE_CONFIG_CONTENT="+string(raw))
		var b []byte
		b, err = cmd.Output()
		var texts []string
		var tmp Session
		for _, l := range bytes.Split(b, []byte("\n")) {
			parseOpencode(l, &tmp, func(e Event) {
				if e.Kind == "text" {
					texts = append(texts, e.Text)
				}
			})
		}
		out = strings.Join(texts, "")
	}
	return out, err
}

// oneLine is a model's answer without fences, cut to its first line.
func oneLine(out string) string {
	out = strings.TrimSpace(out)
	out = strings.TrimPrefix(out, "```")
	if i := strings.IndexByte(out, '\n'); i >= 0 && !strings.Contains(out[:i], " ") && len(out[:i]) < 12 {
		out = out[i+1:] // a fence's language tag
	}
	out = strings.TrimSuffix(strings.TrimSpace(out), "```")
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = out[:i]
	}
	return strings.TrimRight(out, " ")
}

// cleanCompletion keeps one line of new text: no fences, no echo of what was typed.
func cleanCompletion(typed, out string) string {
	out = oneLine(out)
	if t := strings.TrimSpace(typed); t != "" && strings.HasPrefix(out, t) {
		out = out[len(t):]
	} else if strings.HasSuffix(typed, " ") {
		out = strings.TrimLeft(out, " ")
	}
	return out
}

func (r *Runner) chatCompletion(ctx context.Context, endpoint, apiKey, model, prompt string, maxTokens int) (string, error) {
	body, _ := json.Marshal(map[string]any{"model": model, "max_tokens": maxTokens, "temperature": 0, "stream": false,
		"messages": []map[string]string{{"role": "user", "content": prompt}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	c, err := r.httpClient()
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("%s: %s", resp.Status, tail(string(raw), 300))
	}
	var r2 struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &r2); err != nil || len(r2.Choices) == 0 {
		return "", fmt.Errorf("unexpected reply: %s", tail(string(raw), 300))
	}
	return r2.Choices[0].Message.Content, nil
}

// httpClient is kept for the runner's life: completions reuse its connection instead of a TLS
// handshake (through the proxy) per keystroke pause.
func (r *Runner) httpClient() (*http.Client, error) {
	r.clientOnce.Do(func() {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = nil
		// Same extra CAs as opencode trusts, e.g. a self-signed router the chat already reaches.
		if f := os.Getenv("NODE_EXTRA_CA_CERTS"); f != "" {
			if pem, err := os.ReadFile(f); err == nil {
				pool, err := x509.SystemCertPool()
				if err != nil {
					pool = x509.NewCertPool()
				}
				pool.AppendCertsFromPEM(pem)
				tr.TLSClientConfig = &tls.Config{RootCAs: pool}
			}
		}
		if p := r.proxyURL(); p != "" {
			u, err := url.Parse(p)
			if err != nil {
				r.clientErr = fmt.Errorf("proxy %q: %w", p, err)
				return
			}
			// a model on this machine (ollama, lmstudio) is never behind the proxy
			tr.Proxy = func(req *http.Request) (*url.URL, error) {
				if h := req.URL.Hostname(); h == "localhost" || net.ParseIP(h).IsLoopback() {
					return nil, nil
				}
				return u, nil
			}
		}
		r.client = &http.Client{Transport: tr, Timeout: 60 * time.Second}
	})
	return r.client, r.clientErr
}

// Models lists what "model" can be set to, or with fast "fast_model": the endpoint's own list when
// one is set, else the backend's.
func (r *Runner) Models(ctx context.Context, fast bool) ([]string, error) {
	switch {
	case fast && r.Setup.FastURL != "":
		return r.endpointModels(ctx, r.Setup.FastURL, r.Setup.FastAPIKey)
	case compatible(r.Setup.Provider) && r.Setup.URL != "":
		return r.endpointModels(ctx, r.Setup.URL, r.Setup.APIKey)
	case r.Setup.Backend == BackendClaude:
		return []string{"opus", "sonnet", "haiku"}, nil
	case !r.Setup.Enabled():
		return nil, errors.New("AI is " + r.Setup.Describe())
	}
	cmd := exec.CommandContext(ctx, r.Setup.Bin, "models")
	cmd.Dir, cmd.Env = r.Scope.Dir, r.env()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("opencode models: %w", err)
	}
	return strings.Fields(string(out)), nil
}

func (r *Runner) endpointModels(ctx context.Context, endpoint, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	c, err := r.httpClient()
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s: %s", resp.Status, tail(string(raw), 300))
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("unexpected model list: %s", tail(string(raw), 300))
	}
	var out []string
	for _, m := range list.Data {
		out = append(out, m.ID)
	}
	return out, nil
}
