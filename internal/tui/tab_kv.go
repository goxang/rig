package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
	// tree is a JSON value field by field; focused, the keys edit it and every change saves
	tree    *jsonTree
	treeFor string
	inTree  bool
	// treeNext walks into the tree once the value being fetched arrives (enter on a key)
	treeNext bool
	// jumpKey and jumpPath are a search hit being opened: the key to select once its folder
	// loads, and the field to select once its value does
	jumpKey, jumpPath string
	// viaSearch marks the key currently shown as reached from a search jump, so esc from its
	// value/tree reopens the last search results instead of just backing out to the key list
	viaSearch  bool
	searchHits []kvHit
	// idx is the last full read of comp, kept across searches in this run; I forces a re-read
	idx     []kvHit
	idxComp string
}

// kvHit is one search row: a key, the path of a field of its JSON value ($.a.b; empty for the key
// itself), and the value.
type kvHit struct{ key, path, value string }

type kvIndexMsg struct {
	gen  int
	hits []kvHit
	err  error
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
	if t.inTree {
		back := "back to keys"
		if t.viaSearch {
			back = "back to search"
		}
		return [][2]string{{"enter e", "edit field"}, {"a", "add field"}, {"D", "delete field"}, {"←→ space", "fold"}, {"+ - z", "expand all, fold all, toggle"}, {"y", "copy value"}, {"s", "config file"}, {"esc", back}}
	}
	h := [][2]string{{"enter", "open (JSON: field by field)"}, {"←", "up"}, {"e", "edit"}, {"i", "edit inline"}, {"n", "new key"}, {"D", "delete"}, {"/", "search keys and values"}, {"I", "reindex search (re-read store)"}, {"s", "config file"}, {"J/K", "scroll value"}}
	if len(t.related) > 0 {
		h = append([][2]string{{"R", "restart " + strings.Join(t.related, ",")}}, h...)
	}
	return append(h, [2]string{"o", "editor"}, [2]string{"c", "store"}, [2]string{"F", "load config files (kv-* tasks)"})
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

func (t *kvTab) load(m *model) tea.Cmd {
	if t.comp == "" {
		t.err = "no kv component in this environment (add one: type: consul)"
		return nil
	}
	a, gen, ctx, prefix, comp := m.app, m.gen, m.work(), t.prefix, t.comp
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
	a, gen, ctx, comp := m.app, m.gen, m.work(), t.comp
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
	t.viaSearch = false
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
	t.viaSearch = false
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
	if !t.inTree {
		t.setTree()
	}
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
	file := f.Name()
	c := exec.Command("sh", "-c", editorCmd()+` "$1"`, "rig-edit", file)
	return execProcess(c, func(err error) tea.Msg {
		return kvEditedMsg{comp: comp, key: key, file: file, original: value, err: err}
	})
}

// editors are the choices of o; each command must wait until the file is closed, since the value is
// saved when it returns.
var editors = [][2]string{
	{"nano", "nano"},
	{"vim", "vim"},
	{"VS Code", "code --wait"},
	{"text editor (GNOME)", "gnome-text-editor --standalone"},
	{"Kate", "kate --block"},
	{"Mousepad", "mousepad --disable-server"},
	{"Notepad", "notepad"},
}

func editorFile() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "rig", "editor")
}

// editorCmd is $RIG_EDITOR, the one picked with o, $VISUAL, $EDITOR, or vi.
func editorCmd() string {
	if e := os.Getenv("RIG_EDITOR"); e != "" {
		return e
	}
	if raw, err := os.ReadFile(editorFile()); err == nil && len(bytes.TrimSpace(raw)) > 0 {
		return string(bytes.TrimSpace(raw))
	}
	for _, v := range []string{"VISUAL", "EDITOR"} {
		if e := os.Getenv(v); e != "" {
			return e
		}
	}
	return "vi"
}

