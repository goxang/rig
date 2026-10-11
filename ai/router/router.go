// Package router gives an AI a small fixed set of meta-tools over the Engine (find, call, query,
// read, open, skill, docs, file, ui) instead of one tool per action, so forty plugins do not flood its
// context.
package router

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"sort"
	"strings"

	"github.com/goxang/rig/api"
	"github.com/goxang/rig/docs"
	"github.com/goxang/rig/host"
	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/kernel/identity"
	"github.com/goxang/rig/protocol"
)

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type Router struct {
	E api.Engine
	// As is who the tools act for, such as identity.AI(user) or an agent.
	As     *identity.Subject
	Skills []host.Skill
	// Allow, when set, limits call and query to these globs, each name[:effect] (an agent's tools:).
	Allow []string
	// File and UI serve the file and ui tools; nil leaves them out.
	File func(args map[string]any) (string, bool)
	UI   func(args map[string]any) (string, bool)
	// Approve asks a person who is there to approve an inbox item now (args are the call's, with
	// any user_request); nil leaves it waiting.
	Approve func(ctx context.Context, what string, args map[string]any) bool
}

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(d string) map[string]any { return map[string]any{"type": "string", "description": d} }

// Tools are the meta-tools, with the skills' names and descriptions in skill's description.
func (r *Router) Tools() []Tool {
	var skills []string
	for _, s := range r.Skills {
		skills = append(skills, s.Name+": "+s.Description)
	}
	out := []Tool{
		{"find", "find actions, sources, skills and project resources by what they do (ranked); returns their names, effects and argument schemas. Use it before call or query when the name is unknown.",
			obj(map[string]any{"query": str("words describing what you want, e.g. \"restart service\" or \"slow sql\"")}, "query")},
		{"call", "run an action by name with its arguments. Writes go through policy: they may need the user's approval, and then wait in the inbox (rig inbox).",
			obj(map[string]any{"action": str("action name from find, e.g. runtime.restart"), "args": map[string]any{"type": "object"},
				"user_request": str("for a write: the user's own words asking for it, verbatim")}, "action")},
		{"read", "read a source as a table: as table (default, truncated), csv or stats (per column counts, min, max, mean).",
			obj(map[string]any{"source": str("source name from find, e.g. services.status"), "args": map[string]any{"type": "object"}, "as": map[string]any{"type": "string", "enum": []string{"table", "csv", "stats"}}}, "source")},
		{"open", "a resource by ref (svc://api, env://local, ...): its fields, links and the actions available on it.",
			obj(map[string]any{"ref": str("the ref")}, "ref")},
		{"skill", "load a skill's instructions. Skills: " + strings.Join(skills, "; "),
			obj(map[string]any{"name": str("skill name")}, "name")},
		{"docs", "rig's reference (every rig.yaml key, CLI, screens, contracts): the sections holding all the words.",
			obj(map[string]any{"words": str("words to look for, e.g. \"tasks steps\"")}, "words")},
	}
	if r.File != nil {
		out = append(out, Tool{"file", "the project's files, inside its directory only (deny lists apply): list (path, recursive), read (path), write (path, content), edit (path, old, new, all), move (path, to), delete (path).",
			obj(map[string]any{"action": map[string]any{"type": "string", "enum": []string{"list", "read", "write", "edit", "move", "delete"}},
				"path": str("relative to the project"), "to": str("move: new path"), "content": str("write: the whole content"),
				"old": str("edit: exact text"), "new": str("edit: replacement"), "all": map[string]any{"type": "boolean"}, "recursive": map[string]any{"type": "boolean"}}, "action")})
	}
	if r.UI != nil {
		out = append(out, Tool{"ui", "act in the rig UI the user has open: open (screen, select, filter, path), keys (keys), logs (services, grep), panels, add_query, schedule, unschedule, state; steps runs several in one call. Answers with the screen once it settles.",
			obj(map[string]any{"action": str("open, keys, logs, panels, add_query, schedule, unschedule or state"), "steps": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"screen": str("screen to open"), "select": str("row to land on"), "filter": str("narrows the list"), "keys": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}})})
	}
	return out
}

