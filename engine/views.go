package engine

import (
	"cmp"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/contracts"
	"github.com/goxang/rig/host"
	"github.com/goxang/rig/kernel/view"
	"github.com/goxang/rig/protocol"
)

// ViewDef is a view a UI places: from rig.yaml, a plugin's manifest or a contract a plugin serves.
type ViewDef struct {
	// Key is view:<name>, view:<plugin>.<name>, view:<plugin>/<contract> or view:Home
	Key   string
	Title string
	View  *view.View
	// Dir is where the view's file: and its widgets' file: props resolve
	Dir string
	// Remote is <plugin>.<view> for a view its plugin renders
	Remote  string
	Plugin  string
	Secrets []string
	// Rank is 10-13 for the dev loop's contracts, 20 for the project's views, 30 for a user's own
	Rank int
}

// Views are the project's tab and command views in screen order: Home first when any view is a home
// tile, then the dev loop's contracts (changes, merge requests, issues, docs), the project's own, and
// what a user installed for themselves.
func Views(a *App) []ViewDef {
	var out []ViewDef
	var home []protocol.Node
	rank, ranks := 20, map[string]int{}
	served := map[int]string{}
	var owner *host.Manifest
	add := func(key, title string, v *view.View, dir, remote string) {
		if v.Slot == "home" {
			tile := v.Root
			tile.Props = maps.Clone(tile.Props)
			if tile.Props == nil {
				tile.Props = protocol.Args{}
			}
			tile.Props["title"] = cmp.Or(view.Prop(tile, "title"), v.Title, title)
			home = append(home, tile)
			return
		}
		if v.Slot != "tab" && (v.Slot != "command" || v.Command == "") {
			return
		}
		d := ViewDef{Key: key, Title: cmp.Or(v.Title, title), View: v, Dir: dir, Remote: remote, Rank: rank}
		if owner != nil {
			d.Plugin, d.Secrets = owner.Name, owner.Permissions.Secrets
		}
		if plugin, _, ok := strings.Cut(strings.TrimPrefix(key, "view:"), "/"); ok {
			served[len(out)] = plugin
		}
		ranks[key] = rank
		out = append(out, d)
	}
	for _, n := range a.Spec.ViewOrder {
		if v := a.Spec.Views[n]; v != nil {
			add("view:"+n, n, v, a.Spec.Dir, "")
		}
	}
	ms, _ := host.Discover(a.Spec.Dir, a.Spec.Plugins)
	for _, m := range ms {
		if !m.Relevant(a.Spec.Dir, a.Spec.Plugins) {
			continue
		}
		showTabs := m.ShowsTab(a.Spec.Dir, a.Spec.Plugins)
		owner = m
		rank = viewRank(m, a.Spec.Dir)
		names := make([]string, 0, len(m.Views))
		for n := range m.Views {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			node := m.Views[n]
			var v view.View
			if err := a.Spec.Expanded(&node).Decode(&v); err != nil {
				v = view.View{Slot: "tab", Root: protocol.Node{Widget: "text", Props: protocol.Args{"text": err.Error()}}}
			}
			remote := ""
			if v.Root.Widget == "" && v.File == "" {
				remote = m.Name + "." + n
			}
			if !showTabs && v.Slot == "tab" {
				continue
			}
			add("view:"+m.Name+"."+n, n, &v, m.Dir, remote)
		}
		// a plugin with its own views has laid out what the contract views would show
		for _, id := range m.Implements {
			if len(names) > 0 || !showTabs {
				break
			}
			if c := contracts.Get(id); c != nil && c.View != nil {
				var v view.View
				if err := yaml.Unmarshal(c.View, &v); err != nil {
					panic(err)
				}
				Served(&v.Root, m.Name)
				add("view:"+m.Name+"/"+c.ID, v.Title, &v, m.Dir, "")
			}
		}
	}
	// two plugins serving one contract (docs from files and from Confluence) are named after the plugins
	titles := map[string]int{}
	for i := range served {
		titles[out[i].Title]++
	}
	for i, plugin := range served {
		if titles[out[i].Title] > 1 {
			out[i].Title = strings.ToUpper(plugin[:1]) + strings.ReplaceAll(plugin[1:], "-", " ")
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return ranks[out[i].Key] < ranks[out[j].Key] })
	if len(home) > 0 {
		root := protocol.Node{Widget: "grid"}
		for i := 0; i < len(home); i += 2 {
			root.Children = append(root.Children, protocol.Node{Widget: "row", Children: home[i:min(i+2, len(home))]})
		}
		out = append([]ViewDef{{Key: "view:Home", Title: "Home", View: &view.View{Title: "Home", Slot: "tab", Root: root}, Dir: a.Spec.Dir}}, out...)
	}
	return out
}

// viewRank orders the views: the dev loop's contracts (changes, merge requests, issues, docs), then
// the project's own, then what a user installed for themselves (games), git-ignored ones included.
func viewRank(m *host.Manifest, dir string) int {
	for i, c := range []string{"vcs", "scm", "tracker", "docs"} {
		for _, id := range m.Implements {
			if strings.HasPrefix(id, c+"/") {
				return 10 + i
			}
		}
	}
	rel, err := filepath.Rel(dir, m.Dir)
	if err != nil || strings.HasPrefix(rel, "..") || exec.Command("git", "-C", dir, "check-ignore", "-q", rel).Run() == nil {
		return 30
	}
	return 20
}

// Served points a contract's standard view at the plugin serving it: scm.mrs becomes <plugin>.scm.mrs.
func Served(root *protocol.Node, plugin string) {
	view.Walk(root, func(n *protocol.Node) {
		if n.Bind != nil && n.Bind.Source != "" {
			n.Bind.Source = plugin + "." + n.Bind.Source
		}
		acts, _ := n.Props["actions"].([]any)
		for _, a := range acts {
			if am, ok := a.(map[string]any); ok && am["action"] != nil {
				am["action"] = plugin + "." + fmt.Sprint(am["action"])
			}
		}
	})
}
