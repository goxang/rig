package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
)

// kvTab browses a key-value store folder by folder, shows a value, edits it (in $EDITOR or inline),
// and restarts the services that read the key so they pick the change up.
type kvTab struct {
	comp     string
	prefix   string
	keys     []string
	list     *grid
	value    []byte
	valueFor string
	err      string
	related  []string
	filter   string
	scroll   int
}

type kvKeysMsg struct {
	gen    int
	prefix string
	keys   []string
	err    error
}

type kvValueMsg struct {
	gen   int
	key   string
	value []byte
	err   error
}

type kvEditedMsg struct {
	comp     string
	key      string
	file     string
	original []byte
	err      error
}

func (t *kvTab) name() string { return "KV" }
func (t *kvTab) typing() bool { return false }
func (t *kvTab) hints() [][2]string {
	h := [][2]string{{"enter", "open"}, {"←", "up"}, {"e", "edit"}, {"i", "edit inline"}, {"n", "new key"}, {"D", "delete"}, {"/", "filter"}, {"J/K", "scroll value"}}
	if len(t.related) > 0 {
		h = append([][2]string{{"R", "restart " + strings.Join(t.related, ",")}}, h...)
	}
	return append(h, [2]string{"c", "store"})
}
func (t *kvTab) interval() time.Duration { return 0 }

func (t *kvTab) comps(m *model) []string { return m.app.Names(core.KindKV) }

func (t *kvTab) open(m *model) tea.Cmd {
	t.list = newGrid("kv", col("", 2), col("KEY", 0))
	t.list.sortBy = 1
	if cs := t.comps(m); len(cs) > 0 {
		t.comp = cs[0]
	}
	return t.load(m)
}

func (t *kvTab) refresh(m *model) tea.Cmd { return nil }

func (t *kvTab) store(m *model) (core.KV, error) {
	kv, _, err := engine.Get[core.KV](m.app, core.KindKV, t.comp)
	return kv, err
}

func (t *kvTab) load(m *model) tea.Cmd {
	if t.comp == "" {
		t.err = "no kv component in this environment (add one: type: consul)"
		return nil
	}
	a, gen, ctx, prefix, comp := m.app, m.gen, m.ctx, t.prefix, t.comp
	return func() tea.Msg {
		kv, _, err := engine.Get[core.KV](a, core.KindKV, comp)
		if err != nil {
			return kvKeysMsg{gen: gen, prefix: prefix, err: err}
		}
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		keys, err := kv.List(c, prefix)
		return kvKeysMsg{gen: gen, prefix: prefix, keys: keys, err: err}
	}
}

func (t *kvTab) fetch(m *model, key string) tea.Cmd {
	a, gen, ctx, comp := m.app, m.gen, m.ctx, t.comp
	return func() tea.Msg {
		kv, _, err := engine.Get[core.KV](a, core.KindKV, comp)
		if err != nil {
			return kvValueMsg{gen: gen, key: key, err: err}
		}
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		v, _, err := kv.Get(c, key)
		return kvValueMsg{gen: gen, key: key, value: v, err: err}
	}
}

// entries are the folders and keys directly under the current prefix.
func (t *kvTab) entries() []grow {
	seen := map[string]bool{}
	var rows []grow
	f := strings.ToLower(t.filter)
	for _, k := range t.keys {
		rest := strings.TrimPrefix(k, t.prefix)
		if rest == "" {
			continue
		}
		child, _, folder := strings.Cut(rest, "/")
		id := t.prefix + child
		if folder {
			id += "/"
		}
		if seen[id] || f != "" && !strings.Contains(strings.ToLower(child), f) {
			continue
		}
		seen[id] = true
		icon, name := sDim.Render("·"), child
		if folder {
			icon, name = sAccent.Render("▸"), sAccent.Render(child+"/")
		}
		rows = append(rows, grow{id: id, cells: []string{icon, name}, keys: []any{map[bool]string{true: "a", false: "b"}[folder], child}})
	}
	return rows
}

func (t *kvTab) enter(m *model, id string) tea.Cmd {
	if strings.HasSuffix(id, "/") {
		t.prefix, t.filter = id, ""
		t.list.sel, t.value, t.valueFor = 0, nil, ""
		t.list.set(t.entries())
		return t.load(m)
	}
	t.scroll = 0
	return t.fetch(m, id)
}

