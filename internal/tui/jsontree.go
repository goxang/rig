package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// jnode is a JSON value that keeps its object keys in their written order, so an edited value
// writes back with only the changed field different.
type jnode struct {
	key    string
	kind   byte // o object, a array, s string, n number, b bool, z null
	scalar string
	kids   []*jnode
	closed bool
	parent *jnode
}

func parseJSON(raw []byte) (*jnode, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	n, err := decodeNode(d)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data after the JSON value")
	}
	return n, nil
}

func decodeNode(d *json.Decoder) (*jnode, error) {
	tok, err := d.Token()
	if err != nil {
		return nil, err
	}
	n := &jnode{}
	switch v := tok.(type) {
	case json.Delim:
		n.kind = 'a'
		if v == '{' {
			n.kind = 'o'
		}
		for d.More() {
			key := ""
			if n.kind == 'o' {
				k, err := d.Token()
				if err != nil {
					return nil, err
				}
				key, _ = k.(string)
			}
			kid, err := decodeNode(d)
			if err != nil {
				return nil, err
			}
			kid.key, kid.parent = key, n
			n.kids = append(n.kids, kid)
		}
		if _, err := d.Token(); err != nil {
			return nil, err
		}
	case string:
		b, _ := json.Marshal(v)
		n.kind, n.scalar = 's', string(b)
	case json.Number:
		n.kind, n.scalar = 'n', v.String()
	case bool:
		n.kind, n.scalar = 'b', strconv.FormatBool(v)
	case nil:
		n.kind, n.scalar = 'z', "null"
	}
	return n, nil
}

func (n *jnode) container() bool { return n.kind == 'o' || n.kind == 'a' }

