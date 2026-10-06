package tui

import (
	"regexp"
	"strings"
)

// logQuery filters log lines by their fields: `output.Transaction.ID=202604 level!=debug msg~timeout`.
// Terms are ANDed; = and != compare a value's text, ~ looks for text in it (any case). A path may
// start anywhere in the line's tree, and an array in it matches when any element does. Quote a value
// with spaces: msg="no route".
type logQuery []logCond

type logCond struct {
	path     []string
	op, want string
}

var logTerm = regexp.MustCompile(`^\$?\.?([A-Za-z_@][\w@\-]*(?:(?:\.[\w@\-]+)|\[\d+\])*)(!=|=|~)("[^"]*"|\S*)$`)

// parseLogQuery reads s as a field query; ok is false when s is not one (then it is a grep).
func parseLogQuery(s string) (logQuery, bool) {
	terms := splitTerms(strings.TrimSpace(s))
	if len(terms) == 0 {
		return nil, false
	}
	var q logQuery
	for _, t := range terms {
		m := logTerm.FindStringSubmatch(t)
		if m == nil {
			return nil, false
		}
		path := strings.Split(strings.NewReplacer("[", ".[", "]", "").Replace(m[1]), ".")
		q = append(q, logCond{path: path, op: m[2], want: strings.Trim(m[3], `"`)})
	}
	return q, true
}

// splitTerms cuts at spaces outside double quotes.
func splitTerms(s string) []string {
	var out []string
	var b strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			b.WriteRune(r)
		case r == ' ' && !quoted:
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

func (q logQuery) match(root *jnode) bool {
	for _, c := range q {
		found := false
		walkNodes(root, func(n *jnode) bool {
			for _, v := range resolve(n, c.path) {
				if c.test(v) {
					found = true
					return false
				}
			}
			return true
		})
		// != keeps a line that lacks the field
		if found == (c.op == "!=") {
			return false
		}
	}
	return true
}

// test is c's comparison on one value; != is tested as = and inverted by match.
func (c logCond) test(n *jnode) bool {
	got := n.text()
	if c.op == "~" {
		return strings.Contains(strings.ToLower(got), strings.ToLower(c.want))
	}
	return got == c.want || strings.EqualFold(got, c.want) && n.kind == 's'
}

// resolve follows path down from n; a key on an array looks into every element.
func resolve(n *jnode, path []string) []*jnode {
	if len(path) == 0 {
		return []*jnode{n}
	}
	seg := path[0]
	var out []*jnode
	if strings.HasPrefix(seg, "[") {
		i := 0
		for _, c := range seg[1:] {
			i = i*10 + int(c-'0')
		}
		if n.kind == 'a' && i < len(n.kids) {
			out = append(out, resolve(n.kids[i], path[1:])...)
		}
		return out
	}
	for _, k := range n.kids {
		switch {
		case n.kind == 'o' && k.key == seg:
			out = append(out, resolve(k, path[1:])...)
		case n.kind == 'a':
			out = append(out, resolve(k, path)...)
		}
	}
	return out
}

// walkNodes visits n and everything under it until visit returns false.
func walkNodes(n *jnode, visit func(*jnode) bool) bool {
	if !visit(n) {
		return false
	}
	for _, k := range n.kids {
		if !walkNodes(k, visit) {
			return false
		}
	}
	return true
}

// highlightRe matches the values an equality or substring term looks for, so a rendered line can
// show which part made it match; != has no positive text to highlight.
func (q logQuery) highlightRe() *regexp.Regexp {
	var alts []string
	for _, c := range q {
		if c.op != "!=" && c.want != "" {
			alts = append(alts, regexp.QuoteMeta(c.want))
		}
	}
	if len(alts) == 0 {
		return nil
	}
	return regexp.MustCompile("(?i)" + strings.Join(alts, "|"))
}

// queryFor is the term that keeps lines whose field n has n's value.
func queryFor(n *jnode) string {
	path := strings.TrimPrefix(n.path(), "$.")
	v := n.text()
	if n.container() {
		return path + "~"
	}
	if strings.ContainsAny(v, " \t") {
		v = `"` + v + `"`
	}
	return path + "=" + v
}
