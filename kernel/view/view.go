// Package view turns view specs into renderer-neutral widget trees, fetches the frames widgets
// bind to and transforms them. The TUI draws the trees; a web renderer could draw the same ones.
package view

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/protocol"
)

// View is one entry under views: in rig.yaml or a plugin manifest.
type View struct {
	Title string
	// Slot is tab, command, subtab:<view>, service.section, detail:<scheme>, panel or home.
	Slot string
	// Command is the palette command that opens a view of slot command: slot: { command: usage }
	Command string
	Key     string
	// File holds the layout instead, relative to the file that declares the view.
	File string
	// Modes are the UI modes that show the view (software, product); none shows it in every mode.
	Modes []string
	Root  protocol.Node
}

// Layouts are the widgets that hold other widgets.
var Layouts = map[string]bool{"grid": true, "split": true, "tabs": true, "stack": true, "box": true, "scroll": true}

// bindKeys move from a node's props into its binding.
var bindKeys = map[string]bool{"source": true, "args": true, "query": true, "refresh": true, "every": true}

func (v *View) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: a view is a mapping", n.Line)
	}
	body := &yaml.Node{Kind: yaml.MappingNode}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, val := n.Content[i].Value, n.Content[i+1]
		switch k {
		case "title":
			v.Title = val.Value
		case "key":
			v.Key = val.Value
		case "file":
			v.File = val.Value
		case "modes":
			if err := val.Decode(&v.Modes); err != nil {
				return err
			}
		case "slot":
			if err := v.slot(val); err != nil {
				return err
			}
		default:
			body.Content = append(body.Content, n.Content[i], val)
		}
	}
	if v.Slot == "" {
		v.Slot = "tab"
	}
	if len(body.Content) == 0 {
		// no layout and no file: the plugin that declares it renders it
		return nil
	}
	root, err := Parse(body)
	v.Root = root
	return err
}

// slot reads slot: tab, or slot: { tab: { key: G } }.
func (v *View) slot(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		v.Slot = n.Value
		return nil
	}
	if n.Kind != yaml.MappingNode || len(n.Content) != 2 {
		return fmt.Errorf("line %d: slot is a name or { <name>: { key: K } }", n.Line)
	}
	v.Slot = n.Content[0].Value
	if v.Slot == "command" {
		v.Command = n.Content[1].Value
		return nil
	}
	var o struct{ Key string }
	if err := n.Content[1].Decode(&o); err != nil {
		return err
	}
	if o.Key != "" {
		v.Key = o.Key
	}
	return nil
}

// Load reads a view's file: a node, or a whole view whose layout replaces v's.
func (v *View) Load(raw []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 {
		return fmt.Errorf("empty view file")
	}
	var f View
	if err := doc.Content[0].Decode(&f); err != nil {
		return err
	}
	v.Root = f.Root
	return nil
}

// Parse reads a widget node: { widget: table, source: db, query: ..., ...props }, or a layout
// shorthand such as { grid: [[a, b], [c]] }, { tabs: { Name: node } } or { box: node, title: T }.
func Parse(n *yaml.Node) (protocol.Node, error) {
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n.Kind != yaml.MappingNode {
		return protocol.Node{}, fmt.Errorf("line %d: a widget is a mapping", n.Line)
	}
	out := protocol.Node{Props: protocol.Args{}}
	var children *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, val := n.Content[i].Value, n.Content[i+1]
		switch {
		case k == "widget":
			out.Widget = val.Value
		case k == "id":
			out.ID = val.Value
		case k == "layout":
			return Parse(val)
		case k == "children":
			children = val
		case k == "fallback":
			fb, err := Parse(val)
			if err != nil {
				return out, err
			}
			out.Fallback = &fb
		case Layouts[k] && out.Widget == "":
			out.Widget, children = k, val
		case bindKeys[k]:
			if out.Bind == nil {
				out.Bind = &protocol.Binding{}
			}
			if err := bind(out.Bind, k, val); err != nil {
				return out, err
			}
		default:
			var v any
			if err := val.Decode(&v); err != nil {
				return out, err
			}
			out.Props[k] = v
		}
	}
	if out.Widget == "" {
		return out, fmt.Errorf("line %d: no widget: (or grid, split, tabs, stack, box, scroll)", n.Line)
	}
	if children != nil {
		kids, err := parseChildren(out.Widget, children)
		if err != nil {
			return out, err
		}
		out.Children = kids
	}
	if len(out.Props) == 0 {
		out.Props = nil
	}
	return out, nil
}

func bind(b *protocol.Binding, k string, val *yaml.Node) error {
	switch k {
	case "source":
		b.Source = val.Value
	case "refresh", "every":
		b.Every = val.Value
	case "query":
		if b.Args == nil {
			b.Args = protocol.Args{}
		}
		b.Args["query"] = val.Value
	case "args":
		var a protocol.Args
		if err := val.Decode(&a); err != nil {
			return err
		}
		if b.Args == nil {
			b.Args = protocol.Args{}
		}
		for k, v := range a {
			b.Args[k] = v
		}
	}
	return nil
}

func parseChildren(widget string, n *yaml.Node) ([]protocol.Node, error) {
	switch {
	case widget == "tabs" && n.Kind == yaml.MappingNode:
		var out []protocol.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			c, err := Parse(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			if c.Props == nil {
				c.Props = protocol.Args{}
			}
			c.Props["title"] = n.Content[i].Value
			out = append(out, c)
		}
		return out, nil
	case n.Kind == yaml.MappingNode:
		c, err := Parse(n)
		return []protocol.Node{c}, err
	case n.Kind != yaml.SequenceNode:
		return nil, fmt.Errorf("line %d: %s holds a list of widgets", n.Line, widget)
	}
	var out []protocol.Node
	for _, item := range n.Content {
		if widget == "grid" && item.Kind == yaml.SequenceNode {
			row, err := parseChildren("row", item)
			if err != nil {
				return nil, err
			}
			out = append(out, protocol.Node{Widget: "row", Children: row})
			continue
		}
		c, err := Parse(item)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// Walk calls fn on n and every node under it, depth first.
func Walk(n *protocol.Node, fn func(*protocol.Node)) {
	fn(n)
	for i := range n.Children {
		Walk(&n.Children[i], fn)
	}
}

// Kind is a node's widget without its version: table@1 is table.
// Widgets are the widgets rig renders and the highest version of each; plugins learn them at initialize.
var Widgets = map[string]int{
	"table": 1, "list": 1, "kv": 1, "tree": 1, "form": 1, "stat": 1, "gauge": 1, "progress": 1, "bar": 1, "bars": 1,
	"sparkline": 1, "chart": 1, "heatmap": 1, "text": 1, "markdown": 1, "code": 1, "diff": 1, "logs": 1,
	"waterfall": 1, "graph": 1, "timeline": 1, "board": 1, "terminal": 1, "canvas": 1, "calendar": 1,
}

// Supported reports whether rig renders n's widget at the version it asks for (calendar@2).
func Supported(n protocol.Node) bool {
	k, v, versioned := strings.Cut(n.Widget, "@")
	have, ok := Widgets[k]
	want, err := strconv.Atoi(v)
	return ok && (!versioned || err == nil && want <= have)
}

func Kind(n protocol.Node) string {
	k, _, _ := strings.Cut(n.Widget, "@")
	return k
}

// Prop is a string prop, or "".
func Prop(n protocol.Node, k string) string {
	if v, ok := n.Props[k]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}