// Call runs a meta-tool; failed marks a result the model should treat as an error.
func (r *Router) Call(ctx context.Context, name string, args map[string]any) (text string, failed bool) {
	if args == nil {
		args = map[string]any{}
	}
	s := func(k string) string { v, _ := args[k].(string); return v }
	m := func(k string) protocol.Args { v, _ := args[k].(map[string]any); return v }
	var err error
	switch name {
	case "find":
		text = r.find(ctx, s("query"))
	case "call":
		text, err = r.call(ctx, s("action"), m("args"), args)
	case "read":
		text, err = r.query(ctx, s("source"), m("args"), s("as"))
	case "open":
		text, err = r.open(ctx, s("ref"))
	case "skill":
		text, err = r.skill(s("name"))
	case "docs":
		text, err = docsText(s("words"))
	case "file":
		if r.File != nil {
			return r.File(args)
		}
		err = errors.New("no file tool here")
	case "ui":
		if r.UI != nil {
			return r.UI(args)
		}
		err = errors.New("no rig UI is attached: use read")
	default:
		err = fmt.Errorf("no tool %q; tools: find, call, read, open, skill, docs", name)
	}
	if err != nil {
		return err.Error(), true
	}
	return text, false
}

// allowed matches name (and its effect) against Allow.
func (r *Router) allowed(name, effect string) bool {
	if r.Allow == nil {
		return true
	}
	for _, g := range r.Allow {
		pat, eff, _ := strings.Cut(g, ":")
		if ok, _ := path.Match(pat, name); ok && (eff == "" || eff == effect) {
			return true
		}
	}
	return false
}

func (r *Router) call(ctx context.Context, action string, args protocol.Args, raw map[string]any) (string, error) {
	spec, ok := r.E.Provides(ctx).Actions[action]
	if !ok {
		return "", fmt.Errorf("no action %q: use find", action)
	}
	if !r.allowed(action, spec.Effect) {
		return "", fmt.Errorf("%s is not among this agent's tools", action)
	}
	j, res, err := r.E.Run(ctx, action, args, api.RunOpts{As: r.As})
	var pe *protocol.Error
	if errors.As(err, &pe) && pe.Code == protocol.CodeNeedsInput {
		id := inboxID(pe.Message)
		if r.Approve == nil || id == "" || !r.Approve(ctx, fmt.Sprintf("%s %s", action, argsText(args)), raw) {
			return pe.Message + "\nTell the user; it runs once they approve it.", nil
		}
		res, err = r.E.Decide(ctx, id, true)
	}
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if res != nil && res.Message != "" {
		b.WriteString(res.Message + "\n")
	}
	if j != nil {
		for _, l := range tail(j.Output(), 50) {
			b.WriteString(l + "\n")
		}
	}
	if b.Len() == 0 {
		return "done", nil
	}
	return untrusted(action, b.String()), nil
}

func inboxID(msg string) string {
	_, id, ok := strings.Cut(msg, "rig inbox approve ")
	if !ok {
		return ""
	}
	return strings.Fields(id + " ")[0]
}

func argsText(a protocol.Args) string {
	if len(a) == 0 {
		return ""
	}
	b, _ := json.Marshal(a)
	return string(b)
}

func tail(l []string, n int) []string {
	if len(l) > n {
		return l[len(l)-n:]
	}
	return l
}

// untrusted marks what a source or action returned: data, never instructions.
func untrusted(from, text string) string {
	return fmt.Sprintf("<untrusted source=%q>\n%s\n</untrusted>", from, strings.TrimRight(text, "\n"))
}

func (r *Router) query(ctx context.Context, source string, args protocol.Args, as string) (string, error) {
	if !r.allowed(source, protocol.EffectRead) {
		return "", fmt.Errorf("%s is not among this agent's tools", source)
	}
	f, err := r.E.Query(ctx, source, args)
	if err != nil {
		return "", err
	}
	return untrusted(source, shape(f, as)), nil
}

const maxRows, maxCell = 60, 120

// shape renders a frame for a model: few tokens, the size said when cut.
func shape(f *protocol.Frame, as string) string {
	if f == nil || len(f.Fields) == 0 {
		return "(empty)"
	}
	n := frames.Len(f)
	cell := func(fl protocol.Field, v any) string {
		s := frames.Format(fl, v)
		if len(s) > maxCell {
			s = s[:maxCell] + "…"
		}
		return s
	}
	var b strings.Builder
	switch as {
	case "stats":
		fmt.Fprintf(&b, "%d rows\n", n)
		for i, fl := range f.Fields {
			lo, hi, sum, nums := math.Inf(1), math.Inf(-1), 0.0, 0
			distinct := map[string]bool{}
			for _, v := range f.Columns[i] {
				if x, ok := v.(float64); ok {
					lo, hi, sum, nums = math.Min(lo, x), math.Max(hi, x), sum+x, nums+1
				}
				if len(distinct) < 1000 {
					distinct[fmt.Sprint(v)] = true
				}
			}
			if nums > 0 {
				fmt.Fprintf(&b, "%s: min %g max %g mean %g\n", fl.Name, lo, hi, sum/float64(nums))
			} else {
				fmt.Fprintf(&b, "%s: %d distinct\n", fl.Name, len(distinct))
			}
		}
		return b.String()
	case "csv":
		w := csv.NewWriter(&b)
		var head []string
		for _, fl := range f.Fields {
			head = append(head, fl.Name)
		}
		_ = w.Write(head)
		for i := 0; i < n; i++ {
			var row []string
			for c, fl := range f.Fields {
				row = append(row, frames.Format(fl, f.Columns[c][i]))
			}
			_ = w.Write(row)
		}
		w.Flush()
		return b.String()
	}
	for i, fl := range f.Fields {
		if i > 0 {
			b.WriteString("\t")
		}
		b.WriteString(fl.Name)
	}
	b.WriteString("\n")
	for i := 0; i < n && i < maxRows; i++ {
		for c, fl := range f.Fields {
			if c > 0 {
				b.WriteString("\t")
			}
			b.WriteString(cell(fl, f.Columns[c][i]))
		}
		b.WriteString("\n")
	}
	if n > maxRows {
		fmt.Fprintf(&b, "… %d of %d rows shown: as csv or stats for all\n", maxRows, n)
	}
	return b.String()
}