func (n *jnode) marshal(b *bytes.Buffer, indent string) {
	if !n.container() {
		b.WriteString(n.scalar)
		return
	}
	open, end := "[", "]"
	if n.kind == 'o' {
		open, end = "{", "}"
	}
	if len(n.kids) == 0 {
		b.WriteString(open + end)
		return
	}
	b.WriteString(open + "\n")
	for i, k := range n.kids {
		b.WriteString(indent + "  ")
		if n.kind == 'o' {
			kb, _ := json.Marshal(k.key)
			b.Write(kb)
			b.WriteString(": ")
		}
		k.marshal(b, indent+"  ")
		if i < len(n.kids)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString(indent + end)
}

func (n *jnode) bytes() []byte {
	var b bytes.Buffer
	n.marshal(&b, "")
	return b.Bytes()
}

// text is a scalar as a person reads it: strings without their quotes.
func (n *jnode) text() string {
	if n.kind == 's' {
		var s string
		_ = json.Unmarshal([]byte(n.scalar), &s)
		return s
	}
	if n.container() {
		return string(n.bytes())
	}
	return n.scalar
}

// set replaces n's value with input: a JSON value when it parses as one (keeping a string a
// string unless the input is quoted JSON), else the input as a string.
func (n *jnode) set(input string) {
	v, err := parseJSON([]byte(input))
	if err != nil || n.kind == 's' && v.kind != 's' && !v.container() {
		b, _ := json.Marshal(input)
		v = &jnode{kind: 's', scalar: string(b)}
	}
	n.kind, n.scalar, n.kids = v.kind, v.scalar, v.kids
	for _, k := range n.kids {
		k.parent = n
	}
}

func (n *jnode) path() string {
	if n.parent == nil {
		return "$"
	}
	if n.parent.kind == 'a' {
		for i, k := range n.parent.kids {
			if k == n {
				return n.parent.path() + fmt.Sprintf("[%d]", i)
			}
		}
	}
	return n.parent.path() + "." + n.key
}

func (n *jnode) remove() {
	p := n.parent
	if p == nil {
		return
	}
	for i, k := range p.kids {
		if k == n {
			p.kids = append(p.kids[:i], p.kids[i+1:]...)
			return
		}
	}
}

// jsonTree walks a JSON value field by field: fold containers, move between fields, and (when the
// screen allows) edit, add and delete them.
type jsonTree struct {
	zone     string
	root     *jnode
	sel, off int
	rows     []jrow
	shown    int
	// wrap folds long values over several lines; hoff is the sideways scroll when it is off
	wrap bool
	hoff int
	// lineRow is the row drawn on each screen line, from the last render
	lineRow []int
}

type jrow struct {
	n     *jnode
	depth int
}

func newJSONTree(zone string, raw []byte) (*jsonTree, error) {
	root, err := parseJSON(bytes.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	t := &jsonTree{zone: zone, root: root}
	t.flatten()
	return t, nil
}

func (t *jsonTree) flatten() {
	t.rows = t.rows[:0]
	var walk func(n *jnode, depth int)
	walk = func(n *jnode, depth int) {
		t.rows = append(t.rows, jrow{n, depth})
		if n.container() && !n.closed {
			for _, k := range n.kids {
				walk(k, depth+1)
			}
		}
	}
	walk(t.root, 0)
	t.sel = min(t.sel, len(t.rows)-1)
}

func (t *jsonTree) current() *jnode { return t.rows[t.sel].n }

// selectPath selects the node at path ($.a.b[0]), opening the containers above it.
func (t *jsonTree) selectPath(path string) {
	var target *jnode
	walkNodes(t.root, func(n *jnode) bool {
		if n.path() == path {
			target = n
		}
		return target == nil
	})
	if target == nil {
		return
	}
	for p := target.parent; p != nil; p = p.parent {
		p.closed = false
	}
	t.flatten()
	for i, r := range t.rows {
		if r.n == target {
			t.sel = i
		}
	}
}

// fold sets every container below depth d open and every deeper one closed.
func (t *jsonTree) fold(depth int) {
	var walk func(n *jnode, d int)
	walk = func(n *jnode, d int) {
		n.closed = n.container() && d >= depth
		for _, k := range n.kids {
			walk(k, d+1)
		}
	}
	walk(t.root, 0)
	t.flatten()
}

func (t *jsonTree) anyClosed() bool {
	for _, r := range t.rows {
		if r.n.closed {
			return true
		}
	}
	return false
}

// key moves and folds; it reports whether k was one of its keys.
func (t *jsonTree) key(k tea.KeyMsg) bool {
	if listKeys(k, &t.sel, len(t.rows)) {
		return true
	}
	n := t.current()
	switch k.String() {
	case "right", "l":
		if n.container() && n.closed {
			n.closed = false
		} else if n.container() && len(n.kids) > 0 {
			t.sel++
		}
	case "left", "h":
		if n.container() && !n.closed && n.parent != nil {
			n.closed = true
		} else if n.parent != nil {
			for i, r := range t.rows {
				if r.n == n.parent {
					t.sel = i
				}
			}
		}
	case " ", "tab":
		if n.container() {
			n.closed = !n.closed
		}
	case "+", "=":
		t.fold(1 << 20)
		return true
	case "-":
		t.fold(1)
		return true
	case "shift+right", "L":
		if !t.wrap {
			t.hoff += 8
		}
		return true
	case "shift+left", "H":
		t.hoff = max(0, t.hoff-8)
		return true
	case "w":
		t.wrap = !t.wrap
		return true
	case "z":
		if t.anyClosed() {
			t.fold(1 << 20)
		} else {
			t.fold(1)
		}
		return true
	default:
		return false
	}
	t.flatten()
	return true
}

func (t *jsonTree) click(h hit) bool {
	if h.id != t.zone {
		return false
	}
	if h.y >= 0 && h.y < len(t.lineRow) {
		i := t.lineRow[h.y]
		t.sel = i
		if n := t.current(); n.container() && (h.double || h.x <= t.rows[i].depth*2+1) {
			n.closed = !n.closed
			t.flatten()
		}
	}
	return true
}

func (t *jsonTree) wheel(up bool) {
	if up {
		t.sel = max(0, t.sel-3)
	} else {
		t.sel = min(len(t.rows)-1, t.sel+3)
	}
}

func (t *jsonTree) view(m *model, x, y, w, h int) string {
	t.shown = h
	m.zone(t.zone, x, y, w, h)
	if t.wrap {
		t.hoff = 0
	}
	t.off = scroll(t.sel, t.off, h, len(t.rows))
	draw := func(i int) []string {
		r := t.rows[i]
		n := r.n
		mark := "  "
		if n.container() {
			mark = "▾ "
			if n.closed {
				mark = "▸ "
			}
		}
		label := ""
		switch {
		case n.parent == nil:
			label = sDim.Render("$")
		case n.parent.kind == 'a':
			label = sDim.Render(fmt.Sprintf("[%d]", n.index()))
		default:
			label = sAccent.Render(n.key)
		}
		head := strings.Repeat("  ", r.depth) + sDim.Render(mark) + label + sDim.Render(": ")
		value := jvalue(n)
		var lines []string
		if hw := lipgloss.Width(head); t.wrap && hw < w-8 {
			for k, part := range strings.Split(ansi.Hardwrap(value, w-hw, true), "\n") {
				if k == 0 {
					lines = append(lines, head+part)
				} else {
					lines = append(lines, strings.Repeat(" ", hw)+part)
				}
			}
		} else {
			line := head + value
			if t.hoff > 0 {
				line = ansi.TruncateLeft(line, t.hoff, "")
			}
			lines = []string{truncate(line, w)}
		}
		if i == t.sel {
			for k := range lines {
				lines[k] = highlight(sSelected, lines[k], w)
			}
		}
		return lines
	}
	if t.wrap {
		// the selected row must fit whole below the top one
		for used := 0; t.off < t.sel; t.off++ {
			used = 0
			for i := t.off; i <= t.sel; i++ {
				used += len(draw(i))
			}
			if used <= h {
				break
			}
		}
	}
	t.lineRow = t.lineRow[:0]
	var out []string
	for i := t.off; i < len(t.rows) && len(out) < h; i++ {
		for _, l := range draw(i) {
			if len(out) < h {
				out = append(out, l)
				t.lineRow = append(t.lineRow, i)
			}
		}
	}
	return strings.Join(out, "\n")
}

func (n *jnode) index() int {
	for i, k := range n.parent.kids {
		if k == n {
			return i
		}
	}
	return 0
}

func jvalue(n *jnode) string {
	switch n.kind {
	case 'o':
		return sDim.Render(fmt.Sprintf("{%d}", len(n.kids)))
	case 'a':
		return sDim.Render(fmt.Sprintf("[%d]", len(n.kids)))
	case 's':
		return sGreen.Render(`"` + strings.NewReplacer("\n", "⏎", "\t", " ").Replace(n.text()) + `"`)
	case 'n':
		return lipgloss.NewStyle().Foreground(cAmber).Render(n.scalar)
	case 'b':
		return sAccent.Render(n.scalar)
	}
	return sDim.Render(n.scalar)
}
