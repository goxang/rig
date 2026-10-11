package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var docSep = regexp.MustCompile(`(?m)^---[ \t]*(#.*)?$`)

// YAML is the object as its own document, as the file has it (comments kept).
func (o *Object) YAML() ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(o.Node); err != nil {
		return nil, err
	}
	return b.Bytes(), enc.Close()
}

// ReplaceDoc writes doc as the o.Doc-th document of o.File and leaves every other document's text
// as it was. It reads the file back and restores it when the result no longer parses to the same
// documents with o's kind and name in place, so a bad edit never leaves a broken file.
func ReplaceDoc(o *Object, doc []byte) error {
	raw, err := os.ReadFile(o.File)
	if err != nil {
		return err
	}
	var head struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(doc, &head); err != nil {
		return fmt.Errorf("the edit is not YAML: %w", err)
	}
	if head.Kind != o.Kind || head.Metadata.Name != o.Name {
		return fmt.Errorf("the edit is %s/%s, not %s: rename objects in the file itself", head.Kind, head.Metadata.Name, o.ID())
	}
	locs := docSep.FindAllIndex(raw, -1)
	// chunk i spans from the end of separator i-1 to the start of separator i
	starts, ends := []int{0}, []int{}
	for _, l := range locs {
		ends = append(ends, l[0])
		starts = append(starts, l[1])
	}
	ends = append(ends, len(raw))
	// a file that opens with --- has no document before it
	if len(locs) > 0 && len(bytes.TrimSpace(raw[:locs[0][0]])) == 0 {
		starts, ends = starts[1:], ends[1:]
	}
	if o.Doc >= len(starts) {
		return fmt.Errorf("%s: document %d not found", o.File, o.Doc)
	}
	body := bytes.TrimRight(doc, "\n")
	out := make([]byte, 0, len(raw)+len(doc))
	out = append(out, raw[:starts[o.Doc]]...)
	if o.Doc > 0 || starts[o.Doc] > 0 {
		out = append(out, '\n')
	}
	out = append(out, body...)
	out = append(out, '\n')
	out = append(out, raw[ends[o.Doc]:]...)
	before, err := Scan(o.File)
	if err != nil {
		return err
	}
	if err := os.WriteFile(o.File, out, 0o644); err != nil {
		return err
	}
	after, err := Scan(o.File)
	if err == nil && sameDocs(before, after, o) {
		return nil
	}
	_ = os.WriteFile(o.File, raw, 0o644)
	return fmt.Errorf("%s: the edited file did not read back as the same documents; left unchanged", o.File)
}

// sameDocs checks that a write kept the file's objects and o where it was.
func sameDocs(before, after *Set, o *Object) bool {
	if after == nil || len(after.Objects) != len(before.Objects) {
		return false
	}
	for _, x := range after.Objects {
		if x.Doc == o.Doc && x.Kind == o.Kind && x.Name == o.Name {
			return true
		}
	}
	return false
}

