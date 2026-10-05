package tui

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// logTree reads any log line as a tree: a JSON object, a console or logfmt line ("3:58PM INF msg
// key=value ..."), or plain text. String values that hold JSON (quoted, escaped, in backticks, with a
// trailing newline) or a Go value printed with %v / %+v become trees of their own.
func logTree(line string) *jnode {
	s := strings.TrimSpace(ansi.Strip(line))
	if n, err := parseJSON([]byte(s)); err == nil && n.container() {
		expand(n)
		return n
	}
	root := &jnode{kind: 'o'}
	add := func(key, value string) {
		kid := decodeText(value)
		kid.key, kid.parent = key, root
		root.kids = append(root.kids, kid)
	}
	head, pairs := splitPairs(s)
	if len(pairs) == 0 {
		add("text", s)
		return root
	}
	if f := strings.Fields(head); len(f) >= 2 && looksLikeTime(f[0]) {
		add("time", f[0])
		add("level", f[1])
		head = strings.TrimSpace(strings.Join(f[2:], " "))
	}
	if head != "" {
		add("message", head)
	}
	for _, p := range pairs {
		add(p[0], p[1])
	}
	return root
}

func looksLikeTime(s string) bool {
	return strings.Contains(s, ":") && strings.IndexFunc(s, unicode.IsDigit) == 0
}

var pairKey = regexp.MustCompile(`(?:^|\s)([A-Za-z_][\w.\-]*)=`)

// splitPairs cuts a line into the text before its first key=value and the pairs; a value is a quoted
// Go string, a bracketed JSON or Go value, or a word.
func splitPairs(s string) (string, [][2]string) {
	loc := pairKey.FindStringSubmatchIndex(s)
	if loc == nil {
		return s, nil
	}
	head, rest := s[:loc[0]], s[loc[2]:]
	var pairs [][2]string
	for rest != "" {
		k, v, ok := strings.Cut(rest, "=")
		if !ok || !pairKey.MatchString(k+"=") {
			if len(pairs) > 0 {
				pairs[len(pairs)-1][1] += " " + rest
			}
			break
		}
		n := valueEnd(v)
		pairs = append(pairs, [2]string{k, v[:n]})
		rest = strings.TrimLeft(v[n:], " ")
	}
	return strings.TrimSpace(head), pairs
}

func valueEnd(v string) int {
	if v == "" {
		return 0
	}
	switch v[0] {
	case '"':
		if q, err := strconv.QuotedPrefix(v); err == nil {
			return len(q)
		}
	case '{', '[', '&':
		if n := balanced(v); n > 0 {
			return n
		}
	}
	if i := strings.IndexByte(v, ' '); i >= 0 {
		return i
	}
	return len(v)
}

// balanced is the length of the bracketed value v starts with, skipping JSON strings; 0 if it never closes.
func balanced(v string) int {
	depth, inStr := 0, false
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case inStr && c == '\\':
			i++
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			if depth--; depth == 0 {
				return i + 1
			}
		}
	}
	return 0
}

// expand turns the string values of n that hold structure into trees.
func expand(n *jnode) {
	if n.kind == 's' {
		if d := decodeText(n.text()); d.container() {
			n.kind, n.scalar, n.kids = d.kind, "", d.kids
			for _, k := range n.kids {
				k.parent = n
			}
		}
		return
	}
	for _, k := range n.kids {
		expand(k)
	}
}