// pickEditor offers the editors installed here and remembers the choice for every project.
func pickEditor(m *model) {
	var names, desc []string
	cmds := map[string]string{}
	sel := 0
	for _, e := range editors {
		bin, _, _ := strings.Cut(e[1], " ")
		if _, err := exec.LookPath(bin); err != nil {
			continue
		}
		if e[1] == editorCmd() {
			sel = len(names)
		}
		names, desc = append(names, e[0]), append(desc, e[1])
		cmds[e[0]] = e[1]
	}
	if len(names) == 0 {
		m.setStatus("none of nano, vim, code, gnome-text-editor is installed: set $RIG_EDITOR", true)
		return
	}
	m.pick("edit values with", names, desc, sel, false, func(c []string) tea.Cmd {
		if len(c) == 0 {
			return nil
		}
		f := editorFile()
		err := os.MkdirAll(filepath.Dir(f), 0o755)
		if err == nil {
			err = os.WriteFile(f, []byte(cmds[c[0]]+"\n"), 0o644)
		}
		if err != nil {
			m.setStatus("remember editor: "+err.Error(), true)
			return nil
		}
		m.setStatus("values open in "+c[0], false)
		return nil
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
		if k := t.jumpKey; k != "" {
			t.jumpKey = ""
			for i, r := range t.list.rows {
				if r.id == k {
					t.list.sel = i
				}
			}
			t.scroll, t.treeNext = 0, t.jumpPath != ""
			return t.fetch(m, k)
		}
	case kvValueMsg:
		if msg.gen != m.gen {
			return nil
		}
		if msg.err != nil {
			m.setStatus(msg.key+": "+msg.err.Error(), true)
			return nil
		}
		t.value, t.valueFor = msg.value, msg.key
		t.setTree()
		if t.treeNext && t.tree != nil {
			t.inTree = true
			if t.jumpPath != "" {
				t.tree.selectPath(t.jumpPath)
			}
		}
		t.treeNext, t.jumpPath = false, ""
	case kvIndexMsg:
		if msg.gen != m.gen {
			return nil
		}
		if msg.err != nil {
			m.setStatus("search: "+msg.err.Error(), true)
			return nil
		}
		t.searchHits = msg.hits
		t.idx, t.idxComp = msg.hits, t.comp
		t.pickHit(m, msg.hits)
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
		if t.inTree {
			return t.treeKey(m, msg)
		}
		if t.list.key(msg) {
			if r, ok := t.list.current(); ok && !strings.HasSuffix(r.id, "/") {
				t.scroll, t.viaSearch = 0, false
				return t.fetch(m, r.id)
			}
			return nil
		}
		r, ok := t.list.current()
		switch msg.String() {
		case "enter", "right":
			if !ok {
				return nil
			}
			if !strings.HasSuffix(r.id, "/") {
				if t.tree != nil && t.treeFor == r.id {
					t.inTree = true
					return nil
				}
				t.treeNext = msg.String() == "enter"
			}
			return t.enter(m, r.id)
		case "esc":
			if t.viaSearch && len(t.searchHits) > 0 {
				t.pickHit(m, t.searchHits)
				return nil
			}
			return t.up(m)
		case "left", "backspace":
			return t.up(m)
		case "s":
			if ok && !strings.HasSuffix(r.id, "/") {
				return t.openSource(m, r.id, "")
			}
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
				m.askAI(key, string(t.value), "the new value of configuration key "+key+" in "+t.comp+", same format as now", func(v string) tea.Cmd { return t.save(m, key, []byte(v)) })
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
		case "F":
			m.pickTask("kv-")
			return nil
		case "o":
			pickEditor(m)
			return nil
		case "R":
			if len(t.related) > 0 {
				names, a := t.related, m.app
				t.related = nil
				return m.act(label("restart", names), false, func(ctx context.Context) error {
					return each(names, func(n string) error { return a.Restart(ctx, n) })
				})
			}
		case "/":
			if t.idxComp == t.comp && t.idx != nil {
				t.pickHit(m, t.idx)
				return nil
			}
			m.setStatus("reading every key of "+t.comp+"…", false)
			return t.index(m)
		case "I":
			m.setStatus("reading every key of "+t.comp+"…", false)
			return t.index(m)
		case "r":
			return t.load(m)
		case "c":
			cs := t.comps(m)
			m.pick("key-value store", cs, nil, 0, false, func(c []string) tea.Cmd {
				if len(c) == 0 {
					return nil
				}
				t.comp, t.prefix, t.viaSearch = c[0], "", false
				return t.load(m)
			})
		case "t":
			if t.tree != nil && ok && t.treeFor == r.id {
				t.inTree = true
			} else {
				m.setStatus("not a JSON value: e edits it as text", true)
			}
		case "J":
			t.scroll += 10
		case "K":
			t.scroll = max(0, t.scroll-10)
		}
	}
	return nil
}

func (t *kvTab) click(m *model, h hit) tea.Cmd {
	if t.tree != nil && t.tree.click(h) {
		t.inTree = true
		if h.double && !t.tree.current().container() {
			return t.editField(m)
		}
		return nil
	}
	t.inTree, t.viaSearch = false, false
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
	case t.tree != nil && t.treeFor == r.id:
		vt = t.valueFor + " · " + t.tree.current().path()
		if !t.inTree {
			vt += sDim.Render("  (enter or click: edit fields)")
		} else {
			vt += sDim.Render("  z fold/expand all")
		}
		body = t.tree.view(m, lw+1, 1, w-lw-2, h-2-lipgloss.Height(t.relatedNote()))
		if rel := relatedServices(m, t.valueFor); len(rel) > 0 {
			vt += "  ·  read by " + strings.Join(rel, ", ")
		}
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
	if n := t.relatedNote(); n != "" {
		body = n + "\n" + body
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, panel(vt, body, w-lw, h, t.inTree))
}

func (t *kvTab) relatedNote() string {
	if len(t.related) == 0 {
		return ""
	}
	return sAmber.Render("saved: press R to restart "+strings.Join(t.related, ", ")+" so they read it") + "\n"
}

// setTree shows a JSON object or array value field by field, keeping the folds and the place of
// the key it showed before.
func (t *kvTab) setTree() {
	v := bytes.TrimSpace(t.value)
	if len(v) == 0 || v[0] != '{' && v[0] != '[' {
		t.tree, t.treeFor, t.inTree = nil, "", false
		return
	}
	tree, err := newJSONTree("kv:tree", v)
	if err != nil {
		t.tree, t.treeFor, t.inTree = nil, "", false
		return
	}
	if t.tree != nil && t.treeFor == t.valueFor {
		tree.sel = t.tree.sel
		tree.flatten()
	} else {
		t.inTree = false
	}
	t.tree, t.treeFor = tree, t.valueFor
}

func (t *kvTab) treeKey(m *model, k tea.KeyMsg) tea.Cmd {
	n := t.tree.current()
	switch k.String() {
	case "s":
		return t.openSource(m, t.treeFor, n.path())
	case "esc":
		t.inTree = false
		if t.viaSearch && len(t.searchHits) > 0 {
			t.pickHit(m, t.searchHits)
		}
	case "q":
		t.inTree = false
	case "enter", "e":
		if n.container() {
			n.closed = !n.closed
			t.tree.flatten()
			return nil
		}
		return t.editField(m)
	case "a":
		parent := n
		if !n.container() {
			parent = n.parent
		}
		if parent == nil {
			return nil
		}
		value := func(key string) {
			at := parent.path() + "." + key
			if parent.kind == 'a' {
				at = parent.path() + "[+]"
			}
			m.ask("value of "+at+" (JSON, or text)", "", func(v string) tea.Cmd {
				kid := &jnode{key: key, parent: parent}
				kid.set(v)
				parent.kids = append(parent.kids, kid)
				parent.closed = false
				t.tree.flatten()
				return t.saveTree(m, "add "+kid.path())
			})
		}
		if parent.kind == 'a' {
			value("")
			return nil
		}
		m.ask("new field in "+parent.path(), "", func(key string) tea.Cmd {
			if key = strings.TrimSpace(key); key != "" {
				value(key)
			}
			return nil
		})
	case "D", "delete":
		if n.parent == nil {
			return nil
		}
		path := n.path()
		n.remove()
		t.tree.flatten()
		return t.saveTree(m, "delete "+path)
	case "y":
		copyText(n.text())
		m.setStatus("copied "+n.path(), false)
	default:
		t.tree.key(k)
	}
	return nil
}

func (t *kvTab) editField(m *model) tea.Cmd {
	n := t.tree.current()
	m.askAI(n.path(), n.text(), "the new value of field "+n.path()+" of configuration key "+t.treeFor+", same type as now", func(v string) tea.Cmd {
		if v == n.text() {
			return nil
		}
		n.set(v)
		t.tree.flatten()
		return t.saveTree(m, "set "+n.path())
	})
	return nil
}

// saveTree writes the edited value back to the key it came from.
func (t *kvTab) saveTree(m *model, what string) tea.Cmd {
	m.setStatus(what, false)
	return t.save(m, t.treeFor, t.tree.root.bytes())
}

// index reads every key and value of the store, one row per key and per field of a JSON value.
func (t *kvTab) index(m *model) tea.Cmd {
	a, gen, ctx, comp := m.app, m.gen, m.work(), t.comp
	return func() tea.Msg {
		kv, _, err := engine.Get[core.KV](a, core.KindKV, comp)
		if err != nil {
			return kvIndexMsg{gen: gen, err: err}
		}
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		keys, err := kv.List(c, "")
		if err != nil {
			return kvIndexMsg{gen: gen, err: err}
		}
		values := make([][]byte, len(keys))
		sem := make(chan struct{}, 16)
		var wg sync.WaitGroup
		for i, k := range keys {
			if strings.HasSuffix(k, "/") {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				values[i], _, _ = kv.Get(c, k)
			}()
		}
		wg.Wait()
		var hits []kvHit
		for i, k := range keys {
			if strings.HasSuffix(k, "/") {
				continue
			}
			hits = append(hits, kvHits(k, values[i])...)
		}
		return kvIndexMsg{gen: gen, hits: hits}
	}
}

// kvHits are the key's own row and, for a JSON value, a row per scalar field.
func kvHits(key string, value []byte) []kvHit {
	v := bytes.TrimSpace(value)
	root, err := parseJSON(v)
	if err != nil || !root.container() {
		return []kvHit{{key: key, value: oneLine(string(v))}}
	}
	hits := []kvHit{{key: key}}
	walkNodes(root, func(n *jnode) bool {
		if !n.container() {
			hits = append(hits, kvHit{key: key, path: n.path(), value: oneLine(n.text())})
		}
		return true
	})
	return hits
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// pickHit lists the hits in a picker that filters as you type, on key, field and value alike.
func (t *kvTab) pickHit(m *model, hits []kvHit) {
	items, desc := make([]string, len(hits)), make([]string, len(hits))
	for i, h := range hits {
		items[i] = h.key
		if h.path != "" {
			items[i] += "  " + strings.TrimPrefix(h.path, "$.")
		}
		desc[i] = h.value
	}
	m.pick("search "+t.comp+": type part of a key, field or value", items, desc, 0, false, func(c []string) tea.Cmd {
		if len(c) == 0 {
			return nil
		}
		for i, it := range items {
			if it == c[0] {
				return t.jump(m, hits[i])
			}
		}
		return nil
	})
}

// jump opens the hit's folder, selects its key and, for a field, that field in the value's tree.
func (t *kvTab) jump(m *model, h kvHit) tea.Cmd {
	dir := ""
	if i := strings.LastIndex(h.key, "/"); i >= 0 {
		dir = h.key[:i+1]
	}
	t.prefix, t.filter, t.list.sel = dir, "", 0
	t.jumpKey, t.jumpPath = h.key, h.path
	t.viaSearch = true
	return t.load(m)
}