// FromLive turns an object read from the cluster (`kubectl get -o json`) into what its manifest
// should say: only the fields someone set (its managedFields, not the server's defaults or status),
// none of the server's bookkeeping, keys in the file's order, and the file's $VAR templates kept
// where the file has them. drop names env vars to leave out unless the file already has them
// (what a deploy injects from rig.yaml).
func FromLive(file *Object, live []byte, drop map[string]bool) ([]byte, error) {
	var meta struct {
		Metadata struct {
			ManagedFields []struct {
				Manager     string         `json:"manager"`
				Subresource string         `json:"subresource"`
				FieldsV1    map[string]any `json:"fieldsV1"`
			} `json:"managedFields"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(live, &meta); err != nil {
		return nil, err
	}
	owned := map[string]any{}
	for _, mf := range meta.Metadata.ManagedFields {
		if mf.Subresource == "status" {
			continue
		}
		mergeFields(owned, mf.FieldsV1)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(live, &doc); err != nil || len(doc.Content) == 0 {
		return nil, fmt.Errorf("live object: %v", err)
	}
	n := doc.Content[0]
	plain(n)
	if len(owned) > 0 { // without managed fields everything stays, defaults too
		keep := map[string]any{"f:apiVersion": map[string]any{}, "f:kind": map[string]any{},
			"f:metadata": map[string]any{"f:name": map[string]any{}}}
		mergeFields(keep, owned)
		n = ownedOnly(n, keep)
	}
	if md := mapGet(n, "metadata"); md != nil {
		mapDel(md, "managedFields", "uid", "resourceVersion", "generation", "creationTimestamp", "selfLink")
		for _, k := range []string{"annotations", "labels"} {
			if a := mapGet(md, k); a != nil {
				mapDel(a, "kubectl.kubernetes.io/last-applied-configuration", "deployment.kubernetes.io/revision")
				if len(a.Content) == 0 {
					mapDel(md, k)
				}
			}
		}
	}
	mapDel(n, "status")
	if file != nil {
		dropInjected(n, file.Node, drop)
		templates(n, file.Node)
		order(n, file.Node)
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	return b.Bytes(), enc.Close()
}

func mergeFields(dst, src map[string]any) {
	for k, v := range src {
		sub, _ := v.(map[string]any)
		cur, ok := dst[k].(map[string]any)
		if !ok {
			cur = map[string]any{}
			dst[k] = cur
		}
		mergeFields(cur, sub)
	}
}

// ownedOnly keeps the parts of n that fields (a managedFields fieldsV1 tree) names; an empty set
// below a key owns that whole value.
func ownedOnly(n *yaml.Node, fields map[string]any) *yaml.Node {
	if len(fields) == 0 || len(fields) == 1 && fields["."] != nil {
		return n
	}
	switch n.Kind {
	case yaml.MappingNode:
		out := &yaml.Node{Kind: yaml.MappingNode, Tag: n.Tag}
		for i := 0; i+1 < len(n.Content); i += 2 {
			sub, ok := fields["f:"+n.Content[i].Value].(map[string]any)
			if !ok {
				continue
			}
			out.Content = append(out.Content, n.Content[i], ownedOnly(n.Content[i+1], sub))
		}
		return out
	case yaml.SequenceNode:
		out := &yaml.Node{Kind: yaml.SequenceNode, Tag: n.Tag}
		for i, item := range n.Content {
			if sub, ok := itemFields(item, i, fields); ok {
				out.Content = append(out.Content, ownedOnly(item, sub))
			}
		}
		return out
	}
	return n
}

// itemFields finds the fieldsV1 entry of a list item: by key (k:{"name":"x"}), value (v:...) or index (i:N).
func itemFields(item *yaml.Node, i int, fields map[string]any) (map[string]any, bool) {
	var v any
	_ = item.Decode(&v)
	for k, sub := range fields {
		s, _ := sub.(map[string]any)
		switch {
		case strings.HasPrefix(k, "k:"):
			var key map[string]any
			if json.Unmarshal([]byte(k[2:]), &key) != nil {
				continue
			}
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			match := true
			for kk, kv := range key {
				if fmt.Sprint(m[kk]) != fmt.Sprint(kv) {
					match = false
				}
			}
			if match {
				return s, true
			}
		case strings.HasPrefix(k, "v:"):
			var want any
			if json.Unmarshal([]byte(k[2:]), &want) == nil && fmt.Sprint(want) == fmt.Sprint(v) {
				return s, true
			}
		case k == fmt.Sprintf("i:%d", i):
			return s, true
		}
	}
	return nil, false
}

// plain drops the flow and quoting styles JSON input comes with.
func plain(n *yaml.Node) {
	n.Style = 0
	if n.Kind == yaml.ScalarNode && n.Tag == "!!str" && needsQuote(n.Value) {
		n.Style = yaml.DoubleQuotedStyle
	}
	for _, c := range n.Content {
		plain(c)
	}
}

// needsQuote keeps strings that would read back as another type quoted ("80", "true", "").
func needsQuote(s string) bool {
	var v any
	if s == "" || yaml.Unmarshal([]byte(s), &v) != nil {
		return true
	}
	_, isStr := v.(string)
	return !isStr
}

func mapGet(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func mapDel(n *yaml.Node, keys ...string) {
	if n == nil || n.Kind != yaml.MappingNode {
		return
	}
	del := map[string]bool{}
	for _, k := range keys {
		del[k] = true
	}
	var c []*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		if !del[n.Content[i].Value] {
			c = append(c, n.Content[i], n.Content[i+1])
		}
	}
	n.Content = c
}

// templates puts back the file's scalars that hold $VAR references, at the paths both share: the
// live object has their values for one environment only.
func templates(live, file *yaml.Node) {
	if live == nil || file == nil || live.Kind != file.Kind {
		return
	}
	switch file.Kind {
	case yaml.ScalarNode:
		if varRef.MatchString(file.Value) {
			live.Value, live.Tag, live.Style = file.Value, file.Tag, file.Style
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(file.Content); i += 2 {
			templates(mapGet(live, file.Content[i].Value), file.Content[i+1])
		}
	case yaml.SequenceNode:
		for i, f := range file.Content {
			if l := matchItem(live, f, i); l != nil {
				templates(l, f)
			}
		}
	}
}

// matchItem is the live list item standing for the file's item: same name (containers, env, ports),
// else the same position.
func matchItem(live, item *yaml.Node, i int) *yaml.Node {
	if name := mapGet(item, "name"); name != nil {
		for _, l := range live.Content {
			if n := mapGet(l, "name"); n != nil && n.Value == name.Value {
				return l
			}
		}
		return nil
	}
	if i < len(live.Content) {
		return live.Content[i]
	}
	return nil
}

// order sorts each mapping's keys as the file has them; keys the file lacks follow, as the server sent them.
func order(live, file *yaml.Node) {
	if live == nil || file == nil || live.Kind != file.Kind {
		return
	}
	switch live.Kind {
	case yaml.MappingNode:
		var c []*yaml.Node
		used := map[string]bool{}
		for i := 0; i+1 < len(file.Content); i += 2 {
			k := file.Content[i].Value
			for j := 0; j+1 < len(live.Content); j += 2 {
				if live.Content[j].Value == k {
					c = append(c, live.Content[j], live.Content[j+1])
					used[k] = true
					order(live.Content[j+1], file.Content[i+1])
				}
			}
		}
		for j := 0; j+1 < len(live.Content); j += 2 {
			if !used[live.Content[j].Value] {
				c = append(c, live.Content[j], live.Content[j+1])
			}
		}
		live.Content = c
	case yaml.SequenceNode:
		for i, f := range file.Content {
			if l := matchItem(live, f, i); l != nil {
				order(l, f)
			}
		}
	}
}

// dropInjected removes env entries named in drop from every list called env that the file's same
// list does not have, and the managed-by label a deploy adds.
func dropInjected(live, file *yaml.Node, drop map[string]bool) {
	var walk func(l, f *yaml.Node, key string)
	walk = func(l, f *yaml.Node, key string) {
		if l == nil {
			return
		}
		switch l.Kind {
		case yaml.MappingNode:
			if key == "labels" && (f == nil || mapGet(f, "app.kubernetes.io/managed-by") == nil) {
				mapDel(l, "app.kubernetes.io/managed-by")
			}
			for i := 0; i+1 < len(l.Content); i += 2 {
				k := l.Content[i].Value
				walk(l.Content[i+1], mapGet(f, k), k)
			}
		case yaml.SequenceNode:
			var keep []*yaml.Node
			for i, item := range l.Content {
				var fi *yaml.Node
				if f != nil {
					fi = matchItem(f, item, i)
				}
				if key == "env" && fi == nil {
					if n := mapGet(item, "name"); n != nil && drop[n.Value] {
						continue
					}
				}
				walk(item, fi, "")
				keep = append(keep, item)
			}
			l.Content = keep
		}
	}
	walk(live, file, "")
}

// PatchScalar writes n, a scalar of o that was edited in memory (it keeps its Line and Column), over
// the scalar's text in o.File and leaves every other byte alone. ok is false when the scalar is not
// one it can find on its line (block or multi-line scalars); the caller re-encodes the document then.
func PatchScalar(o *Object, n *yaml.Node) (ok bool, err error) {
	if n.Kind != yaml.ScalarNode || n.Line == 0 || n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return false, nil
	}
	raw, err := os.ReadFile(o.File)
	if err != nil {
		return false, err
	}
	lines := bytes.SplitAfter(raw, []byte("\n"))
	if n.Line > len(lines) {
		return false, nil
	}
	line := lines[n.Line-1]
	runes := []rune(string(line))
	if n.Column-1 > len(runes) {
		return false, nil
	}
	start := len(string(runes[:n.Column-1]))
	flow := bytes.Count(line[:start], []byte("{"))+bytes.Count(line[:start], []byte("[")) >
		bytes.Count(line[:start], []byte("}"))+bytes.Count(line[:start], []byte("]"))
	end := scalarEnd(line, start, flow)
	if end < 0 {
		return false, nil
	}
	v := *n
	v.HeadComment, v.LineComment, v.FootComment = "", "", ""
	text, err := yaml.Marshal(&v)
	if err != nil {
		return false, err
	}
	if v.Style == 0 && flow && bytes.ContainsAny(text, ",[]{}") {
		v.Style, n.Style = yaml.DoubleQuotedStyle, yaml.DoubleQuotedStyle
		text, _ = yaml.Marshal(&v)
	}
	text = bytes.TrimSuffix(text, []byte("\n"))
	if bytes.Contains(text, []byte("\n")) {
		return false, nil
	}
	want, err := o.YAML()
	if err != nil {
		return false, err
	}
	var out []byte
	for i, l := range lines {
		if i == n.Line-1 {
			l = append(append(append([]byte{}, line[:start]...), text...), line[end:]...)
		}
		out = append(out, l...)
	}
	if err := os.WriteFile(o.File, out, 0o644); err != nil {
		return false, err
	}
	// the patched file must read back as exactly the edited object, else the edit falls back
	if after, err := Scan(o.File); err == nil {
		for _, x := range after.Objects {
			if x.Doc == o.Doc && x.ID() == o.ID() {
				if got, err := x.YAML(); err == nil && bytes.Equal(got, want) {
					return true, nil
				}
			}
		}
	}
	return false, os.WriteFile(o.File, raw, 0o644)
}

// scalarEnd is where the one-line scalar starting at line[start] ends, -1 when it does not end there.
func scalarEnd(line []byte, start int, flow bool) int {
	s := line[start:]
	if len(s) == 0 {
		return -1
	}
	switch s[0] {
	case '"':
		for i := 1; i < len(s); i++ {
			switch s[i] {
			case '\\':
				i++
			case '"':
				return start + i + 1
			}
		}
		return -1
	case '\'':
		for i := 1; i < len(s); i++ {
			if s[i] == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				return start + i + 1
			}
		}
		return -1
	case '|', '>':
		return -1
	}
	end := len(bytes.TrimRight(s, "\r\n"))
	for i := 0; i < end; i++ {
		if flow && bytes.IndexByte([]byte(",]}"), s[i]) >= 0 || s[i] == '#' && i > 0 && (s[i-1] == ' ' || s[i-1] == '\t') {
			end = i
			break
		}
	}
	return start + len(bytes.TrimRight(s[:end], " \t"))
}
