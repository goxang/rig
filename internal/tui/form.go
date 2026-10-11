package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/goxang/rig/kernel/view"
	"github.com/goxang/rig/protocol"
)

// formState is a form generated from a JSON Schema: the action's args, or schema: in the view.
type formState struct {
	fields []formField
	sel    int
	vals   protocol.Args
}

type formField struct {
	name, typ, help string
	enum            []string
	required        bool
}

func schemaFields(s protocol.Schema) []formField {
	props, _ := s["properties"].(map[string]any)
	var req []string
	for _, r := range asList(s["required"]) {
		req = append(req, fmt.Sprint(r))
	}
	var out []formField
	for name, p := range props {
		pm, _ := p.(map[string]any)
		f := formField{name: name, typ: fmt.Sprint(cmp.Or(pm["type"], any("string"))), help: str(pm["description"]), required: slices.Contains(req, name)}
		for _, e := range asList(pm["enum"]) {
			f.enum = append(f.enum, fmt.Sprint(e))
		}
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b formField) int {
		if a.required != b.required {
			return map[bool]int{true: -1, false: 1}[a.required]
		}
		return strings.Compare(a.name, b.name)
	})
	return out
}

func asList(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case []string:
		out := make([]any, len(t))
		for i, s := range t {
			out[i] = s
		}
		return out
	}
	return nil
}

func (t *viewTab) form(m *model, wn *wnode) *formState {
	if st, ok := wn.st.(*formState); ok {
		return st
	}
	schema, _ := wn.n.Props["schema"].(map[string]any)
	if schema == nil {
		if a, ok := m.eng().Action(view.Prop(wn.n, "action")); ok {
			schema = a.Args
		}
	}
	st := &formState{fields: schemaFields(schema), vals: protocol.Args{}}
	if d, ok := wn.n.Props["values"].(map[string]any); ok {
		for k, v := range d {
			st.vals[k] = v
		}
	}
	wn.st = st
	return st
}

func (t *viewTab) formBody(m *model, wn *wnode, w, h int, focused bool) string {
	st := t.form(m, wn)
	nameW := 0
	for _, f := range st.fields {
		nameW = max(nameW, len(f.name)+1)
	}
	var out []string
	for i, f := range st.fields {
		v := sDim.Render(cmp.Or(f.help, f.typ))
		if x, ok := st.vals[f.name]; ok {
			v = fmt.Sprint(x)
		}
		name := f.name
		if f.required {
			name += "*"
		}
		line := padRight(sDim.Render(padRight(name, nameW))+"  "+v, w)
		if i == st.sel && focused {
			line = sCursor.Render(line)
		}
		out = append(out, line)
	}
	submit := " " + cmp.Or(view.Prop(wn.n, "submit"), "run "+view.Prop(wn.n, "action")) + " "
	if st.sel == len(st.fields) && focused {
		submit = sTabOn.Render(submit)
	} else {
		submit = sTabOff.Render(submit)
	}
	out = append(out, "", submit)
	return clip(strings.Join(out, "\n"), w, h)
}

func (t *viewTab) formKey(m *model, wn *wnode, st *formState, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "up", "k":
		st.sel = max(0, st.sel-1)
	case "down", "j":
		st.sel = min(len(st.fields), st.sel+1)
	case "ctrl+s":
		return t.submit(m, wn, st)
	case "enter":
		if st.sel == len(st.fields) {
			return t.submit(m, wn, st)
		}
		f := st.fields[st.sel]
		switch {
		case len(f.enum) > 0:
			m.pick(f.name, f.enum, nil, 0, false, func(sel []string) tea.Cmd {
				if len(sel) > 0 {
					st.vals[f.name] = sel[0]
				}
				return nil
			})
		case f.typ == "boolean":
			b, _ := st.vals[f.name].(bool)
			st.vals[f.name] = !b
		default:
			cur := ""
			if v, ok := st.vals[f.name]; ok {
				cur = fmt.Sprint(v)
			}
			m.ask(f.name, cur, func(s string) tea.Cmd {
				st.vals[f.name] = typed(s, f.typ)
				return nil
			})
		}
	}
	return nil
}

func typed(s, typ string) any {
	switch typ {
	case "integer":
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	case "number":
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
	case "array":
		return strings.Fields(s)
	}
	return s
}

func (t *viewTab) submit(m *model, wn *wnode, st *formState) tea.Cmd {
	for _, f := range st.fields {
		if _, ok := st.vals[f.name]; f.required && !ok {
			m.setStatus(f.name+" is required", true)
			return nil
		}
	}
	action := view.Prop(wn.n, "action")
	if action == "" {
		m.setStatus("the form has no action:", true)
		return nil
	}
	return t.run(m, action, action, st.vals)
}