func (r *Router) open(ctx context.Context, ref string) (string, error) {
	res, err := r.E.Resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	return untrusted(ref, string(b)), nil
}

func (r *Router) skill(name string) (string, error) {
	for _, s := range r.Skills {
		if s.Name == name {
			return s.Body, nil
		}
	}
	return "", fmt.Errorf("no skill %q", name)
}

func docsText(words string) (string, error) {
	var b strings.Builder
	for _, n := range docs.Names {
		raw, _ := docs.FS.ReadFile(n + ".md")
		found, _ := docs.Sections(string(raw), words)
		b.WriteString(found)
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("no section holds %q", words)
	}
	out := b.String()
	if len(out) > 40_000 {
		out = out[:40_000] + "\n… more: narrow the words"
	}
	return out, nil
}

type entry struct {
	kind, name, text string
}

// find ranks everything the engine offers with BM25 over names and descriptions.
func (r *Router) find(ctx context.Context, q string) string {
	pr := r.E.Provides(ctx)
	var es []entry
	for n, a := range pr.Actions {
		if r.allowed(n, a.Effect) {
			args, _ := json.Marshal(a.Args)
			es = append(es, entry{"action", n, fmt.Sprintf("%s (%s) %s args %s", n, a.Effect, a.Description, args)})
		}
	}
	for n, s := range pr.Sources {
		if r.allowed(n, protocol.EffectRead) {
			args, _ := json.Marshal(s.Args)
			es = append(es, entry{"source", n, fmt.Sprintf("%s %s args %s", n, s.Description, args)})
		}
	}
	for _, s := range r.Skills {
		es = append(es, entry{"skill", s.Name, s.Name + " " + s.Description})
	}
	if c, err := r.E.Catalog(ctx); err == nil {
		for _, res := range c.List("") {
			if ref := res.Ref(); ref != "" {
				es = append(es, entry{res.Kind, ref, res.Kind + " " + res.Name + " " + ref})
			}
		}
	}
	ranked := rank(es, q)
	if len(ranked) == 0 {
		return "nothing matches; try other words, or call plugin.search to look for a plugin that does it"
	}
	var b strings.Builder
	for i, e := range ranked {
		if i == 12 {
			break
		}
		fmt.Fprintf(&b, "%s %s\n", e.kind, e.text)
	}
	return b.String()
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
}

func rank(es []entry, q string) []entry {
	terms := words(q)
	toks := make([][]string, len(es))
	df := map[string]int{}
	total := 0
	for i, e := range es {
		toks[i] = words(e.name + " " + e.text)
		total += len(toks[i])
		seen := map[string]bool{}
		for _, w := range toks[i] {
			if !seen[w] {
				seen[w] = true
				df[w]++
			}
		}
	}
	if len(es) == 0 {
		return nil
	}
	avg := float64(total) / float64(len(es))
	type scored struct {
		e entry
		s float64
	}
	var out []scored
	for i, d := range toks {
		var s float64
		for _, t := range terms {
			tf := 0
			for _, w := range d {
				if w == t || len(t) > 3 && strings.HasPrefix(w, t) {
					tf++
				}
			}
			if tf == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(es))-float64(df[t])+0.5)/(float64(df[t])+0.5))
			s += idf * float64(tf) * 2.2 / (float64(tf) + 1.2*(0.25+0.75*float64(len(d))/avg))
		}
		if strings.Contains(es[i].name, strings.ToLower(q)) {
			s += 2
		}
		if s > 0 {
			out = append(out, scored{es[i], s})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].s != out[j].s {
			return out[i].s > out[j].s
		}
		return out[i].e.name < out[j].e.name
	})
	res := make([]entry, len(out))
	for i, o := range out {
		res[i] = o.e
	}
	return res
}