func (t *kvTab) up(m *model) tea.Cmd {
	if t.prefix == "" {
		return nil
	}
	p := strings.TrimSuffix(t.prefix, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		t.prefix = p[:i+1]
	} else {
		t.prefix = ""
	}
	t.list.sel, t.filter = 0, ""
	return t.load(m)
}

// relatedServices finds the services that read a key: a path segment equal to the service's name,
// its SERVICE_NAME, or its --service-name argument.
func relatedServices(m *model, key string) []string {
	segs := map[string]bool{}
	for _, s := range strings.Split(key, "/") {
		segs[strings.ToLower(s)] = true
	}
	var out []string
	for _, n := range m.app.Spec.ServiceNames() {
		s := m.app.Spec.Services[n]
		names := []string{n, s.Env["SERVICE_NAME"]}
		if s.Run != nil {
			for _, a := range s.Run.Args {
				if v, ok := strings.CutPrefix(a, "--service-name="); ok {
					names = append(names, v)
				}
			}
		}
		for _, x := range names {
			if x != "" && segs[strings.ToLower(x)] {
				out = append(out, n)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func (t *kvTab) save(m *model, key string, value []byte) tea.Cmd {
	return t.saveIn(m, t.comp, key, value)
}

func (t *kvTab) saveIn(m *model, comp, key string, value []byte) tea.Cmd {
	t.related = relatedServices(m, key)
	t.value, t.valueFor = value, key
	a := m.app
	return m.act("save "+key, true, func(ctx context.Context) error {
		kv, _, err := engine.Get[core.KV](a, core.KindKV, comp)
		if err != nil {
			return err
		}
		return kv.Put(ctx, key, value)
	})
}

func (t *kvTab) edit(m *model, key string, value []byte) tea.Cmd {
	return editKV(m, t.comp, key, value)
}

// editKV opens a key's value in $VISUAL/$EDITOR; the KV screen saves it when the editor exits.
func editKV(m *model, comp, key string, value []byte) tea.Cmd {
	f, err := os.CreateTemp("", "rig-kv-*"+extFor(value))
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	_, _ = f.Write(pretty2(value))
	f.Close()
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		ed = "vi"
	}
	file := f.Name()
	c := exec.Command("sh", "-c", ed+` "$1"`, "rig-edit", file)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return kvEditedMsg{comp: comp, key: key, file: file, original: value, err: err}
	})
}

func extFor(v []byte) string {
	if json.Valid(bytes.TrimSpace(v)) && len(bytes.TrimSpace(v)) > 0 {
		return ".json"
	}
	return ".txt"
}

// pretty2 indents JSON values for editing; others pass through.
func pretty2(v []byte) []byte {
	var out bytes.Buffer
	if json.Indent(&out, v, "", "  ") == nil {
		return out.Bytes()
	}
	return v
}

func (t *kvTab) update(m *model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case kvKeysMsg:
		if msg.gen != m.gen || msg.prefix != t.prefix {
			return nil
		}
		t.err = ""
		if msg.err != nil {
			t.err = msg.err.Error()
		}
		t.keys = msg.keys
		t.list.set(t.entries())
	case kvValueMsg:
		if msg.gen != m.gen {
			return nil
		}
		if msg.err != nil {
			m.setStatus(msg.key+": "+msg.err.Error(), true)
			return nil
		}
		t.value, t.valueFor = msg.value, msg.key
	case kvEditedMsg:
		defer os.Remove(msg.file)
		if msg.err != nil {
			m.setStatus("editor: "+msg.err.Error(), true)
			return nil
		}
		raw, err := os.ReadFile(msg.file)
		if err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		if bytes.Equal(bytes.TrimSpace(raw), bytes.TrimSpace(pretty2(msg.original))) {
			m.setStatus("no change to "+msg.key, false)
			return nil
		}
		if json.Valid(bytes.TrimSpace(msg.original)) && len(bytes.TrimSpace(msg.original)) > 0 && !json.Valid(bytes.TrimSpace(raw)) {
			m.setStatus(msg.key+" was JSON and the edit is not: not saved", true)
			return nil
		}
		return t.saveIn(m, msg.comp, msg.key, raw)
	case tea.KeyMsg:
		if t.list.key(msg) {
			if r, ok := t.list.current(); ok && !strings.HasSuffix(r.id, "/") {
				t.scroll = 0
				return t.fetch(m, r.id)
			}
			return nil
		}
		r, ok := t.list.current()
		switch msg.String() {
		case "enter", "right":
			if ok {
				return t.enter(m, r.id)
			}
		case "left", "backspace", "esc":
			return t.up(m)
		case "e":
			if ok && !strings.HasSuffix(r.id, "/") {
				if t.valueFor != r.id {
					m.setStatus("loading "+r.id+", press e again", false)
					return t.fetch(m, r.id)
				}
				return t.edit(m, r.id, t.value)
			}
		case "i":
			if ok && !strings.HasSuffix(r.id, "/") && t.valueFor == r.id {
				if bytes.Contains(t.value, []byte("\n")) || len(t.value) > 400 {
					m.setStatus("long value: use e to edit it in $EDITOR", true)
					return nil
				}
				key := r.id
				m.ask(key, string(t.value), func(v string) tea.Cmd { return t.save(m, key, []byte(v)) })
			}
		case "n":
			m.ask("new key", t.prefix, func(k string) tea.Cmd {
				k = strings.TrimSpace(k)
				if k == "" || strings.HasSuffix(k, "/") {
					return nil
				}
				return t.edit(m, k, nil)
			})
		case "D":
			if ok && !strings.HasSuffix(r.id, "/") {
				key, comp, a := r.id, t.comp, m.app
				return m.act("delete "+key, true, func(ctx context.Context) error {
					kv, _, err := engine.Get[core.KV](a, core.KindKV, comp)
					if err != nil {
						return err
					}
					return kv.Delete(ctx, key)
				})
			}
		case "R":
			if len(t.related) > 0 {
				names, a := t.related, m.app
				t.related = nil
				return m.act(label("restart", names), false, func(ctx context.Context) error {
					return each(names, func(n string) error { return a.Restart(ctx, n) })
				})
			}
		case "/":
			m.ask("filter keys", t.filter, func(v string) tea.Cmd {
				t.filter = strings.TrimSpace(v)
				t.list.set(t.entries())
				return nil
			})
		case "r":
			return t.load(m)
		case "c":
			cs := t.comps(m)
			m.pick("key-value store", cs, nil, 0, false, func(c []string) tea.Cmd {
				if len(c) == 0 {
					return nil
				}
				t.comp, t.prefix = c[0], ""
				return t.load(m)
			})
		case "J":
			t.scroll += 10
		case "K":
			t.scroll = max(0, t.scroll-10)
		}
	}
	return nil
}