// decodeText is s as a tree when it holds one, else as a string.
func decodeText(s string) *jnode {
	t := strings.TrimSpace(s)
	for {
		u := strings.TrimSpace(strings.Trim(t, "`'"))
		if len(u) >= 2 && u[0] == '"' {
			if q, err := strconv.Unquote(u); err == nil {
				u = strings.TrimSpace(q)
			}
		}
		if u == t {
			break
		}
		t = u
	}
	if t != "" && (t[0] == '{' || t[0] == '[') {
		for _, c := range []string{t, strings.ReplaceAll(t, `\"`, `"`)} {
			if n, err := parseJSON([]byte(c)); err == nil {
				expand(n)
				return n
			}
		}
	}
	if n := parseGo(t); n != nil {
		return n
	}
	if i := strings.IndexAny(t, "{["); i > 0 {
		if n, err := parseJSON([]byte(t[i:])); err == nil && n.container() {
			expand(n)
			root := &jnode{kind: 'o'}
			text := scalarNode(strings.TrimSpace(t[:i]))
			text.key, n.key = "text", "value"
			text.parent, n.parent = root, root
			root.kids = []*jnode{text, n}
			return root
		}
	}
	b, _ := json.Marshal(s)
	return &jnode{kind: 's', scalar: string(b)}
}

// parseGo reads a value printed by fmt: {a b}, &{Name:a Age:3}, [x y], map[k:v]; nil if s is not one.
func parseGo(s string) *jnode {
	if !strings.HasPrefix(s, "{") && !strings.HasPrefix(s, "&{") && !strings.HasPrefix(s, "[") && !strings.HasPrefix(s, "map[") {
		return nil
	}
	p := &goParser{s: s}
	n := p.value()
	if n == nil || !n.container() || p.i != len(s) {
		return nil
	}
	return n
}

type goParser struct {
	s string
	i int
}

var goField = regexp.MustCompile(`^[A-Za-z_]\w*:`)

func (p *goParser) value() *jnode {
	if strings.HasPrefix(p.s[p.i:], "&") {
		p.i++
	}
	rest := p.s[p.i:]
	switch {
	case strings.HasPrefix(rest, "map["):
		p.i += 4
		return p.items(']', true)
	case strings.HasPrefix(rest, "{"):
		p.i++
		return p.items('}', false)
	case strings.HasPrefix(rest, "["):
		p.i++
		return p.items(']', false)
	}
	start := p.i
	for p.i < len(p.s) && p.s[p.i] != ' ' && p.s[p.i] != '}' && p.s[p.i] != ']' {
		p.i++
	}
	return scalarNode(p.s[start:p.i])
}

// items reads up to end: an object when the items are named (Name:value, or any map key), else an array.
// An unnamed word after a named one belongs to it: %+v prints strings with spaces as they are.
func (p *goParser) items(end byte, isMap bool) *jnode {
	n := &jnode{kind: 'a'}
	for {
		for p.i < len(p.s) && p.s[p.i] == ' ' {
			p.i++
		}
		if p.i >= len(p.s) {
			return nil
		}
		if p.s[p.i] == end {
			p.i++
			return n
		}
		key, named := "", false
		if isMap {
			if j := strings.IndexByte(p.s[p.i:], ':'); j > 0 {
				key, named = p.s[p.i:p.i+j], true
				p.i += j + 1
			}
		} else if m := goField.FindString(p.s[p.i:]); m != "" && !strings.HasPrefix(p.s[p.i+len(m):], "//") {
			key, named = m[:len(m)-1], true
			p.i += len(m)
		}
		kid := p.value()
		if kid == nil {
			return nil
		}
		if named {
			n.kind = 'o'
		} else if n.kind == 'o' && len(n.kids) > 0 && !kid.container() && !n.kids[len(n.kids)-1].container() {
			last := n.kids[len(n.kids)-1]
			merged := scalarNode(last.text() + " " + kid.text())
			merged.key, merged.parent = last.key, n
			n.kids[len(n.kids)-1] = merged
			continue
		}
		kid.key, kid.parent = key, n
		n.kids = append(n.kids, kid)
	}
}

func scalarNode(s string) *jnode {
	switch s {
	case "true", "false":
		return &jnode{kind: 'b', scalar: s}
	case "<nil>", "null":
		return &jnode{kind: 'z', scalar: "null"}
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil && !strings.HasPrefix(s, "0x") && (len(s) < 2 || s[0] != '0' || s[1] == '.') {
		return &jnode{kind: 'n', scalar: s}
	}
	b, _ := json.Marshal(s)
	return &jnode{kind: 's', scalar: string(b)}
}