func (t *kvTab) click(m *model, h hit) tea.Cmd {
	if h.id == "kv:up" {
		return t.up(m)
	}
	if t.list.click(h) {
		if r, ok := t.list.current(); ok {
			if h.double || strings.HasSuffix(r.id, "/") && h.double {
				return t.enter(m, r.id)
			}
			if !strings.HasSuffix(r.id, "/") {
				return t.fetch(m, r.id)
			}
		}
	}
	return nil
}

func (t *kvTab) view(m *model, w, h int) string {
	if t.err != "" && len(t.keys) == 0 {
		return panel("key-value", sRed.Render(wrap(t.err, w-4)), w, h, true)
	}
	t.list.set(t.entries())
	lw := min(max(w/3, 36), 70)
	where := "/" + t.prefix
	title := t.comp + " · " + where
	if t.filter != "" {
		title += " · filter " + t.filter
	}
	up := ""
	if t.prefix != "" {
		up = sKey.Render("‹ up") + "\n"
		m.zone("kv:up", 1, 1, 4, 1)
	}
	upH := lipgloss.Height(up) - 1
	if up == "" {
		upH = 0
	}
	left := panel(title, up+t.list.view(m, 1, 1+upH, lw-2, h-2-upH, true), lw, h, true)

	vt := "value"
	var body string
	r, _ := t.list.current()
	switch {
	case t.valueFor != "" && t.valueFor == r.id:
		vt = t.valueFor
		lines := strings.Split(string(pretty2(t.value)), "\n")
		t.scroll = min(t.scroll, max(0, len(lines)-1))
		body = colorYAML(strings.Join(lines[t.scroll:], "\n"))
		if rel := relatedServices(m, t.valueFor); len(rel) > 0 {
			vt += "  ·  read by " + strings.Join(rel, ", ")
		}
	case strings.HasSuffix(r.id, "/"):
		body = sDim.Render("folder: enter opens it")
	default:
		body = sDim.Render("select a key")
	}
	if len(t.related) > 0 {
		body = sAmber.Render("saved: press R to restart "+strings.Join(t.related, ", ")+" so they read it") + "\n\n" + body
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, panel(vt, body, w-lw, h, false))
}
